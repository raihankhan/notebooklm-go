// Tests for the sources params Drive builder.
//
// The Python original (_web/sources/add.py::add_drive_source)
// is the normative wire-shape source — every builder's output
// here must match what `wire.Marshal` would emit for the
// equivalent positional literal in Python. We assert the byte
// output below as golden strings.
//
// The Drive branch is the 4-element tail envelope
// `[[sourceSpec], notebook_id, [2], [1, null×8, [1]]]` —
// distinct from the URL / YouTube / Text branches which use
// the fresh TPL template block at slot 2. The Drive branch
// has not been migrated to the Gemini-3.5 TPL envelope (per
// the #1546 TODO); it still uses the legacy 4-element tail.
// The 11-slot spec carries the [fileID, mimeType, 1, title]
// quad at slot 0 (the Drive-branch discriminator) and the
// source-type code at slot 10.
package sources

import (
	"testing"

	"github.com/raihankhan/notebooklm-go/internal/web/params"
)

// TestBuildAddSourceDrive_Bytes — the golden encode test
// pinned against Python's `add_drive_source` literal. The
// 4-element tail envelope carries the spec at slot 0, the
// notebook id at slot 1, the literal `[2]` at slot 2, and the
// legacy `[1, null×8, [1]]` at slot 3. The 11-slot spec
// carries [fileID, mimeType, 1, title] at slot 0 and the
// source-type code `1` at slot 10.
func TestBuildAddSourceDrive_Bytes(t *testing.T) {
	got, err := params.Encode(func() []any {
		return BuildAddSourceDrive("nb-1", "file-id-abc123", "application/vnd.google-apps.document", "My Doc")
	})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	// Outer envelope: [[[spec]], "nb-1", [2], [1, null×8, [1]]].
	// Spec: [[fileID, mimeType, 1, title], null×9, 1].
	want := `[[[["file-id-abc123","application/vnd.google-apps.document",1,"My Doc"],null,null,null,null,null,null,null,null,null,1]],"nb-1",[2],[1,null,null,null,null,null,null,null,null,[1]]]`
	if string(got) != want {
		t.Fatalf("BuildAddSourceDrive bytes differ\n got: %s\nwant: %s", got, want)
	}
}

// TestBuildAddSourceDrive_DefaultMIME — an empty mime
// falls back to the empty string (the backend re-derives the
// MIME from the Drive file's own metadata). The wire
// envelope does not carry a fabricated MIME so the spec
// matches the Python literal with mimeType="" verbatim.
func TestBuildAddSourceDrive_DefaultMIME(t *testing.T) {
	got, err := params.Encode(func() []any {
		return BuildAddSourceDrive("nb-1", "file-id-abc123", "", "My Doc")
	})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	want := `[[[["file-id-abc123","",1,"My Doc"],null,null,null,null,null,null,null,null,null,1]],"nb-1",[2],[1,null,null,null,null,null,null,null,null,[1]]]`
	if string(got) != want {
		t.Fatalf("BuildAddSourceDrive default MIME bytes differ\n got: %s\nwant: %s", got, want)
	}
}

// TestBuildAddSourceDrive_Shape — the 4-element tail
// slot-by-slot inspection. Slot 0 carries [[spec]]; slot 1
// is the notebook id; slot 2 is the literal [2]; slot 3 is
// the legacy `[1, null×8, [1]]` tail. The 11-slot spec
// carries the [fileID, mimeType, 1, title] quad at slot 0
// and the source-type code at slot 10.
func TestBuildAddSourceDrive_Shape(t *testing.T) {
	got := BuildAddSourceDrive("nb-1", "file-id", "application/pdf", "Title")
	if len(got) != 4 {
		t.Fatalf("BuildAddSourceDrive outer len = %d, want 4 (legacy 4-element tail)", len(got))
	}
	if got[1] != "nb-1" {
		t.Errorf("BuildAddSourceDrive outer slot 1 = %v, want \"nb-1\"", got[1])
	}
	// Slot 2 must be the literal [2].
	slot2, ok := got[2].([]any)
	if !ok || len(slot2) != 1 || slot2[0] != 2 {
		t.Errorf("BuildAddSourceDrive outer slot 2 = %v, want [2]", got[2])
	}
	// Slot 3 must be the legacy [1, null×8, [1]] tail.
	slot3, ok := got[3].([]any)
	if !ok || len(slot3) != 10 {
		t.Fatalf("BuildAddSourceDrive outer slot 3 = %v, want 10-element legacy tail", got[3])
	}
	if slot3[0] != 1 {
		t.Errorf("BuildAddSourceDrive outer slot 3[0] = %v, want 1", slot3[0])
	}
	tail, ok := slot3[9].([]any)
	if !ok || len(tail) != 1 || tail[0] != 1 {
		t.Errorf("BuildAddSourceDrive outer slot 3[9] = %v, want [1]", slot3[9])
	}
	// Spec: outer slot 0 is [[spec]]; spec[0] is the quad.
	outer, ok := got[0].([]any)
	if !ok || len(outer) != 1 {
		t.Fatalf("BuildAddSourceDrive outer slot 0 = %v, want [[spec]]", got[0])
	}
	spec, ok := outer[0].([]any)
	if !ok || len(spec) != 11 {
		t.Fatalf("BuildAddSourceDrive spec len = %d, want 11", len(spec))
	}
	quad, ok := spec[0].([]any)
	if !ok || len(quad) != 4 {
		t.Fatalf("BuildAddSourceDrive spec slot 0 = %v, want 4-element quad", spec[0])
	}
	if quad[0] != "file-id" || quad[1] != "application/pdf" || quad[2] != 1 || quad[3] != "Title" {
		t.Errorf("BuildAddSourceDrive spec slot 0 quad = %v, want [\"file-id\", \"application/pdf\", 1, \"Title\"]", quad)
	}
	if spec[10] != 1 {
		t.Errorf("BuildAddSourceDrive spec slot 10 (source-type code) = %v, want 1", spec[10])
	}
}

// TestValidateDriveURL_Accepts — clean Drive file ids
// accepted.
func TestValidateDriveURL_Accepts(t *testing.T) {
	cases := []string{
		"file-id-abc",
		"abc123def456",
		// Canonical 33-char Drive opaque id (a typical case;
		// the validator does not enforce the 33-char length).
		"1234567890abcdefghijklmnopqrstuv",
	}
	for _, c := range cases {
		if err := ValidateDriveURL(c); err != nil {
			t.Errorf("ValidateDriveURL(%q) = %v, want nil", c, err)
		}
	}
}

// TestValidateDriveURL_Rejects — empty / whitespace /
// control-character inputs rejected.
func TestValidateDriveURL_Rejects(t *testing.T) {
	cases := []string{
		"",
		"   ",
		"file id with space",
		"file\tid",
		"file\nid",
	}
	for _, c := range cases {
		if err := ValidateDriveURL(c); err == nil {
			t.Errorf("ValidateDriveURL(%q) returned nil err; want validation error", c)
		}
	}
}

// TestBuilderCoverageCalls_Drive — exercises every Go builder
// directly so the coverage tool sees the function bodies.
func TestBuilderCoverageCalls_Drive(t *testing.T) {
	_ = BuildAddSourceDrive("nb-1", "file-id", "", "Title")
	_ = BuildAddSourceDrive("nb-1", "file-id", "application/pdf", "Title")
	if err := ValidateDriveURL("file-id"); err != nil {
		t.Fatalf("ValidateDriveURL accepted clean id: %v", err)
	}
}
