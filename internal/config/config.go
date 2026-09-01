// Package config resolves the operational configuration of the
// notebooklm-go CLI and library: environment variables, base URL
// allowlist, build label, and the language hint.
//
// Per docs/AGENTS.md rule 5, this package is mode=internal: it may
// import stdlib + internal/redact + internal/buildinfo only. No
// third-party modules. The package is consumed by every later
// phase (CLI flag resolution, MCP server config, REST server
// startup) — keeping it stdlib + two internal siblings preserves
// the "config can be imported anywhere" property from doc 02.
//
// All env-var reads are gated through helper functions so the test
// harness can override one variable at a time via t.Setenv. Empty
// and whitespace-only env values are treated as unset (per
// docs/13-operations.md §"Precedence").
//
// docs/13-operations.md is the design spec for this file.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/raihankhan/notebooklm-go/internal/buildinfo"
	"github.com/raihankhan/notebooklm-go/internal/redact"
	"github.com/raihankhan/notebooklm-go/internal/web/wire"
)

// Environment variable names. Keep these in sync with
// docs/13-operations.md; do not invent new ones.
const (
	// EnvHome is the base directory for all config and credentials.
	// Default: ~/.notebooklm
	EnvHome = "NOTEBOOKLM_HOME"

	// EnvLogLevel sets the structured-logging threshold:
	// DEBUG | INFO | WARNING | ERROR.
	EnvLogLevel = "NOTEBOOKLM_LOG_LEVEL"

	// EnvRPCOverrides is the operator escape hatch when Google
	// changes an RPC id. JSON object: {"MethodName":"newId"}.
	// Documented in internal/web/wire/methods.go (RPCOverridesEnvVar);
	// the alias lives here too so the CLI/MCP/REST adapters have
	// one canonical name to import.
	EnvRPCOverrides = "NOTEBOOKLM_RPC_OVERRIDES"

	// EnvHL is the output-language hint. Defaults to "en".
	EnvHL = "NOTEBOOKLM_HL"

	// EnvBaseURL is the allowlisted NotebookLM host. Defaults to
	// wire.HostPersonal ("https://notebook.google.com").
	EnvBaseURL = "NOTEBOOKLM_BASE_URL"

	// EnvProfile selects the active auth profile within NOTEBOOKLM_HOME.
	EnvProfile = "NOTEBOOKLM_PROFILE"

	// EnvBackend picks the protocol backend (web today; android
	// reserved for v1.1).
	EnvBackend = "NOTEBOOKLM_BACKEND"

	// EnvBL is the chat-endpoint frontend build-label override.
	EnvBL = "NOTEBOOKLM_BL"
)

// Defaults — values used when the env var is unset / blank.
const (
	// DefaultHomeSubdir is the folder name inside the user's home
	// directory that NOTEBOOKLM_HOME defaults to when the env var
	// is unset.
	DefaultHomeSubdir = ".notebooklm"

	// DefaultProfile is the active profile name when NOTEBOOKLM_PROFILE
	// is unset.
	DefaultProfile = "default"

	// DefaultHL is the output language when NOTEBOOKLM_HL is unset.
	DefaultHL = "en"

	// DefaultLogLevel is the logging threshold when NOTEBOOKLM_LOG_LEVEL
	// is unset.
	DefaultLogLevel = "INFO"
)

// Resolution holds the resolved values for one startup. Construct
// via Resolve() so the precedence chain runs in the documented
// order. The struct is small and copy-safe; long-running processes
// capture a snapshot at startup and never re-read it.
type Resolution struct {
	// Home is the absolute base directory resolved from NOTEBOOKLM_HOME
	// or the home-directory default.
	Home string

	// Profile is the active profile name.
	Profile string

	// LogLevel is one of "DEBUG", "INFO", "WARNING", "ERROR". Unset
	// when the value supplied could not be parsed (the caller
	// should fall back to DefaultLogLevel in that case).
	LogLevel string

	// RPCOverrides is the parsed NOTEBOOKLM_RPC_OVERRIDES map. nil
	// when the env var is unset / blank.
	RPCOverrides map[string]string

	// HL is the output language hint. Empty when the env var was
	// an unrecognized value (the caller should fall back to
	// DefaultHL).
	HL string

	// BaseURL is the resolved base URL; one of the three wire.Host
	// allowlisted values.
	BaseURL wire.Host

	// BuildLabel is the chat-endpoint frontend build label,
	// resolved from NOTEBOOKLM_BL or the build-time injection.
	BuildLabel string

	// BuildLabelStale is true when the BuildLabel above is older
	// than the build-time injection (i.e. the operator's override
	// has lagged behind a frontend bump).
	BuildLabelStale bool

	// BuildLabelWarning is non-empty when ParseBuildLabel failed
	// on the supplied or build-time label. The warning text is
	// safe to surface to operators.
	BuildLabelWarning string
}

// Errors surfaced by Resolve.
var (
	// ErrInvalidBaseURL is returned when NOTEBOOKLM_BASE_URL fails
	// the wire.IsAllowedHost check. The error wraps the underlying
	// allowlist violation so a CLI/MCP/REST adapter can render a
	// specific message.
	ErrInvalidBaseURL = errors.New("config: NOTEBOOKLM_BASE_URL is not in the allowlist")

	// ErrInvalidLogLevel is returned when NOTEBOOKLM_LOG_LEVEL is
	// not one of the four accepted labels.
	ErrInvalidLogLevel = errors.New("config: NOTEBOOKLM_LOG_LEVEL must be DEBUG, INFO, WARNING, or ERROR")

	// ErrInvalidHL is returned when NOTEBOOKLM_HL is not a
	// 2-letter ISO 639-1 code.
	ErrInvalidHL = errors.New("config: NOTEBOOKLM_HL must be a 2-letter ISO 639-1 code")
)

// Resolve reads the environment and returns a fully-populated
// Resolution. The function is the single canonical entry point;
// callers should not roll their own env-var reads. An error means
// the environment is malformed in a way that requires the operator
// to fix something before the binary can start (e.g. an
// allowlist-violating BASE_URL). Missing-but-defaultable values
// (e.g. a missing NOTEBOOKLM_HOME) never error.
//
// Resolve is pure: it does not touch the filesystem (no mkdir of
// Home), does not register slog handlers, and does not consult the
// cookie jar. Callers do those after they have a Resolution.
func Resolve() (*Resolution, error) {
	r := &Resolution{}

	home, err := resolveHome()
	if err != nil {
		return nil, err
	}
	r.Home = home

	r.Profile = strings.TrimSpace(os.Getenv(EnvProfile))
	if r.Profile == "" {
		r.Profile = DefaultProfile
	}

	logLevel := strings.TrimSpace(os.Getenv(EnvLogLevel))
	if logLevel == "" {
		logLevel = DefaultLogLevel
	}
	if !isValidLogLevel(logLevel) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidLogLevel, logLevel)
	}
	r.LogLevel = logLevel

	rpcOverrides, err := parseRPCOverrides(os.Getenv(EnvRPCOverrides))
	if err != nil {
		// Malformed overrides are a warning, not a startup error.
		// The Python implementation treats a malformed override
		// file the same way; the binary must still come up so the
		// operator can fix it.
		slog.Warn("NOTEBOOKLM_RPC_OVERRIDES parse failed; ignoring",
			slog.String("error", string(redact.Apply([]byte(err.Error())))))
		rpcOverrides = nil
	}
	r.RPCOverrides = rpcOverrides

	hl := strings.TrimSpace(os.Getenv(EnvHL))
	if hl == "" {
		hl = DefaultHL
	} else if !isValidHL(hl) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidHL, hl)
	}
	r.HL = hl

	baseURL, err := resolveBaseURL(os.Getenv(EnvBaseURL))
	if err != nil {
		return nil, err
	}
	r.BaseURL = baseURL

	bl, stale, warning := resolveBuildLabel(os.Getenv(EnvBL))
	r.BuildLabel = bl
	r.BuildLabelStale = stale
	r.BuildLabelWarning = warning

	return r, nil
}

// resolveHome returns the absolute path to the home directory,
// honoring NOTEBOOKLM_HOME or the ~/.notebooklm default.
func resolveHome() (string, error) {
	raw := strings.TrimSpace(os.Getenv(EnvHome))
	if raw != "" {
		return filepath.Abs(raw)
	}
	homedir, err := os.UserHomeDir()
	if err != nil {
		// Fall back to the literal "~" so the caller still gets a
		// usable (if obviously broken) path; the runtime layers
		// surface a typed error when they try to mkdir under it.
		// The intentional nil-on-error is documented in
		// docs/13-operations.md §"Precedence" and is the
		// nilerr-suppressed fallback path.
		return DefaultHomeSubdir, nil //nolint:nilerr
	}
	return filepath.Join(homedir, DefaultHomeSubdir), nil
}

// resolveBaseURL honors the env var when set and the env value is
// on the allowlist. Otherwise it returns the default
// wire.HostPersonal. An invalid value (not on the allowlist) is
// the only error case — a startup-blocking misconfiguration.
func resolveBaseURL(raw string) (wire.Host, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return wire.HostPersonal, nil
	}
	if !wire.IsAllowedHost(raw) {
		return "", fmt.Errorf("%w: %q", ErrInvalidBaseURL, raw)
	}
	return wire.Host(raw), nil
}

// resolveBuildLabel honors NOTEBOOKLM_BL when set and parseable.
// An unparseable override is surfaced as BuildLabelWarning but the
// binary still starts (the runtime falls back to DEFAULT_BL).
func resolveBuildLabel(supplied string) (string, bool, string) {
	supplied = strings.TrimSpace(supplied)
	if supplied != "" {
		if _, err := ParseBuildLabel(supplied); err != nil {
			warning := fmt.Sprintf(
				"NOTEBOOKLM_BL %q did not parse; falling back to %s",
				supplied, DEFAULT_BL,
			)
			return DEFAULT_BL, false, warning
		}
		stale, err := IsBuildLabelStale(supplied)
		if err != nil || stale {
			warning := fmt.Sprintf(
				"NOTEBOOKLM_BL %q is older than the build-time injection; consider updating",
				supplied,
			)
			return supplied, stale, warning
		}
		return supplied, false, ""
	}
	// No override: use the build-time injection if it parses,
	// otherwise the DEFAULT_BL.
	bl := buildinfo.BuildLabel
	if bl == "" || bl == "unknown" {
		return DEFAULT_BL, false, ""
	}
	if _, err := ParseBuildLabel(bl); err != nil {
		warning := fmt.Sprintf(
			"build-time BuildLabel %q did not parse; falling back to %s",
			bl, DEFAULT_BL,
		)
		return DEFAULT_BL, false, warning
	}
	return bl, false, ""
}

// isValidLogLevel accepts the four documented labels. Case-sensitive
// per docs/13-operations.md.
func isValidLogLevel(s string) bool {
	switch s {
	case "DEBUG", "INFO", "WARNING", "ERROR":
		return true
	}
	return false
}

// isValidHL accepts a 2-letter lowercase ISO 639-1 code. We do not
// validate the code against the canonical table; an unrecognized
// code still passes through and the runtime / server layer surfaces
// the rejection if Google rejects it.
func isValidHL(s string) bool {
	if len(s) != 2 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 'a' || c > 'z' {
			return false
		}
	}
	return true
}

// parseRPCOverrides decodes the env value. Mirrors the wire-level
// helper so the config layer can surface a typed error to a log
// line without exposing the wire package's internals.
//
// Accepts the same shape the wire parser accepts (a JSON object);
// unknown method names are silently dropped, mirroring wire's
// tolerance for a typo'd override key.
func parseRPCOverrides(raw string) (map[string]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if strings.HasPrefix(raw, "{") {
		return wire.ParseOverridesJSON(raw)
	}
	// Name=id,Name=id form: comma- or newline-separated pairs.
	out := make(map[string]string)
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		for _, pair := range strings.Split(line, ",") {
			pair = strings.TrimSpace(pair)
			if pair == "" {
				continue
			}
			eq := strings.IndexByte(pair, '=')
			if eq < 0 {
				return nil, fmt.Errorf("NOTEBOOKLM_RPC_OVERRIDES: missing '=' in pair %q", pair)
			}
			k := strings.TrimSpace(pair[:eq])
			v := strings.TrimSpace(pair[eq+1:])
			if k != "" && v != "" {
				out[k] = v
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("NOTEBOOKLM_RPC_OVERRIDES: no pairs parsed from %q", raw)
	}
	return out, nil
}
