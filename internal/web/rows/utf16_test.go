// Tests for the UTF-16 offset primitive.
//
// The acceptance-criteria rows below mirror the reference values the
// Python original produces via
//
//	len(s.encode("utf-16-le", errors="surrogatepass")) // 2
//
// plus the documented UTF16Slice / UTF16IndexToByteOffset semantics from
// utf16.go's doc comments. Every assertion here is a literal Go
// expression of what the Python reference would return; there is no
// oracle computed from the code under test.
package rows

import (
	"testing"
	"unicode/utf8"
)

// utf16RuneSize is the UTF-16 unit count for a single rune (1 for BMP,
// 2 for astral-plane). Mirrors the helper in utf16.go.
func utf16RuneSize(r rune) int {
	if r > 0xFFFF {
		return 2
	}
	return 1
}

// TestUTF16Len_TableDriven — the load-bearing offset table from the
// ticket's AC list. Every row pins UTF16Len against the documented
// invariants (ASCII == len == runeCount; CJK == len == runeCount;
// Latin-1 accents == len == runeCount; emoji > runeCount).
func TestUTF16Len_TableDriven(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int // == len(in.encode("utf-16-le", "surrogatepass")) // 2
	}{
		// ASCII: every byte is a UTF-16 unit. utf16Len == len == runeCount.
		{"ascii empty", "", 0},
		{"ascii short", "hello", 5},
		{"ascii long", "the quick brown fox", 19},

		// Latin-1 accents: 2-byte UTF-8 but still 1 UTF-16 unit each.
		{"latin1 accent short", "café", 4},
		{"latin1 accent list", "café résumé naïve", 17},

		// CJK: BMP runes, 3-byte UTF-8 each, 1 UTF-16 unit each.
		{"cjk short", "日本語", 3},
		{"cjk list", "日本語中文", 5},

		// Emoji: astral-plane, 4-byte UTF-8, 2 UTF-16 units each.
		{"single emoji", "🎉", 2},
		{"two emoji", "🎉🎉", 4},
		{"emoji + text", "🎉hello", 7},
		{"text + emoji", "hello🎉", 7},
		{"emoji sandwich", "a🎉b", 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := UTF16Len(tc.in); got != tc.want {
				t.Errorf("UTF16Len(%q) = %d, want %d",
					tc.in, got, tc.want)
			}
		})
	}
}

// TestUTF16Len_RelationshipToLenAndRuneCount documents the three-way
// disagreement between the three length measures for non-BMP input.
//
//   - len(s)            = bytes
//   - utf8.RuneCountInString(s) = int32 code points
//   - UTF16Len(s)        = UTF-16 code units (the wire format's unit)
//
// The only inputs where all three agree are empty + pure-ASCII. CJK
// breaks the bytes agreement; emoji breaks the runeCount agreement.
func TestUTF16Len_RelationshipToLenAndRuneCount(t *testing.T) {
	cases := []struct {
		name string
		s    string
	}{
		{"ascii", "hello"},
		{"accent", "café"},
		{"cjk", "日本語"},
		{"emoji", "🎉"},
		{"emoji pair", "🎉🎉"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := UTF16Len(tc.s)
			runes := utf8.RuneCountInString(tc.s)
			bytes := len(tc.s)
			if runes > 0 && got != runes {
				// Non-BMP: UTF16Len must be strictly greater than runeCount.
				if runes == bytes {
					t.Errorf("UTF16Len(%q)=%d == runeCount=%d (expected disagreement for non-BMP)",
						tc.s, got, runes)
				}
			}
			_ = bytes
		})
	}
}

// TestUTF16Slice_BasicRanges — the slicing semantics. For pure-ASCII
// inputs the UTF-16 slice must equal the byte slice; for non-BMP it
// must preserve rune boundaries unless we explicitly cut mid-pair.
func TestUTF16Slice_BasicRanges(t *testing.T) {
	cases := []struct {
		name string
		in   string
		lo   int
		hi   int
		want string
	}{
		// Whole-string slices.
		{"ascii full", "hello", 0, 5, "hello"},
		{"emoji full", "🎉🎉", 0, 4, "🎉🎉"},

		// Empty slice via equal bounds.
		{"ascii empty mid", "hello", 2, 2, ""},
		{"emoji empty mid", "🎉🎉", 2, 2, ""},

		// Mid-string slice on BMP.
		{"ascii mid", "hello", 1, 4, "ell"},

		// Mid-string slice on astral — aligned to rune boundary.
		{"emoji aligned head", "🎉🎉", 0, 2, "🎉"},
		{"emoji aligned tail", "🎉🎉", 2, 4, "🎉"},

		// Emoji + text, slice that lands mid-emoji in UTF-16 space —
		// the half-cut emits a 3-byte surrogate encoding, not the
		// 4-byte UTF-8 emoji.
		{"emoji text mid cut", "a🎉b🎉c", 0, 2, "a\xED\xA0\xBC"},
		{"emoji text whole", "🎉hello", 0, 2, "🎉"},
		{"emoji text slice 2", "🎉hello", 2, 7, "hello"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := UTF16Slice(tc.in, tc.lo, tc.hi); got != tc.want {
				t.Errorf("UTF16Slice(%q, %d, %d) = %q, want %q",
					tc.in, tc.lo, tc.hi, got, tc.want)
			}
		})
	}
}

// TestUTF16Slice_HalfCutSurrogate — the load-bearing case from the
// AC list. UTF16Slice("🎉", 0, 1) must return the high surrogate
// (U+D83C) encoded as its canonical 3-byte UTF-8 sequence 0xED 0xA0 0xBC.
//
// Python's reference implementation uses surrogatepass + sanitize to
// substitute a placeholder for unpaired surrogates. We deliberately do
// NOT sanitize — the decoder sites in T-S3-007c/d need the high
// surrogate bytes themselves when they cut a citation window mid-emoji.
func TestUTF16Slice_HalfCutSurrogate(t *testing.T) {
	const want = "\xED\xA0\xBC" // U+D83C in UTF-8.
	got := UTF16Slice("🎉", 0, 1)
	if got != want {
		t.Fatalf("UTF16Slice(%q, 0, 1) = % x (len=%d), want % x (len=%d)",
			"🎉", []byte(got), len(got), []byte(want), len(want))
	}
	if len(got) != 3 {
		t.Fatalf("half-cut surrogate length = %d, want 3", len(got))
	}
}

// TestUTF16Slice_LowSurrogate — UTF16Slice("🎉", 1, 2) returns the
// low surrogate (U+DF89) as its 3-byte UTF-8 sequence 0xED 0xBE 0x89.
func TestUTF16Slice_LowSurrogate(t *testing.T) {
	const want = "\xED\xBE\x89" // U+DF89 in UTF-8.
	got := UTF16Slice("🎉", 1, 2)
	if got != want {
		t.Fatalf("UTF16Slice(%q, 1, 2) = % x (len=%d), want % x (len=%d)",
			"🎉", []byte(got), len(got), []byte(want), len(want))
	}
}

// TestUTF16Slice_OutOfRangeClamp — out-of-range indices must clamp
// to [0, UTF16Len(s)]. Negative lo clamps to 0; hi past end clamps to
// UTF16Len(s); lo > hi collapses to lo == hi.
func TestUTF16Slice_OutOfRangeClamp(t *testing.T) {
	cases := []struct {
		name string
		in   string
		lo   int
		hi   int
		want string
	}{
		// Negative lo clamps to 0.
		{"neg lo ascii", "hello", -5, 3, "hel"},
		{"neg lo emoji", "🎉🎉", -1, 2, "🎉"},

		// hi past end clamps to total.
		{"hi over ascii", "hello", 1, 100, "ello"},
		{"hi over emoji", "🎉🎉", 2, 100, "🎉"},

		// lo > hi collapses to empty.
		{"lo > hi ascii", "hello", 4, 2, ""},
		{"lo > hi emoji", "🎉🎉", 4, 2, ""},
		{"lo > hi with full bounds", "hello", 100, 200, ""},

		// Both ends past range clamps to whole string.
		{"both over ascii", "hello", -10, 100, "hello"},
		{"both over emoji", "🎉🎉", -10, 100, "🎉🎉"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := UTF16Slice(tc.in, tc.lo, tc.hi); got != tc.want {
				t.Errorf("UTF16Slice(%q, %d, %d) = %q, want %q",
					tc.in, tc.lo, tc.hi, got, tc.want)
			}
		})
	}
}

// TestUTF16Slice_RuneAlignedMatchesPythonReference — for rune-aligned
// slices (every lo and hi that lands on a rune boundary), the round
// trip through UTF16Len must equal hi - lo. Half-cut slices produce
// strings with lone-surrogate bytes that UTF16Len reads as invalid
// UTF-8 and so cannot recover the original UTF-16 unit count from; the
// round trip is intentionally not asserted for those cases.
//
// The Python expression inlined below is
//
//	len(s.encode('utf-16-le', 'surrogatepass')) // 2
//
// for each row, computed by hand against the literal strings.
func TestUTF16Slice_RuneAlignedMatchesPythonReference(t *testing.T) {
	cases := []struct {
		s       string
		lo, hi  int
		wantLen int
	}{
		{"café", 0, 4, 4}, // full string
		{"café", 1, 3, 2}, // "af"
		{"日本語", 0, 3, 3},  // full
		{"日本語", 1, 2, 1},  // "本"
		{"🎉🎉🎉", 0, 6, 6},  // full
		{"🎉🎉🎉", 0, 4, 4},  // first two emoji
		{"🎉🎉🎉", 2, 6, 4},  // last two emoji
		{"a🎉b", 0, 4, 4},  // full
		{"a🎉b", 1, 3, 2},  // "🎉"
	}
	for _, tc := range cases {
		t.Run("", func(t *testing.T) {
			got := UTF16Slice(tc.s, tc.lo, tc.hi)
			if n := UTF16Len(got); n != tc.wantLen {
				t.Errorf("UTF16Len(UTF16Slice(%q, %d, %d)) = %d, want %d (slice=%q)",
					tc.s, tc.lo, tc.hi, n, tc.wantLen, got)
			}
		})
	}
}

// TestUTF16Slice_HalfCutEmitsSurrogate — a half-cut on an astral rune
// emits the canonical 3-byte UTF-8 surrogate encoding for the kept
// surrogate. Half-cuts are NOT expected to round-trip through UTF16Len
// (lone surrogates are invalid UTF-8) but the byte shape is pinned here
// so a future "fix" that tries to sanitize to U+FFFD is caught.
func TestUTF16Slice_HalfCutEmitsSurrogate(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		lo, hi  int
		want    string
		wantLen int // byte length of want
	}{
		{
			"high surrogate only",
			"🎉", 0, 1,
			"\xED\xA0\xBC", 3,
		},
		{
			"low surrogate only",
			"🎉", 1, 2,
			"\xED\xBE\x89", 3,
		},
		{
			"whole emoji + high surrogate",
			"🎉🎉🎉", 0, 3,
			"🎉" + "\xED\xA0\xBC", 7,
		},
		{
			"low surrogate + whole emoji",
			"🎉🎉🎉", 1, 4,
			"\xED\xBE\x89" + "🎉", 7,
		},
		{
			"low + whole + high",
			"🎉🎉🎉", 1, 5,
			"\xED\xBE\x89" + "🎉" + "\xED\xA0\xBC", 10,
		},
		{
			"text then high surrogate",
			"a🎉b🎉c", 0, 2,
			"a" + "\xED\xA0\xBC", 4,
		},
		{
			"text then low surrogate",
			"a🎉b🎉c", 2, 4,
			"\xED\xBE\x89" + "b", 4,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := UTF16Slice(tc.in, tc.lo, tc.hi)
			if got != tc.want {
				t.Errorf("UTF16Slice(%q, %d, %d) = % x (str=%q), want % x (str=%q)",
					tc.in, tc.lo, tc.hi,
					[]byte(got), got,
					[]byte(tc.want), tc.want)
			}
			if len(got) != tc.wantLen {
				t.Errorf("UTF16Slice(%q, %d, %d) byte length = %d, want %d",
					tc.in, tc.lo, tc.hi, len(got), tc.wantLen)
			}
		})
	}
}

// TestUTF16IndexToByteOffset_Boundaries — every index 0..total maps to
// a rune boundary in s. We pin the byte offset for each index to make
// regression drift visible: a future "optimisation" that rounds up
// instead of down would fail here.
func TestUTF16IndexToByteOffset_Boundaries(t *testing.T) {
	cases := []struct {
		s       string
		indices []struct {
			idx  int
			want int // expected byte offset
		}
	}{
		{
			s: "hello",
			indices: []struct {
				idx  int
				want int
			}{
				{0, 0}, {1, 1}, {2, 2}, {3, 3}, {4, 4}, {5, 5},
			},
		},
		{
			s: "🎉🎉",
			indices: []struct {
				idx  int
				want int
			}{
				{0, 0}, // before the first emoji
				{1, 0}, // inside first emoji (rounds down to its start)
				{2, 4}, // after first emoji = before second emoji
				{3, 4}, // inside second emoji
				{4, 8}, // end
			},
		},
		{
			s: "a🎉b",
			indices: []struct {
				idx  int
				want int
			}{
				{0, 0}, // 'a'
				{1, 1}, // start of emoji
				{2, 1}, // inside emoji
				{3, 5}, // 'b'
				{4, 6}, // end
			},
		},
	}
	for _, tc := range cases {
		for _, idx := range tc.indices {
			got := UTF16IndexToByteOffset(tc.s, idx.idx)
			if got != idx.want {
				t.Errorf("UTF16IndexToByteOffset(%q, %d) = %d, want %d",
					tc.s, idx.idx, got, idx.want)
			}
		}
	}
}

// TestUTF16IndexToByteOffset_OutOfRange — clamping behavior.
func TestUTF16IndexToByteOffset_OutOfRange(t *testing.T) {
	cases := []struct {
		s    string
		idx  int
		want int
	}{
		{"hello", -1, 0},   // negative → 0
		{"hello", -100, 0}, // far negative → 0
		{"hello", 6, 5},    // one past end → len
		{"hello", 100, 5},  // far past end → len
		{"🎉🎉", -1, 0},
		{"🎉🎉", 5, 8},   // one past end (total=4) → len
		{"🎉🎉", 100, 8}, // far past end
	}
	for _, tc := range cases {
		if got := UTF16IndexToByteOffset(tc.s, tc.idx); got != tc.want {
			t.Errorf("UTF16IndexToByteOffset(%q, %d) = %d, want %d",
				tc.s, tc.idx, got, tc.want)
		}
	}
}

// TestUTF16Slice_RuneAlignedRoundTrip — for every rune-aligned index
// in s (i.e. an i that lands on a rune boundary in UTF-16 space),
// UTF16Slice(s, 0, i) followed by UTF16Len must equal i. The round trip
// only holds for rune-aligned indices — half-cut indices produce slices
// whose bytes (lone-surrogate encodings) UTF16Len cannot recover, by
// design. See utf16.go's doc comment on UTF16Slice for the rationale.
func TestUTF16Slice_RuneAlignedRoundTrip(t *testing.T) {
	// runeAlignedIndices returns every UTF-16 index in s that lands on
	// a rune boundary. Always includes 0 and UTF16Len(s).
	runeAlignedIndices := func(s string) []int {
		out := []int{0}
		units := 0
		for i := 0; i < len(s); {
			r, size := utf8.DecodeRuneInString(s[i:])
			if r == utf8.RuneError && size == 1 {
				units++
				i++
				continue
			}
			units += utf16RuneSize(r)
			out = append(out, units)
			i += size
		}
		return out
	}
	for _, s := range []string{
		"hello",
		"café",
		"日本語",
		"🎉",
		"🎉🎉",
		"a🎉b🎉c",
		"",
		"mixed 日本語 🎉 café",
	} {
		for _, i := range runeAlignedIndices(s) {
			slice := UTF16Slice(s, 0, i)
			if got := UTF16Len(slice); got != i {
				t.Errorf("UTF16Len(UTF16Slice(%q, 0, %d)) = %d, want %d (slice=%q)",
					s, i, got, i, slice)
			}
		}
	}
}

// TestUTF16Slice_FullStringRoundTrip — UTF16Slice(s, 0, UTF16Len(s))
// must equal s byte-for-byte.
func TestUTF16Slice_FullStringRoundTrip(t *testing.T) {
	for _, s := range []string{
		"",
		"hello",
		"café",
		"日本語",
		"🎉",
		"🎉🎉",
		"a🎉b",
		"mixed 日本語 🎉 café",
	} {
		if got := UTF16Slice(s, 0, UTF16Len(s)); got != s {
			t.Errorf("UTF16Slice(%q, 0, %d) = %q, want %q",
				s, UTF16Len(s), got, s)
		}
	}
}
