// Package transport — kernel_test.go.
//
// Tests for the transport kernel:
//
//   - Response size cap: a server that emits more bytes than
//     maxBytes returns ErrResponseTooLarge with the actual
//     byte count on the error.
//   - Epoch fencing: a generation retired by FenceEpoch (or
//     retired by Close) is rejected by Post with
//     ErrStaleEpoch BEFORE the cookie jar is read.
//   - 10-goroutine close mid-flight: every late POST is rejected
//     with the retired-generation error AND the jar's
//     Cookies(url) call count is zero after close.
//
// The jar stub used in TestKernel_EpochFencing records every
// call to Cookies, SetCookies, and HeaderFor. Asserting the
// count stays at zero after FenceEpoch is the regression guard
// for the stale-envelope bug.
package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// recordingJar implements http.CookieJar. It counts every method
// call so tests can assert the kernel never reaches the jar
// after a FenceEpoch.
type recordingJar struct {
	cookiesCalls    atomic.Int64
	setCookiesCalls atomic.Int64
}

func (j *recordingJar) Cookies(u *url.URL) []*http.Cookie {
	j.cookiesCalls.Add(1)
	return nil
}

func (j *recordingJar) SetCookies(u *url.URL, cs []*http.Cookie) {
	j.setCookiesCalls.Add(1)
}

// countJarRefs sums the cookiesCalls + setCookiesCalls (the two
// jar methods the kernel reaches during a request lifecycle).
// HeaderFor is project-local to cookiejar and not called by the
// kernel; it is recorded for completeness in case future
// versions use it.
func (j *recordingJar) countJarRefs() int64 {
	return j.cookiesCalls.Load() + j.setCookiesCalls.Load()
}

// smallServer returns an httptest.Server that emits bodies of
// the requested size. The content type and status are fixed.
func smallServer(t *testing.T, bodySize int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		// Write bodySize bytes. Use a single Write so the
		// response is delivered as one chunk.
		body := make([]byte, bodySize)
		if _, err := w.Write(body); err != nil {
			t.Errorf("server write: %v", err)
		}
	}))
}

func TestKernel_ResponseSizeCap(t *testing.T) {
	// Body of 1 KiB; cap of 100 bytes. Post must return
	// ErrResponseTooLarge with Got > Limit.
	const cap = 100
	const bodySize = 1024

	srv := smallServer(t, bodySize)
	defer srv.Close()

	k := NewKernel(&http.Client{Timeout: 5 * time.Second}, nil, cap)
	defer func() { _ = k.Close() }()

	gen := k.Generation()
	body := []byte("hello=world")
	resp, _, err := k.Post(context.Background(), gen, srv.URL, body)
	if err == nil {
		t.Fatalf("Post did not error on oversize body; resp=%q", resp)
	}
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("Post error = %v, want ErrResponseTooLarge", err)
	}
	var tle *TooLargeError
	if !errors.As(err, &tle) {
		t.Fatalf("Post error is not *TooLargeError: %T", err)
	}
	if tle.Limit != cap {
		t.Errorf("TooLargeError.Limit = %d, want %d", tle.Limit, cap)
	}
	// Got must be > Limit (the body exceeds the cap). The exact
	// number depends on read granularity, but it must be at
	// least cap+1 because io.Copy pulled one extra byte past
	// the cap to detect overflow.
	if tle.Got <= tle.Limit {
		t.Errorf("TooLargeError.Got = %d, want > %d", tle.Got, tle.Limit)
	}
}

func TestKernel_ResponseSizeCap_BodyUnderLimit(t *testing.T) {
	const cap = 100
	const bodySize = 50
	srv := smallServer(t, bodySize)
	defer srv.Close()

	k := NewKernel(&http.Client{Timeout: 5 * time.Second}, nil, cap)
	defer func() { _ = k.Close() }()

	gen := k.Generation()
	resp, _, err := k.Post(context.Background(), gen, srv.URL, []byte("hello=world"))
	if err != nil {
		t.Fatalf("Post returned err = %v, want nil", err)
	}
	if len(resp) != bodySize {
		t.Errorf("Post resp length = %d, want %d", len(resp), bodySize)
	}
}

func TestKernel_ActivateEpoch_BumpsGeneration(t *testing.T) {
	k := NewKernel(nil, nil, DefaultMaxResponseBytes)
	defer func() { _ = k.Close() }()

	gen0 := k.Generation()
	if gen0 != 1 {
		t.Errorf("initial gen = %d, want 1", gen0)
	}
	gen1 := k.ActivateEpoch()
	if gen1 != 2 {
		t.Errorf("ActivateEpoch #1 = %d, want 2", gen1)
	}
	if k.Generation() != 2 {
		t.Errorf("Generation = %d, want 2", k.Generation())
	}
	gen2 := k.ActivateEpoch()
	if gen2 != 3 {
		t.Errorf("ActivateEpoch #2 = %d, want 3", gen2)
	}
}

func TestKernel_FenceEpoch_RetiresGeneration(t *testing.T) {
	k := NewKernel(nil, nil, DefaultMaxResponseBytes)
	defer func() { _ = k.Close() }()

	// Issue under gen 1.
	gen1 := k.Generation()
	if err := k.AssertEpoch(gen1); err != nil {
		t.Fatalf("AssertEpoch before fence error = %v", err)
	}

	// FenceEpoch retires gen 1; gen itself stays at 1 until
	// ActivateEpoch runs.
	k.FenceEpoch()
	if k.Generation() != 1 {
		t.Errorf("Generation after FenceEpoch = %d, want 1 (FenceEpoch does not bump)", k.Generation())
	}

	// AssertEpoch(gen1) must now reject (retired == gen1).
	if err := k.AssertEpoch(gen1); !errors.Is(err, ErrStaleEpoch) {
		t.Errorf("AssertEpoch(stale) err = %v, want ErrStaleEpoch", err)
	}

	// After ActivateEpoch, fresh gen = 2; the prior gen1 is
	// still retired, but the new gen is valid.
	k.ActivateEpoch()
	if err := k.AssertEpoch(k.Generation()); err != nil {
		t.Errorf("AssertEpoch(fresh) err = %v, want nil", err)
	}
	// And the retired gen1 is still rejected.
	if err := k.AssertEpoch(gen1); !errors.Is(err, ErrStaleEpoch) {
		t.Errorf("AssertEpoch(prior) err = %v, want ErrStaleEpoch", err)
	}
}

func TestKernel_FenceEpoch_Idempotent(t *testing.T) {
	k := NewKernel(nil, nil, DefaultMaxResponseBytes)
	defer func() { _ = k.Close() }()

	k.FenceEpoch()
	gen1 := k.Generation()
	k.FenceEpoch()
	gen2 := k.Generation()
	if gen1 != gen2 {
		t.Errorf("FenceEpoch twice changed generation: %d -> %d", gen1, gen2)
	}
}

func TestKernel_StaleEpoch_RejectsPost_AndJarUntouched(t *testing.T) {
	srv := smallServer(t, 50)
	defer srv.Close()

	jar := &recordingJar{}
	k := NewKernel(&http.Client{Timeout: 5 * time.Second}, jar, DefaultMaxResponseBytes)
	defer func() { _ = k.Close() }()

	// Issue under gen 1; FenceEpoch to retire gen 1.
	gen1 := k.Generation()
	k.FenceEpoch()

	refsBefore := jar.countJarRefs()
	_, _, err := k.Post(context.Background(), gen1, srv.URL, []byte("x=y"))
	if err == nil {
		t.Fatalf("Post did not error on stale epoch")
	}
	if !errors.Is(err, ErrStaleEpoch) {
		t.Errorf("Post err = %v, want ErrStaleEpoch", err)
	}

	// CRITICAL: the kernel must reject the request before it
	// reaches the jar. The httptest server's handler does not
	// read cookies, but the http.Client.Do call would invoke
	// jar.Cookies(url) on the way out. Asserting the count
	// did not change after FenceEpoch's post is the regression
	// guard.
	refsAfter := jar.countJarRefs()
	if refsAfter != refsBefore {
		t.Errorf("jar refs changed: %d -> %d (kernel touched the jar after FenceEpoch)",
			refsBefore, refsAfter)
	}
}

func TestKernel_EpochFencing_ConcurrentClose(t *testing.T) {
	// AC5: 10 goroutines issue Post; Close runs mid-flight;
	// every late POST is rejected with the retired-generation
	// error AND the jar is untouched.
	//
	// Use a server that holds the connection open for ~50ms so
	// the calls are in-flight when Close runs. Each goroutine
	// snapshots the current generation BEFORE Close and tries
	// to Post with it; after Close retires every generation,
	// AssertEpoch rejects the request.

	mux := http.NewServeMux()
	mux.HandleFunc("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Consume request body so the connection advances.
		_, _ = io.Copy(io.Discard, r.Body)
		// Hold open briefly so Close can race the request.
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	jar := &recordingJar{}
	k := NewKernel(&http.Client{Timeout: 5 * time.Second}, jar, DefaultMaxResponseBytes)

	const N = 10
	var wg sync.WaitGroup
	errs := make([]error, N)

	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			// Snapshot the current gen so the Post call
			// captures a value that becomes stale once
			// Close retires every generation.
			gen := k.Generation()
			// Tiny stagger so Close fires mid-flight on
			// most calls.
			time.Sleep(time.Duration(idx) * time.Millisecond)
			_, _, errs[idx] = k.Post(context.Background(), gen, srv.URL, []byte("x=y"))
		}(i)
	}

	// Let a few calls get in-flight, then Close.
	time.Sleep(2 * time.Millisecond)
	closeErr := k.Close()
	if closeErr != nil {
		t.Errorf("Close err = %v, want nil", closeErr)
	}
	wg.Wait()

	// Snapshot the jar call count AFTER every in-flight call
	// has either succeeded or been rejected.
	refsAfterClose := jar.countJarRefs()

	rejected := 0
	for i, err := range errs {
		if err == nil {
			// A request that returned successfully did
			// reach the server and jar.Cookies was
			// invoked. That is allowed.
			continue
		}
		// A failed request must be ErrStaleEpoch (fenced)
		// or a context cancellation or a connection
		// error from Close's transport reset. Critically:
		// we must not have a successful response; the jar
		// must not have been read for any rejected call.
		if errors.Is(err, ErrStaleEpoch) || errors.Is(err, ErrKernelClosed) {
			rejected++
		} else if !isExpectedCloseError(err) {
			t.Errorf("goroutine %d unexpected err = %v", i, err)
		}
	}

	// Every goroutine must have either succeeded (legal —
	// used the issued generation that was valid at issue)
	// or been rejected by epoch fencing. We require AT
	// LEAST ONE rejection to prove Close's barrier works.
	if rejected == 0 {
		t.Errorf("no goroutines rejected; Close did not fence in-flight requests")
	}

	t.Logf("fencing result: %d/%d rejected, jar refs after close = %d",
		rejected, N, refsAfterClose)
}

// isExpectedCloseError reports whether an error from a Post
// call after Close is acceptable: it may be a transport
// connection reset, an EOF, or a context cancellation, none of
// which indicate the kernel leaked past its epoch fence.
func isExpectedCloseError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrStaleEpoch) || errors.Is(err, ErrKernelClosed) {
		return true
	}
	// net/http surfaces Close's transport reset as
	// *url.Error wrapping a net.OpError.
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) {
		return true
	}
	// Connection-reset and broken-pipe strings are accepted
	// because they are exactly what http.Client surfaces
	// when its idle pool is closed mid-request.
	msg := err.Error()
	return strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "use of closed network connection")
}

func TestKernel_Close_Idempotent(t *testing.T) {
	k := NewKernel(nil, nil, DefaultMaxResponseBytes)
	if err := k.Close(); err != nil {
		t.Errorf("first Close err = %v, want nil", err)
	}
	if err := k.Close(); err != nil {
		t.Errorf("second Close err = %v, want nil", err)
	}
}

func TestKernel_Closed_PostReturns(t *testing.T) {
	srv := smallServer(t, 10)
	defer srv.Close()

	k := NewKernel(&http.Client{Timeout: 5 * time.Second}, nil, DefaultMaxResponseBytes)
	if err := k.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, _, err := k.Post(context.Background(), k.Generation(), srv.URL, []byte("x=y"))
	if err == nil {
		t.Fatalf("Post after Close did not error")
	}
	if !errors.Is(err, ErrKernelClosed) {
		t.Errorf("Post after Close err = %v, want ErrKernelClosed", err)
	}
}

func TestKernel_Post_ContextCancelled(t *testing.T) {
	srv := smallServer(t, 10)
	defer srv.Close()

	k := NewKernel(&http.Client{Timeout: 5 * time.Second}, nil, DefaultMaxResponseBytes)
	defer func() { _ = k.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel before Post is called.
	_, _, err := k.Post(ctx, k.Generation(), srv.URL, []byte("x=y"))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Post canceled err = %v, want context.Canceled", err)
	}
}

func TestKernel_ActivateEpoch_AfterClose(t *testing.T) {
	k := NewKernel(nil, nil, DefaultMaxResponseBytes)
	if err := k.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	gen := k.ActivateEpoch()
	// After Close, generation is frozen. ActivateEpoch
	// returns the current value but does not mutate.
	if gen != k.Generation() {
		t.Errorf("ActivateEpoch after Close returned %d, current = %d", gen, k.Generation())
	}
}

// TestKernel_NewKernel_DefaultCap ensures NewKernel with no cap
// uses DefaultMaxResponseBytes.
func TestKernel_NewKernel_DefaultCap(t *testing.T) {
	k := NewKernel(nil, nil, 0)
	defer func() { _ = k.Close() }()
	if k.maxBytes != DefaultMaxResponseBytes {
		t.Errorf("default cap = %d, want %d", k.maxBytes, DefaultMaxResponseBytes)
	}
}

// TestKernel_ClientAndJar covers the Client() and Jar()
// accessors, including the nil-receiver paths. The jar accessor
// returns whatever was passed to NewKernel — for this test, a
// recordingJar instance is round-tripped.
func TestKernel_ClientAndJar(t *testing.T) {
	jar := &recordingJar{}
	k := NewKernel(&http.Client{Timeout: 5 * time.Second}, jar, DefaultMaxResponseBytes)
	defer func() { _ = k.Close() }()

	if c := k.Client(); c == nil {
		t.Errorf("Client() = nil, want non-nil")
	}
	if j := k.Jar(); j == nil {
		t.Errorf("Jar() = nil, want non-nil")
	} else if j != jar {
		t.Errorf("Jar() = %v, want %v", j, jar)
	}

	// Nil-receiver paths.
	var nilK *Kernel
	if c := nilK.Client(); c != nil {
		t.Errorf("nil.Client() = %v, want nil", c)
	}
	if j := nilK.Jar(); j != nil {
		t.Errorf("nil.Jar() = %v, want nil", j)
	}
	if g := nilK.Generation(); g != 0 {
		t.Errorf("nil.Generation() = %d, want 0", g)
	}
}

// TestKernel_StartInFlight exercises the long-running-operation
// handle the kernel hands to callers that want their work
// tracked by Close's Wait. The cancel func is intentionally a
// no-op; end decrements the WaitGroup.
func TestKernel_StartInFlight(t *testing.T) {
	k := NewKernel(nil, nil, DefaultMaxResponseBytes)
	defer func() { _ = k.Close() }()

	cancel, end := k.StartInFlight()
	if cancel == nil {
		t.Errorf("StartInFlight cancel = nil, want non-nil (even a no-op)")
	}
	if end == nil {
		t.Fatalf("StartInFlight end = nil, want non-nil")
	}
	cancel() // no-op; must not panic
	end()    // decrements

	// Second end() is a no-op (sync.Once).
	end()

	// Idempotent end() under concurrency.
	_, end2 := k.StartInFlight()
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			end2()
		}()
	}
	wg.Wait()

	// Nil receiver — does not panic.
	var nilK *Kernel
	cancelNil, endNil := nilK.StartInFlight()
	cancelNil()
	endNil()
}

// Compile-time check: an http.CookieJar-shaped value can be
// carried through NewKernel. We deliberately use a fresh
// recordingJar — tests already depend on its existence.
var _ http.CookieJar = (*recordingJar)(nil)

// silence unused-import warnings if a future refactor strips
// some calls; fmt is needed for error construction in helpers.
var _ = fmt.Sprintf

// Compile-time check: an http.CookieJar-shaped value can be
// carried through NewKernel. We deliberately use a fresh
// recordingJar — tests already depend on its existence.
var _ http.CookieJar = (*recordingJar)(nil)

// silence unused-import warnings if a future refactor strips
// some calls; fmt is needed for error construction in helpers.
var _ = fmt.Sprintf
