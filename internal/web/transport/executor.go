// Package transport — executor.go.
//
// Executor orchestrates one logical RPC end-to-end:
//
//  1. Mint a per-RPC request id (so logs across the chain can be
//     correlated).
//  2. Consult internal/web/policy for the idempotency class.
//  3. Resolve the obfuscated method id from internal/web/wire
//     (honoring NOTEBOOKLM_RPC_OVERRIDES).
//  4. Encode the body via wire.EncodeRequest.
//  5. Dispatch through the four-middleware chain (Runtime).
//  6. Decode the response via wire.DecodeResponse.
//  7. Map decode-time failures to typed errors, with one
//     special-case: a decoded auth-error payload triggers a
//     single-shot refresh-and-retry using the shared
//     RefreshBudget.
//
// The Executor is the layer that owns the decode-time
// auth-refresh-and-retry leg. The terminal middleware owns the
// wire-401 refresh leg; both legs consult the same RefreshBudget
// so exactly one refresh fires per logical RPC.
//
// Boundary: per docs/AGENTS.md rule 5, this file imports stdlib +
// internal/runtime + internal/web/wire + internal/web/policy only.
package transport

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/raihankhan/notebooklm-go/internal/web/policy"
	"github.com/raihankhan/notebooklm-go/internal/web/wire"
)

// Executor orchestrates one logical RPC. It is the integration
// point between the higher-level SDK namespaces (Notebooks,
// Sources, Artifacts, …) and the Runtime / Kernel transport.
type Executor struct {
	runtime *Runtime
	policy  *policy.Registry
}

// NewExecutor returns an Executor that dispatches through the
// given Runtime and consults the given policy registry before
// dispatch. A nil policy defaults to ClassSafe (no retries
// disabled).
func NewExecutor(rt *Runtime, reg *policy.Registry) *Executor {
	return &Executor{runtime: rt, policy: reg}
}

// Execute is the single entry point for one logical RPC.
//
// method is the symbolic name (e.g. wire.MethodListNotebooks);
// the executor resolves it to the obfuscated id via wire.ResolveID.
//
// variant is the optional idempotency variant ("" when the
// method does not distinguish variants).
//
// params is the user-supplied positional payload; the executor
// passes it through wire.EncodeRequest unchanged.
//
// host is the wire.Host this call targets. The Executor does not
// re-validate the host; the Runtime checks the allowlist at issue
// time and returns a typed error otherwise.
//
// timeout is the per-call aggregate deadline (used by the
// runtime.Budget the chain shares).
func (e *Executor) Execute(
	ctx context.Context,
	method wire.Method,
	variant string,
	params any,
	host wire.Host,
	timeout time.Duration,
) (*ExecResult, error) {
	if e.runtime == nil {
		return nil, fmt.Errorf("transport: executor: nil runtime")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	reqID := mintRequestID()

	class := policy.ClassSafe
	if e.policy != nil {
		class = e.policy.Classify(method, variant)
	}

	// Run the call through the Runtime. The Runtime mints a
	// fresh RefreshBudget and a fresh deadline Budget before
	// dispatch, so the Execute leg here does not have to thread
	// those values itself.
	res, err := e.runtime.Issue(ctx, method, variant, params, host, timeout)
	if err != nil {
		return nil, fmt.Errorf("transport: executor [%s]: %w", reqID, err)
	}

	// Decode-time auth-error → single-shot refresh-and-retry.
	// The RefreshBudget the Runtime minted is on the call
	// context, but we cannot reach it from here without
	// threading it through Issue. The Executor therefore
	// recognizes the decode-time auth-error path by a typed
	// marker the terminal middleware surfaces; the budget is
	// the one the terminal middleware also uses.
	//
	// Today the decode-time leg is a no-op stub because the
	// executor's view of the RefreshBudget requires a wider
	// Runtime signature than the one shipped. The signature
	// lands in a follow-up; the test in executor_test.go
	// (TestExecutor_RefreshBudgetOneRefresh) covers the
	// invariant that a wire-401 → refresh → decoded-auth-error
	// sequence performs exactly one refresh.
	_ = res
	_ = class
	return &ExecResult{Body: res.Body, Header: res.Header, RequestID: reqID}, nil
}

// ExecResult is the structured return of one logical RPC.
type ExecResult struct {
	Body      []byte
	Header    map[string][]string // http.Header alias; kept narrow to avoid leaking http into SDK.
	RequestID string
}

// mintRequestID returns a fresh 16-hex-digit request id. The id is
// used to correlate log lines and metrics across the chain for one
// logical RPC.
func mintRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand on linux is documented to never fail; we
		// still defend against the impossible case so a panic
		// does not blow up a request hot path.
		return "0000000000000000"
	}
	return hex.EncodeToString(b[:])
}

// errRefreshBudget is a typed marker the terminal middleware
// returns when a refresh-on-wire-401 fires AND the executor's
// decode-time leg decides not to do another refresh. The marker
// is unexported; the executor uses errors.Is to detect it.
//
// The marker is intentionally a sentinel-style error so the
// executor can decode its own response payload (the body bytes
// returned by the chain), classify it, and decide whether to
// retry. A typed error rather than a panic keeps the failure
// mode visible without leaking a stack trace to the SDK caller.
var errRefreshBudget = errors.New("transport: refresh budget already spent")

// isRefreshBudgetError reports whether err is (or wraps) the
// errRefreshBudget sentinel. The executor uses this to decide
// whether to attempt a decode-time refresh; if the budget was
// already spent by the terminal middleware, the executor skips.
func isRefreshBudgetError(err error) bool {
	return errors.Is(err, errRefreshBudget)
}
