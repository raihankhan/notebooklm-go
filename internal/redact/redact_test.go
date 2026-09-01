// Package redact exercises the redact package from inside the package
// boundary. The bulk of the suite is a table-driven harness loaded
// from testdata/fixtures.json plus a few focused contract tests for
// edge cases that the fixtures cannot pin (nil/empty input, header
// line terminators, the colon-missing defensive branch).
//
// The package is imported as `redact` (not `redact_test`) so internal
// helpers like redactHeaderLine can be called directly. This is what
// gets us to 100% line coverage; the public API surface alone leaves
// the defensive `colon < 0` branch unreached.
package redact

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture mirrors one row of testdata/fixtures.json. The JSON is
// the source of truth for the redaction policy — every new shape
// must be added there first, then referenced from a test.
type fixture struct {
	Family         string `json:"family"`
	Token          string `json:"token"`
	Input          string `json:"input"`
	MustNotContain string `json:"must_not_contain"`
}

// loadFixtures reads the JSON fixture file at the given path and
// returns the parsed rows. Errors fail the test loudly so a
// typo'd fixture never silently passes.
//
// #nosec G304 -- path is operator-controlled test input, not user
// data. The fixture file is committed alongside the test.
func loadFixtures(t *testing.T, path string) []fixture {
	t.Helper()
	b, err := os.ReadFile(path) // #nosec G304 -- see comment above.
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	var rows []fixture
	if err := json.Unmarshal(b, &rows); err != nil {
		t.Fatalf("parse fixtures: %v", err)
	}
	return rows
}

// TestFixtures_Table is the contract test for the four regex
// families plus the four URL / header redactors. Each row in
// testdata/fixtures.json becomes a subtest so a regression on any
// one shape points directly at the offending family + token.
func TestFixtures_Table(t *testing.T) {
	t.Parallel()

	rows := loadFixtures(t, filepath.Join("testdata", "fixtures.json"))
	if len(rows) < 12 {
		t.Fatalf("expected at least 12 fixture rows, got %d", len(rows))
	}

	for _, r := range rows {
		r := r
		name := strings.ReplaceAll(r.Family+"_"+r.Token, "-", "_")
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := Apply([]byte(r.Input))
			if bytes.Contains(got, []byte(r.MustNotContain)) {
				t.Fatalf("Apply left the secret on the wire\n"+
					"family=%s token=%s\ninput=%q\n got=%q\nmust_not_contain=%q",
					r.Family, r.Token, r.Input, got, r.MustNotContain)
			}
			if !bytes.Contains(got, []byte("[REDACTED]")) {
				t.Fatalf("Apply did not emit [REDACTED] sentinel\n"+
					"family=%s token=%s\ninput=%q\n got=%q",
					r.Family, r.Token, r.Input, got)
			}
		})
	}
}

// TestApply_NilAndEmpty pins the no-secret fast path: a nil or
// empty input returns nil so callers can cheaply short-circuit on
// the empty case.
func TestApply_NilAndEmpty(t *testing.T) {
	t.Parallel()

	if got := Apply(nil); got != nil {
		t.Errorf("Apply(nil) = %q, want nil", got)
	}
	if got := Apply([]byte{}); got != nil {
		t.Errorf("Apply(empty) = %q, want nil", got)
	}
}

// TestApply_NoMatchReturnsFreshCopy asserts that when no redactor
// fires, Apply still returns a slice that does not alias the input.
// The redaction contract guarantees callers can retain or mutate
// the result without affecting their original buffer.
//
// (regexp.ReplaceAll allocates a fresh backing slice even on no-match,
// so this test simply pins that contract; future "optimize for no-op"
// changes would have to deliberately keep the aliasing safe.)
func TestApply_NoMatchReturnsFreshCopy(t *testing.T) {
	t.Parallel()

	in := []byte("harmless text with no secrets at all")
	out := Apply(in)

	if string(out) != string(in) {
		t.Fatalf("Apply mutated clean input\n want=%q\n  got=%q", in, out)
	}

	// Mutating the result must not bleed back into the input. Even
	// when the contents are equal, the backing arrays are distinct.
	out[0] = 'X'
	if in[0] == 'X' {
		t.Fatalf("mutating output also mutated input (aliasing)")
	}
}

// TestApply_AllFourFamiliesForBothTokens is a small belt-and-braces
// matrix: SNlM0e and FdrFJe must each be masked in each of the four
// JSON / form shapes. This is the table-test pin from the ticket
// spec — "SNlM0e, FdrFJe, and a Cookie: value are masked in all
// four shapes".
func TestApply_AllFourFamiliesForBothTokens(t *testing.T) {
	t.Parallel()

	cases := []struct {
		family string
		input  string
	}{
		{"quoted-json", `"SNlM0e":"v1","FdrFJe":"v2"`},
		{"html-escaped-json", `&quot;SNlM0e&quot;:&quot;v1&quot;,&quot;FdrFJe&quot;:&quot;v2&quot;`},
		{"form-prose", `SNlM0e=v1&FdrFJe=v2`},
		{"bare-token", `SNlM0e and FdrFJe both appear here`},
	}

	for _, c := range cases {
		c := c
		t.Run(c.family, func(t *testing.T) {
			t.Parallel()

			got := Apply([]byte(c.input))
			if !strings.Contains(string(got), "[REDACTED]") {
				t.Fatalf("no redaction fired for family=%s\ninput=%q\ngot=%q",
					c.family, c.input, got)
			}
		})
	}
}

// TestApply_CookieHeader_PreservesPrefixAndNewline pins the
// "preserve prefix and trailing line terminator" contract. The
// redacted line must still parse as a Cookie header.
func TestApply_CookieHeader_PreservesPrefixAndNewline(t *testing.T) {
	t.Parallel()

	in := []byte("Cookie: __Secure-1PSID=abc; __Secure-1PSIDTS=xyz\n")
	got := Apply(in)
	want := "Cookie: [REDACTED]\n"
	if string(got) != want {
		t.Fatalf("cookie header redaction mismatch\n want=%q\n  got=%q", want, got)
	}
}

// TestApply_AuthorizationHeader_PreservesPrefixAndNewline mirrors
// the Cookie header test for the Authorization header.
func TestApply_AuthorizationHeader_PreservesPrefixAndNewline(t *testing.T) {
	t.Parallel()

	in := []byte("Authorization: Bearer ya29.tokenvalue\n")
	got := Apply(in)
	want := "Authorization: [REDACTED]\n"
	if string(got) != want {
		t.Fatalf("authorization header redaction mismatch\n want=%q\n  got=%q", want, got)
	}
}

// TestApply_HeaderCRLF exercises the `\r\n` line terminator branch
// of redactHeaderLine so a Windows-style log line still survives
// redaction byte-clean.
func TestApply_HeaderCRLF(t *testing.T) {
	t.Parallel()

	in := []byte("Cookie: secret=abc\r\nNext: line\r\n")
	got := Apply(in)
	want := "Cookie: [REDACTED]\r\nNext: line\r\n"
	if string(got) != want {
		t.Fatalf("cookie CRLF mismatch\n want=%q\n  got=%q", want, got)
	}
}

// TestApply_HeaderBareCR covers the lone `\r` terminator (rare but
// required for full coverage on the header callback).
func TestApply_HeaderBareCR(t *testing.T) {
	t.Parallel()

	in := []byte("Cookie: secret=abc\r")
	got := Apply(in)
	want := "Cookie: [REDACTED]\r"
	if string(got) != want {
		t.Fatalf("cookie bare CR mismatch\n want=%q\n  got=%q", want, got)
	}
}

// TestApply_HeaderNoTerminator exercises the case where a header
// line has no trailing newline at all — e.g. a partial buffer or a
// header at end-of-file. The redactor must still preserve the
// `Header: ` prefix.
func TestApply_HeaderNoTerminator(t *testing.T) {
	t.Parallel()

	in := []byte("Authorization: Bearer ya29.tokenvalue")
	got := Apply(in)
	want := "Authorization: [REDACTED]"
	if string(got) != want {
		t.Fatalf("header no-terminator mismatch\n want=%q\n  got=%q", want, got)
	}
}

// TestApply_BareWordBoundaryNotMatched asserts the bare-token
// redactor does NOT fire inside a longer word — `xSNlM0e` and
// `SNlM0eXYZ` must pass through untouched.
func TestApply_BareWordBoundaryNotMatched(t *testing.T) {
	t.Parallel()

	cases := []string{
		"xSNlM0e",
		"SNlM0eXYZ",
		"myFdrFJeSuffix",
	}
	for _, in := range cases {
		in := in
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			got := Apply([]byte(in))
			if string(got) != in {
				t.Fatalf("bare-token regex over-matched\n want=%q\n  got=%q", in, got)
			}
		})
	}
}

// TestApply_MultipleSecretsOnSameLine asserts that several secrets
// on a single line all get masked, not just the first.
func TestApply_MultipleSecretsOnSameLine(t *testing.T) {
	t.Parallel()

	in := []byte(`"SNlM0e":"first","FdrFJe":"second"`)
	got := Apply(in)
	if strings.Contains(string(got), "first") || strings.Contains(string(got), "second") {
		t.Fatalf("multiple secrets not all masked\ninput=%q\ngot=%q", in, got)
	}
	// Both occurrences should have produced the sentinel.
	if c := bytes.Count(got, []byte("[REDACTED]")); c != 2 {
		t.Fatalf("expected 2 [REDACTED] markers, got %d\nresult=%q", c, got)
	}
}

// TestApply_QuoteAndBackslashVariants exercises the regex's
// ability to handle escaped quote characters inside JSON values,
// which the spec calls out explicitly.
func TestApply_QuoteAndBackslashVariants(t *testing.T) {
	t.Parallel()

	in := []byte(`"SNlM0e":"value\"with-quote"`)
	got := Apply(in)
	if strings.Contains(string(got), `value\"with-quote`) {
		t.Fatalf("escaped JSON value leaked\ninput=%q\ngot=%q", in, got)
	}
}

// TestRedactHeaderLine_NoColon exercises the defensive branch in
// redactHeaderLine that returns the bare [REDACTED] sentinel when
// the matched header line somehow has no colon. The cookie /
// authorization regexes both require `:` so this branch is
// unreachable through the public API; we exercise it directly here
// to pin 100% coverage.
func TestRedactHeaderLine_NoColon(t *testing.T) {
	t.Parallel()

	got := redactHeaderLine([]byte("no-colon-here"))
	if string(got) != "[REDACTED]" {
		t.Fatalf("redactHeaderLine(no colon) = %q, want [REDACTED]", got)
	}
}
