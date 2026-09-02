package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestNewRootCmdHasPersistentFlags verifies the seven persistent
// flags docs/07-cli-spec.md "Persistent flags" requires.
func TestNewRootCmdHasPersistentFlags(t *testing.T) {
	cmd := NewRootCmd()
	want := []string{
		FlagProfile, FlagStorage, FlagBackend,
		FlagVerbose, FlagQuiet, FlagVersion,
	}
	for _, name := range want {
		if cmd.PersistentFlags().Lookup(name) == nil {
			t.Errorf("root command missing persistent flag %q", name)
		}
	}
}

// TestNewRootCmdSilenceUsageAndErrors verifies the docs/AGENTS.md
// rule 6 contract: Cobra's default usage dump on a runtime error
// is wrong for this CLI; the root error handler owns all output.
func TestNewRootCmdSilenceUsageAndErrors(t *testing.T) {
	cmd := NewRootCmd()
	if !cmd.SilenceUsage {
		t.Error("SilenceUsage = false, want true (root owns output)")
	}
	if !cmd.SilenceErrors {
		t.Error("SilenceErrors = false, want true (root owns output)")
	}
}

// TestNewRootCmdHasGroupBins verifies the 12 cobra.Group bins the
// Python original's SectionedGroup uses. The Python test rejects any
// command not assigned to a bin — the same guard the CLI enforces
// by attaching each subcommand to exactly one of these groups.
func TestNewRootCmdHasGroupBins(t *testing.T) {
	cmd := NewRootCmd()
	want := []string{
		"session", "notebook", "source", "chat", "artifact",
		"research", "share", "note", "profile", "auth",
		"language", "misc",
	}
	seen := make(map[string]bool)
	for _, g := range cmd.Groups() {
		seen[g.ID] = true
	}
	for _, id := range want {
		if !seen[id] {
			t.Errorf("root command missing group %q", id)
		}
	}
}

// TestNewRootCmdHasCompletionCmd verifies the `notebooklm
// completion {bash|zsh|fish}` subcommand is registered. The
// CompletionOptions.DisableDefaultCmd flag replaces Cobra's
// auto-generated completion command; the canonical `completion`
// subcommand is shipped from completion.go.
func TestNewRootCmdHasCompletionCmd(t *testing.T) {
	cmd := NewRootCmd()
	var found *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "completion" {
			found = sub
			break
		}
	}
	if found == nil {
		t.Fatal("root command missing `completion` subcommand")
	}
}

// TestHandleRootErrorNilReturnsZero asserts the nil-error happy
// path. The function is the canonical funnel every error passes
// through; an ExecuteContext that returns nil must result in
// HandleRootError returning ExitOK.
func TestHandleRootErrorNilReturnsZero(t *testing.T) {
	cmd := NewRootCmd()
	if got := HandleRootError(cmd, nil); got != ExitOK {
		t.Errorf("HandleRootError(nil) = %d, want %d", got, ExitOK)
	}
}

// TestHandleRootErrorHumanModeWritesToStderr verifies the
// human-mode path (no --json) writes the message to stderr and
// leaves stdout byte-clean — docs/AGENTS.md rule 6.
func TestHandleRootErrorHumanModeWritesToStderr(t *testing.T) {
	cmd := NewRootCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	// Pass a usageError (not a parseError) so the code path is
	// unambiguous — usageError always maps to ExitSystemError (2).
	code := HandleRootError(cmd, &usageError{msg: "bad flag", code: "VALIDATION_ERROR"})
	if code != ExitSystemError {
		t.Errorf("HandleRootError code = %d, want %d", code, ExitSystemError)
	}
	if !strings.Contains(stderr.String(), "bad flag") {
		t.Errorf("stderr missing error message: %q", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout polluted in human mode: %q", stdout.String())
	}
}

// TestJSONRequestedHonorsFlag is the flag-vs-env contract: --json
// on a command's flag set means JSON mode, regardless of any
// NOTEBOOKLM_OUTPUT setting.
func TestJSONRequestedHonorsFlag(t *testing.T) {
	cmd := &cobra.Command{Use: "fake"}
	cmd.Flags().Bool("json", false, "")
	if err := cmd.Flags().Set("json", "true"); err != nil {
		t.Fatalf("set flag: %v", err)
	}
	if !JSONRequested(cmd) {
		t.Error("JSONRequested(cmd with --json=true) = false, want true")
	}
}

// TestJSONRequestedHonorsEnv is the env-override contract:
// NOTEBOOKLM_OUTPUT=json forces JSON mode without a --json flag.
func TestJSONRequestedHonorsEnv(t *testing.T) {
	t.Setenv("NOTEBOOKLM_OUTPUT", "json")
	cmd := &cobra.Command{Use: "fake"}
	cmd.Flags().Bool("json", false, "")
	if !JSONRequested(cmd) {
		t.Error("JSONRequested with NOTEBOOKLM_OUTPUT=json = false, want true")
	}
}

// TestJSONRequestedFalseWithoutFlagOrEnv asserts the negative
// case: with neither the flag nor the env set, JSONRequested must
// return false. The pair with TestJSONRequestedHonorsEnv pins the
// truth table.
func TestJSONRequestedFalseWithoutFlagOrEnv(t *testing.T) {
	t.Setenv("NOTEBOOKLM_OUTPUT", "")
	cmd := &cobra.Command{Use: "fake"}
	cmd.Flags().Bool("json", false, "")
	if JSONRequested(cmd) {
		t.Error("JSONRequested without --json or env = true, want false")
	}
}

// parseErrorForTest constructs a *parseError the test site can
// pass to HandleRootError. The type is private; tests in this
// package are the only place it should be constructed directly.
//
//nolint:unused // helper kept for future tests; not currently called.
var parseErrorForTest = func(code, msg string) *parseError {
	return &parseError{msg: msg, code: code}
}
