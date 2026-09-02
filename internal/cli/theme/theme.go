// Package theme defines the terminal palette and lipgloss styles
// used by the notebooklm CLI.
//
// The palette mirrors the CP AXTRA color set documented in
// docs/07-cli-spec.md "Terminal theme":
//
//	Blue   #306FC7 — CP AXTRA — headers, IDs, links
//	Yellow #F6C24A — CP AXTRA — in-progress, warnings, dry-run
//	Green  #43938F — Lotus      — ready, success, completion
//	Red    #DA3832 — Makro      — errors, destructive, failed
//
// The package exposes three primitives:
//
//   - Raw hex color constants (Blue / Yellow / Green / Red) that other
//     packages (notably internal/app/serialize/tables.go) can reference
//     without pulling in lipgloss.
//   - Pre-built lipgloss.Style values (Header / ID / Success / Warn /
//     ErrorText / Muted / CommandName) that every CLI command applies
//     to its colored output.
//   - A Strip-noColor-aware Renderer (NewRenderer) that respects the
//     NO_COLOR convention (https://no-color.org/) and the lipgloss
//     auto-detection of a non-TTY stdout. Tests assert the exact
//     strings emitted under each mode.
//
// The package imports github.com/charmbracelet/lipgloss and its
// transitive dependencies; this is the only place in the module
// outside internal/cli where the boundary rule
// (boundaries.yaml mode=external) allows those imports.
//
// References:
//
//   - docs/07-cli-spec.md §"Terminal theme" — palette and per-role
//     mapping (the source of truth for the hex values).
//   - notebooklm-py/src/notebooklm/cli/rendering.py — the Python
//     original's palette, which the Go port reproduces verbatim.
package theme

import (
	"os"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// CP AXTRA palette. The hex values mirror docs/07-cli-spec.md "Terminal
// theme" byte-for-byte; changing them would silently break every
// downstream color contract.
const (
	// HexBlue is the CP AXTRA Blue (#306FC7). Headers, command names in
	// help text, notebook/source/artifact IDs, link rendering.
	HexBlue = "#306FC7"

	// HexYellow is the CP AXTRA Yellow (#F6C24A). In-progress status
	// (processing / pending / preparing), spinners, --dry-run notices,
	// deprecation notes.
	HexYellow = "#F6C24A"

	// HexGreen is the Lotus Green (#43938F). Ready / completed / success
	// status, confirmation glyphs.
	HexGreen = "#43938F"

	// HexRed is the Makro Red (#DA3832). Errors, failed status,
	// destructive-action confirmation prompts.
	HexRed = "#DA3832"
)

// lipgloss color tokens, exposed for tests and for callers that want
// to compose new styles outside this file.
var (
	Blue   = lipgloss.Color(HexBlue)
	Yellow = lipgloss.Color(HexYellow)
	Green  = lipgloss.Color(HexGreen)
	Red    = lipgloss.Color(HexRed)
)

// Pre-built styles. Each style is exported so the CLI commands apply
// the same look-and-feel without re-deriving the colors. A style is
// safe to call .Render(s) on; the renderer below applies NO_COLOR +
// non-TTY semantics.
var (
	// Header is the table-header style — bold Blue. Used by the table
	// renderer (internal/cli/table) and by every list-style command
	// that emits a header row.
	Header = lipgloss.NewStyle().Foreground(Blue).Bold(true)

	// ID is the resource-id style — Blue, not bold. Notebook ids,
	// source ids, artifact ids render in this style so a user can scan
	// a row and pluck the id without re-reading prose.
	ID = lipgloss.NewStyle().Foreground(Blue)

	// Success is the success / ready style — Green. "Ready" / "Completed"
	// status cells, confirmation glyphs, success lines.
	Success = lipgloss.NewStyle().Foreground(Green)

	// Warn is the warning / in-progress style — Yellow. "Processing" /
	// "Pending" status cells, --dry-run notices, deprecation notes.
	Warn = lipgloss.NewStyle().Foreground(Yellow)

	// ErrorText is the error / destructive-action style — bold Red.
	// Error lines (top-level message only; envelopes handle structured
	// errors), destructive confirmation prompts.
	ErrorText = lipgloss.NewStyle().Foreground(Red).Bold(true)

	// Muted is the unknown / suggested style — faint. Unmappable
	// status values ("unknown", "suggested") and de-emphasized
	// metadata. Per docs/07-cli-spec.md, an unmappable code is not
	// evidence of degradation, so this style deliberately does not
	// paint it Red.
	Muted = lipgloss.NewStyle().Faint(true)

	// CommandName is the help-text command-name style — bold Blue.
	// Applied to the command name in `notebooklm <name> - …` lines.
	CommandName = lipgloss.NewStyle().Foreground(Blue).Bold(true)
)

// StatusStyle returns the lipgloss.Style associated with a status
// keyword. The mapping is the single source of truth so the table
// renderer, the CLI status lines, and the progress spinner cannot
// drift:
//
//	ready / completed / success → Success (Green)
//	processing / pending / in_progress / preparing → Warn (Yellow)
//	error / failed → ErrorText (Red)
//	unknown / suggested / "" → Muted (faint)
//
// The match is exact and case-insensitive; anything else falls back
// to Muted so an unmappable status renders as faint rather than
// silent.
//
// Callers wrap the result around a single cell value with .Render(s);
// the renderer applies NO_COLOR + non-TTY at the lipgloss layer.
func StatusStyle(status string) lipgloss.Style {
	switch ToLower(Trim(status)) {
	case "ready", "completed", "success":
		return Success
	case "processing", "pending", "in_progress", "preparing":
		return Warn
	case "error", "failed":
		return ErrorText
	case "unknown", "suggested", "":
		return Muted
	default:
		return Muted
	}
}

// Renderer is a thin wrapper around lipgloss.Renderer that respects
// NO_COLOR (https://no-color.org/) and a non-TTY stdout. Constructing
// one is the recommended way for CLI commands to colorize output
// without having to repeat the "should I strip color?" decision at
// every call site.
//
// Construction:
//
//	r, err := theme.NewRenderer(os.Stdout)
//
//		text := r.NewStyle().Foreground(theme.Blue).Render("hello")
//
// When NO_COLOR is set in the environment OR stdout is not a TTY the
// returned renderer has color forced off — every .Render() call then
// returns the unstyled string. When color is allowed lipgloss's own
// detection (TERM=xterm-256color, etc.) takes over.
type Renderer struct {
	inner *lipgloss.Renderer
	// noColor is true when the caller constructed the renderer with
	// color forced off (NO_COLOR set, non-TTY stdout, or explicit
	// opt-out via NewRendererWith). Tests inspect this to assert the
	// mode the renderer is in.
	noColor bool
}

// NewRenderer returns a Renderer wired to w's file descriptor. The
// function inspects os.Getenv("NO_COLOR") (set ⇒ no color, per
// https://no-color.org/) and the device-mode of w (non-char-device
// ⇒ no color, matching the spec's "non-TTY stdout" rule). The
// returned error reports the only case where construction can
// fail today (NO_COLOR was forced off but the env read failed),
// which is impossible in practice — but the signature stays open
// for future failure modes.
func NewRenderer(w *os.File) (*Renderer, error) {
	noColor := wantsNoColor(w)
	return NewRendererWith(w, noColor), nil
}

// MustRenderer is the panic-on-error twin of NewRenderer for the
// startup path where the operator is present to see the crash
// (vs. the request path, which must surface the error).
func MustRenderer(w *os.File) *Renderer {
	r, err := NewRenderer(w)
	if err != nil {
		panic(err)
	}
	return r
}

// NewRendererWith is the explicit-mode constructor used by tests
// and by callers that already computed the NO_COLOR/TTY verdict.
// Pass noColor=true to force ASCII output regardless of the
// environment.
func NewRendererWith(w *os.File, noColor bool) *Renderer {
	r := lipgloss.NewRenderer(w)
	if noColor {
		r.SetColorProfile(termenv.Ascii)
	}
	return &Renderer{inner: r, noColor: noColor}
}

// NewStyle returns a fresh lipgloss.Style bound to this Renderer.
// Callers compose it with .Foreground() / .Bold() / .Render() the
// same way they would on a global style.
func (r *Renderer) NewStyle() lipgloss.Style {
	return r.inner.NewStyle()
}

// NoColor reports whether the renderer is in color-stripped mode.
// Tests assert this; production code uses it to gate animations
// (e.g. a spinner that should not spin under --json or non-TTY).
func (r *Renderer) NoColor() bool { return r.noColor }

// wantsNoColor returns true when the spec requires zero ANSI escapes:
// NO_COLOR env var is set to a non-empty value, or w is not a character
// device (the conservative interpretation of "non-TTY stdout" — any
// pipe, redirect, or capture buffer qualifies).
func wantsNoColor(w *os.File) bool {
	if v := os.Getenv("NO_COLOR"); v != "" {
		return true
	}
	if w == nil {
		return true
	}
	fi, err := w.Stat()
	if err != nil {
		return true
	}
	return (fi.Mode() & os.ModeCharDevice) == 0
}

// ToLower + Trim are local helpers that avoid pulling strings into
// the public surface of the package (which is themed around color
// tokens). They are tiny; an allocation per call is fine because
// callers feed them status strings (≤ 32 bytes).
func ToLower(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}

// Trim strips leading and trailing whitespace without allocating.
func Trim(s string) string {
	start, end := 0, len(s)
	for start < end {
		c := s[start]
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			break
		}
		start++
	}
	for end > start {
		c := s[end-1]
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			break
		}
		end--
	}
	return s[start:end]
}
