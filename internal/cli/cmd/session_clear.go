// Package cmd — `notebooklm session clear` subcommand.
//
// Clears the active-notebook pointer by removing context.json
// (or by rewriting it without the notebook_id key). The command
// is idempotent: clearing a session that is already empty exits 0.
package cmd

import (
	"fmt"

	"github.com/raihankhan/notebooklm-go/internal/cligroups"
	"github.com/spf13/cobra"
)

// newSessionClearCmd returns the `notebooklm session clear` subcommand.
func newSessionClearCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "clear",
		Short: "Clear the active-notebook pointer",
		Long: `Clear the active-notebook pointer.

The command is idempotent: clearing a session that is already
empty exits 0 without an error.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		GroupID:       cligroups.Session,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSessionClear(cmd)
		},
	}
	return cmd
}

// clearPayload is the JSON envelope data for `session clear`.
type clearPayload struct {
	Cleared bool `json:"cleared"`
}

func runSessionClear(cmd *cobra.Command) error {
	if err := clearActiveNotebook(storageOverride(cmd)); err != nil {
		return err
	}

	if jsonRequested(cmd) {
		return emitJSON(cmd, clearPayload{Cleared: true}, newRequestID())
	}
	_, err := fmt.Fprintln(cmd.OutOrStdout(), "active notebook cleared")
	return err
}
