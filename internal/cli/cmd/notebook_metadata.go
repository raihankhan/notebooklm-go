// Package cmd — `notebooklm notebook metadata` subcommand.
//
// Returns the per-notebook metadata block (role, last_viewed_at,
// emoji, sources_count, …). Useful for scripting without
// fetching the full notebook row.
package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/raihankhan/notebooklm-go/internal/cligroups"
	"github.com/spf13/cobra"

	"github.com/raihankhan/notebooklm-go/notebooklm"
)

// newNotebookMetadataCmd returns the `notebooklm notebook metadata` subcommand.
func newNotebookMetadataCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "metadata <id|name>",
		Short: "Print a notebook's metadata block",
		Long: `Print the per-notebook metadata block: role, last_viewed_at,
emoji, sources_count, and created_at.

The argument can be a notebook id (preferred) or a resolved name.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		GroupID:       cligroups.Notebook,
		Args:          cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runNotebookMetadata(cmd, args[0])
		},
	}
	return cmd
}

// metadataPayload is the JSON envelope data for `notebook metadata`.
// The metadata block is passed through verbatim from the SDK so
// future fields land without churning this command.
type metadataPayload struct {
	ID       string              `json:"id"`
	Metadata notebooklm.Metadata `json:"metadata"`
}

func runNotebookMetadata(cmd *cobra.Command, target string) error {
	query := strings.TrimSpace(target)
	if query == "" {
		return errUsage("notebook id or name is required")
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	return withClient(cmd, ctx, func(c *notebooklm.Client) error {
		id, err := c.ResolveID(ctx, query)
		if err != nil {
			return err
		}
		meta, err := c.Notebooks().Metadata(ctx, id)
		if err != nil {
			return err
		}

		if jsonRequested(cmd) {
			return emitJSON(cmd, metadataPayload{ID: id, Metadata: meta}, newRequestID())
		}

		out := cmd.OutOrStdout()
		_, err = fmt.Fprintf(out, "notebook %s\n", id)
		if err != nil {
			return err
		}
		if meta.Role != nil {
			_, err = fmt.Fprintf(out, "  role:           %d\n", *meta.Role)
			if err != nil {
				return err
			}
		}
		if meta.LastViewedAt != nil && !meta.LastViewedAt.IsZero() {
			_, err = fmt.Fprintf(out, "  last_viewed_at: %s\n", meta.LastViewedAt.Format("2006-01-02T15:04:05Z07:00"))
			if err != nil {
				return err
			}
		}
		if meta.CreatedAt != nil && !meta.CreatedAt.IsZero() {
			_, err = fmt.Fprintf(out, "  created_at:     %s\n", meta.CreatedAt.Format("2006-01-02T15:04:05Z07:00"))
			if err != nil {
				return err
			}
		}
		if meta.Emoji != nil {
			_, err = fmt.Fprintf(out, "  emoji:          %s\n", *meta.Emoji)
			if err != nil {
				return err
			}
		}
		_, err = fmt.Fprintf(out, "  sources_count:  %d\n", meta.SourcesCount)
		return err
	})
}
