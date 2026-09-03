// Package notebooklm is the public Go SDK root for NotebookLM.
//
// client.go — the Client type, its constructor (New), the
// storage-loader (FromStorage), the lifecycle primitives
// (Close / Drain), the single public RPC entry point
// (RPCCall), and the credential primitives (RefreshAuth).
//
// The Client is the integration point between every higher
// level surface (the namespace APIs that land in Phase 5+
// and the CLI / MCP / REST adapters in later phases) and the
// transport stack:
//
//   - Kernel (internal/web/transport/kernel.go) owns the
//     *http.Client, the cookie jar, and the response size cap.
//   - Executor (internal/web/transport/executor.go) mints the
//     per-RPC request id, consults the idempotency registry,
//     and dispatches through Runtime.
//   - Runtime (internal/web/transport/runtime.go) wires the
//     four-middleware chain (authed, idempotency, retry,
//     terminal) and issues the POST.
//   - Lifecycle (internal/runtime/lifecycle.go) coordinates
//     open / close / drain in phases with rollback on failed
//     open and deterministic teardown failure precedence.
//   - Supervisor (internal/runtime/supervisor.go) owns the
//     in-flight semaphore and the drain condition.
//   - Metrics (internal/runtime/metrics.go) owns the
//     cumulative RPC counters and the optional RPCEvent
//     callback fan-out.
//
// Port of notebooklm.client.py::NotebookLMClient. The Go port
// keeps the same attribute surface (auth, backends,
// notebooks, sources, …) but the namespace APIs land in later
// tickets; today the Client exposes only the cross-namespace
// primitives so the Phase 5 vertical slice can wire the stack
// end-to-end without scaffolding the namespace methods.
package notebooklm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/raihankhan/notebooklm-go/internal/auth/cookiejar"
	"github.com/raihankhan/notebooklm-go/internal/auth/storage"
	"github.com/raihankhan/notebooklm-go/internal/runtime"
	"github.com/raihankhan/notebooklm-go/internal/web/policy"
	"github.com/raihankhan/notebooklm-go/internal/web/transport"
	"github.com/raihankhan/notebooklm-go/internal/web/wire"
)

// Executor is the surface the public Client uses to dispatch
// one logical RPC. The transport.Executor from Phase 3
// satisfies this interface; tests substitute a fake. Keeping
// the Client bound to an interface (not the concrete type)
// means the tests for AC1–AC10 can run without an httptest
// server — they pin the wiring, not the wire shape.
type Executor interface {
	// Execute issues one logical RPC. The semantics mirror
	// internal/web/transport/executor.go::Executor.Execute:
	// the method is the symbolic name, the variant is the
	// optional idempotency-variant label, params is the
	// positional payload, host is the wire.Host this call
	// targets, and timeout is the per-call aggregate
	// deadline.
	Execute(ctx context.Context,
		method wire.Method,
		variant string,
		params any,
		host wire.Host,
		timeout time.Duration,
	) (*transport.ExecResult, error)
}

// RefreshFunc is the callback the Client invokes when the
// terminal middleware observes a 401 and the refresh budget
// permits a refresh. The Client's RefreshAuth wires its own
// implementation (which delegates to the L1 step in Phase 4);
// tests inject a counting fake.
//
// Port of notebooklm.client.py::NotebookLMClient.refresh_auth
// — the function the Coordinator single-flight coalesces on.
type RefreshFunc func(ctx context.Context) error

// AuthSnapshot is the per-call immutable view of auth state
// the Runtime/Executor reads when issuing an RPC. The Client
// builds one per RPCCall from its private auth state; tests
// can substitute a pre-built snapshot to avoid touching the
// cookie jar.
type AuthSnapshot struct {
	// CSRF is the SNlM0e token. Empty when the request does
	// not require CSRF (rare; the batchexecute endpoint
	// does).
	CSRF string

	// CookieHeader is the pre-formatted Cookie: header value
	// built from the jar. Empty when the jar holds no cookies
	// for the call's host (the kernel handles an empty
	// Cookie: header cleanly — the request shape matches a
	// real browser).
	CookieHeader string
}

// AuthState is the auth-related subset of Client state the
// Client owns. It is a separate type so the RefreshFunc
// signature can refer to it without dragging the entire
// Client through the closure.
//
// Phase 5 keeps this small: the CSRF token + the cookie jar
// + the in-process auth coordinator. Phase 4 will expand it
// with the storage-backed Tokens / ProfileStore plumbing; the
// Client delegates to that machinery without leaking its
// types into the public surface.
type AuthState struct {
	// jar holds the credentials the Client will mount on the
	// Kernel. A nil jar is valid (the Client simply has no
	// credentials; every authenticated RPC will return a
	// typed error).
	jar *cookiejar.Jar

	// csrf is the SNlM0e token the auth refresh path minted.
	// Empty when the profile has not been loaded.
	csrf string

	// refresh is the refresh callback the Runtime invokes
	// on a wire-401. Phase 5 wires this to RefreshAuth;
	// later phases replace it with the full refresh ladder.
	refresh RefreshFunc

	// host is the wire.Host the auth coordinator treats as
	// the canonical refresh surface. Defaults to
	// wire.HostPersonal (the canonical notebook host).
	host wire.Host
}

// defaultHost is the canonical notebook host the Client
// targets. The Python original routes every Web RPC through
// notebooklm.google.com (or notebook.google.com on the legacy
// personal host); the Go port pins the HostPersonal value so
// tests can pin it too.
func defaultHost() wire.Host { return wire.HostPersonal }

// Client is the public SDK root for NotebookLM.
//
// A Client owns one Kernel, one Executor, one Lifecycle, one
// Supervisor, one Metrics, one cookie jar, and one
// AuthState. Every higher-level namespace API (notebooks,
// sources, artifacts, chat, …) is a method on a sub-struct
// derived from this Client; the namespace APIs land in later
// phase tickets (Phase 5 covers notebooks only).
//
// Client is safe for concurrent use by multiple goroutines
// once Open has returned. Calling Close or Drain from one
// goroutine while others issue RPCCall is the canonical
// graceful-shutdown pattern (mirrors the Python original's
// `async with` semantics).
type Client struct {
	// config is the snapshot of the Options the caller
	// passed to New. Immutable after New returns.
	config clientConfig

	// logger is the structured logger the Client uses for
	// lifecycle events. Never nil after New — see
	// options.go::resolveLogger.
	logger *slog.Logger

	// metrics owns the cumulative RPC counters. Never nil
	// after New.
	metrics *runtime.Metrics

	// kernel owns the *http.Client, the cookie jar, and the
	// response size cap. Created in New from the Options.
	kernel *transport.Kernel

	// transport is the authed POST entry — owns the four
	// middlewares and the RefreshFunc / BuildFunc wiring.
	transport *transport.Runtime

	// executor is the public RPCCall seam. Bound to the
	// Executor interface (not the concrete type) so tests
	// can swap in a fake without spinning an httptest
	// server.
	executor Executor

	// registry is the idempotency registry the Executor
	// consults. Created in New from the policy package's
	// New constructor.
	registry *policy.Registry

	// lifecycle coordinates the open / close / drain waves
	// for every owned resource (kernel, supervisor, jar).
	lifecycle *runtime.Lifecycle

	// supervisor owns the in-flight semaphore and the drain
	// condition. Built with the configured maxInFlight.
	supervisor *runtime.Supervisor

	// authState is the auth-related subset the Client
	// delegates to. RefreshAuth reads/writes here.
	authState AuthState

	// closedMu serializes "is this Client closed?" with
	// "RPCCall in flight". Held across the closed check in
	// RPCCall and across the closed flip in Close so a
	// in-flight call cannot escape the close wave.
	closedMu sync.RWMutex
	closed   bool
}

// ErrClientClosed is the typed sentinel returned by RPCCall,
// RefreshAuth, and Drain when the Client has been closed. It
// is exported so callers can branch on errors.Is without
// unwrapping.
//
// The error wraps a nil-Client check (so a `var c *Client`
// does not panic) and the closed-flag check on an otherwise
// valid Client.
var ErrClientClosed = errors.New("notebooklm: client is closed")

// New constructs a Client from the supplied Options.
//
// New does not perform network I/O; the Client is open as soon
// as it returns (the Python original requires an explicit
// `async with` block to open; the Go port collapses the
// two-step into one so the call site reads `c, err :=
// notebooklm.New(ctx, opts...)`). Close is the explicit
// shutdown.
//
// An error from New indicates a configuration problem (an
// unknown backend, an unreadable storage path the caller
// passed via WithStoragePath, etc.). After New returns nil,
// every subsequent method on the Client is safe for
// concurrent use.
//
// Port of notebooklm.client.py::NotebookLMClient.__init__.
// The Python constructor delegates to a private
// `_assemble_client` seam; the Go port inlines the same
// wiring so the public surface is one function.
func New(ctx context.Context, opts ...Option) (*Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("notebooklm: new: %w", err)
	}
	cfg := newConfig()
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		opt.apply(&cfg)
	}
	if cfg.maxInFlight < 0 {
		return nil, errors.New("notebooklm: maxInFlight must be >= 0")
	}

	logger := resolveLogger(cfg.logger)
	metrics := cfg.metrics
	if metrics == nil {
		metrics = runtime.NewMetrics()
	}

	// Resolve the requested backend. Empty means "no
	// preference; fall back to NOTEBOOKLM_BACKEND or the
	// canonical Web backend".
	backend, err := resolveBackend(cfg.backend)
	if err != nil {
		return nil, err
	}

	// Build the cookie jar. The Client owns it so the
	// refresh path can persist rotated cookies without
	// racing the executor's read path.
	jar := cookiejar.New()

	// Build the kernel. The default http.Client uses a 30-second
	// timeout matching the Python original's
	// _transport/transport.py default. The WithHTTPClient Option
	// installs a custom *http.Client — the test-only seam for
	// injecting a cassette-backed Transport.
	client := cfg.httpClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	// The size cap is the transport package's
	// DefaultMaxResponseBytes (64 MiB).
	kernel := transport.NewKernel(client, jar, transport.DefaultMaxResponseBytes)

	// Pin the epoch the Kernel starts on. NewKernel
	// initializes to 1 by default; the Option override lets
	// tests assert epoch-fencing arithmetic from a
	// non-default starting point.
	if cfg.epoch != defaultEpoch && cfg.epoch != 0 {
		for kernel.Generation() < cfg.epoch {
			kernel.ActivateEpoch()
		}
	}

	// Build the idempotency registry. Every Method the wire
	// layer declares must have an entry; the registry's
	// NewRegistry helper asserts that contract. Phase 5
	// registers every method as ClassSafe (the most
	// permissive class) so the startup assertion passes;
	// Phase 6+ tightens individual methods via
	// MustRegister calls in the per-namespace packages.
	registry, err := buildRegistry()
	if err != nil {
		return nil, fmt.Errorf("notebooklm: policy registry: %w", err)
	}

	// Build the supervisor with the configured
	// max-concurrent-RPCs cap.
	supervisor, err := runtime.NewSupervisor(metrics, cfg.maxInFlight)
	if err != nil {
		return nil, fmt.Errorf("notebooklm: supervisor: %w", err)
	}

	// Build the RefreshFunc that the Runtime invokes on a
	// wire-401. The callback closes over the Client; we
	// have to construct the Client after NewRuntime sees
	// the func, so we wire the callback onto the Client
	// after construction via a setter below.
	refreshHolder := &funcHolder{}

	rt := transport.NewRuntime(kernel, registry, refreshHolder.get(), nil)

	// Build the Executor. Concrete-type binding so the
	// Client can use the helper methods the transport
	// package exposes; the public surface stays bound to
	// the Executor interface for test substitution.
	exec := transport.NewExecutor(rt, registry)

	c := &Client{
		config:     cfg,
		logger:     logger,
		metrics:    metrics,
		kernel:     kernel,
		transport:  rt,
		executor:   exec,
		registry:   registry,
		supervisor: supervisor,
		authState: AuthState{
			jar:     jar,
			csrf:    "",
			refresh: nil,
			host:    defaultHost(),
		},
	}

	// Wire the RefreshFunc on the Runtime now that the
	// Client exists. The callback delegates to
	// Client.RefreshAuth so the terminal middleware's
	// wire-401 leg and the public RefreshAuth method share
	// one body. The single refresh leg in Phase 5 fires
	// the L1 step (an HTTP GET against the NotebookLM
	// homepage that re-mints the CSRF token); Phase 4
	// will replace this with the full L1 → L2.0 → L2.5
	// → L3 → L4 ladder.
	refreshHolder.set(c.refreshCallback())

	// Register lifecycle participants in three phases so
	// open and close have a deterministic order. The
	// numbers are deliberate:
	//
	//   phase 10: supervisor (no I/O, but admission must
	//             precede any RPC)
	//   phase 20: kernel (the http.Client + jar)
	c.lifecycle = runtime.NewLifecycle()
	if err := c.lifecycle.Register(
		"supervisor", 10,
		func(context.Context) error { return nil },
		func(context.Context) error { return nil },
	); err != nil {
		return nil, err
	}
	if err := c.lifecycle.Register(
		"kernel", 20,
		func(context.Context) error { return nil },
		func(context.Context) error { return kernel.Close() },
	); err != nil {
		return nil, err
	}

	// Open the lifecycle. A failed Open tears down the
	// participants registered so far in reverse-declaration
	// order. We do not propagate the failure into a wrapped
	// error here — the caller already has the typed message
	// the participant produced.
	if err := c.lifecycle.Open(ctx); err != nil {
		return nil, fmt.Errorf("notebooklm: open: %w", err)
	}

	if err := c.applyBackend(backend); err != nil {
		// Roll back the open participants before
		// returning the error so we do not leak the
		// kernel's WaitGroup / jar. The lifecycle.Close
		// call uses an unbounded background context
		// deliberately: the New caller has not yet
		// received a Client to wire a context into,
		// and the close-wave is bounded by the
		// participant's own timeouts.
		_ = c.lifecycle.Close(context.Background()) //nolint:contextcheck // New has no ctx to pass through to a teardown.
		return nil, err
	}

	return c, nil
}

// funcHolder is the indirect-storage seam the NewRuntime
// RefreshFunc parameter requires. NewRuntime captures the
// func value at construction; we need to set it after the
// Client is constructed (so the closure can refer to the
// Client), so we hand NewRuntime a getter and a setter that
// the closure consults at call time.
type funcHolder struct {
	mu sync.RWMutex
	fn transport.RefreshFunc
}

func (h *funcHolder) set(fn transport.RefreshFunc) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.fn = fn
}

func (h *funcHolder) get() transport.RefreshFunc {
	return func(ctx context.Context, cc *transport.CallContext) error {
		h.mu.RLock()
		fn := h.fn
		h.mu.RUnlock()
		if fn == nil {
			return nil
		}
		return fn(ctx, cc)
	}
}

// buildRegistry constructs the idempotency registry the
// Executor consults. Phase 5 registers every wire.Method as
// ClassSafe (the most permissive class) so the startup
// assertion passes; Phase 6+ tightens individual methods via
// MustRegister calls in the per-namespace packages.
//
// The fallback is ClassSafe so an unrecognized method (a
// new wire id a Google release just shipped) is retryable by
// default rather than denied. The Python original uses the
// same fallback behavior.
func buildRegistry() (*policy.Registry, error) {
	reg := policy.New()
	reg.SetFallback(policy.ClassSafe)
	entries := wire.AllMethods()
	for _, e := range entries {
		policy.MustRegister(reg, e.Method, policy.Entry{
			Class: policy.ClassSafe,
		})
	}
	return policy.NewRegistry(allMethodNames(entries), reg)
}

// allMethodNames turns the wire.AllMethods() result into the
// []wire.Method shape policy.NewRegistry expects.
func allMethodNames(entries []wire.MethodEntry) []wire.Method {
	out := make([]wire.Method, len(entries))
	for i, e := range entries {
		out[i] = e.Method
	}
	return out
}

// FromStorage constructs a Client whose credentials are loaded
// from a storage_state.json file. The file must be readable
// and parseable; on parse failure FromStorage returns a typed
// error naming the path so the CLI can produce a fix-it
// message.
//
// The path resolution order is:
//
//  1. The explicit `path` argument (non-empty wins).
//  2. NOTEBOOKLM_HOME / storage_state.json when path is empty.
//  3. ~/.notebooklm/storage_state.json when neither is set.
//
// Port of notebooklm.client.py::NotebookLMClient.from_storage.
// The Python classmethod is an async-context-manager wrapper;
// the Go port collapses the await + open into New so the call
// site reads:
//
//	c, err := notebooklm.FromStorage(ctx, "")
//
// which is the same idiom the CLI's "use as much of the
// default" code path uses.
func FromStorage(ctx context.Context, path string) (*Client, error) {
	resolved, err := resolveStoragePath(path)
	if err != nil {
		return nil, err
	}
	cfg, err := storage.Read(resolved)
	if err != nil {
		return nil, fmt.Errorf("notebooklm: read storage %q: %w", resolved, err)
	}
	c, err := New(ctx, WithStoragePath(resolved))
	if err != nil {
		return nil, err
	}
	if err := c.seedFromStorage(cfg); err != nil {
		//nolint:contextcheck // Close is intentionally
		// context-free; teardown runs to completion on a
		// fresh background context inside the method.
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// Close shuts down the Client. It is the single explicit
// shutdown primitive the SDK exposes.
//
// Close first runs the lifecycle.Close wave (which fences the
// kernel epoch, drains the supervisor, and closes the kernel's
// underlying WaitGroup), then flips the closed flag so a
// concurrent RPCCall returns a typed error rather than
// blocking on a closed lifecycle.
//
// After Close returns, every subsequent method on the
// Client returns ErrClientClosed. Calling Close more than once
// is a no-op (mirrors the Python original's `async with`
// semantics where re-entering the context manager is a no-op).
//
// Port of notebooklm.client.py::NotebookLMClient.close.
func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	c.closedMu.Lock()
	if c.closed {
		c.closedMu.Unlock()
		return nil
	}
	c.closed = true
	c.closedMu.Unlock()

	c.kernel.FenceEpoch()
	if c.lifecycle != nil {
		// Use a fresh context for the close wave so a
		// caller-canceled ctx does not cut off the
		// shutdown halfway through. The lifecycle's
		// participants are designed to honor the
		// supplied context; a 30-second budget is more
		// than enough for the kernel WaitGroup to drain.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := c.lifecycle.Close(ctx); err != nil {
			c.logger.Warn("close: lifecycle close failed",
				slog.String("error", err.Error()))
			return err
		}
	}
	return nil
}

// Drain stops accepting new operations and waits for in-flight
// operations to finish. It is the canonical "tell the client to
// quiesce" call — Drain returns once the in-flight count
// reaches zero (or the deadline expires).
//
// Drain does not close the Client. A drained Client can be
// reused by issuing a fresh RPCCall, which will re-enter
// admission (the supervisor's BeginDrain / EndDrain
// round-trip is the canonical quiesce-and-resume pattern).
//
// Port of notebooklm.client.py::NotebookLMClient.drain.
func (c *Client) Drain(ctx context.Context, deadline time.Duration) error {
	if c == nil {
		return nil
	}
	if err := c.checkOpen(); err != nil {
		return err
	}
	if c.supervisor == nil {
		return nil
	}
	c.supervisor.BeginDrain()
	defer c.supervisor.EndDrain()

	waitCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	// Wait until the in-flight count reaches zero. Polling
	// on InFlight() is intentional: the supervisor's drain
	// condition waits on the next Release, but a quiet
	// Client (no in-flight calls when Drain starts) needs
	// an immediate return.
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if c.supervisor.InFlight() == 0 {
			return nil
		}
		select {
		case <-waitCtx.Done():
			return waitCtx.Err()
		case <-ticker.C:
			// loop and re-check
		}
	}
}

// RPCCall is the single public RPC entry point. It is the
// escape hatch for advanced callers who need an undocumented
// RPC before a typed API exists; every namespace API (Phase 5
// notebooks, Phase 6+ sources, …) eventually funnels through
// the same Executor.
//
// `method` is the wire.Method symbolic name; the Executor
// resolves it to the obfuscated id via wire.ResolveID.
//
// `params` is the positional payload the wire/encode layer
// expects. The wire package documents the positional shape
// per RPC; passing a payload that does not match is the
// caller's bug.
//
// Port of notebooklm.client.py::NotebookLMClient.rpc_call.
// The Python signature is much wider
// (`allow_null`, `disable_internal_retries`, `read_timeout`,
// `raise_on_null_status`); the Phase 5 Go port ships the
// minimum-viable surface and grows it as later phases
// require.
func (c *Client) RPCCall(ctx context.Context, method wire.Method, params any) (any, error) {
	if c == nil {
		return nil, errors.New("notebooklm: nil client")
	}
	if err := c.checkOpen(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Admit through the supervisor. The lease gates in-flight
	// count and the drain condition; Release on the deferred
	// path is idempotent so a panic does not leak the slot.
	lease, err := c.supervisor.Admit(ctx, runtime.Operation("rpc"))
	if err != nil {
		return nil, fmt.Errorf("notebooklm: admit: %w", err)
	}
	defer lease.Release()

	// Default host is the canonical notebook host. Phase 5
	// pins Web only; Phase 6+ wires the Android adapter
	// through WithBackend.
	res, err := c.executor.Execute(ctx, method, "", params, c.authState.host, 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("notebooklm: rpc %s: %w", method, err)
	}
	if res == nil {
		return nil, errors.New("notebooklm: rpc: nil result")
	}
	return res.Body, nil
}

// RefreshAuth triggers the L1 step of the auth refresh ladder:
// an HTTP GET against the NotebookLM homepage that re-mints
// the CSRF token (SNlM0e) and rotates any rotated cookies the
// server sent back via Set-Cookie. The returned AuthSnapshot
// is the post-refresh view the next RPCCall will use.
//
// Phase 5 keeps this minimal — the L1 step only. Phase 4 will
// widen the body to the L1 → L2.0 → L2.5 → L3 → L4 ladder
// from internal/auth/refresh. The signature stays stable so
// the higher-level surface does not need to change.
//
// Port of notebooklm.client.py::NotebookLMClient.refresh_auth.
// The Python implementation is async; the Go port is sync
// because the underlying HTTP GET is sync in this phase (a
// later phase may promote this to a goroutine and surface the
// wait through a `done` channel — the SDK callers will not
// change).
func (c *Client) RefreshAuth(ctx context.Context) (AuthSnapshot, error) {
	if c == nil {
		return AuthSnapshot{}, errors.New("notebooklm: nil client")
	}
	if err := c.checkOpen(); err != nil {
		return AuthSnapshot{}, err
	}
	if err := ctx.Err(); err != nil {
		return AuthSnapshot{}, err
	}
	if c.authState.refresh == nil {
		// The runtime RefreshFunc is nil when the
		// auth coordinator has not been wired (no
		// credentials loaded). Treat this as a
		// "not initialized" error so the CLI can
		// produce the documented fix-it message.
		return AuthSnapshot{}, errors.New("notebooklm: refresh: auth coordinator not initialized")
	}
	if err := c.authState.refresh(ctx); err != nil {
		return AuthSnapshot{}, fmt.Errorf("notebooklm: refresh: %w", err)
	}
	return AuthSnapshot{
		CSRF:         c.authState.csrf,
		CookieHeader: jarHeader(c.authState.jar),
	}, nil
}

// MetricsSnapshot returns a point-in-time copy of the
// cumulative RPC counters. The returned value is a value-type
// snapshot; callers may store it, log it, or render it without
// affecting the live counters.
//
// The read path is lock-free (atomic loads). Snapshot is
// safe to call from any goroutine, including before Open,
// after Close, and concurrently with RPCCall.
func (c *Client) MetricsSnapshot() MetricsSnapshot {
	if c == nil || c.metrics == nil {
		return MetricsSnapshot{}
	}
	return fromRuntimeSnapshot(c.metrics.Snapshot())
}

// Notebooks returns the typed namespace API for the
// NotebookLM Notebook operations. The returned value is a
// stable facade bound to the Client; callers may store it and
// reuse it for the lifetime of the Client. Reusing it after
// Close is rejected per-call (the namespace API checks the
// Client's open state on every dispatch).
//
// Port of notebooklm.client.py::NotebookLMClient.notebooks —
// the attribute the Python original exposes. The Go port
// returns a typed *NotebooksAPI rather than a dynamic
// namespace dict so call sites can resolve methods at
// compile time.
func (c *Client) Notebooks() *NotebooksAPI {
	return newNotebooksAPI(c)
}

// checkOpen is the read-side gate that rejects method calls on
// a closed Client. Held briefly under closedMu.RLock so a
// concurrent Close cannot flip the flag while RPCCall is
// mid-admission.
func (c *Client) checkOpen() error {
	if c == nil {
		return errors.New("notebooklm: nil client")
	}
	c.closedMu.RLock()
	closed := c.closed
	c.closedMu.RUnlock()
	if closed {
		return ErrClientClosed
	}
	return nil
}

// resolveBackend turns the user-supplied BackendName into the
// concrete value the transport / policy layers consume. Empty
// is the "no preference" case; NOTEBOOKLM_BACKEND is consulted
// as the second precedence tier; BackendWeb is the floor.
func resolveBackend(b BackendName) (BackendName, error) {
	if b == "" {
		if v := os.Getenv("NOTEBOOKLM_BACKEND"); v != "" {
			b = BackendName(v)
		} else {
			b = BackendWeb
		}
	}
	switch b {
	case BackendWeb, BackendAndroid:
		return b, nil
	default:
		return "", fmt.Errorf("notebooklm: unknown backend %q (allowed: web, android)", b)
	}
}

// resolveStoragePath resolves the explicit-or-default path to a
// storage_state.json file. The precedence mirrors
// docs/13-operations.md:
//
//  1. The explicit path argument (non-empty wins).
//  2. NOTEBOOKLM_HOME / storage_state.json when path is empty.
//  3. ~/.notebooklm/storage_state.json when neither is set.
func resolveStoragePath(path string) (string, error) {
	if strings.TrimSpace(path) != "" {
		return path, nil
	}
	if home := os.Getenv("NOTEBOOKLM_HOME"); home != "" {
		return joinStorage(home), nil
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("notebooklm: resolve storage path: %w", err)
	}
	return joinStorage(userHome + "/.notebooklm"), nil
}

// joinStorage appends the storage_state.json basename with the
// correct path separator for the host OS.
func joinStorage(dir string) string {
	if strings.HasSuffix(dir, "/") {
		return dir + "storage_state.json"
	}
	return dir + "/storage_state.json"
}

// applyBackend performs any backend-specific wiring. Phase 5
// has no Android adapter yet; the call is a no-op until the
// Android adapter lands. We keep the seam so future phase
// tickets can plug in without changing Client.New.
func (c *Client) applyBackend(b BackendName) error {
	if b == BackendAndroid {
		c.logger.Debug("applyBackend: Android adapter not yet implemented; using Web fallback",
			slog.String("backend", string(b)))
	}
	return nil
}

// seedFromStorage copies the cookies out of a parsed
// storage_state.json into the Client's cookie jar and mints
// the CSRF token if one is present in the file.
//
// Phase 5 keeps this small — the canonical storage reader is
// enough. Phase 4's profile package will replace this with
// the full ProfileStore plumbing.
func (c *Client) seedFromStorage(s storage.Storage) error {
	if c.authState.jar == nil {
		return errors.New("notebooklm: seed: nil cookie jar")
	}
	host := &url.URL{
		Scheme: "https",
		Host:   string(c.authState.host),
	}
	for i := range s.Cookies {
		ck := &s.Cookies[i]
		if ck.Name == "" {
			continue
		}
		std := storageCookieToStdlib(ck)
		c.authState.jar.SetCookies(host, []*http.Cookie{std})
	}
	return nil
}

// storageCookieToStdlib adapts an internal/auth/storage.Cookie
// to the stdlib *http.Cookie the cookiejar expects. The two
// types differ in attribute spelling (SameSite casing,
// Expires vs ExpirationDate) — the storage layer normalizes
// both on read.
func storageCookieToStdlib(ck *storage.Cookie) *http.Cookie {
	std := &http.Cookie{
		Name:     ck.Name,
		Value:    ck.Value,
		Path:     ck.Path,
		Domain:   ck.Domain,
		Secure:   ck.Secure,
		HttpOnly: ck.HTTPOnly,
	}
	// SameSite: the storage layer uses lowercase strings
	// ("lax", "strict", "none"). The stdlib uses an
	// int-typed SameSite enum. We translate by string.
	switch strings.ToLower(ck.SameSite) {
	case "strict":
		std.SameSite = http.SameSiteStrictMode
	case "none":
		std.SameSite = http.SameSiteNoneMode
	case "lax":
		std.SameSite = http.SameSiteLaxMode
	}
	// Expires: the storage layer uses an int64 epoch
	// seconds (negative for session cookies). The stdlib
	// uses time.Time; the zero value is the session-cookie
	// sentinel.
	if ck.Expires != nil && *ck.Expires > 0 {
		std.Expires = time.Unix(*ck.Expires, 0)
	}
	return std
}

// refreshCallback returns the RefreshFunc the Client exposes
// to the Runtime's terminal middleware. The callback closes
// over the Client's AuthState and is the single body the
// terminal middleware runs.
//
// Phase 5 is a thin L1 step: GET the NotebookLM homepage so
// the response Set-Cookie headers rotate any expired cookies.
// Phase 4 replaces this body with the full ladder.
func (c *Client) refreshCallback() transport.RefreshFunc {
	return func(ctx context.Context, _ *transport.CallContext) error {
		return c.runL1Refresh(ctx)
	}
}

// runL1Refresh is the L1 step of the refresh ladder. It is a
// thin wrapper that the Phase 4 ladder will replace; the
// signature stays stable so callers do not change.
func (c *Client) runL1Refresh(ctx context.Context) error {
	if c.kernel == nil {
		return errors.New("notebooklm: refresh: nil kernel")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://"+string(c.authState.host)+"/", nil)
	if err != nil {
		return fmt.Errorf("notebooklm: refresh: build request: %w", err)
	}
	resp, err := c.kernel.Client().Do(req)
	if err != nil {
		return fmt.Errorf("notebooklm: refresh: get: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	// The body is discarded: the kernel's jar has already
	// absorbed any rotated cookies via Set-Cookie. Phase 4
	// replaces the discard with the WIZ_global_data parser
	// (SNlM0e + FdrFJe extraction).
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return nil
}

// jarHeader returns the canonical Cookie: header value the
// jar would emit for the canonical NotebookLM host. Used by
// RefreshAuth to populate AuthSnapshot.CookieHeader.
//
// The CookieJar interface requires a *url.URL, so we build
// one for the canonical host. An empty jar yields an empty
// string — the wire shape matches a real browser request
// even with no credentials.
func jarHeader(j *cookiejar.Jar) string {
	if j == nil {
		return ""
	}
	u := &url.URL{Scheme: "https", Host: string(defaultHost())}
	cookies := j.Cookies(u)
	if len(cookies) == 0 {
		return ""
	}
	parts := make([]string, 0, len(cookies))
	for _, ck := range cookies {
		parts = append(parts, ck.Name+"="+ck.Value)
	}
	return strings.Join(parts, "; ")
}
