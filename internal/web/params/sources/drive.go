// Package sources — drive.go: byte-stable request-payload
// builder for the Google Drive branch of the AddSources RPC.
//
// This file ships the canonical positional shape
// `add_drive_source` writes in
// notebooklm-py/src/notebooklm/_web/sources/add.py::SourceAddService
// .add_drive_source. The shape is:
//
//	[
//	  [[[fileID, mimeType, 1, title], None×9, 1]],
//	  notebook_id,
//	  [2],
//	  [1, None×8, [1]],
//	]
//
// — a 4-slot outer envelope (NOT 3-slot like URL / YouTube /
// Text — the Drive branch has not been migrated to the
// Gemini-3.5 TPL template block; it still uses the legacy
// four-element tail). The 11-slot spec carries the
// `[fileID, mimeType, 1, title]` quad at slot 0 (the
// Drive-branch discriminator), and the trailing `1` at slot
// 10 is the source-type code shared with every other kind.
//
// Per AGENTS.md rule 1 the Python source is normative. The
// shape is mirrored position for position; the
// `[fileID, mimeType, 1, title]` quad at slot 0 is the
// load-bearing discriminator that tells the backend "this is
// a Drive source, not a generic URL". Removing the inner `1`
// at quad[2] (the Drive source-type code) breaks the Drive
// branch silently.
//
// Per AGENTS.md rule 3 JSON encoding is delegated to wire.Marshal;
// this file never imports encoding/json. The byte output of the
// builder is locked by a golden-bytes test under drive_test.go.
//
// Boundary: this file lives under internal/web/params/sources,
// which is mode=internal in boundaries.yaml. It imports stdlib
// + the wire sibling.
package sources

// (No imports — the Drive branch uses literal `[]any` shapes
// and does not call any wire helper; per AGENTS.md rule 3
// JSON encoding is delegated to wire.Marshal at the
// encoding seam, so this builder is stdlib-free.)

// defaultDriveMIME is the canonical MIME envelope fallback
// for the Drive branch when the caller does not pass an
// explicit `--mime-type`. The Drive handler on the backend
// re-derives the MIME from the Drive file's own metadata, so
// the wire envelope carries whatever the caller supplied
// (or "" if unset). An empty mime falls back to "" — the
// wire envelope remains valid, and the backend's Drive
// metadata handler fills in the right value server-side.
//
// We deliberately do NOT default to "application/octet-stream"
// or any other binary-blob fallback because the Drive branch
// always has a real MIME server-side; sending a fabricated
// MIME would mask the actual content type during debugging.
const defaultDriveMIME = ""

// driveSourceTypeCode is the literal the inner quad carries at
// position 2 (the Drive source-type code, distinct from the
// URL/YouTube `1`). The code is the same value the
// `_types.enums.SourceType.DRIVE` Python enum emits; the
// backend's Drive handler matches on it before the spec-slot
// discriminator runs.
const driveSourceTypeCode = 1

// driveSpecSourceTypeCode is the trailing literal the wire
// spec carries at slot 10. Every AddSources spec ends with
// this literal; the value is shared across URL / YouTube /
// Text / Drive (the spec slot — not the trailing literal — is
// the per-kind discriminator).
const driveSpecSourceTypeCode = 1

// BuildAddSourceDrive returns the positional payload for
// `AddSources` (`izAoDd`) — the Google Drive branch of the
// add-source RPC.
//
// The shape is the four-element tail `_web/sources/add.py`
// uses for the Drive branch (per #1546 TODO, the Drive branch
// has not been migrated to the Gemini-3.5 TPL template block):
// `[[sourceSpec], notebook_id, [2], [1, null×8, [1]]]`. The
// spec is 11 slots; slot 0 carries the `[fileID, mimeType, 1,
// title]` quad (the Drive-branch discriminator), and slot 10
// carries the trailing source-type code.
//
// `fileID` is the Drive file id extracted from the share URL
// (the SDK's caller pre-extracts; see ValidateDriveURL). It
// rides at quad[0].
//
// `mimeType` is the Drive MIME envelope (e.g.
// `application/vnd.google-apps.document`). The wire spec
// carries it at quad[1]; the SDK's caller pre-resolves the
// MIME from the share URL or accepts a `SetMIMEOverride`. An
// empty mimeType falls back to "" (the backend re-derives
// from Drive metadata server-side).
//
// `title` is the user-supplied display name (CLI `--title`
// flag). It rides at quad[3].
//
// Port of `_web/sources/add.py::SourceAddService
// .add_drive_source` (the Drive-branch variant of the
// add-source RPC). The Python literal:
//
//	params = [[[[fileID, mimeType, 1, title], None×9, 1]], notebook_id, [2], [1, None×8, [1]]]
//
// The Go port mirrors the four-element tail shape; the Drive
// branch's spec is the only one that does NOT use the fresh
// template block at slot 2 because the backend's Drive handler
// still requires the legacy tail for source-type dispatch.
//
// This builder is the T-S3-004c Drive-branch sibling of
// `BuildAddSourceURL` (the URL-branch builder). The two
// builders route through the same RPC (`izAoDd`) but with
// different envelope tails — the SDK's features layer
// dispatches each branch through its own method.
func BuildAddSourceDrive(notebookID, fileID, mimeType, title string) []any {
	if mimeType == "" {
		mimeType = defaultDriveMIME
	}
	// 11-slot spec: [[fileID, mimeType, 1, title], null×9, 1].
	// Slot 0 carries the Drive quad; slot 10 is the source-type
	// code. The spec length is 11, matching the URL / YouTube /
	// Text branches so the wire envelope shape is uniform across
	// kinds (the outer-envelope tail differs, not the spec).
	spec := []any{
		[]any{fileID, mimeType, driveSourceTypeCode, title},
		nil, nil, nil, nil, nil, nil, nil, nil, nil,
		driveSpecSourceTypeCode,
	}
	// Four-element tail (NOT the fresh TPL block) — see the
	// file-level docstring. The legacy tail is what the
	// backend's Drive handler matches on; migrating to TPL would
	// break the branch silently. wire.TemplateBlock returns
	// the [2, null, null, [1, ...]] block; for the Drive branch
	// the slots are `[2], [1, null×8, [1]]` (literal, no
	// per-spec template block).
	return []any{
		[]any{spec},
		notebookID,
		[]any{2},
		[]any{1, nil, nil, nil, nil, nil, nil, nil, nil, []any{1}},
	}
}

// ValidateDriveURL is the defensive pre-flight the Drive-source
// builder shares with the URL / YouTube branches. The rule is
// narrower than `ValidateURL` because the Drive branch extracts
// the file id from the share URL (a 33-character Drive opaque
// id) before dispatching; the SDK's caller pre-flights for a
// well-formed share URL via `sourceadd.Classify`, and this
// helper only catches the wire-level mistakes on the
// pre-extracted file id.
//
// The file id must be non-empty, non-whitespace, and free of
// control characters. We do NOT enforce the 33-char Drive id
// shape because Drive ids are opaque alphanumeric strings of
// varying length (the canonical length is 33 chars but the
// backend accepts shorter ids for shared-drive shortcuts);
// validating the shape here would be brittle.
func ValidateDriveURL(fileID string) error {
	if fileID == "" {
		return &paramError{Field: "fileID", Reason: "must not be empty"}
	}
	if containsWhitespace(fileID) {
		return &paramError{Field: "fileID", Reason: "must not contain whitespace"}
	}
	if containsControl(fileID) {
		return &paramError{Field: "fileID", Reason: "must not contain control characters"}
	}
	return nil
}
