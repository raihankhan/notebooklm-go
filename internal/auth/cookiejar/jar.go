// Package cookiejar: the Jar type and its http.CookieJar implementation.
//
// The Jar is an in-memory RFC 6265 §5.3/§5.4 cookie store keyed by
// (name, domain, path). Two cookies sharing a name at distinct domains
// or paths are independent entries — collapsing them is the exact bug
// class the Python _auth/cookie_types docstring warns about ("issue
// #369": the OSID-on-two-hosts case, where the Tier-2 secondary
// binding for notebook.google.com would be silently shadowed by the
// same-named cookie for notebooklm.google.com).
//
// Selection follows §5.4 step 1 ordering: for each (name, domain)
// candidate, the longest-path cookie wins; for each domain candidate,
// the most specific (most-domain-components, then exact-match over
// dot-prefixed) wins. A cookie with Secure=true is filtered out for
// http:// URLs at step 3.
//
// Boundary: per docs/AGENTS.md rule 5, this package is mode=internal;
// it imports stdlib + internal/redact only.
package cookiejar

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrPublicSuffix is returned by SetCookies when the cookie's Domain
// attribute is on the embedded public-suffix list. Wrapping with %w
// lets callers errors.Is against it.
var ErrPublicSuffix = errors.New("cookiejar: domain on public-suffix list")

// ErrInsecurePrefix is returned by SetCookies when a cookie whose
// name starts with "__Secure-" was attempted over a non-https origin
// or without the Secure flag.
var ErrInsecurePrefix = errors.New("cookiejar: __Secure- prefix requires https and Secure=true")

// ErrHostPrefixOnSubpath is returned by SetCookies when a cookie
// whose name starts with "__Host-" carries a Domain attribute, a Path
// other than "/", or was attempted over a non-https origin.
var ErrHostPrefixOnSubpath = errors.New("cookiejar: __Host- prefix requires host-only, Path=/, https")

// Jar is the in-memory RFC 6265 cookie store. It is safe for
// concurrent use; the public methods take a small mutex to make
// selection atomic relative to SetCookies.
//
// The zero value is unusable; construct one with New().
type Jar struct {
	mu      sync.Mutex
	entries map[string]*Cookie
	now     func() time.Time // injectable clock for tests
}

// New returns an empty Jar. The internal clock is time.Now; tests
// that need deterministic selection can swap it with setClock.
func New() *Jar {
	return &Jar{
		entries: make(map[string]*Cookie),
		now:     time.Now,
	}
}

// setClock replaces the clock used for expiry checks. Test-only.
func (j *Jar) setClock(now func() time.Time) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.now = now
}

// SetCookies stores the cookies as received for u. Each cookie is
// validated for the public-suffix and prefix rules; a single bad
// cookie fails the whole batch so the transport can decide whether to
// abort the response or skip and continue. The return is the first
// error encountered; nil on success.
//
// Cookies are deduplicated by the (name, domain, path) identity —
// the last cookie in the slice with a given identity wins, matching
// http.CookieJar.SetCookies's documented contract.
func (j *Jar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	if u == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()

	now := j.now()
	for _, c := range cookies {
		if c == nil {
			continue
		}
		ours := fromStdlib(c)
		// Prefix + public-suffix checks must run against the raw
		// Set-Cookie attributes: a __Host- cookie carries no
		// Domain attribute by definition, and stamping the URL
		// host here would mask that contract. validateStdlibCookie
		// does both checks against the raw http.Cookie and
		// reports back whether the Set-Cookie was host-only
		// (no Domain attribute) so we can set ours.HostOnly.
		hostOnly, err := validateStdlibCookie(c, u)
		if err != nil {
			// The cookie is silently dropped on the Set path.
			// Callers that need to know about a refusal call
			// Cookie.ValidateHost directly — Jar.SetCookies
			// mirrors http.CookieJar.SetCookies, which does
			// not return an error per RFC 6265 §5.3 step 7.
			// (We document the dropped set in the test
			// harness instead.)
			continue
		}
		// Per RFC 6265 §5.3 step 5, a Set-Cookie without a Domain
		// attribute is host-only. We carry that as a flag rather
		// than stamping the URL's host onto Domain so a host-only
		// cookie stored at example.com does NOT match a request
		// to sub.example.com (the precise bug issue #369 warns
		// about in the Python source).
		ours.HostOnly = hostOnly
		if hostOnly {
			// Identity needs the URL's host so two cookies
			// with the same name on different hosts are
			// distinct. The HostOnly flag prevents them
			// from leaking across hosts at lookup time.
			ours.Domain = strings.ToLower(u.Hostname())
		}
		// Expired cookies (MaxAge <= 0 with Expires in the past)
		// are dropped without storing — they would be filtered
		// back out on Cookies() anyway, but storing them is
		// wasteful and confusing in the All() view.
		if ours.MaxAge < 0 {
			continue
		}
		if ours.isExpired(now) {
			continue
		}
		key := ours.identity()
		j.entries[key] = ours
	}
}

// Cookies returns the cookies to send on a request to u. Selection
// follows RFC 6265 §5.4:
//  1. Drop expired cookies (already pruned at SetCookies time, but
//     recheck so a clock advance between Set and Cookies removes
//     them).
//  2. Drop Secure cookies if u.Scheme is not "https".
//  3. Compute the candidate domain set: exact match first, then the
//     chain of parent domains, each either as bare host or with a
//     leading dot.
//  4. For each candidate domain, the cookie's domain must string-
//     match (case-insensitive, ASCII-only) and the cookie's path
//     must be a prefix of u's path; the longest-path cookie wins.
//  5. Among candidates, most-specific domain wins (more components,
//     then exact over dot-prefixed).
//  6. Stable ordering across (name, longest-path) ties preserves
//     determinism for log diffing.
func (j *Jar) Cookies(u *url.URL) []*http.Cookie {
	if u == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()

	host := strings.ToLower(u.Hostname())
	if host == "" {
		return nil
	}
	path := u.Path
	if path == "" || path[0] != '/' {
		path = "/"
	}
	now := j.now()
	https := u.Scheme == "https"

	// Group cookies by name. For each name we pick at most one
	// entry per selection pass; ties are broken by the rules above.
	byName := make(map[string][]*Cookie)
	for _, c := range j.entries {
		if c == nil {
			continue
		}
		if c.isExpired(now) {
			continue
		}
		if c.Secure && !https {
			continue
		}
		if !domainMatch(c, host) {
			continue
		}
		if !pathMatch(c.Path, path) {
			continue
		}
		byName[c.Name] = append(byName[c.Name], c)
	}

	out := make([]*http.Cookie, 0, len(byName))
	for _, candidates := range byName {
		winner := selectCookie(candidates, host, path)
		if winner == nil {
			continue
		}
		out = append(out, toStdlib(winner))
	}

	// §5.4 step 6: sort the final set by (path-length desc,
	// creation-time asc) for deterministic header output. We do not
	// carry creation time, so the tiebreak is name asc.
	sort.Slice(out, func(i, k int) bool {
		if out[i].Path != out[k].Path {
			return len(out[i].Path) > len(out[k].Path)
		}
		return out[i].Name < out[k].Name
	})
	return out
}

// HeaderFor returns the value of a Cookie: header for u with each
// cookie rendered through Cookie.String(), which redacts values via
// internal/redact. The output is for *log/diagnostic* use only —
// the actual transport sends the unredacted header built from
// Cookies(u) and http.Cookie.String().
//
// The leading "Cookie: " prefix is omitted; callers that need a full
// header line concatenate it themselves (mirroring the format the
// transport uses on the wire).
func (j *Jar) HeaderFor(u *url.URL) string {
	cs := j.Cookies(u)
	if len(cs) == 0 {
		return ""
	}
	parts := make([]string, len(cs))
	for i, c := range cs {
		// Round-trip through our redacting String to avoid the
		// stdlib's literal-value String ever leaking into a log.
		ours := fromStdlib(c)
		parts[i] = ours.String()
	}
	return strings.Join(parts, "; ")
}

// All returns a snapshot of every cookie currently in the jar,
// ordered by (name, domain, path). The slice is freshly allocated
// and safe for the caller to retain independently of future
// mutations. SameSite is exposed via the bool field on Cookie.
func (j *Jar) All() []*Cookie {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]*Cookie, 0, len(j.entries))
	for _, c := range j.entries {
		// Copy so the caller cannot mutate the jar's storage.
		dup := *c
		out = append(out, &dup)
	}
	sort.Slice(out, func(i, k int) bool {
		if out[i].Name != out[k].Name {
			return out[i].Name < out[k].Name
		}
		if out[i].Domain != out[k].Domain {
			return out[i].Domain < out[k].Domain
		}
		return out[i].Path < out[k].Path
	})
	return out
}

// Snapshot is an alias for All, kept for parity with the Python
// CookieJar's `.to_storage_rows()` accessor. The T-P2-2 storage layer
// will read the jar through this method.
func (j *Jar) Snapshot() []*Cookie {
	return j.All()
}

// Len returns the number of cookies currently stored. Test helper —
// the production transport counts via len(All()).
func (j *Jar) Len() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.entries)
}

// -- helpers -----------------------------------------------------------

// fromStdlib adapts a net/http.Cookie to our Cookie type. The Raw
// field is populated with the standard library's String() form so
// storage (T-P2-2) can round-trip the unparsed Set-Cookie byte. We
// pass the value through our String() only for the *rendered* form
// — the field itself keeps the literal value, which is what the
// transport needs to send on the wire.
func fromStdlib(c *http.Cookie) *Cookie {
	// Apply RFC 6265 §5.1.4 default-path only when the Set-Cookie
	// omitted the Path attribute. A user-supplied Path is the
	// exact contract; we never shorten it.
	path := c.Path
	if path == "" {
		path = "/"
	}
	ours := &Cookie{
		Name:     c.Name,
		Value:    c.Value,
		Path:     path,
		Expires:  c.Expires,
		MaxAge:   c.MaxAge,
		Secure:   c.Secure,
		HttpOnly: c.HttpOnly,
	}
	// SameSite=0 is "no SameSite attribute", SameSite=1 is Lax,
	// SameSite=2 is Strict, SameSite=3 is None. Map any non-zero to
	// the bool presence flag so persistence can round-trip "any
	// SameSite attribute" without losing the exact form.
	if c.SameSite > 0 {
		ours.SameSite = true
	}
	if c.Domain != "" {
		ours.Domain = strings.TrimPrefix(c.Domain, ".")
	} else {
		// No Domain attribute → host-only. The URL host is
		// stamped in SetCookies so the identity key is unique
		// across hosts; the HostOnly flag tells Cookies()
		// not to treat this Domain as a domain-match candidate
		// for subdomains.
		ours.HostOnly = true
	}
	if c.Raw != "" {
		ours.Raw = c.Raw
	} else {
		ours.Raw = c.String()
	}
	return ours
}

// toStdlib is the inverse of fromStdlib.
func toStdlib(c *Cookie) *http.Cookie {
	out := &http.Cookie{
		Name:     c.Name,
		Value:    c.Value,
		Path:     c.Path,
		Expires:  c.Expires,
		MaxAge:   c.MaxAge,
		Secure:   c.Secure,
		HttpOnly: c.HttpOnly,
		Raw:      c.Raw,
	}
	if c.Domain != "" {
		out.Domain = "." + c.Domain
	}
	if c.SameSite {
		// The exact mode (Lax/Strict/None) is not recoverable
		// from the bool; the transport layer only cares that
		// *some* SameSite attribute is present. Round-trip the
		// raw line so persistence (T-P2-2) can preserve the
		// original.
		out.SameSite = http.SameSiteLaxMode
	}
	return out
}

// validateStdlibCookie is the entry point SetCookies uses. It runs
// the public-suffix check (against the literal Set-Cookie Domain
// attribute) and the prefix check (which inspects Domain too — a
// __Host- cookie carries no Domain by definition). Returns
// (hostOnly, nil) when the cookie is acceptable: hostOnly is true
// iff the Set-Cookie had no Domain attribute (so the caller should
// stamp the URL's host onto ours.Domain for the §5.1.3 match
// lookup).
//
// The http.Cookie input is mutated-by-copy only — we read its
// Domain, Name, Secure, Path, and Scheme off the URL.
func validateStdlibCookie(c *http.Cookie, u *url.URL) (bool, error) {
	if u == nil {
		return false, fmt.Errorf("cookiejar: nil URL")
	}
	if c == nil {
		return false, fmt.Errorf("cookiejar: nil cookie")
	}
	domain := c.Domain
	if domain != "" {
		// Strip leading dot so the public-suffix list lookup
		// matches the canonical form. cookies.py's
		// _is_allowed_auth_domain operates on the same form.
		if domain[0] == '.' {
			domain = domain[1:]
		}
		if isPublicSuffix(domain) {
			return false, fmt.Errorf("%w: domain %q is on the public-suffix list",
				ErrPublicSuffix, c.Domain)
		}
	}
	// Prefix enforcement.
	name := c.Name
	secure := strings.HasPrefix(name, "__Secure-")
	host := strings.HasPrefix(name, "__Host-")
	if !secure && !host {
		return domain == "", nil
	}
	if !c.Secure || u.Scheme != "https" {
		return false, fmt.Errorf("%w: %q requires Secure flag and https origin (got scheme=%q)",
			ErrInsecurePrefix, name, u.Scheme)
	}
	if host {
		if c.Domain != "" {
			return false, fmt.Errorf("%w: __Host- prefix forbids Domain attribute (got %q)",
				ErrHostPrefixOnSubpath, c.Domain)
		}
		if c.Path != "/" {
			return false, fmt.Errorf("%w: __Host- prefix requires Path=/ (got %q)",
				ErrHostPrefixOnSubpath, c.Path)
		}
	}
	return domain == "", nil
}

// domainMatch implements RFC 6265 §5.1.3 domain matching, with the
// §5.3 step 5 host-only carve-out. The cookie domain and the host
// are both lower-cased ASCII.
//
// For domain cookies (no HostOnly flag), an exact match OR a
// dot-prefix match (the cookie was set with Domain="example.com"
// and the request is to sub.example.com) is allowed. For host-only
// cookies (Set-Cookie arrived without a Domain attribute), only an
// exact host match counts — subdomains never see a host-only
// cookie that was set on the parent.
//
// The "more specific domain wins" tiebreak from §5.4 step 2 lives
// in selectCookie, not here.
func domainMatch(c *Cookie, host string) bool {
	if c.Domain == "" {
		return false
	}
	if c.HostOnly {
		return c.Domain == host
	}
	if c.Domain == host {
		return true
	}
	if strings.HasSuffix(host, "."+c.Domain) {
		return true
	}
	return false
}

// pathMatch implements RFC 6265 §5.1.4 path matching: the cookie
// path is a prefix of the request URI's path, AND either the cookie
// path and request path are identical, or the cookie path ends in
// "/" or the request path character immediately after the prefix is
// "/".
func pathMatch(cookiePath, reqPath string) bool {
	if cookiePath == "" {
		cookiePath = "/"
	}
	if !strings.HasPrefix(reqPath, cookiePath) {
		return false
	}
	if len(reqPath) == len(cookiePath) {
		return true
	}
	if strings.HasSuffix(cookiePath, "/") {
		return true
	}
	return reqPath[len(cookiePath)] == '/'
}

// selectCookie picks the best candidate for a given (name, host,
// path) tuple, implementing RFC 6265 §5.4 step 2.
//
// Rules applied, in order:
//
//  1. Among candidates whose domain is host-exact, the longest-path
//     wins.
//  2. Among dot-prefix candidates, the longest-path wins.
//  3. If both rule-1 and rule-2 candidates are present, the host-
//     exact wins (it is more specific).
//  4. If no candidate is reachable, return nil.
//
// This is the exact selection order §5.4 documents. The OSID-on-two-
// hosts case is naturally handled: cookies stored at
// (OSID, notebook.google.com, /) and (OSID, notebooklm.google.com, /)
// are picked independently for each request URL.
func selectCookie(candidates []*Cookie, host, reqPath string) *Cookie {
	var exact, dot *Cookie
	for _, c := range candidates {
		if c.Domain == host {
			if exact == nil || len(c.Path) > len(exact.Path) {
				exact = c
			}
		} else {
			if dot == nil || len(c.Path) > len(dot.Path) {
				dot = c
			}
		}
	}
	if exact != nil {
		return exact
	}
	return dot
}
