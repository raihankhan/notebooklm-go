// Package cmd — `notebooklm auth check` subcommand.
//
// Verifies the active profile's auth state. Without --test the
// command is a local-only check (storage file exists, file mode
// is 0o600 or stricter). With --test the command opens a real
// Client and forces a cheap round-trip (Notebooks.List) so stale
// cookies surface as AUTH_ERROR rather than as a runtime 401 on
// the user's next command.
package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/raihankhan/notebooklm-go/internal/cligroups"
	"github.com/spf13/cobra"

	"github.com/raihankhan/notebooklm-go/notebooklm"
)

// newAuthCheckCmd returns the `notebooklm auth check` subcommand.
func newAuthCheckCmd() *cobra.Command {
	var test bool
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Verify the active profile's auth state",
		Long: `Verify the active profile's auth state.

By default the check is local-only: the storage file exists and
its mode is 0o600 or stricter. Pass --test to also open a real
Client and force a cheap round-trip — useful for flushing stale
cookies before they hit the user's next command.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		GroupID:       cligroups.Auth,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runAuthCheck(cmd, test)
		},
	}
	cmd.Flags().BoolVar(&test, "test", false,
		"force a live round-trip (Notebooks.List) to verify the auth state end-to-end")
	return cmd
}

// authCheckPayload is the JSON envelope data for `auth check`.
type authCheckPayload struct {
	OK         bool   `json:"ok"`
	Storage    string `json:"storage,omitempty"`
	LiveTested bool   `json:"live_tested"`
}

func runAuthCheck(cmd *cobra.Command, test bool) error {
	storageFlag := storageOverride(cmd)
	storagePath, err := resolveStoragePath(storageFlag)
	if err != nil {
		return err
	}

	info, err := os.Stat(storagePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("auth storage %s does not exist (run `notebooklm login`)", storagePath)
		}
		return fmt.Errorf("auth storage stat: %w", err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		return fmt.Errorf("auth storage %s is world-readable (mode %#o); chmod 600 to fix", storagePath, mode)
	}

	if test {
		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		err := withClient(cmd, ctx, func(c *notebooklm.Client) error {
			// A cheap round-trip. List returns one row on
			// healthy accounts and an AUTH_ERROR on stale
			// cookies.
			_, lerr := c.Notebooks().List(ctx)
			return lerr
		})
		if err != nil {
			return err
		}
	}

	if jsonRequested(cmd) {
		return emitJSON(cmd, authCheckPayload{
			OK:         true,
			Storage:    storagePath,
			LiveTested: test,
		}, newRequestID())
	}

	out := cmd.OutOrStdout()
	_, err = fmt.Fprintf(out, "auth storage OK (%s, %d bytes, mode %#o)\n",
		storagePath, info.Size(), info.Mode().Perm())
	if err != nil {
		return err
	}
	if test {
		_, err = fmt.Fprintln(out, "live test OK (Notebooks.List round-trip)")
	}
	return err
}
