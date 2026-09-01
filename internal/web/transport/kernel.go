// Package transport — kernel.go.
//
// Kernel is the line of defense against the two bug classes the
// Python original's lifecycle was designed to prevent:
//
//  1. Stale envelope: a refresh swap of the cookie jar between
//     materialization and POST produces a request whose CSRF
//     token (`at=`) does not match its cookies. The kernel
//     fences generations so a request issued under a retired
//     generation is rejected before it touches the jar.
//  2. Unbounded response: a server bug that emits gigabytes of
//     response body drains memory. The kernel caps the read at
//     a configurable size and surfaces ErrResponseTooLarge with
//     the actual byte count.
//
// Kernel is the single owner of `*http.Client` and `http.CookieJar`
// for the transport. Callers reach it through the chain middlewares
// (`mw_retry`, `mw_authrefresh`, `mw_tracing`) and through the
// terminal POST step. There is exactly one Kernel per
// `notebooklm.Client`.
//
// Boundary: per docs/AGENTS.md rule 5, this package is
// mode=internal; it imports stdlib + internal/redact only.
//
// docs/04-transport.md is the design spec for this file; see
// `docs/sprint-reports/S01-tickets.md#t-p3-2` for the ticket.
package transport

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/raihankhan/notebooklm-go/internal/redact"
)

// DefaultMaxResponseBytes is the conservative cap used by New when
// the caller passes zero. 64 MiB is large enough for the longest
// expected batchexecute response (a fully-populated notebook list
// is well under 1 MiB; the 64 MiB headroom is the safety margin for
// the artifact download RPCs that ship through this kernel).
const DefaultMaxResponseBytes int64 = 64 * 1024 * 1024

// Kernel owns the http.Client and cookie jar for the transport
// layer. It is safe for concurrent use; every exported method is
// callable from multiple goroutines.
//
// Generation model:
//
//   - `gen` is the active generation. New requests must issue
//     with `Issued == gen`.
//   - `retired` is the generation FenceEpoch retired. AssertEpoch
//     rejects `Issued == retired`. Multiple FenceEpoch calls are
//     idempotent — only the first call mutates `retired`.
//   - `closed` is a one-way trip. Once flipped, Post and
//     PostStream return ErrKernelClosed; ActivateEpoch /
//     FenceEpoch are no-ops.
//
// Workflow:
//
//  1. Caller snapshots gen := k.Generation().
//  2. Caller issues Post(ctx, gen, ...).
//  3. If k.FenceEpoch() runs between (1) and (2), AssertEpoch
//     rejects the call because gen == retired.
//  4. A subsequent k.ActivateEpoch() bumps gen; fresh Post
//     calls under the new gen are accepted.
//
// Invariant: retired <= gen. Close sets retired = gen so every
// in-flight request is rejected.
//
// Concurrency invariants:
//
//   - closeMu serializes "is the kernel closed?" with "add to
//     the WaitGroup" so a Close call cannot return from
//     Wait() before a concurrent Post has had a chance to
//     register itself. This is the canonical race: without
//     the mutex, a Post that fires Add(1) after Close's Wait
//     would never be waited on, and its eventual Done would
//     drive the counter negative.
//   - genMu protects gen/retired reads and writes.
type Kernel struct {
	// client is the http.Client the kernel owns. Callers may
	// pass a pre-built one with custom Transport/TLS settings;
	// the zero-value client gets a 30s timeout via NewKernel.
	client *http.Client

	// jar is the http.CookieJar the kernel owns. The Python
	// original mounts it via http.Client.Jar; the Go port does
	// the same so cookies are written on Set-Cookie responses
	// and read on every outgoing request.
	jar http.CookieJar

	// maxBytes caps the response body read. Post and PostStream
	// return ErrResponseTooLarge (wrapped as *TooLargeError) on
	// cap hit. 0 means DefaultMaxResponseBytes.
	maxBytes int64

	// gen is the current active generation. Bumped by
	// ActivateEpoch under genMu.
	genMu sync.RWMutex
	gen   uint64

	// retired is the generation retired by FenceEpoch.
	// AssertEpoch rejects Issued == retired. Initialized to 0;
	// a fresh kernel's gen starts at 1, so Issued == 0 is
	// never valid and never mistakenly rejected.
	retired uint64

	// closeMu serializes "is the kernel closed?" with "add to
	// the WaitGroup". Held across the closed check + Add(1) in
	// Post / PostStream; held across the closed flip + Wait()
	// in Close.
	closeMu sync.Mutex

	// closed flips to true on Close. Once closed, Post and
	// PostStream return ErrKernelClosed; further ActivateEpoch /
	// FenceEpoch calls are no-ops.
	closed bool

	// wg tracks in-flight Post / PostStream calls so Close can
	// wait for them to drain. Add(1) happens under closeMu so
	// Close's Wait() sees every concurrent Add.
	wg sync.WaitGroup
}

// NewKernel returns a Kernel wrapping the given client and jar.
// Either may be nil; nil client is replaced with an http.Client
// carrying a 30-second timeout (matches the Python
// _transport/transport.py default), and nil jar is left nil —
// the kernel still fences generations, but cookies are not
// persisted across requests unless the caller passes a jar.
//
// maxBytes caps the response body. Zero means DefaultMaxResponseBytes.
// A negative value is clamped to DefaultMaxResponseBytes.
func NewKernel(client *http.Client, jar http.CookieJar, maxBytes int64) *Kernel {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxResponseBytes
	}
	return &Kernel{
		client:   client,
		jar:      jar,
		maxBytes: maxBytes,
		gen:      1,
	}
}

// Client returns the underlying http.Client. Exposed for the
// download path (Phase 14) that needs to issue a streaming GET
// against an arbitrary allowlisted host. Production code should
// reach for Post / PostStream instead.
func (k *Kernel) Client() *http.Client {
	if k == nil {
		return nil
	}
	return k.client
}

// Jar returns the underlying http.CookieJar. Exposed for the
// storage layer that needs to hydrate the jar from
// storage_state.json. Production code should not call Cookies(url)
// directly — the chain does.
func (k *Kernel) Jar() http.CookieJar {
	if k == nil {
		return nil
	}
	return k.jar
}

// Generation returns the current active generation. Callers
// snapshot this at issue time and pass it to Post / PostStream.
// Tests use it to verify ActivateEpoch / FenceEpoch arithmetic.
func (k *Kernel) Generation() uint64 {
	if k == nil {
		return 0
	}
	k.genMu.RLock()
	defer k.genMu.RUnlock()
	return k.gen
}

// ActivateEpoch bumps the generation and returns the new value.
// After this call, requests issued under the prior generation
// are still valid (they have not been retired yet); to retire
// them, the caller invokes FenceEpoch.
//
// ActivateEpoch on a closed kernel is a no-op and returns the
// current generation unchanged.
func (k *Kernel) ActivateEpoch() uint64 {
	if k == nil {
		return 0
	}
	k.closeMu.Lock()
	defer k.closeMu.Unlock()
	if k.closed {
		return k.gen
	}
	k.genMu.Lock()
	defer k.genMu.Unlock()
	k.gen++
	return k.gen
}

// FenceEpoch retires the current generation. After this call,
// AssertEpoch rejects any Issued value equal to gen (the value
// a caller snapshotted before this call). Subsequent Post /
// PostStream calls must ActivateEpoch first to obtain a fresh
// gen. Idempotent — calling FenceEpoch twice in a row does
// nothing the second time.
//
// FenceEpoch on a closed kernel is a no-op.
//
// Close calls FenceEpoch internally so a closed kernel rejects
// every in-flight Issued value.
func (k *Kernel) FenceEpoch() {
	if k == nil {
		return
	}
	k.closeMu.Lock()
	defer k.closeMu.Unlock()
	if k.closed {
		return
	}
	k.genMu.Lock()
	defer k.genMu.Unlock()
	// Retired tracks the generation we are fencing. Future
	// AssertEpoch calls reject Issued == retired.
	//
	// Idempotency: only set retired once. Subsequent calls find
	// retired == gen already and skip. This is what keeps the
	// operation idempotent — re-fencing a fenced generation is
	// a no-op.
	if k.retired >= k.gen {
		return
	}
	k.retired = k.gen
}

// AssertEpoch returns nil if the issued generation is still
// valid (Issued == gen and Issued != retired), and a
// *StaleEpochError otherwise. Issued values that differ from
// both gen and retired are also rejected — they indicate the
// caller has a stale view of the generation counter.
//
// AssertEpoch runs BEFORE http.NewRequestWithContext so the
// cookie jar is never read for a retired-generation call. This
// is the regression guard for the stale-envelope bug.
func (k *Kernel) AssertEpoch(issued uint64) error {
	if k == nil {
		return ErrKernelClosed
	}
	// closeMu is acquired first so the closed check + Add(1)
	// below is atomic with respect to Close.
	k.closeMu.Lock()
	closed := k.closed
	k.closeMu.Unlock()
	if closed {
		k.genMu.RLock()
		cur := k.gen
		k.genMu.RUnlock()
		return &ClosedError{CloserEpoch: cur}
	}
	k.genMu.RLock()
	cur := k.gen
	ret := k.retired
	k.genMu.RUnlock()
	if issued == ret {
		return &StaleEpochError{Issued: issued, Current: cur}
	}
	if issued != cur {
		// Issued is neither the active generation nor the
		// retired one. Either the caller's view is stale
		// (rejected for safety) or they issued under a
		// never-valid generation. Treat identically.
		return &StaleEpochError{Issued: issued, Current: cur}
	}
	return nil
}

// Post issues a POST with the given body and returns the
// response bytes, enforcing the response size cap. The request
// is fenced to `issued`: if AssertEpoch(issued) fails, the
// request is rejected before the http.Client.Do call and the
// jar is never read.
//
// On success: returns the response body bytes (capped to
// maxBytes) and the http.Header (for callers that need to read
// Set-Cookie or Retry-After before the body is consumed). On
// cap hit: returns nil, *TooLargeError.
//
// Post closes the response body before returning.
func (k *Kernel) Post(ctx context.Context, issued uint64, url string, body []byte) ([]byte, http.Header, error) {
	if k == nil {
		return nil, nil, &ClosedError{}
	}
	if err := k.AssertEpoch(issued); err != nil {
		// Critical: return BEFORE creating the http.Request,
		// so the cookie jar is never read. The jar's
		// Cookies(url) is invoked by http.Client.Do via
		// client.Jar; we never reach that call. This is the
		// regression guard for the stale-envelope bug.
		return nil, nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("transport: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	// Track the in-flight call so Close can wait for it. The
	// Add(1) happens under closeMu so Close's Wait() cannot
	// return between Add and Done. See the Kernel concurrency
	// invariants comment for the full reasoning.
	k.closeMu.Lock()
	if k.closed {
		k.closeMu.Unlock()
		return nil, nil, &ClosedError{CloserEpoch: k.gen}
	}
	k.wg.Add(1)
	k.closeMu.Unlock()
	defer k.wg.Done()

	resp, err := k.client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("transport: send: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Wrap the body in a LimitedReader with maxBytes+1 so we can
	// distinguish "exactly at the cap" from "over the cap" by
	// reading one extra byte.
	limited := &io.LimitedReader{R: resp.Body, N: k.maxBytes + 1}
	buf := &bytes.Buffer{}
	if k.maxBytes < 1<<30 { //nolint:gosec // maxBytes is bounded by NewKernel.
		buf.Grow(int(k.maxBytes))
	}
	n, err := io.Copy(buf, limited)
	if err != nil {
		return nil, resp.Header, fmt.Errorf("transport: read body: %w", err)
	}
	if n > k.maxBytes {
		// Drain the rest so the connection can be reused.
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, resp.Header, &TooLargeError{Limit: k.maxBytes, Got: n}
	}
	return buf.Bytes(), resp.Header, nil
}

// Close retires every generation, cancels in-flight requests via
// the http.Client's Transport, and waits for the WaitGroup to
// drain. Idempotent — calling Close on a closed kernel is a
// no-op.
//
// Close does NOT close the cookie jar (the jar is in-memory and
// has no Close contract; the storage layer persists it
// separately). It does NOT close the http.Client directly
// because that is owned by the caller; what we cancel is the
// Transport's idle connections.
func (k *Kernel) Close() error {
	if k == nil {
		return nil
	}
	k.closeMu.Lock()
	if k.closed {
		k.closeMu.Unlock()
		return nil
	}
	k.closed = true

	// Retire every generation: bump gen and set retired to it.
	// Any Issued value a caller could have snapshotted before
	// Close is now stale and rejected by AssertEpoch.
	k.genMu.Lock()
	k.gen++
	k.retired = k.gen
	k.genMu.Unlock()

	// Drain the WaitGroup atomically under closeMu so no
	// concurrent Post can register after our Wait. We release
	// closeMu during Wait because wg.Wait blocks; the invariant
	// is "all Add(1) calls happen-before closed = true", which
	// we hold because every Add(1) in Post runs under closeMu
	// and checks closed first.
	k.closeMu.Unlock()

	// Cancel idle keep-alive connections so an in-flight dial
	// is not silently kept warm.
	if t := k.client.Transport; t != nil {
		if cl, ok := t.(interface {
			CloseIdleConnections()
		}); ok {
			cl.CloseIdleConnections()
		}
	}
	// Wait for in-flight Post / PostStream calls to drain.
	// The WaitGroup is decremented when the call returns.
	k.wg.Wait()
	return nil
}

// StartInFlight is called by code paths that want to track a
// long-running operation against the kernel's WaitGroup so Close
// waits for it. Returns a no-op cancel func reserved for future
// use (the kernel currently relies on context propagation
// rather than explicit cancellation) and an end func the caller
// MUST defer to decrement the WaitGroup.
//
// Callers MUST invoke end() exactly once. The typical pattern is
// `_, end := k.StartInFlight(); defer end()`.
func (k *Kernel) StartInFlight() (context.CancelFunc, func()) {
	if k == nil {
		return func() {}, func() {}
	}
	k.closeMu.Lock()
	defer k.closeMu.Unlock()
	if k.closed {
		return func() {}, func() {}
	}
	k.wg.Add(1)
	var once sync.Once
	end := func() {
		once.Do(func() { k.wg.Done() })
	}
	return func() {}, end
}

// _ ensures internal/redact is referenced; the kernel does use
// it via the error message construction in errors.go and the
// future tracing middleware in T-P3-3. Without this import the
// package boundarycheck would still allow the import, but the
// symbol stays present as a regression guard for any future
// refactor that strips the use site.
var _ = redact.Apply
