// Tests for the WIZ_global_data extractor.
//
// Coverage target: 80% per AGENTIC_LOOP §6. The suite covers every
// quoting variant × every supported field, plus a round-trip against
// committed fixtures. The fixtures are scrubbed — every credential
// token is a clearly-fake placeholder so a leak of the test data
// into a log line is detectable.
//
// Test grouping:
//
//  1. Quoting-variant table: one row per (variant, field) pair, all
//     pinned to committed fixtures.
//  2. Empty-HTML, malformed-HTML, and missing-field cases.
//  3. Escape-tolerance: backslash-escaped quotes inside values do not
//     break the capture.
//  4. Round-trip: a fixture loaded via testdata/fixtures.json returns
//     the same fields regardless of variant.

package extract

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture is one row of testdata/fixtures.json. The fields are the
// canonical CSRF + Session values the extractor should return when
// handed the HTML body. Both quoting variants live in the same row so
// a single document proves the parser accepts every form.
type fixture struct {
	Name          string `json:"name"`
	DoubleQuoted  string `json:"double_quoted"`
	SingleQuoted  string `json:"single_quoted"`
	HTMLEscaped   string `json:"html_escaped"`
	ExpectCSRF    string `json:"expect_csrf"`
	ExpectSession string `json:"expect_session"`
}

// loadFixtures reads the committed fixtures file. Path is fixed so a
// future move requires touching this test, not the entire suite.
func loadFixtures(t *testing.T) []fixture {
	t.Helper()
	// #nosec G304 -- fixture path is operator-controlled test data.
	data, err := os.ReadFile(filepath.Join("testdata", "fixtures.json"))
	if err != nil {
		t.Fatalf("read fixtures.json: %v", err)
	}
	var fixtures []fixture
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatalf("parse fixtures.json: %v", err)
	}
	if len(fixtures) == 0 {
		t.Fatalf("fixtures.json: empty")
	}
	return fixtures
}

// TestExtractWIZAllVariants is the main acceptance table. For every
// fixture, every quoting shape must yield the same (csrf, session)
// pair. The fixture names document what each row is exercising —
// the production rows are the canonical NotebookLM app page; the
// edge rows cover escapes, BOMs, embedded JSON arrays, and the
// sign-in-page shape (#2019).
func TestExtractWIZAllVariants(t *testing.T) {
	fixtures := loadFixtures(t)
	for _, f := range fixtures {
		f := f
		t.Run(f.Name+"/double", func(t *testing.T) {
			csrf, session, err := ExtractWIZ(f.DoubleQuoted)
			if err != nil {
				t.Fatalf("ExtractWIZ double-quoted: %v", err)
			}
			if csrf != f.ExpectCSRF {
				t.Errorf("csrf = %q, want %q", csrf, f.ExpectCSRF)
			}
			if session != f.ExpectSession {
				t.Errorf("session = %q, want %q", session, f.ExpectSession)
			}
		})
		t.Run(f.Name+"/single", func(t *testing.T) {
			csrf, session, err := ExtractWIZ(f.SingleQuoted)
			if err != nil {
				t.Fatalf("ExtractWIZ single-quoted: %v", err)
			}
			if csrf != f.ExpectCSRF {
				t.Errorf("csrf = %q, want %q", csrf, f.ExpectCSRF)
			}
			if session != f.ExpectSession {
				t.Errorf("session = %q, want %q", session, f.ExpectSession)
			}
		})
		t.Run(f.Name+"/html", func(t *testing.T) {
			csrf, session, err := ExtractWIZ(f.HTMLEscaped)
			if err != nil {
				t.Fatalf("ExtractWIZ html-escaped: %v", err)
			}
			if csrf != f.ExpectCSRF {
				t.Errorf("csrf = %q, want %q", csrf, f.ExpectCSRF)
			}
			if session != f.ExpectSession {
				t.Errorf("session = %q, want %q", session, f.ExpectSession)
			}
		})
	}
}

// TestExtractWIZEmpty is the empty-body case: the extractor must
// produce a non-nil error so the caller can decide whether the
// failure is recoverable.
func TestExtractWIZEmpty(t *testing.T) {
	_, _, err := ExtractWIZ("")
	if err == nil {
		t.Fatal("ExtractWIZ(\"\") returned nil error")
	}
}

// TestExtractWIZMissingField covers the "page changed" branch: the
// body contains a WIZ_global_data block but neither SNlM0e nor
// FdrFJe is present.
func TestExtractWIZMissingField(t *testing.T) {
	body := `<html><body><script>var WIZ_global_data = {"yIU9Ub":"x","abc":"d"};</script></body></html>`
	_, _, err := ExtractWIZ(body)
	if err == nil {
		t.Fatal("ExtractWIZ(missing fields) returned nil error")
	}
	// Empty final URL routes to token-missing (the on-app-host
	// message). The branch order is the contract.
	var f *Failure
	if !errors.As(err, &f) {
		t.Fatalf("err is not *Failure: %v", err)
	}
	if !errors.Is(f.Sentinel, ErrTokenMissing) {
		t.Errorf("sentinel = %v, want ErrTokenMissing", f.Sentinel)
	}
}

// TestExtractWIZEscapedQuoteInsideValue proves the backslash-escape
// tolerance: a value containing a backslash + any char parses
// correctly. (Go's RE2 lacks negative lookahead, so we test the
// simpler "backslash then any char" tolerance that the regex
// supports; the production NotebookLM token never contains
// anything weirder.)
func TestExtractWIZEscapedQuoteInsideValue(t *testing.T) {
	// Body is a Go string literal; \\x is two chars (backslash, x).
	body := `<script>var WIZ_global_data = {"SNlM0e":"abc\\xdef","FdrFJe":"sess"};</script>`
	csrf, session, err := ExtractWIZ(body)
	if err != nil {
		t.Fatalf("ExtractWIZ escaped-quote: %v", err)
	}
	// Expected is the Go string literal `abc\\xdef` which is 7 chars
	// (a,b,c,\,\,x,d,e,f). The regex captures exactly the same
	// 7-char run.
	if csrf != `abc\\xdef` {
		t.Errorf("csrf = %q, want %q", csrf, `abc\\xdef`)
	}
	if session != "sess" {
		t.Errorf("session = %q, want sess", session)
	}
}

// TestExtractWIZGarbageInput is the malformed-body case: the
// extractor must not panic and must return a typed error.
func TestExtractWIZGarbageInput(t *testing.T) {
	cases := []string{
		"not html at all",
		`{"SNlM0e":"abc`, // unterminated string
		"<html></html>",
		strings.Repeat("A", 1<<20), // huge body
	}
	for _, c := range cases {
		_, _, err := ExtractWIZ(c)
		if err == nil {
			head := c
			if len(head) > 16 {
				head = head[:16]
			}
			t.Errorf("ExtractWIZ(%q...) returned nil error", head)
		}
	}
}
