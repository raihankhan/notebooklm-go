// Package sources — text.go: byte-stable request-payload builder
// for the inline-text branch of the AddSources RPC.
//
// This file ships the canonical positional shape
// `add_text_source` writes in
// notebooklm-py/src/notebooklm/_web/sources/add.py::SourceAddService
// .add_text_source. The shape is:
//
//	[
//	  [[None, [title, content], None, 2, None×6, 1]],
//	  notebook_id,
//	  build_template_block(),
//	]
//
// — an 11-slot spec where the [title, content] pair rides at
// slot 1 (the text-branch discriminator), the type marker `2`
// rides at slot 3 (the constant the backend uses to dispatch
// the text branch), the trailing `1` at slot 10 is the
// source-type code shared with every other kind, and slot 2 is
// reserved (it is the URL-branch discriminator; the text
// branch leaves it null). The outer envelope is the same
// nested wrapper `_web/sources/add.py` uses post the Gemini-3.5
// wire migration (#1546): a fresh template block at slot 2,
// with the spec riding at slot 0.
//
// Per AGENTS.md rule 1 the Python source is normative. The spec
// is mirrored position for position; the type marker `2` at
// slot 3 is the load-bearing branch discriminator the backend
// reads to decide between text (2), URL/YouTube (1), and Drive
// (different slot0 shape). Removing it (or moving it) breaks
// the text branch silently.
//
// Per AGENTS.md rule 3 JSON encoding is delegated to wire.Marshal;
// this file never imports encoding/json. The byte output of the
// builder is locked by a golden-bytes test under text_test.go.
//
// Boundary: this file lives under internal/web/params/sources,
// which is mode=internal in boundaries.yaml. It imports stdlib
// + the wire sibling.
package sources

import (
	"strings"

	"github.com/raihankhan/notebooklm-go/internal/web/wire"
)

// defaultTextMIME is the canonical MIME envelope for the
// inline-text branch of AddSources. The wire envelope carries
// `text/plain` so the backend's text handler matches the
// expected content type without sniffing. An empty mime on the
// caller's side falls back to this value so a non-T-S3-004c
// code path that did not compute a MIME still produces a valid
// envelope.
const defaultTextMIME = "text/plain"

// textTypeMarker is the literal the backend reads at source-spec
// slot 3 to dispatch the inline-text branch. The marker is
// shared across notebooklm-py and the live wire — `2` is the
// Python `_types.enums.SourceType.TEXT` value and the live
// backend's own handler matches on it. URL/YouTube use `1`; the
// marker is the per-kind branch discriminator, not the
// per-source-type code at slot 10 (which is also `1` for URL /
// YouTube and not used by the text branch).
const textTypeMarker = 2

// textSourceTypeCode is the trailing source-type code the wire
// spec carries at slot 10. Every AddSources spec ends with this
// literal; the value is shared across URL / YouTube / Text /
// Drive (the spec slot — not the trailing literal — is the
// per-kind discriminator).
const textSourceTypeCode = 1

// BuildAddSourceText returns the positional payload for
// `AddSources` (`izAoDd`) — the inline-text branch of the
// add-source RPC.
//
// The shape is the same nested wrapper the URL / YouTube
// branches use (per #1546, the Gemini-3.5 wire migration): a
// fresh template block at slot 2, with the spec riding at slot
// 0. The spec is 11 slots; slot 1 carries the [title, content]
// pair (the text-branch discriminator), slot 3 carries the
// type marker `2` (the load-bearing branch selector), and
// slot 10 carries the trailing source-type code.
//
// `title` is the user-supplied display name (CLI `--title`
// flag). An empty title falls back to "" so the wire envelope
// is still valid; the backend renders the empty title as the
// raw content. `content` is the raw text body; it is required
// (the caller pre-flights for non-empty content — see
// ValidateText). `mime` is the MIME envelope the wire layer
// attaches; pass `""` to use the default (`text/plain`).
//
// Port of `_web/sources/add.py::SourceAddService.add_text_source`.
// The shape mirrors `add_url_source` position for position
// except for the text-branch discriminator (slot 1) and the
// type marker (slot 3).
func BuildAddSourceText(notebookID, title, content, mime string) []any {
	if mime == "" {
		mime = defaultTextMIME
	}
	// 11-slot spec: [null, [title, content], null, 2, null×6, 1].
	// Slot 1 carries the [title, content] pair; slot 3 carries the
	// type marker `2`; slot 10 is the source-type code. The spec
	// length is 11, matching the URL / YouTube / Drive branches so
	// the wire envelope shape is uniform across kinds.
	spec := []any{
		nil,
		[]any{title, content},
		nil,
		textTypeMarker,
		nil, nil, nil, nil, nil, nil,
		textSourceTypeCode,
	}
	return []any{
		[]any{spec},
		notebookID,
		wire.TemplateBlock(nil),
	}
}

// ValidateText is the defensive pre-flight the text-source
// builder shares with the URL / YouTube branches. The rule is
// narrower than `ValidateURL` because inline text has no URL
// shape to enforce; the only invariant is that the content
// is non-empty (after trimming), and free of control
// characters the wire layer would reject.
//
// The trim check is intentionally narrow — a single-space
// input is rejected as "no content", but "Hello world" is
// accepted (the embedded space is part of the payload). The
// caller is expected to pass raw text the user pasted;
// rejecting all-whitespace input surfaces a typed error
// rather than a backend 5xx.
//
// The control-character check matches the rule `params`
// applies to share-builder email addresses (no newlines, no
// tabs) because the wire encoder escapes control characters
// but a literal \n in a text source can confuse the
// backend's text-tokenizer. Embedded spaces are NOT control
// characters; the rule only flags \r, \n, \t, and the
// 0x00-0x1F / 0x7F control range.
//
// An empty title is allowed (the backend renders the content
// itself); only the content is mandatory.
func ValidateText(content string) error {
	if strings.TrimSpace(content) == "" {
		return &paramError{Field: "content", Reason: "must not be empty or whitespace"}
	}
	if containsControl(content) {
		return &paramError{Field: "content", Reason: "must not contain control characters"}
	}
	return nil
}

// containsControl reports whether s contains any ASCII control
// character (0x00-0x1F, 0x7F). Centralized here so the
// text-branch validator shares the rule with any future
// in-line text surfaces (e.g. a chat-note body the same RPC
// ingests).
func containsControl(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c == 0x7F {
			return true
		}
	}
	// Also flag CR/LF as control characters so the text payload
	// stays single-line on the wire; a multi-line paste that the
	// caller wants preserved must go through the File branch
	// (a local file with the multi-line content). strings.ContainsAny
	// catches the standard whitespace set so the test surface
	// stays portable.
	return strings.ContainsAny(s, "\r\n\t")
}
