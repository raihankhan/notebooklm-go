// Pre-commit guard test: walks every committed cassette in
// testdata/cassettes/ and fails if any credential pattern survives
// into the file. The harness's AfterCaptureHook rewrites credentials
// on capture; this test is the second, independent layer of the
// two-layer scrubbing guarantee in docs/11-testing-strategy.md §"Tier
// 2 — Cassette replay".
//
// Boundary: this file lives under internal/web/policy, which is
// mode=internal in boundaries.yaml. The guard walks its own
// testdata with stdlib only; no third-party imports.
package policy

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/raihankhan/notebooklm-go/internal/redact"
)

// cassetteDir is the canonical location of every committed
// cassette. Tests record into this directory so the guard sees what
// CI sees.
const cassetteDir = "testdata/cassettes"

// TestNoCredentialInCassettes is the guardrail test: every committed
// cassette must pass redact.Apply's full credential sweep. The
// function is the same primitive every log/error/debug preview
// runs through (docs/AGENTS.md rule 4), so a green test is a
// load-bearing property: a future scrubber that drops a pattern
// from the redact primitive fails here at PR review time, not in
// production.
func TestNoCredentialInCassettes(t *testing.T) {
	t.Parallel()

	files := cassettes(t)
	if len(files) == 0 {
		// No cassettes committed yet — the test is informational.
		// We do NOT fail because the directory is empty; that is
		// the correct state for a brand-new module. The first
		// recording lands here and the test fires for it.
		t.Skip("no cassettes committed under testdata/cassettes/")
	}

	for _, path := range files {
		path := path
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()
			raw, err := os.ReadFile(path) // #nosec G304 -- test-owned file.
			if err != nil {
				t.Fatalf("read cassette: %v", err)
			}
			out := redact.Apply(raw)
			if !bytes.Equal(raw, out) {
				t.Fatalf("cassette %s contains a credential pattern\n"+
					"re-run `go run ./internal/tools/scrubhar/cmd %s`\n"+
					"and commit the scrubbed result",
					path, path)
			}
		})
	}
}

// cassettes returns every .yaml file under cassetteDir that is not
// the README or a hidden file. The README is allowed to contain
// example credential shapes (e.g. as illustrative documentation);
// the guard must not flag those.
func cassettes(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(cassetteDir)
	if err != nil {
		t.Fatalf("read cassette dir: %v", err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if !strings.HasSuffix(name, ".yaml") {
			continue
		}
		out = append(out, filepath.Join(cassetteDir, name))
	}
	return out
}
