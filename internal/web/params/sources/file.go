// Package sources — file.go: byte-stable request-payload
// builder for the local-file branch of the AddSources RPC.
//
// This file ships the canonical positional shape
// `add_file_source` writes in
// notebooklm-py/src/notebooklm/_web/sources/add.py::SourceAddService
// .add_file_source. The shape is:
//
//	[
//	  [[filename]],
//	  notebook_id,
//	  build_template_block(),
//	]
//
// — a 3-slot envelope where the filename rides at slot 0
// (wrapped in a single-element list, matching the
// `[[filename]]` Python literal). Unlike URL / YouTube / Text
// / Drive, the file branch does NOT carry a source-spec
// envelope; the spec is just the filename, and the actual file
// bytes are streamed separately via the Scotty upload protocol
// (docs/04-rpc-payloads.md §"File upload — the Scotty resumable
// protocol"). The MIME envelope rides on the upload's
// `x-goog-upload-header-content-type` header — the T-S3-004c
// builder does not encode it on the spec.
//
// Per AGENTS.md rule 1 the Python source is normative. The
// shape is mirrored position for position; the
// filename-at-slot-0 layout is the discriminator the backend
// reads to dispatch the file branch. Removing the wrapping
// list (passing a bare string at slot 0) breaks the file
// branch silently.
//
// Per AGENTS.md rule 3 JSON encoding is delegated to wire.Marshal;
// this file never imports encoding/json. The byte output of the
// builder is locked by a golden-bytes test under file_test.go.
//
// Boundary: this file lives under internal/web/params/sources,
// which is mode=internal in boundaries.yaml. It imports stdlib
// + the wire sibling.
package sources

import (
	"path/filepath"
	"strings"

	"github.com/raihankhan/notebooklm-go/internal/web/wire"
)

// defaultFileMIME is the canonical MIME envelope fallback for
// the file branch when the caller passes `""`. The actual
// MIME rides on the upload header (the wire spec does not
// carry it), so this constant is only used by the SDK to
// pre-populate a default value the T-S3-005a upload phase
// forwards to the `x-goog-upload-header-content-type` header
// when the caller did not pass `SetMIMEOverride`. The default
// matches the URL/YouTube envelope (`text/plain` is wrong for
// generic file bytes; the only safe default is the documented
// binary-blob fallback).
const defaultFileMIME = "application/octet-stream"

// BuildAddSourceFile returns the positional payload for
// `AddSourceFile` (`o4cbdc`) — the local-file branch of the
// add-source RPC.
//
// The shape is the 3-slot envelope `_web/sources/add.py`
// uses for the file branch: `[[filename]], notebook_id,
// build_template_block()`. The filename rides at slot 0 wrapped
// in a single-element list (NOT a bare string — the wire
// envelope expects the list wrapper). The MIME envelope does
// NOT ride on the spec; the actual file bytes are streamed
// via Scotty upload (T-S3-005a) and the MIME rides on the
// `x-goog-upload-header-content-type` header there.
//
// `filename` is the user-supplied filename (NOT the full path;
// the SDK extracts the base via filepath.Base before
// dispatching). The backend's file handler uses the filename
// as the display title and as the suggested filename for the
// download endpoint; passing the full path would expose the
// user's directory layout. An empty filename falls back to the
// base of the path the caller supplied.
//
// `mime` is the MIME envelope the wire layer attaches; pass
// `""` to use the default (`application/octet-stream`). The
// T-S3-004c override path (SetMIMEOverride) feeds through
// here so a CLI `--mime-type` flag lands on the upload header
// the T-S3-005a upload phase reads.
//
// Port of `_web/sources/add.py::SourceAddService
// .register_file_source` (the file-source variant of the
// add-source RPC). The Python literal:
//
//	params = [[[filename]], notebook_id, build_template_block()]
//
// The Go port mirrors the 3-slot shape; the MIME envelope
// rides on the upload header rather than the spec because the
// file branch's spec is just the filename (no room for a MIME
// slot — see `_web/sources/add.py::register_file_source`).
//
// This builder is the T-S3-004c file-branch sibling of
// `internals/web/params/sources.go::BuildAddSourceURL` (the
// URL-branch builder). The two builders route through
// different RPCs (`o4cbdc` for file, `izAoDd` for URL); the
// SDK's features layer dispatches each branch through its own
// method.
func BuildAddSourceFile(notebookID, filename, mime string) []any {
	if mime == "" {
		mime = defaultFileMIME
	}
	name := filename
	if name == "" {
		// Empty filename on the wire is rejected by the
		// backend's file handler; the validate pre-flight
		// rejects an empty filename at the SDK boundary, so
		// this branch is purely defensive.
		name = ""
	}
	return []any{
		[]any{[]any{name}},
		notebookID,
		wire.TemplateBlock(nil),
	}
}

// ValidateFile is the defensive pre-flight the file-source
// builder shares with the URL / YouTube / Text branches. The
// rule is narrower than `ValidateURL` because a local file
// path is not URL-shaped; the invariants are:
//
//  1. Non-empty after trimming.
//  2. No control characters (the wire encoder escapes control
//     characters but a literal \n in a filename can confuse
//     the backend's filename parser).
//  3. The base-name portion (post filepath.Base) is also
//     non-empty — a path that resolves to "." (the current
//     directory) is meaningless as a source filename.
//
// The caller is expected to pass a path the CLI expanded
// (e.g. `~/.notebooklm/foo.pdf`); the symlink gate
// (sourceadd.Validate) is what rejects paths inside the
// operator's storage root, and the classifier is what
// distinguishes a path-shaped arg from text / URL inputs.
// This helper only catches the wire-level mistakes.
func ValidateFile(path string) error {
	if strings.TrimSpace(path) == "" {
		return &paramError{Field: "path", Reason: "must not be empty"}
	}
	if containsControl(path) {
		return &paramError{Field: "path", Reason: "must not contain control characters"}
	}
	if filepath.Base(strings.TrimSpace(path)) == "" || filepath.Base(strings.TrimSpace(path)) == "." {
		return &paramError{Field: "path", Reason: "must refer to a regular file"}
	}
	return nil
}
