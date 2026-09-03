// Package internal/cli/cmd holds the leaf Cobra commands every CLI
// subcommand registers on root. The package follows one-file-per-
// command (notebook_list.go, notebook_create.go, …) so each
// command's tests and helpers stay co-located and small.
//
// All command bodies follow the same skeleton:
//
//  1. open a *notebooklm.Client via newClient.
//  2. call the typed NotebooksAPI / SourcesAPI method (resolving
//     name → id via Client.ResolveID where needed).
//  3. emit a JSON envelope (under --json) or a human table
//     (otherwise) and return.
//
// Errors flow back to root.go's HandleRootError via RunE returns;
// no command writes to stderr itself.
package cmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	apperrors "github.com/raihankhan/notebooklm-go/internal/app/errors"
	"github.com/raihankhan/notebooklm-go/internal/app/serialize"
	"github.com/raihankhan/notebooklm-go/notebooklm"
)

// withClient opens a notebooklm.Client and ensures it is closed
// before run returns. The helper exists so every command body is a
// single-line wrapper:
//
//	err := withClient(cmd, ctx, func(c *notebooklm.Client) error {
//	    return c.Notebooks().List(ctx)
//	})
func withClient(cmd *cobra.Command, ctx context.Context, fn func(*notebooklm.Client) error) error {
	client, err := newClient(cmd, ctx)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }() //nolint:contextcheck // SDK Close takes no ctx today.
	return fn(client)
}

// storageOverride returns the --storage flag value (empty string
// when the flag is unset).
func storageOverride(cmd *cobra.Command) string {
	v, _ := cmd.Flags().GetString(flagStorage)
	return v
}

// emitJSON writes a byte-clean JSON envelope to cmd's stdout.
// Under --json the CLI contract is "stdout is the payload,
// stderr is empty"; the helper enforces both invariants.
//
// requestID is forwarded to the envelope so a downstream consumer
// can correlate a CLI invocation with server-side logs.
func emitJSON(cmd *cobra.Command, data any, requestID string) error {
	bytes, err := serialize.MarshalSuccess(data, requestID)
	if err != nil {
		return fmt.Errorf("cmd: marshal envelope: %w", err)
	}
	out := cmd.OutOrStdout()
	if _, werr := out.Write(bytes); werr != nil {
		return werr
	}
	if _, werr := out.Write([]byte("\n")); werr != nil {
		return werr
	}
	return nil
}

// newRequestID returns a fresh 8-byte hex request id for envelope
// correlation. The generator uses crypto/rand so two concurrent CLI
// invocations never share an id; the 16-char output matches the
// Python original's `notebooklm_<8hex>` shape minus the prefix.
func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// rand.Read on Linux/macOS only fails when getrandom is
		// blocked — a degenerate environment. Fall back to a
		// zero id rather than crashing the CLI; the envelope still
		// round-trips.
		return "00000000"
	}
	return hex.EncodeToString(b[:])
}

// errCancelled is the typed sentinel a command surfaces when the
// user aborts with Ctrl-C. The CLI exit code is 130 (128 + 2) per
// docs/07-cli-spec.md; HandleRootError derives the code via
// apperrors.Classify.
//
//nolint:misspell // "cancelled" matches docs/07-cli-spec.md verbatim.
func errCancelled() error {
	return apperrors.Wrap(apperrors.CodeCancelled, errors.New("operation cancelled"))
}

// errUsage returns a VALIDATION_ERROR-classified sentinel so a
// command that wants to flag a user-fixable input problem does
// not need to import apperrors directly.
func errUsage(msg string) error {
	return apperrors.Wrap(apperrors.CodeValidationError, errors.New(msg))
}
