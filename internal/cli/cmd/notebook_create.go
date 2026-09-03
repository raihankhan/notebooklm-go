// Package cmd — `notebooklm notebook create` subcommand.
//
// Creates a new notebook with the given title. On success the
// command prints the new notebook id to stdout (human mode) or a
// JSON envelope (under --json) with the typed Notebook view.
package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/raihankhan/notebooklm-go/internal/cligroups"
	"github.com/spf13/cobra"

	"github.com/raihankhan/notebooklm-go/notebooklm"
)

// newNotebookCreateCmd returns the `notebooklm notebook create` subcommand.
func newNotebookCreateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create <title>",
		Short: "Create a new notebook",
		Long: `Create a new notebook with the given title.

The title is required and is the only positional argument. The new
notebook's id is printed to stdout (human mode) or returned in the
JSON envelope's data field.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		GroupID:       cligroups.Notebook,
		Args:          cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			title := strings.TrimSpace(args[0])
			if title == "" {
				return errUsage("notebook title cannot be empty")
			}
			return runNotebookCreate(cmd, title)
		},
	}
	return cmd
}

func runNotebookCreate(cmd *cobra.Command, title string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	return withClient(cmd, ctx, func(c *notebooklm.Client) error {
		nb, err := c.Notebooks().Create(ctx, title)
		if err != nil {
			return err
		}
		if jsonRequested(cmd) {
			return emitJSON(cmd, notebookView(&nb), newRequestID())
		}
		out := cmd.OutOrStdout()
		_, err = fmt.Fprintf(out, "created notebook %s (%q)\n", nb.ID, nb.Title)
		return err
	})
}
