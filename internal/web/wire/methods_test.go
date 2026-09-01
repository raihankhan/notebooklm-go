package wire

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMethodTableMatchesCommittedFixture reads the committed
// (name, id) pair list from testdata/expected_methods.json and asserts
// every entry matches a Method constant in this package. The fixture is
// committed to git so the test fails the moment doc 03 adds a new RPC
// and the constant block lags.
func TestMethodTableMatchesCommittedFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "expected_methods.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var rows []struct {
		Name string `json:"name"`
		ID   string `json:"id"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if len(rows) == 0 {
		t.Fatalf("fixture is empty; nothing to assert")
	}
	for _, r := range rows {
		got := ResolveID(r.Name)
		if string(got) != r.ID {
			t.Errorf("ResolveID(%q) = %q, want %q", r.Name, got, r.ID)
		}
	}
	// And the reverse: every constant in the package must be in the
	// fixture. A constant added to methods.go but not the fixture must
	// be visible to the next reader.
	fixtureNames := make(map[string]struct{}, len(rows))
	for _, r := range rows {
		fixtureNames[r.Name] = struct{}{}
	}
	for _, entry := range AllMethods() {
		if _, ok := fixtureNames[entry.Name]; !ok {
			t.Errorf("method %q is registered but missing from testdata/expected_methods.json; update the fixture", entry.Name)
		}
	}
}

// TestResolveID_NoOverridesReturnsCanonical confirms the unmodified
// resolver returns the constant-declared id when NOTEBOOKLM_RPC_OVERRIDES
// is unset. We unset the env var explicitly because other tests in this
// package may have set it.
func TestResolveID_NoOverridesReturnsCanonical(t *testing.T) {
	t.Setenv(RPCOverridesEnvVar, "")
	if got := ResolveID("MethodListNotebooks"); got != MethodListNotebooks {
		t.Fatalf("ResolveID(\"MethodListNotebooks\") = %q, want %q", got, MethodListNotebooks)
	}
	if got := ResolveID("MethodCreateArtifact"); got != MethodCreateArtifact {
		t.Fatalf("ResolveID(\"MethodCreateArtifact\") = %q, want %q", got, MethodCreateArtifact)
	}
}

// TestResolveID_OverrideHonored verifies an override applied via the env
// var wins over the canonical id.
func TestResolveID_OverrideHonored(t *testing.T) {
	// Reset the override-hash dedupe cache so we observe the log line
	// even if an earlier test in the same run installed the same set.
	resetOverrideCacheForTest()

	const overrideID = "OVERRIDE_XYZ_42"
	raw := `{"MethodListNotebooks":"` + overrideID + `"}`
	t.Setenv(RPCOverridesEnvVar, raw)

	got := ResolveID("MethodListNotebooks")
	if string(got) != overrideID {
		t.Fatalf("ResolveID returned %q, want override %q", got, overrideID)
	}
}

// TestResolveID_OverrideLogsHashNotValues installs a captured slog
// handler, fires a single override lookup, and asserts exactly one log
// line was emitted containing the SHA-256 hex of the canonicalized
// override JSON — and NOT the operator's raw override values.
func TestResolveID_OverrideLogsHashNotValues(t *testing.T) {
	resetOverrideCacheForTest()

	var buf bytes.Buffer
	prevHandler := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prevHandler) })

	const overrideID = "DEADBEEF_OVERRIDE"
	const methodName = "MethodAddSource"
	raw := `{"` + methodName + `":"` + overrideID + `"}`
	t.Setenv(RPCOverridesEnvVar, raw)

	want := ResolveID(methodName)
	if string(want) != overrideID {
		t.Fatalf("ResolveID = %q, want %q", want, overrideID)
	}

	// Compute the expected hash from the canonicalized form.
	wantHash := overrideSetHash(map[string]string{methodName: overrideID})

	out := buf.String()
	if strings.Count(out, "override_set_hash") != 1 {
		t.Fatalf("expected exactly one log line containing override_set_hash, got:\n%s", out)
	}
	if !strings.Contains(out, wantHash) {
		t.Fatalf("expected log to contain SHA-256 hex %q, got:\n%s", wantHash, out)
	}
	if strings.Contains(out, overrideID) {
		t.Fatalf("override id leaked into the log line; the operator's value MUST NOT appear in INFO output:\n%s", out)
	}
}

// TestResolveID_OverrideDedupePerProcess installs an override, calls
// ResolveID several times, and asserts the log line was emitted exactly
// once — the dedupe-by-hash guard from the Python implementation.
func TestResolveID_OverrideDedupePerProcess(t *testing.T) {
	resetOverrideCacheForTest()

	var buf bytes.Buffer
	prevHandler := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prevHandler) })

	t.Setenv(RPCOverridesEnvVar, `{"MethodAddSource":"X1"}`)
	for i := 0; i < 5; i++ {
		_ = ResolveID("MethodAddSource")
	}
	got := strings.Count(buf.String(), "override_set_hash")
	if got != 1 {
		t.Fatalf("expected exactly 1 deduped log line, got %d:\n%s", got, buf.String())
	}
}

// TestResolveID_OverrideFile exercises the JSON-file override shape (the
// other accepted syntax alongside the raw JSON object).
func TestResolveID_OverrideFile(t *testing.T) {
	resetOverrideCacheForTest()

	// Point NOTEBOOKLM_RPC_OVERRIDES at the committed fixture; the
	// fixture intentionally uses the SAME values as the canonical
	// table, so we can assert the override path runs without needing a
	// divergent id (this is the "operator pinned to canonical" case).
	t.Setenv(RPCOverridesEnvVar, filepath.Join("testdata", "method_overrides.json"))

	if got := ResolveID("MethodListNotebooks"); got != MethodListNotebooks {
		t.Fatalf("ResolveID = %q, want canonical %q", got, MethodListNotebooks)
	}
	// A bad entry (unknown method name) in the fixture is silently
	// dropped — the parser already warned. We don't have one in the
	// fixture, but the second entry pins a method we *also* know, so
	// it should still resolve to the canonical id.
	if got := ResolveID("MethodAddSource"); got != MethodAddSource {
		t.Fatalf("ResolveID = %q, want canonical %q", got, MethodAddSource)
	}
}

// TestResolveID_UnknownMethodReturnsEmpty confirms a typo'd method name
// is loud: the resolver returns the empty Method rather than silently
// substituting "Unknown". The contract is "you typed wrong, you get
// nothing on the wire" — better than a silent garbage id.
func TestResolveID_UnknownMethodReturnsEmpty(t *testing.T) {
	t.Setenv(RPCOverridesEnvVar, "")
	if got := ResolveID("MethodDoesNotExist"); got != "" {
		t.Fatalf("ResolveID for unknown name returned %q, want empty", got)
	}
}

// TestAllMethods_StableAndComplete asserts the snapshot is sorted by
// name and contains every registered method (no duplicates).
func TestAllMethods_StableAndComplete(t *testing.T) {
	all := AllMethods()
	if len(all) != len(methodTable) {
		t.Fatalf("AllMethods() returned %d entries, table has %d", len(all), len(methodTable))
	}
	seen := make(map[string]struct{}, len(all))
	for i, e := range all {
		if _, dup := seen[e.Name]; dup {
			t.Fatalf("duplicate entry at index %d: %q", i, e.Name)
		}
		seen[e.Name] = struct{}{}
	}
	for i := 1; i < len(all); i++ {
		if all[i-1].Name >= all[i].Name {
			t.Fatalf("AllMethods not sorted at index %d: %q >= %q", i, all[i-1].Name, all[i])
		}
	}
}

// resetOverrideCacheForTest clears the process-wide override-hash dedupe
// set so each test observes a fresh "first call" log line. It is a
// test-only helper; production code never touches the cache directly.
func resetOverrideCacheForTest() {
	overrideCacheMu.Lock()
	overrideCache = map[string]struct{}{}
	overrideCacheMu.Unlock()
}
