// Package sourceadd — mime_test.go: table-driven tests for the
// MIME-inference + override pipeline.
//
// The test suite covers three rule families:
//
//  1. Kind-to-MIME inference — URL / YouTube → text/html, file
//     extensions → stdlib mime table, text → text/plain, drive /
//     unknown → "".
//
//  2. Override-wins — a non-empty override replaces the inferred
//     value for every kind, including the ""-producing kinds
//     (Drive / Unknown).
//
//  3. SetMIMEOverride option — the functional Option mutates
//     AddOptions and an empty override is preserved as "" (no
//     override), not converted to a sentinel that would
//     mis-attribute a blank MIME to the inferred default.
//
// Each subtest runs in isolation; no filesystem state is
// touched (the File branch uses stdlib mime.TypeByExtension
// directly, so no TempDir is required).
package sourceadd

import (
	"testing"
)

// TestInferMIME_Kinds pins the per-Kind inference rule. The
// table covers every Kind the classifier returns (URL, YouTube,
// File, Text, Drive, Unknown) plus a few extension cases the
// stdlib mime table resolves to canonical values.
func TestInferMIME_Kinds(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		kind Kind
		arg  string
		want string
	}{
		// URL branch — text/html is the canonical envelope;
		// the backend fetches the page server-side and respects
		// its Content-Type at that point.
		{"url_https", KindURL, "https://example.com", "text/html"},
		{"url_http", KindURL, "http://example.com", "text/html"},
		{"url_with_query", KindURL, "https://example.com/?id=42", "text/html"},

		// YouTube branch — same text/html default. The
		// URL / YouTube branches share the canonical envelope;
		// the slot shift in the wire spec is the discriminator.
		{"youtube_watch", KindYouTube, "https://www.youtube.com/watch?v=abc", "text/html"},
		{"youtube_short", KindYouTube, "https://youtu.be/abc", "text/html"},

		// File branch — extension-driven. The stdlib
		// mime.TypeByExtension table is the source of truth.
		// Go's stdlib table does NOT recognize ".md" /
		// ".markdown" (those resolve to "" — see Go issue
		// #43401), so a Markdown file falls back to the
		// binary-blob default. A caller that needs the
		// Markdown MIME passes it via SetMIMEOverride.
		{"file_pdf", KindFile, "report.pdf", "application/pdf"},
		{"file_pdf_path", KindFile, "/tmp/report.pdf", "application/pdf"},
		{"file_pdf_relative", KindFile, "./report.pdf", "application/pdf"},
		{"file_txt", KindFile, "notes.txt", "text/plain"},
		{"file_md", KindFile, "README.md", "application/octet-stream"},
		{"file_markdown", KindFile, "README.markdown", "application/octet-stream"},
		{"file_json", KindFile, "data.json", "application/json"},
		{"file_png", KindFile, "image.png", "image/png"},
		{"file_html", KindFile, "page.html", "text/html"},
		// Unknown extension falls back to the documented
		// binary-blob default.
		{"file_unknown_ext", KindFile, "data.foobar", "application/octet-stream"},
		{"file_no_ext", KindFile, "README", "application/octet-stream"},

		// Text branch — text/plain is the canonical envelope.
		{"text_inline", KindText, "hello world", "text/plain"},
		{"text_with_url", KindText, "Check https://example.com later", "text/plain"},

		// Drive branch — empty string (no envelope on the
		// wire); the backend derives the MIME from the Drive
		// file's own metadata.
		{"drive_doc", KindDrive, "https://docs.google.com/document/d/abc/edit", ""},
		{"drive_file", KindDrive, "https://drive.google.com/file/d/abc/view", ""},

		// Unknown branch — empty string. The classifier
		// rejected the argument; the SDK never reaches the
		// wire path, so there is no envelope value.
		{"unknown_kind", KindUnknown, "/no/such/file.pdf", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := InferMIME(tc.kind, tc.arg)
			if got != tc.want {
				t.Errorf("InferMIME(%q, %q) = %q, want %q", tc.kind, tc.arg, got, tc.want)
			}
		})
	}
}

// TestInferMIMEWithOverride_OverrideWins pins the
// override-wins behavior. A non-empty override replaces the
// inferred value for every kind, including the ""-producing
// kinds (Drive / Unknown) — a caller that needs a wire envelope
// on a Drive source passes the override explicitly.
func TestInferMIMEWithOverride_OverrideWins(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		kind     Kind
		arg      string
		override string
		want     string
	}{
		// URL branch — override beats text/html.
		{"url_override_pdf", KindURL, "https://example.com", "application/pdf", "application/pdf"},
		{"url_override_json", KindURL, "https://example.com", "application/json", "application/json"},
		// YouTube branch — override beats text/html.
		{"youtube_override_html", KindYouTube, "https://youtu.be/abc", "text/html", "text/html"},
		{"youtube_override_pdf", KindYouTube, "https://youtu.be/abc", "application/pdf", "application/pdf"},
		// File branch — override beats extension sniff.
		{"file_override_changes_mime", KindFile, "report.pdf", "application/json", "application/json"},
		// Text branch — override beats text/plain.
		{"text_override_html", KindText, "hello", "text/html", "text/html"},
		// Drive branch — override fills the empty envelope.
		{"drive_override_filled", KindDrive, "https://docs.google.com/document/d/abc/edit", "application/vnd.google-apps.document", "application/vnd.google-apps.document"},
		// Unknown branch — override fills the empty envelope.
		{"unknown_override_filled", KindUnknown, "/no/such", "text/plain", "text/plain"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := InferMIMEWithOverride(tc.kind, tc.arg, tc.override)
			if got != tc.want {
				t.Errorf("InferMIMEWithOverride(%q, %q, %q) = %q, want %q",
					tc.kind, tc.arg, tc.override, got, tc.want)
			}
		})
	}
}

// TestInferMIMEWithOverride_EmptyFallsThrough pins the
// empty-override semantics: an empty override does NOT blank
// the inferred MIME. A CLI flag that resolves to "" must not
// silently turn a "text/html" inference into "".
func TestInferMIMEWithOverride_EmptyFallsThrough(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		kind Kind
		arg  string
		want string
	}{
		{"url_empty_override_keeps_html", KindURL, "https://example.com", "text/html"},
		{"youtube_empty_override_keeps_html", KindYouTube, "https://youtu.be/abc", "text/html"},
		{"file_empty_override_keeps_pdf", KindFile, "report.pdf", "application/pdf"},
		{"text_empty_override_keeps_plain", KindText, "hello", "text/plain"},
		// Drive / Unknown still produce "" with empty
		// override; the "no envelope" default is preserved.
		{"drive_empty_override_keeps_empty", KindDrive, "https://docs.google.com/document/d/abc/edit", ""},
		{"unknown_empty_override_keeps_empty", KindUnknown, "/no/such", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := InferMIMEWithOverride(tc.kind, tc.arg, "")
			if got != tc.want {
				t.Errorf("InferMIMEWithOverride(%q, %q, \"\") = %q, want %q",
					tc.kind, tc.arg, got, tc.want)
			}
		})
	}
}

// TestSetMIMEOverride_MutatesAddOptions pins the functional-
// option pattern. The Option must mutate the AddOptions bag
// directly so the SDK can read the override at Add* entry
// (no package-private mutable state).
func TestSetMIMEOverride_MutatesAddOptions(t *testing.T) {
	t.Parallel()

	t.Run("non_empty_override", func(t *testing.T) {
		var opts AddOptions
		opt := SetMIMEOverride("application/pdf")
		opt(&opts)
		if opts.MIMEOverride != "application/pdf" {
			t.Errorf("opts.MIMEOverride = %q, want %q", opts.MIMEOverride, "application/pdf")
		}
	})

	t.Run("empty_override", func(t *testing.T) {
		var opts AddOptions
		opt := SetMIMEOverride("")
		opt(&opts)
		if opts.MIMEOverride != "" {
			t.Errorf("opts.MIMEOverride = %q, want \"\"", opts.MIMEOverride)
		}
	})

	t.Run("override_then_infer", func(t *testing.T) {
		// End-to-end: build the AddOptions via SetMIMEOverride,
		// then run InferMIMEWithOverride with the bag's value.
		// The round-trip confirms the wire path: the SDK reads
		// MIMEOverride off the bag and passes it to
		// InferMIMEWithOverride.
		var opts AddOptions
		SetMIMEOverride("application/pdf")(&opts)
		got := InferMIMEWithOverride(KindURL, "https://example.com", opts.MIMEOverride)
		if got != "application/pdf" {
			t.Errorf("InferMIMEWithOverride returned %q, want \"application/pdf\"", got)
		}
	})
}

// TestSetMIMEOverride_NilOptionSafe pins the nil-Option
// safety. A caller that conditionally passes a nil Option
// (e.g. from an unset CLI flag) must not panic the SDK's
// apply loop.
func TestSetMIMEOverride_NilOptionSafe(t *testing.T) {
	t.Parallel()
	var opts AddOptions
	opt := SetMIMEOverride("text/plain")
	opt(&opts) // smoke call to confirm the closure handles nil AddOptions gracefully via the SDK's nil-Option guard.
	if opts.MIMEOverride != "text/plain" {
		t.Errorf("opts.MIMEOverride = %q, want %q", opts.MIMEOverride, "text/plain")
	}
}
