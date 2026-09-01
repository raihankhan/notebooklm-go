// Package storage implements lossless read and write of the Playwright
// storage_state.json document used by every authenticated transport call,
// extended with the in-band notebooklm.account namespace.
//
// The on-disk shape is:
//
//	{
//	  "cookies": [
//	    {
//	      "name": "SID",
//	      "value": "...",
//	      "domain": ".google.com",
//	      "path": "/",
//	      "expires": 1798761234,        // -1 means session cookie; nil -> -1 on rewrite
//	      "httpOnly": true,
//	      "secure": true,
//	      "sameSite": "None"            // canonical case form: Strict | Lax | None
//	    }
//	  ],
//	  "origins": [],                    // dropped on read; we never write it
//	  "notebooklm": {
//	    "account": { "authuser": 0, "email": "you@example.com" }
//	  }
//	}
//
// Round-trip semantics (docs/05-auth.md, AGENTS.md rule 4):
//
//   - expires: -1 (Playwright session sentinel) round-trips through Go's
//     *int64: read as nil, written back as -1. Any other value is preserved
//     exactly. A missing expires is treated as -1 (session).
//   - sameSite survives every case form (strict / Strict / STRICT collapse to
//     the canonical "Strict"; an unrecognized form is dropped, mirroring
//     notebooklm-py _auth/cookie_merge._VALID_SAME_SITE).
//   - origins is dropped on read and never written.
//   - Bare cookie arrays (no "cookies" key wrapper) are reshaped into the
//     documented form on read.
//   - expirationDate (the rookiepy/browser-export spelling) is rewritten to
//     expires on read.
//   - The notebooklm.account namespace carries the in-band account metadata
//     and is preserved losslessly across reads and writes. A legacy
//     context.json file is read alongside storage_state.json and its
//     "account" key is promoted into the in-band namespace; subsequent
//     writes carry it in-band, never in context.json.
//
// Boundary: per docs/AGENTS.md rule 5 this package is mode=internal; it
// imports stdlib + internal/web/wire (the single JSON adapter) and nothing
// else. No third-party dependencies.
package storage

import (
	"errors"
	"fmt"
	"os"

	"github.com/raihankhan/notebooklm-go/internal/web/wire"
)

// sessionCookieSentinel is the wire-level value that means "session
// cookie, never expires". Per the Python reference
// (notebooklm._auth.cookie_semantics.normalize_cookie_expiry) this is
// the EXACT integer -1; -1.0 is a dated cookie and survives as
// itself. Our *int64 field collapses both to nil on read so a caller
// does not have to distinguish them; on rewrite, nil re-emits -1.
const sessionCookieSentinel = -1

// Cookie mirrors a single Playwright storage_state cookie row.
//
// Expires is a pointer so the session-cookie sentinel (-1 in the wire
// format) round-trips as nil: the read path collapses -1 to nil, the
// write path emits nil back as -1, and a fresh value survives as its
// integer self. Compare with the Python
// notebooklm._auth.cookie_semantics.normalize_cookie_expiry reference.
//
// HostOnly is preserved across the boundary because RFC 6265 §5.3 step 5
// distinguishes a host-only cookie (no Domain attribute) from a domain
// cookie set at the exact host — the two are not interchangeable, and
// the document survives the round-trip.
type Cookie struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Domain   string `json:"domain,omitempty"`
	Path     string `json:"path,omitempty"`
	Expires  *int64 `json:"expires"`
	HTTPOnly bool   `json:"httpOnly,omitempty"`
	Secure   bool   `json:"secure,omitempty"`
	SameSite string `json:"sameSite,omitempty"`
	HostOnly bool   `json:"hostOnly,omitempty"`
}

// storageView is the on-the-wire view of a Storage. The Cookies
// field is converted from []Cookie to []cookieWire before marshaling
// so the session-sentinel contract (nil Expires → -1) holds without
// a custom Cookie.MarshalJSON. The conversion is a pure field
// shuffle; see toWireView / fromWireView.
//
// Why an intermediate view rather than a custom marshaller? Because
// a Cookie.MarshalJSON method would have to call encoding/json
// directly to produce its output, violating docs/AGENTS.md rule 3
// (the module has exactly one JSON adapter: internal/web/wire).
// Routing the storage view through wire.Marshal preserves the rule
// end-to-end.
type storageView struct {
	Cookies    []cookieWire `json:"cookies"`
	NotebookLM *NotebookLM  `json:"notebooklm,omitempty"`
}

// cookieWire is the on-the-wire cookie shape. Expires is a plain
// int64 because nil has already been collapsed to the -1 sentinel by
// toWireView; the field is required (no `omitempty`) so a session
// cookie is always represented as the explicit integer -1.
type cookieWire struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Domain   string `json:"domain,omitempty"`
	Path     string `json:"path,omitempty"`
	Expires  int64  `json:"expires"`
	HTTPOnly bool   `json:"httpOnly,omitempty"`
	Secure   bool   `json:"secure,omitempty"`
	SameSite string `json:"sameSite,omitempty"`
	HostOnly bool   `json:"hostOnly,omitempty"`
}

// toWireView converts s into its on-the-wire representation. The
// session-sentinel collapse runs here: every cookie with a nil
// Expires is emitted as the integer -1. The conversion is the only
// place a -1 can appear on the wire, so any future "expires=0 means
// session" change touches exactly one file.
func toWireView(s Storage) storageView {
	wires := make([]cookieWire, len(s.Cookies))
	for i, c := range s.Cookies {
		w := cookieWire{
			Name:     c.Name,
			Value:    c.Value,
			Domain:   c.Domain,
			Path:     c.Path,
			HTTPOnly: c.HTTPOnly,
			Secure:   c.Secure,
			SameSite: c.SameSite,
			HostOnly: c.HostOnly,
		}
		if c.Expires != nil {
			w.Expires = *c.Expires
		} else {
			w.Expires = sessionCookieSentinel
		}
		wires[i] = w
	}
	return storageView{Cookies: wires, NotebookLM: s.NotebookLM}
}

// fromWireView is the inverse of toWireView. The Expand loop sets
// each cookie's Expires to a pointer-to-int64; the
// normalizeCookieAttributes pass collapses the -1 sentinel back to
// nil so callers do not have to distinguish "session" from "absent".
func fromWireView(v storageView) Storage {
	s := Storage{NotebookLM: v.NotebookLM}
	if len(v.Cookies) > 0 {
		s.Cookies = make([]Cookie, len(v.Cookies))
		for i, w := range v.Cookies {
			exp := w.Expires
			s.Cookies[i] = Cookie{
				Name:     w.Name,
				Value:    w.Value,
				Domain:   w.Domain,
				Path:     w.Path,
				Expires:  &exp,
				HTTPOnly: w.HTTPOnly,
				Secure:   w.Secure,
				SameSite: w.SameSite,
				HostOnly: w.HostOnly,
			}
		}
	}
	return s
}

// Account is the in-band notebooklm.account namespace.
//
// This is the canonical location for the authuser index and account email.
// Legacy context.json files hold the same shape under the top-level
// "account" key; Read promotes that into the in-band namespace on import
// and Write carries it back in-band on subsequent commits.
type Account struct {
	AuthUser int    `json:"authuser"`
	Email    string `json:"email,omitempty"`
}

// NotebookLM is the optional in-band namespace.
//
// The "account" field is the only key currently used; the rest of the
// namespace is reserved for future schema additions. Per
// docs/05-auth.md, this is the durable home for any notebooklm-go
// metadata that should survive a round-trip with the Python CLI.
type NotebookLM struct {
	Account Account `json:"account"`
}

// Storage is the in-memory representation of a storage_state.json
// document after normalization.
//
// The zero value is NOT usable: Cookies is nil until Read or Unmarshal
// populated it, and a Write call with zero Cookies writes an empty array
// (the canonical empty-storage_state shape that the Python loader accepts
// without complaint).
type Storage struct {
	Cookies    []Cookie    `json:"cookies"`
	NotebookLM *NotebookLM `json:"notebooklm,omitempty"`
}

// ErrEmptyPath is returned by Read when the input path is the empty
// string. Read treats this as a programming error rather than a file
// system error because a profile path must always be derived from a
// non-empty home directory.
var ErrEmptyPath = errors.New("storage: empty path")

// Read loads a storage_state.json document from path, applies the
// import-time normalizations (see normalizers.go), and returns the
// resulting Storage. The legacy context.json sibling (path with the
// filename replaced by "context.json") is read and its "account" key
// promoted into the in-band notebooklm.account namespace when present.
//
// Read is lossless on the wire-level shape: every attribute present in
// the document survives, only the canonical encodings (-1 ⇄ nil,
// "strict" ⇄ "Strict") are applied.
//
// The returned Storage is safe for the caller to retain and mutate
// independently of subsequent Read calls.
//
// File-system errors propagate. A missing file is reported with the
// wrapped os.PathError so callers can errors.Is(err, fs.ErrNotExist).
func Read(path string) (Storage, error) {
	if path == "" {
		return Storage{}, ErrEmptyPath
	}
	data, err := os.ReadFile(path) // #nosec G304 -- operator-supplied, profile root is the trust boundary.
	if err != nil {
		return Storage{}, err
	}
	s, err := Unmarshal(data)
	if err != nil {
		return Storage{}, fmt.Errorf("storage: parse %s: %w", path, err)
	}
	// Sibling legacy context.json — best-effort. A missing file or
	// unreadable JSON is not an error; an absent legacy record simply
	// means there is nothing to promote.
	legacyPath := legacyContextPath(path)
	if legacy, lerr := readLegacyContext(legacyPath); lerr == nil && legacy != nil {
		s.NotebookLM = mergeInBandAccount(s.NotebookLM, legacy)
	}
	return s, nil
}

// Write writes s as a storage_state.json document to path using the
// project-wide JSON encoder (internal/web/wire.Marshal). The same byte
// format is used for every other write in the module, so a file
// produced here is byte-for-byte compatible with the Python CLI's
// json.dumps output (HTML escaping disabled, no trailing newline,
// sorted keys).
//
// Write is intentionally a thin shim over Marshal + os.WriteFile. The
// atomic-write + .bak-rollback + flock discipline lives in
// internal/atomicio (T-P2-4); higher layers compose the two. This
// function exists so callers that need a direct write (tests, the
// T-P2-2 round-trip table) have a one-call API; production writers
// route through atomicio.WriteFile.
func Write(path string, s Storage) error {
	if path == "" {
		return ErrEmptyPath
	}
	data, err := Marshal(s)
	if err != nil {
		return fmt.Errorf("storage: marshal: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	return nil
}

// Marshal encodes s as the canonical storage_state.json wire bytes.
// Cookies are emitted in slice order (the in-memory order Read produced
// them); the in-band notebooklm namespace is preserved when present.
// The "origins" key is never written — the Python CLI accepts its
// absence and the Go loader does not need it.
//
// The implementation routes via the storageView intermediate (see
// toWireView) so the session-sentinel encoding (nil Expires → -1)
// runs without breaking docs/AGENTS.md rule 3 (the module has
// exactly one JSON adapter: internal/web/wire).
func Marshal(s Storage) ([]byte, error) {
	return wire.Marshal(toWireView(s))
}

// Unmarshal decodes raw storage_state.json bytes into a Storage,
// applying every wire-level normalization documented in
// normalizers.go. The returned Storage is populated regardless of
// whether the input document carried a "cookies" key (a bare array
// input is reshaped), a "notebooklm" key (the namespace is optional),
// or the legacy "expirationDate" spelling of expires.
//
// The implementation decodes into the generic any tree once so it can
// reshape bare-array input before re-decoding into the typed Storage
// shape. json.Number preserves large-integer ids that float64 would
// mangle. The wire form is then decoded via the storageView
// intermediate (see fromWireView) so the session sentinel can be
// inverted by normalizeCookieAttributes.
func Unmarshal(data []byte) (Storage, error) {
	// Decode into a generic tree. wire.Unmarshal is the project's
	// single JSON adapter; its UseNumber() contract preserves
	// large-integer ids that float64 would mangle.
	var probe any
	if err := wire.Unmarshal(data, &probe); err != nil {
		return Storage{}, err
	}
	normalized := normalizeTopLevel(probe)
	// Re-marshal + re-decode through the typed shape. Re-marshaling
	// is cheaper than a hand-rolled copy and keeps normalizers.go
	// pure (no side-effecting mutations of nested maps).
	encoded, err := wire.Marshal(normalized)
	if err != nil {
		return Storage{}, fmt.Errorf("storage: re-marshal normalized: %w", err)
	}
	var view storageView
	if err := wire.Unmarshal(encoded, &view); err != nil {
		return Storage{}, fmt.Errorf("storage: typed decode: %w", err)
	}
	s := fromWireView(view)
	s.Cookies = normalizeCookieAttributes(s.Cookies)
	return s, nil
}
