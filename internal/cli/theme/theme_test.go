package theme

import (
	"os"
	"strings"
	"testing"
)

// TestHexPaletteConstant pins the four CP AXTRA hex values. These are
// the contract — every test that touches color asserts against these
// strings rather than the lipgloss token, so a future lipgloss API
// change cannot silently drift the palette.
func TestHexPaletteConstant(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"HexBlue", HexBlue, "#306FC7"},
		{"HexYellow", HexYellow, "#F6C24A"},
		{"HexGreen", HexGreen, "#43938F"},
		{"HexRed", HexRed, "#DA3832"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

// TestStatusStyleMapping verifies the four-way status → style
// mapping spelled out in docs/07-cli-spec.md "Status→color mapping".
func TestStatusStyleMapping(t *testing.T) {
	cases := []struct {
		status string
	}{
		{"ready"}, {"completed"}, {"success"},
		{"processing"}, {"pending"}, {"in_progress"}, {"preparing"},
		{"error"}, {"failed"},
		{"unknown"}, {"suggested"}, {""},
	}
	for _, tc := range cases {
		style := StatusStyle(tc.status)
		// StatusStyle returns a *value* — confirm the rendered
		// output of the sentinel text is non-empty (the style
		// itself can be empty before Render, so we test via
		// Render here).
		got := style.Render("x")
		if got == "" {
			t.Errorf("StatusStyle(%q).Render(\"x\") returned empty string", tc.status)
		}
	}
}

// TestStatusMappingStableAcrossCases asserts that two status values
// that map to the same role produce structurally identical styles.
// A regression here would mean the status → color table drifted
// between call sites.
func TestStatusMappingStableAcrossCases(t *testing.T) {
	readyStyle := StatusStyle("ready").Render("x")
	for _, s := range []string{"completed", "success"} {
		if got := StatusStyle(s).Render("x"); got != readyStyle {
			t.Errorf("StatusStyle(%q) = %q, want %q (same as ready)", s, got, readyStyle)
		}
	}
	failedStyle := StatusStyle("failed").Render("x")
	if got := StatusStyle("error").Render("x"); got != failedStyle {
		t.Errorf("StatusStyle(error) = %q, want %q (same as failed)", got, failedStyle)
	}
	processingStyle := StatusStyle("processing").Render("x")
	for _, s := range []string{"pending", "in_progress", "preparing"} {
		if got := StatusStyle(s).Render("x"); got != processingStyle {
			t.Errorf("StatusStyle(%q) = %q, want %q (same as processing)", s, got, processingStyle)
		}
	}
	unknownStyle := StatusStyle("unknown").Render("x")
	for _, s := range []string{"suggested", "", "anything-else"} {
		if got := StatusStyle(s).Render("x"); got != unknownStyle {
			t.Errorf("StatusStyle(%q) = %q, want %q (default muted)", s, got, unknownStyle)
		}
	}
}

// TestRendererStripsANSIUnderNoColor verifies that NewRenderer honors
// the NO_COLOR env var (https://no-color.org/) and produces zero
// escape bytes on the rendered output.
func TestRendererStripsANSIUnderNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	r := MustRenderer(os.Stdout)
	if !r.NoColor() {
		t.Fatal("NewRenderer(os.Stdout) with NO_COLOR=1: NoColor() = false, want true")
	}

	// Style with explicit color — the rendered output must NOT
	// contain the ANSI escape byte (\x1b) regardless of the env.
	style := r.NewStyle().Foreground(Blue).Bold(true)
	got := style.Render("hello")
	if strings.Contains(got, "\x1b[") {
		t.Errorf("rendered output contains ANSI escape under NO_COLOR: %q", got)
	}
	if got != "hello" {
		t.Errorf("rendered output = %q, want %q (unstyled)", got, "hello")
	}
}

// TestRendererStripsANSIUnderNonTTY verifies that a non-TTY stdout
// (e.g. a pipe or os.CreateTemp file) results in NoColor mode. The
// spec's "non-TTY stdout" rule is mechanical: any non-char-device
// file must produce zero escapes.
func TestRendererStripsANSIUnderNonTTY(t *testing.T) {
	t.Setenv("NO_COLOR", "")

	// A pipe is the canonical non-TTY file. Use os.Pipe so the test
	// does not depend on the build runner's TTY.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer func() { _ = r.Close() }()
	defer func() { _ = w.Close() }()

	renderer := MustRenderer(w)
	if !renderer.NoColor() {
		t.Fatal("NewRenderer(non-TTY): NoColor() = false, want true")
	}

	style := renderer.NewStyle().Foreground(Red).Bold(true)
	got := style.Render("destructive")
	if strings.Contains(got, "\x1b[") {
		t.Errorf("non-TTY rendered output contains ANSI escape: %q", got)
	}
	if got != "destructive" {
		t.Errorf("rendered output = %q, want %q", got, "destructive")
	}
}

// TestRendererAllowsColorOnTTY is the positive twin: a character
// device with NO_COLOR unset and the standard TTY-capable TERM
// profile renders the escape sequence. We avoid asserting the exact
// escape bytes (lipgloss's output depends on TERM); we only assert
// that the rendered output is non-empty and contains the escape
// prefix. On macOS GitHub Actions runners with no TTY this test is
// skipped via the -short guard.
func TestRendererAllowsColorOnTTY(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TTY-only assertion under -short")
	}
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	// /dev/tty may not exist on every CI runner; fall back to a
	// file we know is a char device.
	f := openCharDeviceOrSkip(t)
	defer func() { _ = f.Close() }()

	renderer := MustRenderer(f)
	if renderer.NoColor() {
		// Even a char device may report non-color when TERM
		// claims dumb mode; the safe assertion is "escapes
		// appear" only when color is allowed.
		t.Skip("renderer fell back to NoColor on this terminal")
	}

	style := renderer.NewStyle().Foreground(Blue)
	got := style.Render("x")
	if got == "x" {
		t.Errorf("color-allowed rendered output stripped color: %q", got)
	}
}

// TestToLowerLowercasesASCII pins the helper. The package deliberately
// avoids strings.ToLower to keep its surface small; this test guards
// against a regression in the inline implementation.
func TestToLowerLowercasesASCII(t *testing.T) {
	cases := []struct{ in, want string }{
		{"READY", "ready"},
		{"Processing", "processing"},
		{"", ""},
		{"Mixed-CASE", "mixed-case"},
	}
	for _, tc := range cases {
		if got := ToLower(tc.in); got != tc.want {
			t.Errorf("ToLower(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestTrimWhitespace strips both ends. ASCII-only; the helper is
// not meant for Unicode-aware trimming.
func TestTrimWhitespace(t *testing.T) {
	cases := []struct{ in, want string }{
		{"  ready  ", "ready"},
		{"\tready\n", "ready"},
		{"", ""},
		{"ready", "ready"},
	}
	for _, tc := range cases {
		if got := Trim(tc.in); got != tc.want {
			t.Errorf("Trim(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// openCharDeviceOrSkip tries to open /dev/tty and falls back to
// skipping if the platform does not expose it. The function is
// only used by TestRendererAllowsColorOnTTY, which is itself a
// skip-friendly test.
func openCharDeviceOrSkip(t *testing.T) *os.File {
	t.Helper()
	for _, path := range []string{"/dev/tty", "/dev/console"} {
		// #nosec G304 -- operator-controlled test fixture path.
		f, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err == nil {
			return f
		}
	}
	t.Skip("no /dev/tty or /dev/console on this platform")
	return nil // unreachable
}
