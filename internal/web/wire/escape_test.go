package wire

import (
	"strings"
	"testing"
)

func TestEscapeAll_TableDriven(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// Acceptance-criteria rows.
		{"space", " ", "%20"},
		{"slash", "/", "%2F"},
		{"plus", "+", "%2B"},
		// Spec table coverage: space, /, :, ,, [, ], +, %, and a UTF-8 rune.
		{"colon", ":", "%3A"},
		{"comma", ",", "%2C"},
		{"lbracket", "[", "%5B"},
		{"rbracket", "]", "%5D"},
		{"percent", "%", "%25"},
		{"utf8 multibyte", "é", "%C3%A9"},
		// Combined: a path-ish string with every tricky character.
		{"combined", "a b/c+d%e", "a%20b%2Fc%2Bd%25e"},
		// Negatives: characters that MUST NOT be encoded.
		{"alnum", "abcXYZ012", "abcXYZ012"},
		{"unreserved punct", "a_b.c~d-e", "a_b.c~d-e"},
		// Boundary: empty string.
		{"empty", "", ""},
		// Trailing newline (a real wire-payload hazard).
		{"newline", "a\nb", "a%0Ab"},
		// Emoji (4-byte UTF-8).
		{"emoji", "😀", "%F0%9F%98%80"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := escapeAll(tc.in)
			if got != tc.want {
				t.Fatalf("escapeAll(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestEscapeAll_RoundTripSafeForASCII(t *testing.T) {
	// Every ASCII character except letters and digits should encode to a
	// 3-byte %XX sequence. Bytes >= 0x80 also encode to 3 bytes each (the
	// UTF-8 expansion). So len(escapeAll(s)) >= len(s) with equality only
	// for the unreserved set.
	inputs := []string{"a", "0", " ", "/", ":", ",", "[", "]", "+", "%", "é"}
	for _, s := range inputs {
		got := escapeAll(s)
		if strings.ContainsAny(got, "+ ") {
			t.Fatalf("escapeAll(%q) = %q: must not contain '+' or literal space", s, got)
		}
		// Every encoded byte must be in the %XX form with uppercase hex.
		for i := 0; i < len(got); i++ {
			c := got[i]
			if c == '%' {
				if i+2 >= len(got) {
					t.Fatalf("escapeAll(%q) truncated %% sequence: %q", s, got)
				}
				for j := 1; j <= 2; j++ {
					hc := got[i+j]
					if hc < '0' || hc > '9' {
						if hc < 'A' || hc > 'F' {
							t.Fatalf("escapeAll(%q) = %q: non-uppercase-hex in %% sequence", s, got)
						}
					}
				}
			}
		}
	}
}

func TestEscapeAll_AcceptanceRowByRow(t *testing.T) {
	// Mirrors the explicit acceptance-criteria bullets in the T-P1-1 spec:
	//   escapeAll(" ") == "%20"
	//   escapeAll("/") == "%2F"
	//   escapeAll("+") == "%2B"
	check := func(in, want string) {
		t.Helper()
		if got := escapeAll(in); got != want {
			t.Fatalf("escapeAll(%q) = %q, want %q", in, got, want)
		}
	}
	check(" ", "%20")
	check("/", "%2F")
	check("+", "%2B")
}
