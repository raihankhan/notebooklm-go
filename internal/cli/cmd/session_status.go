// Package cmd — `notebooklm session status` subcommand.
//
// Prints the active-notebook pointer (or a "no active notebook"
// message when context.json does not exist). Under --json the
// empty-state response carries a `set: false` field so a scripted
// caller can branch on it without parsing prose.
package cmd

import (
	"errors"
	"fmt"

	"github.com/raihankhan/notebooklm-go/internal/cligroups"
	"github.com/spf13/cobra"
)

// newSessionStatusCmd returns the `notebooklm session status` subcommand.
func newSessionStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Print the active-notebook pointer (or empty state)",
		Long: `Print the active-notebook pointer from <profile_dir>/context.json.

When no pointer is set the command prints a human "no active
notebook" message or a JSON envelope with set:false (under --json).`,
		SilenceUsage:  true,
		SilenceErrors: true,
		GroupID:       cligroups.Session,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSessionStatus(cmd)
		},
	}
	return cmd
}

// statusPayload is the JSON envelope data for `session status`.
type statusPayload struct {
	Set        bool   `json:"set"`
	NotebookID string `json:"notebook_id,omitempty"`
	Title      string `json:"title,omitempty"`
	Path       string `json:"path,omitempty"`
}

func runSessionStatus(cmd *cobra.Command) error {
	storageFlag := storageOverride(cmd)
	path, err := resolveContextPath(storageFlag)
	if err != nil {
		return err
	}
	doc, err := getActiveNotebook(storageFlag)

	switch {
	case errors.Is(err, errContextNotFound):
		if jsonRequested(cmd) {
			return emitJSON(cmd, statusPayload{Set: false}, newRequestID())
		}
		_, werr := fmt.Fprintln(cmd.OutOrStdout(), "no active notebook (use `notebooklm session use <id>` to set one)")
		return werr

	case err != nil:
		return err
	}

	if jsonRequested(cmd) {
		return emitJSON(cmd, statusPayload{
			Set:        true,
			NotebookID: doc.NotebookID,
			Title:      doc.Title,
			Path:       path,
		}, newRequestID())
	}

	out := cmd.OutOrStdout()
	if doc.Title != "" {
		_, err = fmt.Fprintf(out, "active notebook: %s (%s)\n", doc.NotebookID, doc.Title)
	} else {
		_, err = fmt.Fprintf(out, "active notebook: %s\n", doc.NotebookID)
	}
	return err
}
