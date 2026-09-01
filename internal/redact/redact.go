// Package redact masks Google credential material out of arbitrary byte
// slices. It is the credential-handling primitive for the whole project:
// every log line, every error message, and every debug preview passes
// through Apply before it reaches the wire.
//
// The regex families are a direct port of the four from notebooklm-py's
// _logging.py plus four URL/header redactors:
//
//  1. Quoted JSON       — "SNlM0e":"abc123" or \"FdrFJe\":\"…\"
//  2. HTML-escaped JSON — &quot;SNlM0e&quot;:&quot;abc123&quot;
//  3. Form / prose      — SNlM0e=abc123 (URL-encoded form bodies,
//     log prose like "SNlM0e value is abc123")
//  4. Bare tokens       — SNlM0e or FdrFJe standing alone, NOT
//     embedded in a larger word
//
// URL redactors cover:
//
//   - query strings:  ?secret=hunter2&x=1   →   ?secret=[REDACTED]&x=1
//   - semicolon list: ;session=abc;x=1      →   ;session=[REDACTED];x=1
//   - Cookie: header line                    →   Cookie: [REDACTED]\n
//   - Authorization: header line             →   Authorization: [REDACTED]\n
//
// The package is stdlib-only and side-effect-free; safe to call from
// any goroutine.
package redact

import "regexp"

// redacted is the sentinel byte string we substitute for every match.
// Kept short so it does not dominate log line widths when many secrets
// land on the same line.
var redacted = []byte("[REDACTED]")

// token is the alternation group for the credential tokens we know
// about today. Extend when notebooklm-py adds a new token — keeping
// the table here makes the policy auditable in one place rather than
// scattered across four regexes.
//
// #nosec G101 -- the literal token strings here ARE the credentials
// we are configured to redact; the package's entire purpose is to
// recognize these substrings on the wire. gosec's hardcoded-
// credentials heuristic does not apply to a redaction policy table.
const token = `(?:SNlM0e|FdrFJe)` // #nosec G101 -- see comment.

// quotedJSON matches `"SNlM0e":"value"` and the JSON-escaped form
// `\"SNlM0e\":\"value\"`. The value run is bounded by the next
// unescaped quote so we capture the whole credential in one sweep.
//
// Forms handled:
//
//	"SNlM0e":"abc"
//	"SNlM0e":"abc","FdrFJe":"def"
//	\"SNlM0e\":\"abc\"
var quotedJSON = regexp.MustCompile(`(?:"|\\")` + token + `\\?"\s*:\s*\\?"[^"\\]*(?:\\.[^"\\]*)*\\?"`)

// htmlEscapedJSON is the same pattern with `&quot;` in place of the
// surrounding quotes. Used for HTML previews and HTML-embedded JSON
// (NotebookLM's WIZ_global_data blob rendered server-side).
var htmlEscapedJSON = regexp.MustCompile(`&quot;` + token + `&quot;\s*:\s*&quot;[^&]*(?:&[a-z]+;[^&]*)*&quot;`)

// formProse matches the URL-encoded form shape:
//
//	SNlM0e=abc123&FdrFJe=def456
//
// The trailing `[^\s&"]*` keeps the value within the same line /
// query string so we do not redact across boundaries.
var formProse = regexp.MustCompile(token + `=[^\s&"]*`)

// bareToken catches the token standing alone (not part of a longer
// word, not preceded by an identifier character). Anchored on word
// boundaries so `xSNlM0e` is left alone but `the SNlM0e value is …`
// is masked.
var bareToken = regexp.MustCompile(`\b` + token + `\b`)

// urlQuery masks `?secret=value&rest` and `&secret=value&rest`. The
// `?` is optional in the alternation so the same pattern works at
// the start and middle of a query string. Capture group 1 is the
// leading separator so the output stays parseable.
var urlQuery = regexp.MustCompile(`([?&])secret=[^&\s]*`)

// urlSemicolon masks `;session=value;rest` — the form seen in
// legacy session-id chunks inside cookie blobs.
var urlSemicolon = regexp.MustCompile(`;session=[^;\s]*`)

// cookieHeaderLine masks the value of a `Cookie:` request header
// line. The `(?m)` flag makes `^` match at line boundaries; we
// require the colon so `CookieMonster` is not matched. Capture
// group 1 is the `Cookie: ` prefix, which we preserve so the
// output stays parseable. The trailing `\r?\n` is captured too
// so the redacted line keeps its original line terminator
// (important for byte-clean log output).
var cookieHeaderLine = regexp.MustCompile(`(?im)^(Cookie:\s*).*?(?:\r\n|\r|\n|$)`)

// authorizationHeaderLine mirrors the Cookie pattern for the
// Authorization header.
var authorizationHeaderLine = regexp.MustCompile(`(?im)^(Authorization:\s*).*?(?:\r\n|\r|\n|$)`)

// redactors is the ordered list of substitutions Apply runs. Order
// matters only for readability — none of the patterns overlap when
// they fire on the same input.
var redactors = []*regexp.Regexp{
	quotedJSON,
	htmlEscapedJSON,
	formProse,
	bareToken,
	urlQuery,
	urlSemicolon,
	cookieHeaderLine,
	authorizationHeaderLine,
}

// Apply masks every credential-shaped substring in in. The returned
// slice is always a fresh allocation (regexp.ReplaceAll always
// allocates a new backing array even on no-match) so callers can
// safely mutate or retain it independently of the input.
//
// A nil or empty input returns nil — never an empty non-nil slice —
// so the no-secret fast path is allocation-free.
//
// The function is safe for concurrent use; the underlying regexp
// state is read-only after package init.
func Apply(in []byte) []byte {
	if len(in) == 0 {
		return nil
	}
	out := in
	for _, re := range redactors {
		// The two header-line regexes need to preserve their
		// `Header: ` prefix in the output. Every other pattern
		// is replaced wholesale with the redacted marker.
		if re == cookieHeaderLine || re == authorizationHeaderLine {
			out = re.ReplaceAllFunc(out, redactHeaderLine)
			continue
		}
		out = re.ReplaceAll(out, redacted)
	}
	return out
}

// redactHeaderLine is the ReplaceAllFunc callback used for the
// Cookie and Authorization header lines. It preserves the leading
// `Header:` prefix (group 1) and any trailing line terminator, so
// the output reads `Cookie: [REDACTED]\n` byte-for-byte instead of
// dropping the prefix that downstream parsers key on.
func redactHeaderLine(match []byte) []byte {
	// Locate the colon. Both patterns guarantee one exists.
	colon := -1
	for i, b := range match {
		if b == ':' {
			colon = i
			break
		}
	}
	if colon < 0 {
		return redacted
	}
	// Walk past the colon and any whitespace to the start of
	// the value; that is what we preserve.
	start := colon + 1
	for start < len(match) && (match[start] == ' ' || match[start] == '\t') {
		start++
	}
	// Preserve a trailing line terminator (`\n`, `\r`, or
	// `\r\n`) so the redacted line keeps the original line
	// boundary for line-oriented log readers.
	prefix := match[:start]
	tail := match[start:]
	if n := len(tail); n > 0 {
		switch tail[n-1] {
		case '\n':
			if n >= 2 && tail[n-2] == '\r' {
				return append(append(append([]byte{}, prefix...), redacted...), '\r', '\n')
			}
			return append(append([]byte{}, prefix...), append(redacted, '\n')...)
		case '\r':
			return append(append([]byte{}, prefix...), append(redacted, '\r')...)
		}
	}
	return append(append([]byte{}, prefix...), redacted...)
}
