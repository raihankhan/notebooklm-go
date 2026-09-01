// Package wire is the single JSON adapter for the notebooklm-go module.
//
// Per docs/AGENTS.md rule 3 ("Preserve JSON byte-compatibility"), there is
// exactly one JSON encoder and one JSON decoder in this repo, and both live
// here. Every other package — including everything under internal/web and
// every adapter under cmd/ — must route JSON work through wire.Marshal,
// wire.Unmarshal, and (when deterministic output matters) wire.MarshalSorted.
//
// Calling encoding/json directly from any other package is a layering
// violation; internal/tools/boundarycheck enforces it, and code review should
// reject it on sight.
//
// Boundary: per docs/AGENTS.md rule 5, this package is stdlib-only. It must
// not import internal/redact, internal/logging, internal/buildinfo, or any
// third-party module. Bytes that may contain credentials flow through this
// package's encoder unchanged; redaction is the caller's responsibility, but
// it happens upstream of the wire boundary, never inside it.
package wire
