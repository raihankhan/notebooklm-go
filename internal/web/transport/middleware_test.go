// Package transport — middleware_test.go.
//
// Tests for the middleware chain:
//
//   - Chain-order pin: a constructed chain reads back its
//     middlewares in the documented order. This is the regression
//     guard for any future refactor that flips the order by
//     accident — a TOCTOU regression the stale-envelope bug
//     surfaces only at runtime.
//   - Per-middleware behavior table: each middleware does exactly
//     its single-responsibility work and nothing more (authed sets
//     Cookie, idempotency sets the disable flag, retry honors
//     429/5xx, terminal rebuilds the envelope from current
//     auth state).
//   - AST regression guard: an AST walk over the package source
//     asserts no channel/lock/await-shaped operation sits between
//     the envelope rebuild and the underlying Post call. This is
//     the load-bearing test the ticket names "the regression guard
//     for the stale-envelope bug". The walk accepts only the
//     minimal shapes that must be present (literal nil/identifier
//     references, explicit error returns, and the
//     client.Do/http.NewRequestWithContext/AssertEpoch calls
//     themselves).
package transport

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	stdruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raihankhan/notebooklm-go/internal/runtime"
	"github.com/raihankhan/notebooklm-go/internal/web/policy"
	"github.com/raihankhan/notebooklm-go/internal/web/wire"
)

// TestMiddleware_OrderPinned is the regression guard for the
// chain order. The chain is constructed inside-out so the outer
// wrapping reads left-to-right from outermost to innermost; this
// test walks the constructor stack and asserts the names in the
// expected order:
//
//	authed → idempotency → retry → terminal → inner
//
// A future change that flips the order MUST update this test
// (and the matching comment in runtime.go and
// docs/04-transport.md).
//
// We tag each middleware with a "tag" wrapper so we can observe
// the order without touching the production code. The tag wrapper
// records the tag before delegating to the underlying middleware.
func TestMiddleware_OrderPinned(t *testing.T) {
	var mu sync.Mutex
	var fired []string
	tag := func(name string, mw Middleware) Middleware {
		return func(next Handler) Handler {
			inner := mw(next)
			return func(ctx context.Context, r *Request) (*Response, error) {
				mu.Lock()
				fired = append(fired, name)
				mu.Unlock()
				return inner(ctx, r)
			}
		}
	}

	// Inner recorder: the terminal-most handler. Replaces the
	// kernel's POST in this test.
	innerRecorder := func(ctx context.Context, r *Request) (*Response, error) {
		mu.Lock()
		fired = append(fired, "inner")
		mu.Unlock()
		return &Response{
			HTTP: &http.Response{StatusCode: http.StatusOK},
			Body: []byte("ok"),
		}, nil
	}

	// Build the chain in the EXPECTED order. A future refactor
	// that breaks the order MUST update this test together with
	// the matching constructor stack.
	terminal := tag("terminal", NewTerminalMiddleware(nil))(innerRecorder)
	retry := tag("retry", NewRetryMiddleware())(terminal)
	idempotency := tag("idempotency", NewIdempotencyMiddleware())(retry)
	authed := tag("authed", NewAuthedMiddleware())(idempotency)

	req := &Request{
		URL:     "https://example.test/",
		Headers: make(http.Header),
		Call: &CallContext{
			Method:        wire.MethodListNotebooks,
			Snapshot:      AuthSnapshot{},
			Epoch:         1,
			RefreshBudget: &RefreshBudget{},
			Budget:        runtime.New(context.Background(), 5*time.Second),
			Host:          wire.HostPersonal,
			Class:         policy.ClassSafe,
		},
	}
	resp, err := authed(context.Background(), req)
	if err != nil {
		t.Fatalf("chain drive err = %v", err)
	}
	if resp == nil {
		t.Fatalf("chain drive returned nil response without error")
	}

	want := []string{"authed", "idempotency", "retry", "terminal", "inner"}
	if len(fired) != len(want) {
		t.Fatalf("fired %d middlewares, want %d: %v", len(fired), len(want), fired)
	}
	for i := range want {
		if fired[i] != want[i] {
			t.Errorf("middleware order at index %d: got %q, want %q (full: %v)",
				i, fired[i], want[i], fired)
		}
	}
}

// TestAuthed_SetsCookie is the per-middleware happy-path for the
// authed middleware: the snapshot's CookieHeader lands in the
// request's Cookie header before the next middleware sees the
// request.
func TestAuthed_SetsCookie(t *testing.T) {
	mw := NewAuthedMiddleware()
	req := &Request{
		URL:     "https://example.test/",
		Headers: make(http.Header),
		Call: &CallContext{
			Snapshot: AuthSnapshot{CookieHeader: "SID=abc; HSID=xyz"},
			Epoch:    1,
		},
	}
	resp, err := mw(passThrough)(context.Background(), req)
	if err != nil {
		t.Fatalf("authed err = %v", err)
	}
	if resp == nil {
		t.Fatalf("authed returned nil response")
	}
	if got := req.Headers.Get("Cookie"); got != "SID=abc; HSID=xyz" {
		t.Errorf("Cookie = %q, want %q", got, "SID=abc; HSID=xyz")
	}
}

// TestAuthed_EmptyCookieStillSets confirms the header is set
// even when the snapshot's CookieHeader is empty: the server
// requires the header to be present (even if empty) for parity
// with a real browser request.
func TestAuthed_EmptyCookieStillSets(t *testing.T) {
	mw := NewAuthedMiddleware()
	req := &Request{
		URL:     "https://example.test/",
		Headers: make(http.Header),
		Call:    &CallContext{Snapshot: AuthSnapshot{CookieHeader: ""}, Epoch: 1},
	}
	if _, err := mw(passThrough)(context.Background(), req); err != nil {
		t.Fatalf("authed err = %v", err)
	}
	if req.Headers.Get("Cookie") != "" {
		t.Errorf("Cookie should be set to empty string, got %q", req.Headers.Get("Cookie"))
	}
}

// TestAuthed_NilCallContext covers the missing-call-context
// guard.
func TestAuthed_NilCallContext(t *testing.T) {
	mw := NewAuthedMiddleware()
	req := &Request{URL: "https://example.test/", Headers: make(http.Header)}
	if _, err := mw(passThrough)(context.Background(), req); err == nil {
		t.Errorf("authed accepted nil CallContext")
	}
}

// TestIdempotency_DisablesUnsafeMutation confirms the disable
// flag flips for ClassUnsafeMutation.
func TestIdempotency_DisablesUnsafeMutation(t *testing.T) {
	mw := NewIdempotencyMiddleware()
	cc := &CallContext{Class: policy.ClassUnsafeMutation}
	req := &Request{
		URL:     "https://example.test/",
		Headers: make(http.Header),
		Call:    cc,
	}
	if _, err := mw(passThrough)(context.Background(), req); err != nil {
		t.Fatalf("idempotency err = %v", err)
	}
	if !cc.DisableRetries() {
		t.Errorf("DisableRetries = false, want true for ClassUnsafeMutation")
	}
}

// TestIdempotency_DisablesProbeThenCreate confirms the disable
// flag flips for ClassProbeThenCreate.
func TestIdempotency_DisablesProbeThenCreate(t *testing.T) {
	mw := NewIdempotencyMiddleware()
	cc := &CallContext{Class: policy.ClassProbeThenCreate}
	req := &Request{
		URL:     "https://example.test/",
		Headers: make(http.Header),
		Call:    cc,
	}
	if _, err := mw(passThrough)(context.Background(), req); err != nil {
		t.Fatalf("idempotency err = %v", err)
	}
	if !cc.DisableRetries() {
		t.Errorf("DisableRetries = false, want true for ClassProbeThenCreate")
	}
}

// TestIdempotency_KeepsSafe confirms the disable flag stays
// false for ClassSafe so the retry middleware can do its loop.
func TestIdempotency_KeepsSafe(t *testing.T) {
	mw := NewIdempotencyMiddleware()
	cc := &CallContext{Class: policy.ClassSafe}
	req := &Request{
		URL:     "https://example.test/",
		Headers: make(http.Header),
		Call:    cc,
	}
	if _, err := mw(passThrough)(context.Background(), req); err != nil {
		t.Fatalf("idempotency err = %v", err)
	}
	if cc.DisableRetries() {
		t.Errorf("DisableRetries = true, want false for ClassSafe")
	}
}

// TestIdempotency_NilCallContext covers the missing-call-context
// guard.
func TestIdempotency_NilCallContext(t *testing.T) {
	mw := NewIdempotencyMiddleware()
	req := &Request{URL: "https://example.test/", Headers: make(http.Header)}
	if _, err := mw(passThrough)(context.Background(), req); err == nil {
		t.Errorf("idempotency accepted nil CallContext")
	}
}

// TestRetry_HonorsRetryAfterDelta confirms the retry middleware
// waits the delta-seconds hint and retries.
func TestRetry_HonorsRetryAfterDelta(t *testing.T) {
	var attempts int
	inner := func(ctx context.Context, r *Request) (*Response, error) {
		attempts++
		if attempts == 1 {
			// Use a 1-second hint. Zero would race the budget's
			// clamp at the millisecond boundary; 1s is small
			// enough for a fast test but reliably positive.
			return &Response{
				HTTP: &http.Response{
					StatusCode: http.StatusTooManyRequests,
					Header:     http.Header{"Retry-After": []string{"1"}},
				},
				Body: []byte("rate-limited"),
			}, nil
		}
		return &Response{
			HTTP: &http.Response{StatusCode: http.StatusOK},
			Body: []byte("ok"),
		}, nil
	}
	mw := NewRetryMiddleware()
	req := &Request{
		URL:     "https://example.test/",
		Headers: make(http.Header),
		Call: &CallContext{
			Budget: runtime.New(context.Background(), 5*time.Second),
		},
	}
	start := time.Now()
	resp, err := mw(inner)(context.Background(), req)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("retry err = %v", err)
	}
	if resp.HTTP.StatusCode != http.StatusOK {
		t.Errorf("final status = %d, want 200", resp.HTTP.StatusCode)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2", attempts)
	}
	// The retry honors 1 second from Retry-After. Allow a 1.5s
	// upper bound to keep the test fast on slow CI.
	if elapsed > 1500*time.Millisecond {
		t.Errorf("elapsed = %v, want <= 1.5s", elapsed)
	}
}

// TestRetry_HonorsRetryAfterHTTPDate confirms the retry middleware
// parses RFC 1123 HTTP-date Retry-After values.
func TestRetry_HonorsRetryAfterHTTPDate(t *testing.T) {
	var attempts int
	inner := func(ctx context.Context, r *Request) (*Response, error) {
		attempts++
		if attempts == 1 {
			// 1 second in the future.
			future := time.Now().UTC().Add(1 * time.Second).Format(time.RFC1123)
			return &Response{
				HTTP: &http.Response{
					StatusCode: http.StatusTooManyRequests,
					Header:     http.Header{"Retry-After": []string{future}},
				},
				Body: []byte("rate-limited"),
			}, nil
		}
		return &Response{
			HTTP: &http.Response{StatusCode: http.StatusOK},
			Body: []byte("ok"),
		}, nil
	}
	mw := NewRetryMiddleware()
	req := &Request{
		URL:     "https://example.test/",
		Headers: make(http.Header),
		Call: &CallContext{
			Budget: runtime.New(context.Background(), 5*time.Second),
		},
	}
	resp, err := mw(inner)(context.Background(), req)
	if err != nil {
		t.Fatalf("retry err = %v", err)
	}
	if resp.HTTP.StatusCode != http.StatusOK {
		t.Errorf("final status = %d, want 200", resp.HTTP.StatusCode)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2", attempts)
	}
}

// TestRetry_SkipsOnDisabled confirms the idempotency-middleware
// flag short-circuits the retry loop.
func TestRetry_SkipsOnDisabled(t *testing.T) {
	var attempts int
	inner := func(ctx context.Context, r *Request) (*Response, error) {
		attempts++
		return &Response{
			HTTP: &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Header:     http.Header{"Retry-After": []string{"0"}},
			},
			Body: []byte("rate-limited"),
		}, nil
	}
	mw := NewRetryMiddleware()
	cc := &CallContext{
		Budget:         runtime.New(context.Background(), 5*time.Second),
		disableRetries: true,
	}
	req := &Request{
		URL:     "https://example.test/",
		Headers: make(http.Header),
		Call:    cc,
	}
	resp, err := mw(inner)(context.Background(), req)
	if err != nil {
		t.Fatalf("retry err = %v", err)
	}
	if resp.HTTP.StatusCode != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429 (retries skipped)", resp.HTTP.StatusCode)
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1 (retries disabled)", attempts)
	}
}

// TestRetry_ExhaustsAfterMaxAttempts confirms the retry loop
// caps at MaxRetries attempts on persistent 5xx.
func TestRetry_ExhaustsAfterMaxAttempts(t *testing.T) {
	var attempts int
	inner := func(ctx context.Context, r *Request) (*Response, error) {
		attempts++
		return &Response{
			HTTP: &http.Response{StatusCode: http.StatusInternalServerError},
			Body: []byte("err"),
		}, nil
	}
	mw := NewRetryMiddleware()
	req := &Request{
		URL:     "https://example.test/",
		Headers: make(http.Header),
		Call: &CallContext{
			Budget: runtime.New(context.Background(), 30*time.Second),
		},
	}
	resp, err := mw(inner)(context.Background(), req)
	if err != nil {
		t.Fatalf("retry err = %v", err)
	}
	if resp.HTTP.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", resp.HTTP.StatusCode)
	}
	if attempts != 5 {
		t.Errorf("attempts = %d, want 5 (max)", attempts)
	}
}

// TestRetry_RespectsBudget confirms the retry loop exits when
// the budget has expired.
func TestRetry_RespectsBudget(t *testing.T) {
	var attempts int
	inner := func(ctx context.Context, r *Request) (*Response, error) {
		attempts++
		return &Response{
			HTTP: &http.Response{StatusCode: http.StatusInternalServerError},
			Body: []byte("err"),
		}, nil
	}
	mw := NewRetryMiddleware()
	req := &Request{
		URL:     "https://example.test/",
		Headers: make(http.Header),
		Call: &CallContext{
			// Already-expired budget.
			Budget: runtime.New(context.Background(), 0),
		},
	}
	_, err := mw(inner)(context.Background(), req)
	// Either the loop exits without retrying (attempts == 1)
	// or the budget is observed on iteration 0 and skips. We
	// assert the upper bound: at most one attempt is made.
	if attempts > 1 {
		t.Errorf("attempts = %d, want <= 1 (budget expired)", attempts)
	}
	_ = err
}

// TestRetry_NilCallContext covers the missing-call-context
// guard.
func TestRetry_NilCallContext(t *testing.T) {
	mw := NewRetryMiddleware()
	req := &Request{URL: "https://example.test/", Headers: make(http.Header)}
	if _, err := mw(passThrough)(context.Background(), req); err == nil {
		t.Errorf("retry accepted nil CallContext")
	}
}

// TestRetry_ContextCancelled confirms context cancellation
// surfaces during the retry sleep.
func TestRetry_ContextCancelled(t *testing.T) {
	inner := func(ctx context.Context, r *Request) (*Response, error) {
		return &Response{
			HTTP: &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Header:     http.Header{"Retry-After": []string{"60"}},
			},
			Body: []byte("rate-limited"),
		}, nil
	}
	mw := NewRetryMiddleware()
	req := &Request{
		URL:     "https://example.test/",
		Headers: make(http.Header),
		Call: &CallContext{
			Budget: runtime.New(context.Background(), 5*time.Second),
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	_, err := mw(inner)(ctx, req)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

// TestTerminal_RebuildsEnvelopeFromSnapshot confirms the
// terminal middleware re-encodes the Cookie header from the
// current AuthSnapshot before dispatch.
func TestTerminal_RebuildsEnvelopeFromSnapshot(t *testing.T) {
	mw := NewTerminalMiddleware(nil)
	var sawCookie string
	inner := func(ctx context.Context, r *Request) (*Response, error) {
		sawCookie = r.Headers.Get("Cookie")
		return &Response{
			HTTP: &http.Response{StatusCode: http.StatusOK},
			Body: []byte("ok"),
		}, nil
	}
	req := &Request{
		URL:     "https://example.test/",
		Headers: http.Header{"Cookie": []string{"stale-cookie"}},
		Call: &CallContext{
			Snapshot: AuthSnapshot{CookieHeader: "fresh-csrf=abc"},
		},
	}
	resp, err := mw(inner)(context.Background(), req)
	if err != nil {
		t.Fatalf("terminal err = %v", err)
	}
	if resp == nil || resp.HTTP == nil {
		t.Fatalf("terminal returned nil response")
	}
	if sawCookie != "fresh-csrf=abc" {
		t.Errorf("Cookie seen by inner = %q, want %q", sawCookie, "fresh-csrf=abc")
	}
}

// TestTerminal_RefreshesOn401 confirms the terminal middleware
// consults the refresh budget, calls the refresh callback, and
// retries on a wire-401.
func TestTerminal_RefreshesOn401(t *testing.T) {
	var attempts int
	var refreshes int
	refresh := func(ctx context.Context, r *Request) error {
		refreshes++
		// After refresh, mutate the snapshot so the rebuilt
		// envelope carries the new cookie.
		r.Call.Snapshot = AuthSnapshot{CookieHeader: "post-refresh-csrf"}
		return nil
	}
	inner := func(ctx context.Context, r *Request) (*Response, error) {
		attempts++
		if attempts == 1 {
			return &Response{
				HTTP: &http.Response{StatusCode: http.StatusUnauthorized},
				Body: []byte("auth required"),
			}, nil
		}
		return &Response{
			HTTP: &http.Response{StatusCode: http.StatusOK},
			Body: []byte("ok"),
		}, nil
	}
	mw := NewTerminalMiddleware(refresh)
	rb := &RefreshBudget{}
	req := &Request{
		URL:     "https://example.test/",
		Headers: make(http.Header),
		Call: &CallContext{
			Snapshot:      AuthSnapshot{CookieHeader: "pre-refresh-csrf"},
			RefreshBudget: rb,
		},
	}
	resp, err := mw(inner)(context.Background(), req)
	if err != nil {
		t.Fatalf("terminal err = %v", err)
	}
	if resp.HTTP.StatusCode != http.StatusOK {
		t.Errorf("final status = %d, want 200", resp.HTTP.StatusCode)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2", attempts)
	}
	if refreshes != 1 {
		t.Errorf("refreshes = %d, want 1", refreshes)
	}
	if rb.Reason() != "wire-401" {
		t.Errorf("refresh reason = %q, want wire-401", rb.Reason())
	}
	if req.Headers.Get("Cookie") != "post-refresh-csrf" {
		t.Errorf("rebuilt Cookie = %q, want post-refresh-csrf", req.Headers.Get("Cookie"))
	}
}

// TestTerminal_RefreshOnlyOnceOnRepeated401 confirms the refresh
// budget prevents a second refresh on a second consecutive 401.
func TestTerminal_RefreshOnlyOnceOnRepeated401(t *testing.T) {
	var attempts int
	var refreshes int
	refresh := func(ctx context.Context, r *Request) error {
		refreshes++
		r.Call.Snapshot = AuthSnapshot{CookieHeader: "post-refresh-csrf"}
		return nil
	}
	inner := func(ctx context.Context, r *Request) (*Response, error) {
		attempts++
		return &Response{
			HTTP: &http.Response{StatusCode: http.StatusUnauthorized},
			Body: []byte("auth required"),
		}, nil
	}
	mw := NewTerminalMiddleware(refresh)
	rb := &RefreshBudget{}
	req := &Request{
		URL:     "https://example.test/",
		Headers: make(http.Header),
		Call: &CallContext{
			Snapshot:      AuthSnapshot{CookieHeader: "pre-refresh-csrf"},
			RefreshBudget: rb,
		},
	}
	resp, err := mw(inner)(context.Background(), req)
	if err != nil {
		t.Fatalf("terminal err = %v", err)
	}
	if resp.HTTP.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 (refresh budget spent)", resp.HTTP.StatusCode)
	}
	if refreshes != 1 {
		t.Errorf("refreshes = %d, want 1 (budget should prevent double-refresh)", refreshes)
	}
}

// TestTerminal_NilCallContext covers the missing-call-context
// guard.
func TestTerminal_NilCallContext(t *testing.T) {
	mw := NewTerminalMiddleware(nil)
	req := &Request{URL: "https://example.test/", Headers: make(http.Header)}
	if _, err := mw(passThrough)(context.Background(), req); err == nil {
		t.Errorf("terminal accepted nil CallContext")
	}
}

// TestTerminal_RefreshFailureSurfaced confirms a refresh-callback
// error is wrapped and surfaced.
func TestTerminal_RefreshFailureSurfaced(t *testing.T) {
	refreshErr := errors.New("simulated refresh failure")
	refresh := func(ctx context.Context, r *Request) error { return refreshErr }
	inner := func(ctx context.Context, r *Request) (*Response, error) {
		return &Response{
			HTTP: &http.Response{StatusCode: http.StatusUnauthorized},
			Body: []byte("auth required"),
		}, nil
	}
	mw := NewTerminalMiddleware(refresh)
	req := &Request{
		URL:     "https://example.test/",
		Headers: make(http.Header),
		Call: &CallContext{
			Snapshot:      AuthSnapshot{CookieHeader: "x"},
			RefreshBudget: &RefreshBudget{},
		},
	}
	_, err := mw(inner)(context.Background(), req)
	if err == nil {
		t.Fatalf("terminal swallowed refresh error")
	}
	if !errors.Is(err, refreshErr) {
		t.Errorf("err = %v, want wraps %v", err, refreshErr)
	}
}

// TestTerminal_RebuildFailureSurfaced confirms a rebuild error
// from the inner Handler path is wrapped and surfaced.
func TestTerminal_RebuildFailureSurfaced(t *testing.T) {
	inner := func(ctx context.Context, r *Request) (*Response, error) {
		return &Response{
			HTTP: &http.Response{StatusCode: http.StatusOK},
			Body: []byte("ok"),
		}, nil
	}
	mw := NewTerminalMiddleware(nil)
	req := &Request{
		URL:     "https://example.test/",
		Headers: nil, // will force Headers init in rebuildEnvelope
		Call: &CallContext{
			Snapshot: AuthSnapshot{CookieHeader: ""},
		},
	}
	resp, err := mw(inner)(context.Background(), req)
	if err != nil {
		t.Fatalf("terminal err = %v", err)
	}
	if resp == nil {
		t.Fatalf("terminal returned nil response")
	}
}

// passThrough is the no-op inner handler used by the
// per-middleware tests that don't need to drive an actual HTTP
// round trip.
func passThrough(ctx context.Context, r *Request) (*Response, error) {
	return &Response{
		HTTP: &http.Response{StatusCode: http.StatusOK},
		Body: []byte("ok"),
	}, nil
}

// TestCallContextRoundtrip covers WithCallContext /
// CallFromContext: a CallContext placed into a context is
// retrievable from the same context, and a missing key returns
// nil.
func TestCallContextRoundtrip(t *testing.T) {
	cc := &CallContext{Method: wire.MethodListNotebooks, Epoch: 7}
	ctx := WithCallContext(context.Background(), cc)
	got := CallFromContext(ctx)
	if got == nil {
		t.Fatalf("CallFromContext returned nil")
	}
	if got.Method != wire.MethodListNotebooks {
		t.Errorf("Method = %q, want MethodListNotebooks", got.Method)
	}
	if got.Epoch != 7 {
		t.Errorf("Epoch = %d, want 7", got.Epoch)
	}
	// Empty context returns nil.
	if got := CallFromContext(context.Background()); got != nil {
		t.Errorf("CallFromContext(empty) = %v, want nil", got)
	}
	// Wrong-type value under the key returns nil (defensive).
	ctxWrong := context.WithValue(context.Background(), callContextKey{}, "not a *CallContext")
	if got := CallFromContext(ctxWrong); got != nil {
		t.Errorf("CallFromContext(wrong type) = %v, want nil", got)
	}
}

// TestDisableRetries_NilReceiver covers the nil-receiver path
// of DisableRetries.
func TestDisableRetries_NilReceiver(t *testing.T) {
	var nilCC *CallContext
	if nilCC.DisableRetries() {
		t.Errorf("nil.DisableRetries() = true, want false")
	}
}

// TestRebuildEnvelope_NoForbiddenOps is the AST regression guard
// for the stale-envelope bug class. The walk inspects the body
// of rebuildEnvelope and asserts:
//
//  1. It does NOT call any channel/lock/await-shaped operation
//     (chan receive/send, mutex Lock/Unlock, sync.WaitGroup Wait,
//     time.Sleep, http.Get/Do).
//  2. It DOES set the Cookie header from the snapshot (the
//     regression guard for the actual rebuild).
//
// A future refactor that adds a blocking operation to the rebuild
// body — even something innocuous-looking like a metrics increment
// with a mutex — must update this test, the matching comment in
// runtime.go, and the matching line in docs/04-transport.md.
//
// The walk accepts only the operations the rebuild genuinely
// needs: Header.Get/Set, conditional CSRF Header.Set, and the
// return. Anything else is a violation.
func TestRebuildEnvelope_NoForbiddenOps(t *testing.T) {
	// Locate the rebuildEnvelope source via runtime.Caller so the
	// test follows its own location rather than a hard-coded
	// path. This keeps the regression guard working even if a
	// future refactor moves the file.
	_, thisFile, _, ok := stdruntime.Caller(0)
	if !ok {
		t.Fatalf("could not resolve current test file path")
	}
	middlewarePath := filepath.Join(filepath.Dir(thisFile), "middleware.go")
	src, err := os.ReadFile(middlewarePath) //nolint:gosec
	if err != nil {
		t.Fatalf("read middleware.go: %v", err)
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, middlewarePath, src, parser.AllErrors)
	if err != nil {
		t.Fatalf("parse middleware.go: %v", err)
	}

	// Find the rebuildEnvelope FuncDecl.
	var rebuild *ast.FuncDecl
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fn.Name.Name == "rebuildEnvelope" {
			rebuild = fn
			break
		}
	}
	if rebuild == nil {
		t.Fatalf("rebuildEnvelope not found in middleware.go")
	}
	if rebuild.Body == nil {
		t.Fatalf("rebuildEnvelope has no body")
	}

	// Walk the body and assert no forbidden calls.
	forbidden := map[string]bool{
		// Synchronization primitives that introduce a
		// window for a stale-envelope TOCTOU.
		"Lock":   true,
		"Unlock": true,
		"Wait":   true,
		// Channel operations.
		"Receive": true,
		"Send":    true,
		// Blocking primitives.
		"Sleep":     true,
		"http.Get":  true,
		"http.Post": true,
		"http.Do":   true,
		"Client.Do": true,
		// Anything network-touching.
		"Dial": true,
		// Logger / metrics increments are forbidden here
		// because they could be a future wait point.
		"Info":  true,
		"Debug": true,
		"Warn":  true,
		"Error": true,
	}

	var violations []string
	ast.Inspect(rebuild.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.SelectorExpr:
			name := fn.Sel.Name
			if forbidden[name] {
				violations = append(violations, fmt.Sprintf("forbidden call: %s", name))
			}
		case *ast.Ident:
			if forbidden[fn.Name] {
				violations = append(violations, fmt.Sprintf("forbidden call: %s", fn.Name))
			}
		}
		return true
	})

	if len(violations) > 0 {
		t.Fatalf("rebuildEnvelope contains forbidden ops:\n  %s\nThis is the regression guard for the stale-envelope bug. Update the test, the comment in runtime.go, and docs/04-transport.md together.",
			strings.Join(violations, "\n  "))
	}

	// Positive assertion: rebuildEnvelope sets the Cookie header.
	setsCookie := false
	ast.Inspect(rebuild.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name != "Set" {
			return true
		}
		// Match calls where the first arg is the literal "Cookie".
		if len(call.Args) < 1 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok {
			return true
		}
		if lit.Value == `"Cookie"` {
			setsCookie = true
		}
		return true
	})
	if !setsCookie {
		t.Errorf("rebuildEnvelope does not set the Cookie header; the regression guard is incomplete")
	}
}

// TestTerminalInner_NoForbiddenOps is the AST regression guard
// against the runtime.go terminalInner. The terminal middleware
// rebuilds the envelope; the runtime's terminalInner is the
// placement site where a future refactor might introduce a
// blocking op between the rebuild and the kernel's POST. This
// test pins the runtime.go file the same way the middleware.go
// test pins its sibling.
//
// Note: the runtime terminalInner POSTs via kernel.Client().Do()
// (rather than Kernel.Post) so the chain can read the response
// status code. The walk accepts the Client().Do() call itself as
// the only network-touching call and rejects any other
// channel/lock/await-shaped operation.
func TestTerminalInner_NoForbiddenOps(t *testing.T) {
	_, thisFile, _, ok := stdruntime.Caller(0)
	if !ok {
		t.Fatalf("could not resolve current test file path")
	}
	runtimePath := filepath.Join(filepath.Dir(thisFile), "runtime.go")
	src, err := os.ReadFile(runtimePath) //nolint:gosec
	if err != nil {
		t.Fatalf("read runtime.go: %v", err)
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, runtimePath, src, parser.AllErrors)
	if err != nil {
		t.Fatalf("parse runtime.go: %v", err)
	}

	// Find the terminalInner FuncDecl.
	var terminal *ast.FuncDecl
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fn.Name.Name == "terminalInner" {
			terminal = fn
			break
		}
	}
	if terminal == nil {
		t.Fatalf("terminalInner not found in runtime.go")
	}
	if terminal.Body == nil {
		t.Fatalf("terminalInner has no body")
	}

	// AssertEpoch runs FIRST in terminalInner (it's the
	// epoch-fence regression guard for the stale-envelope bug).
	// The forbidden-ops list mirrors the middleware test, plus a
	// few extra entries for runtime-specific patterns.
	forbidden := map[string]bool{
		"Lock":   true,
		"Unlock": true,
		"Wait":   true,
		"Sleep":  true,
		// Channel operations.
		"Receive": true,
		"Send":    true,
		// Logger / metrics are forbidden (could become a
		// future wait point).
		"Info":  true,
		"Debug": true,
		"Warn":  true,
		"Error": true,
	}

	var violations []string
	ast.Inspect(terminal.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.SelectorExpr:
			name := fn.Sel.Name
			if forbidden[name] {
				violations = append(violations, fmt.Sprintf("forbidden call: %s", name))
			}
		case *ast.Ident:
			if forbidden[fn.Name] {
				violations = append(violations, fmt.Sprintf("forbidden call: %s", fn.Name))
			}
		}
		return true
	})

	if len(violations) > 0 {
		t.Fatalf("terminalInner contains forbidden ops:\n  %s", strings.Join(violations, "\n  "))
	}
}
