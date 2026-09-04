// Package sources contains the higher-level "business logic"
// wrappers that sit between the SDK root (notebooklm.SourcesAPI)
// and the byte builders (internal/web/params) + row adapters
// (internal/web/rows).
//
// Every exported function takes a Caller — the minimal interface
// the SDK exposes for dispatching one RPC — rather than reaching
// for the concrete client type. That way the MCP / REST / CLI
// adapters can each construct their own Caller and exercise these
// wrappers without pulling in the public SDK surface (which would
// re-introduce the import cycle the boundary rules forbid).
//
// Per AGENTS.md rule 5 (boundary table) and rule 3 (one JSON
// encoder) this package imports wire (encode + decode) and the
// params/rows siblings, never encoding/json. The transport
// layer's Caller interface hides the concrete RPC plumbing behind
// a single method.
//
// T-P6-1 ships the minimum-viable surface: List (decodes a
// GET_NOTEBOOK source list) and AddURL (issues ADD_SOURCE for the
// URL branch). The rest of the source lifecycle (AddText,
// AddDrive, AddYouTube, Delete, Rename, Refresh, …) lands in
// later phase tickets as additional exported functions on this
// package.
package sources

import (
	"context"
	"errors"
	"fmt"

	"github.com/raihankhan/notebooklm-go/internal/web/params"
	sourcesparams "github.com/raihankhan/notebooklm-go/internal/web/params/sources"
	"github.com/raihankhan/notebooklm-go/internal/web/rows"
	"github.com/raihankhan/notebooklm-go/internal/web/wire"
)

// Caller is the minimal interface this package needs from the SDK.
//
// One method per RPC. Implementations live in `notebooklm` (the
// public SDK root) and the test fixtures; the interface boundary
// here is the seam that lets `notebooklm.SourcesAPI` drive every
// feature without the feature package importing the SDK in
// return.
//
// Every method:
//
//   - Returns the wire-decoded payload (`any`, after the
//     `wire.DecodeResponse` envelope-unwrap) so callers can run the
//     rows.Decoder on it.
//   - Returns an error the caller should NOT match against
//     `wire.ErrDecoding` directly — the rows / features layers
//     re-wrap drift errors with their own typed vocabulary.
//
// `method` is the obfuscated id (`wire.Method`) the SDK resolved
// for the call; the features layer never reads it (it is opaque to
// row decoding), but the parameter keeps the contract explicit so
// a future schema-drift diagnostic has the method id available
// without an extra round-trip.
type Caller interface {
	Call(ctx context.Context, method wire.Method, params any, sourcePath string, allowNull bool) (any, error)
}

// List returns the typed source list for one notebook, in
// backend-defined order. The backing RPC is `GetNotebook`
// (`rLM1Ne`) — see `_web/sources/listing.py::SourceLister.list`
// for the Python original.
//
// The wire envelope is a single-element wrapper whose first
// element is the row list (`[[row1, row2, ...]]`); we unwrap
// through wire.At so a malformed envelope surfaces a
// *wire.ShapeDriftError rather than fabricating empty-id sources
// (the documented failure mode in
// `_web/sources/listing.py::list`).
//
// The source list lives at `notebook[0][1]` — the standard
// GET_NOTEBOOK envelope unwraps `notebook → nb_info → sources`.
// Each row is decoded via `rows.DecodeSource`, which handles the
// three id-envelope layouts (`[id]`, `[None, True, [id]]`, bare
// string) and the type / status enums.
func List(ctx context.Context, c Caller, notebookID string) ([]rows.Source, error) {
	if c == nil {
		return nil, errors.New("features.sources.List: caller is nil")
	}
	sourcePath := "/notebook/" + notebookID
	raw, err := c.Call(ctx, wire.MethodGetNotebook, params.BuildListSources(notebookID), sourcePath, false)
	if err != nil {
		return nil, fmt.Errorf("features.sources.List: %w", err)
	}
	return decodeSourceRows(raw)
}

// AddURL adds a URL source to a notebook and returns the typed
// view of the freshly added source. The backing RPC is `AddSources`
// (`izAoDd`) — see `_web/sources/add.py::SourceAddService
// .add_url_source` for the Python original.
//
// The wire shape is the nested wrapper (#1546 wire migration): a
// fresh template block at slot 2, with the URL riding at
// source-spec slot 2. The trailing literal `1` at the end of the
// spec is the source-type code. Returns the single decoded source
// row from the response envelope.
//
// allowNull is set to true because the backend occasionally
// returns a null result on a successful add (the documented
// "silent commit" failure mode — see
// `_web/sources/add.py::SourceAddService.add_url`).
//
// This is the legacy 2-arg entry point (notebookID, url). The
// T-S3-004b extension `AddURLWithMIME` accepts an explicit MIME
// envelope so the wire envelope slot 10 carries a value the
// caller chose (the CLI `--mime-type` flag). Existing callers
// that pass through the SDK's `AddURL` (no MIME override) hit
// this function unchanged — the default MIME is "text/html"
// which the sourceadd package's InferMIME produces.
func AddURL(ctx context.Context, c Caller, notebookID, url string) (rows.Source, error) {
	return AddURLWithMIME(ctx, c, notebookID, url, "")
}

// AddURLWithMIME is the T-S3-004b extension of AddURL that takes
// an explicit MIME envelope. The MIME rides at source-spec slot
// 10 — see `sourcesparams.BuildAddSourceURL` for the positional
// shape. An empty mime falls back to "text/html" (the URL
// branch's canonical envelope).
//
// The function is the single seam the SDK uses for the URL
// branch; the legacy AddURL delegates here so the wire shape
// stays in one place.
func AddURLWithMIME(ctx context.Context, c Caller, notebookID, url, mime string) (rows.Source, error) {
	if c == nil {
		return rows.Source{}, errors.New("features.sources.AddURL: caller is nil")
	}
	sourcePath := "/notebook/" + notebookID
	raw, err := c.Call(ctx, wire.MethodAddSource, sourcesparams.BuildAddSourceURL(notebookID, url, mime), sourcePath, true)
	if err != nil {
		return rows.Source{}, fmt.Errorf("features.sources.AddURL: %w", err)
	}
	return decodeAddedSourceRow(raw)
}

// AddYouTube adds a YouTube URL source to a notebook and returns
// the typed view of the freshly added source. The backing RPC is
// `AddSources` (`izAoDd`) — see
// `_web/sources/add.py::SourceAddService.add_youtube_source` for
// the Python original.
//
// The wire shape is the same nested wrapper as `add_url_source`
// (#1546 migration), but with the URL riding at source-spec slot
// 7 (not slot 2). The slot shift is the URL / YouTube branch
// discriminator: the backend reads the URL from whichever slot
// the spec populates and dispatches the matching branch. The
// trailing literal `1` is the source-type code (shared between
// URL and YouTube — the source-type code is not the
// discriminator, the spec slot is).
//
// `mime` is the MIME envelope the wire layer attaches; pass
// `""` to use the default (`text/html`).
//
// allowNull is set to false for YouTube per the Python original
// (`_web/sources/add.py::add_youtube_source`) — the backend
// rejects a YouTube add that returns null, unlike the URL
// branch which tolerates the silent-commit shape.
func AddYouTube(ctx context.Context, c Caller, notebookID, url, mime string) (rows.Source, error) {
	if c == nil {
		return rows.Source{}, errors.New("features.sources.AddYouTube: caller is nil")
	}
	sourcePath := "/notebook/" + notebookID
	raw, err := c.Call(ctx, wire.MethodAddSource, sourcesparams.BuildAddSourceYouTube(notebookID, url, mime), sourcePath, false)
	if err != nil {
		return rows.Source{}, fmt.Errorf("features.sources.AddYouTube: %w", err)
	}
	return decodeAddedSourceRow(raw)
}

// AddText adds an inline-text source to a notebook and returns
// the typed view of the freshly added source. The backing RPC is
// `AddSources` (`izAoDd`) — see
// `_web/sources/add.py::SourceAddService.add_text_source` for the
// Python original.
//
// The wire shape is the same nested wrapper as `add_url_source`
// (#1546 migration), with the text-branch discriminator at
// source-spec slot 1 (the [title, content] pair) and the type
// marker `2` at source-spec slot 3 (the load-bearing branch
// selector). `mime` is the wire envelope's MIME slot; pass `""`
// to use the default (`text/plain`).
//
// allowNull is set to true for Text per the Python original
// (`_web/sources/add.py::add_text_source`) — the backend
// tolerates the silent-commit shape on text adds, unlike the
// URL / YouTube branches.
//
// Text adds are intentionally non-idempotent: the wire envelope
// does not carry a stable identifier (a text source is uniquely
// identified only by its content bytes, not by an id the
// backend emits in the response), so a caller that wants
// dedupe must handle it externally — see docs/04-rpc-payloads.md
// §"Sources" / "Operation variants for the idempotency registry".
func AddText(ctx context.Context, c Caller, notebookID, title, content, mime string) (rows.Source, error) {
	if c == nil {
		return rows.Source{}, errors.New("features.sources.AddText: caller is nil")
	}
	sourcePath := "/notebook/" + notebookID
	raw, err := c.Call(ctx, wire.MethodAddSource, sourcesparams.BuildAddSourceText(notebookID, title, content, mime), sourcePath, true)
	if err != nil {
		return rows.Source{}, fmt.Errorf("features.sources.AddText: %w", err)
	}
	return decodeAddedSourceRow(raw)
}

// AddFile adds a local-file source to a notebook and returns
// the typed view of the freshly added source. The backing RPC
// is `AddSourceFile` (`o4cbdc`) — see
// `_web/sources/add.py::SourceAddService.register_file_source`
// for the Python original.
//
// The wire shape is the 3-slot envelope `[[filename]],
// notebook_id, TPL` — distinct from the URL / YouTube / Text /
// Drive branches which use a 3-slot envelope with a source-spec
// inner list. The File branch's spec is just the filename
// wrapped in a single-element list (the wire envelope requires
// the wrapping); the actual file bytes are NOT on the spec.
// They stream via the Scotty upload protocol
// (docs/04-rpc-payloads.md §"File upload — the Scotty resumable
// protocol"); the MIME rides on the
// `x-goog-upload-header-content-type` header in the upload phase
// (T-S3-005a), not on the wire spec.
//
// For T-S3-004c the SDK calls only the `AddSourceFile` RPC to
// register the source row; the upload phase is a follow-up
// ticket. The `mime` parameter is reserved for that follow-up;
// today it has no effect on the wire envelope (the spec is
// filename-only). Pass `""` to use the default binary-blob
// MIME during the upload phase.
//
// allowNull is set to true per the Python original — the
// backend tolerates a silent-commit on file-register so a
// caller that loses the response can re-list the notebook and
// find the freshly-added source row.
func AddFile(ctx context.Context, c Caller, notebookID, filename, mime string) (rows.Source, error) {
	if c == nil {
		return rows.Source{}, errors.New("features.sources.AddFile: caller is nil")
	}
	sourcePath := "/notebook/" + notebookID
	raw, err := c.Call(ctx, wire.MethodAddSourceFile, sourcesparams.BuildAddSourceFile(notebookID, filename, mime), sourcePath, true)
	if err != nil {
		return rows.Source{}, fmt.Errorf("features.sources.AddFile: %w", err)
	}
	return decodeAddedSourceRow(raw)
}

// AddDrive adds a Google Drive source to a notebook and returns
// the typed view of the freshly added source. The backing RPC
// is `AddSources` (`izAoDd`) — see
// `_web/sources/add.py::SourceAddService.add_drive_source` for
// the Python original.
//
// The wire shape is the 4-element legacy tail envelope
// `[[sourceSpec], notebook_id, [2], [1, null×8, [1]]]` — the
// Drive branch has not been migrated to the Gemini-3.5 TPL
// block (per the #1546 TODO in `_web/sources/add.py`). The
// 11-slot spec carries the `[fileID, mimeType, 1, title]` quad
// at slot 0 (the Drive-branch discriminator) and the
// source-type code `1` at slot 10.
//
// `fileID` is the Drive file id (pre-extracted from the share
// URL by the caller; the SDK does not parse the URL). `mimeType`
// is the Drive MIME envelope (e.g.
// `application/vnd.google-apps.document`); pass `""` to let
// the backend re-derive from the Drive file's metadata.
// `title` is the user-supplied display name (CLI `--title`
// flag).
//
// allowNull is set to false per the Python original — the
// backend rejects a Drive add that returns null, so the
// features layer surfaces a wire-layer ErrEmptyResult rather
// than degrading to a zero Source.
func AddDrive(ctx context.Context, c Caller, notebookID, fileID, mimeType, title string) (rows.Source, error) {
	if c == nil {
		return rows.Source{}, errors.New("features.sources.AddDrive: caller is nil")
	}
	sourcePath := "/notebook/" + notebookID
	raw, err := c.Call(ctx, wire.MethodAddSource, sourcesparams.BuildAddSourceDrive(notebookID, fileID, mimeType, title), sourcePath, false)
	if err != nil {
		return rows.Source{}, fmt.Errorf("features.sources.AddDrive: %w", err)
	}
	return decodeAddedSourceRow(raw)
}

// decodeSourceRows unwraps a GET_NOTEBOOK source-list envelope into
// a slice of typed Source rows. The envelope shape is
// `[[row1, row2, ...]]` (per `_web/sources/listing.py::list`); a
// falsy / non-list payload degrades to `nil` (the documented "no
// sources" contract); a truthy payload that does not match the
// envelope is schema drift and surfaces a *wire.ShapeDriftError.
//
// A genuinely empty notebook elides the sources slot (`None`
// instead of an empty list). This is a valid empty state, NOT a
// malformed response, so return `nil` without raising even under
// strict-decode — mirrors `_web/sources/listing.py::_extract_
// sources_list` line 195-200 (issue #1159 reserves the empty list
// for the genuinely-empty case).
func decodeSourceRows(raw any) ([]rows.Source, error) {
	if raw == nil {
		return nil, nil
	}
	outer, ok := raw.([]any)
	if !ok {
		return nil, &wire.ShapeDriftError{
			Path:    "",
			Method:  string(wire.MethodGetNotebook),
			Reason:  "not_a_list",
			GotType: typeName(raw),
		}
	}
	if len(outer) == 0 {
		return nil, nil
	}
	// GET_NOTEBOOK envelope: [nb_info, ...]. nb_info[0] is the
	// title, nb_info[1] is the sources list. We descend one level
	// deeper than `_web/sources/listing.py` (which starts from the
	// already-unwrapped `notebook` payload) because the wire layer
	// hands us the full GET_NOTEBOOK response, not the unwrapped
	// nb_info.
	nbInfo, err := wire.At(outer, 0)
	if err != nil {
		return nil, err
	}
	nbArr, ok := nbInfo.([]any)
	if !ok {
		// `outer[0]` is null = "no notebook info". Treat as empty.
		if nbInfo == nil {
			return nil, nil
		}
		return nil, &wire.ShapeDriftError{
			Path:    "[0]",
			Method:  string(wire.MethodGetNotebook),
			Reason:  "not_a_list",
			GotType: typeName(nbInfo),
		}
	}
	// Per `_web/sources/listing.py::_extract_sources_list` line
	// 178-183: a malformed `nb_info` (non-list OR len <= 1) is a
	// genuine "structure changed" error.
	if len(nbArr) <= 1 {
		return nil, nil
	}
	// nb_info[1] is the sources list. A genuinely empty notebook
	// elides this slot — see the issue #1159 rationale in the
	// caller docstring.
	rawList := nbArr[1]
	if rawList == nil {
		return nil, nil
	}
	list, ok := rawList.([]any)
	if !ok {
		return nil, &wire.ShapeDriftError{
			Path:    "[0][1]",
			Method:  string(wire.MethodGetNotebook),
			Reason:  "not_a_list",
			GotType: typeName(rawList),
		}
	}
	out := make([]rows.Source, 0, len(list))
	for i, row := range list {
		s, err := rows.DecodeSource(row)
		if err != nil {
			return nil, fmt.Errorf("features.sources.decodeSourceRows[%d]: %w", i, err)
		}
		out = append(out, s)
	}
	return out, nil
}

// decodeAddedSourceRow decodes an ADD_SOURCE response envelope into
// a typed Source row. The envelope is `[[[id], title, metadata,
// ...]]` — the medium-nested shape
// `_web/rows/sources.py::SourceRowShape.MEDIUM_NESTED` describes.
// A genuinely empty / null response degrades to a zero Source
// rather than raising (the documented "silent commit" failure mode
// for `AddSources`); a present-but-wrong-typed envelope surfaces a
// *wire.ShapeDriftError.
func decodeAddedSourceRow(raw any) (rows.Source, error) {
	if raw == nil {
		return rows.Source{}, nil
	}
	outer, ok := raw.([]any)
	if !ok || len(outer) == 0 {
		return rows.Source{}, nil
	}
	// Medium-nested shape: outer[0] is the entry row; the entry
	// itself is `[id_envelope, title, metadata, ...]`. We pass the
	// entry directly to rows.DecodeSource which handles the
	// id-envelope variants.
	entry, err := wire.At(outer, 0)
	if err != nil {
		return rows.Source{}, err
	}
	if entry == nil {
		return rows.Source{}, nil
	}
	return rows.DecodeSource(entry)
}

// typeName is the small helper that gives the
// *ShapeDriftError.GotType field a stable format. Mirrors
// `typeName` in `features/notebooks.go` so the diagnostic surface
// is uniform across the namespaces.
func typeName(v any) string {
	if v == nil {
		return "nil"
	}
	switch v.(type) {
	case []any:
		return "[]any"
	case map[string]any:
		return "map[string]any"
	case string:
		return "string"
	case bool:
		return "bool"
	case float64:
		return "float64"
	}
	return fmt.Sprintf("%T", v)
}
