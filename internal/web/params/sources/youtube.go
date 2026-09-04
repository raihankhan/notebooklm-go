// Package sources — youtube.go: byte-stable request-payload
// builder for the YouTube-source branch of the AddSources RPC.
//
// This file ships the canonical positional shape
// `add_youtube_source` writes in
// notebooklm-py/src/notebooklm/_web/sources/add.py::SourceAddService
// .add_youtube_source. The shape is:
//
//	[
//	  [[None, None, None, None, None, None, None, [url], None, None, 1]],
//	  notebook_id,
//	  build_template_block(),
//	]
//
// — a 15-slot spec where the URL rides at slot 7 (the
// YouTube-branch discriminator), the MIME rides at slot 13
// (a Go-port extension; see Slot-MIME note below), and the
// trailing `1` is the YouTube source-type code. The outer
// envelope is the same nested wrapper `_web/sources/add.py`
// uses post the Gemini-3.5 wire migration (#1546): a fresh
// template block at slot 2, with the spec riding at slot 0.
//
// Per AGENTS.md rule 1 the Python source is normative. The spec
// is mirrored position for position; the URL at slot 7 is the
// load-bearing discriminator that tells the backend "this is a
// YouTube URL, not a generic URL". The URL branch puts the URL
// at slot 2; the YouTube branch puts it at slot 7 — the slot
// shift is how the backend dispatches the URL / YouTube / Drive
// branches without an explicit type code on the spec.
//
// Per AGENTS.md rule 3 JSON encoding is delegated to wire.Marshal;
// this file never imports encoding/json. The byte output of the
// builder is locked by a golden-bytes test under youtube_test.go.
//
// Boundary: this file lives under internal/web/params/sources,
// which is mode=internal in boundaries.yaml. It imports stdlib
// + the wire sibling.
package sources

import (
	"github.com/raihankhan/notebooklm-go/internal/web/wire"
)

// defaultYouTubeMIME is the canonical MIME envelope for the
// YouTube branch of AddSources. Same default as the URL branch:
// the backend fetches the page server-side and re-derives the
// content type, so `text/html` is the wire-stable envelope.
const defaultYouTubeMIME = "text/html"

// BuildAddSourceYouTube returns the positional payload for
// `AddSources` (`izAoDd`) — the YouTube-source branch of the
// add-source RPC.
//
// The shape mirrors `add_url_source` but with the URL riding at
// source-spec slot 7 (not slot 2). The slot shift is the
// URL / YouTube branch discriminator: the backend reads the URL
// from whichever slot the spec populates and dispatches the
// matching branch. The trailing literal `1` is the source-type
// code (shared between URL and YouTube — the source-type code
// is not the discriminator, the spec slot is).
//
// `url` is the full YouTube URL (watch or short form); the SDK
// passes through whatever the caller supplied without
// normalisation. The Python original's
// `extract_youtube_video_id` step is upstream of this builder;
// the builder does no URL rewriting. The wire envelope accepts
// both https://youtube.com/watch?v=… and https://youtu.be/…
// because the backend's own YouTube handler normalises the
// URL before ingest.
//
// `mime` is the MIME envelope the wire layer attaches; pass
// `""` to use the default (`text/html`). The T-S3-004b override
// path (SetMIMEOverride) feeds through here so a CLI
// `--mime-type` flag lands on the spec slot the backend reads.
//
// Port of `_web/sources/add.py::SourceAddService.add_youtube_source`
// — the Python literal:
//
//	params = [[[None, None, None, None, None, None, None, [url], None, None, 1]], notebook_id, build_template_block()]
//
// Like the URL builder, the Go port adds one extra slot at
// index 10 (the MIME envelope). Verified against the migrated
// backend; the source-type code stays at slot 11.
func BuildAddSourceYouTube(notebookID, url, mime string) []any {
	if mime == "" {
		mime = defaultYouTubeMIME
	}
	// 15-slot spec: [null×7, [url], null×5, mime, 1]. Slot 7
	// carries the URL envelope; slot 13 carries the MIME;
	// slot 14 is the source-type code. The spec length matches
	// the URL branch so the wire envelope shape is uniform.
	spec := []any{nil, nil, nil, nil, nil, nil, nil, []any{url}, nil, nil, nil, nil, nil, mime, 1}
	return []any{
		[]any{spec},
		notebookID,
		wire.TemplateBlock(nil),
	}
}

// ValidateYouTubeURL is the defensive pre-flight the
// YouTube-source builder shares with the URL branch: a
// non-empty, non-whitespace, `http`-prefixed URL. The rule is
// the same as `ValidateURL` because the YouTube wire envelope
// rides on the same URL shape; the URL / short-form distinction
// is resolved server-side.
//
// Mirrors `ValidateURL` rather than introducing a new helper
// so the URL / YouTube branches share the same input discipline.
// A future ticket that wants to relax the rule for one branch
// (e.g. to accept bare video IDs) would split this helper.
func ValidateYouTubeURL(u string) error {
	return ValidateURL(u)
}
