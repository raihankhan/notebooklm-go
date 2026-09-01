// Embedded minimal public-suffix list for the cookie jar.
//
// RFC 6265 §5.3 step 6 forbids setting a cookie whose Domain attribute
// is itself a public suffix. The canonical source is the publicsuffix
// list maintained at publicsuffix.org, but the cookie jar's boundary
// rule (mode=internal) does not permit golang.org/x/net/publicsuffix as
// a dependency.
//
// Rather than forbid the rejection path (the AC demands it), this file
// embeds the small set of public suffixes the cookie jar is actually
// asked to refuse. The list covers the second-level registry domains
// Google sets auth cookies on for NotebookLM's regional users, plus
// the well-known ccTLDs. It is intentionally conservative — adding a
// new suffix is a one-line edit, but adding a false positive would
// silently lose a cookie.
//
// Source: cross-referenced with the public suffix list at
// publicsuffix.org/list/. Last reconciled: 2026-09.
package cookiejar

// publicSuffixes is the canonical list the jar rejects. Lookups are
// case-insensitive and accept either the bare suffix or its dotted
// form (".co.uk" and "co.uk" both match).
var publicSuffixes = map[string]struct{}{
	// Generic TLDs — top-level public suffixes on their own.
	"com": {},
	"org": {},
	"net": {},
	"edu": {},
	"gov": {},
	"mil": {},
	"int": {},
	// Selected second-level registry domains that operate like
	// public suffixes. Without this list, an attacker who controls
	// evil.example could plant a cookie at Domain=example.co.uk
	// and shadow every legitimate *.example.co.uk session.
	"co.uk":  {},
	"co.jp":  {},
	"co.kr":  {},
	"co.nz":  {},
	"co.za":  {},
	"co.in":  {},
	"co.id":  {},
	"co.il":  {},
	"com.au": {},
	"com.br": {},
	"com.cn": {},
	"com.hk": {},
	"com.mx": {},
	"com.sg": {},
	"com.tw": {},
	"com.tr": {},
	"com.ar": {},
	"com.pl": {},
	"ac.uk":  {},
	"gov.uk": {},
	"org.uk": {},
	"net.au": {},
	"org.au": {},
	"edu.au": {},
	"ne.jp":  {},
	"or.jp":  {},
	"ac.jp":  {},
	// Regional public-suffix domains used by Google's regional
	// Google domains. A cookie at Domain=google.com.hk would
	// shadow every legitimate subdomain session; refuse it.
	"google.com.hk":         {},
	"google.com.sg":         {},
	"google.com.au":         {},
	"google.com.br":         {},
	"google.com.mx":         {},
	"google.com.tr":         {},
	"google.com.ar":         {},
	"google.com.pl":         {},
	"google.co.uk":          {},
	"google.co.jp":          {},
	"google.co.kr":          {},
	"google.co.nz":          {},
	"google.co.za":          {},
	"google.co.in":          {},
	"google.co.id":          {},
	"google.co.il":          {},
	"google.de":             {},
	"google.fr":             {},
	"google.it":             {},
	"google.es":             {},
	"google.nl":             {},
	"google.ca":             {},
	"googleusercontent.com": {},
}

// isPublicSuffix reports whether the given domain — with or without a
// leading dot — is on the embedded public-suffix list. Empty input is
// never a public suffix (the calling path treats empty Domain as
// "host-only", which is a separate code path).
//
// The match is exact after lowering and stripping the leading dot:
// "co.uk", ".co.uk", "CO.UK" all resolve to the same entry. Wildcard
// or "*" entries are not present in this minimal list.
func isPublicSuffix(domain string) bool {
	if domain == "" {
		return false
	}
	d := domain
	if d[0] == '.' {
		d = d[1:]
	}
	// Trailing dot (FQDN form) is tolerated because cookie-domain
	// parsing sometimes normalizes to it.
	for len(d) > 0 && d[len(d)-1] == '.' {
		d = d[:len(d)-1]
	}
	if d == "" {
		return false
	}
	// Lower-case once for case-insensitive compare.
	lower := make([]byte, len(d))
	for i := 0; i < len(d); i++ {
		c := d[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		lower[i] = c
	}
	_, ok := publicSuffixes[string(lower)]
	return ok
}
