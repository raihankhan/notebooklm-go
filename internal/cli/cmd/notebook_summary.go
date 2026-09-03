// Package cmd — `notebooklm notebook summary` subcommand.
//
// Returns the AI-generated summary for a notebook. With --topics
// the suggested-topics list is included as well; without it
// only the summary text is returned.
package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/raihankhan/notebooklm-go/internal/cligroups"
	"github.com/spf13/cobra"

	"github.com/raihankhan/notebooklm-go/notebooklm"
)

// newNotebookSummaryCmd returns the `notebooklm notebook summary` subcommand.
func newNotebookSummaryCmd() *cobra.Command {
	var withTopics bool
	cmd := &cobra.Command{
		Use:   "summary <id|name>",
		Short: "Print a notebook's AI-generated summary",
		Long: `Print the AI-generated summary for a notebook.

The argument can be a notebook id (preferred) or a resolved name.
With --topics the suggested-topics list is included as well.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		GroupID:       cligroups.Notebook,
		Args:          cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runNotebookSummary(cmd, args[0], withTopics)
		},
	}
	cmd.Flags().BoolVar(&withTopics, "topics", false,
		"include the suggested-topics list in the response")
	return cmd
}

// summaryTopicJSON is the JSON row shape the --topics list uses.
// Mirrors the typed notebooklm.Topic with snake_case keys.
type summaryTopicJSON struct {
	Question string `json:"question"`
	Prompt   string `json:"prompt"`
}

// summaryPayload is the JSON envelope data for `notebook summary`.
type summaryPayload struct {
	ID              string             `json:"id"`
	Summary         string             `json:"summary"`
	SuggestedTopics []summaryTopicJSON `json:"suggested_topics,omitempty"`
}

func runNotebookSummary(cmd *cobra.Command, target string, withTopics bool) error {
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

		// Without --topics, fetch only the summary by discarding
		// the topics slot server-side is not currently exposed;
		// we accept the topics array and filter on the client.
		sum, err := c.Notebooks().Summary(ctx, id)
		if err != nil {
			return err
		}

		payload := summaryPayload{
			ID:      id,
			Summary: sum.Summary,
		}
		if withTopics {
			payload.SuggestedTopics = make([]summaryTopicJSON, 0, len(sum.SuggestedTopics))
			for _, t := range sum.SuggestedTopics {
				payload.SuggestedTopics = append(payload.SuggestedTopics, summaryTopicJSON{
					Question: t.Question,
					Prompt:   t.Prompt,
				})
			}
		}

		if jsonRequested(cmd) {
			return emitJSON(cmd, payload, newRequestID())
		}

		out := cmd.OutOrStdout()
		if _, err := fmt.Fprintln(out, payload.Summary); err != nil {
			return err
		}
		if withTopics && len(payload.SuggestedTopics) > 0 {
			if _, err := fmt.Fprintln(out, ""); err != nil {
				return err
			}
			for i, t := range payload.SuggestedTopics {
				if _, err := fmt.Fprintf(out, "%d. %s\n", i+1, t.Question); err != nil {
					return err
				}
				if _, err := fmt.Fprintf(out, "   %s\n", t.Prompt); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
