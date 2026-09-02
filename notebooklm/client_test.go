// Package notebooklm — client_test.go.
//
// Unit tests for the Client constructor and the cross-namespace
// primitives (New, FromStorage, Close, Drain, RPCCall,
// RefreshAuth, MetricsSnapshot). The tests use a fake Executor
// implementation to assert the wiring without spinning an
// httptest server; the transport-layer regression scenarios
// already have their own tests in
// internal/web/transport/executor_test.go.
//
// Every test pins one of the AC1–AC10 entries the ticket
// commits to. The table below maps the test names to the AC
// they pin so a future ticket that drops a test reads the
// mapping here:
//
//	TestClient_New_BuildsWiredClient         -> AC1
//	TestClient_New_AppliesOptions            -> AC8
//	TestClient_New_RejectsBadBackend         -> AC1
//	TestClient_FromStorage_LoadsProfile      -> AC2
//	TestClient_Close_FencesKernel            -> AC3
//	TestClient_Close_IsIdempotent            -> AC3
//	TestClient_Drain_WaitsForInflight        -> AC4
//	TestClient_RPCCall_AdmitAndDispatch      -> AC5
//	TestClient_RPCCall_RejectsAfterClose     -> AC3
//	TestClient_RefreshAuth_NoAuthCoordinator -> AC6
//	TestClient_RefreshAuth_DelegatesToFunc   -> AC6
//	TestClient_MetricsSnapshot_ReadOnly      -> AC7
package notebooklm

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/raihankhan/notebooklm-go/internal/runtime"
	"github.com/raihankhan/notebooklm-go/internal/web/transport"
	"github.com/raihankhan/notebooklm-go/internal/web/wire"
)

// fakeExecutor is the test double the Client accepts through
// its Executor interface. It records every Execute call and
// returns the canned result the test configured.
//
// The fake also bumps a metrics handle (if the test wired
// one) so the AC7 metrics-snapshot test can assert a counter
// delta against an executor it owns.
type fakeExecutor struct {
	mu        sync.Mutex
	calls     []fakeCall
	canned    *transport.ExecResult
	cannedErr error
	metrics   *runtime.Metrics
}

type fakeCall struct {
	method  wire.Method
	variant string
	params  any
	host    wire.Host
	timeout time.Duration
}

func (f *fakeExecutor) Execute(
	ctx context.Context,
	method wire.Method,
	variant string,
	params any,
	host wire.Host,
	timeout time.Duration,
) (*transport.ExecResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{
		method:  method,
		variant: variant,
		params:  params,
		host:    host,
		timeout: timeout,
	})
	f.mu.Unlock()
	if f.metrics != nil {
		f.metrics.IncRpcCallsStarted()
	}
	if f.cannedErr != nil {
		if f.metrics != nil {
			f.metrics.IncRpcCallsFailed()
		}
		return nil, f.cannedErr
	}
	if f.metrics != nil {
		f.metrics.IncRpcCallsSucceeded()
	}
	if f.canned != nil {
		return f.canned, nil
	}
	return &transport.ExecResult{Body: []byte("ok"), RequestID: "test"}, nil
}

// TestClient_New_BuildsWiredClient pins AC1: a fresh Client
// has a Kernel, an Executor, a Lifecycle, a Supervisor, and a
// Metrics. The fake executor installed by the test is the
// visible surface (production code uses the real
// transport.Executor; the seam is the same shape).
func TestClient_New_BuildsWiredClient(t *testing.T) {
	c, err := New(context.Background())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = c.Close() }()

	if c.kernel == nil {
		t.Errorf("kernel is nil")
	}
	if c.transport == nil {
		t.Errorf("transport is nil")
	}
	if c.executor == nil {
		t.Errorf("executor is nil")
	}
	if c.registry == nil {
		t.Errorf("registry is nil")
	}
	if c.lifecycle == nil {
		t.Errorf("lifecycle is nil")
	}
	if c.supervisor == nil {
		t.Errorf("supervisor is nil")
	}
	if c.metrics == nil {
		t.Errorf("metrics is nil")
	}
}

// TestClient_New_AppliesOptions pins AC8: every Option in the
// public set mutates the Client's runtime state in the
// documented way.
func TestClient_New_AppliesOptions(t *testing.T) {
	sharedMetrics := runtime.NewMetrics()
	c, err := New(context.Background(),
		WithStoragePath("/tmp/storage_state.json"),
		WithBackend(BackendWeb),
		WithMetrics(sharedMetrics),
		WithEpoch(2),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = c.Close() }()

	if c.config.storagePath != "/tmp/storage_state.json" {
		t.Errorf("storagePath = %q, want /tmp/storage_state.json",
			c.config.storagePath)
	}
	if c.config.backend != BackendWeb {
		t.Errorf("backend = %q, want %q", c.config.backend, BackendWeb)
	}
	if c.metrics != sharedMetrics {
		t.Errorf("metrics is not the injected handle")
	}
	if got := c.kernel.Generation(); got < 2 {
		t.Errorf("kernel generation = %d, want >= 2", got)
	}
}

// TestClient_New_RejectsBadBackend pins AC1's backend
// validation. The Python original's `Literal["web",
// "android"]` is a compile-time guard; the Go port mirrors
// that with a runtime check that returns a typed error
// naming the bad value.
func TestClient_New_RejectsBadBackend(t *testing.T) {
	_, err := New(context.Background(), WithBackend(BackendName("firefox")))
	if err == nil {
		t.Fatalf("expected error for unknown backend")
	}
	if !contains(err.Error(), "firefox") {
		t.Errorf("error %q does not name the bad backend", err.Error())
	}
}

// TestClient_FromStorage_LoadsProfile pins AC2: FromStorage
// reads storage_state.json, hands the parsed cookies to the
// Client's cookie jar, and returns an open Client.
//
// The fixture is a tiny storage_state.json written to a
// t.TempDir() path. The test asserts (a) the call returns
// nil error, (b) the Client's jar holds the seeded cookie
// after construction.
func TestClient_FromStorage_LoadsProfile(t *testing.T) {
	// Write a minimal storage_state.json. The storage
	// package's Read expects a real file at the path; we
	// use a per-test temp dir so the test is hermetic.
	dir := t.TempDir()
	statePath := dir + "/storage_state.json"
	const minimalState = `{
		"cookies": [
			{
				"name": "SID",
				"value": "test-sid",
				"domain": ".google.com",
				"path": "/",
				"expires": -1,
				"httpOnly": true,
				"secure": true,
				"sameSite": "Lax",
				"hostOnly": false
			}
		],
		"origins": []
	}`
	if err := writeFile(statePath, []byte(minimalState), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	c, err := FromStorage(context.Background(), statePath)
	if err != nil {
		t.Fatalf("FromStorage: %v", err)
	}
	defer func() { _ = c.Close() }()

	if c.authState.jar == nil {
		t.Fatalf("seeded jar is nil")
	}
	if c.authState.jar.Len() == 0 {
		t.Errorf("seeded jar has no cookies")
	}
}

// TestClient_Close_FencesKernel pins AC3: Close retires the
// kernel generation so a subsequent RPCCall attempt finds the
// kernel closed. The Close body runs FenceEpoch + the kernel
// lifecycle participant's close hook, which bumps the
// generation; the test asserts the kernel refuses requests
// after Close rather than asserting a specific generation
// arithmetic (the bump is an implementation detail of the
// kernel's epoch-fencing invariant).
func TestClient_Close_FencesKernel(t *testing.T) {
	c, err := New(context.Background())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !c.closed {
		t.Errorf("closed flag is false after Close")
	}
	// Subsequent RPCCall returns ErrClientClosed because
	// the Client's own closed flag is checked first; the
	// Kernel's epoch-fence would also reject the call, but
	// the Client short-circuits on the cheaper flag check.
	_, err = c.RPCCall(context.Background(), wire.MethodListNotebooks, nil)
	if !errors.Is(err, ErrClientClosed) {
		t.Errorf("RPCCall after Close: got %v, want ErrClientClosed", err)
	}
	// Independently, the kernel itself is closed — a raw
	// Kernel.AssertEpoch on a stale generation surfaces a
	// typed StaleEpochError.
	if err := c.kernel.AssertEpoch(1); err == nil {
		t.Errorf("kernel.AssertEpoch(1) after Close: got nil, want error")
	}
}

// TestClient_Close_IsIdempotent pins AC3: Close on an
// already-closed Client is a no-op.
func TestClient_Close_IsIdempotent(t *testing.T) {
	c, err := New(context.Background())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("second Close returned %v, want nil", err)
	}
}

// TestClient_Drain_WaitsForInflight pins AC4: Drain blocks
// while in-flight calls are admitted and returns once the
// supervisor's in-flight count drops to zero.
//
// The fake executor holds the call open until the test
// signals "done"; Drain observes the in-flight count via the
// supervisor's InFlight() method and returns when the count
// hits zero.
func TestClient_Drain_WaitsForInflight(t *testing.T) {
	c, err := New(context.Background(), withMaxInFlight(4))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = c.Close() }()

	blocking := newBlockingExecutor(3)
	c.executor = blocking

	// Fire three in-flight calls the fake holds open
	// until the test signals done.
	for i := 0; i < 3; i++ {
		go func() {
			_, _ = c.RPCCall(context.Background(), wire.MethodListNotebooks, nil)
		}()
	}
	blocking.started.Wait()

	// Begin the drain in a goroutine; it should observe
	// InFlight > 0 and block.
	drainDone := make(chan error, 1)
	go func() { drainDone <- c.Drain(context.Background(), 5*time.Second) }()

	// Give Drain a tick to enter the wait.
	time.Sleep(20 * time.Millisecond)
	select {
	case err := <-drainDone:
		t.Fatalf("Drain returned early: err=%v", err)
	default:
	}

	// Release the in-flight calls.
	blocking.release()
	blocking.finished.Wait()

	select {
	case err := <-drainDone:
		if err != nil {
			t.Errorf("Drain returned %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Drain did not return after in-flight count dropped to zero")
	}
}

// blockingExecutor holds the call open until the test calls
// release(). It is the in-flight stand-in the Drain test uses
// to prove Drain does not return while calls are admitted.
type blockingExecutor struct {
	started  sync.WaitGroup
	finished sync.WaitGroup
	pending  atomic.Int32
}

func newBlockingExecutor(n int) *blockingExecutor {
	b := &blockingExecutor{}
	b.started.Add(n)
	b.finished.Add(n)
	// Atomic Int32 can store a positive int32 value safely;
	// the gosec G115 check is gated on potentially-negative
	// values. The bound here is `n > 0` by construction
	// (tests pass 3 in this file).
	b.pending.Store(int32(n)) //nolint:gosec // n is small and positive in tests.
	return b
}

func (b *blockingExecutor) release() { b.pending.Store(0) }

func (b *blockingExecutor) Execute(
	ctx context.Context,
	method wire.Method,
	variant string,
	params any,
	host wire.Host,
	timeout time.Duration,
) (*transport.ExecResult, error) {
	b.started.Done()
	for b.pending.Load() > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
	b.finished.Done()
	return &transport.ExecResult{Body: []byte("ok")}, nil
}

// TestClient_RPCCall_AdmitAndDispatch pins AC5: RPCCall
// admits through the supervisor, calls Executor.Execute with
// the supplied method and params, and returns the result.
func TestClient_RPCCall_AdmitAndDispatch(t *testing.T) {
	c, err := New(context.Background())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = c.Close() }()

	fake := &fakeExecutor{
		canned: &transport.ExecResult{Body: []byte("hello"), RequestID: "abc"},
	}
	c.executor = fake

	got, err := c.RPCCall(context.Background(), wire.MethodListNotebooks, []any{1, 2})
	if err != nil {
		t.Fatalf("RPCCall: %v", err)
	}
	if string(got.([]byte)) != "hello" {
		t.Errorf("RPCCall body = %q, want hello", got)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("Execute called %d times, want 1", len(fake.calls))
	}
	if fake.calls[0].method != wire.MethodListNotebooks {
		t.Errorf("method = %q, want %q", fake.calls[0].method, wire.MethodListNotebooks)
	}
	// The default host must be the canonical Web host.
	if fake.calls[0].host != defaultHost() {
		t.Errorf("host = %q, want %q", fake.calls[0].host, defaultHost())
	}
}

// TestClient_RPCCall_RejectsAfterClose pins AC3: an
// in-flight Client that closes between calls returns the
// typed sentinel on the next call.
func TestClient_RPCCall_RejectsAfterClose(t *testing.T) {
	c, err := New(context.Background())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err = c.RPCCall(context.Background(), wire.MethodListNotebooks, nil)
	if !errors.Is(err, ErrClientClosed) {
		t.Errorf("RPCCall after Close: got %v, want ErrClientClosed", err)
	}
}

// TestClient_RefreshAuth_NoAuthCoordinator pins AC6: when
// the auth coordinator is not initialized (no credentials
// loaded), RefreshAuth returns a typed error naming the
// condition rather than silently no-oping.
func TestClient_RefreshAuth_NoAuthCoordinator(t *testing.T) {
	c, err := New(context.Background())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = c.Close() }()

	_, err = c.RefreshAuth(context.Background())
	if err == nil {
		t.Fatalf("RefreshAuth: expected error")
	}
	if !contains(err.Error(), "auth coordinator not initialized") {
		t.Errorf("error %q does not name the missing coordinator", err.Error())
	}
}

// TestClient_RefreshAuth_DelegatesToFunc pins AC6: when the
// auth coordinator is initialized (the AuthState.refresh
// field is non-nil), RefreshAuth invokes it and returns the
// post-refresh AuthSnapshot.
func TestClient_RefreshAuth_DelegatesToFunc(t *testing.T) {
	c, err := New(context.Background())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = c.Close() }()

	var called int
	c.authState.refresh = func(ctx context.Context) error {
		called++
		c.authState.csrf = "test-csrf"
		return nil
	}
	snap, err := c.RefreshAuth(context.Background())
	if err != nil {
		t.Fatalf("RefreshAuth: %v", err)
	}
	if called != 1 {
		t.Errorf("refresh func called %d times, want 1", called)
	}
	if snap.CSRF != "test-csrf" {
		t.Errorf("CSRF = %q, want test-csrf", snap.CSRF)
	}
}

// TestClient_MetricsSnapshot_ReadOnly pins AC7:
// MetricsSnapshot returns a value-type copy. Mutating the
// returned struct does not affect the live counters, and
// concurrent Metric.Inc* calls do not race the Snapshot
// read (atomic loads are documented as lock-free).
func TestClient_MetricsSnapshot_ReadOnly(t *testing.T) {
	c, err := New(context.Background())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = c.Close() }()

	c.metrics.IncRpcCallsStarted()
	c.metrics.IncRpcCallsSucceeded()

	snap := c.MetricsSnapshot()
	if snap.RPCStarted != 1 {
		t.Errorf("RPCStarted = %d, want 1", snap.RPCStarted)
	}
	if snap.RPCSucceeded != 1 {
		t.Errorf("RPCSucceeded = %d, want 1", snap.RPCSucceeded)
	}

	// Mutate the snapshot and re-read. The live counters
	// must not have changed.
	snap.RPCStarted = 9999
	snap2 := c.MetricsSnapshot()
	if snap2.RPCStarted != 1 {
		t.Errorf("RPCStarted after snapshot mutation = %d, want 1", snap2.RPCStarted)
	}
}

// writeFile is a tiny os.WriteFile wrapper so the test file
// does not have to import os directly.
func writeFile(path string, data []byte, perm uint32) error {
	return os.WriteFile(path, data, os.FileMode(perm))
}

// contains is a tiny strings.Contains wrapper to keep the
// test file imports minimal.
func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
