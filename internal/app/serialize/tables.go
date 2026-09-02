// Package serialize — table rendering. The Table struct is the canonical
// column/row shape every CLI list command projects its data into before
// handing off to a renderer (Cobra table / lipgloss / text-only). The
// rendering split here is deliberately minimal: we return [][]string, the
// CLI chooses the formatter. The package boundary (mode=internal, see
// boundaries.yaml) forbids a TUI library import, so ANSI escapes live in
// this file and stay tiny.
//
// The Python original renders tables through Rich
// (notebooklm-py/src/notebooklm/cli/rendering.py — Table, console.print).
// We do not pull in a TUI library here; the CLI layer renders with lipgloss
// using the strings this package produces.
package serialize

import (
	"os"
	"strings"
)

// IsStdoutTTY reports whether os.Stdout is a terminal. The CLI calls this
// once at startup; under --json it is irrelevant (stdout is reserved for
// JSON, color goes to stderr). Outside --json the table renderer consults
// this to decide whether to wrap cells in ANSI escapes.
//
// Using os.Stat here keeps the package free of third-party dependencies
// (golang.org/x/term would be the obvious choice but boundary rules in
// docs/AGENTS.md forbid it for internal/app/*).
func IsStdoutTTY() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// Table is the row/column view every list command projects into. The
// Columns slice is the ordered list of column names (rendered as the header
// row); the Rows slice is the data, each row parallel to Columns. Cell
// values are pre-formatted strings — the table does not know about the
// domain types.
//
// This is the load-bearing struct for the table-rendering path the CLI
// commands use to build human-readable output. --json mode bypasses it
// entirely (the envelope carries the structured data instead); tables are
// strictly the colored/text-mode path.
type Table struct {
	Columns []string
	Rows    [][]string
}

// ANSI palette. The hex values mirror the CP AXTRA palette in
// docs/07-cli-spec.md "Terminal theme" — Blue #306FC7, Yellow #F6C24A,
// Green #43938F, Red #DA3832. Keeping the escape prefixes in this package
// means the CLI does not duplicate them; the theme package the spec
// references can wrap these in lipgloss.Style later.
//
// The escapes are deliberately raw strings rather than a color library:
// the package boundary forbids importing a TUI package, and these are the
// minimum surface the renderer needs.
const (
	ansiReset = "\x1b[0m"
	ansiBlue  = "\x1b[38;2;48;111;199m" // CP AXTRA Blue — headers & ids
	ansiGreen = "\x1b[38;2;67;147;143m" // Lotus Green — ready / success
	ansiRed   = "\x1b[38;2;218;56;50m"  // Makro Red — error / destructive
	ansiMuted = "\x1b[2m"               // Faint — unknown / suggested
)

// RenderTable formats t as a slice of rendered lines. The lines are
// separated with '\n'; the caller chooses where to print (stdout for the
// default mode, stderr for --json mode — see docs/07-cli-spec.md
// "Rendering rules": stdout is the payload, stderr is the narration).
//
// noColor suppresses all ANSI escapes — true when:
//   - the user passed --no-color, or
//   - the NO_COLOR env var is set (https://no-color.org/), or
//   - stdout is not a TTY (piped to a file or another process).
//
// isStdoutTTY is the result of IsStdoutTTY at the call site. The two flags
// are passed separately so the function is pure — the test suite can
// assert byte-exact output without manipulating os.Stdout.
//
// The returned slice has one element per output line; the caller joins
// with '\n'. A table with no rows still produces a header line, which is
// how the CLI conveys "this notebook has no sources" without a separate
// "empty" branch.
func RenderTable(t Table, noColor, isStdoutTTY bool) []string {
	if !noColor && isStdoutTTY {
		header := make([]string, len(t.Columns))
		for i, c := range t.Columns {
			header[i] = wrapANSI(c, ansiBlue)
		}
		lines := []string{strings.Join(header, "  ")}
		for _, row := range t.Rows {
			lines = append(lines, renderRow(row, noColor, isStdoutTTY))
		}
		return lines
	}
	// No-color path: same shape, no escapes. Keeping the column alignment
	// at the CLI layer would be nicer (pad to the widest cell), but a
	// tight package without lipgloss means we hand the column-join job
	// to the CLI. The CLI can pad as it wishes.
	header := strings.Join(t.Columns, "  ")
	if header == "" {
		header = ""
	}
	lines := []string{header}
	for _, row := range t.Rows {
		lines = append(lines, strings.Join(row, "  "))
	}
	return lines
}

// renderRow applies the per-cell color rules from docs/07-cli-spec.md to a
// single row. The mapping is:
//
//	"ready"/"completed"   → Green
//	"processing"/"pending"/"in_progress"/"preparing" → Yellow (via CLI)
//	"error"/"failed"      → Red
//	"unknown"/"suggested" → Muted
//
// The actual mapping is delegated to the caller (the cell value is a
// pre-formatted string; the renderer does not parse status enums). For
// tables we render the column header in Blue and leave the cell text
// unstyled — a richer per-cell coloring lives in the theme package the
// CLI uses, and is intentionally out of scope here.
func renderRow(row []string, noColor, isStdoutTTY bool) string {
	if !noColor && isStdoutTTY {
		// Highlight status-looking cells (last column conventionally). The
		// simplest possible rule: if a cell value matches a known status
		// word, wrap it. Anything else renders unstyled.
		out := make([]string, len(row))
		for i, cell := range row {
			out[i] = colorStatus(cell)
		}
		return strings.Join(out, "  ")
	}
	return strings.Join(row, "  ")
}

// colorStatus maps a single cell value to a colored rendering. The match
// is exact and case-insensitive; anything else is returned as-is. This is
// deliberately a tiny helper so the table renderer does not grow a TUI
// dependency.
func colorStatus(cell string) string {
	switch strings.ToLower(strings.TrimSpace(cell)) {
	case "ready", "completed", "success":
		return wrapANSI(cell, ansiGreen)
	case "processing", "pending", "in_progress", "preparing":
		// Yellow belongs to the theme package; use muted as a safe
		// fallback so callers do not see a colorless status cell when
		// ANSI is on. The theme/CLI layer overrides this with the
		// Makro-Yellow style at the lipgloss boundary.
		return wrapANSI(cell, ansiMuted)
	case "error", "failed":
		return wrapANSI(cell, ansiRed)
	case "unknown", "suggested":
		return wrapANSI(cell, ansiMuted)
	default:
		return cell
	}
}

func wrapANSI(s, prefix string) string {
	return prefix + s + ansiReset
}
