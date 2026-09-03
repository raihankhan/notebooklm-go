// Package cmd — command registration.
//
// Register wires every leaf Cobra command onto the supplied root
// command. The function is the single seam internal/cli/root.go
// calls; adding a new subcommand is a two-step process:
//
//  1. add the leaf constructor in its own file (notebook_*.go,
//     session_*.go, …)
//  2. add the constructor to the relevant register call below.
//
// The registration order is the same as docs/07-cli-spec.md
// "Command tree" so `notebooklm --help` renders the groups in the
// spec-canonical order.
package cmd

import (
	"github.com/spf13/cobra"

	"github.com/raihankhan/notebooklm-go/internal/cligroups"
)

// Register wires every leaf command onto root. Called once from
// internal/cli.NewRootCmd (T-P5-8).
func Register(root *cobra.Command) {
	// Session bin: use / status / clear
	root.AddCommand(newSessionUseCmd())
	root.AddCommand(newSessionStatusCmd())
	root.AddCommand(newSessionClearCmd())

	// Notebook bin: list / create / delete / rename / summary / metadata
	notebook := newNotebookCmd()
	notebook.AddCommand(
		newNotebookListCmd(),
		newNotebookCreateCmd(),
		newNotebookDeleteCmd(),
		newNotebookRenameCmd(),
		newNotebookSummaryCmd(),
		newNotebookMetadataCmd(),
	)
	root.AddCommand(notebook)

	// Profile bin: list / create / switch / delete / rename
	root.AddCommand(newProfileCmd())

	// Auth bin: check
	auth := newAuthCmd()
	auth.AddCommand(newAuthCheckCmd())
	root.AddCommand(auth)
}

// newNotebookCmd returns the `notebooklm notebook` parent command.
// The six leaf subcommands (list/create/delete/rename/summary/
// metadata) are attached in Register.
//
// Cobra's group enforcement walks every parent's commands and
// calls `parent.ContainsGroup(sub.GroupID)`. The parent must
// declare the same groups the leaves reference — duplicating
// them here is the standard workaround.
func newNotebookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "notebook",
		Short: "Manage notebooks (list / create / delete / rename / summary / metadata)",
		Long: `Manage notebooks. The leaf subcommands map directly onto
the typed NotebooksAPI methods; see docs/07-cli-spec.md "Notebook
commands" for the per-command contract.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.AddGroup(&cobra.Group{ID: cligroups.Notebook, Title: "Notebook"})
	return cmd
}

// newAuthCmd returns the `notebooklm auth` parent command.
// Only the `check` subcommand ships in T-P5-8; `login` /
// `import-cookies` / `refresh` / `logout` land in follow-ups.
func newAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "auth",
		Short:         "Auth commands (check today; login / refresh / logout coming)",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.AddGroup(&cobra.Group{ID: cligroups.Auth, Title: "Auth"})
	return cmd
}
