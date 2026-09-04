// Package sourceadd — mime.go: MIME inference + AddOption helpers
// for the source-add pipeline.
//
// Every source kind needs a MIME type the wire layer can attach
// to the upload / URL / Drive envelope. The kind-to-MIME mapping
// is deterministic and depends only on (Kind, raw arg) — there is
// no remote fetch at this stage; the backend re-derives the MIME
// from the content it retrieves. The questions this file answers:
//
//  1. "What MIME does the wire envelope carry for this Kind?" —
//     handled by InferMIME. URL / YouTube are `text/html` because
//     the backend fetches the page server-side and respects its
//     Content-Type at that point; File kinds sniff the extension;
//     Text is `text/plain`. Drive kinds defer to the backend
//     entirely (a Drive `documentId` carries its MIME as part of
//     the Drive file metadata, not the add-source envelope).
//
//  2. "How does a caller override the inferred MIME?" — handled by
//     SetMIMEOverride, a functional Option the sourceadd package
//     owns. The SDK reads the override off the AddOptions bag
//     each Add* call, so concurrent callers cannot share a stale
//     override.
//
// Boundary: this file is part of internal/app (mode=internal in
// boundaries.yaml). It imports stdlib only. The notebooklm SDK
// depends on this package's Option type; this package must NOT
// import notebooklm (the boundary table forbids the reverse).
package sourceadd

import (
	"mime"
	"path/filepath"
	"strings"
)

// AddOption tunes one Add* call on the public SDK. The
// functional-options pattern lets later phase tickets grow the
// surface (filtering, wait, batch size, …) without breaking
// call sites that pass zero options today. The Option is owned
// by sourceadd rather than notebooklm because every AddOption
// belongs to the source-add pipeline (T-S3-004a/b/c/d) — a
// future ticket that adds Options for other SDK namespaces
// would live in their own packages, not here.
//
// The Option mutates an AddOptions bag (rather than calling the
// SDK directly) so the SDK owns the apply-loop and can short-
// circuit a nil Option, refuse unknown Option shapes, and apply
// options in a deterministic order.
//
// Per AGENTS.md rule 5 the sourceadd package is mode=internal,
// so the Option type lives next to the only call sites that
// build it (the SDK reads it).
type AddOption func(*AddOptions)

// AddOptions is the per-call option bag AddOption mutates. The
// zero value is the default: no MIME override, no wait, etc.
//
// The struct is intentionally minimal — only the fields
// T-S3-004b ships. Later tickets (T-S3-004c/d, T-S3-006) extend
// the bag with their own fields rather than retrofitting this
// one.
type AddOptions struct {
	// MIMEOverride, when non-empty, replaces the inferred MIME
	// for the upcoming Add* call. Empty means "no override;
	// use InferMIME(kind, arg)". A CLI flag that resolves to
	// "" does not blank the inferred value.
	MIMEOverride string
}

// InferMIME returns the MIME type the wire layer attaches for the
// given Kind + raw argument. The decision tree is deterministic
// and depends only on (kind, arg); no network or filesystem
// calls are made at this stage. The function is the no-override
// entry point — see InferMIMEWithOverride for the explicit
// override path.
//
// Per-kind rules:
//
//   - KindURL, KindYouTube → "text/html". The backend fetches the
//     page server-side and respects its Content-Type at that
//     point. Sending `text/html` up-front is the wire-stable
//     shape the notebooklm-py client emits (`_web/sources/add.py`).
//
//   - KindFile → sniff the extension. The mapping uses Go's
//     stdlib `mime.TypeByExtension` so the table is shared with
//     the rest of the module; an unknown extension falls back to
//     `application/octet-stream` (the documented binary-blob
//     default). The base-name extraction strips the directory
//     portion so a relative path like `./report.pdf` still
//     sniffs `application/pdf`.
//
//   - KindText → "text/plain". The wire envelope for inline-text
//     sources carries `text/plain`; the backend does no
//     transformation.
//
//   - KindDrive → "". The MIME is NOT carried on the add-source
//     wire envelope; it is derived from the Drive file's own
//     metadata server-side. Returning "" here means "let the
//     backend decide" rather than fabricating a value the wire
//     would reject. Callers that need a non-empty MIME for
//     Drive must pass it explicitly through the override path.
//
//   - KindUnknown → "". The classifier rejected the argument;
//     the SDK never reaches the wire path. The empty string
//     signals "no inference" so a caller that logged the value
//     would not mis-attribute a `text/plain` to a malformed
//     input.
//
// The function is total — every Kind value produces a defined
// output. A nil / unknown MIME returned by the stdlib mime
// package (e.g. ".foobar" extension) is replaced with
// "application/octet-stream" so the wire envelope always carries
// a non-empty MIME for the File branch.
func InferMIME(kind Kind, arg string) string {
	return InferMIMEWithOverride(kind, arg, "")
}

// InferMIMEWithOverride returns the MIME the wire layer attaches
// for the given Kind + raw argument, with an explicit override
// that beats the inferred value. The override is intended to be
// the value of an `--mime-type` CLI flag or its REST / MCP
// counterpart; an empty override is treated as "no override" so
// a flag that resolves to "" does not blank the inferred MIME.
//
// Override semantics:
//
//   - non-empty override → the override wins, regardless of kind.
//   - empty override → the inferred rule for (kind, arg) wins.
//
// The override is passed by value rather than read from a
// package-private variable so concurrent callers cannot share a
// stale override. The InferMIME convenience wrapper passes "" so
// the no-override case has one entry point.
func InferMIMEWithOverride(kind Kind, arg, override string) string {
	if override != "" {
		return override
	}
	switch kind {
	case KindURL, KindYouTube:
		// Backend fetches the page server-side; `text/html` is
		// the canonical envelope value.
		return "text/html"
	case KindFile:
		return inferFileMIME(arg)
	case KindText:
		return "text/plain"
	case KindDrive:
		// Drive MIME is server-derived; the wire envelope does
		// not carry one. Returning "" forces callers to set an
		// explicit override if they need a value.
		return ""
	case KindUnknown:
		return ""
	default:
		return ""
	}
}

// inferFileMIME sniffs the extension off arg and returns the
// stdlib-registered MIME type for that extension. An unknown
// extension (or one for which the stdlib returns no mapping)
// falls back to `application/octet-stream`.
//
// The base-name extraction uses filepath.Base so a path-shaped
// argument (`./report.pdf`, `/tmp/report.pdf`, `~/report.pdf`)
// resolves to the file portion. The extension lookup goes
// through mime.TypeByExtension (Go's stdlib table) so the
// mapping is shared with the rest of the module and updates
// automatically when Go's stdlib table grows.
func inferFileMIME(arg string) string {
	if arg == "" {
		// Empty input — the classifier already rejected an
		// empty arg via the path-shaped branch; return the
		// default binary-blob MIME so the wire envelope is
		// never empty.
		return "application/octet-stream"
	}
	base := filepath.Base(arg)
	// mime.TypeByExtension returns "ext/ext; charset=..." for
	// text types and "ext/ext" for binary types. We trim the
	// `; charset=...` suffix so the wire envelope carries a
	// bare MIME (not a content-type).
	guess := mime.TypeByExtension(filepath.Ext(base))
	if guess == "" {
		return "application/octet-stream"
	}
	if i := strings.Index(guess, ";"); i >= 0 {
		guess = strings.TrimSpace(guess[:i])
	}
	if guess == "" {
		return "application/octet-stream"
	}
	return guess
}

// SetMIMEOverride returns an AddOption that sets an explicit MIME
// type for the upcoming Add* call. The override beats
// InferMIME; an empty override is treated as "no override" so a
// CLI flag that resolves to "" does not blank the inferred MIME.
//
// The Option mutates the AddOptions bag directly; the SDK reads
// the bag at Add* entry so concurrent callers each pass their own
// Options and the override cannot leak between goroutines. The
// Option is the same functional-options pattern the SDK already
// uses for WithSourcesMaxItems — keeping it here means a future
// MCP / REST tool can pass SetMIMEOverride without reaching into
// a setter side-channel.
//
// Example use (CLI):
//
//	api.AddURL(ctx, "nb-1", "https://example.com",
//	    sourceadd.SetMIMEOverride("application/pdf"))
func SetMIMEOverride(override string) AddOption {
	return func(o *AddOptions) {
		if o == nil {
			return
		}
		o.MIMEOverride = override
	}
}
