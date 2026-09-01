// Package cookiejar implements the in-memory RFC 6265 cookie jar used by
// every authenticated transport call. It mirrors net/http/cookiejar's
// selection algorithm (RFC 6265 §5.3 storage and §5.4 selection) and adds
// the two security layers the stdlib jar does not enforce:
//
//  1. Public-suffix rejection: setting a cookie whose Domain attribute
//     matches the public-suffix list (co.uk, com.au, …) is refused with
//     ErrPublicSuffix. This is the precise bug class that lets an
//     attacker plant cookies on every subdomain of a shared suffix.
//  2. __Secure-/__Host- prefix enforcement: cookies with a __Secure- or
//     __Host- prefix are refused unless the originating request is
//     https:// and every documented attribute invariant holds. Setting
//     a __Host- cookie with a Domain attribute, for example, is rejected
//     with ErrHostPrefixOnSubpath; setting a __Secure- cookie over a
//     plain-http URL is rejected with ErrInsecurePrefix.
//
// Cookies in this package carry a Raw field for the unparsed Set-Cookie
// line so storage and persistence can round-trip every byte the server
// sent (T-P2-2). The Cookie.String() method runs the value through
// internal/redact so log lines, error messages, and HeaderFor() output
// never leak the credential to a sink outside the secure boundary.
//
// Boundary: per docs/AGENTS.md rule 5, this package is mode=internal;
// it imports stdlib + internal/redact only. No third-party dependencies.
package cookiejar

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// redactedMarker is the byte string substituted for the cookie value
// in Cookie.String() and Jar.HeaderFor(). It is intentionally short
// so a header line carrying many cookies stays legible, and it matches
// the marker internal/redact uses internally so a log line that
// already routed through the primitive is byte-identical to a line
// that did not.
const redactedMarker = "[REDACTED]"

// Cookie mirrors net/http.Cookie plus a Raw field for the unparsed
// Set-Cookie line and a HostOnly flag. The field set is identical to
// http.Cookie so callers can convert with (*http.Cookie)(c) — the Go
// compiler does not allow the conversion directly because of the extra
// fields, but the layout is compatible for any code that uses only
// the standard fields.
//
// Persistence (T-P2-2) uses Raw to reproduce the server's Set-Cookie
// byte-for-byte. Round-tripping Raw preserves attributes the typed
// fields do not cover (e.g. extensions like "Priority=High" or future
// attributes not yet in the struct).
//
// HostOnly is true iff the Set-Cookie arrived without a Domain
// attribute. Per RFC 6265 §5.3 step 5, such a cookie is only sent to
// the exact origin that received it; we use the flag (rather than
// stamping the URL's host onto Domain) so a host-only cookie stored
// at example.com does NOT match a request to sub.example.com — that
// is the exact behavior issue #369 documents.
type Cookie struct {
	Name     string
	Value    string
	Domain   string
	Path     string
	Expires  time.Time
	MaxAge   int
	Secure   bool
	HttpOnly bool
	SameSite bool
	HostOnly bool
	Raw      string
}

// String renders the cookie as "name=value" — the form used in
// Set-Cookie request lines — with the value run through
// internal/redact.Apply so logs and error messages never leak the
// credential to an unsafe sink.
//
// Note this differs from http.Cookie.String, which would render
// "name=value" with the literal value. The standard library is right
// for *transport* use; for *log* use (and that is every other callsite
// in this project), the redacted form is mandatory under docs/AGENTS.md
// rule 4. Callers that need the literal "name=value" form for a real
// Cookie: header must construct it themselves from Name and Value, or
// use Jar.HeaderFor and accept the redaction (the redacted form is what
// every log line in the project gets).
func (c *Cookie) String() string {
	if c == nil {
		return ""
	}
	if c.Name == "" {
		return redactedMarker
	}
	return c.Name + "=" + redactedMarker
}

// ValidateHost returns an error if the cookie's Domain attribute is
// on the embedded public-suffix list, or if the cookie violates one of
// the __Secure-/__Host- prefix invariants for the originating URL.
//
// Returns nil when the cookie is safe to set.
//
// Per docs/AGENTS.md rule 4, error messages do not echo the cookie
// value. They echo the name (not credential-equivalent) and the
// failing condition.
func (c *Cookie) ValidateHost(u *url.URL) error {
	if c == nil {
		return fmt.Errorf("cookiejar: nil cookie")
	}
	if u == nil {
		return fmt.Errorf("cookiejar: nil URL")
	}
	// Public-suffix rejection: cookies cannot be set on a TLD or a
	// public suffix like ".co.uk" — the latter would let any
	// subdomain's cookie shadow another's. RFC 6265 §5.3 step 6
	// calls this out explicitly.
	if c.Domain != "" && isPublicSuffix(c.Domain) {
		return fmt.Errorf("%w: domain %q is on the public-suffix list",
			ErrPublicSuffix, c.Domain)
	}
	// Prefix enforcement happens here too so the cookie struct alone
	// can answer the question "is this safe to set?". The jar
	// re-runs ValidateHost on the way in to get the typed error
	// back to callers.
	return validatePrefix(c, u)
}

// validatePrefix checks the __Secure- and __Host- invariants against
// the originating URL. The check is duplicated from Jar.SetCookies so
// callers that hold a Cookie (e.g. a persistence path) can answer the
// question without going through the jar.
func validatePrefix(c *Cookie, u *url.URL) error {
	name := c.Name
	secure := strings.HasPrefix(name, "__Secure-")
	host := strings.HasPrefix(name, "__Host-")
	if !secure && !host {
		return nil
	}
	if !c.Secure || u.Scheme != "https" {
		return fmt.Errorf("%w: %q requires Secure flag and https origin (got scheme=%q)",
			ErrInsecurePrefix, name, u.Scheme)
	}
	if host {
		if c.Domain != "" {
			return fmt.Errorf("%w: __Host- prefix forbids Domain attribute (got %q)",
				ErrHostPrefixOnSubpath, c.Domain)
		}
		if c.Path != "/" {
			return fmt.Errorf("%w: __Host- prefix requires Path=/ (got %q)",
				ErrHostPrefixOnSubpath, c.Path)
		}
	}
	return nil
}

// identity is the RFC 6265 §5.3 storage key: (name, domain, path).
// Two cookies sharing a name at distinct domains or paths are
// independent entries; collapsing them would lose auth cookies the way
// the Python source warns about (see notebooklm-py _auth/cookie_types
// docstring, "issue #369").
func (c *Cookie) identity() string {
	return c.Name + "\x00" + strings.ToLower(c.Domain) + "\x00" + c.Path
}

// isExpired reports whether the cookie has passed its Expires time.
// A zero Expires means a session cookie, which never expires by wall
// clock; MaxAge is handled separately in the jar's SetCookies path.
func (c *Cookie) isExpired(now time.Time) bool {
	if c.Expires.IsZero() {
		return false
	}
	return !c.Expires.After(now)
}
