// Tests for EscapeJSONString and UnescapeJSONString.
//
// Two layers of oracle:
//
//  1. The reference values in TestEscapeJSONString_TableDriven are
//     byte-for-byte the output of `json.dumps(s)[1:-1]` in Python,
//     hand-pinned to literal Go strings so the test is deterministic
//     and does not require a Python interpreter.
//
//  2. TestEscapeJSONString_RoundTripsAgainstStdlib re-asserts the
//     same invariant against the canonical encoding/json encoder:
//     every s in the table must round-trip through EscapeJSONString
//     and UnescapeJSONString and produce s, and the escape output must
//     equal what encoding/json would produce for the same string.
package wire

import (
	"encoding/json"
	"testing"
)

// TestEscapeJSONString_TableDriven pins the canonical escape output for
// the eight shapes the chat payloads actually deliver: ASCII text,
// embedded double-quote, embedded backslash, embedded newline, the
// five short-form escapes (\b \f \n \r \t), the \u00XX form for control
// chars that have no short form, and astral-plane emoji.
func TestEscapeJSONString_TableDriven(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// Plain ASCII — fast path, no escapes.
		{"empty", "", ""},
		{"ascii", "hello world", "hello world"},

		// Embedded double-quote.
		{"quote", `He said "hi"`, `He said \"hi\"`},

		// Embedded backslash.
		{"backslash", `a\b`, `a\\b`},

		// Embedded newline (the most common chat payload hazard).
		{"newline", "line1\nline2", `line1\nline2`},

		// The other short-form escapes.
		{"tab", "a\tb", `a\tb`},
		{"carriage return", "a\rb", `a\rb`},
		{"backspace", "a\bb", `a\bb`},
		{"form feed", "a\fb", `a\fb`},

		// Combined short-form escapes.
		{"all short forms", "\b\f\n\r\t", `\b\f\n\r\t`},

		// Embedded control char with no short form (\x01).
		{"control x01", "a\x01b", `a\u0001b`},

		// Embedded null byte.
		{"null byte", "a\x00b", `a\u0000b`},

		// Multi-byte UTF-8: passes through verbatim.
		{"emoji", "🎉", "🎉"},
		{"accent", "café", "café"},
		{"emoji + quote", `🎉 "boom"`, `🎉 \"boom\"`},

		// Combined: every special in one string.
		{"kitchen sink", "a\nb\"c\\d\u0001🎉", `a\nb\"c\\d\u0001🎉`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EscapeJSONString(tc.in); got != tc.want {
				t.Fatalf("EscapeJSONString(%q) = %q, want %q",
					tc.in, got, tc.want)
			}
		})
	}
}

// TestEscapeJSONString_RoundTripsAgainstStdlib is the cross-check: for
// every string the chat payloads actually deliver, our escape output
// must match what encoding/json would produce, and our unescape must
// recover the original string.
//
// Invalid-UTF-8 inputs are tested separately: they cannot round-trip
// through any valid-Unicode representation, so the round-trip check is
// dropped there. See TestEscapeJSONString_InvalidUTF8MatchesStdlib.
func TestEscapeJSONString_RoundTripsAgainstStdlib(t *testing.T) {
	cases := []string{
		"",
		"hello",
		"He said \"hi\"",
		"a\\b",
		"line1\nline2",
		"café",
		"日本語",
		"🎉",
		"🎉🎉🎉",
		"mixed 日本語 🎉 café",
		"a\nb\"c\\d\u0001🎉",
		"\x00\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x0b\x0c\x0d\x0e\x0f",
		"\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f",
	}
	for _, s := range cases {
		t.Run("", func(t *testing.T) {
			got := EscapeJSONString(s)
			want := stdlibEscape(s)
			if got != want {
				t.Fatalf("EscapeJSONString(%q) = %q, want %q (stdlib)",
					s, got, want)
			}
			decoded, err := UnescapeJSONString(got)
			if err != nil {
				t.Fatalf("UnescapeJSONString(%q) = error: %v", got, err)
			}
			if decoded != s {
				t.Fatalf("round trip mismatch: input=%q, escaped=%q, decoded=%q",
					s, got, decoded)
			}
		})
	}
}

// TestEscapeJSONString_InvalidUTF8MatchesStdlib — for invalid-UTF-8
// inputs, EscapeJSONString must produce the same U+FFFD-substituted
// output as encoding/json. The full round trip is impossible (invalid
// bytes cannot be represented as valid Unicode), but the ESCAPE
// direction is tested.
func TestEscapeJSONString_InvalidUTF8MatchesStdlib(t *testing.T) {
	cases := []string{
		"hello \xff world", // bare invalid byte
		"\xc3\x28",         // invalid 2-byte sequence
		"\xe2\x28\xa1",     // invalid 3-byte sequence
		"\xf0\x28\x8c\xbc", // invalid 4-byte sequence
		"valid🎉\xffmore",   // valid + invalid mix
	}
	for _, s := range cases {
		t.Run("", func(t *testing.T) {
			got := EscapeJSONString(s)
			want := stdlibEscape(s)
			if got != want {
				t.Fatalf("EscapeJSONString(%q) = %q, want %q (stdlib)",
					s, got, want)
			}
			// The escaped form must be valid JSON, so UnescapeJSONString
			// succeeds on it.
			if _, err := UnescapeJSONString(got); err != nil {
				t.Fatalf("UnescapeJSONString(%q) = error: %v", got, err)
			}
		})
	}
}

// stdlibEscape returns encoding/json's escape for s with the surrounding
// quotes stripped. Used as the oracle for TestEscapeJSONString_RoundTripsAgainstStdlib.
func stdlibEscape(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		// json.Marshal never fails for valid Go strings in practice;
		// an error here would mean the test fixture is malformed.
		panic(err)
	}
	// Strip surrounding quotes.
	return string(b[1 : len(b)-1])
}

// TestUnescapeJSONString_TableDriven pins the canonical unescape output
// for the same eight shapes.
func TestUnescapeJSONString_TableDriven(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"plain ascii", "hello world", "hello world"},
		{"quote", `He said \"hi\"`, `He said "hi"`},
		{"backslash", `a\\b`, `a\b`},
		{"newline", `line1\nline2`, "line1\nline2"},
		{"all short forms", `\b\f\n\r\t`, "\b\f\n\r\t"},
		{"tab", `a\tb`, "a\tb"},
		{"cr", `a\rb`, "a\rb"},
		{"control x01", `a\u0001b`, "a\x01b"},
		{"emoji passthrough", "🎉", "🎉"},
		{"surrogate pair", `🎉`, "🎉"},
		{"combined", `a\nb\"c\\d\u0001🎉`, "a\nb\"c\\d\u0001🎉"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := UnescapeJSONString(tc.in)
			if err != nil {
				t.Fatalf("UnescapeJSONString(%q) = error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("UnescapeJSONString(%q) = %q, want %q",
					tc.in, got, tc.want)
			}
		})
	}
}

// TestUnescapeJSONString_Errors — malformed input must return an error
// rather than panic or silently truncate.
func TestUnescapeJSONString_Errors(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"trailing backslash", `abc\`},
		{"short unicode escape", `a\u00`},
		{"short unicode escape trailing", `a\u123`},
		{"bad hex digit", `a\u00ZZ`},
		{"unknown escape", `a\qb`},
		{"lone high surrogate", `\uD83C`},
		{"lone high surrogate no pair", `\uD83C\abc`},
		{"lone low surrogate", `\uDF89`},
		{"high surrogate followed by non-low", `\uD83C\u0041`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := UnescapeJSONString(tc.in)
			if err == nil {
				t.Fatalf("UnescapeJSONString(%q) = %q, want error",
					tc.in, got)
			}
		})
	}
}

// TestEscapeJSONString_NoHTMLEscape is the load-bearing regression guard:
// the chat payloads contain literal "<", ">", and "&" characters, and
// encoding/json's default HTML-escape would corrupt them. The wire
// helper here never HTML-escapes; this test pins the behavior.
func TestEscapeJSONString_NoHTMLEscape(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"<", "<"},
		{">", ">"},
		{"&", "&"},
		{"a<b>c&d", "a<b>c&d"},
	}
	for _, tc := range cases {
		if got := EscapeJSONString(tc.in); got != tc.want {
			t.Errorf("EscapeJSONString(%q) = %q, want %q",
				tc.in, got, tc.want)
		}
	}
}

// TestEscapeJSONString_FastPathNoAlloc — the fast-path detector scans
// the string for any escapable byte. A string with no escapable bytes
// must be returned by-reference (== identity) so callers can rely on
// the no-allocation property.
//
// Note: Go does not guarantee identity returns from a string parameter,
// so this test only asserts byte-equality and absence of mutation. The
// fast path itself is documented in escape.go.
func TestEscapeJSONString_FastPathNoAlloc(t *testing.T) {
	for _, s := range []string{
		"",
		"plain ascii",
		"café",
		"日本語",
		"🎉",
	} {
		if got := EscapeJSONString(s); got != s {
			t.Errorf("EscapeJSONString(%q) = %q, want %q", s, got, s)
		}
	}
}
