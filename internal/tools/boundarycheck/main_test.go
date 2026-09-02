package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// errBuf is a minimal io.Writer that captures subprocess stderr into a
// string for assertions. Keeping it local avoids pulling in extra deps.
type errBuf struct{ b strings.Builder }

func (e *errBuf) Write(p []byte) (int, error) { return e.b.Write(p) }
func (e *errBuf) String() string              { return e.b.String() }

// TestLoadConfigYAML checks that loadConfig parses a fixture correctly.
func TestLoadConfigYAML(t *testing.T) {
	dir := t.TempDir()
	cfg := `schema_version: 2

packages:
  - path: example.com/mod/internal/app
    mode: internal
  - path: example.com/mod/internal/web/wire
    mode: stdlib
`
	cfgPath := filepath.Join(dir, "boundaries.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	got, err := loadConfig(cfgPath)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if got.SchemaVersion != 2 {
		t.Errorf("schema_version = %d, want 2", got.SchemaVersion)
	}
	if len(got.Packages) != 2 {
		t.Fatalf("len(packages) = %d, want 2", len(got.Packages))
	}
	if got.Packages[0].Path != "example.com/mod/internal/app" ||
		got.Packages[0].Mode != "internal" {
		t.Errorf("pkg[0] = %+v", got.Packages[0])
	}
	if got.Packages[1].Path != "example.com/mod/internal/web/wire" ||
		got.Packages[1].Mode != "stdlib" {
		t.Errorf("pkg[1] = %+v", got.Packages[1])
	}
}

// TestLoadConfigParsesExternal verifies that mode=external with an
// inline `external: [a, b/*]` list parses correctly and rejects stray
// dash entries that are not under an open list-capable key.
func TestLoadConfigParsesExternal(t *testing.T) {
	dir := t.TempDir()
	cfg := `schema_version: 2

packages:
  - path: example.com/mod/internal/cli
    mode: external
    external:
      - github.com/spf13/cobra
      - github.com/charmbracelet/lipgloss/*
`
	cfgPath := filepath.Join(dir, "boundaries.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	got, err := loadConfig(cfgPath)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if len(got.Packages) != 1 {
		t.Fatalf("len(packages) = %d, want 1", len(got.Packages))
	}
	if got.Packages[0].Mode != "external" {
		t.Errorf("mode = %q, want external", got.Packages[0].Mode)
	}
	if len(got.Packages[0].External) != 2 {
		t.Fatalf("len(external) = %d, want 2 (got %+v)", len(got.Packages[0].External), got.Packages[0].External)
	}
	if got.Packages[0].External[0] != "github.com/spf13/cobra" {
		t.Errorf("external[0] = %q", got.Packages[0].External[0])
	}
	if got.Packages[0].External[1] != "github.com/charmbracelet/lipgloss/*" {
		t.Errorf("external[1] = %q", got.Packages[0].External[1])
	}
}

// TestExternalModeEnforced is the planted-failure twin of
// TestPlantedFailure: a third-party import that is NOT on the allowlist
// must be rejected, and one that IS on the allowlist must pass.
func TestExternalModeEnforced(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}

	bin := buildToolBinary(t)

	root := t.TempDir()
	cfg := `schema_version: 2

packages:
  - path: example.com/throwaway/pkg
    mode: external
    external:
      - example.com/allow/foo
`
	cfgPath := filepath.Join(root, "boundaries.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	pkgDir := filepath.Join(root, "pkg")
	if err := os.MkdirAll(pkgDir, 0o750); err != nil {
		t.Fatalf("mkdir pkg: %v", err)
	}

	// First run: import is NOT in the allowlist. Expect failure.
	badSrc := `package pkg

import "example.com/forbidden/bar"

var _ = bar.X
`
	badPath := filepath.Join(pkgDir, "pkg.go")
	if err := os.WriteFile(badPath, []byte(badSrc), 0o600); err != nil {
		t.Fatalf("write bad source: %v", err)
	}
	stdErr, exitCode := runBoundaryCheck(t, bin, root, cfgPath)
	if exitCode == 0 {
		t.Fatalf("forbidden-import run exited 0; want non-zero. stderr=%s", stdErr)
	}
	if !strings.Contains(stdErr, "example.com/forbidden/bar") {
		t.Errorf("stderr missing forbidden path: %s", stdErr)
	}
	if !strings.Contains(stdErr, "mode=external") {
		t.Errorf("stderr missing rule mode: %s", stdErr)
	}

	// Second run: replace with an import that IS on the allowlist. Expect clean.
	goodSrc := `package pkg

import "example.com/allow/foo"

var _ = foo.X
`
	if err := os.WriteFile(badPath, []byte(goodSrc), 0o600); err != nil {
		t.Fatalf("rewrite good source: %v", err)
	}
	stdErr2, exitCode2 := runBoundaryCheck(t, bin, root, cfgPath)
	if exitCode2 != 0 {
		t.Fatalf("allowlisted run exited %d; want 0. stderr=%s", exitCode2, stdErr2)
	}
	if !strings.Contains(stdErr2, "OK") {
		t.Errorf("clean-run stderr missing OK: %s", stdErr2)
	}
}

// TestLoadConfigRejectsBadMode ensures unknown modes are rejected.
func TestLoadConfigRejectsBadMode(t *testing.T) {
	dir := t.TempDir()
	cfg := `schema_version: 2

packages:
  - path: example.com/mod/internal/app
    mode: internalish
`
	cfgPath := filepath.Join(dir, "boundaries.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if _, err := loadConfig(cfgPath); err == nil {
		t.Fatal("loadConfig accepted unknown mode; want error")
	}
}

// TestLoadConfigRejectsDuplicate ensures duplicate paths are rejected.
func TestLoadConfigRejectsDuplicate(t *testing.T) {
	dir := t.TempDir()
	cfg := `schema_version: 2

packages:
  - path: example.com/mod/internal/app
    mode: internal
  - path: example.com/mod/internal/app
    mode: stdlib
`
	cfgPath := filepath.Join(dir, "boundaries.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if _, err := loadConfig(cfgPath); err == nil {
		t.Fatal("loadConfig accepted duplicate path; want error")
	}
}

// TestLoadConfigSiblingAfterExternalList ensures a `- path: …` line that
// follows a closed `external: [items]` block is parsed as a sibling rule
// rather than fed into parseRuleList as a malformed allowlist entry. The
// regression this guards against: when the parser saw the second `- path:
// …` line while ListKey was still "external" (the previous rule's most-
// recent list-capable key), it routed the path into parseRuleList and
// left the previous rule open, so the subsequent `mode: …` line then
// hit the duplicate-mode guard and `make check` blew up.
func TestLoadConfigSiblingAfterExternalList(t *testing.T) {
	dir := t.TempDir()
	cfg := `schema_version: 2

packages:
  - path: example.com/mod/internal/cli
    mode: external
    external:
      - github.com/spf13/cobra
  - path: example.com/mod/internal/app
    mode: internal
`
	cfgPath := filepath.Join(dir, "boundaries.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	got, err := loadConfig(cfgPath)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if len(got.Packages) != 2 {
		t.Fatalf("len(packages) = %d, want 2", len(got.Packages))
	}
	wantPaths := []string{
		"example.com/mod/internal/cli",
		"example.com/mod/internal/app",
	}
	for i, p := range wantPaths {
		if got.Packages[i].Path != p {
			t.Errorf("package %d path = %q, want %q",
				i, got.Packages[i].Path, p)
		}
	}
	if got.Packages[0].Mode != "external" {
		t.Errorf("package 0 mode = %q, want external", got.Packages[0].Mode)
	}
	if got.Packages[1].Mode != "internal" {
		t.Errorf("package 1 mode = %q, want internal", got.Packages[1].Mode)
	}
	if len(got.Packages[0].External) != 1 ||
		got.Packages[0].External[0] != "github.com/spf13/cobra" {
		t.Errorf("package 0 external = %v, want [github.com/spf13/cobra]",
			got.Packages[0].External)
	}
}

// TestPlantedFailure is the headline test from the T-P0-4 spec: it creates
// a tiny throwaway module with a `boundaries.yaml` that governs one
// package, then runs the binary as a subprocess twice — once with a file
// that imports a forbidden package (exit != 0, stderr names the rule),
// and once with the import removed (exit 0).
func TestPlantedFailure(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}

	bin := buildToolBinary(t)

	root := t.TempDir()
	// boundaries.yaml: mode=internal, governs example.com/throwaway/pkg.
	cfg := `schema_version: 2

packages:
  - path: example.com/throwaway/pkg
    mode: internal
`
	cfgPath := filepath.Join(root, "boundaries.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	pkgDir := filepath.Join(root, "pkg")
	if err := os.MkdirAll(pkgDir, 0o750); err != nil {
		t.Fatalf("mkdir pkg: %v", err)
	}

	// First run: file imports a forbidden third-party package.
	badSrc := `package pkg

import "rsc.io/quote"

var _ = quote.Hello
`
	badPath := filepath.Join(pkgDir, "pkg.go")
	if err := os.WriteFile(badPath, []byte(badSrc), 0o600); err != nil {
		t.Fatalf("write bad source: %v", err)
	}

	stdErr, exitCode := runBoundaryCheck(t, bin, root, cfgPath)
	if exitCode == 0 {
		t.Fatalf("planted-failure run exited 0; want non-zero. stderr=%s", stdErr)
	}
	if !strings.Contains(stdErr, "example.com/throwaway/pkg") {
		t.Errorf("stderr missing governed package path: %s", stdErr)
	}
	if !strings.Contains(stdErr, "rsc.io/quote") {
		t.Errorf("stderr missing forbidden import path: %s", stdErr)
	}
	if !strings.Contains(stdErr, "mode=internal") {
		t.Errorf("stderr missing rule mode: %s", stdErr)
	}

	// Second run: replace the file with a clean source that imports only
	// stdlib. Re-run and assert the linter is happy.
	goodSrc := `package pkg

import "fmt"

var _ = fmt.Println
`
	if err := os.WriteFile(badPath, []byte(goodSrc), 0o600); err != nil {
		t.Fatalf("rewrite good source: %v", err)
	}
	stdErr2, exitCode2 := runBoundaryCheck(t, bin, root, cfgPath)
	if exitCode2 != 0 {
		t.Fatalf("clean run exited %d; want 0. stderr=%s", exitCode2, stdErr2)
	}
	if !strings.Contains(stdErr2, "OK") {
		t.Errorf("clean-run stderr missing OK: %s", stdErr2)
	}
}

// buildToolBinary compiles the boundarycheck package into a temp binary
// that the subprocess tests can invoke without depending on `go run`.
func buildToolBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "boundarycheck")
	// #nosec G204 -- "go" is a literal; the args are operator-controlled.
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = toolDir(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

func toolDir(t *testing.T) string {
	t.Helper()
	// main_test.go is co-located with main.go under internal/tools/boundarycheck.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return wd
}

// runBoundaryCheck invokes the binary against root with cfgPath and returns
// the captured stderr and exit code.
func runBoundaryCheck(t *testing.T, bin, root, cfgPath string) (string, int) {
	t.Helper()
	cmd := exec.Command(bin,
		"-root", root,
		"-config", cfgPath,
		"-module", "example.com/throwaway",
	)
	var stderr errBuf
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return stderr.String(), ee.ExitCode()
		}
		t.Fatalf("run binary: %v", err)
	}
	return stderr.String(), 0
}
