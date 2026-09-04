// Package notebooklm — sources_add_text_file_drive_test.go.
//
// Tests for the T-S3-004c extensions of SourcesAPI: AddText,
// AddFile, AddDrive (plus their AddTextWithTitle /
// AddTextWithAddOptions / AddFileWithAddOptions /
// AddDriveWithAddOptions siblings).
//
// The tests follow the fakeSourcesExecutor pattern
// sources_test.go and sources_add_test.go use; the wire
// envelope produced by the SDK is asserted at the slot level
// so a regression in the text/file/drive branch dispatch
// surfaces before a cassette replay would.
package notebooklm

import (
	"context"
	"testing"

	apperrors "github.com/raihankhan/notebooklm-go/internal/app/errors"
	"github.com/raihankhan/notebooklm-go/internal/app/sourceadd"
	"github.com/raihankhan/notebooklm-go/internal/web/wire"
)

// (writeFile is declared in client_test.go — this test file
// reuses the existing helper for the file-add tests that
// need a real file on disk so the sourceadd symlink gate
// passes.)

// TestSourcesAPI_AddText_Dispatch — AddText routes through
// the same AddSources RPC; the wire envelope has the
// [title, content] pair at slot 1 and the type marker `2`
// at slot 3 (the text-branch discriminator). Default MIME
// "text/plain" at slot 1[1] (the content element of the pair).
func TestSourcesAPI_AddText_Dispatch(t *testing.T) {
	const okBody = `[["src-text","Pasted text",[null,null,null,null,3,null,null,null],[null,2]]]`
	fake := &fakeSourcesExecutor{
		canned: map[wire.Method]string{
			wire.MethodAddSource: okBody,
		},
	}
	c := newClientWithFakeSources(t, fake)

	src, err := c.Sources().AddText(
		context.Background(),
		"nb-1",
		"Hello world",
	)
	if err != nil {
		t.Fatalf("AddText: %v", err)
	}
	if src.ID != "src-text" {
		t.Errorf("src.ID = %q, want src-text", src.ID)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("Execute calls = %d, want 1", len(fake.calls))
	}
	if fake.calls[0].method != wire.MethodAddSource {
		t.Errorf("call[0] method = %v, want MethodAddSource", fake.calls[0].method)
	}
	addParams, ok := fake.calls[0].params.([]any)
	if !ok || len(addParams) != 3 {
		t.Fatalf("AddSource params = %T len=%d, want []any len=3", fake.calls[0].params, len(addParams))
	}
	spec, ok := addParams[0].([]any)[0].([]any)
	if !ok {
		t.Fatalf("AddSource spec = %T, want []any", addParams[0].([]any)[0])
	}
	if len(spec) != 11 {
		t.Fatalf("spec len = %d, want 11", len(spec))
	}
	// The [title, content] pair rides at slot 1.
	pair, ok := spec[1].([]any)
	if !ok || len(pair) != 2 {
		t.Fatalf("spec slot 1 = %T len=%d, want []any len=2", spec[1], len(pair))
	}
	if pair[0] != "" {
		t.Errorf("spec slot 1[0] (title) = %v, want \"\" (empty title by default)", pair[0])
	}
	if pair[1] != "Hello world" {
		t.Errorf("spec slot 1[1] (content) = %v, want \"Hello world\"", pair[1])
	}
	// Type marker `2` at slot 3.
	if spec[3] != 2 {
		t.Errorf("spec slot 3 (type marker) = %v, want 2", spec[3])
	}
	// Source-type code at slot 10.
	if spec[10] != 1 {
		t.Errorf("spec slot 10 = %v, want 1", spec[10])
	}
}

// TestSourcesAPI_AddTextWithTitle — AddTextWithTitle passes
// the title through to wire-spec slot 1[0].
func TestSourcesAPI_AddTextWithTitle(t *testing.T) {
	const okBody = `[["src-text","My Title",[null,null,null,null,3,null,null,null],[null,2]]]`
	fake := &fakeSourcesExecutor{
		canned: map[wire.Method]string{
			wire.MethodAddSource: okBody,
		},
	}
	c := newClientWithFakeSources(t, fake)

	if _, err := c.Sources().AddTextWithTitle(
		context.Background(),
		"nb-1",
		"My Title",
		"Hello world",
	); err != nil {
		t.Fatalf("AddTextWithTitle: %v", err)
	}
	addParams := fake.calls[0].params.([]any)
	spec := addParams[0].([]any)[0].([]any)
	pair, ok := spec[1].([]any)
	if !ok || pair[0] != "My Title" {
		t.Errorf("spec slot 1[0] (title) = %v, want \"My Title\"", spec[1])
	}
}

// TestSourcesAPI_AddTextWithMIMEOverride — the Text branch
// does NOT carry the MIME envelope on the wire spec (the
// spec is `[null, [title, content], null, 2, null×6, 1]` —
// slot 1 is the [title, content] pair, NOT a MIME envelope).
// SetMIMEOverride is captured by the SDK but has no effect
// on the wire envelope today; the override is forwarded
// upstream in a future ticket (T-S3-005a or follow-up).
// The test pins the no-op-on-wire behavior so a future
// ticket that adds a Text-branch MIME slot has to update
// this assertion.
func TestSourcesAPI_AddTextWithMIMEOverride(t *testing.T) {
	const okBody = `[["src-text","Pasted text",[null,null,null,null,3,null,null,null],[null,2]]]`
	fake := &fakeSourcesExecutor{
		canned: map[wire.Method]string{
			wire.MethodAddSource: okBody,
		},
	}
	c := newClientWithFakeSources(t, fake)

	if _, err := c.Sources().AddTextWithAddOptions(
		context.Background(),
		"nb-1",
		"Hello world",
		[]sourceadd.AddOption{sourceadd.SetMIMEOverride("text/html")},
	); err != nil {
		t.Fatalf("AddTextWithAddOptions: %v", err)
	}
	addParams := fake.calls[0].params.([]any)
	spec := addParams[0].([]any)[0].([]any)
	pair := spec[1].([]any)
	// Spec slot 1[1] carries the raw content (NOT the
	// override); the override is captured for upstream
	// phases but does not appear on this spec slot.
	if pair[1] != "Hello world" {
		t.Errorf("spec slot 1[1] (content) = %v, want \"Hello world\" (Text branch has no MIME on spec)", pair[1])
	}
}

// TestSourcesAPI_AddText_BadContent — empty / control-
// character content is rejected with apperrors.
// CodeValidationError envelope.
func TestSourcesAPI_AddText_BadContent(t *testing.T) {
	fake := &fakeSourcesExecutor{}
	c := newClientWithFakeSources(t, fake)

	cases := []string{"", "  ", "hello\nworld"}
	for _, bad := range cases {
		_, err := c.Sources().AddText(context.Background(), "nb-1", bad)
		if err == nil {
			t.Errorf("AddText(%q) returned nil err; want validation error", bad)
			continue
		}
		code, _ := apperrors.Classify(err)
		if code != apperrors.CodeValidationError {
			t.Errorf("AddText(%q) code = %q, want %q", bad, code, apperrors.CodeValidationError)
		}
	}
}

// TestSourcesAPI_AddText_NilClient — AddText on a nil
// Client returns a typed error without dispatching.
func TestSourcesAPI_AddText_NilClient(t *testing.T) {
	var c *Client
	if c.Sources() == nil {
		t.Fatal("Sources() on nil Client returned nil SourcesAPI")
	}
	_, err := c.Sources().AddText(context.Background(), "nb-1", "hello")
	if err == nil {
		t.Error("AddText on nil Client returned nil err")
	}
}

// TestSourcesAPI_AddFile_Dispatch — AddFile routes through
// the AddSourceFile RPC (`o4cbdc`), with the filename wrapped
// in a single-element list at outer slot 0. The MIME override
// has no effect on the wire envelope today (it rides on the
// upload header in T-S3-005a).
func TestSourcesAPI_AddFile_Dispatch(t *testing.T) {
	const okBody = `[["src-file","report.pdf",[null,null,null,null,4,null,null,null],[null,2]]]`
	fake := &fakeSourcesExecutor{
		canned: map[wire.Method]string{
			wire.MethodAddSourceFile: okBody,
		},
	}
	c := newClientWithFakeSources(t, fake)

	// Create a real temp file so the sourceadd symlink gate
	// (which calls filepath.EvalSymlinks) passes. The file
	// content is irrelevant — the SDK only registers the
	// filename on the wire; the actual file bytes stream in
	// the upload phase (T-S3-005a).
	tmpDir := t.TempDir()
	filePath := tmpDir + "/report.pdf"
	if err := writeFile(filePath, []byte("fake pdf content"), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	src, err := c.Sources().AddFile(
		context.Background(),
		"nb-1",
		filePath, // real path → wire carries base name only
	)
	if err != nil {
		t.Fatalf("AddFile: %v", err)
	}
	if src.ID != "src-file" {
		t.Errorf("src.ID = %q, want src-file", src.ID)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("Execute calls = %d, want 1", len(fake.calls))
	}
	if fake.calls[0].method != wire.MethodAddSourceFile {
		t.Errorf("call[0] method = %v, want MethodAddSourceFile", fake.calls[0].method)
	}
	addParams, ok := fake.calls[0].params.([]any)
	if !ok || len(addParams) != 3 {
		t.Fatalf("AddSourceFile params = %T len=%d, want []any len=3", fake.calls[0].params, len(addParams))
	}
	// The filename rides at outer slot 0 wrapped in a
	// single-element list — `[["report.pdf"]]`.
	outer0, ok := addParams[0].([]any)
	if !ok || len(outer0) != 1 {
		t.Fatalf("AddSourceFile outer slot 0 = %T len=%d, want []any len=1", addParams[0], len(outer0))
	}
	inner, ok := outer0[0].([]any)
	if !ok || len(inner) != 1 || inner[0] != "report.pdf" {
		t.Errorf("AddSourceFile outer slot 0 inner = %v, want [\"report.pdf\"]", inner)
	}
}

// TestSourcesAPI_AddFile_BadPath — empty / control-character
// paths are rejected with apperrors.CodeValidationError.
func TestSourcesAPI_AddFile_BadPath(t *testing.T) {
	fake := &fakeSourcesExecutor{}
	c := newClientWithFakeSources(t, fake)

	cases := []string{"", "  "}
	for _, bad := range cases {
		_, err := c.Sources().AddFile(context.Background(), "nb-1", bad)
		if err == nil {
			t.Errorf("AddFile(%q) returned nil err; want validation error", bad)
			continue
		}
		code, _ := apperrors.Classify(err)
		if code != apperrors.CodeValidationError {
			t.Errorf("AddFile(%q) code = %q, want %q", bad, code, apperrors.CodeValidationError)
		}
	}
}

// TestSourcesAPI_AddFile_NilClient — AddFile on a nil
// Client returns a typed error.
func TestSourcesAPI_AddFile_NilClient(t *testing.T) {
	var c *Client
	_, err := c.Sources().AddFile(context.Background(), "nb-1", "/tmp/foo.pdf")
	if err == nil {
		t.Error("AddFile on nil Client returned nil err")
	}
}

// TestSourcesAPI_AddDrive_Dispatch — AddDrive routes through
// the AddSources RPC (`izAoDd`), with the [fileID, mimeType,
// 1, title] quad at source-spec slot 0 (the Drive-branch
// discriminator). The wire envelope uses the legacy 4-element
// tail — slot 2 is the literal `[2]` and slot 3 is the
// legacy `[1, null×8, [1]]` tail (NOT the fresh TPL block).
func TestSourcesAPI_AddDrive_Dispatch(t *testing.T) {
	const okBody = `[["src-drive","My Doc",[null,null,null,null,5,null,null,null],[null,2]]]`
	fake := &fakeSourcesExecutor{
		canned: map[wire.Method]string{
			wire.MethodAddSource: okBody,
		},
	}
	c := newClientWithFakeSources(t, fake)

	shareURL := "https://drive.google.com/file/d/file-id-abc123/view"
	src, err := c.Sources().AddDrive(
		context.Background(),
		"nb-1",
		shareURL,
	)
	if err != nil {
		t.Fatalf("AddDrive: %v", err)
	}
	if src.ID != "src-drive" {
		t.Errorf("src.ID = %q, want src-drive", src.ID)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("Execute calls = %d, want 1", len(fake.calls))
	}
	if fake.calls[0].method != wire.MethodAddSource {
		t.Errorf("call[0] method = %v, want MethodAddSource", fake.calls[0].method)
	}
	addParams, ok := fake.calls[0].params.([]any)
	if !ok || len(addParams) != 4 {
		t.Fatalf("AddSource params = %T len=%d, want []any len=4 (legacy 4-element tail)", fake.calls[0].params, len(addParams))
	}
	// Outer slot 2 is the literal [2].
	slot2, ok := addParams[2].([]any)
	if !ok || len(slot2) != 1 || slot2[0] != 2 {
		t.Errorf("AddSource outer slot 2 = %v, want [2]", addParams[2])
	}
	// Outer slot 3 is the legacy [1, null×8, [1]] tail.
	slot3, ok := addParams[3].([]any)
	if !ok || len(slot3) != 10 {
		t.Fatalf("AddSource outer slot 3 = %T len=%d, want []any len=10", addParams[3], len(slot3))
	}
	if slot3[0] != 1 {
		t.Errorf("AddSource outer slot 3[0] = %v, want 1", slot3[0])
	}
	tail, ok := slot3[9].([]any)
	if !ok || len(tail) != 1 || tail[0] != 1 {
		t.Errorf("AddSource outer slot 3[9] = %v, want [1]", slot3[9])
	}
	// The 11-slot spec carries the Drive quad at slot 0.
	spec, ok := addParams[0].([]any)[0].([]any)
	if !ok || len(spec) != 11 {
		t.Fatalf("spec = %T len=%d, want []any len=11", spec, len(spec))
	}
	quad, ok := spec[0].([]any)
	if !ok || len(quad) != 4 {
		t.Fatalf("spec slot 0 = %T len=%d, want []any len=4", spec[0], len(quad))
	}
	if quad[0] != "file-id-abc123" {
		t.Errorf("spec slot 0[0] (fileID) = %v, want \"file-id-abc123\"", quad[0])
	}
	if quad[2] != 1 {
		t.Errorf("spec slot 0[2] (Drive source-type code) = %v, want 1", quad[2])
	}
	if spec[10] != 1 {
		t.Errorf("spec slot 10 (source-type code) = %v, want 1", spec[10])
	}
}

// TestSourcesAPI_AddDrive_BadURL — empty / whitespace /
// control-character URLs are rejected with
// apperrors.CodeValidationError.
func TestSourcesAPI_AddDrive_BadURL(t *testing.T) {
	fake := &fakeSourcesExecutor{}
	c := newClientWithFakeSources(t, fake)

	cases := []string{"", "  ", "file id with space"}
	for _, bad := range cases {
		_, err := c.Sources().AddDrive(context.Background(), "nb-1", bad)
		if err == nil {
			t.Errorf("AddDrive(%q) returned nil err; want validation error", bad)
			continue
		}
		code, _ := apperrors.Classify(err)
		if code != apperrors.CodeValidationError {
			t.Errorf("AddDrive(%q) code = %q, want %q", bad, code, apperrors.CodeValidationError)
		}
	}
}

// TestSourcesAPI_AddDrive_NilClient — AddDrive on a nil
// Client returns a typed error.
func TestSourcesAPI_AddDrive_NilClient(t *testing.T) {
	var c *Client
	_, err := c.Sources().AddDrive(context.Background(), "nb-1", "https://drive.google.com/file/d/abc/view")
	if err == nil {
		t.Error("AddDrive on nil Client returned nil err")
	}
}

// TestExtractDriveFileID — the minimal Drive-id extraction
// helper the SDK runs before dispatch. The URL shapes covered
// here are the ones the SDK accepts (drive.google.com/file/d,
// drive.google.com/document/d, docs.google.com/{document,
// spreadsheets,presentation}/d).
func TestExtractDriveFileID(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want string
	}{
		{"file_view", "https://drive.google.com/file/d/file-id-abc/view", "file-id-abc"},
		{"file_view_with_query", "https://drive.google.com/file/d/abc123/view?usp=sharing", "abc123"},
		{"document_edit", "https://docs.google.com/document/d/doc-id-456/edit", "doc-id-456"},
		{"spreadsheet_edit", "https://docs.google.com/spreadsheets/d/sheet-id-789/edit", "sheet-id-789"},
		{"presentation_edit", "https://docs.google.com/presentation/d/slide-id-012/edit", "slide-id-012"},
		// No /d/ segment → fallback to the raw URL.
		{"no_d_segment", "https://example.com/abc", "https://example.com/abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractDriveFileID(tc.url)
			if got != tc.want {
				t.Errorf("extractDriveFileID(%q) = %q, want %q", tc.url, got, tc.want)
			}
		})
	}
}
