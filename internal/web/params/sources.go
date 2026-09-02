// Package params — sources.go: byte-stable request-payload builders for the
// `batchexecute` sources surface.
//
// Per AGENTS.md rule 1 the Python source is normative. Every builder
// below corresponds to a positional literal in
// notebooklm-py/src/notebooklm/_web/sources/add.py (the `add_url_source` /
// `add_youtube_source` / `add_text` shapes) or
// notebooklm-py/src/notebooklm/_web/sources/listing.py::list (which
// reuses `build_get_notebook_params` from `_web/params/notebooks.py`).
//
// Phase 6 (T-P6-1) ships only the two minimum-viable builders:
// `BuildListSources` and `BuildAddSourceURL`. The full source lifecycle
// (AddText, AddDrive, AddYouTube, Delete, Rename, Refresh, …) lands in
// later tickets; their builder stubs panic with a TODO marker so the
// wrong-payload failure mode stays loud.
//
// JSON encoding is delegated to wire.Marshal (AGENTS.md rule 3): this
// package never imports encoding/json directly. The byte output of every
// builder is locked by a golden-bytes test under sources_test.go.
package params

import (
	"strings"

	"github.com/raihankhan/notebooklm-go/internal/web/wire"
)

// Source status label constants — the wire-stable string forms the
// `status_label` field on the typed Source view surfaces. The labels
// mirror the `_types.enums.SourceStatus` (PROCESSING / READY / ERROR)
// Python values used in the row adapter's status decoder. Mirrored here
// rather than in rows/sources.go because the labels double as the
// not-yet-rendered ingest code paths in the policy/registry layer
// (T-P9 will widen this with the typed enum).
const (
	SourceStatusProcessing = "PROCESSING"
	SourceStatusReady      = "READY"
	SourceStatusError      = "ERROR"
)

// Source kind constants — the wire-stable string forms the `kind`
// field on the typed Source view surfaces. The labels mirror the
// `_types.sources.SourceType` enum used in `rows/sources.py`. Kept
// here so the typed Source type in `rows/sources.go` can refer to
// them without an import cycle.
const (
	SourceKindURL     = "url"
	SourceKindYouTube = "youtube"
	SourceKindDrive   = "drive"
	SourceKindText    = "text"
	SourceKindFile    = "file"
)

// BuildListSources returns the positional payload for `GetNotebook`
// (`rLM1Ne`) — the read path that surfaces the source list at
// `notebook[0][1]`. The listing surface (T-P6-1's `SourcesAPI.List`)
// descends the same envelope, so the wire shape is identical to
// `BuildGet`. Kept under a sources-specific name so the builder
// surface reads intent-first at every call site.
//
// The trailing `[None, 0]` and the nested template block at slot 2 are
// load-bearing — see `BuildGet`'s rationale.
//
// Port of `_web/params/notebooks.py::build_get_notebook_params`,
// inlined here so the sources package owns its own surface (mirrors
// the `_web/sources/listing.py::list` choice — that file inlines
// `build_get_notebook_params` to avoid an ownership cycle).
func BuildListSources(notebookID string) []any {
	return []any{notebookID, nil, wire.TemplateBlock(nil), nil, 0}
}

// BuildAddSourceURL returns the positional payload for `AddSources`
// (`izAoDd`) — the URL-source branch of the add-source RPC.
//
// The shape is the same nested wrapper `build_register_file_source_params`
// uses (per #1546, the Gemini-3.5 wire migration): a fresh template
// block at slot 2, with the URL riding at source-spec slot 2
// (`[null, null, [url], null, …]`). The trailing literal `1` at the
// end of the spec is the source-type code; the four inner slots
// (`null, null, [url], null`) are the URL-branch discriminator.
//
// Port of `_web/sources/add.py::SourceAddService.add_url_source` —
// the literal at line 911:
// `params = [[[None, None, [url], None, None, None, None, None, None, None, None, 1]], notebook_id, build_template_block()]`.
// Note: the spec is 12 slots of which 10 are null — the
// `_web/sources/add.py` literal carries one extra trailing null
// relative to the canonical file-source shape (the URL branch has
// no per-source metadata envelope, so the trailing slot is left
// null).
func BuildAddSourceURL(notebookID, url string) []any {
	spec := []any{nil, nil, []any{url}, nil, nil, nil, nil, nil, nil, nil, nil, 1}
	return []any{
		[]any{spec},
		notebookID,
		wire.TemplateBlock(nil),
	}
}

// ValidateURL is the defensive pre-flight every URL-source builder
// shares: a non-empty, non-whitespace, `http`-prefixed URL. The
// backend silently drops malformed URLs on some backends and surfaces
// an opaque schema-drift error on others, so we check up-front and
// surface a typed *paramError the caller can route to the same
// ValidationError envelope every other input guard raises.
//
// Kept next to the URL builders (rather than in rows) because the
// rule applies only to the wire-side writes.
func ValidateURL(u string) error {
	if u == "" {
		return &paramError{Field: "url", Reason: "must not be empty"}
	}
	if strings.ContainsAny(u, " \t\r\n") {
		return &paramError{Field: "url", Reason: "must not contain whitespace"}
	}
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return &paramError{Field: "url", Reason: "must start with http:// or https://"}
	}
	return nil
}
