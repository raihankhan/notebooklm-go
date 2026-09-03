// Package cmd — `notebooklm notebook delete` subcommand.
//
// Deletes a notebook by id (or resolved name). Per docs/AGENTS.md
// rule 6, destructive commands require `-y/--yes` confirmation.
// Under --json the command refuses to prompt and exits 1 with
// VALIDATION_ERROR when --yes is missing so a CI runner does not
// hang.
package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/raihankhan/notebooklm-go/internal/cligroups"
	"github.com/spf13/cobra"

	"github.com/raihankhan/notebooklm-go/notebooklm"
)

// newNotebookDeleteCmd returns the `notebooklm notebook delete` subcommand.
func newNotebookDeleteCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "delete <id|name>",
		Short: "Delete a notebook (requires --yes)",
		Long: `Delete a notebook by id or name.

The argument can be a notebook id (preferred), a name, or an
unambiguous prefix. Names are resolved through Client.ResolveID.

Under --json the command refuses to prompt even on a TTY; the
user must pass --yes to confirm deletion.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		GroupID:       cligroups.Notebook,
		Args:          cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runNotebookDelete(cmd, args[0], yes)
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false,
		"skip the confirmation prompt (required under --json)")
	return cmd
}

func runNotebookDelete(cmd *cobra.Command, target string, yes bool) error {
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

		// Confirm: under --json we never prompt; missing --yes
		// exits with VALIDATION_ERROR so a CI runner does not
		// hang on the prompt.
		if !yes {
			if jsonRequested(cmd) {
				return errUsage(
					"delete requires --yes under --json (no interactive prompts)",
				)
			}
			if !confirmDelete(id) {
				return errCancelled()
			}
		}

		if err := c.Notebooks().Delete(ctx, id); err != nil {
			return err
		}

		if jsonRequested(cmd) {
			payload := map[string]any{
				"id":      id,
				"deleted": true,
			}
			return emitJSON(cmd, payload, newRequestID())
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "deleted notebook %s\n", id)
		return err
	})
}

// confirmDelete prints a single-line prompt and reads one line
// from stdin. Only invoked on a TTY + non-json path; the function
// reads from os.Stdin which the root command wires in
// ExecuteContext.
func confirmDelete(id string) bool {
	prompt := fmt.Sprintf("delete notebook %s? [y/N] ", id)
	fmt.Fprint(os.Stderr, prompt)
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return false
	}
	ans := strings.ToLower(strings.TrimSpace(scanner.Text()))
	return ans == "y" || ans == "yes"
}
