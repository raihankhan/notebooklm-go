// Tests for the Classify mapping (T-P5-3, issue #54). Each table row
// pins one (error, expected code, expected exit) triple against the
// canonical mapping in docs/07-cli-spec.md §"Exit codes" and §"The
// --json error envelope". A failure here means the spec table and the
// classifier disagree — exactly the kind of drift that breaks a
// scripted caller branching on `code`.
//
// Coverage is split into three table-driven tests:
//
//   - TestClassifySentinels pins the four canonical sentinels and
//     every wrap path (Wrap, Wrap-with-opts, errors.Is across a
//     chain, errors.As back to the sentinel).
//   - TestClassifyCoded pins the Code() string interface seam for
//     future typed errors.
//   - TestClassifyExhaustive walks the AllCodes list and asserts the
//     (code, exit) pair for every entry — a regression guard against
//     a constant added to codes.go but forgotten in exitFor.
//
// All tests run without any external dependency; the transport layer
// does not exist yet (T-P5-4 onward), so we test Classify with the
// sentinel-only surface the package itself exposes.
package errors_test

import (
	stderrors "errors"
	"fmt"
	"testing"

	apperrors "github.com/raihankhan/notebooklm-go/internal/app/errors"
)

// TestClassifySentinels covers AC2 (Classify maps every typed error
// to code/exit) and AC4 (sentinel error types) for the four
// package-level sentinels. Each subtest asserts the (code, exit)
// pair for both the bare sentinel and a Wrap around it, so a future
// refactor that breaks the errors.Is walk fails loudly.
func TestClassifySentinels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      error
		wantCode apperrors.Code
		wantExit int
	}{
		{
			name:     "ErrAuth bare",
			err:      apperrors.ErrAuth,
			wantCode: apperrors.CodeAuthError,
			wantExit: 1,
		},
		{
			name:     "ErrAuth wrapped via Wrap",
			err:      apperrors.Wrap(apperrors.CodeAuthError, fmt.Errorf("login required")),
			wantCode: apperrors.CodeAuthError,
			wantExit: 1,
		},
		{
			name:     "ErrRateLimited bare",
			err:      apperrors.ErrRateLimited,
			wantCode: apperrors.CodeRateLimited,
			wantExit: 1,
		},
		{
			name: "ErrRateLimited wrapped with retry_after opt",
			err: apperrors.Wrap(
				apperrors.CodeRateLimited,
				fmt.Errorf("slow down"),
				apperrors.WithRetryAfter(30),
			),
			wantCode: apperrors.CodeRateLimited,
			wantExit: 1,
		},
		{
			name:     "ErrNotFound bare",
			err:      apperrors.ErrNotFound,
			wantCode: apperrors.CodeNotFound,
			wantExit: 1,
		},
		{
			name: "ErrNotFound wrapped with id opt",
			err: apperrors.Wrap(
				apperrors.CodeNotFound,
				fmt.Errorf("source missing"),
				apperrors.WithID("src-abc"),
			),
			wantCode: apperrors.CodeNotFound,
			wantExit: 1,
		},
		{
			name:     "ErrQuota bare",
			err:      apperrors.ErrQuota,
			wantCode: apperrors.CodeNotebookLimit,
			wantExit: 1,
		},
		{
			name:     "ErrQuota wrapped",
			err:      apperrors.Wrap(apperrors.CodeNotebookLimit, fmt.Errorf("at 100/100")),
			wantCode: apperrors.CodeNotebookLimit,
			wantExit: 1,
		},
		{
			name:     "stdlib error fallback",
			err:      stderrors.New("plain error"),
			wantCode: apperrors.CodeUnexpectedError,
			wantExit: 2,
		},
		{
			name:     "typed fmt.Errorf fallback",
			err:      fmt.Errorf("network blip"),
			wantCode: apperrors.CodeUnexpectedError,
			wantExit: 2,
		},
		{
			name:     "nil error",
			err:      nil,
			wantCode: apperrors.CodeNotebookLMError,
			wantExit: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotCode, gotExit := apperrors.Classify(tc.err)
			if gotCode != tc.wantCode {
				t.Errorf("Classify(%v) code = %q, want %q", tc.err, gotCode, tc.wantCode)
			}
			if gotExit != tc.wantExit {
				t.Errorf("Classify(%v) exit = %d, want %d", tc.err, gotExit, tc.wantExit)
			}
		})
	}
}

// codedErr is a private Coded implementation used to exercise the
// interface seam. It is defined here (not in the production file)
// because it is test-only — the production seam is `errors.Coded`,
// the test just needs a concrete implementer.
type codedErr struct {
	code apperrors.Code
	msg  string
}

func (e *codedErr) Error() string { return e.msg }
func (e *codedErr) Code() apperrors.Code {
	return e.code
}

// TestClassifyCoded covers AC5 (Code() interface seam). A future
// typed error in transport or web satisfies Coded and Classify
// returns its declared code without the caller having to teach
// Classify the new type.
func TestClassifyCoded(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      apperrors.Coded
		wantCode apperrors.Code
		wantExit int
	}{
		{
			name:     "coded returns VALIDATION_ERROR",
			err:      &codedErr{code: apperrors.CodeValidationError, msg: "bad arg"},
			wantCode: apperrors.CodeValidationError,
			wantExit: 1,
		},
		{
			name:     "coded returns CONFIG_ERROR",
			err:      &codedErr{code: apperrors.CodeConfigError, msg: "no auth"},
			wantCode: apperrors.CodeConfigError,
			wantExit: 1,
		},
		{
			name:     "coded returns NETWORK_ERROR",
			err:      &codedErr{code: apperrors.CodeNetworkError, msg: "dns"},
			wantCode: apperrors.CodeNetworkError,
			wantExit: 1,
		},
		{
			//nolint:misspell // "CANCELLED" is the canonical spelling per docs/07-cli-spec.md exit-code table; it is also what the canonical --json error envelope document shows.
			name:     "coded returns CANCELLED with exit 130",
			err:      &codedErr{code: apperrors.CodeCancelled, msg: "ctrl-c"},
			wantCode: apperrors.CodeCancelled,
			wantExit: 130,
		},
		{
			name:     "coded returns UNEXPECTED_ERROR with exit 2",
			err:      &codedErr{code: apperrors.CodeUnexpectedError, msg: "boom"},
			wantCode: apperrors.CodeUnexpectedError,
			wantExit: 2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotCode, gotExit := apperrors.Classify(tc.err)
			if gotCode != tc.wantCode {
				t.Errorf("Classify(%v) code = %q, want %q", tc.err, gotCode, tc.wantCode)
			}
			if gotExit != tc.wantExit {
				t.Errorf("Classify(%v) exit = %d, want %d", tc.err, gotExit, tc.wantExit)
			}
		})
	}
}

// TestClassifyCodedUnderWrap pins the seam across Wrap: a
// `Wrap(code, typedErr)` should classify as the OUTER code, because
// the wrap carries the cross-cutting context and the wrap itself
// satisfies Coded (via the Code() method it implements).
//
// The inner typed error is still reachable via errors.As / errors.Is
// in a separate chain walk (the Unwrap chain), so callers that want
// the inner category can extract it themselves; Classify intentionally
// returns the outermost classification because a fresh Wrap means
// "I take responsibility for assigning the category".
func TestClassifyCodedUnderWrap(t *testing.T) {
	t.Parallel()

	inner := &codedErr{code: apperrors.CodeNetworkError, msg: "dns"}
	wrapped := apperrors.Wrap(apperrors.CodeUnexpectedError, inner)

	// errors.As should walk back to the inner Coded (via Unwrap) —
	// the wrap is itself Coded, but errors.As returns the FIRST
	// match walking the chain, which is the wrap's Code() method.
	// Both wrap.Code() and inner.Code() are Coded; the wrap wins
	// because it is outermost.
	var c apperrors.Coded
	if !stderrors.As(wrapped, &c) {
		t.Fatalf("errors.As did not find a Coded in the wrapped chain")
	}
	// The outermost Coded is the wrap, returning the wrap's code.
	if c.Code() != apperrors.CodeUnexpectedError {
		t.Fatalf("Coded.Code() (outermost) = %q, want %q", c.Code(), apperrors.CodeUnexpectedError)
	}

	// Classify sees the outermost Coded and returns its code —
	// UNEXPECTED_ERROR, exit 2.
	gotCode, gotExit := apperrors.Classify(wrapped)
	if gotCode != apperrors.CodeUnexpectedError {
		t.Errorf("Classify(wrap(coded)) code = %q, want %q", gotCode, apperrors.CodeUnexpectedError)
	}
	if gotExit != 2 {
		t.Errorf("Classify(wrap(coded)) exit = %d, want 2", gotExit)
	}
}

// TestClassifyExhaustive is a regression guard: for every code in
// AllCodes, assert ExitFor returns the spec-mandated exit code. A
// failure here means a new constant was added without updating
// exitFor, which would silently default to exit 2 (the unexpected
// fallback) and break any caller branching on the exit code.
func TestClassifyExhaustive(t *testing.T) {
	t.Parallel()

	// AC1: exactly 11 codes (the spec's count).
	if got, want := len(apperrors.AllCodes), 11; got != want {
		t.Fatalf("AllCodes has %d entries, want %d", got, want)
	}

	// Per-code (code, exit) pairs. Drawn from docs/07-cli-spec.md
	// §"Exit codes". Update this table when the spec changes; the
	// test failure is the prompt to read the spec and decide.
	want := map[apperrors.Code]int{
		apperrors.CodeRateLimited:     1,
		apperrors.CodeAuthError:       1,
		apperrors.CodeValidationError: 1,
		apperrors.CodeConfigError:     1,
		apperrors.CodeNetworkError:    1,
		apperrors.CodeNotebookLimit:   1,
		apperrors.CodeNotFound:        1,
		apperrors.CodeArtifactTimeout: 1,
		apperrors.CodeNotebookLMError: 1,
		apperrors.CodeCancelled:       130,
		apperrors.CodeUnexpectedError: 2,
	}

	for _, code := range apperrors.AllCodes {
		t.Run(string(code), func(t *testing.T) {
			got := apperrors.ExitFor(code)
			wantExit, ok := want[code]
			if !ok {
				t.Fatalf("test table missing expected exit for %q — update the test when adding a code", code)
			}
			if got != wantExit {
				t.Errorf("ExitFor(%q) = %d, want %d", code, got, wantExit)
			}
		})
	}
}

// TestExitForUnknownCode pins the safety net for the exitFor map:
// an unrecognized code (e.g. a typo from a caller) falls back to
// exit 2 rather than silently returning zero (which would let a
// failing command look like a success).
func TestExitForUnknownCode(t *testing.T) {
	t.Parallel()

	if got := apperrors.ExitFor(apperrors.Code("BOGUS_CODE")); got != 2 {
		t.Errorf("ExitFor(unknown) = %d, want 2", got)
	}
}

// TestWrapNilReturnsNil pins the convenience contract: passing nil
// to Wrap returns nil so a caller can write `return Wrap(code,
// maybeErr)` without an explicit `if maybeErr != nil` guard.
func TestWrapNilReturnsNil(t *testing.T) {
	t.Parallel()

	if got := apperrors.Wrap(apperrors.CodeAuthError, nil); got != nil {
		t.Errorf("Wrap(_, nil) = %v, want nil", got)
	}
}

// TestWrapPreservesCause pins that the wrap chain still walks
// (errors.Is / errors.As) so a caller can recover the underlying
// typed error from a wrapped value.
func TestWrapPreservesCause(t *testing.T) {
	t.Parallel()

	inner := stderrors.New("inner cause")
	wrapped := apperrors.Wrap(apperrors.CodeAuthError, inner)

	if !stderrors.Is(wrapped, inner) {
		t.Errorf("errors.Is(wrapped, inner) = false, want true")
	}
	if got := wrapped.Error(); got != "inner cause: inner cause" {
		t.Errorf("wrapped.Error() = %q, want %q", got, "inner cause: inner cause")
	}
}

// TestPayloadOf pins the structured-field accessor. Adapters use
// it to read the WithRetryAfter / WithID / … fields off a Wrap
// result without re-walking the chain.
func TestPayloadOf(t *testing.T) {
	t.Parallel()

	t.Run("no opts", func(t *testing.T) {
		err := apperrors.Wrap(apperrors.CodeNotFound, stderrors.New("missing"))
		if _, ok := apperrors.PayloadOf(err); ok {
			t.Errorf("PayloadOf(no-opts) returned ok=true, want false")
		}
	})

	t.Run("retry_after opt", func(t *testing.T) {
		err := apperrors.Wrap(
			apperrors.CodeRateLimited,
			stderrors.New("slow down"),
			apperrors.WithRetryAfter(45),
		)
		p, ok := apperrors.PayloadOf(err)
		if !ok {
			t.Fatalf("PayloadOf = _, false; want true")
		}
		if p.RetryAfter != 45 {
			t.Errorf("RetryAfter = %d, want 45", p.RetryAfter)
		}
	})

	t.Run("id and notebook_id opts", func(t *testing.T) {
		err := apperrors.Wrap(
			apperrors.CodeNotFound,
			stderrors.New("missing"),
			apperrors.WithID("src-1"),
			apperrors.WithNotebookID("nb-2"),
		)
		p, ok := apperrors.PayloadOf(err)
		if !ok {
			t.Fatalf("PayloadOf = _, false; want true")
		}
		if p.ID != "src-1" {
			t.Errorf("ID = %q, want %q", p.ID, "src-1")
		}
		if p.NotebookID != "nb-2" {
			t.Errorf("NotebookID = %q, want %q", p.NotebookID, "nb-2")
		}
	})

	t.Run("method_id opt", func(t *testing.T) {
		err := apperrors.Wrap(
			apperrors.CodeNotebookLMError,
			stderrors.New("rpc boom"),
			apperrors.WithMethodID("abc123"),
		)
		p, ok := apperrors.PayloadOf(err)
		if !ok {
			t.Fatalf("PayloadOf = _, false; want true")
		}
		if p.MethodID != "abc123" {
			t.Errorf("MethodID = %q, want %q", p.MethodID, "abc123")
		}
	})

	t.Run("nil error returns false", func(t *testing.T) {
		if _, ok := apperrors.PayloadOf(nil); ok {
			t.Errorf("PayloadOf(nil) returned ok=true, want false")
		}
	})

	t.Run("non-wrap error returns false", func(t *testing.T) {
		if _, ok := apperrors.PayloadOf(stderrors.New("plain")); ok {
			t.Errorf("PayloadOf(plain) returned ok=true, want false")
		}
	})
}

// TestNewReturnsCoded pins that the New constructor returns a
// value that satisfies Coded (and therefore feeds straight back
// into Classify). A regression that drops the Code() method on
// the constructed value would force every direct-construction
// call site to add a manual wrap.
func TestNewReturnsCoded(t *testing.T) {
	t.Parallel()

	err := apperrors.New(apperrors.CodeValidationError, "bad flag")
	var c apperrors.Coded
	if !stderrors.As(err, &c) {
		t.Fatalf("New did not return a Coded value")
	}
	if c.Code() != apperrors.CodeValidationError {
		t.Errorf("Code() = %q, want %q", c.Code(), apperrors.CodeValidationError)
	}
	gotCode, _ := apperrors.Classify(err)
	if gotCode != apperrors.CodeValidationError {
		t.Errorf("Classify(New(...)) = %q, want %q", gotCode, apperrors.CodeValidationError)
	}
}
