// Package rows — utf16.go: UTF-16 offset primitive used by every chat-wire
// decoder in this package.
//
// The NotebookLM chat wire format (chatstream / chatnote payloads) counts
// every character offset — chat citation anchors, note passage spans,
// cited-text windows — in UTF-16 code units. Not bytes, not Go runes
// (int32 code points). One emoji anywhere in a cited fragment shifts
// every subsequent offset by one unit if you count in the wrong space,
// misaligning the citation anchor or getting the server to reject the
// saved note. This file is the load-bearing primitive that T-S3-007c/d
// (chatstream parser + history decoder) will rely on.
//
// Port of notebooklm-py/src/notebooklm/_types/documents.py::utf16_len and
// _utf16_slice. The Python reference implementation is
//
//	len(text.encode("utf-16-le", errors="surrogatepass")) // 2
//
// which is equivalent to `len(utf16.Encode([]rune(s)))` in Go for the
// inputs the wire format actually delivers (valid UTF-8, no lone
// surrogates).
//
// Per docs/AGENTS.md rule 5 this package is mode=internal: stdlib only.
// The unicode/utf16 and unicode/utf8 stdlib packages are the only
// imports; nothing else.
package rows

import (
	"unicode/utf16"
	"unicode/utf8"
)

// UTF16Len returns the length of s measured in UTF-16 code units (NOT Go
// runes / int32 code points). This is what batchexecute's chat wire
// format uses for slicing answer text out of larger conversation blobs.
//
// Semantics:
//   - ASCII / BMP runes: each rune == 1 UTF-16 unit.
//   - Non-BMP runes (emoji, supplementary plane): each rune == 2
//     UTF-16 units (high+low surrogate pair).
//   - Invalid UTF-8 sequences: each invalid byte becomes a U+FFFD
//     replacement rune on the way to utf16.Encode, so each invalid
//     byte is counted as 1 unit. The wire format never delivers invalid
//     UTF-8 in practice; this fallback exists so a malformed slice
//     cannot trigger a panic.
//
// Matches the Python reference
// `len(s.encode('utf-16-le', 'surrogatepass')) // 2` for all valid-UTF-8
// inputs (which covers every fixture and every live capture).
func UTF16Len(s string) int {
	return len(utf16.Encode([]rune(s)))
}

// UTF16Slice returns s[lo:hi] measured in UTF-16 code units. Out-of-range
// indices are clamped to [0, UTF16Len(s)]; lo > hi is treated as lo == hi.
//
// A half-cut on a surrogate pair (e.g. UTF16Slice("🎉", 0, 1)) returns the
// high surrogate as a Go string (three UTF-8 bytes: 0xED 0xA0 0xBC for
// U+D83C). The Python reference normalises any unpaired surrogate the
// cut creates to a placeholder rune via _sanitize; we deliberately do
// NOT replicate that sanitization here. The decoder sites in
// T-S3-007c/d work in UTF-16 space throughout, so a high surrogate that
// arrives as a Go string is exactly the bytes they need. Sanitization is
// a downstream concern of the wire-formatter layer (T-S3-007b), not
// this offset primitive.
//
// Whole runes that pass through the slice keep their canonical UTF-8
// encoding: UTF16Slice("🎉🎉", 0, 2) returns the same 8 bytes as
// `"🎉"` written in a Go source literal, not the 6-byte surrogate form.
// Only the half-cut cases emit the 3-byte surrogate encoding.
func UTF16Slice(s string, lo, hi int) string {
	total := UTF16Len(s)
	lo, hi = clampUTF16Range(lo, hi, total)
	if lo == 0 && hi == total {
		return s
	}

	// Walk s rune by rune, tracking the UTF-16 unit index at each rune
	// boundary. Append either the original UTF-8 byte sequence (whole
	// runes) or the surrogate-encoded bytes (half-cut astral runes) to
	// the result while the rune's UTF-16 units sit inside [lo, hi).
	var b []byte
	startUnit := 0
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			// Invalid byte: counts as 1 UTF-16 unit. Preserve the byte
			// verbatim so the result is byte-equal to the input for
			// the slice.
			if startUnit < hi && startUnit+1 > lo {
				b = append(b, s[i])
			}
			i++
			startUnit++
			if startUnit >= hi {
				break
			}
			continue
		}
		units := utf16UnitCount(r)
		endUnit := startUnit + units

		if endUnit > lo && startUnit < hi {
			keepLo := lo - startUnit
			if keepLo < 0 {
				keepLo = 0
			}
			keepHi := hi - startUnit
			if keepHi > units {
				keepHi = units
			}
			b = appendUTF16Units(b, s[i:i+size], r, size, keepLo, keepHi)
		}

		i += size
		startUnit = endUnit
		if startUnit >= hi {
			break
		}
	}
	return string(b)
}

// UTF16IndexToByteOffset converts a UTF-16 code-unit index in s to a byte
// offset suitable for slicing the original Go string. The returned
// offset is the byte position of the rune that OWNS the indexed UTF-16
// unit, so UTF16IndexToByteOffset is always a rune boundary in s
// (between surrogates of the same pair is not a valid offset — the wire
// format addresses pairs as one unit).
//
// Out-of-range indices are clamped: negative inputs become 0; inputs
// greater than UTF16Len(s) become len(s).
func UTF16IndexToByteOffset(s string, utf16Idx int) int {
	if utf16Idx <= 0 {
		return 0
	}
	total := UTF16Len(s)
	if utf16Idx >= total {
		return len(s)
	}
	units := 0
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			if units >= utf16Idx {
				return i
			}
			units++
			i++
			continue
		}
		u := utf16UnitCount(r)
		if units+u > utf16Idx {
			return i
		}
		units += u
		i += size
	}
	return len(s)
}

// clampUTF16Range normalises a [lo, hi] request against a [0, total]
// window. lo > hi collapses to lo == hi; out-of-range ends clamp.
func clampUTF16Range(lo, hi, total int) (int, int) {
	if lo < 0 {
		lo = 0
	}
	if hi > total {
		hi = total
	}
	if lo > hi {
		lo = hi
	}
	if lo > total {
		lo = total
	}
	return lo, hi
}

// utf16UnitCount returns 1 for BMP runes and 2 for astral-plane runes.
func utf16UnitCount(r rune) int {
	if r > 0xFFFF {
		return 2
	}
	return 1
}

// appendUTF16Units appends to b the UTF-8 bytes for rune r at offset
// runeBytes in s, retaining only the UTF-16 units in [keepLo, keepHi).
//
// For whole runes (the [0, 2] cut of an astral rune, or the [0, 1] cut
// of a BMP rune) we copy the original UTF-8 bytes from s verbatim so
// the result is byte-equal to the source — a 4-byte emoji stays a
// 4-byte emoji, never the 6-byte surrogate encoding.
//
// For half-cut astral runes (e.g. UTF16Slice("🎉", 0, 1) keeps only
// the high surrogate) we emit the canonical 3-byte UTF-8 surrogate
// sequence. Go's stdlib utf8.EncodeRune refuses to encode surrogates
// and substitutes U+FFFD, so we construct the bytes by hand:
//
//	surrogate = 0xD800 + ((r - 0x10000) >> 10)        // high
//	         or 0xDC00 + ((r - 0x10000) & 0x3FF)        // low
//	UTF-8 3-byte: 1110xxxx 10xxxxxx 10xxxxxx
//	  byte 1 = 0xED
//	  byte 2 = 0xA0 + ((surrogate >> 6) & 0x1F)
//	  byte 3 = 0x80 + (surrogate & 0x3F)
func appendUTF16Units(b []byte, runeBytes string, r rune, size int, keepLo, keepHi int) []byte {
	if r <= 0xFFFF {
		// BMP rune: keepLo == 0, keepHi == 1 keeps the whole rune.
		if keepLo == 0 && keepHi == 1 {
			b = append(b, runeBytes[:size]...)
		}
		return b
	}
	// Astral rune.
	if keepLo == 0 && keepHi == 2 {
		// Whole rune: copy the original UTF-8 bytes verbatim.
		b = append(b, runeBytes[:size]...)
		return b
	}
	high := 0xD800 + (r-0x10000)>>10
	low := 0xDC00 + (r-0x10000)&0x3FF
	switch {
	case keepLo == 0 && keepHi == 1:
		b = append(b, encodeSurrogate(high)...)
	case keepLo == 1 && keepHi == 2:
		b = append(b, encodeSurrogate(low)...)
	}
	return b
}

// encodeSurrogate returns the 3-byte UTF-8 sequence for a code point in
// the surrogate range U+D800..U+DFFF. Callers must guarantee the input
// is a surrogate; behavior on any other input is undefined.
func encodeSurrogate(surrogate rune) []byte {
	b1 := byte(0xED)
	b2 := byte(0xA0 + ((surrogate >> 6) & 0x1F))
	b3 := byte(0x80 + (surrogate & 0x3F))
	return []byte{b1, b2, b3}
}
