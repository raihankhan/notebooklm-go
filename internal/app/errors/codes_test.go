package errors

import (
	"errors"
	"testing"
)

// TestAllCodesHaveExitCodes pins the 11-code → exit-code mapping
// the docs/07-cli-spec.md "Exit codes" table enumerates. A
// regression here is a breaking change for scripted callers that
// key on the numeric code.
func TestAllCodesHaveExitCodes(t *testing.T) {
	cases := []struct {
		code    Code
		exit    int
		meaning string
	}{
		{CodeRateLimit, 1, "rate limit"},
		{CodeAuth, 1, "auth"},
		{CodeValidation, 1, "validation"},
		{CodeConfig, 1, "config"},
		{CodeNetwork, 1, "network"},
		{CodeNotebookLimit, 1, "notebook limit"},
		{CodeNotFound, 1, "not found"},
		{CodeArtifactTimeout, 1, "artifact timeout"},
		{CodeNotebookLM, 1, "library error"},
		{CodeCancelled, 130, "SIGINT"},
		{CodeUnexpected, 2, "system/unexpected"},
	}
	for _, tc := range cases {
		if got := tc.code.ExitCode(); got != tc.exit {
			t.Errorf("Code(%q).ExitCode() = %d, want %d (%s)",
				tc.code, got, tc.exit, tc.meaning)
		}
	}
}

// TestExitCodeForUnknownCode falls back to 2 (system/unexpected).
// A new code that should map to 1 or 130 must add a case in
// ExitCode(); the safe default is 2.
func TestExitCodeForUnknownCode(t *testing.T) {
	if got := Code("NOPE_NOT_REAL").ExitCode(); got != 2 {
		t.Errorf("unknown code ExitCode() = %d, want 2", got)
	}
}

// TestClassifyNilReturnsZero is the defensive case: Classify(nil)
// must not panic and must return (zero, 0). Callers should never
// invoke Classify on a nil error but the contract is no-panic.
func TestClassifyNilReturnsZero(t *testing.T) {
	code, exit := Classify(nil)
	if code != "" {
		t.Errorf("Classify(nil) code = %q, want empty", code)
	}
	if exit != 0 {
		t.Errorf("Classify(nil) exit = %d, want 0", exit)
	}
}

// TestClassifyErrorReturnsUnexpected is the STUB contract: every
// non-nil error maps to UNEXPECTED_ERROR (2) until T-P5-3 lands.
// The test pins the stub behavior so a future replace with the
// real classifier is a deliberate commit, not a silent drift.
func TestClassifyErrorReturnsUnexpected(t *testing.T) {
	err := errors.New("anything")
	code, exit := Classify(err)
	if code != CodeUnexpected {
		t.Errorf("Classify(err) code = %q, want %q (STUB contract)", code, CodeUnexpected)
	}
	if exit != 2 {
		t.Errorf("Classify(err) exit = %d, want 2", exit)
	}
}

// TestCodeValuesAreStable pins the string values of the 11 codes.
// These are part of the public CLI/MCP/REST contract — automation
// branches on the literal strings.
func TestCodeValuesAreStable(t *testing.T) {
	cases := map[Code]string{
		CodeRateLimit:       "RATE_LIMITED",
		CodeAuth:            "AUTH_ERROR",
		CodeValidation:      "VALIDATION_ERROR",
		CodeConfig:          "CONFIG_ERROR",
		CodeNetwork:         "NETWORK_ERROR",
		CodeNotebookLimit:   "NOTEBOOK_LIMIT",
		CodeNotFound:        "NOT_FOUND",
		CodeArtifactTimeout: "ARTIFACT_TIMEOUT",
		CodeNotebookLM:      "NOTEBOOKLM_ERROR",
		CodeCancelled:       "CANCELLED", //nolint:misspell // docs/07-cli-spec.md canonical.
		CodeUnexpected:      "UNEXPECTED_ERROR",
	}
	for code, want := range cases {
		if string(code) != want {
			t.Errorf("Code value = %q, want %q", string(code), want)
		}
	}
}
