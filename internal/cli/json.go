// Package internal/cli — --json parse-time interception.
//
// Cobra's default flag-error path prints a usage dump to stderr
// and exits 2 with an "Error: " prefix. The notebooklm CLI cannot
// use that path under --json because:
//
//  1. The usage dump mixes a usage dump with the error message,
//     breaking any consumer that pipes stdout into jq (the
//     envelope would be missing because the failure happened at
//     parse time, leaving the caller with NO envelope and a
//     non-zero exit, which is unrecoverable).
//  2. The exit code is uniform; docs/07-cli-spec.md splits parse-
//     time failures into "usage" (exit 2) and "flag-value" (exit 1).
//
// This file installs a FlagErrorFunc on the root command that
// wraps Cobra's behavior with the JSON-aware envelope output and
// the right exit code. The root error handler (errors.go) does
// the same thing for runtime errors.
//
// References:
//
//   - docs/07-cli-spec.md "Parse-time errors are wrapped too".
//   - docs/07-cli-spec.md "Exit codes" — the per-error-type mapping.
//   - spf13/cobra documentation on FlagErrorFunc.
package cli

import (
	"os"

	"github.com/spf13/cobra"
)

// wireJSONInterceptor installs the FlagErrorFunc on cmd so every
// flag-parse failure routes through the JSON-aware error path.
// Called once from NewRootCmd.
func wireJSONInterceptor(cmd *cobra.Command) {
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		// The parse-time error becomes a parseError carrying the
		// right exit code. HandleRootError unwraps it and emits
		// the JSON envelope (or human message) per --json.
		return &parseError{
			inner: err,
			msg:   err.Error(),
			code:  "VALIDATION_ERROR",
		}
	})
}

// parseError is the wrapper Cobra receives from wireJSONInterceptor
// and HandleRootError unwraps. The struct is private — callers
// outside this package must never construct one; the FlagErrorFunc
// is the only entry point.
type parseError struct {
	inner error
	msg   string
	code  string
}

// Error implements error. Returns the human-readable message so
// HandleRootError can use it directly.
func (e *parseError) Error() string { return e.msg }

// Unwrap exposes the underlying Cobra error for errors.Is /
// errors.As chains (and for the ExitCodeForParseError path in
// exit_codes.go, which inspects err.Error()).
func (e *parseError) Unwrap() error { return e.inner }

// JSONRequested reports whether --json was set on cmd's flag set,
// honoring the per-command vs. persistent distinction from
// docs/07-cli-spec.md: --json is per-command, not persistent, so
// the lookup walks cmd.Flags() (not cmd.PersistentFlags()).
//
// The function also honors the NOTEBOOKLM_OUTPUT=json env var, so a
// caller that wants machine-readable output can set the env without
// editing the command line.
func JSONRequested(cmd *cobra.Command) bool {
	if f := cmd.Flags().Lookup("json"); f != nil {
		if f.Value.Type() == "bool" {
			v, _ := cmd.Flags().GetBool("json")
			if v {
				return true
			}
		}
	}
	// Per-command flag not set: try the env override.
	return jsonOutputEnv()
}

// jsonOutputEnv mirrors the Python original's
// "NOTEBOOKLM_OUTPUT=json" convention. The single env var is the
// knob a wrapper script uses to opt into machine-readable output
// without parsing the flag list itself.
func jsonOutputEnv() bool {
	for _, env := range []string{"NOTEBOOKLM_OUTPUT"} {
		if v := os.Getenv(env); v == "json" {
			return true
		}
	}
	return false
}
