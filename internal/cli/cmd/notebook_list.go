// Package cmd — `notebooklm notebook list` subcommand.
//
// Lists every notebook the active profile can see. The output is
// either a JSON envelope (under --json) or a human table (otherwise).
//
// JSON envelope shape (under --json):
//
//	{ "ok": true, "data": { "items": [...], "has_more": false }, "request_id": "..." }
//
// The data.items slice is a JSON array of {id, title, summary,
// metadata} rows. Empty accounts emit a header-only table (or an
// envelope with items:[]); the CLI does not raise a NOT_FOUND for
// an empty list (that is a valid state).
package cmd

import (
	"context"
	"fmt"

	"github.com/raihankhan/notebooklm-go/internal/cligroups"
	"github.com/spf13/cobra"

	"github.com/raihankhan/notebooklm-go/internal/app/serialize"
	"github.com/raihankhan/notebooklm-go/notebooklm"
)

// newNotebookListCmd returns the `notebooklm notebook list` subcommand.
func newNotebookListCmd() *cobra.Command {
	var maxItems int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List every notebook the active profile can see",
		Long: `List every notebook the active profile can see.

Under --json the output is a JSON envelope whose data field carries
` + "`{items, has_more, next_offset}`" + `. Without --json the output
is a human-aligned table with columns ID, Title, Status.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		GroupID:       cligroups.Notebook,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runNotebookList(cmd, maxItems)
		},
	}
	cmd.Flags().IntVar(&maxItems, "max-items", 0,
		"cap on the number of items returned (0 = no cap)")
	return cmd
}

// notebookListItem is the JSON row shape for --json mode.
type notebookListItem struct {
	ID       string               `json:"id"`
	Title    string               `json:"title"`
	Summary  string               `json:"summary,omitempty"`
	Metadata *notebooklm.Metadata `json:"metadata,omitempty"`
}

// notebookListPayload is the envelope's data field for list. The
// shape mirrors the Page[Notebook] view the SDK returns so a
// future paged RPC can land without renaming call sites.
type notebookListPayload struct {
	Items      []notebookListItem `json:"items"`
	HasMore    bool               `json:"has_more"`
	NextOffset string             `json:"next_offset,omitempty"`
}

func runNotebookList(cmd *cobra.Command, maxItems int) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	return withClient(cmd, ctx, func(c *notebooklm.Client) error {
		var opts []notebooklm.NotebooksOption
		if maxItems > 0 {
			opts = append(opts, notebooklm.WithMaxItems(maxItems))
		}
		page, err := c.Notebooks().List(ctx, opts...)
		if err != nil {
			return err
		}

		if jsonRequested(cmd) {
			items := make([]notebookListItem, 0, len(page.Items))
			for _, n := range page.Items {
				items = append(items, notebookListItem{
					ID:       n.ID,
					Title:    n.Title,
					Summary:  n.Summary,
					Metadata: n.Metadata,
				})
			}
			payload := notebookListPayload{
				Items:      items,
				HasMore:    page.HasMore,
				NextOffset: page.NextOffset,
			}
			return emitJSON(cmd, payload, newRequestID())
		}

		// Human mode: build a text-mode table from the items.
		rows := make([][]string, 0, len(page.Items))
		for _, n := range page.Items {
			title := n.Title
			if title == "" {
				title = "(untitled)"
			}
			rows = append(rows, []string{n.ID, title, "ready"})
		}
		t := serialize.Table{
			Columns: []string{"ID", "Title", "Status"},
			Rows:    rows,
		}
		for _, line := range serialize.RenderTable(t, !serialize.IsStdoutTTY(), serialize.IsStdoutTTY()) {
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), line); err != nil {
				return err
			}
		}
		return nil
	})
}
