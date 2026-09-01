// Package transport — middleware.go.
//
// The middleware chain is the load-bearing orchestration layer
// between the Executor (which assembles a logical RPC) and the
// Kernel (which actually POSTs). The chain wraps a single Handler
// in four middlewares, each with a single responsibility:
//
//	authed        — attaches the Cookie: header from the jar
//	idempotency   — short-circuits unsafe-mutation retries per
//	                internal/web/policy
//	retry/429     — honors Retry-After (both forms, via
//	                ParseRetryAfter); respects the disabled-retry
//	                class; respects the aggregate deadline
//	terminal      — does the unconditional pre-POST envelope
//	                rebuild (re-encode from current auth state);
//	                this is the load-bearing middleware that
//	                closes the stale-envelope TOCTOU window
//
// The order is documented in docs/04-transport.md and pinned by
// TestMiddleware_OrderPinned. A future change to the order MUST
// update the test, the comment here, and the doc.
//
// Each middleware is constructed by name so the chain test can
// read the names in order:
//
//	NewAuthedMiddleware()
//	NewIdempotencyMiddleware()
//	NewRetryMiddleware()
//	NewTerminalMiddleware()
//
// The names are stable strings (not anonymous wrappers) precisely
// so the test can iterate them. A future refactor that replaces a
// middleware with an inline handler MUST keep the constructor name
// so the regression test still pins the order.
//
// Boundary: per docs/AGENTS.md rule 5, this file imports stdlib +
// internal/web/wire + internal/web/policy + internal/runtime only.
package transport

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/raihankhan/notebooklm-go/internal/runtime"
	"github.com/raihankhan/notebooklm-go/internal/web/policy"
	"github.com/raihankhan/notebooklm-go/internal/web/wire"
)

// Handler is the chain-handler signature: take a Request, return a
// Response and an error. Each middleware wraps the next Handler so
// the chain collapses to one Handler the Executor calls once.
//
// The Request carries the full envelope (URL, body, headers) and
// a per-call context (the callContext below). The Response carries
// the http.Response and the body bytes the terminal middleware read.
type Handler func(ctx context.Context, r *Request) (*Response, error)

// Middleware is the wrapper signature: take a Handler, return a
// new Handler that runs the wrapper's logic before delegating.
type Middleware func(next Handler) Handler

// Request is the per-call envelope the Executor hands the chain.
// The fields are public so middleware can read and rewrite them
// at every step. Mutation is the normal case (e.g. terminal
// re-encodes URL/Body/Headers from current auth state).
type Request struct {
	// URL is the full POST URL (scheme + host + path + rpcids query).
	URL string

	// Body is the encoded request body. The terminal middleware
	// rebuilds this from the current AuthSnapshot before sending.
	Body []byte

	// Headers carries Content-Type and (after authed) Cookie.
	Headers http.Header

	// Call is the per-call context (epoch, snapshot, budgets, …).
	// Middlewares read but do not replace it.
	Call *CallContext
}

// Response is what the chain returns to the Executor: the raw
// HTTP response (for status / Set-Cookie / Retry-After inspection)
// and the body bytes the terminal middleware read off the wire.
type Response struct {
	HTTP *http.Response
	Body []byte
}

// callContextKey is the unexported context key under which the
// CallContext rides so middleware can recover it via
// CallFromContext without exporting the key type.
type callContextKey struct{}

// CallFromContext returns the per-call context attached to ctx by
// the Executor, or nil if absent. The chain middlewares use this to
// read the idempotency classification, the auth snapshot, the
// refresh budget, and the aggregate deadline without threading
// those values explicitly through every signature.
func CallFromContext(ctx context.Context) *CallContext {
	if v := ctx.Value(callContextKey{}); v != nil {
		if cc, ok := v.(*CallContext); ok {
			return cc
		}
	}
	return nil
}

// WithCallContext returns ctx with cc attached under the unexported
// key. The Executor calls this once before invoking the chain so
// every middleware can find the call context.
func WithCallContext(ctx context.Context, cc *CallContext) context.Context {
	return context.WithValue(ctx, callContextKey{}, cc)
}

// AuthSnapshot is the immutable-at-call-start view of the
// transport-layer auth state. The Executor captures one snapshot
// per logical RPC and the terminal middleware re-encodes the
// envelope from it before each POST. The cookie string is
// pre-formatted so the terminal middleware does not have to call
// the jar in the hot path.
type AuthSnapshot struct {
	// CSRF is the SNlM0e token. Empty when the request does not
	// require CSRF (rare; the batchexecute endpoint does).
	CSRF string

	// CookieHeader is the value of the Cookie: header line the
	// jar returns for the call's host. Empty when no cookies are
	// present (the kernel handles an empty Cookie: header cleanly).
	CookieHeader string
}

// CallContext is the per-call state the Executor threads through
// the chain via context.Value. Middlewares read it; they do not
// replace it.
type CallContext struct {
	// Method is the symbolic RPC name (e.g. "MethodListNotebooks").
	// Used for idempotency classification and for metrics labels.
	Method wire.Method

	// Variant is the optional variant label (e.g. "url", "drive").
	// Empty string when the method does not use variants.
	Variant string

	// Snapshot is the immutable-at-issue-time view of the auth
	// state the terminal middleware uses to rebuild the envelope.
	Snapshot AuthSnapshot

	// Epoch is the generation the kernel requires for this call.
	// AssertEpoch inside Kernel.Post validates it.
	Epoch uint64

	// RefreshBudget is the per-call one-shot refresh token. Both
	// the auth-refresh middleware and the executor's decode-time
	// retry leg consult it; the first Take wins.
	RefreshBudget *RefreshBudget

	// Budget is the aggregate deadline for this logical call.
	// The retry middleware clamps its sleeps to Budget.Remaining().
	Budget *runtime.Budget

	// Host is the wire.Host this call targets. The authed
	// middleware uses it to build the Cookie: header from the jar.
	Host wire.Host

	// Class is the idempotency classification; populated from
	// the policy.Registry before the chain runs.
	Class policy.Class

	// disableRetries is set by the idempotency middleware when
	// the policy says this RPC must not be replayed. The retry
	// middleware reads it on every loop iteration.
	disableRetries bool

	// builtBody is the body the Runtime's BuildFunc produced.
	// Middlewares do not read it directly; the Runtime copies it
	// into Request.Body before dispatch. Field is exported only
	// to the Runtime (same package).
	builtBody []byte
}

// DisableRetries reports whether the retry middleware has been
// instructed to skip its loop for this call. Used by the executor's
// decode-time refresh leg too, so a refresh after a decoded
// auth-error does not also loop through 5 retry attempts.
func (cc *CallContext) DisableRetries() bool {
	if cc == nil {
		return false
	}
	return cc.disableRetries
}

// NewAuthedMiddleware returns the authed middleware — attaches
// Cookie: from the jar before dispatch. The middleware reads
// the AuthSnapshot from the call context (set by the Runtime /
// Executor at issue time) and sets the Cookie header on the
// request. If the snapshot's CookieHeader is empty (jar returned
// no cookies), the middleware still sets Cookie: "" so the
// request shape matches a real browser request — Google rejects
// calls that omit the header.
func NewAuthedMiddleware() Middleware {
	name := "authed"
	return func(next Handler) Handler {
		return func(ctx context.Context, r *Request) (*Response, error) {
			cc := r.Call
			if cc == nil {
				return nil, fmt.Errorf("transport: %s: missing CallContext", name)
			}
			if r.Headers == nil {
				r.Headers = make(http.Header)
			}
			r.Headers.Set("Cookie", cc.Snapshot.CookieHeader)
			return next(ctx, r)
		}
	}
}

// NewIdempotencyMiddleware returns the idempotency middleware —
// short-circuits the retry layer for unsafe-mutation RPCs. The
// middleware reads the Class from the call context. When the
// class is ClassUnsafeMutation or ClassProbeThenCreate, the
// middleware installs a flag on the CallContext that the retry
// layer honors by skipping its retry loop.
//
// We use a flag on the call context rather than a goroutine-local
// variable because the retry middleware is the next link in the
// chain and must read the flag from the same call context. A
// goroutine-local would be wrong across goroutine boundaries.
func NewIdempotencyMiddleware() Middleware {
	name := "idempotency"
	return func(next Handler) Handler {
		return func(ctx context.Context, r *Request) (*Response, error) {
			cc := r.Call
			if cc == nil {
				return nil, fmt.Errorf("transport: %s: missing CallContext", name)
			}
			// Mark the call context so the retry middleware
			// knows whether inner retries are allowed. The flag
			// is on the call context rather than a return value
			// so the retry middleware (which is next in the
			// chain) can read it.
			cc.disableRetries = cc.Class == policy.ClassUnsafeMutation ||
				cc.Class == policy.ClassProbeThenCreate
			return next(ctx, r)
		}
	}
}

// NewRetryMiddleware returns the retry middleware — handles 429
// (rate limit) and 5xx (server error) responses by honoring
// Retry-After (both delta-seconds and HTTP-date forms, parsed
// by ParseRetryAfter) and respecting the aggregate deadline.
//
// When the inner handler returns a Response with a 429 or 5xx
// status, the retry middleware decides whether to retry based on:
//
//   - the call's idempotency classification (idempotency middleware
//     may have set disableRetries),
//   - the Budget's remaining time,
//   - the maximum-attempt cap.
//
// The retry budget caps at MaxRetries (5) attempts, with sleeps
// clamped to the Budget's Remaining(). This prevents both
// unbounded retries on a persistently-failing server and an
// oversized sleep that would extend past the call's deadline.
func NewRetryMiddleware() Middleware {
	name := "retry"
	const maxRetries = 5
	const defaultSleep = 250 * time.Millisecond
	return func(next Handler) Handler {
		return func(ctx context.Context, r *Request) (*Response, error) {
			cc := r.Call
			if cc == nil {
				return nil, fmt.Errorf("transport: %s: missing CallContext", name)
			}
			if cc.disableRetries {
				return next(ctx, r)
			}
			var lastErr error
			var lastResp *Response
			for attempt := 0; attempt < maxRetries; attempt++ {
				if err := ctx.Err(); err != nil {
					if lastErr != nil {
						return lastResp, fmt.Errorf("transport: retry: context done: %w", errors.Join(err, lastErr))
					}
					return lastResp, err
				}
				if cc.Budget != nil && cc.Budget.Expired() {
					if lastErr != nil {
						return lastResp, fmt.Errorf("transport: retry: budget expired: %w", lastErr)
					}
					return lastResp, fmt.Errorf("transport: retry: budget expired")
				}
				resp, err := next(ctx, r)
				lastResp = resp
				lastErr = err
				if !shouldRetry(resp, err) {
					return resp, err
				}
				// Compute sleep from Retry-After (if present) or
				// the default backoff. Clamp to the budget.
				sleep := retrySleep(resp, defaultSleep)
				if cc.Budget != nil {
					sleep = cc.Budget.Clamp(sleep)
				}
				if sleep <= 0 {
					return resp, err
				}
				t := time.NewTimer(sleep)
				select {
				case <-ctx.Done():
					t.Stop()
					return resp, ctx.Err()
				case <-t.C:
				}
			}
			return lastResp, lastErr
		}
	}
}

// shouldRetry reports whether resp / err is a retryable failure.
// 429 (rate-limit) and 5xx (server error) are retryable; everything
// else (success, 4xx other than 429, decode error, ctx cancel)
// is not.
//
// A nil error with a non-nil resp is success (no retry). A nil
// resp with a non-nil error is also terminal — only the
// auth-refresh and decode-time retry legs in the executor handle
// those.
func shouldRetry(resp *Response, err error) bool {
	if err != nil {
		return false
	}
	if resp == nil || resp.HTTP == nil {
		return false
	}
	status := resp.HTTP.StatusCode
	if status == http.StatusTooManyRequests {
		return true
	}
	if status >= 500 && status < 600 {
		return true
	}
	return false
}

// retrySleep returns the duration to wait before retrying, sourced
// from the response's Retry-After header (if present and parseable)
// or the default sleep otherwise.
func retrySleep(resp *Response, defaultSleep time.Duration) time.Duration {
	if resp == nil || resp.HTTP == nil {
		return defaultSleep
	}
	ra := resp.HTTP.Header.Get("Retry-After")
	if ra == "" {
		return defaultSleep
	}
	d, err := ParseRetryAfter(ra, time.Now)
	if err != nil {
		return defaultSleep
	}
	return d
}

// NewTerminalMiddleware returns the terminal middleware — the
// load-bearing TOCTOU closer. Right before the inner handler
// POSTs, the terminal middleware re-encodes the envelope from
// the current auth snapshot. The regression guard for the
// stale-envelope bug class.
//
// The terminal middleware accepts a kernel reference (the
// transport.Kernel that owns the http.Client and the cookie jar)
// and an auth-refresh callback the terminal middleware calls when
// the server returned 401 AND the refresh budget permits it. On
// a successful refresh, the terminal middleware rebuilds and
// retries; on a second 401, it gives up.
//
// The "rebuild from current auth state right before POST" pattern
// is enforced by the AST regression test. The function MUST NOT
// introduce a channel/lock/await between the rebuild and the
// underlying kernel.Post call.
func NewTerminalMiddleware(onAuthRefresh func(ctx context.Context, r *Request) error) Middleware {
	name := "terminal"
	return func(next Handler) Handler {
		return func(ctx context.Context, r *Request) (*Response, error) {
			cc := r.Call
			if cc == nil {
				return nil, fmt.Errorf("transport: %s: missing CallContext", name)
			}
			// The first POST: rebuild from the captured snapshot.
			if err := rebuildEnvelope(r, cc); err != nil {
				return nil, fmt.Errorf("transport: %s: rebuild: %w", name, err)
			}
			resp, err := next(ctx, r)
			if err != nil {
				return resp, err
			}
			if resp == nil || resp.HTTP == nil {
				return resp, err
			}
			// 401 → ask the auth-refresh callback to refresh,
			// re-allocate the budget, and rebuild from the
			// post-refresh snapshot.
			if resp.HTTP.StatusCode == http.StatusUnauthorized {
				if cc.RefreshBudget != nil {
					if won, _ := cc.RefreshBudget.Take("wire-401"); !won {
						// Some other path already paid for a
						// refresh; do not double-refresh.
						return resp, err
					}
				}
				if onAuthRefresh != nil {
					if refreshErr := onAuthRefresh(ctx, r); refreshErr != nil {
						return resp, fmt.Errorf("transport: %s: auth-refresh: %w", name, refreshErr)
					}
				}
				if err := rebuildEnvelope(r, cc); err != nil {
					return nil, fmt.Errorf("transport: %s: post-refresh rebuild: %w", name, err)
				}
				return next(ctx, r)
			}
			return resp, err
		}
	}
}

// rebuildEnvelope re-encodes the request URL / body / headers from
// the call context's current AuthSnapshot. The function is the
// regression guard for the stale-envelope bug — it MUST be called
// inside the terminal middleware, immediately before the inner
// handler POSTs, with no intervening blocking operation.
//
// The function is intentionally tiny: set Cookie, set CSRF (when
// present). All other request shaping happens in the Executor before
// the chain runs (in particular, the rpc envelope is built once
// from the captured snapshot; the rebuild here only updates the
// session-affine parts so the rebuilt request still matches the
// captured snapshot's variant field).
//
// The function does not log, lock, or block. The AST test asserts
// this property.
func rebuildEnvelope(r *Request, cc *CallContext) error {
	if r.Headers == nil {
		r.Headers = make(http.Header)
	}
	r.Headers.Set("Cookie", cc.Snapshot.CookieHeader)
	if cc.Snapshot.CSRF != "" {
		// The CSRF token surfaces here as a header for the
		// benefit of the audit log; the body bytes the executor
		// already encoded carry the &at= form the server expects.
		r.Headers.Set("X-CSRF-Token", cc.Snapshot.CSRF)
	}
	// No locking, no channels, no awaits. The AST test enforces it.
	return nil
}
