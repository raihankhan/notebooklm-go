// Package storage: import-time normalizers for storage_state.json.
//
// The normalizers below run on every Unmarshal / Read call. They are
// deliberately pure (no I/O, no logging, no policy) so the test
// suite can exercise every branch with literal input/output pairs.
//
// Reference for every transformation:
//   - notebooklm._auth.cookie_semantics.normalize_cookie_expiry
//     — expires: -1 ⇄ nil
//   - notebooklm._auth.cookie_merge._VALID_SAME_SITE
//     — sameSite canonicalization to {Strict, Lax, None}
//   - notebooklm._auth.cookie_filter + rookiepy_row_to_storage_row
//     — expirationDate → expires (browser-export spelling)
//   - docs/05-auth.md "Round-trip requirements"
//     — origins dropped, in-band notebooklm.account namespace, legacy
//     context.json promotion
//
// Boundary: per docs/AGENTS.md rule 5 this file is part of the
// mode=internal package; it imports stdlib only (no JSON adapter
// call — wire.Marshal/Unmarshal live in storage.go).
package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// canonicalSameSites is the set of sameSite values the storage layer
// emits. Any input that is not in this set, in any case form, is
// dropped. The lower-case mapping is built once in init().
var canonicalSameSites = map[string]string{
	"strict": "Strict",
	"lax":    "Lax",
	"none":   "None",
}

// normalizeTopLevel reshapes the root JSON value into the canonical
// {cookies: [...], ...} document:
//
//   - A bare array at the top level is wrapped in {cookies: [...].
//     This is the shape most browser-export extensions produce, and
//     the Playwright loader never sees it (it expects a map), but
//     the Go loader must accept it because auth import-cookies is a
//     primary entry point (docs/05-auth.md, "Cookie JSON import").
//   - A top-level map has its "origins" key dropped — we never carry
//     localStorage / sessionStorage through the auth path.
//   - Cookie rows have their legacy spellings normalized (see
//     normalizeCookieRows, called from normalizeCookieAttributes).
//   - The notebooklm.account namespace is preserved as-is.
//   - The expirationDate spelling is rewritten to expires per row.
//
// The function is a value transformer, not a mutator: the input is
// walked once and a new tree is returned. Callers do not observe
// side-effects on their input.
func normalizeTopLevel(v any) any {
	switch t := v.(type) {
	case []any:
		// Bare cookie array — wrap.
		return map[string]any{
			"cookies": normalizeCookieRows(t),
		}
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if k == "origins" {
				// Dropped on import; we never carry
				// localStorage / sessionStorage
				// through the auth path. See
				// docs/05-auth.md "Round-trip
				// requirements".
				continue
			}
			out[k] = val
		}
		if raw, ok := out["cookies"]; ok {
			if rows, ok := raw.([]any); ok {
				out["cookies"] = normalizeCookieRows(rows)
			}
		}
		return out
	default:
		// A scalar at the top level is not a valid storage_state
		// document. Return it as-is so the typed decode produces
		// an empty Storage with a parse error later — better than
		// swallowing the document silently.
		return v
	}
}

// normalizeCookieRows rewrites per-cookie legacy spellings and
// in-place normalizations that must run on the any-tree before it is
// re-encoded:
//
//   - expirationDate → expires (the rookiepy / browser-export spelling)
//   - The expires / sameSite / hostOnly fields are normalized in
//     normalizeCookieAttributes once we have a typed []Cookie.
//
// Rows that are not map[string]any (e.g. a stray scalar inside the
// array) are dropped silently — Python's sanitize_cookie_entry does
// the same on the load path.
func normalizeCookieRows(rows []any) []any {
	out := make([]any, 0, len(rows))
	for _, row := range rows {
		m, ok := row.(map[string]any)
		if !ok {
			continue
		}
		// expirationDate (snake_case browser-export spelling) →
		// expires. Only rewrite when "expires" is absent so a row
		// that carries both keeps the canonical form. The Python
		// rookiepy_row_to_storage_row does the equivalent.
		if _, hasExpires := m["expires"]; !hasExpires {
			if alt, ok := m["expirationDate"]; ok {
				m["expires"] = alt
				delete(m, "expirationDate")
			}
		}
		out = append(out, m)
	}
	return out
}

// normalizeCookieAttributes is the typed post-decode pass.
//
// Three normalizations live here, in this order:
//
//  1. expires: -1 ⇄ nil. A JSON -1 (session sentinel) becomes a nil
//     *int64; a JSON nil (missing or null) becomes -1 on the next
//     rewrite; any other value passes through unchanged.
//  2. sameSite canonicalization. The four case forms (Strict, strict,
//     STRICT, sTrIcT) all collapse to "Strict"; "Lax" / "lax" / etc.
//     collapse to "Lax"; "None" / "none" collapse to "None". Anything
//     outside the set is dropped (matches Python _VALID_SAME_SITE
//     behavior — a stray "no_restriction" or "" becomes "no sameSite
//     attribute written").
//  3. hostOnly is a boolean pass-through: a true → true, a false /
//     missing → false. We never derive it from the Domain attribute
//     here; that derivation lives in the cookie jar layer
//     (RFC 6265 §5.3 step 5) because it requires knowing the URL
//     host. The storage layer's only job is to preserve what the
//     wire document said.
func normalizeCookieAttributes(rows []Cookie) []Cookie {
	for i := range rows {
		// expires — collapse -1 to nil so callers do not have to
		// distinguish "session cookie" from "no expires set".
		if rows[i].Expires != nil && *rows[i].Expires == sessionCookieSentinel {
			rows[i].Expires = nil
		}
		// sameSite — canonical case form.
		if rows[i].SameSite != "" {
			if canon, ok := canonicalSameSites[strings.ToLower(rows[i].SameSite)]; ok {
				rows[i].SameSite = canon
			} else {
				rows[i].SameSite = ""
			}
		}
	}
	return rows
}

// legacyContextPath is the sibling context.json next to a
// storage_state.json file. The Python source documents this as
// path.with_name("context.json"); the Go equivalent is filepath.Join
// with the directory and the fixed filename. Tests inject paths that
// already include the filename, so the helper is a one-liner that
// avoids pulling in path/filepath here — but path/filepath is stdlib
// and its use keeps the helper future-proof for callers that pass
// only a directory.
func legacyContextPath(storagePath string) string {
	dir := filepath.Dir(storagePath)
	return filepath.Join(dir, "context.json")
}

// readLegacyContext reads and validates the legacy context.json
// sibling, returning its "account" block when present.
//
// Best-effort: any I/O error, JSON parse error, or shape mismatch
// returns (nil, nil) so callers do not have to gate the promotion on
// a missing sibling. nilerr-style lint guards against returning nil
// for an error that is non-nil, but here we explicitly discard the
// error because the legacy sibling is best-effort by contract.
func readLegacyContext(path string) (map[string]any, error) {
	if path == "" {
		return nil, nil
	}
	data, readErr := os.ReadFile(path) // #nosec G304 -- operator-supplied, sibling of the profile root.
	if readErr != nil {
		return nil, nil //nolint:nilerr // best-effort; see docstring
	}
	var doc any
	if jsonErr := json.Unmarshal(data, &doc); jsonErr != nil {
		return nil, nil //nolint:nilerr // best-effort; see docstring
	}
	m, ok := doc.(map[string]any)
	if !ok {
		return nil, nil
	}
	acct, ok := m["account"].(map[string]any)
	if !ok {
		return nil, nil
	}
	return acct, nil
}

// mergeInBandAccount merges a legacy "account" block into the
// in-band notebooklm namespace. When the Storage already carries a
// meaningful in-band record (any field populated from the on-disk
// state), the existing record wins — the legacy context.json is a
// one-way promotion that never overwrites durable in-band metadata.
// A fresh or empty Storage gets the legacy record copied in.
//
// authuser is preserved as a number when present; email is preserved
// as a string when present. Missing fields fall through to their
// zero values (AuthUser=0, Email="") because the legacy record is
// informational — a malformed or partial entry is still better than
// the wrong account being routed (authuser=0 is the default profile
// index and is safe).
//
// The "any field populated" test is what makes
// TestLegacyContextExistingInBandWins pass: an in-band record with
// authuser=5 and email=foo must survive a legacy record that says
// authuser=2 and email=bar.
func mergeInBandAccount(existing *NotebookLM, legacy map[string]any) *NotebookLM {
	// Detect whether the existing record carries meaningful data.
	// An in-band record written by a previous Read or by the
	// profile-store migration carries a non-zero authuser OR a
	// non-empty email; a freshly-defaulted one (both zero)
	// represents "no in-band data was on disk" and the legacy
	// record wins by default.
	inBandPresent := existing != nil &&
		(existing.Account.AuthUser != 0 || existing.Account.Email != "")

	if inBandPresent {
		return existing
	}
	if existing == nil {
		existing = &NotebookLM{}
	}
	if v, ok := legacy["authuser"]; ok {
		switch n := v.(type) {
		case float64:
			existing.Account.AuthUser = int(n)
		case json.Number:
			if i, err := n.Int64(); err == nil {
				existing.Account.AuthUser = int(i)
			}
		}
	}
	if v, ok := legacy["email"].(string); ok {
		existing.Account.Email = v
	}
	return existing
}
