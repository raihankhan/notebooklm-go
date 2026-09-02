// Package internal/cli — persistent flag wiring.
//
// The Cobra persistent flags below mirror docs/07-cli-spec.md
// "Persistent flags" byte-for-byte. Every CLI command inherits the
// same set via root.PersistentFlags(), so a user does not have to
// re-discover --profile / --storage / --backend / -v / --quiet /
// --version per subcommand.
//
// The flags resolve to internal/config env vars per the
// precedence chain in docs/13-operations.md:
//
//	flag  >  NOTEBOOKLM_* env  >  active profile / default
//
// The flag value is captured by Cobra; the env resolution lives in
// internal/config and runs once at startup. This file only
// declares the *flag names* — the binding to config.Resolution is
// done in root.go so the precedence is enforced uniformly.
package cli

import (
	"github.com/spf13/cobra"
)

// Persistent flag names. The string constants live here (not in
// root.go) so the per-command --help screens that name the flag
// (e.g. `Flags inherited from parent commands`) cannot drift from
// the actual flag names on root.PersistentFlags().
const (
	// FlagProfile is the named-profile flag (-p / --profile). It
	// overrides NOTEBOOKLM_PROFILE.
	FlagProfile = "profile"

	// FlagProfileShort is the short form (-p) for FlagProfile.
	FlagProfileShort = "p"

	// FlagStorage is the auth-storage override (--storage). It is
	// the highest-precedence auth source per docs/07-cli-spec.md
	// "Auth-source precedence": --storage > NOTEBOOKLM_AUTH_JSON
	// > active profile storage.
	FlagStorage = "storage"

	// FlagBackend is the namespace backend flag (--backend
	// web|android). Default is "web"; android returns
	// ErrBackendUnavailable in v1.
	FlagBackend = "backend"

	// FlagVerbose is the verbose count flag (-v / --verbose).
	// Each -v bumps the log level: -v → INFO, -vv → DEBUG.
	FlagVerbose      = "verbose"
	FlagVerboseShort = "v"

	// FlagQuiet is the suppress-info flag (--quiet). Mutually
	// exclusive with -v; combining exits 2.
	FlagQuiet = "quiet"

	// FlagVersion is the version flag (--version). The handler
	// prints the build-time injected version and exits 0.
	FlagVersion = "version"
)

// PersistentFlags wires the seven persistent flags onto cmd. The
// caller (root.go) passes the same cmd to every AddCommand so the
// flags propagate automatically; this function only declares them.
//
// The function is exported so a test (or a future custom command
// group) can rebuild the flag set without copying the seven lines.
func PersistentFlags(cmd *cobra.Command) {
	pf := cmd.PersistentFlags()
	pf.StringP(FlagProfile, FlagProfileShort, "",
		"named profile; overrides NOTEBOOKLM_PROFILE")
	pf.String(FlagStorage, "",
		"override the storage location; highest-precedence auth source")
	pf.String(FlagBackend, "web",
		"namespace backend: web (default) or android (v1: returns ErrBackendUnavailable)")
	pf.CountP(FlagVerbose, FlagVerboseShort,
		"verbose level: -v = INFO, -vv = DEBUG (count flag)")
	pf.Bool(FlagQuiet, false,
		"suppress status output and INFO/WARN records; --json payloads still emitted (mutually exclusive with -v)")
	pf.Bool(FlagVersion, false,
		"print version and exit")
}

// ValidBackendChoices is the closed set --backend accepts. Used by
// the FlagBackend completion helper (root.go) and by the parse-time
// validator (json.go). Mirrors the Python original's choice list.
var ValidBackendChoices = []string{"web", "android"} //nolint:gochecknoglobals // registry value
