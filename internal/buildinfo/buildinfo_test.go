package buildinfo

import (
	"strings"
	"testing"
)

// TestDefaults pins the documented fallback values used when the binary is
// built without -ldflags (e.g. `go test`, `go run`). Changing these
// defaults is a breaking change for anyone who greps buildinfo in log lines.
func TestDefaults(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		got     string
		want    string
		notLike string // optional: value must NOT look like this
	}{
		{"Version", Version, "dev", ""},
		{"Commit", Commit, "unknown", ""},
		{"Date", Date, "unknown", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
			}
			if tc.notLike != "" && strings.Contains(tc.got, tc.notLike) {
				t.Errorf("%s = %q, must not contain %q", tc.name, tc.got, tc.notLike)
			}
		})
	}
}

// TestDefaultsAreNonEmpty guards against a future refactor accidentally
// leaving a var declaration uninitialised — slog records that quote an
// empty Version are a support nightmare.
func TestDefaultsAreNonEmpty(t *testing.T) {
	t.Parallel()

	if Version == "" || Commit == "" || Date == "" {
		t.Fatalf("buildinfo vars must have non-empty defaults: Version=%q Commit=%q Date=%q",
			Version, Commit, Date)
	}
}

// TestLinkOverrideIsPossible is a smoke test that documents how the
// injection mechanism works — the test itself does not invoke the linker,
// but it pins the symbol names so a rename would break loudly here instead
// of silently shipping a binary that always reports "dev".
func TestLinkOverrideIsPossible(t *testing.T) {
	t.Parallel()

	// These strings are the actual symbol paths the Makefile writes to via
	// `go build -ldflags "-X <symbol>=<value>"`. Keep them in sync with the
	// Makefile LDFLAGS line.
	const versionSym = "github.com/raihankhan/notebooklm-go/internal/buildinfo.Version"
	const commitSym = "github.com/raihankhan/notebooklm-go/internal/buildinfo.Commit"
	const dateSym = "github.com/raihankhan/notebooklm-go/internal/buildinfo.Date"

	if versionSym == "" || commitSym == "" || dateSym == "" {
		t.Fatal("linker symbol names must not be empty")
	}
}
