package cli

import (
	"errors"
	"testing"
)

// TestExitCodeConstants pins the numeric exit codes the docs
// enumerate. A regression here is a breaking change for scripted
// callers.
func TestExitCodeConstants(t *testing.T) {
	cases := []struct {
		name string
		got  int
		want int
	}{
		{"ExitOK", ExitOK, 0},
		{"ExitUserError", ExitUserError, 1},
		{"ExitSystemError", ExitSystemError, 2},
		{"ExitCancelled", ExitCancelled, 130},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, tc.got, tc.want)
		}
	}
}

// TestExitCodeFromClassifiedTable pins the code → exit-code
// mapping the root error handler (errors.go) consults for every
// non-nil runtime error.
func TestExitCodeFromClassifiedTable(t *testing.T) {
	cases := []struct {
		code string
		want int
	}{
		{"RATE_LIMITED", ExitUserError},
		{"AUTH_ERROR", ExitUserError},
		{"VALIDATION_ERROR", ExitUserError},
		{"CONFIG_ERROR", ExitUserError},
		{"NETWORK_ERROR", ExitUserError},
		{"NOTEBOOK_LIMIT", ExitUserError},
		{"NOT_FOUND", ExitUserError},
		{"ARTIFACT_TIMEOUT", ExitUserError},
		{"NOTEBOOKLM_ERROR", ExitUserError},
		{"CANCELLED", ExitCancelled}, //nolint:misspell // docs/07-cli-spec.md canonical.
		{"UNEXPECTED_ERROR", ExitSystemError},
		{"UNKNOWN_FUTURE_CODE", ExitUserError},
	}
	for _, tc := range cases {
		if got := ExitCodeFromClassified(tc.code); got != tc.want {
			t.Errorf("ExitCodeFromClassified(%q) = %d, want %d", tc.code, got, tc.want)
		}
	}
}

// TestExitCodeForParseError distinguishes the two parse-time
// failure modes docs/07-cli-spec.md "Exit codes" enumerates:
// "usage" → 2, "other flag error" → 1.
func TestExitCodeForParseError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, ExitOK},
		{"usage: unknown command", errors.New("unknown command \"foo\""), ExitSystemError},
		{"usage: unknown flag", errors.New("unknown flag: --bogus"), ExitSystemError},
		{"usage: missing required", errors.New("required flag(s) missing"), ExitSystemError},
		{"usage: accepts N", errors.New("accepts 1 arg(s), received 0"), ExitSystemError},
		{"flag-value: bad value", errors.New("invalid value \"x\" for flag --backend"), ExitUserError},
	}
	for _, tc := range cases {
		if got := ExitCodeForParseError(tc.err); got != tc.want {
			t.Errorf("%s: ExitCodeForParseError = %d, want %d", tc.name, got, tc.want)
		}
	}
}
