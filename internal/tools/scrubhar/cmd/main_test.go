// Tests for the scrubhar CLI driver. The byte-level redaction
// primitive is exercised by tests in the parent scrubhar package;
// here we exercise processFile (the atomic-read+rewrite driver) and
// the -check flag (used by the pre-commit guard wiring in CI).
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/raihankhan/notebooklm-go/internal/tools/scrubhar"
)

// TestScrubHarCLI exercises the on-disk driver end-to-end: write a
// fixture with embedded credentials, run processFile, assert the
// rewritten file no longer contains them, and assert that running
// processFile a second time is a no-op (the file is unchanged).
func TestScrubHarCLI(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "cassette.yaml")
	if err := os.WriteFile(path, loadedFixture(t), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	changed, err := processFile(path, false)
	if err != nil {
		t.Fatalf("processFile: %v", err)
	}
	if !changed {
		t.Fatalf("processFile did not change file containing credentials")
	}

	rewritten, err := os.ReadFile(path) // #nosec G304 -- test-owned file.
	if err != nil {
		t.Fatalf("read rewritten: %v", err)
	}
	for _, f := range []string{"AEC=AVs14e", "SNlM0e=R2hh", "user@example.com"} {
		if bytes.Contains(rewritten, []byte(f)) {
			t.Errorf("rewritten file still contains %q", f)
		}
	}

	changed, err = processFile(path, false)
	if err != nil {
		t.Fatalf("processFile second: %v", err)
	}
	if changed {
		t.Errorf("processFile rewrote an already-clean file (not idempotent)")
	}
}

// TestScrubHarCheckMode exercises the -check flag: a clean file reports
// changed=false and exits 0, while a dirty file reports changed=true and
// (via the main wrapper) would exit 1. We cannot exercise main's exit
// code without an os.Exit mock, so we drive processFile directly with
// checkOnly=true.
func TestScrubHarCheckMode(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	dirty := filepath.Join(dir, "dirty.yaml")
	if err := os.WriteFile(dirty, loadedFixture(t), 0o600); err != nil {
		t.Fatalf("write dirty: %v", err)
	}
	changed, err := processFile(dirty, true)
	if err != nil {
		t.Fatalf("check dirty: %v", err)
	}
	if !changed {
		t.Errorf("check mode missed credentials in dirty file")
	}
	raw, err := os.ReadFile(dirty) // #nosec G304 -- test-owned file.
	if err != nil {
		t.Fatalf("read dirty: %v", err)
	}
	if !bytes.Contains(raw, []byte("AEC=AVs14e")) {
		t.Errorf("check mode rewrote the file")
	}

	clean := filepath.Join(dir, "clean.yaml")
	cleaned := scrubhar.ScrubBytes(loadedFixture(t))
	if err := os.WriteFile(clean, cleaned, 0o600); err != nil {
		t.Fatalf("write clean: %v", err)
	}
	changed, err = processFile(clean, true)
	if err != nil {
		t.Fatalf("check clean: %v", err)
	}
	if changed {
		t.Errorf("check mode flagged a clean file as dirty")
	}
}

// loadedFixture reads the fixture file committed in the parent
// scrubhar package's testdata so the CLI driver can be tested
// against the same contract as the library.
func loadedFixture(t *testing.T) []byte {
	t.Helper()
	const path = "../testdata/fixture.yaml"
	b, err := os.ReadFile(path) // #nosec G304 -- committed fixture path.
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return b
}
