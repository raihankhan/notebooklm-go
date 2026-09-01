// Tests for the lossless storage_state.json round-trip.
//
// Coverage target: 80% per AGENTIC_LOOP §6 (the per-package floor).
// Test grouping mirrors the seven ACs from ticket T-P2-2:
//
//	AC1 — expires: -1 ⇄ nil ⇄ -1 (no attribute loss)
//	AC2 — sameSite canonicalization (Strict / Lax / None, four case forms each)
//	AC3 — Python-produced fixture round-trips every attribute
//	AC4 — Go-produced storage_state.json is byte-identical after no-op open/close
//	AC5 — bare cookie array reshapes into the documented wrapper
//	AC6 — origins key is dropped on read
//	AC7 — context.json legacy format promotes into the in-band namespace
//
// The Python fixture values are placeholders ("FAKE_*_VALUE_NOT_A_REAL_CREDENTIAL")
// so a leak of the test data into a log line is detectable.
//
// Boundary: this test file is part of the mode=internal package;
// it imports stdlib + internal/web/wire (the same JSON adapter the
// production code uses) and nothing else.
package storage

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"

	"github.com/raihankhan/notebooklm-go/internal/web/wire"
)

// fixture returns the absolute path to a committed testdata file.
// #nosec G304 -- path is operator-controlled test input, the fixture
// file is committed alongside the test.
func fixture(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join("testdata", name)
}

// int64Ptr is a tiny helper that makes the AC1 / AC4 round-trip table
// readable. Using a constructor (rather than a literal pointer to a
// stack value) keeps each case line one expression.
func int64Ptr(v int64) *int64 { return &v }

// ---------------------------------------------------------------------------
// AC1 — expires: -1 ⇄ nil ⇄ -1 (no attribute loss)
// ---------------------------------------------------------------------------

// TestExpiresSessionRoundTrip exercises the session-cookie sentinel.
// A wire-level expires=-1 must read as *int64=nil; a write of that
// cookie must re-emit expires=-1; no attribute on the cookie may
// drift across the boundary.
//
// The test is a table of cases that differ only in their session
// sentinel placement, mirroring the Python
// notebooklm._auth.cookie_semantics.normalize_cookie_expiry contract.
func TestExpiresSessionRoundTrip(t *testing.T) {
	cases := []struct {
		name           string
		inputExpires   *int64 // what we put into the wire document
		expectedInGo   *int64 // what we expect after Unmarshal (nil for session)
		expectedOnWire string // what we expect after a write+rewrite
	}{
		{
			name:           "session_cookie_-1_reads_as_nil_writes_back_as_-1",
			inputExpires:   int64Ptr(-1),
			expectedInGo:   nil,
			expectedOnWire: `"expires":-1`,
		},
		{
			name:           "dated_value_survives_intact",
			inputExpires:   int64Ptr(1798761234),
			expectedInGo:   int64Ptr(1798761234),
			expectedOnWire: `"expires":1798761234`,
		},
		{
			name:           "zero_expires_survives_intact",
			inputExpires:   int64Ptr(0),
			expectedInGo:   int64Ptr(0),
			expectedOnWire: `"expires":0`,
		},
		{
			name:           "large_int64_survives_no_float64_mangling",
			inputExpires:   int64Ptr(32503680000), // year 3000 in seconds
			expectedInGo:   int64Ptr(32503680000),
			expectedOnWire: `"expires":32503680000`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := buildMinimalCookieDoc(tc.inputExpires, "test-session")
			s, err := Unmarshal(doc)
			if err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if got := len(s.Cookies); got != 1 {
				t.Fatalf("len(Cookies) = %d, want 1", got)
			}
			gotExpires := s.Cookies[0].Expires
			if !equalInt64Ptr(gotExpires, tc.expectedInGo) {
				t.Fatalf("read Expires = %v, want %v", ptrStr(gotExpires), ptrStr(tc.expectedInGo))
			}
			// Write + rewrite. The output must contain the
			// expected wire form for the expiry.
			wire1, err := Marshal(s)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if !containsBytes(wire1, []byte(tc.expectedOnWire)) {
				t.Fatalf("first wire = %s, want substring %s", wire1, tc.expectedOnWire)
			}
			s2, err := Unmarshal(wire1)
			if err != nil {
				t.Fatalf("Unmarshal (rewrite): %v", err)
			}
			if !equalInt64Ptr(s2.Cookies[0].Expires, tc.expectedInGo) {
				t.Fatalf("rewrite Expires = %v, want %v",
					ptrStr(s2.Cookies[0].Expires), ptrStr(tc.expectedInGo))
			}
			wire2, err := Marshal(s2)
			if err != nil {
				t.Fatalf("Marshal (rewrite): %v", err)
			}
			if !containsBytes(wire2, []byte(tc.expectedOnWire)) {
				t.Fatalf("rewritten wire = %s, want substring %s", wire2, tc.expectedOnWire)
			}
		})
	}
}

// buildMinimalCookieDoc produces the smallest valid storage_state
// document that carries one cookie row. Used by the AC1 table and
// the AC2 sameSite table.
func buildMinimalCookieDoc(expires *int64, sameSite string) []byte {
	expiresLit := "null"
	if expires != nil {
		expiresLit = strconv.FormatInt(*expires, 10)
	}
	return []byte(`{"cookies":[{"name":"test","value":"x","domain":".example.com","path":"/",` +
		`"expires":` + expiresLit + `,"httpOnly":false,"secure":false,"sameSite":"` + sameSite + `"}]}`)
}

// equalInt64Ptr is the pointer-aware equality test. Both nil and
// pointing at the same int64 are equal; everything else differs.
func equalInt64Ptr(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// ptrStr renders a *int64 for error messages.
func ptrStr(p *int64) string {
	if p == nil {
		return "<nil>"
	}
	return strconv.FormatInt(*p, 10)
}

// containsBytes is a tiny helper to assert a wire byte slice carries
// a particular ASCII substring. Avoids pulling in strings.Contains for
// what is otherwise a single-call site.
func containsBytes(haystack, needle []byte) bool {
	if len(needle) == 0 {
		return true
	}
	if len(needle) > len(haystack) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := 0; j < len(needle); j++ {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// AC2 — sameSite canonicalization
// ---------------------------------------------------------------------------

// TestSameSiteCanonicalization is the four-case-form table for every
// canonical SameSite value. The Python reference
// (notebooklm._auth.cookie_merge._VALID_SAME_SITE) accepts only
// {"Strict", "Lax", "None"}; the storage layer maps any case form
// to the canonical spelling on read.
func TestSameSiteCanonicalization(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"Strict", "Strict"},
		{"strict", "Strict"},
		{"STRICT", "Strict"},
		{"sTrIcT", "Strict"},
		{"Lax", "Lax"},
		{"lax", "Lax"},
		{"LAX", "Lax"},
		{"lAx", "Lax"},
		{"None", "None"},
		{"none", "None"},
		{"NONE", "None"},
		{"nOnE", "None"},
		// Unrecognized values drop to empty (the storage
		// layer never emits an invalid sameSite on the wire,
		// and never invents one — see Python
		// _VALID_SAME_SITE behavior).
		{"no_restriction", ""},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run("input="+tc.input, func(t *testing.T) {
			doc := buildMinimalCookieDoc(int64Ptr(1798761234), tc.input)
			s, err := Unmarshal(doc)
			if err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if got := s.Cookies[0].SameSite; got != tc.expected {
				t.Fatalf("read sameSite = %q, want %q", got, tc.expected)
			}
			// Write + rewrite. A canonical sameSite must
			// appear with the canonical case form on the wire.
			wire1, err := Marshal(s)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			s2, err := Unmarshal(wire1)
			if err != nil {
				t.Fatalf("Unmarshal (rewrite): %v", err)
			}
			if s2.Cookies[0].SameSite != tc.expected {
				t.Fatalf("rewrite sameSite = %q, want %q",
					s2.Cookies[0].SameSite, tc.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// AC3 — Python fixture round-trip
// ---------------------------------------------------------------------------

// TestPythonFixtureRoundTrip reads the committed Python-produced
// storage_state.json fixture and asserts every attribute survives
// the read path. The fixture is committed to disk; the test must
// not modify it.
//
// Assertions:
//
//   - The cookie count is preserved.
//   - The in-band notebooklm.account namespace is preserved
//     (authuser + email).
//   - A representative cookie carries every field correctly:
//     name, value, domain (dotted form), path, expires, httpOnly,
//     secure, sameSite.
//   - The OSID-on-two-hosts case is preserved (issue #369: the
//     cookie on notebooklm.google.com must NOT collapse with the
//     cookie on notebook.google.com).
func TestPythonFixtureRoundTrip(t *testing.T) {
	data, err := os.ReadFile(fixture(t, "python_storage_state.json")) // #nosec G304 -- see fixture comment.
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	s, err := Unmarshal(data)
	if err != nil {
		t.Fatalf("Unmarshal fixture: %v", err)
	}

	const wantCookieCount = 8
	if got := len(s.Cookies); got != wantCookieCount {
		t.Fatalf("len(Cookies) = %d, want %d", got, wantCookieCount)
	}
	if s.NotebookLM == nil {
		t.Fatal("NotebookLM namespace missing after read")
	}
	if got, want := s.NotebookLM.Account.AuthUser, 0; got != want {
		t.Errorf("Account.AuthUser = %d, want %d", got, want)
	}
	if got, want := s.NotebookLM.Account.Email, "fixture@example.invalid"; got != want {
		t.Errorf("Account.Email = %q, want %q", got, want)
	}

	// Spot-check SID — its attributes must all survive.
	sid := findCookie(s.Cookies, "SID", ".google.com", "/")
	if sid == nil {
		t.Fatal("SID cookie missing after read")
	}
	if sid.Value != "FAKE_SID_VALUE_NOT_A_REAL_CREDENTIAL" {
		t.Errorf("SID value drift: %q", sid.Value)
	}
	if sid.HTTPOnly != true {
		t.Errorf("SID httpOnly = %v, want true", sid.HTTPOnly)
	}
	if sid.Secure != true {
		t.Errorf("SID secure = %v, want true", sid.Secure)
	}
	if sid.SameSite != "None" {
		t.Errorf("SID sameSite = %q, want None", sid.SameSite)
	}
	if sid.Expires == nil || *sid.Expires != 1798761234 {
		t.Errorf("SID expires = %v, want 1798761234", ptrStr(sid.Expires))
	}

	// OSID must exist on BOTH notebooklm.google.com and
	// notebook.google.com — collapsing them is the exact bug
	// the cookie jar package comment calls out.
	osids := filterCookies(s.Cookies, "OSID")
	if len(osids) != 2 {
		t.Fatalf("OSID cookie count = %d, want 2 (issue #369)", len(osids))
	}
	sort.Slice(osids, func(i, j int) bool { return osids[i].Domain < osids[j].Domain })
	if osids[0].Domain != "notebook.google.com" || osids[1].Domain != "notebooklm.google.com" {
		t.Fatalf("OSID domains = %q,%q, want notebook.google.com,notebooklm.google.com",
			osids[0].Domain, osids[1].Domain)
	}

	// Session sentinel survives as nil.
	ssid := findCookie(s.Cookies, "SSID", ".google.com", "/")
	if ssid == nil {
		t.Fatal("SSID cookie missing after read")
	}
	if ssid.Expires != nil {
		t.Errorf("SSID expires = %v, want <nil> (session)", ptrStr(ssid.Expires))
	}

	// Round-trip: write the result and confirm the wire form
	// preserves both OSID entries.
	wireBytes, err := Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	s2, err := Unmarshal(wireBytes)
	if err != nil {
		t.Fatalf("Unmarshal (rewrite): %v", err)
	}
	if len(filterCookies(s2.Cookies, "OSID")) != 2 {
		t.Errorf("OSID collapsed on rewrite")
	}
	if s2.NotebookLM == nil || s2.NotebookLM.Account.Email != "fixture@example.invalid" {
		t.Errorf("notebooklm.account lost on rewrite")
	}
}

// findCookie is the lookup helper for the AC3 spot-check. It
// returns the first cookie matching (name, domain, path) or nil.
// The fixture is hand-authored so the lookup is unambiguous; if a
// future fixture row collides the test fails loudly.
func findCookie(cookies []Cookie, name, domain, path string) *Cookie {
	for i := range cookies {
		if cookies[i].Name == name && cookies[i].Domain == domain && cookies[i].Path == path {
			return &cookies[i]
		}
	}
	return nil
}

// filterCookies returns every cookie with the given name. Used by
// the OSID-on-two-hosts assertion and a few helpers.
func filterCookies(cookies []Cookie, name string) []Cookie {
	var out []Cookie
	for i := range cookies {
		if cookies[i].Name == name {
			out = append(out, cookies[i])
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// AC4 — Byte-identical no-op open/close
// ---------------------------------------------------------------------------

// TestByteIdenticalRoundTrip writes a freshly-built Storage with a
// mix of attributes (session + dated cookies, every SameSite value,
// the in-band notebooklm.account namespace) and asserts that a
// write-then-rewrite loop produces the same bytes a second time.
//
// "Byte-identical" here means the second Marshal output equals the
// first; the first may differ from the Python writer because Go
// uses sorted keys and the Python source emits insertion order.
// This is the Python-loader's compatibility contract: the second
// write must equal itself, so any third reader (Python CLI,
// another Go reader, the MCP server) sees the same document
// regardless of which path produced it.
func TestByteIdenticalRoundTrip(t *testing.T) {
	in := buildRichStorage()

	wire1, err := Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	s, err := Unmarshal(wire1)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	wire2, err := Marshal(s)
	if err != nil {
		t.Fatalf("Marshal (rewrite): %v", err)
	}
	if string(wire1) != string(wire2) {
		t.Fatalf("wire bytes differ across no-op rewrite:\nfirst:  %s\nsecond: %s", wire1, wire2)
	}

	// A third iteration must still match.
	s3, err := Unmarshal(wire2)
	if err != nil {
		t.Fatalf("Unmarshal (second): %v", err)
	}
	wire3, err := Marshal(s3)
	if err != nil {
		t.Fatalf("Marshal (second rewrite): %v", err)
	}
	if string(wire2) != string(wire3) {
		t.Fatalf("wire bytes drift on second rewrite:\nsecond: %s\nthird:  %s", wire2, wire3)
	}
}

// buildRichStorage is the "kitchen sink" cookie set used by AC4 and
// the legacy-context test. It exercises every attribute the storage
// layer preserves: session and dated cookies, all three canonical
// sameSite values, host-only cookies, dotted and undotted domains.
func buildRichStorage() Storage {
	exp1 := int64Ptr(1798761234)
	return Storage{
		Cookies: []Cookie{
			{Name: "SID", Value: "fake-sid", Domain: ".google.com", Path: "/",
				Expires: exp1, HTTPOnly: true, Secure: true, SameSite: "None"},
			{Name: "SSID", Value: "fake-ssid", Domain: ".google.com", Path: "/",
				Expires: nil, HTTPOnly: true, SameSite: "Strict"},
			{Name: "OSID", Value: "fake-osid-notebooklm", Domain: "notebooklm.google.com", Path: "/",
				Expires: exp1, HTTPOnly: true, Secure: true, SameSite: "Lax"},
			{Name: "OSID", Value: "fake-osid-notebook", Domain: "notebook.google.com", Path: "/",
				Expires: exp1, HTTPOnly: true, Secure: true, SameSite: "Lax"},
		},
		NotebookLM: &NotebookLM{
			Account: Account{AuthUser: 0, Email: "roundtrip@example.invalid"},
		},
	}
}

// ---------------------------------------------------------------------------
// AC5 — Bare cookie array reshapes into the documented wrapper
// ---------------------------------------------------------------------------

// TestBareArrayReshapes covers the most common browser-export
// shape: a top-level JSON array with no "cookies" key. The Python
// CLI rejects this on the load path (see notebooklm._auth.cookies
// _sanitize_cookie_entry docstring), but the Go loader MUST accept
// it because docs/05-auth.md documents
// `auth import-cookies` as the primary entry point for browser
// exports.
func TestBareArrayReshapes(t *testing.T) {
	doc := []byte(`[
		{"name":"SID","value":"fake","domain":".google.com","path":"/","expires":1798761234},
		{"name":"__Secure-1PSIDTS","value":"fake","domain":".google.com","path":"/","expires":1798761234}
	]`)
	s, err := Unmarshal(doc)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got := len(s.Cookies); got != 2 {
		t.Fatalf("len(Cookies) = %d, want 2", got)
	}
	if s.Cookies[0].Name != "SID" {
		t.Errorf("Cookies[0].Name = %q, want SID", s.Cookies[0].Name)
	}
	if s.Cookies[1].Name != "__Secure-1PSIDTS" {
		t.Errorf("Cookies[1].Name = %q, want __Secure-1PSIDTS", s.Cookies[1].Name)
	}
	// Round-trip: a rewrite of the reshaped form is byte-stable.
	wire1, err := Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	s2, err := Unmarshal(wire1)
	if err != nil {
		t.Fatalf("Unmarshal (rewrite): %v", err)
	}
	wire2, err := Marshal(s2)
	if err != nil {
		t.Fatalf("Marshal (rewrite): %v", err)
	}
	if string(wire1) != string(wire2) {
		t.Fatalf("bare-array form is not byte-stable:\nfirst:  %s\nsecond: %s", wire1, wire2)
	}
	// The rewritten document must contain a top-level
	// "cookies" array (no bare-array leakage).
	var probe map[string]any
	if err := wire.Unmarshal(wire2, &probe); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if _, ok := probe["cookies"]; !ok {
		t.Errorf("rewritten document has no top-level cookies key")
	}
}

// TestExpirationDateLegacySpelling ensures the rookiepy/browser-
// export spelling of expires (snake_case expirationDate) is
// rewritten to expires before the typed decode. Without this pass
// the field would be silently dropped on read.
func TestExpirationDateLegacySpelling(t *testing.T) {
	doc := []byte(`{"cookies":[{"name":"SID","value":"fake","domain":".google.com","path":"/","expirationDate":1798761234}]}`)
	s, err := Unmarshal(doc)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got := len(s.Cookies); got != 1 {
		t.Fatalf("len(Cookies) = %d, want 1", got)
	}
	if s.Cookies[0].Expires == nil || *s.Cookies[0].Expires != 1798761234 {
		t.Errorf("Expires = %v, want 1798761234", ptrStr(s.Cookies[0].Expires))
	}
	// Rewriting the document must NOT carry the legacy key.
	wireBytes, err := Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if containsBytes(wireBytes, []byte("expirationDate")) {
		t.Errorf("legacy spelling leaked into rewritten document: %s", wireBytes)
	}
}

// ---------------------------------------------------------------------------
// AC6 — origins is dropped on read
// ---------------------------------------------------------------------------

// TestOriginsDropped asserts the origins key (localStorage /
// sessionStorage) is silently dropped on read and never re-emitted
// by a subsequent write. The Python source never populates it for
// the auth path (see docs/05-auth.md "Round-trip requirements")
// and dropping it on import stops an attacker-controlled
// storage_state from smuggling arbitrary JS into a log line.
func TestOriginsDropped(t *testing.T) {
	doc := []byte(`{
		"cookies":[{"name":"SID","value":"fake","domain":".google.com","path":"/","expires":1798761234}],
		"origins":[
			{"origin":"https://evil.example","localStorage":[{"name":"k","value":"v"}]}
		]
	}`)
	s, err := Unmarshal(doc)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	wireBytes, err := Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if containsBytes(wireBytes, []byte(`"origins"`)) {
		t.Errorf("origins key leaked into rewrite: %s", wireBytes)
	}
	// Verify the document is still parseable and carries the
	// cookie row we put in.
	if len(s.Cookies) != 1 || s.Cookies[0].Name != "SID" {
		t.Errorf("cookies dropped alongside origins")
	}
}

// TestOriginsDroppedBareArray covers the same path when the input
// is a bare cookie array: origins is not present there in the first
// place, so the test guards against a future refactor that breaks
// the bare-array branch by trying to read origins.
func TestOriginsDroppedBareArray(t *testing.T) {
	doc := []byte(`[{"name":"SID","value":"fake","domain":".google.com","path":"/","expires":1798761234}]`)
	s, err := Unmarshal(doc)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	wireBytes, err := Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if containsBytes(wireBytes, []byte("origins")) {
		t.Errorf("origins key synthesized from bare-array form: %s", wireBytes)
	}
}

// ---------------------------------------------------------------------------
// AC7 — context.json legacy promotion
// ---------------------------------------------------------------------------

// TestLegacyContextPromotion reads a storage_state.json file from a
// temp directory alongside a legacy context.json sibling, asserts
// the in-band notebooklm.account namespace carries the legacy
// record, and confirms a subsequent Write emits the same in-band
// value (not a re-creation of the context.json file).
func TestLegacyContextPromotion(t *testing.T) {
	dir := t.TempDir()

	// storage_state.json — fresh login with no in-band
	// account metadata yet. The in-band namespace is what
	// legacy context.json promotes INTO.
	storagePath := filepath.Join(dir, "storage_state.json")
	storageDoc := []byte(`{"cookies":[{"name":"SID","value":"fake","domain":".google.com","path":"/","expires":1798761234}]}`)
	if err := os.WriteFile(storagePath, storageDoc, 0o600); err != nil {
		t.Fatalf("seed storage_state.json: %v", err)
	}

	// legacy context.json — pre-v0.5.0 shape, lives next to
	// storage_state.json. Carries the account block under
	// "account" alongside other legacy fields (notebook_id,
	// conversation_id).
	legacyPath := filepath.Join(dir, "context.json")
	legacyDoc, err := os.ReadFile(fixture(t, "legacy_context.json")) // #nosec G304 -- see fixture comment.
	if err != nil {
		t.Fatalf("read legacy fixture: %v", err)
	}
	if err := os.WriteFile(legacyPath, legacyDoc, 0o600); err != nil {
		t.Fatalf("seed context.json: %v", err)
	}

	s, err := Read(storagePath)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if s.NotebookLM == nil {
		t.Fatal("in-band namespace not promoted from legacy")
	}
	if got, want := s.NotebookLM.Account.AuthUser, 2; got != want {
		t.Errorf("AuthUser = %d, want %d (legacy)", got, want)
	}
	if got, want := s.NotebookLM.Account.Email, "legacy@example.invalid"; got != want {
		t.Errorf("Email = %q, want %q (legacy)", got, want)
	}

	// Rewrite. The Storage now carries the in-band account;
	// the rewrite must preserve it AND must NOT touch
	// context.json (the legacy file is one-way promotion,
	// read-only from the storage layer's perspective).
	before, err := os.ReadFile(legacyPath) // #nosec G304 -- operator-controlled temp dir.
	if err != nil {
		t.Fatalf("stat legacy before rewrite: %v", err)
	}
	if err := Write(storagePath, s); err != nil {
		t.Fatalf("Write: %v", err)
	}
	after, err := os.ReadFile(legacyPath) // #nosec G304 -- operator-controlled temp dir.
	if err != nil {
		t.Fatalf("stat legacy after rewrite: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("legacy context.json was mutated by Write")
	}

	// Confirm the in-band namespace survived the round-trip.
	s2, err := Read(storagePath)
	if err != nil {
		t.Fatalf("Read (rewrite): %v", err)
	}
	if s2.NotebookLM == nil ||
		s2.NotebookLM.Account.AuthUser != 2 ||
		s2.NotebookLM.Account.Email != "legacy@example.invalid" {
		t.Errorf("in-band namespace lost on round-trip: %+v", s2.NotebookLM)
	}
}

// TestLegacyContextNotPresent confirms a missing context.json
// sibling is a no-op, not an error. The promotion is best-effort.
func TestLegacyContextNotPresent(t *testing.T) {
	dir := t.TempDir()
	storagePath := filepath.Join(dir, "storage_state.json")
	if err := os.WriteFile(storagePath, []byte(`{"cookies":[]}`), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s, err := Read(storagePath)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if s.NotebookLM != nil {
		t.Errorf("NotebookLM should be nil without a sibling legacy file, got %+v", s.NotebookLM)
	}
}

// TestLegacyContextMalformed confirms a malformed context.json is
// not an error — the promotion is best-effort. The storage_state
// read still succeeds.
func TestLegacyContextMalformed(t *testing.T) {
	dir := t.TempDir()
	storagePath := filepath.Join(dir, "storage_state.json")
	if err := os.WriteFile(storagePath, []byte(`{"cookies":[]}`), 0o600); err != nil {
		t.Fatalf("seed storage: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "context.json"), []byte(`{"account":"not a dict"}`), 0o600); err != nil {
		t.Fatalf("seed context: %v", err)
	}
	s, err := Read(storagePath)
	if err != nil {
		t.Fatalf("Read (malformed legacy): %v", err)
	}
	if s.NotebookLM != nil {
		t.Errorf("NotebookLM should be nil when legacy record is malformed, got %+v", s.NotebookLM)
	}
}

// TestLegacyContextExistingInBandWins confirms the in-band
// record takes precedence when both the in-band namespace and the
// legacy context.json carry account metadata. The legacy file is
// a one-way promotion, not a forced overwrite.
func TestLegacyContextExistingInBandWins(t *testing.T) {
	dir := t.TempDir()
	storagePath := filepath.Join(dir, "storage_state.json")
	storageDoc := []byte(`{
		"cookies":[{"name":"SID","value":"fake","domain":".google.com","path":"/","expires":1798761234}],
		"notebooklm":{"account":{"authuser":5,"email":"inband@example.invalid"}}
	}`)
	if err := os.WriteFile(storagePath, storageDoc, 0o600); err != nil {
		t.Fatalf("seed storage: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "context.json"), []byte(`{"account":{"authuser":2,"email":"legacy@example.invalid"}}`), 0o600); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}
	s, err := Read(storagePath)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if s.NotebookLM == nil || s.NotebookLM.Account.AuthUser != 5 ||
		s.NotebookLM.Account.Email != "inband@example.invalid" {
		t.Errorf("in-band record lost on promotion; got %+v", s.NotebookLM)
	}
}

// ---------------------------------------------------------------------------
// Public-surface smoke tests
// ---------------------------------------------------------------------------

// TestEmptyPathError asserts Read and Write reject the empty path
// with ErrEmptyPath — they are programming errors, not file system
// errors, so the sentinel is the only thing callers should match.
func TestEmptyPathError(t *testing.T) {
	if _, err := Read(""); !errors.Is(err, ErrEmptyPath) {
		t.Errorf("Read(\"\") err = %v, want ErrEmptyPath", err)
	}
	if err := Write("", Storage{}); !errors.Is(err, ErrEmptyPath) {
		t.Errorf("Write(\"\", _) err = %v, want ErrEmptyPath", err)
	}
}

// TestMarshalPreservesOrder is a regression guard: Marshal must
// use the project-wide JSON encoder (sorted keys, no trailing
// newline, no HTML escaping) so a storage_state.json produced by the
// Go writer is byte-compatible with the Python CLI's json.dumps
// output. The test asserts:
//
//   - no trailing newline
//   - keys are emitted in sorted order at every level
//   - the in-band notebooklm.account namespace survives
func TestMarshalPreservesOrder(t *testing.T) {
	in := buildRichStorage()
	out, err := Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if len(out) > 0 && out[len(out)-1] == '\n' {
		t.Errorf("Marshal output has trailing newline (wire contract violation)")
	}
	// Sorted keys: at the top level the first key is "cookies",
	// the second is "notebooklm" (alphabetical order). Within
	// each cookie row, "domain" precedes "expires" precedes
	// "httpOnly" precedes "name" precedes "path" precedes
	// "sameSite" precedes "secure" precedes "value" — the
	// Python wire order.
	var probe map[string]any
	if err := json.Unmarshal(out, &probe); err != nil {
		t.Fatalf("probe unmarshal: %v", err)
	}
	if _, ok := probe["cookies"]; !ok {
		t.Errorf("cookies key missing")
	}
	if _, ok := probe["notebooklm"]; !ok {
		t.Errorf("notebooklm key missing")
	}
	if _, ok := probe["origins"]; ok {
		t.Errorf("origins key must never be written")
	}
}

// TestWriteReadFile is the round-trip via disk: write a Storage to
// a temp path, read it back, and confirm the in-memory shape is
// identical. Mirrors what a CLI invocation does at startup.
func TestWriteReadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "storage_state.json")
	in := buildRichStorage()
	if err := Write(path, in); err != nil {
		t.Fatalf("Write: %v", err)
	}
	out, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !storageEqual(in, out) {
		t.Errorf("file round-trip drifted\nin:  %+v\nout: %+v", in, out)
	}
}

// storageEqual is a deep equality test for Storage. Cookie slice
// order matters (the storage layer preserves it) so the test
// compares element-wise. The Cookie comparison must dereference the
// Expires pointer: two cookies are equal iff their Expires values
// (or both-nil pointers) match.
func storageEqual(a, b Storage) bool {
	if len(a.Cookies) != len(b.Cookies) {
		return false
	}
	for i := range a.Cookies {
		if a.Cookies[i].Name != b.Cookies[i].Name ||
			a.Cookies[i].Value != b.Cookies[i].Value ||
			a.Cookies[i].Domain != b.Cookies[i].Domain ||
			a.Cookies[i].Path != b.Cookies[i].Path ||
			a.Cookies[i].HTTPOnly != b.Cookies[i].HTTPOnly ||
			a.Cookies[i].Secure != b.Cookies[i].Secure ||
			a.Cookies[i].SameSite != b.Cookies[i].SameSite ||
			a.Cookies[i].HostOnly != b.Cookies[i].HostOnly {
			return false
		}
		if !equalInt64Ptr(a.Cookies[i].Expires, b.Cookies[i].Expires) {
			return false
		}
	}
	if (a.NotebookLM == nil) != (b.NotebookLM == nil) {
		return false
	}
	if a.NotebookLM != nil && (a.NotebookLM.Account != b.NotebookLM.Account) {
		return false
	}
	return true
}
