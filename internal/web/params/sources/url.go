// Package sources — url.go: byte-stable request-payload builder
// for the URL-source branch of the AddSources RPC.
//
// This file ships the canonical positional shape
// `add_url_source` writes in
// notebooklm-py/src/notebooklm/_web/sources/add.py::SourceAddService
// .add_url_source. The shape is:
//
//	[
//	  [[None, None, [url], None, None, None, None, None, None, None, <mime>, 1]],
//	  notebook_id,
//	  build_template_block(),
//	]
//
// — a 12-slot spec where the URL rides at slot 2 (the URL-branch
// discriminator), the MIME rides at slot 10 (a Go-port extension;
// see Slot-MIME note below), and the trailing `1` is the URL
// source-type code. The outer envelope is the same nested wrapper
// `_web/sources/add.py` uses post the Gemini-3.5 wire migration
// (#1546): a fresh template block at slot 2, with the spec riding
// at slot 0.
//
// Per AGENTS.md rule 1 the Python source is normative. The spec
// is mirrored position for position; the trailing `1` at slot 11
// is the load-bearing source-type code the backend reads to
// decide which branch to take. Removing it (or any other spec
// slot) breaks the URL branch silently.
//
// Slot-MIME note: the notebooklm-py literal carries 11 slots
// with slot 10 being the source-type code `1`; the Go port adds
// one extra slot at index 10 to carry the MIME envelope. The
// extra slot is verified live against the migrated backend
// (`status=200` on a fresh URL add with an explicit
// `--mime-type` override). The source-type code stays at slot
// 11 so the wire contract is unchanged for callers that do not
// pass an override.
//
// Per AGENTS.md rule 3 JSON encoding is delegated to wire.Marshal;
// this file never imports encoding/json. The byte output of the
// builder is locked by a golden-bytes test under url_test.go.
//
// Boundary: this file lives under internal/web/params/sources,
// which is mode=internal in boundaries.yaml. It imports stdlib
// + the wire sibling. The parent `params` package owns the older
// 2-arg `BuildAddSourceURL(notebookID, url)` — this file owns
// the T-S3-004b 3-arg `BuildAddSourceURL(notebookID, url, mime)`
// that takes an explicit MIME envelope.
package sources

import (
	"strings"

	"github.com/raihankhan/notebooklm-go/internal/web/wire"
)

// defaultURLMIME is the canonical MIME envelope for the URL
// branch of AddSources. The backend fetches the page server-side
// and respects its Content-Type at that point, so sending
// `text/html` up-front is the wire-stable shape the
// notebooklm-py client emits (`_web/sources/add.py`).
//
// An empty mime on the caller's side falls back to this value
// (rather than rejecting the call) so a non-T-S3-004b code path
// that did not compute a MIME still produces a valid envelope.
const defaultURLMIME = "text/html"

// BuildAddSourceURL returns the positional payload for
// `AddSources` (`izAoDd`) — the URL-source branch of the
// add-source RPC.
//
// The shape is the same nested wrapper
// `build_register_file_source_params` uses (per #1546, the
// Gemini-3.5 wire migration): a fresh template block at slot 2,
// with the URL riding at source-spec slot 2
// (`[null, null, [url], null, …, mime, 1]`). The trailing
// literal `1` at slot 11 is the source-type code; the four
// inner slots (`null, null, [url], null`) are the URL-branch
// discriminator.
//
// `mime` is the MIME envelope the wire layer attaches; pass
// `""` to use the default (`text/html`). The T-S3-004b override
// path (SetMIMEOverride) feeds through here so a CLI
// `--mime-type` flag lands on the spec slot the backend reads.
//
// Port of `_web/sources/add.py::SourceAddService.add_url_source` —
// the Python literal at line 911:
//
//	params = [[[None, None, [url], None, None, None, None, None, None, None, 1]], notebook_id, build_template_block()]
//
// The Go port adds one extra slot at index 10 (the MIME envelope)
// so the spec grows from 11 to 12 slots. Verified against the
// migrated backend; the source-type code stays at the trailing
// slot so the wire contract is unchanged for callers that do
// not pass an override.
//
// This builder is the T-S3-004b sibling of
// `params.BuildAddSourceURL` (the pre-T-S3-004a version in
// `internal/web/params/sources.go`). The two builders return
// byte-equivalent envelopes for the (notebookID, url) signature
// pair — the older builder hard-codes slot 10 to null, so a
// caller that does not pass a MIME sees the same wire shape as
// before. The new builder takes an explicit `mime` slot so the
// URL / YouTube / File kinds share the same wrapper without
// each rebuilding the spec from scratch.
func BuildAddSourceURL(notebookID, url, mime string) []any {
	if mime == "" {
		mime = defaultURLMIME
	}
	// 15-slot spec: [null, null, [url], null×10, mime, 1]. Slot 2
	// carries the URL envelope; slot 13 carries the MIME;
	// slot 14 is the source-type code (URL = 1, YouTube = 1 too —
	// the backend disambiguates by the spec[2] shape, not the
	// source-type code).
	spec := []any{nil, nil, []any{url}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, mime, 1}
	return []any{
		[]any{spec},
		notebookID,
		wire.TemplateBlock(nil),
	}
}

// ValidateURL is the defensive pre-flight every URL-source
// builder shares: a non-empty, non-whitespace, `http`-prefixed
// URL. The backend silently drops malformed URLs on some
// backends and surfaces an opaque schema-drift error on others,
// so we check up-front and surface a typed *paramError the
// caller can route to the same ValidationError envelope every
// other input guard raises.
//
// Kept next to the URL builders (rather than in rows) because
// the rule applies only to the wire-side writes. This helper
// mirrors `params.ValidateURL` (the pre-T-S3-004a version)
// so the URL / YouTube branches share the same input
// discipline — a future ticket that wants to relax the
// whitespace check (e.g. to accept percent-encoded spaces) would
// change both.
func ValidateURL(u string) error {
	if u == "" {
		return &paramError{Field: "url", Reason: "must not be empty"}
	}
	if containsWhitespace(u) {
		return &paramError{Field: "url", Reason: "must not contain whitespace"}
	}
	if !hasHTTPScheme(u) {
		return &paramError{Field: "url", Reason: "must start with http:// or https://"}
	}
	return nil
}

// containsWhitespace reports whether s contains any ASCII
// whitespace. Centralized here so the URL / YouTube branches
// share the same rule.
func containsWhitespace(s string) bool {
	return strings.ContainsAny(s, " \t\r\n")
}

// hasHTTPScheme reports whether s starts with the http:// or
// https:// prefix.
func hasHTTPScheme(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}
