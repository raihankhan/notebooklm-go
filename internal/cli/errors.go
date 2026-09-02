// Package internal/cli — root-level error handler.
//
// Cobra commands return errors from RunE; the convention is to
// bubble them up to main(), which calls cmd.Execute() and exits
// with a non-zero code on error. That default behavior is wrong
// for this CLI because:
//
//   - docs/07-cli-spec.md "Parse-time errors are wrapped too" —
//     every error must produce a JSON envelope under --json, and
//     a typed exit code (1 / 2 / 130). Cobra's default only emits
//     a stderr line and exits 1.
//   - docs/07-cli-spec.md "Exit codes" — the exit code is derived
//     from the error's typed code, not from a uniform "any error"
//     verdict.
//   - docs/AGENTS.md rule 6 — "stdout is the payload; stderr is the
//     narration". Cobra's default mixes the error into the same
//     stream the help/usage dump goes to; a --json consumer would
//     see the error in stderr but no envelope in stdout.
//
// HandleRootError is the canonical funnel. It is called by
// ExecuteContext on every Execute error and by the parseError
// FlagErrorFunc on every flag-parse failure. It classifies the
// error via internal/app/errors.Classify and emits either a JSON
// envelope (under --json) or a human message (otherwise), then
// returns the numeric exit code the caller passes to os.Exit.
//
// Reference: docs/07-cli-spec.md "The --json error envelope" +
// "Exit codes" — the spec this file implements verbatim.
package cli

import (
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	apperrors "github.com/raihankhan/notebooklm-go/internal/app/errors"
	"github.com/raihankhan/notebooklm-go/internal/app/serialize"
)

// HandleRootError is the single funnel every error path passes
// through. It emits either the JSON envelope (under --json) or a
// human message, and returns the numeric exit code the caller
// passes to os.Exit.
//
// The function is called by:
//
//   - ExecuteContext, after cmd.ExecuteContext returns a non-nil
//     error — runtime errors from RunE.
//   - the FlagErrorFunc installed by wireJSONInterceptor (json.go)
//     — the wrapped parseError travels back through ExecuteContext.
//
// The bool return is the "should print usage?" verdict. Today it
// is always false (root owns output); the signature is left open
// so a future per-command override can opt back into Cobra's
// usage dump.
func HandleRootError(cmd *cobra.Command, err error) int {
	if err == nil {
		return ExitOK
	}

	// Parse-time errors carry a sentinel we can match on. The
	// JSON-aware path emits the envelope; the human path prints
	// the message to stderr.
	var perr *parseError
	if errors.As(err, &perr) {
		return emitError(cmd, perr.code, perr.msg, ExitCodeForParseError(perr.inner))
	}

	// The --quiet/-v mutual-exclusion sentinel from root.go is a
	// usageError; same envelope shape, exit code 2.
	var uerr *usageError
	if errors.As(err, &uerr) {
		return emitError(cmd, uerr.code, uerr.msg, ExitSystemError)
	}

	// Otherwise: classify the error via internal/app/errors.
	// Classify is the single funnel the typed-error vocabulary
	// (T-P5-3) and the stub (T-P5-7) share; both return (Code,
	// exitCode). Today the stub returns UNEXPECTED_ERROR for every
	// non-nil err — T-P5-3 will restore the type-sensitive ladder.
	code, exitCode := apperrors.Classify(err)
	return emitError(cmd, string(code), err.Error(), exitCode)
}

// emitError writes the error to the right stream. Under --json
// the envelope goes to stdout (byte-clean) and stderr is empty;
// otherwise the human message goes to stderr and stdout is empty.
//
// The exit code is returned unchanged.
func emitError(cmd *cobra.Command, code, message string, exitCode int) int {
	out := cmd.OutOrStdout()
	errOut := cmd.ErrOrStderr()
	if out == nil {
		out = io.Discard
	}
	if errOut == nil {
		errOut = io.Discard
	}

	if JSONRequested(cmd) {
		// --json mode: envelope to stdout, nothing on stderr.
		// marshalErrorViaEnvelope swallows credential-shaped
		// substrings via the redaction funnel the serialize
		// package exposes; the wire adapter encodes with
		// SetEscapeHTML(false) and trims the trailing newline.
		bytes, marshalErr := serialize.MarshalError(code, message, "")
		if marshalErr != nil {
			// Fall back to a hand-rolled envelope so a
			// marshal failure (which should never happen on
			// the current shape) does not leave stdout empty.
			_, _ = fmt.Fprintf(errOut, `{"error":true,"code":"UNEXPECTED_ERROR","message":"json marshal failed: %s"}`+"\n", marshalErr)
			return ExitSystemError
		}
		_, _ = out.Write(bytes)
		_, _ = out.Write([]byte("\n"))
		return exitCode
	}

	// Human mode: print the message to stderr (per docs/AGENTS.md
	// rule 6 — stderr is the narration channel).
	_, _ = fmt.Fprintln(errOut, message)
	return exitCode
}
