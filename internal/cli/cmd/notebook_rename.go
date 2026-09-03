// Package cmd — `notebooklm notebook rename` subcommand.
//
// Renames a notebook. The new title is required; the emoji is
// optional (omitted when empty). The argument can be a notebook
// id or resolved name.
package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/raihankhan/notebooklm-go/internal/cligroups"
	"github.com/spf13/cobra"

	"github.com/raihankhan/notebooklm-go/notebooklm"
)

// newNotebookRenameCmd returns the `notebooklm notebook rename` subcommand.
func newNotebookRenameCmd() *cobra.Command {
	var emoji string
	cmd := &cobra.Command{
		Use:   "rename <id|name> <new-title>",
		Short: "Rename a notebook",
		Long: `Rename a notebook.

The first argument is the notebook id (or resolved name); the
second is the new title. Pass --emoji to update the notebook's
display emoji at the same time.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		GroupID:       cligroups.Notebook,
		Args:          cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runNotebookRename(cmd, args[0], args[1], emoji)
		},
	}
	cmd.Flags().StringVar(&emoji, "emoji", "",
		"update the notebook's display emoji (omit to leave unchanged)")
	return cmd
}

func runNotebookRename(cmd *cobra.Command, target, newTitle, emoji string) error {
	query := strings.TrimSpace(target)
	title := strings.TrimSpace(newTitle)
	if query == "" {
		return errUsage("notebook id or name is required")
	}
	if title == "" {
		return errUsage("new title is required")
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	var emojiPtr *string
	if emoji = strings.TrimSpace(emoji); emoji != "" {
		emojiPtr = &emoji
	}

	return withClient(cmd, ctx, func(c *notebooklm.Client) error {
		id, err := c.ResolveID(ctx, query)
		if err != nil {
			return err
		}
		if err := c.Notebooks().Rename(ctx, id, title, emojiPtr); err != nil {
			return err
		}

		if jsonRequested(cmd) {
			payload := map[string]any{
				"id":      id,
				"title":   title,
				"renamed": true,
			}
			if emojiPtr != nil {
				payload["emoji"] = *emojiPtr
			}
			return emitJSON(cmd, payload, newRequestID())
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "renamed notebook %s\n", id)
		return err
	})
}
