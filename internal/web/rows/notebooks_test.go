// Tests for the notebook row decoder.
//
// Coverage target: 100% on rows/notebooks.go. Every positional slot
// in the contract table is exercised by at least one test below.
package rows

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// makeRow assembles a minimal `Project` row for tests. The default
// values produce the "empty notebook" envelope; callers override the
// slots they want to exercise.
func makeRow(opts ...func(*[]any)) []any {
	row := []any{
		"Title", // 0: title
		nil,     // 1: sources list
		"nb-1",  // 2: notebook id
		nil,     // 3: emoji
		nil,     // 4: (unused)
		nil,     // 5: meta block
	}
	for _, opt := range opts {
		opt(&row)
	}
	return row
}

// withMeta swaps the row's meta block (slot 5) for the supplied value.
// Most typed fields ride inside meta; this helper keeps the test body
// focused on the field under test.
func withMeta(meta []any) func(*[]any) {
	return func(row *[]any) {
		(*row)[5] = meta
	}
}

// TestDecodeNotebook_RequiredFields — the load-bearing title and id
// slots. A row that omits either still decodes (the short-row tolerance),
// but the typed field stays empty.
func TestDecodeNotebook_RequiredFields(t *testing.T) {
	row := []any{"Hello World", nil, "nb-123", nil, nil, nil}
	nb, err := DecodeNotebook(row, "")
	if err != nil {
		t.Fatalf("DecodeNotebook: %v", err)
	}
	if nb.ID != "nb-123" {
		t.Errorf("nb.ID = %q, want nb-123", nb.ID)
	}
	if nb.Title != "Hello World" {
		t.Errorf("nb.Title = %q, want Hello World", nb.Title)
	}
}

// TestDecodeNotebook_ThoughtStripped — the leading "thought\n" prefix
// the Python original strips. Matches `_web/rows/notebooks.py` line 171.
func TestDecodeNotebook_ThoughtStripped(t *testing.T) {
	row := []any{"thought\nHello World", nil, "nb-1", nil, nil, nil}
	nb, _ := DecodeNotebook(row, "")
	if nb.Title != "Hello World" {
		t.Errorf("title not stripped: %q", nb.Title)
	}
}

// TestDecodeNotebook_TitleTrimmed — surrounding whitespace is stripped
// alongside the "thought\n" sentinel.
func TestDecodeNotebook_TitleTrimmed(t *testing.T) {
	row := []any{"  Hello World  \n", nil, "nb-1", nil, nil, nil}
	nb, _ := DecodeNotebook(row, "")
	if nb.Title != "Hello World" {
		t.Errorf("title not trimmed: %q", nb.Title)
	}
}

// TestDecodeNotebook_ShortRow — a row missing the title or id slots
// degrades to an empty field rather than raising. The short-row
// tolerance is the documented "absent slot" contract.
func TestDecodeNotebook_ShortRow(t *testing.T) {
	row := []any{}
	nb, err := DecodeNotebook(row, "")
	if err != nil {
		t.Fatalf("DecodeNotebook empty row: %v", err)
	}
	if nb.Title != "" || nb.ID != "" {
		t.Errorf("nb = %+v, want empty ID and Title", nb)
	}
}

// TestDecodeNotebook_Nil — nil payload decodes to a zero Notebook.
func TestDecodeNotebook_Nil(t *testing.T) {
	nb, err := DecodeNotebook(nil, "")
	if err != nil {
		t.Fatalf("DecodeNotebook nil: %v", err)
	}
	if nb.Title != "" || nb.ID != "" {
		t.Errorf("nil payload decoded to %+v, want zero", nb)
	}
}

// TestDecodeNotebook_RoleOwner_NotShared — role=1 (OWNER) means the
// notebook is NOT shared with the calling user.
func TestDecodeNotebook_RoleOwner_NotShared(t *testing.T) {
	row := makeRow(withMeta([]any{1}))
	nb, _ := DecodeNotebook(row, "")
	if nb.IsShared {
		t.Errorf("IsShared = true with role=1 (owner); want false")
	}
	if nb.Metadata == nil || nb.Metadata.Role == nil || *nb.Metadata.Role != 1 {
		t.Errorf("Role = %+v, want &1", nb.Metadata)
	}
}

// TestDecodeNotebook_RoleEditor_IsShared — role=2 (EDITOR) means the
// notebook IS shared.
func TestDecodeNotebook_RoleEditor_IsShared(t *testing.T) {
	row := makeRow(withMeta([]any{2}))
	nb, _ := DecodeNotebook(row, "")
	if !nb.IsShared {
		t.Errorf("IsShared = false with role=2; want true")
	}
}

// TestDecodeNotebook_RoleViewer_IsShared — role=3 (VIEWER) also means
// shared.
func TestDecodeNotebook_RoleViewer_IsShared(t *testing.T) {
	row := makeRow(withMeta([]any{3}))
	nb, _ := DecodeNotebook(row, "")
	if !nb.IsShared {
		t.Errorf("IsShared = false with role=3; want true")
	}
}

// TestDecodeNotebook_CreatedAt — the meta[8][0] slot carries the
// creation unix-seconds; we decode it as time.Time.
func TestDecodeNotebook_CreatedAt(t *testing.T) {
	const secs = int64(1700000000)
	row := makeRow(withMeta([]any{
		nil,                // 0: role
		nil, nil, nil, nil, // 1-4: padding
		nil,      // 5: viewedAt (empty here)
		nil, nil, // 6-7: padding
		[]any{secs}, // 8: createdAt
	}))
	nb, _ := DecodeNotebook(row, "")
	if nb.CreatedAt == nil {
		t.Fatal("CreatedAt nil; want time")
	}
	want := time.Unix(secs, 0).UTC()
	if !nb.CreatedAt.Equal(want) {
		t.Errorf("CreatedAt = %v, want %v", nb.CreatedAt, want)
	}
}

// TestDecodeNotebook_LastViewedAt — meta[5][0] slot.
func TestDecodeNotebook_LastViewedAt(t *testing.T) {
	const secs = int64(1750000000)
	row := makeRow(withMeta([]any{
		nil,                // 0: role
		nil, nil, nil, nil, // 1-4
		[]any{secs}, // 5: viewedAt
		nil, nil,    // 6-7
		nil, // 8: createdAt
	}))
	nb, _ := DecodeNotebook(row, "")
	if nb.Metadata == nil || nb.Metadata.LastViewedAt == nil {
		t.Fatal("LastViewedAt nil; want time")
	}
	want := time.Unix(secs, 0).UTC()
	if !nb.Metadata.LastViewedAt.Equal(want) {
		t.Errorf("LastViewedAt = %v, want %v", nb.Metadata.LastViewedAt, want)
	}
}

// TestDecodeNotebook_ZeroTimestampIsNil — the wire writes 0 to mean
// "no claim"; we must not surface the Unix epoch as a real timestamp.
func TestDecodeNotebook_ZeroTimestampIsNil(t *testing.T) {
	row := makeRow(withMeta([]any{
		nil,                // 0: role
		nil, nil, nil, nil, // 1-4
		[]any{0}, // 5: viewedAt = 0 (absent)
		nil, nil, // 6-7
		[]any{0}, // 8: createdAt = 0 (absent)
	}))
	nb, _ := DecodeNotebook(row, "")
	if nb.CreatedAt != nil {
		t.Errorf("CreatedAt = %v, want nil for unix-0", nb.CreatedAt)
	}
	if nb.Metadata.LastViewedAt != nil {
		t.Errorf("LastViewedAt = %v, want nil for unix-0", nb.Metadata.LastViewedAt)
	}
}

// TestDecodeNotebook_Emoji — the top-level row[3] slot carries the
// notebook's display emoji.
func TestDecodeNotebook_Emoji(t *testing.T) {
	row := []any{"Title", nil, "nb-1", "📓", nil, nil}
	nb, _ := DecodeNotebook(row, "")
	if nb.Metadata == nil || nb.Metadata.Emoji == nil {
		t.Fatal("Emoji nil; want pointer to string")
	}
	if *nb.Metadata.Emoji != "📓" {
		t.Errorf("Emoji = %q, want 📓", *nb.Metadata.Emoji)
	}
}

// TestDecodeNotebook_SourcesCount — slot 1 is the source list; the
// typed view exposes its length only.
func TestDecodeNotebook_SourcesCount(t *testing.T) {
	row := makeRow()
	row[1] = []any{"s1", "s2", "s3"}
	nb, _ := DecodeNotebook(row, "")
	if nb.Metadata == nil {
		t.Fatal("Metadata nil; want populated")
	}
	if nb.Metadata.SourcesCount != 3 {
		t.Errorf("SourcesCount = %d, want 3", nb.Metadata.SourcesCount)
	}
}

// TestDecodeNotebook_SummaryPassthrough — the optional summary
// argument flows into the typed Summary field without modification.
func TestDecodeNotebook_SummaryPassthrough(t *testing.T) {
	nb, _ := DecodeNotebook(nil, "AI-generated summary text.")
	if nb.Summary != "AI-generated summary text." {
		t.Errorf("Summary = %q, want AI-generated summary text.", nb.Summary)
	}
}

// TestValidateNotebookID — the input guard every notebook-scoped
// builder shares. A non-empty, non-whitespace string passes; the
// others fail.
func TestValidateNotebookID(t *testing.T) {
	cases := []struct {
		id    string
		valid bool
	}{
		{"nb-1", true},
		{"abc_def-123", true},
		{"", false},
		{" ", false},
		{"\tnb-1", false},
		{"nb-1\n", false},
	}
	for _, c := range cases {
		err := ValidateNotebookID(c.id)
		if c.valid && err != nil {
			t.Errorf("ValidateNotebookID(%q) = %v, want nil", c.id, err)
		}
		if !c.valid && err == nil {
			t.Errorf("ValidateNotebookID(%q) returned nil err; want validation error", c.id)
		}
	}
}

// TestErrNotFound — the typed sentinel callers branch on. We don't
// construct one manually (the sentinel is the public surface); just
// confirm it matches the package doc.
func TestErrNotFound(t *testing.T) {
	if !strings.Contains(ErrNotFound.Error(), "not found") {
		t.Errorf("ErrNotFound message = %q, want 'not found' substring", ErrNotFound.Error())
	}
	// errors.Is must work for the typed sentinel — IsNotFound is the
	// public predicate.
	if !IsNotFound(ErrNotFound) {
		t.Errorf("IsNotFound(ErrNotFound) = false; want true")
	}
	// A wrapped not-found also matches.
	wrapped := wrap(ErrNotFound, "decode row 0")
	if !IsNotFound(wrapped) {
		t.Errorf("IsNotFound(wrapped) = false; want true")
	}
	// A different error does not match.
	if IsNotFound(errors.New("other")) {
		t.Errorf("IsNotFound(other) = true; want false")
	}
}

// wrap is the small helper to confirm errors.Is walks the chain.
func wrap(inner error, _ string) error {
	return &wrappedErr{inner: inner}
}

type wrappedErr struct {
	inner error
}

func (w *wrappedErr) Error() string { return "wrapped: " + w.inner.Error() }
func (w *wrappedErr) Unwrap() error { return w.inner }
