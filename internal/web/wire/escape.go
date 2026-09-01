package wire

import "strings"

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
