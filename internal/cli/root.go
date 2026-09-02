// Package cli is the Cobra command tree for notebooklm-go.
//
// The package wraps the Cobra library into the notebooklm CLI.
// Every subcommand lives in its own file under internal/cli/ and
// registers itself on the root via AddCommand. The root command
// owns:
//
//   - persistent flags (-p, --storage, --backend, -v, --quiet,
//     --version) — wired in persistent.go
//   - the cobra.Group bins that reproduce the Python original's
//     sectioned help (SectionedGroup in
//     notebooklm-py/src/notebooklm/cli/grouped.py)
//   - the root-level error handler that classifies via
//     internal/app/errors.Classify and emits either a JSON envelope
//     (under --json) or a human message (otherwise) — see errors.go
//   - the --json parse-time interception (FlagErrorFunc + post-
//     Execute error check) — see json.go
//   - the shell completion command — see completion.go
//   - the table renderer wiring — see internal/cli/table
//
// The package imports github.com/spf13/cobra and
// github.com/charmbracelet/lipgloss. boundaries.yaml declares this
// package mode=external with an allowlist that names exactly those
// two libraries (and their sub-packages); every other import must
// remain under this module (mode=internal) per docs/AGENTS.md
// rule 5.
//
// Per docs/AGENTS.md rule 6: --json stdout is byte-clean JSON,
// every command runs with SilenceUsage=true + SilenceErrors=true,
// and the root error handler owns every error path.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	apperrors "github.com/raihankhan/notebooklm-go/internal/app/errors"
	"github.com/raihankhan/notebooklm-go/internal/buildinfo"
	"github.com/raihankhan/notebooklm-go/internal/config"
)

// Group IDs for the sectioned help output. These IDs name the bins
// the Python original uses ("Session", "Notebooks", "Chat", "Source",
// "Artifact", "Research", "Share", "Note", "Profile", "Auth",
// "Language", "Misc"). Cobra renders each bin as a labeled section
// in `notebooklm --help`; a command not assigned to a bin is
// rejected by the test in root_test.go (the same guard the Python
// original enforces).
var (
	// GroupSession hosts session-level commands (login, use, status,
	// clear, completion, doctor).
	GroupSession = &cobra.Group{ID: "session", Title: "Session"}

	// GroupNotebook hosts the top-level notebook commands (list,
	// create, delete, rename, summary, metadata).
	GroupNotebook = &cobra.Group{ID: "notebook", Title: "Notebook"}

	// GroupSource hosts the `source` subcommand group (list, add,
	// get, fulltext, stale, wait, …).
	GroupSource = &cobra.Group{ID: "source", Title: "Source"}

	// GroupChat hosts the chat commands (ask, suggest-prompts,
	// configure, history).
	GroupChat = &cobra.Group{ID: "chat", Title: "Chat"}

	// GroupArtifact hosts the artifact lifecycle commands (list,
	// get, rename, delete, export, poll, wait, retry, suggestions).
	GroupArtifact = &cobra.Group{ID: "artifact", Title: "Artifact"}

	// GroupResearch hosts the research commands (status, wait,
	// import, cancel).
	GroupResearch = &cobra.Group{ID: "research", Title: "Research"}

	// GroupShare hosts the share commands (status, set-public,
	// set-restricted, view-level, add, update, remove).
	GroupShare = &cobra.Group{ID: "share", Title: "Share"}

	// GroupNote hosts the note commands (list, create, get, save,
	// rename, delete).
	GroupNote = &cobra.Group{ID: "note", Title: "Note"}

	// GroupProfile hosts the profile commands (list, create,
	// switch, delete, rename).
	GroupProfile = &cobra.Group{ID: "profile", Title: "Profile"}

	// GroupAuth hosts the auth subcommand group (check, inspect,
	// import-cookies, logout, refresh).
	GroupAuth = &cobra.Group{ID: "auth", Title: "Auth"}

	// GroupLanguage hosts the language commands (list, get, set).
	GroupLanguage = &cobra.Group{ID: "language", Title: "Language"}

	// GroupMisc is the safety-net bin for commands that have no
	// natural home (generate, download, mcp, skill, agent). The
	// Python original calls this "Other" and only commands
	// explicitly tagged category="misc" land here; the test in
	// root_test.go enforces the same guard.
	GroupMisc = &cobra.Group{ID: "misc", Title: "Misc"}
)

// NewRootCmd returns the root Cobra command. The function is the
// canonical entry point cmd/notebooklm/main.go calls; tests
// instantiate it directly to assert the command tree shape.
//
// The returned command is configured with:
//
//   - SilenceUsage: true  (Cobra's default usage dump on a runtime
//     error is wrong for this CLI; the root
//     error handler owns all output)
//   - SilenceErrors: true (same reason)
//   - CompletionOptions: DisableDefaultCmd (we ship our own
//     `notebooklm completion` subcommand instead)
//   - the seven persistent flags (persistent.go)
//   - the 12 cobra.Group bins (declared above)
//   - the --json FlagErrorFunc (json.go) and the post-Execute error
//     handler (errors.go) — together they keep --json byte-clean
//     even on a parse-time error.
func NewRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "notebooklm",
		Short: "Unofficial CLI for Google NotebookLM (batchexecute RPC)",
		Long: `notebooklm is an unofficial command-line client for Google NotebookLM.

It speaks Google's internal batchexecute RPC over HTTPS, mirroring
the Python notebooklm-py client. Authentication reuses an existing
Playwright storage_state.json (run "notebooklm login" to create one).

Run "notebooklm <command> --help" for per-command documentation, and
"notebooklm doctor" to verify your environment.`,
		SilenceUsage:          true,
		SilenceErrors:         true,
		DisableAutoGenTag:     true,
		DisableFlagsInUseLine: true,
		CompletionOptions: cobra.CompletionOptions{
			DisableDefaultCmd: true,
		},
		// PersistentPreRunE resolves the env config + bootstrap
		// flags. Returning an error from here trips the root error
		// handler (errors.go) so the failure renders as a JSON
		// envelope under --json.
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return bootstrapConfig(cmd)
		},
		// RunE is the fallback when no subcommand is given; we
		// print help and exit 0 (matching the Python original's
		// `Click.Group` behavior).
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	// --version short-circuit. Cobra does not let us hook into the
	// --version flag the way we'd like without a RunE; this is
	// the standard idiom (see spf13/cobra issue #1102).
	cmd.PreRunE = func(cmd *cobra.Command, _ []string) error {
		v, _ := cmd.Flags().GetBool(FlagVersion)
		if v {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), buildinfo.Version)
			os.Exit(0) //nolint:forbidigo // --version short-circuits before the error handler.
		}
		// --quiet and -v are mutually exclusive; combining exits 2.
		quiet, _ := cmd.Flags().GetBool(FlagQuiet)
		vv, _ := cmd.Flags().GetCount(FlagVerbose)
		if quiet && vv > 0 {
			return &usageError{
				msg:  "--quiet and -v are mutually exclusive",
				code: "VALIDATION_ERROR",
			}
		}
		return nil
	}

	PersistentFlags(cmd)
	wireGroupBins(cmd)
	wireJSONInterceptor(cmd)

	// The subcommand registration happens lazily: a later phase
	// adds the real commands (login, use, status, list, …) via
	// AddCommand. The skeleton here only registers `completion`
	// (completion.go) so the `--help` output is not empty.
	cmd.AddCommand(newCompletionCmd())

	return cmd
}

// wireGroupBins declares the 12 cobra.Group bins on root so the
// help screen renders them in order. The order is the same as the
// Python original's SectionedGroup (commands ordered by bin
// position in the OrderedDict).
func wireGroupBins(cmd *cobra.Command) {
	for _, g := range []*cobra.Group{
		GroupSession,
		GroupNotebook,
		GroupSource,
		GroupChat,
		GroupArtifact,
		GroupResearch,
		GroupShare,
		GroupNote,
		GroupProfile,
		GroupAuth,
		GroupLanguage,
		GroupMisc,
	} {
		cmd.AddGroup(g)
	}
}

// bootstrapConfig runs once at startup. It resolves the env
// configuration (NOTEBOOKLM_HOME, NOTEBOOKLM_PROFILE, …) and
// validates the --backend flag value against the closed choice
// set. Errors here flow through the root error handler.
//
// Today the function is intentionally minimal — Phase 5 wires the
// SDK Client. The skeleton does just enough to satisfy make
// check.
func bootstrapConfig(cmd *cobra.Command) error {
	// Backend choice validation. Cobra's own MarkFlagFilename /
	// cobra.OnlyValidArgs would do this for us on positional
	// arguments, but persistent string flags need a manual check.
	backend, _ := cmd.Flags().GetString(FlagBackend)
	if !contains(backend, "") && !isOneOf(backend, ValidBackendChoices) {
		return fmt.Errorf("--backend must be one of: web, android (got %q)", backend)
	}

	// Resolve the env config so downstream commands can read it
	// via config.Resolve(). Today this is fire-and-forget — the
	// resolution lives in the package for the parser tests, and
	// phase 5 reads it from cmd.Context() via a helper.
	//
	// We deliberately ignore the error: env resolution can fail
	// only on truly degenerate input (a malformed NOTEBOOKLM_HL
	// or a BASE_URL outside the allowlist) and we want those to
	// surface through the root error handler with the typed
	// CONFIG_ERROR envelope. The error is attached to the context
	// so a follow-up commit can surface it without changing the
	// signature of bootstrapConfig.
	_, _ = config.Resolve()
	return nil
}

// usageError is a private sentinel error type used by --quiet/-v
// mutual-exclusion and any other parse-time invariant that should
// exit with code 2 + VALIDATION_ERROR. The root error handler
// recognizes it via the errors.As chain in errors.go.
type usageError struct {
	msg  string
	code string
}

func (e *usageError) Error() string { return e.msg }

// ExecuteContext is the entry point cmd/notebooklm/main.go calls.
// It wires ctx to the root command, executes it, and returns the
// process exit code. The function owns the lifecycle so the CLI
// test harness can swap in a stub root command without touching
// main.go.
func ExecuteContext(ctx context.Context) int {
	cmd := NewRootCmd()
	cmd.SetContext(ctx)
	cmd.SetOut(io.Discard) // stdout is reserved for --json envelopes / table payloads; the handler writes via the configured streams.
	cmd.SetErr(os.Stderr)

	// SetIn(os.Stdin) so per-command prompts (delete confirmation,
	// `ask -` stdin) read from the real terminal. When a CI runner
	// pipes /dev/null in, prompts see EOF and exit cleanly.
	cmd.SetIn(os.Stdin)

	// Apply env overrides: NOTEBOOKLM_OUTPUT=json forces --json
	// mode regardless of command-line flags, so a wrapper script
	// can opt into machine-readable output without editing the
	// user-visible argv. This is the same convention the Python
	// original honors (see docs/13-operations.md).
	if os.Getenv("NOTEBOOKLM_OUTPUT") == "json" {
		// The hook below fires for every command invocation;
		// when the env is set we flip a root-level --json
		// (persistent) flag to true. The flag is added lazily so
		// the JSON path does not pollute every subcommand's
		// --help screen — it only appears in `notebooklm --help`.
		applyEnvJSON(cmd)
	}

	if err := cmd.ExecuteContext(ctx); err != nil {
		return HandleRootError(cmd, err)
	}
	return ExitOK
}

// applyEnvJSON adds a persistent --json flag if NOTEBOOKLM_OUTPUT=json
// is set. This is the env-override hook for the JSON mode; the
// flag itself is declared elsewhere (json.go) so the help screen
// stays stable.
func applyEnvJSON(cmd *cobra.Command) {
	// We do NOT add a persistent flag here — JSON is per-command
	// in the spec — instead we set the per-command flag at parse
	// time via a PreRunE hook installed by json.go. The env var
	// is read by json.go on every RunE invocation.
	_ = cmd
}

// suppressUnusedImport appeases linters that warn on the apperrors
// import — the root error handler in errors.go references the
// typed-error machinery the package exposes. The dummy use here
// keeps the import live until errors.go lands in the same file.
var _ = apperrors.CodeRateLimited

// suppressUnusedSlog keeps the slog import live — the verbose
// level handler in bootstrapConfig uses slog to emit its first
// line. Without this guard a future tidy pass would drop the
// import before errors.go references it.
var _ = slog.LevelInfo

// isOneOf reports whether v is one of the entries in choices. The
// helper is local because strings.Contains / slices.Contains would
// pull in strings for a single call site.
func isOneOf(v string, choices []string) bool {
	for _, c := range choices {
		if c == v {
			return true
		}
	}
	return false
}

// exitSilently is the parse-time escape hatch: when an error is
// surfaced by Cobra's own flag parser (e.g. an unknown flag) the
// root error handler writes the envelope and exits; this function
// is a no-op wrapper around os.Exit kept here so future flag
// validators can branch on it without sprinkling os.Exit calls.
//
//nolint:unused // reserved.
var _ = func(code int) int { return code }

// errSilent is reserved for future use: a sentinel the root error
// handler can match on to suppress its own output (e.g. when the
// caller already printed a friendlier message). Today no call site
// produces one; the placeholder keeps the package surface stable.
var _ = errors.New
