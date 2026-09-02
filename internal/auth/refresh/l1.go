// Package refresh: the L1 ladder rung — reload profile, seed the
// cookie jar, return Tokens.
//
// Phase 4 wires L1 only (T-P4-2). L1 is the rung the docs describe as
// "Token refresh" but in Phase 4 we implement the smaller piece:
// reload the on-disk storage_state.json into the in-memory cookie
// jar and surface whatever account identity the in-band namespace
// recorded. The network-fetched CSRF/SessionID arrive in Sprint 3
// (T-P4-3 / T-P4-4) when L2.5 / L3 / L4 land.
//
// The contract:
//
//	tokens, err := refresh.ReloadL1(ctx, store, jar, name)
//
// store is any internal/auth/profile.Reader (the production
// ladder wires DiskStore; tests use FakeStore). jar is the
// in-memory cookiejar.Jar the transport will read from. name is the
// validated profile directory name. On success ReloadL1 returns
// Tokens populated from the disk, and the jar is seeded with the
// cookies via Jar.SetCookies for one synthetic https URL per
// cookie. On failure ReloadL1 returns an error; the jar is left
// untouched.
//
// Boundary: per docs/AGENTS.md rule 5, this file is part of the
// mode=internal package; it imports stdlib + internal/auth/cookiejar
// (the Jar type) + internal/auth/profile (the Profile value) only.
package refresh

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/raihankhan/notebooklm-go/internal/auth/cookiejar"
	"github.com/raihankhan/notebooklm-go/internal/auth/profile"
)

// ReloadL1 reads the profile from store, seeds jar with the profile's
// cookies, and returns a Tokens value reflecting the in-band account
// metadata.
//
// The function is named ReloadL1 rather than L1 because the package
// also exports the L1 Level constant; matching names would shadow
// the constant across the package. Callers that go through the
// Ladder interface use Step(ctx, refresh.L1) and never see this
// rename.
//
// Errors:
//   - context.Canceled / context.DeadlineExceeded — ctx is done before
//     or during the reload.
//   - profile.ErrProfileNotFound — the named profile has no
//     storage_state.json on disk.
//   - any wrapped IO / parse error from the underlying Reader.
//   - an invalid profile.Name.
//
// On any error the jar is left untouched (we only SetCookies after
// the read succeeds) so a caller that prefers to bail rather than
// clear-and-reset can rely on ReloadL1 being transactional.
//
// The jar is seeded with each cookie via a synthetic https URL per
// cookie's (Domain, Path). Host-only cookies (no Domain attribute)
// are skipped — profile.Read tolerates the case but seeding the
// jar requires a URL. Two cookies sharing a name at distinct
// domains stay distinct entries in the jar's identity key
// (RFC 6265 §5.3; issue #369).
func ReloadL1(ctx context.Context, store profile.Reader, jar *cookiejar.Jar, name profile.Name) (Tokens, error) {
	if store == nil {
		return Tokens{}, fmt.Errorf("refresh: L1 store is nil")
	}
	if jar == nil {
		return Tokens{}, fmt.Errorf("refresh: L1 jar is nil")
	}
	if err := ctx.Err(); err != nil {
		return Tokens{}, err
	}
	if _, err := profile.NewName(string(name)); err != nil {
		return Tokens{}, fmt.Errorf("refresh: L1: %w", err)
	}

	p, err := store.Read(ctx, name)
	if err != nil {
		return Tokens{}, err
	}

	seedJar(jar, p.Cookies)

	views := make([]CookieView, 0, len(p.Cookies))
	for _, c := range p.Cookies {
		views = append(views, CookieView{Name: c.Name, Domain: c.Domain, Path: c.Path})
	}

	return Tokens{
		Cookies:      views,
		AuthUser:     p.Account.AuthUser,
		AccountEmail: p.Account.Email,
		Backend:      backendOfProfile(p.Backend),
		FetchedAt:    time.Now().UTC(),
	}, nil
}

// seedJar writes the profile's cookies into the jar. The synthetic
// URL per cookie is https://<domain>/<path>, derived from the
// cookie's own Domain / Path attributes so a host-only cookie and
// a domain cookie that happen to share a name on the same host do
// not collide in the jar's identity key.
//
// A cookie that has no Domain attribute is skipped: profile.Read
// tolerates the case, but a jar seeding requires a URL. The ladder
// is robust to this because the underlying storage package rejects
// host-only cookies with no Domain in production — the skip is
// belt-and-braces.
func seedJar(jar *cookiejar.Jar, cookies []profile.Cookie) {
	for _, c := range cookies {
		host := strings.TrimPrefix(c.Domain, ".")
		if host == "" {
			continue
		}
		u := &url.URL{
			Scheme: "https",
			Host:   host,
			Path:   c.Path,
		}
		if u.Path == "" {
			u.Path = "/"
		}
		jar.SetCookies(u, []*http.Cookie{toStdlib(c, host)})
	}
}

// toStdlib lifts a profile.Cookie into a *http.Cookie that the
// jar accepts. The round-trip is lossless on the standard fields;
// the HostOnly flag maps onto the http.Cookie's Domain attribute
// ("cookie set without a Domain attribute is host-only per RFC
// 6265 §5.3 step 5").
func toStdlib(c profile.Cookie, host string) *http.Cookie {
	out := &http.Cookie{
		Name:     c.Name,
		Value:    c.Value,
		Path:     c.Path,
		Secure:   c.Secure,
		HttpOnly: c.HTTPOnly,
	}
	if !c.Expires.IsZero() {
		out.Expires = c.Expires
	}
	if !c.HostOnly {
		out.Domain = host
	}
	switch c.SameSite {
	case "Strict":
		out.SameSite = http.SameSiteStrictMode
	case "Lax":
		out.SameSite = http.SameSiteLaxMode
	case "None":
		out.SameSite = http.SameSiteNoneMode
	}
	return out
}

// backendOfProfile lifts a profile.Backend into a refresh.Backend.
// Keeping the two types distinct lets the profile package evolve
// (a future "BrowserReauth" backend, say) without dragging the
// refresh package along.
func backendOfProfile(b profile.Backend) Backend {
	switch b {
	case profile.BackendStorageFile:
		return BackendStorageFile
	case profile.BackendInlineEnv:
		return BackendInlineEnv
	default:
		return BackendUnknown
	}
}

// DefaultLadder is the Ladder implementation Phase 4 ships. It wires
// L1 and stubs the other four levels with ErrLadderLevelNotImplemented.
//
// Sprint 3 extends DefaultLadder with L2.0 (disk-sample under
// bounded attempts), L2.5 (refresh-cmd subprocess), L3 (headless
// re-auth), and L4 (master-token re-mint).
type DefaultLadder struct {
	// Store is the Reader the L1 rung reads. Required.
	Store profile.Reader
	// Jar is the cookie jar the L1 rung seeds. Required.
	Jar *cookiejar.Jar
	// Name is the profile name the L1 rung reloads. Required.
	Name profile.Name
	// Now is the wall-clock supplier; tests inject a fixed
	// clock. Nil falls back to time.Now.
	Now func() time.Time
}

// Step is the Ladder implementation. Phase-4 behavior:
//
//   - L1   — run the L1 reload.
//   - L2_0 / L2_5 / L3 / L4 — return (zero, false,
//     ErrLadderLevelNotImplemented) wrapped with the level name.
func (d *DefaultLadder) Step(ctx context.Context, level Level) (Tokens, bool, error) {
	switch level {
	case L1:
		t, err := ReloadL1(ctx, d.Store, d.Jar, d.Name)
		if err != nil {
			return Tokens{}, false, err
		}
		return t, true, nil
	case L2_0, L2_5, L3, L4:
		return Tokens{}, false, fmt.Errorf("%w: %s", ErrLadderLevelNotImplemented, level)
	default:
		return Tokens{}, false, fmt.Errorf("%w: %s", ErrLadderLevelNotImplemented, level)
	}
}
