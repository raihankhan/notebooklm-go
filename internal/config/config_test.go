// Package config — config_test.go.
//
// Coverage target: ≥80% on internal/config (per docs/10-implementation-plan.md
// Phase 3 / T-P3-3 acceptance). The tests below exercise every
// precedence branch (env var wins, default applied), every error
// path (malformed log level, malformed HL, off-allowlist URL), and
// the build-label staleness helper with a committed fixture.
package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/raihankhan/notebooklm-go/internal/buildinfo"
	"github.com/raihankhan/notebooklm-go/internal/web/wire"
)

// withCleanEnv clears every NOTEBOOKLM_* env var the resolver reads,
// then sets the named ones to the supplied values. Returns a
// cleanup func the caller MUST defer so subsequent tests see a
// pristine environment.
//
// We use os.Unsetenv rather than t.Setenv("","") because an empty
// value is treated as "unset" by the resolver (a documented
// precedence rule), so leaving a real empty value works for the
// default-application path. A test that wants a specific empty
// value should use t.Setenv directly.
func withCleanEnv(t *testing.T, kv map[string]string) func() {
	t.Helper()
	known := []string{
		EnvHome, EnvLogLevel, EnvRPCOverrides, EnvHL, EnvBaseURL,
		EnvProfile, EnvBackend, EnvBL,
	}
	preExisting := make(map[string]string)
	for _, k := range known {
		if v, ok := os.LookupEnv(k); ok {
			preExisting[k] = v
		}
		if err := os.Unsetenv(k); err != nil {
			t.Fatalf("unsetenv %s: %v", k, err)
		}
	}
	for k, v := range kv {
		t.Setenv(k, v)
	}
	return func() {
		for k := range preExisting {
			if err := os.Setenv(k, preExisting[k]); err != nil {
				t.Fatalf("restore %s: %v", k, err)
			}
		}
	}
}

// TestResolve_Defaults confirms the precedence chain falls through
// to documented defaults when no env var is set. We do not assert
// the exact Home value because it depends on the runner's HOME;
// only that it is non-empty and uses the DefaultHomeSubdir suffix.
func TestResolve_Defaults(t *testing.T) {
	defer withCleanEnv(t, nil)()
	r, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !strings.HasSuffix(r.Home, DefaultHomeSubdir) {
		t.Errorf("Home = %q; does not end with %q", r.Home, DefaultHomeSubdir)
	}
	if r.Profile != DefaultProfile {
		t.Errorf("Profile = %q, want %q", r.Profile, DefaultProfile)
	}
	if r.LogLevel != DefaultLogLevel {
		t.Errorf("LogLevel = %q, want %q", r.LogLevel, DefaultLogLevel)
	}
	if r.HL != DefaultHL {
		t.Errorf("HL = %q, want %q", r.HL, DefaultHL)
	}
	if r.BaseURL != wire.HostPersonal {
		t.Errorf("BaseURL = %q, want %q", r.BaseURL, wire.HostPersonal)
	}
	if r.RPCOverrides != nil {
		t.Errorf("RPCOverrides = %v, want nil", r.RPCOverrides)
	}
}

// TestResolve_EnvOverridesAll confirms each env var wins over its
// default. The table-driven form matches the env-override table in
// docs/13-operations.md.
func TestResolve_EnvOverridesAll(t *testing.T) {
	defer withCleanEnv(t, nil)()
	customHome := t.TempDir()
	defer withCleanEnv(t, map[string]string{
		EnvHome:         customHome,
		EnvProfile:      "alice",
		EnvLogLevel:     "DEBUG",
		EnvHL:           "fr",
		EnvBaseURL:      string(wire.HostPersonalLegacy),
		EnvBL:           "boq_labs-tailwind-orchestration_20260101.42.0_p3",
		EnvRPCOverrides: `{"MethodListNotebooks":"OVERRIDE_ID"}`,
	})()
	r, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got, _ := filepath.Abs(customHome); r.Home != got {
		t.Errorf("Home = %q, want %q", r.Home, got)
	}
	if r.Profile != "alice" {
		t.Errorf("Profile = %q, want alice", r.Profile)
	}
	if r.LogLevel != "DEBUG" {
		t.Errorf("LogLevel = %q, want DEBUG", r.LogLevel)
	}
	if r.HL != "fr" {
		t.Errorf("HL = %q, want fr", r.HL)
	}
	if r.BaseURL != wire.HostPersonalLegacy {
		t.Errorf("BaseURL = %q, want %q", r.BaseURL, wire.HostPersonalLegacy)
	}
	if r.BuildLabel != "boq_labs-tailwind-orchestration_20260101.42.0_p3" {
		t.Errorf("BuildLabel = %q", r.BuildLabel)
	}
	if r.RPCOverrides["MethodListNotebooks"] != "OVERRIDE_ID" {
		t.Errorf("RPCOverrides[MethodListNotebooks] = %q, want OVERRIDE_ID",
			r.RPCOverrides["MethodListNotebooks"])
	}
}

// TestResolve_InvalidLogLevel confirms a non-canonical log level
// is a startup-blocking error.
func TestResolve_InvalidLogLevel(t *testing.T) {
	defer withCleanEnv(t, nil)()
	defer withCleanEnv(t, map[string]string{
		EnvLogLevel: "VERBOSE",
	})()
	_, err := Resolve()
	if err == nil {
		t.Fatalf("Resolve accepted non-canonical log level")
	}
	if !errors.Is(err, ErrInvalidLogLevel) {
		t.Errorf("err = %v, want ErrInvalidLogLevel", err)
	}
}

// TestResolve_InvalidHL confirms a malformed HL is rejected.
func TestResolve_InvalidHL(t *testing.T) {
	defer withCleanEnv(t, nil)()
	for _, bad := range []string{"EN", "english", "e", "12"} {
		t.Run(bad, func(t *testing.T) {
			defer withCleanEnv(t, nil)()
			defer withCleanEnv(t, map[string]string{EnvHL: bad})()
			_, err := Resolve()
			if err == nil {
				t.Fatalf("Resolve accepted HL=%q", bad)
			}
			if !errors.Is(err, ErrInvalidHL) {
				t.Errorf("err = %v, want ErrInvalidHL", err)
			}
		})
	}
}

// TestResolve_OffAllowlistURL confirms a base URL outside the
// three-host allowlist is a startup-blocking error.
func TestResolve_OffAllowlistURL(t *testing.T) {
	defer withCleanEnv(t, nil)()
	defer withCleanEnv(t, map[string]string{
		EnvBaseURL: "https://evil.example.com",
	})()
	_, err := Resolve()
	if err == nil {
		t.Fatalf("Resolve accepted off-allowlist URL")
	}
	if !errors.Is(err, ErrInvalidBaseURL) {
		t.Errorf("err = %v, want ErrInvalidBaseURL", err)
	}
}

// TestResolve_BlankEnvTreatedAsUnset confirms the precedence rule:
// empty / whitespace-only env values count as unset.
func TestResolve_BlankEnvTreatedAsUnset(t *testing.T) {
	defer withCleanEnv(t, nil)()
	defer withCleanEnv(t, map[string]string{
		EnvHome:     "   ",
		EnvProfile:  "",
		EnvLogLevel: "\t",
		EnvHL:       " ",
		EnvBaseURL:  "",
		EnvBL:       "",
	})()
	r, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.LogLevel != DefaultLogLevel {
		t.Errorf("LogLevel = %q, want %q", r.LogLevel, DefaultLogLevel)
	}
	if r.HL != DefaultHL {
		t.Errorf("HL = %q, want %q", r.HL, DefaultHL)
	}
	if r.BaseURL != wire.HostPersonal {
		t.Errorf("BaseURL = %q, want %q", r.BaseURL, wire.HostPersonal)
	}
}

// TestResolve_RPCOverrides_KVForm confirms the Name=id pair form
// is parsed when the value does not look like a JSON object.
func TestResolve_RPCOverrides_KVForm(t *testing.T) {
	defer withCleanEnv(t, nil)()
	defer withCleanEnv(t, map[string]string{
		EnvRPCOverrides: "MethodListNotebooks=OVERRIDE_X,MethodAddSource=OVERRIDE_Y",
	})()
	r, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(r.RPCOverrides) != 2 {
		t.Fatalf("RPCOverrides len = %d, want 2: %v", len(r.RPCOverrides), r.RPCOverrides)
	}
	if r.RPCOverrides["MethodListNotebooks"] != "OVERRIDE_X" {
		t.Errorf("RPCOverrides[MethodListNotebooks] = %q", r.RPCOverrides["MethodListNotebooks"])
	}
	if r.RPCOverrides["MethodAddSource"] != "OVERRIDE_Y" {
		t.Errorf("RPCOverrides[MethodAddSource] = %q", r.RPCOverrides["MethodAddSource"])
	}
}

// TestResolve_BuildLabelFallsBackToDefault confirms a non-canonical
// override surfaces as a warning and falls back to DEFAULT_BL.
func TestResolve_BuildLabelFallsBackToDefault(t *testing.T) {
	defer withCleanEnv(t, nil)()
	defer withCleanEnv(t, map[string]string{
		EnvBL: "this-is-not-a-build-label",
	})()
	r, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.BuildLabel != DEFAULT_BL {
		t.Errorf("BuildLabel = %q, want %q", r.BuildLabel, DEFAULT_BL)
	}
	if !strings.Contains(r.BuildLabelWarning, "did not parse") {
		t.Errorf("BuildLabelWarning = %q", r.BuildLabelWarning)
	}
}

// TestResolve_BuildLabelStaleOverride confirms an older override
// surfaces as BuildLabelStale=true.
func TestResolve_BuildLabelStaleOverride(t *testing.T) {
	defer withCleanEnv(t, nil)()
	// Build-time injection is "newer" than the override.
	prevBL := buildinfo.BuildLabel
	buildinfo.BuildLabel = "boq_labs-tailwind-orchestration_20990101.99.0_p9"
	defer func() { buildinfo.BuildLabel = prevBL }()
	defer withCleanEnv(t, map[string]string{
		EnvBL: "boq_labs-tailwind-orchestration_20260101.42.0_p3",
	})()
	r, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !r.BuildLabelStale {
		t.Errorf("BuildLabelStale = false, want true (override is older than build-time)")
	}
	if !strings.Contains(r.BuildLabelWarning, "older") {
		t.Errorf("BuildLabelWarning = %q", r.BuildLabelWarning)
	}
}

// TestParseBuildLabel_AcceptsCanonicalShape confirms the regex
// accepts the canonical shape.
func TestParseBuildLabel_AcceptsCanonicalShape(t *testing.T) {
	cases := []string{
		"boq_labs-tailwind-orchestration_20260101.42.0_p3",
		"boq_labs-tailwind-orchestration_20991231.99999.0_p0",
		"boq_labs-tailwind-orchestration_20000101.0.0_p0",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			parts, err := ParseBuildLabel(in)
			if err != nil {
				t.Fatalf("ParseBuildLabel(%q) = %v, want nil", in, err)
			}
			if parts.Raw != in {
				t.Errorf("Raw = %q, want %q", parts.Raw, in)
			}
			if parts.Channel < 0 {
				t.Errorf("Channel = %d, want >= 0", parts.Channel)
			}
		})
	}
}

// TestParseBuildLabel_RejectsNonCanonical confirms the regex
// rejects non-canonical inputs.
func TestParseBuildLabel_RejectsNonCanonical(t *testing.T) {
	for _, bad := range []string{
		"",
		"boq_OTHER-app_20260101.42.0_p3",
		"boq_labs-tailwind-orchestration_2026010.42.0_p3", // 7-digit date
		"boq_labs-tailwind-orchestration_20260101.42.0_pX",
		"prefix-boq_labs-tailwind-orchestration_20260101.42.0_p3",
	} {
		t.Run(bad, func(t *testing.T) {
			_, err := ParseBuildLabel(bad)
			if err == nil {
				t.Fatalf("ParseBuildLabel(%q) accepted, want error", bad)
			}
			if !errors.Is(err, ErrBuildLabelShape) {
				t.Errorf("err = %v, want ErrBuildLabelShape", err)
			}
		})
	}
}

// TestIsBuildLabelStale_Comparison exercises the (Date, Revision,
// Channel) ordering.
func TestIsBuildLabelStale_Comparison(t *testing.T) {
	prevBL := buildinfo.BuildLabel
	defer func() { buildinfo.BuildLabel = prevBL }()

	buildinfo.BuildLabel = "boq_labs-tailwind-orchestration_20260101.42.0_p3"

	// Older date.
	if stale, err := IsBuildLabelStale(
		"boq_labs-tailwind-orchestration_20251231.99.0_p9"); err != nil || !stale {
		t.Errorf("older-date: stale=%v err=%v, want stale=true err=nil", stale, err)
	}
	// Same date, newer revision (so NOT stale).
	if stale, err := IsBuildLabelStale(
		"boq_labs-tailwind-orchestration_20260101.99.0_p0"); err != nil || stale {
		t.Errorf("same-date-newer-rev: stale=%v err=%v, want stale=false err=nil", stale, err)
	}
	// Same date, same revision, newer channel (so NOT stale).
	if stale, err := IsBuildLabelStale(
		"boq_labs-tailwind-orchestration_20260101.42.0_p9"); err != nil || stale {
		t.Errorf("same-rev-newer-channel: stale=%v err=%v, want stale=false err=nil", stale, err)
	}
	// Newer date.
	if stale, err := IsBuildLabelStale(
		"boq_labs-tailwind-orchestration_20270101.0.0_p0"); err != nil || stale {
		t.Errorf("newer-date: stale=%v err=%v, want stale=false err=nil", stale, err)
	}
}

// TestIsBuildLabelStale_NoBuildLabel covers the "build-time
// injection is unknown" branch: there is nothing to compare against,
// so the supplied override is never stale.
func TestIsBuildLabelStale_NoBuildLabel(t *testing.T) {
	prevBL := buildinfo.BuildLabel
	defer func() { buildinfo.BuildLabel = prevBL }()
	buildinfo.BuildLabel = "unknown"

	stale, err := IsBuildLabelStale(
		"boq_labs-tailwind-orchestration_20260101.42.0_p3")
	if err != nil || stale {
		t.Errorf("stale=%v err=%v, want stale=false err=nil when build-time BL is unknown", stale, err)
	}
}

// TestIsBuildLabelStale_MalformedInput covers the parse-failure
// branch: a non-canonical supplied value returns (true, error).
func TestIsBuildLabelStale_MalformedInput(t *testing.T) {
	stale, err := IsBuildLabelStale("not a build label")
	if err == nil {
		t.Fatalf("IsBuildLabelStale accepted non-canonical input")
	}
	if !stale {
		t.Errorf("stale = false, want true on parse failure")
	}
}

// TestBuildLabelError_Message confirms the error message includes
// the raw input and a reason string.
func TestBuildLabelError_Message(t *testing.T) {
	_, err := ParseBuildLabel("garbage")
	if err == nil {
		t.Fatalf("ParseBuildLabel accepted garbage")
	}
	var ble *BuildLabelError
	if !errors.As(err, &ble) {
		t.Fatalf("err = %T, want *BuildLabelError", err)
	}
	if ble.Raw != "garbage" {
		t.Errorf("Raw = %q, want garbage", ble.Raw)
	}
	if ble.Reason == "" {
		t.Errorf("Reason is empty")
	}
	if !strings.Contains(ble.Error(), "garbage") {
		t.Errorf("Error() = %q; does not name the raw input", ble.Error())
	}
}
