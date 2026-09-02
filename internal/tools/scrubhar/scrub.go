// Package scrubhar is the byte-level redaction primitive used by every
// recorded-cassette writer and by the pre-commit guard test. It rewrites
// every credential-shaped substring (Cookie: line, SNlM0e / FdrFJe,
// at=, f.sid, signed URL params, account email) to a fixed sentinel
// so the result is safe to commit and safe to share with a contractor.
//
// A companion CLI binary lives under ./cmd and is the operator-facing
// driver. The library and the CLI share the same primitives so a
// recorded cassette and a manually rewritten one are byte-identical
// after the second pass.
//
// Boundary: scrubhar lives under internal/tools/ and is exempt from
// the import-boundary rules in docs/AGENTS.md rule 5. It imports
// internal/atomicio (atomic on-disk write) and stdlib only.
package scrubhar

import (
	"bytes"
	"net/url"
	"regexp"
)

// Sentinel is the placeholder we substitute for every matched
// credential. The string is a fixed, project-wide constant so
// downstream guard tests can grep for it without false positives.
const Sentinel = "SCRUBBED"

// PlaceholderEmail is the canned email we substitute for any real
// account address. Keeping it deterministic and at a reserved domain
// prevents the scrubber from accidentally re-leaking a real email
// through a chain of substitutions.
const PlaceholderEmail = Sentinel + "@example.com"

// signedURLParamKeys is the allowlist of query-string keys that carry
// signed-URL material on Google's download endpoints.
var signedURLParamKeys = []string{
	"x-goog-algorithm",
	"x-goog-credential",
	"x-goog-date",
	"x-goog-expires",
	"x-goog-signedheaders",
	"x-goog-signature",
}

// emailPattern is the canonical RFC 5322-ish email matcher,
// intentionally permissive — false positives here only affect a
// cassette, never production code, and the cost of a missed real
// email (leaking an account address) is high.
const emailPattern = `[\w.+\-]+@[\w-]+(?:\.[\w-]+)+`

// emailRegex is the compiled form of emailPattern.
var emailRegex = regexp.MustCompile(emailPattern)

// urlRegex matches an http(s) URL up to (but not including) the next
// whitespace, quote, or angle bracket.
var urlRegex = regexp.MustCompile(`https?://[A-Za-z0-9.\-]+(?::\d+)?(?:/[^"'<> \t\r\n]*)?`)

// ScrubBytes applies every credential-pattern rewrite to a single
// byte slice. The input may be header text, a URL, or a body — all
// credential shapes show up somewhere on the wire. The function is
// pure: callers decide whether the result replaces the input in
// place or is written to a new location.
func ScrubBytes(in []byte) []byte {
	out := in

	// Headers go first so the `Cookie: SCRUBBED` substitution wins
	// over a body-shaped `Cookie=` query param of the same value.
	out = replaceCookieHeader(out)

	// Signed-URL hosts carry x-goog-* query parameters whose values
	// are the credential-bearing signature. Walk each URL in the
	// input and scrub those query params before we fall through to
	// the name=value rewrites.
	out = scrubURLs(out)

	// The four token-shape rewrites. Each emits a name=value
	// placeholder so a reader can still see what kind of token
	// lived at that slot.
	out = replaceQueryParam(out, "SNlM0e", Sentinel)
	out = replaceQueryParam(out, "FdrFJe", Sentinel)
	out = replaceQueryParam(out, "at", Sentinel)
	out = replaceQueryParam(out, "f.sid", Sentinel)

	// Email rewrite. Applied last so the email-specific substring
	// wins over a generic cookie value that happened to contain an
	// `@`.
	out = emailRegex.ReplaceAll(out, []byte(PlaceholderEmail))

	return out
}

// ScrubQueryValue is the exported form of the URL scrubber; it lets
// other packages (notably the cassette harness) apply the same
// signed-URL scrubbing logic to a single URL string.
//
// The boolean reports whether the input was changed; a no-op rewrite
// (non-signed host, or every query param already scrubbed) reports
// false so callers can skip writing the result.
func ScrubQueryValue(rawURL string) (string, bool) {
	return scrubQueryValue(rawURL)
}

// scrubURLs walks every URL-shaped substring in in and rewrites the
// signed query parameters of those whose host is in the allowlist.
// The function is a no-op for hosts that do not carry signed URLs.
func scrubURLs(in []byte) []byte {
	return urlRegex.ReplaceAllFunc(in, func(m []byte) []byte {
		rewritten, changed := scrubQueryValue(string(m))
		if !changed {
			return m
		}
		return []byte(rewritten)
	})
}

// isSignedURLHost reports whether the given host is one of the
// allowlisted download hosts whose URLs carry signed query
// parameters.
func isSignedURLHost(host string) bool {
	switch host {
	case "lh3.googleusercontent.com",
		"storage.googleapis.com",
		"www.googleapis.com",
		"docs.google.com",
		"drive.google.com":
		return true
	}
	return false
}

// isSignedParamKey reports whether key is one of the allowlisted
// signed-URL query parameters.
func isSignedParamKey(key string) bool {
	for _, k := range signedURLParamKeys {
		if key == k {
			return true
		}
	}
	return false
}

// replaceCookieHeader rewrites a `Cookie:` header line so its value
// is the sentinel. The leading `Cookie:` prefix is preserved
// (downstream parsers key on it) and the trailing line terminator is
// preserved so the redacted line keeps its original line boundary.
func replaceCookieHeader(in []byte) []byte {
	const headerName = "Cookie:"
	var out bytes.Buffer
	out.Grow(len(in))
	at := 0
	for at < len(in) {
		eol := bytes.IndexByte(in[at:], '\n')
		var line []byte
		if eol < 0 {
			line = in[at:]
			at = len(in)
		} else {
			line = in[at : at+eol+1] // include the '\n'
			at += eol + 1
		}
		content := line
		if len(content) > 0 && content[len(content)-1] == '\n' {
			content = content[:len(content)-1]
		}
		if len(content) > 0 && content[len(content)-1] == '\r' {
			content = content[:len(content)-1]
		}
		if hasCIHeaderPrefix(content, headerName) {
			indent := leadingWhitespace(content)
			out.WriteString(indent)
			out.WriteString(headerName)
			out.WriteByte(' ')
			out.WriteString(Sentinel)
			out.Write(line[len(content):])
			continue
		}
		out.Write(line)
	}
	return out.Bytes()
}

// replaceQueryParam rewrites every occurrence of name=VALUE where
// VALUE runs to the next terminator. The placeholder is written as
// name=SCRUBBED so the resulting string is still parseable.
func replaceQueryParam(in []byte, name, placeholder string) []byte {
	prefix := []byte(name + "=")
	var out bytes.Buffer
	out.Grow(len(in))
	at := 0
	for at < len(in) {
		idx := bytes.Index(in[at:], prefix)
		if idx < 0 {
			out.Write(in[at:])
			break
		}
		out.Write(in[at : at+idx])
		out.WriteString(name)
		out.WriteByte('=')
		out.WriteString(placeholder)
		at += idx + len(prefix)
		for at < len(in) {
			c := in[at]
			if c == '&' || c == ' ' || c == '"' || c == '\n' {
				break
			}
			at++
		}
	}
	return out.Bytes()
}

// scrubQueryValue rewrites every query-string parameter whose key
// is in signedURLParamKeys (and whose host is a signed-URL host) so
// the value becomes the sentinel.
func scrubQueryValue(rawURL string) (string, bool) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL, false
	}
	if !isSignedURLHost(u.Host) {
		return rawURL, false
	}
	q := u.Query()
	changed := false
	for k, vs := range q {
		if !isSignedParamKey(k) {
			continue
		}
		for i := range vs {
			if vs[i] == Sentinel {
				continue
			}
			vs[i] = Sentinel
			changed = true
		}
		q[k] = vs
	}
	if !changed {
		return rawURL, false
	}
	u.RawQuery = q.Encode()
	return u.String(), true
}

// leadingWhitespace returns the run of leading ASCII spaces and tabs
// in b.
func leadingWhitespace(b []byte) string {
	for i, c := range b {
		if c != ' ' && c != '\t' {
			return string(b[:i])
		}
	}
	return string(b)
}

// hasCIHeaderPrefix reports whether content begins with headerName
// after a run of ASCII whitespace, comparing case-insensitively.
func hasCIHeaderPrefix(content []byte, headerName string) bool {
	if len(content) < len(headerName) {
		return false
	}
	i := 0
	for i < len(content) && (content[i] == ' ' || content[i] == '\t') {
		i++
	}
	if i+len(headerName) > len(content) {
		return false
	}
	return bytes.EqualFold(content[i:i+len(headerName)], []byte(headerName))
}
