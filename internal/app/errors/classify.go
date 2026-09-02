package errors

import (
	stderrors "errors"
	"fmt"
)

// Coded is the public seam. Any error type that implements
// `Code() Code` is recognized by Classify and short-circuits the
// sentinel / wrap walk. Adapters that build their own typed errors
// can satisfy it directly:
//
//	type myErr struct{ msg string }
//	func (e *myErr) Error() string { return e.msg }
//	func (e *myErr) Code() Code    { return CodeNetworkError }
//
// The interface is named in opposition to a per-package private
// type so the public seam reads naturally at call sites
// (`errors.Coded`). The single method mirrors the Python original's
// `.code` attribute on the typed exception hierarchy in
// notebooklm-py/src/notebooklm/exceptions.py, so a port of a Python
// `except SomeError as e: handle(e.code)` finds the same name.
type Coded interface {
	error
	Code() Code
}

// wrapError is the canonical typed error that Wrap (and the
// package-level sentinels) returns. It implements both `error` and
// `Code() Code`, so it satisfies Coded and feeds straight back into
// Classify. The original cause is preserved via Unwrap so errors.Is
// walks the chain. opts is nil for sentinels and for Wrap calls
// without envelope-field options; PayloadOf reads it to expose the
// structured fields (retry_after / id / notebook_id / method_id).
//
// Method order matches the receiver-naming convention used elsewhere
// in the module (receiver is always `e`, the zero-value form).
type wrapError struct {
	code  Code
	msg   string
	cause error
	opts  *payload
}

// Error returns the user-facing message. The wrap message is always
// present (Wrap requires a non-nil err argument), so the format is
// "<msg>: <cause>" — never just "<cause>" — to keep the chain
// visible in logs. Credentials never reach this string: the caller
// is responsible for routing through internal/redact before passing
// the message in, per docs/AGENTS.md rule 4.
func (e *wrapError) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %s", e.msg, e.cause.Error())
	}
	return e.msg
}

// Code returns the canonical machine code attached at construction.
// Stable across the lifetime of the value — never derived from the
// wrapped cause, so a re-wrap with a different code replaces the
// classification.
func (e *wrapError) Code() Code { return e.code }

// Unwrap returns the wrapped underlying error (if any). Lets
// errors.Is and errors.As walk back to the sentinel or typed error
// that triggered the wrap, so adapters that want to inspect a
// concrete transport.AuthError still can after Wrap.
func (e *wrapError) Unwrap() error { return e.cause }

// WrapOpt mutates an internal envelope payload during construction.
// The companion helpers below (WithRetryAfter, WithID,
// WithNotebookID, WithMethodID) are the four shapes the canonical
// --json envelope currently accepts; the type is exported so a
// future envelope field can add a fifth without breaking the Wrap
// signature.
//
// WrapOpts stay private to wrapError — they are not part of the
// Coded seam. Adapters that need to read the structured fields
// off the result of Wrap call the matching accessor
// (RetryAfterOf / IDOf / …) below.
type WrapOpt func(*wrapError)

// payload is the structured envelope payload attached by WrapOpts.
// One struct (rather than four parallel slices) keeps the field
// set coordinated — adding a WithX just adds a field here.
type payload struct {
	// retryAfter is the Retry-After hint in seconds. Only meaningful
	// on CodeRateLimited envelopes.
	retryAfter int
	// retryAfterSet is true when WithRetryAfter was called. Distinct
	// from a zero value so an adapter can distinguish "no hint
	// attached" from "hint attached as zero".
	retryAfterSet bool
	// id is the missing-resource id (NOT_FOUND envelopes).
	id string
	// notebookID is the notebook scope id.
	notebookID string
	// methodID is the verbose-only RPC method id.
	methodID string
}

// WithRetryAfter attaches a Retry-After hint (in seconds) to the
// wrapped error. Use only with CodeRateLimited; the field is
// meaningless on other codes and would confuse an adapter that
// branches on key presence.
func WithRetryAfter(seconds int) WrapOpt {
	return func(e *wrapError) {
		if e.opts == nil {
			e.opts = &payload{}
		}
		e.opts.retryAfter = seconds
		e.opts.retryAfterSet = true
	}
}

// WithID attaches the missing-resource id (NOT_FOUND envelopes).
// When the failure is also scoped to a notebook, prefer
// WithNotebookID alongside.
func WithID(id string) WrapOpt {
	return func(e *wrapError) {
		if e.opts == nil {
			e.opts = &payload{}
		}
		e.opts.id = id
	}
}

// WithNotebookID attaches the notebook scope id.
func WithNotebookID(id string) WrapOpt {
	return func(e *wrapError) {
		if e.opts == nil {
			e.opts = &payload{}
		}
		e.opts.notebookID = id
	}
}

// WithMethodID attaches the batchexecute RPC method id. Reserved
// for -v verbose envelopes; the id is not a stable contract and
// should never be the only signal a caller branches on.
func WithMethodID(id string) WrapOpt {
	return func(e *wrapError) {
		if e.opts == nil {
			e.opts = &payload{}
		}
		e.opts.methodID = id
	}
}

// Payload is the structured envelope fields attached by WrapOpts.
// One struct (rather than four parallel accessors) keeps the field
// set coordinated — adding a WithX is a one-line addition.
//
// Field semantics:
//
//   - RetryAfter — the Retry-After hint in seconds. Only meaningful
//     on CodeRateLimited envelopes. Zero is a meaningful value
//     when RetryAfterSet is true (caller attached a literal zero).
//
//   - RetryAfterSet — true when WithRetryAfter was called. Distinct
//     from a zero value so an adapter can distinguish "no hint
//     attached" from "hint attached as zero".
//
//   - ID — the missing-resource id (NOT_FOUND envelopes).
//
//   - NotebookID — the notebook scope id.
//
//   - MethodID — the verbose-only RPC method id.
type Payload struct {
	RetryAfter    int
	RetryAfterSet bool
	ID            string
	NotebookID    string
	MethodID      string
}

// PayloadOf returns the structured envelope fields attached to err
// by Wrap (via WithRetryAfter / WithID / WithNotebookID /
// WithMethodID). Returns false when err is nil, when err does not
// wrap a Wrap-built value, or when no opt was attached. The adapter
// passes the result to serialize.ErrorOpt builders to copy each
// present field into the --json envelope without re-walking the
// chain.
func PayloadOf(err error) (Payload, bool) {
	if err == nil {
		return Payload{}, false
	}
	var w *wrapError
	if !stderrors.As(err, &w) {
		return Payload{}, false
	}
	if w.opts == nil {
		return Payload{}, false
	}
	return Payload{
		RetryAfter:    w.opts.retryAfter,
		RetryAfterSet: w.opts.retryAfterSet,
		ID:            w.opts.id,
		NotebookID:    w.opts.notebookID,
		MethodID:      w.opts.methodID,
	}, true
}

// New constructs a sentinel-style typed error with the given Code
// and message. The returned value satisfies Coded and is the
// canonical way to mint a sentinel — the four package-level vars
// (ErrAuth, ErrRateLimited, ErrNotFound, ErrQuota) are themselves
// New(...) calls.
//
// New is exported because adapters occasionally need to mint a
// typed error directly (e.g. an MCP server that has no upstream
// error to wrap). Prefer the package-level sentinels for the four
// canonical categories so callers can `errors.Is` against them.
func New(code Code, message string) error {
	return &wrapError{code: code, msg: message}
}

// Wrap attaches a Code to an existing error. The returned error
// satisfies Coded and is suitable for direct return from a public
// function — the caller does not need to remember to also assign
// the code, because Wrap does both jobs. If err is nil, Wrap
// returns nil so a `return Wrap(Code, maybeErr)` pattern compiles
// cleanly without an `if maybeErr != nil` guard.
//
// opts are the WrapOpt values that attach structured envelope
// fields (retry_after, id, notebook_id, method_id). The same set
// of options the serialize.ErrorBody accepts; the wrap copies them
// through so a later Coded-error pass can read them via PayloadOf.
func Wrap(code Code, err error, opts ...WrapOpt) error {
	if err == nil {
		return nil
	}
	w := &wrapError{code: code, msg: err.Error(), cause: err}
	for _, opt := range opts {
		opt(w)
	}
	return w
}

// Classify returns the (Code, exit) pair for the given error per
// docs/07-cli-spec.md §"Exit codes". The classifier is purely
// structural — it does not log, render, or mutate the error. It
// is the single source of truth for the code/exit mapping; every
// adapter (CLI / MCP / REST) calls it and never rolls its own
// switch.
//
// Resolution order:
//
//  1. nil → NOTEBOOKLM_ERROR + exit 1. An adapter that classifies
//     a nil error is itself a bug, but returning a visible
//     failure rather than panicking is the friendlier
//     degradation.
//  2. Sentinel match (errors.Is) — fast path for the four
//     canonical sentinels. Each sentinel knows its own Code, so
//     this is a map lookup, not a long if/else.
//  3. Coded seam — any error implementing Code() Code returns its
//     own code. This is how future typed errors plug in without
//     touching this function.
//  4. Fallback — UNEXPECTED_ERROR + exit 2 for any non-library
//     error (stdlib panic escape, third-party import surprise).
//
// The result is the contract every other adapter depends on; do
// not change either field without updating docs/07-cli-spec.md.
func Classify(err error) (Code, int) {
	if err == nil {
		// A nil classification is a programmer error, but the safe
		// behavior is "visible failure, not crash" — the upstream
		// caller should treat nil-classify as a bug to fix, not
		// silently succeed.
		return CodeNotebookLMError, ExitFor(CodeNotebookLMError)
	}

	// --- 2. Sentinel chain -----------------------------------
	// errors.Is walks the wrap chain, so a `Wrap(CodeAuthError,
	// typedAuthErr)` matches ErrAuth without any extra glue.
	switch {
	case stderrors.Is(err, ErrAuth):
		return CodeAuthError, ExitFor(CodeAuthError)
	case stderrors.Is(err, ErrRateLimited):
		return CodeRateLimited, ExitFor(CodeRateLimited)
	case stderrors.Is(err, ErrNotFound):
		return CodeNotFound, ExitFor(CodeNotFound)
	case stderrors.Is(err, ErrQuota):
		return CodeNotebookLimit, ExitFor(CodeNotebookLimit)
	}

	// --- 3. Coded seam ---------------------------------------
	// A future typed error that knows its own code returns it
	// here. The interface check is a type assertion, not a string
	// match — zero allocation. errors.As walks the chain so a
	// `Wrap(CodeX, codedErr)` still resolves.
	var c Coded
	if stderrors.As(err, &c) {
		code := c.Code()
		return code, ExitFor(code)
	}

	// --- 4. Fallback -----------------------------------------
	// Non-library error. Likely a bug or a panic escape.
	return CodeUnexpectedError, ExitFor(CodeUnexpectedError)
}
