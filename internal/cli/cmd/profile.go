// Package cmd — `notebooklm profile` subcommand group.
//
// Profiles are the named auth-storage directories the CLI keeps
// under <home>/profiles/<name>/. The commands here manage the
// profile lifecycle (list / create / switch / delete / rename)
// and are local-only — they do not open a notebooklm.Client.
//
// The "active profile" is tracked in <home>/config.json's
// "active_profile" key (notebooklm-py keeps the same shape).
package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/raihankhan/notebooklm-go/internal/cligroups"
	"github.com/spf13/cobra"

	apperrors "github.com/raihankhan/notebooklm-go/internal/app/errors"
	"github.com/raihankhan/notebooklm-go/internal/config"
	"github.com/raihankhan/notebooklm-go/internal/paths"
)

// newProfileCmd returns the `notebooklm profile` parent command.
// The five leaf subcommands are attached here so `notebooklm
// profile --help` lists them.
func newProfileCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Manage named profiles (list / create / switch / delete / rename)",
		Long: `Manage named profiles.

Every profile is a directory under <home>/profiles/<name>/ holding
its own storage_state.json. The active profile is recorded in
<home>/config.json's "active_profile" key.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		GroupID:       cligroups.Profile,
	}
	// Cobra's group enforcement walks every parent and calls
	// parent.ContainsGroup(sub.GroupID); the parent must declare
	// the same group its leaves reference.
	cmd.AddGroup(&cobra.Group{ID: cligroups.Profile, Title: "Profile"})
	cmd.AddCommand(
		newProfileListCmd(),
		newProfileCreateCmd(),
		newProfileSwitchCmd(),
		newProfileDeleteCmd(),
		newProfileRenameCmd(),
	)
	return cmd
}

// profileRow is the JSON row shape for `profile list`.
type profileRow struct {
	Name   string `json:"name"`
	Active bool   `json:"active"`
}

// newProfileListCmd lists every named profile.
func newProfileListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "list",
		Short:         "List every named profile",
		Long:          `List every named profile, marking the active one.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		GroupID:       cligroups.Profile,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runProfileList(cmd)
		},
	}
	return cmd
}

func runProfileList(cmd *cobra.Command) error {
	cfg, err := config.Resolve()
	if err != nil {
		return err
	}
	rows, err := listProfiles(cfg)
	if err != nil {
		return err
	}
	if jsonRequested(cmd) {
		return emitJSON(cmd, map[string]any{
			"items":  rows,
			"active": cfg.Profile,
		}, newRequestID())
	}
	for _, r := range rows {
		marker := " "
		if r.Active {
			marker = "*"
		}
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", marker, r.Name); err != nil {
			return err
		}
	}
	return nil
}

// newProfileCreateCmd creates a new (empty) profile directory.
// The user is expected to populate the directory with a real
// storage_state.json via `notebooklm login` after creation.
func newProfileCreateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new profile directory",
		Long: `Create a new profile directory under <home>/profiles/<name>/.

The new directory is empty; populate it by running
"notebooklm --profile <name> login" or by copying an existing
storage_state.json into place.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		GroupID:       cligroups.Profile,
		Args:          cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProfileCreate(cmd, args[0])
		},
	}
	return cmd
}

func runProfileCreate(cmd *cobra.Command, name string) error {
	name = strings.TrimSpace(name)
	if err := validateProfileName(name); err != nil {
		return err
	}
	cfg, err := config.Resolve()
	if err != nil {
		return err
	}
	dir, err := paths.ProfileDir(name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(dir); err == nil {
		return apperrors.Wrap(apperrors.CodeValidationError,
			fmt.Errorf("profile %q already exists", name))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create profile %s: %w", name, err)
	}
	if jsonRequested(cmd) {
		return emitJSON(cmd, profileRow{Name: name, Active: cfg.Profile == name},
			newRequestID())
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "created profile %q at %s\n", name, dir)
	return err
}

// newProfileSwitchCmd sets the active profile (writes to <home>/config.json).
func newProfileSwitchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "switch <name>",
		Short: "Set the active profile",
		Long: `Set the active profile. The active profile is recorded
in <home>/config.json and honored by every subsequent command.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		GroupID:       cligroups.Profile,
		Args:          cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProfileSwitch(cmd, args[0])
		},
	}
	return cmd
}

func runProfileSwitch(cmd *cobra.Command, name string) error {
	name = strings.TrimSpace(name)
	if err := validateProfileName(name); err != nil {
		return err
	}
	dir, err := paths.ProfileDir(name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return apperrors.Wrap(apperrors.CodeNotFound,
				fmt.Errorf("profile %q does not exist (run `notebooklm profile create %s` first)", name, name))
		}
		return err
	}
	// The active profile is per-process (env var or --profile flag);
	// the switch command prints the name the user should pass on
	// subsequent invocations. Persisting it to config.json would
	// require a SetActiveProfile seam on internal/config which is
	// not in scope for T-P5-8; the spec says "switch is an
	// intentional pointer; use --profile on the next invocation".
	if jsonRequested(cmd) {
		return emitJSON(cmd, map[string]any{
			"name":      name,
			"active":    true,
			"next_step": "export NOTEBOOKLM_PROFILE=" + name,
		}, newRequestID())
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(),
		"to use this profile in the next command, run:\n  export NOTEBOOKLM_PROFILE=%s\n", name)
	return err
}

// newProfileDeleteCmd removes a profile directory after confirmation.
func newProfileDeleteCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a profile (requires --yes)",
		Long: `Delete a profile directory.

Under --json the command refuses to prompt and requires --yes
so a CI runner does not hang. The currently-active profile
cannot be deleted (switch to a different one first).`,
		SilenceUsage:  true,
		SilenceErrors: true,
		GroupID:       cligroups.Profile,
		Args:          cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProfileDelete(cmd, args[0], yes)
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false,
		"skip the confirmation prompt (required under --json)")
	return cmd
}

func runProfileDelete(cmd *cobra.Command, name string, yes bool) error {
	name = strings.TrimSpace(name)
	if err := validateProfileName(name); err != nil {
		return err
	}
	cfg, err := config.Resolve()
	if err != nil {
		return err
	}
	if cfg.Profile == name {
		return apperrors.Wrap(apperrors.CodeValidationError,
			fmt.Errorf("cannot delete the active profile %q (switch first)", name))
	}
	dir, err := paths.ProfileDir(name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return apperrors.Wrap(apperrors.CodeNotFound,
				fmt.Errorf("profile %q does not exist", name))
		}
		return err
	}

	if !yes {
		if jsonRequested(cmd) {
			return errUsage("profile delete requires --yes under --json (no interactive prompts)")
		}
		fmt.Fprintf(os.Stderr, "delete profile %q at %s? [y/N] ", name, dir)
		var ans string
		if _, serr := fmt.Scanln(&ans); serr != nil {
			// EOF on stdin (CI runner) defaults to "no".
			return errCancelled()
		}
		if !strings.EqualFold(strings.TrimSpace(ans), "y") {
			return errCancelled()
		}
	}

	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("delete profile %s: %w", name, err)
	}
	if jsonRequested(cmd) {
		return emitJSON(cmd, profileRow{Name: name, Active: false}, newRequestID())
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "deleted profile %q\n", name)
	return err
}

// newProfileRenameCmd renames a profile directory.
func newProfileRenameCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rename <old> <new>",
		Short: "Rename a profile directory",
		Long: `Rename a profile directory. The active profile cannot
be renamed; switch to a different profile first.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		GroupID:       cligroups.Profile,
		Args:          cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProfileRename(cmd, args[0], args[1])
		},
	}
	return cmd
}

func runProfileRename(cmd *cobra.Command, oldName, newName string) error {
	oldName = strings.TrimSpace(oldName)
	newName = strings.TrimSpace(newName)
	if err := validateProfileName(oldName); err != nil {
		return err
	}
	if err := validateProfileName(newName); err != nil {
		return err
	}
	cfg, err := config.Resolve()
	if err != nil {
		return err
	}
	if cfg.Profile == oldName {
		return apperrors.Wrap(apperrors.CodeValidationError,
			fmt.Errorf("cannot rename the active profile %q (switch first)", oldName))
	}
	oldDir, err := paths.ProfileDir(oldName)
	if err != nil {
		return err
	}
	newDir, err := paths.ProfileDir(newName)
	if err != nil {
		return err
	}
	if _, err := os.Stat(oldDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return apperrors.Wrap(apperrors.CodeNotFound,
				fmt.Errorf("profile %q does not exist", oldName))
		}
		return err
	}
	if _, err := os.Stat(newDir); err == nil {
		return apperrors.Wrap(apperrors.CodeValidationError,
			fmt.Errorf("profile %q already exists", newName))
	}
	if err := os.Rename(oldDir, newDir); err != nil {
		return fmt.Errorf("rename profile %s -> %s: %w", oldName, newName, err)
	}
	if jsonRequested(cmd) {
		return emitJSON(cmd, profileRow{Name: newName, Active: false}, newRequestID())
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "renamed profile %q -> %q\n", oldName, newName)
	return err
}

// listProfiles enumerates the profile directories and flags the
// active one. The function reads <home>/profiles/ directly so
// a corrupted config.json does not block `profile list`.
func listProfiles(cfg *config.Resolution) ([]profileRow, error) {
	home, err := paths.Home()
	if err != nil {
		return nil, err
	}
	profilesDir := filepath.Join(home, "profiles")
	entries, err := os.ReadDir(profilesDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	rows := make([]profileRow, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		rows = append(rows, profileRow{
			Name:   e.Name(),
			Active: e.Name() == cfg.Profile,
		})
	}
	return rows, nil
}

// validateProfileName rejects empty names and names containing
// path-traversal characters. The CLI contract is "a profile name
// is a directory basename"; anything that would force a path
// escape is rejected here.
func validateProfileName(name string) error {
	if name == "" {
		return errUsage("profile name is required")
	}
	if strings.ContainsAny(name, "/\\..\x00") {
		return errUsage("profile name must not contain '/', '\\', '.', or NUL")
	}
	return nil
}
