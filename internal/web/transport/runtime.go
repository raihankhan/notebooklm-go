// Package transport — runtime.go.
//
// Runtime is the authed POST entry point. It owns:
//
//   - the loop-free epoch check (Kernel.AssertEpoch runs once per
//     logical call, not in a retry loop),
//   - the auth-snapshot capture (the immutable-at-issue-time view
//     the terminal middleware rebuilds the envelope from),
//   - the envelope materialization (URL + body + headers, built
//     from the captured snapshot once before the chain runs),
//   - the chain dispatch (the four middlewares in pinned order),
//   - and the kernel post itself.
//
// Runtime is the single integration point between the higher-level
// SDK / Executor and the four-middleware chain. The Executor
// builds a logical RPC; Runtime turns it into an HTTP POST.
//
// Boundary: per docs/AGENTS.md rule 5, this file imports stdlib +
// internal/runtime + internal/web/wire + internal/web/policy only.
// The Kernel is in the same package so it does not appear in the
// import list.
package transport

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/raihankhan/notebooklm-go/internal/runtime"
	"github.com/raihankhan/notebooklm-go/internal/web/policy"
	"github.com/raihankhan/notebooklm-go/internal/web/wire"
)

// RefreshFunc is the callback the Runtime invokes when the
// terminal middleware observes a 401 and the refresh budget
// permits a refresh. The function is expected to:
//
//   - run the auth-refresh ladder (L1 / L2.5 / L3 / L4),
//   - re-capture the auth snapshot into cc.Snapshot (the
//     terminal middleware will rebuild the envelope from it),
//   - return a typed error if refresh fails (the caller will
//     surface the error to the SDK).
type RefreshFunc func(ctx context.Context, cc *CallContext) error

// BuildFunc is the callback the Runtime invokes to materialize the
// request body. The function returns the encoded body bytes; the
// Runtime copies them into Request.Body before dispatch.
//
// The function is invoked exactly once per logical RPC, BEFORE the
// chain runs. Retries inside the chain reuse the same body; the
// terminal middleware rebuilds the credential-affine headers
// without re-running BuildFunc.
type BuildFunc func(ctx context.Context, cc *CallContext) ([]byte, error)

// Runtime is the authed POST entry. It owns the chain wiring and
// the kernel reference; it does not own the auth state (the auth
// snapshot lives in the call context per-RPC).
type Runtime struct {
	kernel *Kernel

	// policy is the idempotency registry the Executor consults
	// before building the call context. Stored on Runtime so the
	// chain middlewares do not have to import policy directly.
	policy *policy.Registry

	// onAuthRefresh is the RefreshFunc the terminal middleware
	// invokes on a wire-401. May be nil in test fixtures that
	// do not exercise the refresh path.
	onAuthRefresh RefreshFunc

	// onBuild is the BuildFunc the Executor invokes before the
	// chain runs. May be nil in test fixtures that supply a
	// pre-built body via the Request.
	onBuild BuildFunc
}

// NewRuntime returns a Runtime wiring the four middlewares against
// the given kernel and policy. The RefreshFunc and BuildFunc are
// optional; tests can pass nil for either.
func NewRuntime(k *Kernel, reg *policy.Registry, refresh RefreshFunc, build BuildFunc) *Runtime {
	return &Runtime{
		kernel:        k,
		policy:        reg,
		onAuthRefresh: refresh,
		onBuild:       build,
	}
}

// Chain constructs the four-middleware chain in the pinned order.
// The order is enforced by TestMiddleware_OrderPinned. The returned
// Handler is the one the Executor invokes; it threads the request
// through authed → idempotency → retry → terminal → inner.
//
// authed, idempotency, retry, terminal — outermost to innermost.
func (r *Runtime) Chain() Handler {
	// Build the chain inside-out so the wrapping reads left-to-right
	// from outermost to innermost, matching the documented order.
	inner := r.terminalInner()
	terminal := NewTerminalMiddleware(r.refreshCallback())(inner)
	retry := NewRetryMiddleware()(terminal)
	idempotency := NewIdempotencyMiddleware()(retry)
	authed := NewAuthedMiddleware()(idempotency)
	return authed
}

// terminalInner is the inner Handler the terminal middleware
// wraps. It actually POSTs against the request URL the executor
// built, returning the raw *http.Response so the terminal
// middleware can read its status code.
//
// We go through Kernel.Client() rather than Kernel.Post() because
// the chain's terminal middleware needs the *http.Response
// (status + headers) to make its 401 / 429 / 5xx decisions. We
// re-implement the AssertEpoch fence here so the regression
// guard for stale-envelope still applies.
func (r *Runtime) terminalInner() Handler {
	return func(ctx context.Context, req *Request) (*Response, error) {
		if r.kernel == nil {
			return nil, fmt.Errorf("transport: runtime: nil kernel")
		}
		cc := req.Call
		if cc == nil {
			return nil, fmt.Errorf("transport: runtime: missing CallContext")
		}
		if err := r.kernel.AssertEpoch(cc.Epoch); err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, req.URL, bytes.NewReader(req.Body))
		if err != nil {
			return nil, fmt.Errorf("transport: runtime: build request: %w", err)
		}
		httpReq.Header = req.Headers.Clone()
		if httpReq.Header.Get("Content-Type") == "" {
			httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		// Track the in-flight call against the kernel's WaitGroup so
		// Close drains it. StartInFlight is the canonical entry point;
		// we use it rather than reaching into the kernel's wg.
		_, endInFlight := r.kernel.StartInFlight()
		defer endInFlight()

		httpResp, err := r.kernel.Client().Do(httpReq)
		if err != nil {
			return nil, fmt.Errorf("transport: runtime: send: %w", err)
		}
		defer func() { _ = httpResp.Body.Close() }()

		// Read the body under the kernel's size cap. The chain
		// tests pass a body that fits comfortably; a too-large body
		// surfaces here as a typed TooLargeError.
		body, err := io.ReadAll(&cappedReader{
			src:   httpResp.Body,
			limit: r.kernel.maxBytes,
			read:  0,
		})
		if err != nil {
			return nil, err
		}
		return &Response{
			HTTP: httpResp,
			Body: body,
		}, nil
	}
}

// cappedReader wraps an io.Reader with a byte-count cap. When the
// cap is exceeded, it surfaces a *TooLargeError and stops reading.
// Mirrors the kernel's Post size cap without depending on the
// kernel's internal LimitedReader.
type cappedReader struct {
	src   io.Reader
	limit int64
	read  int64
}

func (r *cappedReader) Read(p []byte) (int, error) {
	if r.read >= r.limit {
		// Already at or past the cap; surface a typed error.
		return 0, &TooLargeError{Limit: r.limit, Got: r.read}
	}
	// Allow reading 1 byte past the cap to detect overflow.
	allowed := r.limit - r.read + 1
	if int64(len(p)) > allowed {
		p = p[:allowed]
	}
	n, err := r.src.Read(p)
	r.read += int64(n)
	if r.read > r.limit {
		return n, &TooLargeError{Limit: r.limit, Got: r.read}
	}
	return n, err
}

// refreshCallback wraps r.onAuthRefresh in the typed signature the
// terminal middleware expects.
func (r *Runtime) refreshCallback() func(ctx context.Context, r *Request) error {
	if r.onAuthRefresh == nil {
		return nil
	}
	return func(ctx context.Context, req *Request) error {
		return r.onAuthRefresh(ctx, req.Call)
	}
}

// IssueResult is the resolved outcome of one logical RPC.
type IssueResult struct {
	Body   []byte
	Header http.Header
}

// Issue is the authed POST entry point. The Executor calls Issue
// once per logical RPC; Issue builds the call context (epoch,
// snapshot, budgets), materializes the envelope, dispatches
// through the chain, and returns the body bytes the inner kernel
// produced.
//
// The function is the loop-free side of the chain: any retry
// decision is delegated to the retry middleware; any refresh
// decision is delegated to the terminal middleware. Issue itself
// runs the call once.
func (r *Runtime) Issue(
	ctx context.Context,
	method wire.Method,
	variant string,
	params any,
	host wire.Host,
	timeout time.Duration,
) (*IssueResult, error) {
	if r.kernel == nil {
		return nil, fmt.Errorf("transport: runtime: nil kernel")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Step 1: loop-free epoch check. The epoch is snapshotted
	// once at issue time; retries inside the chain reuse the
	// same epoch value because the kernel's AssertEpoch rejects
	// only retired generations, and a generation that is active
	// at issue is still active at every retry tick.
	epoch := r.kernel.Generation()

	// Step 2: classify the RPC against the policy registry.
	// A nil registry defaults to ClassSafe (no retries disabled).
	class := policy.ClassSafe
	if r.policy != nil {
		class = r.policy.Classify(method, variant)
	}

	// Step 3: mint a fresh refresh budget and a fresh deadline
	// budget. The T0 anchor for the budget is captured here,
	// not inside any retry loop, so post-refresh sleeps are
	// clamped to the remaining budget.
	rb := &RefreshBudget{}
	budget := runtime.New(ctx, timeout)

	// Step 4: capture the auth snapshot. A real client would
	// read the CSRF token from the auth tokens object and the
	// Cookie: header from the jar. We accept empty snapshots
	// in tests; production code wires this from internal/auth.
	//
	// This is intentionally a stub — the snapshot is filled in by
	// a future phase that wires internal/auth. The signature is
	// fixed so the wire-up is mechanical.
	snapshot := AuthSnapshot{
		CSRF:         "",
		CookieHeader: "",
	}

	cc := &CallContext{
		Method:        method,
		Variant:       variant,
		Snapshot:      snapshot,
		Epoch:         epoch,
		RefreshBudget: rb,
		Budget:        budget,
		Host:          host,
		Class:         class,
	}

	// Step 5: materialize the envelope (URL + body + headers).
	// The build func is supplied by the Executor; we fall back
	// to a no-op body when nil so test fixtures can pass a
	// pre-built Request.
	if r.onBuild != nil {
		body, err := r.onBuild(ctx, cc)
		if err != nil {
			return nil, fmt.Errorf("transport: runtime: build: %w", err)
		}
		cc.builtBody = body
	}

	url := wire.BatchexecuteURL(host)
	if url == "" {
		return nil, fmt.Errorf("transport: runtime: host %q is not on the allowlist", host)
	}

	req := &Request{
		URL:     url,
		Body:    cc.builtBody,
		Headers: make(http.Header),
		Call:    cc,
	}

	// Step 6: dispatch through the chain.
	chain := r.Chain()
	resp, err := chain(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, fmt.Errorf("transport: runtime: chain returned nil response without error")
	}
	return &IssueResult{Body: resp.Body, Header: resp.HTTP.Header}, nil
}
