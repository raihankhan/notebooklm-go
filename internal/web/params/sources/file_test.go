// Tests for the sources params File builder.
//
// The Python original (_web/sources/add.py::register_file_source)
// is the normative wire-shape source — every builder's output
// here must match what `wire.Marshal` would emit for the
// equivalent positional literal in Python. We assert the byte
// output below as golden strings.
//
// The File branch is the 3-slot envelope `[[filename]],
// notebook_id, TPL` — distinct from the URL / YouTube / Text /
// Drive branches which use a 3-slot envelope with a source-spec
// inner list. The File branch's spec is just the filename
// wrapped in a single-element list (the wire envelope requires
// the wrapping); the actual file bytes stream via Scotty upload
// (T-S3-005a), not on the spec.
package sources

import (
	"testing"

	"github.com/raihankhan/notebooklm-go/internal/web/params"
)

// TestBuildAddSourceFile_Bytes — the golden encode test
// pinned against Python's `register_file_source` literal. The
// 3-slot envelope carries [[filename]] at slot 0, the
// notebook id at slot 1, and the fresh template block at
// slot 2. The MIME override is forwarded upstream to the
// upload phase (T-S3-005a) and does NOT ride on the wire
// spec — the spec is filename-only.
func TestBuildAddSourceFile_Bytes(t *testing.T) {
	got, err := params.Encode(func() []any {
		return BuildAddSourceFile("nb-1", "report.pdf")
	})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	want := `[[["report.pdf"]],"nb-1",[2,null,null,[1,null,null,null,null,null,null,null,null,null,[1]]]]`
	if string(got) != want {
		t.Fatalf("BuildAddSourceFile bytes differ\n got: %s\nwant: %s", got, want)
	}
}

// TestBuildAddSourceFile_Shape — the 3-slot envelope
// slot-by-slot inspection. Slot 0 carries [[filename]];
// slot 1 is the notebook id; slot 2 is the template block.
func TestBuildAddSourceFile_Shape(t *testing.T) {
	got := BuildAddSourceFile("nb-1", "report.pdf")
	if len(got) != 3 {
		t.Fatalf("BuildAddSourceFile outer len = %d, want 3", len(got))
	}
	if got[1] != "nb-1" {
		t.Errorf("BuildAddSourceFile outer slot 1 = %v, want \"nb-1\"", got[1])
	}
	// Slot 0 must be wrapped in a single-element list whose
	// only element is also a single-element list containing the
	// filename — the double-wrap is the wire-stable shape.
	outer0, ok := got[0].([]any)
	if !ok || len(outer0) != 1 {
		t.Fatalf("BuildAddSourceFile outer slot 0 = %v, want [[filename]]", got[0])
	}
	inner, ok := outer0[0].([]any)
	if !ok || len(inner) != 1 || inner[0] != "report.pdf" {
		t.Errorf("BuildAddSourceFile slot 0 inner = %v, want [\"report.pdf\"]", inner)
	}
}

// TestBuildAddSourceFile_TemplateBlockFresh —
// wire.TemplateBlock must return a fresh slice every call.
func TestBuildAddSourceFile_TemplateBlockFresh(t *testing.T) {
	a := BuildAddSourceFile("alpha", "a.pdf")
	a[2].([]any)[3].([]any)[0] = "MUTATED"
	b := BuildAddSourceFile("beta", "b.pdf")
	if b[2].([]any)[3].([]any)[0] != 1 {
		t.Fatalf("BuildAddSourceFile shared template block; got %v", b[2])
	}
}

// TestValidateFile_Accepts — clean file paths accepted.
// The pre-flight only catches the wire-level mistakes (empty,
// whitespace, control characters); the symlink gate
// (sourceadd.Validate) is what rejects paths inside the
// operator's storage root.
func TestValidateFile_Accepts(t *testing.T) {
	cases := []string{
		"report.pdf",
		"/tmp/report.pdf",
		"./report.pdf",
		"~/report.pdf",
	}
	for _, c := range cases {
		if err := ValidateFile(c); err != nil {
			t.Errorf("ValidateFile(%q) = %v, want nil", c, err)
		}
	}
}

// TestValidateFile_Rejects — empty / whitespace / control
// character / dot-only inputs rejected.
func TestValidateFile_Rejects(t *testing.T) {
	cases := []string{
		"",
		"   ",
		// A path that resolves to "." (the current directory)
		// is meaningless as a source filename.
		".",
		// Control characters.
		"report\n.pdf",
	}
	for _, c := range cases {
		if err := ValidateFile(c); err == nil {
			t.Errorf("ValidateFile(%q) returned nil err; want validation error", c)
		}
	}
}

// TestBuilderCoverageCalls_File — exercises every Go builder
// directly so the coverage tool sees the function bodies.
func TestBuilderCoverageCalls_File(t *testing.T) {
	_ = BuildAddSourceFile("nb-1", "report.pdf")
	if err := ValidateFile("report.pdf"); err != nil {
		t.Fatalf("ValidateFile accepted clean path: %v", err)
	}
}
