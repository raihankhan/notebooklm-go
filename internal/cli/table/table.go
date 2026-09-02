// Package table is the canonical CLI table renderer. Every list
// command (notebook list, source list, artifact list, …) projects
// its data into a serialize.Table and hands it to RenderTable.
//
// The renderer is split from internal/app/serialize/tables.go for
// two reasons:
//
//  1. The serialize package is mode=internal in boundaries.yaml and
//     cannot import lipgloss; the table renderer here can.
//  2. serialize.Table is the structural shape (Columns + Rows);
//     theme-aware rendering is the CLI's concern, not the app
//     layer's.
//
// The package boundary contract:
//
//   - NO_COLOR and a non-TTY stdout produce zero ANSI escapes. The
//     table looks like "Col1  Col2  Col3" with no color, regardless
//     of which colors the data would have used.
//   - The header row is bold Blue.
//   - The status-looking cells (matching the keyword table in
//     internal/cli/theme.StatusStyle) get the matching color.
//   - A table with no rows still emits a header row, which is how
//     the CLI conveys "this notebook has no sources" without a
//     separate "empty" branch.
package table

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/raihankhan/notebooklm-go/internal/app/serialize"
	"github.com/raihankhan/notebooklm-go/internal/cli/theme"
)

// RenderTable formats t as a single rendered string. The function
// applies the CP AXTRA palette (via theme) and respects NO_COLOR +
// non-TTY semantics.
//
// The two flags noColor and isStdoutTTY mirror
// serialize.RenderTable's contract: passed separately so the
// function is pure and tests can assert byte-exact output without
// manipulating os.Stdout.
//
// The returned string is a single line per row, joined with '\n',
// without a trailing newline. The caller decides where to print
// (stdout for the default mode, stderr under --json — see
// docs/07-cli-spec.md "Rendering rules": stdout is the payload,
// stderr is the narration).
//
// A degenerate table (empty Columns) returns "" — the caller
// chooses whether to emit a "no results" line in that case.
func RenderTable(t serialize.Table, noColor, isStdoutTTY bool) (string, error) {
	if len(t.Columns) == 0 {
		return "", nil
	}
	color := !noColor && isStdoutTTY

	// Pad each cell to the width of its column header. The renderer
	// does not know which column carries status keywords (a future
	// improvement is a per-column role annotation), so we pad
	// uniformly — the colored status cell has a slightly wider
	// visual footprint than the uncolored one but the column
	// alignment is more useful than a tighter-but-staggered row.
	widths := make([]int, len(t.Columns))
	for i, h := range t.Columns {
		widths[i] = lipgloss.Width(h)
	}
	for _, row := range t.Rows {
		for i, cell := range row {
			if i >= len(widths) {
				break
			}
			w := lipgloss.Width(cell)
			if w > widths[i] {
				widths[i] = w
			}
		}
	}

	// Styles are bound to the renderer so noColor is honored at the
	// lipgloss layer (the default renderer auto-detects the test
	// environment as no-color, which would make the color-allowed
	// path look identical to the color-stripped path). We use a
	// hand-built lipgloss renderer pinned to the right profile so
	// the test that calls RenderTable(t, false, true) actually
	// emits escape sequences.
	var profile termenv.Profile
	if color {
		profile = termenv.TrueColor
	} else {
		profile = termenv.Ascii
	}
	r := lipgloss.NewRenderer(os.Stdout)
	r.SetColorProfile(profile)
	headerStyle := r.NewStyle().Foreground(theme.Blue).Bold(true)

	// Header line — bold Blue when color is allowed, plain otherwise.
	headerCells := make([]string, len(t.Columns))
	for i, h := range t.Columns {
		if color {
			headerCells[i] = headerStyle.Render(pad(h, widths[i]))
		} else {
			headerCells[i] = pad(h, widths[i])
		}
	}
	lines := []string{strings.Join(headerCells, "  ")}

	// Body rows — color the status-looking cells per theme.StatusStyle.
	for _, row := range t.Rows {
		cells := make([]string, len(t.Columns))
		for i := range t.Columns {
			cell := ""
			if i < len(row) {
				cell = row[i]
			}
			padded := pad(cell, widths[i])
			if color {
				cells[i] = applyStatusStyle(r, cell, padded)
			} else {
				cells[i] = padded
			}
		}
		lines = append(lines, strings.Join(cells, "  "))
	}

	return strings.Join(lines, "\n"), nil
}

// applyStatusStyle is the renderer-bound twin of
// theme.StatusStyle. The function re-implements the four-way mapping
// using the supplied lipgloss renderer so a forced color profile
// (set by tests) is honored.
func applyStatusStyle(r *lipgloss.Renderer, status, padded string) string {
	switch theme.ToLower(theme.Trim(status)) {
	case "ready", "completed", "success":
		return r.NewStyle().Foreground(theme.Green).Render(padded)
	case "processing", "pending", "in_progress", "preparing":
		return r.NewStyle().Foreground(theme.Yellow).Render(padded)
	case "error", "failed":
		return r.NewStyle().Foreground(theme.Red).Bold(true).Render(padded)
	default:
		return r.NewStyle().Faint(true).Render(padded)
	}
}

// pad right-pads s with spaces to width w. If s is already at least
// w wide the string is returned unchanged. The function is a small
// helper because strings.Repeat + len-aware concatenation in every
// call site is ugly; lipgloss.Width is the right measurement because
// it understands escape sequences (which the colored path may have
// inserted earlier in the same row).
func pad(s string, w int) string {
	sw := lipgloss.Width(s)
	if sw >= w {
		return s
	}
	return s + strings.Repeat(" ", w-sw)
}

// FormatRow is a small convenience for commands that need to
// colorize a single row (e.g. progress lines, --wait output). The
// function applies the same status-color mapping as RenderTable
// but does not pad columns — the caller decides.
//
// An empty status argument disables the mapping and renders the
// row unstyled. Pass a known status keyword to color it per the
// theme table.
func FormatRow(status string) string {
	if status == "" {
		return status
	}
	return theme.StatusStyle(status).Render(status)
}

// ErrEmptyColumns is reserved for callers that want to detect the
// degenerate empty-Columns case explicitly (today RenderTable
// returns "" with no error; future versions may surface a typed
// error). The placeholder keeps the package surface symmetric with
// the rest of the CLI.
var ErrEmptyColumns = fmt.Errorf("table: empty Columns") //nolint:gochecknoglobals // sentinel

// Compile-time guard: serialize.Table must remain the canonical
// shape — the table renderer cannot drift from it.
var _ = serialize.Table{}
