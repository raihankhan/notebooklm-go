// Package cmd tests — seam helpers in cliclient.go.
//
// These tests pin the small set of helpers every leaf command
// relies on: storage-path resolution, active-notebook I/O,
// jsonRequested truth table, and the request id generator.
package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/raihankhan/notebooklm-go/internal/config"
	"github.com/raihankhan/notebooklm-go/internal/paths"
	"github.com/raihankhan/notebooklm-go/notebooklm"
)

func TestNewRequestIDIsFresh(t *testing.T) {
	t.Parallel()
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := newRequestID()
		if id == "" {
			t.Fatalf("newRequestID returned empty")
		}
		if seen[id] {
			t.Fatalf("newRequestID returned duplicate id %q", id)
		}
		seen[id] = true
	}
}

func TestJSONRequestedFlag(t *testing.T) {
	t.Parallel()
	cmd := &cobra.Command{}
	cmd.Flags().Bool("json", false, "")
	if err := cmd.Flags().Set("json", "true"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if !jsonRequested(cmd) {
		t.Errorf("jsonRequested with --json=true = false, want true")
	}
}

func TestJSONRequestedEnv(t *testing.T) {
	// t.Setenv touches the real process env; cannot run in parallel.
	t.Setenv("NOTEBOOKLM_OUTPUT", "json")
	cmd := &cobra.Command{}
	cmd.Flags().Bool("json", false, "")
	if !jsonRequested(cmd) {
		t.Errorf("jsonRequested with NOTEBOOKLM_OUTPUT=json = false, want true")
	}
}

func TestJSONRequestedFalseWithoutFlagOrEnv(t *testing.T) {
	t.Setenv("NOTEBOOKLM_OUTPUT", "")
	cmd := &cobra.Command{}
	cmd.Flags().Bool("json", false, "")
	if jsonRequested(cmd) {
		t.Errorf("jsonRequested without flag/env = true, want false")
	}
}

// TestStorageOverrideHonorsStorageFlag verifies the --storage
// flag short-circuit; the long env-resolution branch is covered
// in helpers_test.go (the integration tests).
func TestStorageOverrideHonorsStorageFlag(t *testing.T) {
	t.Parallel()
	cmd := &cobra.Command{}
	cmd.Flags().String(flagStorage, "", "")
	if err := cmd.Flags().Set(flagStorage, "/tmp/foo.json"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if got := storageOverride(cmd); got != "/tmp/foo.json" {
		t.Errorf("storageOverride = %q, want /tmp/foo.json", got)
	}
}

func TestSetFactoryRoundTrip(t *testing.T) {
	t.Parallel()
	wantCalled := false
	stub := func(_ *cobra.Command, _ context.Context) (*notebooklm.Client, error) {
		wantCalled = true
		return nil, errors.New("stub: not opening a real client")
	}
	restore := SetFactory(stub)
	defer restore()

	if _, err := newClient(&cobra.Command{}, context.Background()); err == nil ||
		!strings.Contains(err.Error(), "stub") {
		t.Errorf("newClient did not call stub factory: err=%v", err)
	}
	if !wantCalled {
		t.Errorf("stub factory was not invoked")
	}
}

func TestSetFactoryNilRestoresDefault(t *testing.T) {
	restore := SetFactory(nil)
	defer restore()
	if currentFactory == nil {
		t.Errorf("SetFactory(nil) did not restore default factory")
	}
}

// TestEmitJSONWritesEnvelopeByteClean verifies the --json path:
// one write to stdout, no writes to stderr, the body parses as
// the canonical { ok, data, request_id } envelope.
func TestEmitJSONWritesEnvelopeByteClean(t *testing.T) {
	t.Parallel()
	cmd := &cobra.Command{}
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := emitJSON(cmd, map[string]any{"id": "abc"}, "req-1"); err != nil {
		t.Fatalf("emitJSON: %v", err)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr polluted: %q", stderr.String())
	}
	if !bytes.HasSuffix(stdout.Bytes(), []byte("\n")) {
		t.Errorf("stdout missing trailing newline: %q", stdout.String())
	}
	var env struct {
		OK        bool           `json:"ok"`
		Data      map[string]any `json:"data"`
		RequestID string         `json:"request_id"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &env); err != nil {
		t.Fatalf("envelope does not parse: %v\nbody=%q", err, stdout.String())
	}
	if !env.OK || env.RequestID != "req-1" || env.Data["id"] != "abc" {
		t.Errorf("envelope shape wrong: %+v", env)
	}
}

// TestErrUsageAndErrCancelledAreTyped verifies both helpers
// surface canonical codes the root error handler recognizes.
func TestErrUsageAndErrCancelledAreTyped(t *testing.T) {
	t.Parallel()
	if err := errUsage("bad arg"); err == nil ||
		!strings.Contains(err.Error(), "bad arg") {
		t.Errorf("errUsage: %v", err)
	}
	if err := errCancelled(); err == nil ||
		!strings.Contains(err.Error(), "cancel") {
		t.Errorf("errCancelled: %v", err)
	}
}

// TestResolveContextPathHonorsOverride verifies the --storage
// short-circuit on resolveContextPath (the long env path is
// covered separately by the config layer).
func TestResolveContextPathHonorsOverride(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	storage := filepath.Join(dir, "storage.json")
	want := filepath.Join(dir, contextFilename)
	got, err := resolveContextPath(storage)
	if err != nil {
		t.Fatalf("resolveContextPath: %v", err)
	}
	if got != want {
		t.Errorf("resolveContextPath = %q, want %q", got, want)
	}
}

// TestSetAndGetActiveNotebook exercises the full write+read loop.
func TestSetAndGetActiveNotebook(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	storage := filepath.Join(dir, "storage.json")
	want := contextDoc{NotebookID: "abc-123", Title: "Research"}

	if _, err := setActiveNotebook(storage, want); err != nil {
		t.Fatalf("setActiveNotebook: %v", err)
	}
	got, err := getActiveNotebook(storage)
	if err != nil {
		t.Fatalf("getActiveNotebook: %v", err)
	}
	if got.NotebookID != want.NotebookID || got.Title != want.Title {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// TestGetActiveNotebookMissingIsSentinel verifies the empty-state
// branch (clean install / never-used profile).
func TestGetActiveNotebookMissingIsSentinel(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	storage := filepath.Join(dir, "storage.json")
	_, err := getActiveNotebook(storage)
	if !errors.Is(err, errContextNotFound) {
		t.Errorf("err = %v, want errContextNotFound", err)
	}
}

// TestClearActiveNotebookMissingIsOK clears a missing pointer
// idempotently (the command is allowed under docs/07-cli-spec.md).
func TestClearActiveNotebookMissingIsOK(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	storage := filepath.Join(dir, "storage.json")
	if err := clearActiveNotebook(storage); err != nil {
		t.Errorf("clearActiveNotebook(missing) = %v, want nil", err)
	}
}

// TestResolveContextPathViaConfig exercises the env-resolution
// branch (no --storage override): config.Resolve + paths.ProfileDir.
func TestResolveContextPathViaConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("NOTEBOOKLM_HOME", home)
	cfg, err := config.Resolve()
	if err != nil {
		t.Fatalf("config.Resolve: %v", err)
	}
	want, perr := paths.ProfileDir(cfg.Profile)
	if perr != nil {
		t.Fatalf("paths.ProfileDir: %v", perr)
	}
	wantPath := filepath.Join(want, contextFilename)

	got, err := resolveContextPath("")
	if err != nil {
		t.Fatalf("resolveContextPath: %v", err)
	}
	if got != wantPath {
		t.Errorf("got %q, want %q", got, wantPath)
	}
	_ = os.Unsetenv // keep the import live if future tests need it.
}
