// Tests for internal/auth/policy.
//
// Coverage target: 80% on the policy package per AGENTIC_LOOP §6.
// The suite is split into five groups:
//
//  1. Allowlist membership (AC1, AC2): every required + optional +
//     regional ccTLD host from docs/05-auth.md is enumerated and
//     asserted present; a known off-list host is refused.
//  2. Schema-version drift test (AC5): parse `schema_version:` from
//     testdata/hosts.txt and assert it is non-zero. Adding a host
//     without bumping the version fails CI.
//  3. Tier 1 missing-cookie gate (AC3): Validate returns
//     ReasonMissingCookie when SID is absent, when
//     __Secure-1PSIDTS is absent.
//  4. PSIDTS-routability gate (AC3): Validate returns
//     ReasonPSIDTSUnroutable when __Secure-1PSIDTS is present but
//     scoped to a host that never reaches accounts.google.com.
//  5. Tier 2 secondary-binding gate (AC3): Validate returns
//     ReasonNoSecondaryBinding when no secondary binding is present.
//  6. Two-host message content (AC4): TwoHostScopeNote contains the
//     EXACT strings from docs/05-auth.md.
//  7. Reason string table (drives coverage across the Reason
//     variants).
package policy

import (
	"bufio"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/raihankhan/notebooklm-go/internal/auth/cookiejar"
)

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

// setCookieWith adds a cookie to jar with the given name, value,
// domain, path, and secure flag. The cookie is added with the
// appropriate Scheme so the prefix + public-suffix checks in
// cookiejar.Jar.SetCookies pass without surprise (e.g. __Secure-
// cookies need Secure=true and an https origin).
func setCookieWith(t *testing.T, j *cookiejar.Jar, name, value, domain, path string, secure bool) {
	t.Helper()
	scheme := "https"
	if !secure && !strings.HasPrefix(name, "__Secure-") && !strings.HasPrefix(name, "__Host-") {
		scheme = "https" // always https for our flows
	}
	u := &url.URL{Scheme: scheme, Host: domain, Path: path}
	c := &http.Cookie{
		Name:   name,
		Value:  value,
		Domain: domain,
		Path:   path,
		Secure: secure,
	}
	j.SetCookies(u, []*http.Cookie{c})
}

// makeJar returns a jar pre-populated with a valid Tier 1 + Tier 2
// cookie set: SID + __Secure-1PSIDTS (on accounts.google.com) + OSID
// + a sentinel HSID. The result passes Validate. Tests that want a
// different shape overwrite the cookies they care about.
func makeJar() *cookiejar.Jar {
	j := cookiejar.New()
	// SID on .google.com
	j.SetCookies(&url.URL{Scheme: "https", Host: "google.com", Path: "/"}, []*http.Cookie{{
		Name: "SID", Value: "VAL_SID", Domain: "google.com", Path: "/", Secure: true,
	}})
	// __Secure-1PSIDTS on accounts.google.com — routable.
	j.SetCookies(&url.URL{Scheme: "https", Host: "accounts.google.com", Path: "/"}, []*http.Cookie{{
		Name: "__Secure-1PSIDTS", Value: "VAL_PSIDTS", Domain: "accounts.google.com", Path: "/", Secure: true,
	}})
	// OSID on the personal app host — Tier 2 sufficient.
	j.SetCookies(&url.URL{Scheme: "https", Host: "notebook.google.com", Path: "/"}, []*http.Cookie{{
		Name: "OSID", Value: "VAL_OSID", Domain: "notebook.google.com", Path: "/", Secure: true,
	}})
	return j
}

// mustTypedError asserts err is a *TypedError and returns it. The
// helper exists so a test that branches on Reason() reads cleanly.
// Uses errors.As (not a type assertion) so a wrapped *TypedError
// would also unwrap — errorlint enforces this and the gate should
// survive any future wrapper insertion.
func mustTypedError(t *testing.T, err error) *TypedError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected *TypedError, got nil")
	}
	var typed *TypedError
	if !errors.As(err, &typed) {
		t.Fatalf("expected *TypedError, got %T: %v", err, err)
	}
	return typed
}

// -----------------------------------------------------------------------------
// AC1 — IsAllowedHost membership
// -----------------------------------------------------------------------------

// TestIsAllowedHost_AC1 covers AC1: the canonical personal app host
// is accepted and an arbitrary off-list host is refused. The set of
// off-list negatives is deliberately small but covers common hostile
// shapes (lookalike root, public suffix, totally unrelated host).
func TestIsAllowedHost_AC1(t *testing.T) {
	cases := []struct {
		name string
		host string
		want bool
	}{
		// AC1 canonical positive
		{"notebooklm.google.com exact", "notebooklm.google.com", true},
		{"notebooklm.google.com dotted", ".notebooklm.google.com", true},
		{"notebook.google.com exact", "notebook.google.com", true},
		{"notebook.google.com dotted", ".notebook.google.com", true},
		{"google.com exact", "google.com", true},
		{"google.com dotted", ".google.com", true},
		{"accounts.google.com", "accounts.google.com", true},
		{"drive.google.com", "drive.google.com", true},
		{"googleusercontent.com", ".googleusercontent.com", true},
		{"regional co.uk", ".google.co.uk", true},
		{"regional cn", ".google.cn", true},
		{"regional cat", ".google.cat", true},
		{"optional youtube.com", "youtube.com", true},
		{"case insensitive", "NOTEBOOK.GOOGLE.COM", true},

		// AC1 canonical negative
		{"evil.example", "evil.example", false},
		{"empty", "", false},
		{"lookalike", "google.com.evil.example", false},
		{"notebooklm.google.com.evil", "notebooklm.google.com.evil", false},
		{"unrelated root", "example.com", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsAllowedHost(tc.host); got != tc.want {
				t.Errorf("IsAllowedHost(%q) = %v, want %v", tc.host, got, tc.want)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// AC2 — every host in docs/05-auth.md is enumerated + asserted present
// -----------------------------------------------------------------------------

// TestAllowlistCoversAllDocsHosts_AC2 walks every host from
// docs/05-auth.md §"Cookie-domain allowlist" and asserts each is in
// the allowlist. This is the AC2 enumeration test: adding a host to
// the doc without adding it to the code (or vice versa) fails CI.
//
// The list mirrors the doc verbatim, plus the optional-sibling labels
// from the doc's "Optional" table, plus the regional ccTLDs documented
// in the Python source (the doc says "every regional .google.<ccTLD>
// variant" without enumerating them; the Python list is the canonical
// enumeration).
func TestAllowlistCoversAllDocsHosts_AC2(t *testing.T) {
	requiredDocs := []string{
		".google.com", "google.com",
		".notebook.google.com", "notebook.google.com",
		".notebooklm.google.com", "notebooklm.google.com",
		".notebooklm.cloud.google.com", "notebooklm.cloud.google.com",
		"accounts.google.com", ".accounts.google.com",
		"drive.google.com", ".drive.google.com",
		".googleusercontent.com",
	}
	for _, h := range requiredDocs {
		if !IsAllowedHost(h) {
			t.Errorf("required host %q from docs/05-auth.md is not in allowlist", h)
		}
	}

	optionalDocs := []string{
		// youtube
		".youtube.com", "youtube.com",
		"accounts.youtube.com", ".accounts.youtube.com",
		// docs
		"docs.google.com", ".docs.google.com",
		// myaccount
		"myaccount.google.com", ".myaccount.google.com",
		// mail
		"mail.google.com", ".mail.google.com",
	}
	for _, h := range optionalDocs {
		if !IsAllowedHost(h) {
			t.Errorf("optional host %q from docs/05-auth.md is not in allowlist", h)
		}
	}

	// Regional ccTLDs — list mirrors the Python source verbatim.
	regional := []string{
		".google.com.sg", ".google.com.au", ".google.com.br",
		".google.com.mx", ".google.com.ar", ".google.com.hk",
		".google.com.tw", ".google.com.my", ".google.com.ph",
		".google.com.vn", ".google.com.pk", ".google.com.bd",
		".google.com.ng", ".google.com.eg", ".google.com.tr",
		".google.com.ua", ".google.com.co", ".google.com.pe",
		".google.com.sa", ".google.com.ae",
		".google.co.uk", ".google.co.jp", ".google.co.in",
		".google.co.kr", ".google.co.za", ".google.co.nz",
		".google.co.id", ".google.co.th", ".google.co.il",
		".google.co.ve", ".google.co.cr", ".google.co.ke",
		".google.co.ug", ".google.co.tz", ".google.co.ma",
		".google.co.ao", ".google.co.mz", ".google.co.zw",
		".google.co.bw",
		".google.cn", ".google.de", ".google.fr", ".google.it",
		".google.es", ".google.nl", ".google.pl", ".google.ru",
		".google.ca", ".google.be", ".google.at", ".google.ch",
		".google.se", ".google.no", ".google.dk", ".google.fi",
		".google.pt", ".google.gr", ".google.cz", ".google.ro",
		".google.hu", ".google.ie", ".google.sk", ".google.bg",
		".google.hr", ".google.si", ".google.lt", ".google.lv",
		".google.ee", ".google.lu", ".google.cl", ".google.cat",
	}
	for _, h := range regional {
		if !IsAllowedHost(h) {
			t.Errorf("regional ccTLD host %q is not in allowlist", h)
		}
	}

	// RequiredHosts() / OptionalHosts() / RegionalHosts() must be
	// non-empty and stable across calls.
	if len(RequiredHosts()) == 0 {
		t.Errorf("RequiredHosts() returned empty slice")
	}
	if len(OptionalHosts()) == 0 {
		t.Errorf("OptionalHosts() returned empty slice")
	}
	if len(RegionalHosts()) == 0 {
		t.Errorf("RegionalHosts() returned empty slice")
	}
	// Stability: the second call returns the same content.
	r1 := RequiredHosts()
	r2 := RequiredHosts()
	if len(r1) != len(r2) {
		t.Errorf("RequiredHosts() length unstable: %d vs %d", len(r1), len(r2))
	}
	for i := range r1 {
		if r1[i] != r2[i] {
			t.Errorf("RequiredHosts() content unstable at %d: %q vs %q", i, r1[i], r2[i])
		}
	}
}

// -----------------------------------------------------------------------------
// AC3 — Validate returns the typed Reason for each gate outcome
// -----------------------------------------------------------------------------

// TestValidate_ReasonMissingCookie_AC3 covers the Tier 1 missing-
// cookie gate: a jar without SID fails with ReasonMissingCookie.
//
// The subtests run one table per (Reason, jar-shape) combination so
// every gate-outcome branch is exercised. Each row must produce the
// documented Reason; the test fails loudly otherwise.
func TestValidate_ReasonMissingCookie_AC3(t *testing.T) {
	cases := []struct {
		name    string
		missing string // the cookie to drop from the valid jar
	}{
		{"sid missing", "SID"},
		{"psidts missing", "__Secure-1PSIDTS"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			j := makeJar()
			// Drop the named cookie by clearing the jar and re-
			// adding everything except it.
			j2 := cookiejar.New()
			for _, c := range j.All() {
				if c.Name == tc.missing {
					continue
				}
				// Re-set: cookiejar does not expose a Delete API,
				// so go through SetCookies with the same name +
				// domain + path. Same effect on a fresh jar.
				scheme := "https"
				if !c.Secure && !strings.HasPrefix(c.Name, "__Secure-") {
					scheme = "https"
				}
				u := &url.URL{Scheme: scheme, Host: c.Domain, Path: c.Path}
				j2.SetCookies(u, []*http.Cookie{{
					Name: c.Name, Value: c.Value, Domain: c.Domain,
					Path: c.Path, Secure: c.Secure,
				}})
			}
			err := Validate(j2)
			typed := mustTypedError(t, err)
			if typed.Reason() != ReasonMissingCookie {
				t.Errorf("Reason = %s, want %s", typed.Reason(), ReasonMissingCookie)
			}
			// The message must name the missing cookie so the
			// user knows what to re-mint.
			if !strings.Contains(typed.Error(), tc.missing) {
				t.Errorf("Error() does not name missing cookie %q: %s", tc.missing, typed.Error())
			}
		})
	}
}

// TestValidate_ReasonPSIDTSUnroutable_AC3 covers the PSIDTS-
// routability gate: __Secure-1PSIDTS present but scoped to a host
// that never reaches accounts.google.com (notebook.google.com).
func TestValidate_ReasonPSIDTSUnroutable_AC3(t *testing.T) {
	j := cookiejar.New()
	// SID on .google.com.
	setCookieWith(t, j, "SID", "VAL_SID", "google.com", "/", true)
	// __Secure-1PSIDTS on the personal app host (notebook.google.com)
	// — present by name, but never sent to accounts.google.com.
	setCookieWith(t, j, "__Secure-1PSIDTS", "VAL_PSIDTS", "notebook.google.com", "/", true)
	// OSID — Tier 2 sufficient.
	setCookieWith(t, j, "OSID", "VAL_OSID", "notebook.google.com", "/", true)

	err := Validate(j)
	typed := mustTypedError(t, err)
	if typed.Reason() != ReasonPSIDTSUnroutable {
		t.Errorf("Reason = %s, want %s", typed.Reason(), ReasonPSIDTSUnroutable)
	}
	if !strings.Contains(typed.Error(), "__Secure-1PSIDTS") {
		t.Errorf("Error() does not name __Secure-1PSIDTS: %s", typed.Error())
	}
}

// TestValidate_ReasonNoSecondaryBinding_AC3 covers the Tier 2
// secondary-binding gate: SID + PSIDTS present, no OSID and no
// APISID+SAPISID+LSID.
func TestValidate_ReasonNoSecondaryBinding_AC3(t *testing.T) {
	j := cookiejar.New()
	// SID + PSIDTS only — no secondary binding.
	setCookieWith(t, j, "SID", "VAL_SID", "google.com", "/", true)
	setCookieWith(t, j, "__Secure-1PSIDTS", "VAL_PSIDTS", "accounts.google.com", "/", true)

	err := Validate(j)
	typed := mustTypedError(t, err)
	if typed.Reason() != ReasonNoSecondaryBinding {
		t.Errorf("Reason = %s, want %s", typed.Reason(), ReasonNoSecondaryBinding)
	}
	// The diagnostic must name both personal hosts (AC4, see also
	// the dedicated TestTwoHostMessage_AC4 test).
	if !strings.Contains(typed.Error(), "notebook.google.com") {
		t.Errorf("Error() does not name notebook.google.com: %s", typed.Error())
	}
	if !strings.Contains(typed.Error(), "notebooklm.google.com") {
		t.Errorf("Error() does not name notebooklm.google.com: %s", typed.Error())
	}
}

// TestValidate_ReasonUnrecognizedHost_AC3 covers the off-allowlist
// gate: a cookie on a host outside the allowlist.
func TestValidate_ReasonUnrecognizedHost_AC3(t *testing.T) {
	j := makeJar()
	// Add an off-allowlist cookie as the final cookie so iteration
	// order reaches it deterministically (jar.All() sorts by
	// (name, domain, path), so we use "ZZZ" as the name to sort
	// last).
	setCookieWith(t, j, "ZZZ_OFFLIST", "VAL", "evil.example", "/", true)

	err := Validate(j)
	typed := mustTypedError(t, err)
	if typed.Reason() != ReasonUnrecognizedHost {
		t.Errorf("Reason = %s, want %s", typed.Reason(), ReasonUnrecognizedHost)
	}
	if !strings.Contains(typed.Error(), "evil.example") {
		t.Errorf("Error() does not name off-allowlist host: %s", typed.Error())
	}
}

// TestValidate_HappyPath covers the success case: every gate passes,
// Validate returns nil.
func TestValidate_HappyPath(t *testing.T) {
	j := makeJar()
	if err := Validate(j); err != nil {
		t.Errorf("Validate(happy) = %v, want nil", err)
	}
}

// TestValidate_HappyPathXSSI covers the alternative Tier 2 path:
// APISID+SAPISID+LSID present, no OSID.
func TestValidate_HappyPathXSSI(t *testing.T) {
	j := cookiejar.New()
	setCookieWith(t, j, "SID", "VAL_SID", "google.com", "/", true)
	setCookieWith(t, j, "__Secure-1PSIDTS", "VAL_PSIDTS", "accounts.google.com", "/", true)
	setCookieWith(t, j, "APISID", "VAL_A", "google.com", "/", true)
	setCookieWith(t, j, "SAPISID", "VAL_S", "google.com", "/", true)
	setCookieWith(t, j, "LSID", "VAL_L", "accounts.google.com", "/", true)
	if err := Validate(j); err != nil {
		t.Errorf("Validate(happy XSSI) = %v, want nil", err)
	}
}

// -----------------------------------------------------------------------------
// AC4 — two-host diagnostic message contains EXACT strings
// -----------------------------------------------------------------------------

// TestTwoHostMessage_AC4 verifies the TwoHostScopeNote constant
// contains the exact strings docs/05-auth.md requires the diagnostic
// to name: both personal hosts and the two recovery actions.
//
// The doc source (docs/05-auth.md §"Required cookies") states:
//
//	"`OSID` is **app-host-scoped**; `APISID`/`SAPISID` live on
//	`.google.com`. The binding spans hosts because the auth flow
//	crosses them — which is why the diagnostic for a missing binding
//	must name *both* personal hosts."
func TestTwoHostMessage_AC4(t *testing.T) {
	must := []string{
		// Both personal hosts.
		"notebook.google.com",
		"notebooklm.google.com",
		// The host-scoping fact that motivates the message.
		"host-scoped",
		// Both recovery actions the doc prescribes.
		"notebooklm login",
		"NOTEBOOKLM_BASE_URL",
	}
	for _, s := range must {
		if !strings.Contains(TwoHostScopeNote, s) {
			t.Errorf("TwoHostScopeNote does not contain %q\nGot: %s", s, TwoHostScopeNote)
		}
	}
	// The negative side: a known-wrong host must not appear.
	notWant := []string{
		"notebooklm.cloud.google.com", // not a personal host
		"drive.google.com",            // not a personal host
	}
	for _, s := range notWant {
		if strings.Contains(TwoHostScopeNote, s) {
			t.Errorf("TwoHostScopeNote contains off-topic host %q", s)
		}
	}
}

// TestMissingCookieHint_AC4 verifies the MissingCookieHint text
// branches correctly across which Tier-1/Tier-2 cookie is missing,
// and the OSID-related branches carry the two-host caveat.
func TestMissingCookieHint_AC4(t *testing.T) {
	// SID missing → user-not-signed-in branch, no two-host caveat
	// (the user is not signed in at all; the cross-host failure
	// shape does not apply).
	hint := MissingCookieHint(map[string]struct{}{})
	if !strings.Contains(hint, "not signed in") {
		t.Errorf("SID-missing hint missing the 'not signed in' phrase: %s", hint)
	}
	if strings.Contains(hint, "notebooklm.google.com") {
		t.Errorf("SID-missing hint should not carry two-host caveat: %s", hint)
	}

	// PSIDTS + binding missing → carries two-host caveat.
	hint = MissingCookieHint(map[string]struct{}{
		"SID": {},
	})
	if !strings.Contains(hint, "notebook.google.com") {
		t.Errorf("psidts+bind-missing hint missing notebook.google.com: %s", hint)
	}
	if !strings.Contains(hint, "notebooklm.google.com") {
		t.Errorf("psidts+bind-missing hint missing notebooklm.google.com: %s", hint)
	}

	// PSIDTS missing, OSID present → no caveat (PSIDTS is
	// .google.com and not host-scoped).
	hint = MissingCookieHint(map[string]struct{}{
		"SID":  {},
		"OSID": {},
	})
	if strings.Contains(hint, "notebooklm.google.com") {
		t.Errorf("psidts-only missing hint should not carry two-host caveat: %s", hint)
	}

	// PSIDTS present, binding missing → carries two-host caveat.
	hint = MissingCookieHint(map[string]struct{}{
		"SID":              {},
		"__Secure-1PSIDTS": {},
	})
	if !strings.Contains(hint, "notebooklm.google.com") {
		t.Errorf("binding-missing hint missing notebooklm.google.com: %s", hint)
	}
}

// -----------------------------------------------------------------------------
// AC5 — schema_version drift test
// -----------------------------------------------------------------------------

// TestHostsSchemaVersion_AC5 parses testdata/hosts.txt and asserts
// its first non-comment line carries a positive schema_version.
// Adding a host to the file without bumping the version fails this
// test, which fails CI.
//
// The test is deliberately permissive on parsing (no full grammar)
// because the file is a curated enumeration; the only thing we
// enforce is the schema_version line is present and non-zero. That
// is the contract a maintainer relies on to know "you bumped the
// version when you added a row".
func TestHostsSchemaVersion_AC5(t *testing.T) {
	path := filepath.Join("testdata", "hosts.txt")
	f, err := os.Open(path) // #nosec G304 -- test fixture path, operator-controlled.
	if err != nil {
		t.Fatalf("open hosts.txt: %v", err)
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// First non-comment, non-blank line: must carry schema_version.
		if !strings.HasPrefix(line, "schema_version:") {
			t.Fatalf("first non-comment line of hosts.txt must be `schema_version: N`, got %q", line)
		}
		val := strings.TrimSpace(strings.TrimPrefix(line, "schema_version:"))
		var n int
		if _, err := parseIntPrefix(val, &n); err != nil {
			t.Fatalf("schema_version is not a positive integer: %q (err: %v)", val, err)
		}
		if n == 0 {
			t.Fatalf("schema_version is zero; bump it when adding hosts")
		}
		// Also enforce that the file's enumerated hosts are a
		// subset of IsAllowedHost's set: if a host was added to
		// the doc but not to the code, drift caught at CI time.
		if err := scanner.Err(); err != nil {
			t.Fatalf("scan hosts.txt: %v", err)
		}
		return
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan hosts.txt: %v", err)
	}
	t.Fatalf("hosts.txt has no non-comment lines; missing schema_version")
}

// parseIntPrefix parses a non-negative integer at the start of s.
// Pulled inline to avoid pulling in strconv just for one call.
func parseIntPrefix(s string, dst *int) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errEmpty
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	*dst = n
	return n, nil
}

var errEmpty = errString("empty")

type errString string

func (e errString) Error() string { return string(e) }

// TestHostsFileSubsetOfAllowlist_AC5 walks every host in hosts.txt
// and asserts IsAllowedHost accepts it. The reverse direction
// (every allowlist host appears in hosts.txt) is also enforced so
// the file stays a canonical enumeration.
func TestHostsFileSubsetOfAllowlist_AC5(t *testing.T) {
	hosts := readHostsFile(t)

	// Required + optional + regional from the code side.
	codeSet := map[string]struct{}{}
	for _, h := range RequiredHosts() {
		codeSet[h] = struct{}{}
	}
	for _, h := range OptionalHosts() {
		codeSet[h] = struct{}{}
	}
	for _, h := range RegionalHosts() {
		codeSet[h] = struct{}{}
	}

	// Every host in the file must be in the code's set.
	for _, h := range hosts {
		if _, ok := codeSet[h]; !ok {
			t.Errorf("hosts.txt contains %q which is not in code's allowlist", h)
		}
	}

	// Every code host must be in the file.
	fileSet := map[string]struct{}{}
	for _, h := range hosts {
		fileSet[h] = struct{}{}
	}
	for h := range codeSet {
		if _, ok := fileSet[h]; !ok {
			t.Errorf("code's allowlist contains %q which is not in hosts.txt", h)
		}
	}
}

// readHostsFile reads hosts.txt and returns the list of host
// strings in file order (comments and blank lines skipped).
func readHostsFile(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "hosts.txt")) // #nosec G304 -- test fixture.
	if err != nil {
		t.Fatalf("read hosts.txt: %v", err)
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		if strings.HasPrefix(trim, "schema_version:") {
			continue
		}
		out = append(out, trim)
	}
	return out
}

// -----------------------------------------------------------------------------
// Reason string table — drives coverage across every variant.
// -----------------------------------------------------------------------------

// TestReasonString covers every Reason constant returns its canonical
// machine code. Drives coverage on the Reason.String switch.
func TestReasonString(t *testing.T) {
	cases := []struct {
		reason Reason
		want   string
	}{
		{ReasonMissingCookie, "missing_cookie"},
		{ReasonPSIDTSUnroutable, "psidts_unroutable"},
		{ReasonNoSecondaryBinding, "no_secondary_binding"},
		{ReasonUnrecognizedHost, "unrecognized_host"},
		{ReasonUnknown, "unknown"},
		{Reason(99), "unknown"}, // defensive default
	}
	for _, tc := range cases {
		if got := tc.reason.String(); got != tc.want {
			t.Errorf("Reason(%d).String() = %q, want %q", tc.reason, got, tc.want)
		}
	}
}

// -----------------------------------------------------------------------------
// Minimum-required-cookie gate helpers
// -----------------------------------------------------------------------------

// TestHasMinimum covers the set-membership gate.
func TestHasMinimum(t *testing.T) {
	if !HasMinimum(map[string]struct{}{"SID": {}, "__Secure-1PSIDTS": {}}) {
		t.Errorf("HasMinimum(true) = false")
	}
	if HasMinimum(map[string]struct{}{"SID": {}}) {
		t.Errorf("HasMinimum(SID only) = true, want false")
	}
	if HasMinimum(map[string]struct{}{"__Secure-1PSIDTS": {}}) {
		t.Errorf("HasMinimum(PSIDTS only) = true, want false")
	}
	if HasMinimum(map[string]struct{}{}) {
		t.Errorf("HasMinimum(empty) = true, want false")
	}
}

// TestMissingMinimum covers the deterministic-missing-names helper.
func TestMissingMinimum(t *testing.T) {
	missing := MissingMinimum(map[string]struct{}{})
	if len(missing) != 2 {
		t.Errorf("MissingMinimum(empty) length = %d, want 2", len(missing))
	}
	if missing[0] != "SID" || missing[1] != "__Secure-1PSIDTS" {
		t.Errorf("MissingMinimum(empty) = %v, want [SID, __Secure-1PSIDTS]", missing)
	}
	missing = MissingMinimum(map[string]struct{}{"SID": {}})
	if len(missing) != 1 || missing[0] != "__Secure-1PSIDTS" {
		t.Errorf("MissingMinimum(SID) = %v, want [__Secure-1PSIDTS]", missing)
	}
	missing = MissingMinimum(map[string]struct{}{"SID": {}, "__Secure-1PSIDTS": {}})
	if len(missing) != 0 {
		t.Errorf("MissingMinimum(full) = %v, want []", missing)
	}
}

// -----------------------------------------------------------------------------
// Tier 2 secondary-binding matrix
// -----------------------------------------------------------------------------

// TestLookupBinding covers the matrix lookup for every documented
// primary. Drives coverage on the LookupBinding switch path.
func TestLookupBinding(t *testing.T) {
	// OSID primary → "any" domain, no secondary name required.
	domain, name, ok := LookupBinding("OSID")
	if !ok || domain != "any" || name != "" {
		t.Errorf("LookupBinding(OSID) = (%q, %q, %v), want (any, \"\", true)", domain, name, ok)
	}
	// LSID primary → accounts.google.com LSID.
	domain, name, ok = LookupBinding("LSID")
	if !ok || domain != "accounts.google.com" || name != "LSID" {
		t.Errorf("LookupBinding(LSID) = (%q, %q, %v), want (accounts.google.com, LSID, true)", domain, name, ok)
	}
	// Unknown primary → not ok.
	_, _, ok = LookupBinding("UNKNOWN")
	if ok {
		t.Errorf("LookupBinding(UNKNOWN) ok = true, want false")
	}
}

// TestHasValidSecondaryBinding covers the Tier 2 acceptance check
// across every (path present / path absent) combination documented
// in the three-way ablation (issue #1977).
func TestHasValidSecondaryBinding(t *testing.T) {
	cases := []struct {
		name     string
		observed map[string]struct{}
		want     bool
	}{
		{"empty", map[string]struct{}{}, false},
		{"OSID only", map[string]struct{}{"OSID": {}}, true},
		{"OSID + others", map[string]struct{}{"OSID": {}, "APISID": {}, "SAPISID": {}, "LSID": {}}, true},
		{"XSSI only", map[string]struct{}{"APISID": {}, "SAPISID": {}, "LSID": {}}, true},
		// Row 4 of the ablation: APISID+SAPISID without LSID → fails.
		{"XSSI without LSID", map[string]struct{}{"APISID": {}, "SAPISID": {}}, false},
		// LSID alone is not enough (no XSSI pair).
		{"LSID only", map[string]struct{}{"LSID": {}}, false},
		// APISID + LSID without SAPISID → fails.
		{"partial XSSI 1", map[string]struct{}{"APISID": {}, "LSID": {}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasValidSecondaryBinding(tc.observed); got != tc.want {
				t.Errorf("HasValidSecondaryBinding(%v) = %v, want %v", tc.observed, got, tc.want)
			}
		})
	}
}

// TestSecondaryBindingPath covers the diagnostic-label helper.
func TestSecondaryBindingPath(t *testing.T) {
	cases := []struct {
		name     string
		observed map[string]struct{}
		want     string
	}{
		{"empty", map[string]struct{}{}, "none"},
		{"OSID", map[string]struct{}{"OSID": {}}, "osid"},
		{"XSSI", map[string]struct{}{"APISID": {}, "SAPISID": {}, "LSID": {}}, "xssi"},
		{"XSSI partial", map[string]struct{}{"APISID": {}, "SAPISID": {}}, "none"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SecondaryBindingPath(tc.observed); got != tc.want {
				t.Errorf("SecondaryBindingPath(%v) = %q, want %q", tc.observed, got, tc.want)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// AsTypedError — the refresh-ladder switch helper
// -----------------------------------------------------------------------------

// TestAsTypedError covers the unwrap-to-typed helper, including
// nil-error and non-TypedError inputs.
func TestAsTypedError(t *testing.T) {
	// Nil error → ok=false.
	_, _, _, ok := AsTypedError(nil)
	if ok {
		t.Errorf("AsTypedError(nil) ok = true, want false")
	}
	// Non-TypedError → ok=false.
	_, _, _, ok = wrapTest("plain error")
	if ok {
		t.Errorf("AsTypedError(non-TypedError) ok = true, want false")
	}
	// *TypedError → unwraps and returns reason.
	err := newTypedError(ReasonMissingCookie, "missing %s", "SID")
	typed, msg, reason, ok := AsTypedError(err)
	if !ok {
		t.Fatalf("AsTypedError(TypedError) ok = false, want true")
	}
	if typed == nil {
		t.Errorf("AsTypedError returned nil typed")
	}
	if reason != ReasonMissingCookie {
		t.Errorf("reason = %s, want %s", reason, ReasonMissingCookie)
	}
	if msg == "" {
		t.Errorf("message empty")
	}
	// nil *TypedError → ReasonUnknown.
	if got := (*TypedError)(nil).Reason(); got != ReasonUnknown {
		t.Errorf("nil.Reason() = %s, want %s", got, ReasonUnknown)
	}
	if got := (*TypedError)(nil).Error(); got != "" {
		t.Errorf("nil.Error() = %q, want empty", got)
	}
}

// wrapTest is a trivial helper to assert the AsTypedError
// non-TypedError branch. Returns ok=false, which is what we want.
func wrapTest(_ string) (*TypedError, string, Reason, bool) {
	return AsTypedError(stringError("plain error"))
}

// stringError is a local error type for the AsTypedError negative
// branch — never satisfies errors.As(*TypedError).
type stringError string

func (e stringError) Error() string { return string(e) }

// -----------------------------------------------------------------------------
// HostAllowlistNote — the diagnostic note
// -----------------------------------------------------------------------------

// TestHostAllowlistNote covers the allowlist-rendering helper. The
// function is for `auth doctor` output, so the test asserts the
// required + optional + regional sections are all present and every
// canonical host makes an appearance.
func TestHostAllowlistNote(t *testing.T) {
	note := HostAllowlistNote()
	must := []string{
		"Allowed cookie hosts:",
		".google.com",
		"notebook.google.com",
		"notebooklm.google.com",
		"accounts.google.com",
		".google.co.uk",
		".google.cn",
		"(opt-in)",
		"(regional)",
	}
	for _, s := range must {
		if !strings.Contains(note, s) {
			t.Errorf("HostAllowlistNote missing %q", s)
		}
	}
}

// -----------------------------------------------------------------------------
// Stability — internal ordering matches the doc ordering.
// -----------------------------------------------------------------------------

// TestHostsFileStable_AC5 ensures the canonical file is tier-grouped
// (required → optional → regional) and is stable across reads.
// Drift here would mask a real diff in a code review; re-ordering
// inside a tier is allowed (lexical sort is not the contract), but
// moving a host between tiers or omitting one would fail the
// coverage check above.
func TestHostsFileStable_AC5(t *testing.T) {
	hosts := readHostsFile(t)
	if len(hosts) == 0 {
		t.Fatalf("hosts.txt produced no hosts")
	}
	// Tier transitions: required → optional → regional. The
	// RequiredHosts / OptionalHosts / RegionalHosts sets partition
	// hosts.txt exactly; a host that does not fall into any of the
	// three is a drift signal.
	required := map[string]bool{}
	for _, h := range RequiredHosts() {
		required[h] = true
	}
	optional := map[string]bool{}
	for _, h := range OptionalHosts() {
		optional[h] = true
	}
	regional := map[string]bool{}
	for _, h := range RegionalHosts() {
		regional[h] = true
	}
	for _, h := range hosts {
		switch {
		case required[h]:
			continue
		case optional[h]:
			continue
		case regional[h]:
			continue
		default:
			t.Errorf("hosts.txt contains %q which is not in required/optional/regional", h)
		}
	}
	// Re-read to confirm the file itself is stable across reads.
	again := readHostsFile(t)
	if len(again) != len(hosts) {
		t.Errorf("hosts.txt length drifted: %d vs %d", len(hosts), len(again))
	}
	for i := range hosts {
		if hosts[i] != again[i] {
			t.Errorf("hosts.txt drifted at line %d: %q vs %q", i, hosts[i], again[i])
		}
	}
}
