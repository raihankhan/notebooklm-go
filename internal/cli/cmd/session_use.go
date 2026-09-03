// Package cmd — `notebooklm session use` subcommand.
//
// Sets the active-notebook pointer. By default the command
// verifies the notebook exists (a cheap existence check); pass
// --force to skip the verification. Under --json the response
// always carries a `verified` field so a scripted caller can tell
// whether the existence probe actually ran.
package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/raihankhan/notebooklm-go/internal/cligroups"
	"github.com/spf13/cobra"

	apperrors "github.com/raihankhan/notebooklm-go/internal/app/errors"
	"github.com/raihankhan/notebooklm-go/notebooklm"
)

// newSessionUseCmd returns the `notebooklm session use` subcommand.
func newSessionUseCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "use <id|name>",
		Short: "Set the active notebook (writes context.json)",
		Long: `Set the active notebook. The pointer is written to
<profile_dir>/context.json; future notebook-scoped commands that
accept no id read the pointer from this file.

By default the command verifies the notebook exists through a
cheap Get call. Pass --force to skip the probe (useful in
offline / scripting contexts where you trust the id).`,
		SilenceUsage:  true,
		SilenceErrors: true,
		GroupID:       cligroups.Session,
		Args:          cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSessionUse(cmd, args[0], force)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false,
		"skip the existence probe (do not call Get)")
	return cmd
}

// usePayload is the JSON envelope data for `session use`. The
// `verified` field tells a scripted caller whether the existence
// probe actually ran (always present under --json so consumers
// can branch on it without checking the flag set).
type usePayload struct {
	ID       string `json:"id"`
	Title    string `json:"title,omitempty"`
	Verified bool   `json:"verified"`
	Path     string `json:"path,omitempty"`
}

func runSessionUse(cmd *cobra.Command, target string, force bool) error {
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

		verified := false
		title := ""
		if !force {
			nb, gerr := c.Notebooks().Get(ctx, id)
			if gerr != nil {
				// If the Get fails we surface the error
				// rather than silently writing a stale
				// pointer; --force is the escape hatch.
				if errors.Is(gerr, apperrors.ErrNotFound) {
					return apperrors.Wrap(
						apperrors.CodeNotFound,
						fmt.Errorf("notebook %s not found (use --force to skip the probe)", id),
					)
				}
				return gerr
			}
			verified = true
			title = nb.Title
		}

		path, err := setActiveNotebook(storageOverride(cmd), contextDoc{
			NotebookID: id,
			Title:      title,
		})
		if err != nil {
			return err
		}

		if jsonRequested(cmd) {
			payload := usePayload{ID: id, Title: title, Verified: verified}
			if path != "" {
				payload.Path = path
			}
			return emitJSON(cmd, payload, newRequestID())
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(),
			"active notebook set to %s (%s)\n", id, title)
		return err
	})
}
