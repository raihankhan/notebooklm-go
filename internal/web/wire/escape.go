package wire

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// escapeAll percent-encodes a string the way Python's
// urllib.parse.quote(s, safe="") does:
//
//   - Every byte outside [A-Za-z0-9] becomes %XX in uppercase hex.
//   - Space encodes as %20, never as '+' (QueryEscape returns '+').
//   - '/', ':', ',', '[', ']', '+', '%' are ALL encoded; url.PathEscape
//     leaves '/' alone, so it is not a substitute.
//
// This is the percent-encoding rule the NotebookLM wire payloads expect;
// Python's quote(s, safe="") is the reference implementation. Keep this in
// sync with notebooks/urls.py::quote (see docs/AGENTS.md rule 1).
func escapeAll(s string) string {
	// Fast path: ASCII alphanumeric-only strings need no allocation.
	needs := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !isUnreserved(c) {
			needs = true
			break
		}
	}
	if !needs {
		return s
	}

	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isUnreserved(c) {
			b.WriteByte(c)
			continue
		}
		// Pass multi-byte UTF-8 sequences through verbatim: Python's
		// quote(s, safe="") emits UTF-8 bytes percent-encoded, which is
		// exactly what url.PathEscape does for a single rune. We replicate
		// that by percent-encoding each non-ASCII byte individually rather
		// than letting %XX collapse a multi-byte rune into one blob.
		const hex = "0123456789ABCDEF"
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0xF])
	}
	return b.String()
}

// isUnreserved matches the Python urllib.parse.unreserved letters: ASCII
// letters, digits, and the four "always safe" punctuation marks '_', '.',
// '~', '-'. Everything else — including space, '/', ':', ',', '[', ']',
// '+', '%' — must be percent-encoded.
func isUnreserved(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z':
		return true
	case c >= 'a' && c <= 'z':
		return true
	case c >= '0' && c <= '9':
		return true
	case c == '_' || c == '.' || c == '~' || c == '-':
		return true
	}
	return false
}

// EscapeJSONString returns the JSON-escaped form of s (no surrounding
// quotes). Used by chatstream/chatnote payload builders to encode
// user-supplied strings into the wire format.
//
// Equivalent to `json.dumps(s)[1:-1]` in Python: the leading/trailing
// quote characters are stripped so the caller can splice the result
// into a larger JSON template that owns the surrounding context.
//
// The escape grammar mirrors RFC 8259 §7:
//
//	"  →  \"
//	\  →  \\
//	\b →  \b
//	\f →  \f
//	\n →  \n
//	\r →  \r
//	\t →  \t
//	< 0x20 → \u00XX
//
// UTF-8 multi-byte sequences pass through verbatim; the "/" character
// is NOT escaped (it is an optional escape in RFC 8259 and json.dumps
// does not emit it). encoding/json produces the same output, modulo
// HTML-escape: this helper never HTML-escapes "<", ">", "&" — those
// are not JSON escapes per RFC 8259, and the chat payloads contain
// them verbatim.
func EscapeJSONString(s string) string {
	// Fast path: scan for any byte that needs escaping. If none, return s.
	// An invalid UTF-8 byte (>= 0x80 not part of a valid multi-byte
	// sequence) also forces the slow path because we must replace it
	// with the U+FFFD escape to match encoding/json's output.
	needsEscape := false
	for i := 0; i < len(s); {
		c := s[i]
		if c == '"' || c == '\\' || c < 0x20 {
			needsEscape = true
			break
		}
		if c >= 0x80 {
			// Possible start of multi-byte UTF-8. Decode to confirm.
			_, size := utf8.DecodeRuneInString(s[i:])
			if size == 1 {
				// Invalid byte: needs U+FFFD escape.
				needsEscape = true
				break
			}
			i += size
			continue
		}
		i++
	}
	if !needsEscape {
		return s
	}

	out := make([]byte, 0, len(s)+8)
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '"':
			out = append(out, '\\', '"')
			i++
		case c == '\\':
			out = append(out, '\\', '\\')
			i++
		case c == '\b':
			out = append(out, '\\', 'b')
			i++
		case c == '\f':
			out = append(out, '\\', 'f')
			i++
		case c == '\n':
			out = append(out, '\\', 'n')
			i++
		case c == '\r':
			out = append(out, '\\', 'r')
			i++
		case c == '\t':
			out = append(out, '\\', 't')
			i++
		case c < 0x20:
			out = append(out, '\\', 'u', '0', '0',
				hexDigit(c>>4), hexDigit(c&0xF))
			i++
		default:
			// Multi-byte UTF-8 or ASCII printable: copy through verbatim.
			r, size := utf8.DecodeRuneInString(s[i:])
			if r == utf8.RuneError && size == 1 {
				// Invalid byte: escape as U+FFFD so the output is
				// valid JSON (encoding/json replaces with U+FFFD on
				// encode, matching this behavior).
				out = append(out, '\\', 'u', 'f', 'f', 'f', 'd')
				i++
				continue
			}
			out = append(out, s[i:i+size]...)
			i += size
		}
	}
	return string(out)
}

// UnescapeJSONString is the inverse of EscapeJSONString: it parses the
// JSON-escape grammar in s and returns the unescaped string. The input
// must be the JSON string content (no surrounding quotes); an input
// that ends mid-escape returns an error rather than panicking, so a
// malformed wire payload surfaces as a typed error at the boundary
// rather than crashing the parser.
//
// The grammar matched here is a strict subset of RFC 8259 §7 — exactly
// what encoding/json produces on encode and accepts on decode:
//
//	\" \\ \/ \b \f \n \r \t \uXXXX \uXXXX\uXXXX (UTF-16 surrogate pair)
func UnescapeJSONString(s string) (string, error) {
	// Fast path: no backslash means no escapes.
	if !containsBackslash(s) {
		return s, nil
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if c != '\\' {
			out = append(out, c)
			i++
			continue
		}
		// Need at least one more byte for the escape.
		if i+1 >= len(s) {
			return "", fmt.Errorf("wire: UnescapeJSONString: trailing backslash at offset %d", i)
		}
		esc := s[i+1]
		switch esc {
		case '"':
			out = append(out, '"')
			i += 2
		case '\\':
			out = append(out, '\\')
			i += 2
		case '/':
			out = append(out, '/')
			i += 2
		case 'b':
			out = append(out, '\b')
			i += 2
		case 'f':
			out = append(out, '\f')
			i += 2
		case 'n':
			out = append(out, '\n')
			i += 2
		case 'r':
			out = append(out, '\r')
			i += 2
		case 't':
			out = append(out, '\t')
			i += 2
		case 'u':
			if i+6 > len(s) {
				return "", fmt.Errorf("wire: UnescapeJSONString: short \\u escape at offset %d", i)
			}
			hex := s[i+2 : i+6]
			r, err := unhexRune(hex)
			if err != nil {
				return "", fmt.Errorf("wire: UnescapeJSONString: bad \\u escape %q: %w", hex, err)
			}
			if r >= 0xD800 && r <= 0xDBFF {
				// High surrogate — must be followed by a low surrogate.
				if i+12 > len(s) || s[i+6] != '\\' || s[i+7] != 'u' {
					return "", fmt.Errorf("wire: UnescapeJSONString: lone high surrogate %U at offset %d", r, i)
				}
				hex2 := s[i+8 : i+12]
				r2, err := unhexRune(hex2)
				if err != nil {
					return "", fmt.Errorf("wire: UnescapeJSONString: bad \\u escape %q: %w", hex2, err)
				}
				if r2 < 0xDC00 || r2 > 0xDFFF {
					return "", fmt.Errorf("wire: UnescapeJSONString: expected low surrogate after %U, got %U", r, r2)
				}
				combined := 0x10000 + ((r - 0xD800) << 10) + (r2 - 0xDC00)
				var buf [utf8.UTFMax]byte
				n := utf8.EncodeRune(buf[:], combined)
				out = append(out, buf[:n]...)
				i += 12
				continue
			}
			if r >= 0xDC00 && r <= 0xDFFF {
				return "", fmt.Errorf("wire: UnescapeJSONString: lone low surrogate %U at offset %d", r, i)
			}
			var buf [utf8.UTFMax]byte
			n := utf8.EncodeRune(buf[:], r)
			out = append(out, buf[:n]...)
			i += 6
		default:
			return "", fmt.Errorf("wire: UnescapeJSONString: unknown escape \\%c at offset %d", esc, i)
		}
	}
	return string(out), nil
}

// containsBackslash reports whether s contains any '\\' byte. It is the
// fast-path detector for UnescapeJSONString.
func containsBackslash(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' {
			return true
		}
	}
	return false
}

// hexDigit returns the lowercase-hex representation of the low 4 bits
// of b. Only the bottom 4 bits are consulted.
func hexDigit(b byte) byte {
	if b < 10 {
		return '0' + b
	}
	return 'a' + (b - 10)
}

// unhexRune parses a 4-character hex string as a Unicode code point in
// the range U+0000..U+FFFF. The returned rune is in [0, 0xFFFF].
func unhexRune(s string) (rune, error) {
	var r rune
	for i := 0; i < 4; i++ {
		c := s[i]
		var v byte
		switch {
		case c >= '0' && c <= '9':
			v = c - '0'
		case c >= 'a' && c <= 'f':
			v = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			v = c - 'A' + 10
		default:
			return 0, fmt.Errorf("non-hex digit %q", c)
		}
		r = r<<4 | rune(v)
	}
	return r, nil
}
