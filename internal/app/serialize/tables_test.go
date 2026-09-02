// Tests for the table renderer. The renderer is small on purpose — every
// CLI command ultimately projects its data through it before going to a
// TTY-aware formatter, so the byte-exact output must hold.
package serialize

import (
	"strings"
	"testing"
)

// TestRenderTable_NoColor asserts the bytes returned in no-color mode
// contain no ANSI escapes. NO_COLOR semantics: when the env is unset but
// stdout is not a TTY, noColor is what the CLI passes in.
func TestRenderTable_NoColor(t *testing.T) {
	tbl := Table{
		Columns: []string{"ID", "Status"},
		Rows: [][]string{
			{"src-1", "ready"},
			{"src-2", "error"},
		},
	}
	lines := RenderTable(tbl, true, false)
	if len(lines) != 3 { // header + 2 rows
		t.Fatalf("len(lines) = %d, want 3; got %v", len(lines), lines)
	}
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "\x1b[") {
		t.Fatalf("no-color mode must not emit ANSI escapes; got %q", joined)
	}
	if !strings.Contains(joined, "ready") || !strings.Contains(joined, "error") {
		t.Fatalf("row data lost in no-color render: %q", joined)
	}
}

// TestRenderTable_NonTTY_NoColor asserts the AC6 NO_COLOR-and-non-TTY
// contract: even if the caller passes isStdoutTTY=true, noColor=true
// suppresses every escape.
func TestRenderTable_NonTTY_NoColor(t *testing.T) {
	tbl := Table{Columns: []string{"ID"}, Rows: [][]string{{"x"}}}
	lines := RenderTable(tbl, true, true)
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "\x1b[") {
		t.Fatalf("no-color override must suppress ANSI; got %q", joined)
	}
}

// TestRenderTable_ColorOnTTY asserts that color escapes appear when both
// noColor=false and isStdoutTTY=true.
func TestRenderTable_ColorOnTTY(t *testing.T) {
	tbl := Table{
		Columns: []string{"ID", "Status"},
		Rows: [][]string{
			{"src-1", "ready"},
			{"src-2", "error"},
		},
	}
	lines := RenderTable(tbl, false, true)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "\x1b[") {
		t.Fatalf("expected ANSI escapes in color-on-TTY mode; got %q", joined)
	}
	// Status cells should be color-tagged: "ready" green, "error" red.
	if !strings.Contains(joined, "\x1b[38;2;67;147;143m") {
		t.Fatalf("green escape missing for ready cell: %q", joined)
	}
	if !strings.Contains(joined, "\x1b[38;2;218;56;50m") {
		t.Fatalf("red escape missing for error cell: %q", joined)
	}
	if !strings.Contains(joined, "\x1b[0m") {
		t.Fatalf("ANSI reset missing — output would bleed color into the prompt: %q", joined)
	}
	// Header column should be blue.
	if !strings.Contains(joined, "\x1b[38;2;48;111;199m") {
		t.Fatalf("blue escape missing for header: %q", joined)
	}
}

// TestRenderTable_EmptyRows ensures an empty Rows slice still produces a
// header line — the "this notebook has no sources" path the CLI uses to
// avoid a separate empty-state branch.
func TestRenderTable_EmptyRows(t *testing.T) {
	tbl := Table{Columns: []string{"A", "B"}}
	lines := RenderTable(tbl, true, false)
	if len(lines) != 1 {
		t.Fatalf("empty rows must still emit header; got %v", lines)
	}
	if strings.TrimSpace(lines[0]) != "A  B" {
		t.Fatalf("header malformed: %q", lines[0])
	}
}

// TestIsStdoutTTY_DoesNotPanic is a defensive test: in the test runner
// stdout is a pipe (not a TTY), so the function must return false without
// panicking. It would be nice to also assert true under `script`, but
// running that in CI is too brittle to be worth the maintenance.
func TestIsStdoutTTY_DoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("IsStdoutTTY panicked: %v", r)
		}
	}()
	_ = IsStdoutTTY()
}
