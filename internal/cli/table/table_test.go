package table

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/raihankhan/notebooklm-go/internal/app/serialize"
)

// lipglossWidth wraps lipgloss.Width to keep the assertion sites
// short. The test asserts column-alignment without depending on
// ANSI escape sequences (the no-color path does not emit them).
func lipglossWidth(s string) int { return lipgloss.Width(s) }

// TestRenderTableColorOnTTY verifies the colored path: header in
// bold Blue, status cells colored per theme.StatusStyle. We assert
// that the rendered output contains the literal text of the cells
// (escapes are not asserted byte-for-byte because lipgloss's exact
// escape depends on the termenv profile; the goal here is the
// no-ANSI-stripped-on-color-allowed contract).
func TestRenderTableColorOnTTY(t *testing.T) {
	tbl := serialize.Table{
		Columns: []string{"ID", "Title", "Status"},
		Rows: [][]string{
			{"abc", "Research", "ready"},
			{"def", "Draft", "processing"},
			{"ghi", "Failed", "error"},
		},
	}
	got, err := RenderTable(tbl, false, true)
	if err != nil {
		t.Fatalf("RenderTable: %v", err)
	}

	// Every cell text must appear.
	for _, want := range []string{"ID", "Title", "Status", "abc", "Research", "ready", "def", "Draft", "processing", "ghi", "Failed", "error"} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered output missing %q\n-- got --\n%s", want, got)
		}
	}

	// With color allowed, the output must contain at least one
	// ANSI escape — otherwise the renderer accidentally fell back
	// to the no-color path.
	if !strings.Contains(got, "\x1b[") {
		t.Errorf("color-allowed output stripped escapes\n-- got --\n%s", got)
	}
}

// TestRenderTableStripsANSIAgainstNoColor is the no-color path:
// when noColor is true (NO_COLOR env or --no-color flag) every
// escape byte must be absent.
func TestRenderTableStripsANSIAgainstNoColor(t *testing.T) {
	tbl := serialize.Table{
		Columns: []string{"ID", "Status"},
		Rows: [][]string{
			{"abc", "ready"},
		},
	}
	got, err := RenderTable(tbl, true, false)
	if err != nil {
		t.Fatalf("RenderTable: %v", err)
	}
	if strings.Contains(got, "\x1b[") {
		t.Errorf("no-color output contains ANSI escape\n-- got --\n%s", got)
	}
	// Header row text must still be present (just uncolored).
	if !strings.Contains(got, "ID") || !strings.Contains(got, "Status") {
		t.Errorf("no-color output missing header\n-- got --\n%s", got)
	}
	if !strings.Contains(got, "abc") || !strings.Contains(got, "ready") {
		t.Errorf("no-color output missing data row\n-- got --\n%s", got)
	}
}

// TestRenderTableStripsANSIAgainstNonTTY is the same contract as
// the no-color path but triggered by the non-TTY verdict.
func TestRenderTableStripsANSIAgainstNonTTY(t *testing.T) {
	tbl := serialize.Table{
		Columns: []string{"A", "B"},
		Rows: [][]string{
			{"x", "ready"},
		},
	}
	got, err := RenderTable(tbl, false, false)
	if err != nil {
		t.Fatalf("RenderTable: %v", err)
	}
	if strings.Contains(got, "\x1b[") {
		t.Errorf("non-TTY output contains ANSI escape\n-- got --\n%s", got)
	}
}

// TestRenderTableEmptyColumns returns "" with no error. The
// contract is documented in package.go; this test pins it.
func TestRenderTableEmptyColumns(t *testing.T) {
	tbl := serialize.Table{}
	got, err := RenderTable(tbl, false, true)
	if err != nil {
		t.Fatalf("RenderTable(empty): %v", err)
	}
	if got != "" {
		t.Errorf("RenderTable(empty) = %q, want empty string", got)
	}
}

// TestRenderTableEmptyRows verifies that a table with only a
// header (no data rows) still renders the header. The CLI uses
// this to convey "this notebook has no sources" without a separate
// "empty" branch.
func TestRenderTableEmptyRows(t *testing.T) {
	tbl := serialize.Table{Columns: []string{"ID", "Title"}}
	got, err := RenderTable(tbl, true, false)
	if err != nil {
		t.Fatalf("RenderTable: %v", err)
	}
	if !strings.Contains(got, "ID") || !strings.Contains(got, "Title") {
		t.Errorf("empty-rows table missing header\n-- got --\n%s", got)
	}
}

// TestRenderTablePadsColumns verifies column alignment: the widest
// cell in a column dictates that column's width.
func TestRenderTablePadsColumns(t *testing.T) {
	tbl := serialize.Table{
		Columns: []string{"ID", "Title"},
		Rows: [][]string{
			{"abc", "LongerTitle"},
			{"xy", "X"},
		},
	}
	got, err := RenderTable(tbl, true, false)
	if err != nil {
		t.Fatalf("RenderTable: %v", err)
	}
	// Header "Title" is 5 chars wide. "LongerTitle" is 11 chars
	// wide. After padding to 11, the first data row should have
	// exactly 4 trailing spaces after "abc" (ID width 3 → 3). The
	// simpler invariant: every line has the same length (assuming
	// one row per output line).
	lines := strings.Split(got, "\n")
	if len(lines) < 2 {
		t.Fatalf("got %d lines, want at least 2 (header + 2 rows)", len(lines))
	}
	first := lipglossWidth(lines[0])
	for i, l := range lines[1:] {
		if w := lipglossWidth(l); w != first {
			t.Errorf("line %d width = %d, want %d (aligned to header)\n  line: %q", i+1, w, first, l)
		}
	}
}

// TestFormatRowColoring passes through the theme.StatusStyle
// coloring for known status keywords.
func TestFormatRowColoring(t *testing.T) {
	if got := FormatRow("ready"); !strings.Contains(got, "ready") {
		t.Errorf("FormatRow(ready) = %q, missing status text", got)
	}
	if got := FormatRow(""); got != "" {
		t.Errorf("FormatRow(\"\") = %q, want empty", got)
	}
}
