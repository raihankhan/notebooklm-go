// Tests for the sources row decoder.
//
// Coverage target: 100% on rows/sources.go. Every positional slot
// in the contract table is exercised by at least one test below.
package rows

import (
	"errors"
	"strings"
	"testing"

	"github.com/raihankhan/notebooklm-go/internal/web/wire"
)

// makeSourceRow assembles a minimal Source entry for tests. The
// default values produce the "empty source" envelope; callers
// override the slots they want to exercise. Mirrors the
// `makeRow` helper in `notebooks_test.go`.
func makeSourceRow(opts ...func(*[]any)) []any {
	row := []any{
		[]any{"src-1"}, // 0: id envelope (typical)
		"Source Title", // 1: title
		[]any{nil, nil, nil, nil, 1, nil, nil, nil}, // 2: metadata (type=1 at slot 4)
		[]any{nil, 2}, // 3: settings (status=2 READY at slot 1)
	}
	for _, opt := range opts {
		opt(&row)
	}
	return row
}

// withSourceStatus swaps the row's settings block (slot 3) for the
// supplied value. Most status reads live inside the settings block.
func withSourceStatus(status int) func(*[]any) {
	return func(row *[]any) {
		(*row)[3] = []any{nil, status}
	}
}

// withSourceType swaps the metadata type-code (metadata slot 4)
// for the supplied value. Source kind derives from this slot.
func withSourceType(kind int) func(*[]any) {
	return func(row *[]any) {
		meta := (*row)[2].([]any)
		// Grow meta if too short.
		for len(meta) <= 4 {
			meta = append(meta, nil)
		}
		meta[4] = kind
		(*row)[2] = meta
	}
}

// withSourceIDEnvelope swaps the row's id envelope (slot 0) for
// the supplied value. Exercises the three id-envelope layouts.
func withSourceIDEnvelope(env any) func(*[]any) {
	return func(row *[]any) {
		(*row)[0] = env
	}
}

// TestDecodeSource_RequiredFields — the load-bearing id and title
// slots. A row that omits either still decodes (the short-row
// tolerance), but the typed field stays empty.
func TestDecodeSource_RequiredFields(t *testing.T) {
	row := makeSourceRow()
	s, err := DecodeSource(row)
	if err != nil {
		t.Fatalf("DecodeSource: %v", err)
	}
	if s.ID != "src-1" {
		t.Errorf("ID = %q, want src-1", s.ID)
	}
	if s.Title != "Source Title" {
		t.Errorf("Title = %q, want Source Title", s.Title)
	}
}

// TestDecodeSource_Nil — nil payload decodes to a zero Source.
func TestDecodeSource_Nil(t *testing.T) {
	s, err := DecodeSource(nil)
	if err != nil {
		t.Fatalf("DecodeSource nil: %v", err)
	}
	if s.ID != "" || s.Title != "" || s.Kind != "" || s.StatusLabel != "" {
		t.Errorf("nil payload decoded to %+v, want zero", s)
	}
}

// TestDecodeSource_ShortRow — a row missing all but the id/title
// slots degrades to empty Kind / StatusLabel rather than raising.
// The short-row tolerance is the documented "absent slot" contract.
func TestDecodeSource_ShortRow(t *testing.T) {
	row := []any{[]any{"src-1"}, "Title"}
	s, _ := DecodeSource(row)
	if s.ID != "src-1" || s.Title != "Title" {
		t.Errorf("Short row decoded to %+v, want id+title only", s)
	}
	if s.Kind != "" || s.StatusLabel != "" {
		t.Errorf("Short row decoded to %+v, want empty kind/status", s)
	}
}

// TestDecodeSource_IDEnvelopeTypical — the typical
// `["id"]` envelope at slot 0.
func TestDecodeSource_IDEnvelopeTypical(t *testing.T) {
	row := makeSourceRow(withSourceIDEnvelope([]any{"src-abc"}))
	s, _ := DecodeSource(row)
	if s.ID != "src-abc" {
		t.Errorf("ID = %q, want src-abc", s.ID)
	}
}

// TestDecodeSource_IDEnvelopeBare — the flat-shape bare-string
// envelope at slot 0.
func TestDecodeSource_IDEnvelopeBare(t *testing.T) {
	row := makeSourceRow(withSourceIDEnvelope("src-bare"))
	s, _ := DecodeSource(row)
	if s.ID != "src-bare" {
		t.Errorf("ID = %q, want src-bare", s.ID)
	}
}

// TestDecodeSource_IDEnvelopeDrive — the drive-backed
// `[None, True, ["id"]]` envelope at slot 0.
func TestDecodeSource_IDEnvelopeDrive(t *testing.T) {
	row := makeSourceRow(withSourceIDEnvelope([]any{nil, true, []any{"src-drive"}}))
	s, _ := DecodeSource(row)
	if s.ID != "src-drive" {
		t.Errorf("ID = %q, want src-drive", s.ID)
	}
}

// TestDecodeSource_IDEnvelopeMalformed — a present-but-wrong-typed
// id envelope degrades to empty ID rather than drift. Mirrors the
// Python original's tolerance.
func TestDecodeSource_IDEnvelopeMalformed(t *testing.T) {
	// Number instead of string at slot 0.
	row := makeSourceRow(withSourceIDEnvelope(42))
	s, _ := DecodeSource(row)
	if s.ID != "" {
		t.Errorf("ID = %q, want empty for malformed envelope", s.ID)
	}
}

// TestDecodeSource_StatusProcessing — status code 1 → PROCESSING.
func TestDecodeSource_StatusProcessing(t *testing.T) {
	row := makeSourceRow(withSourceStatus(1))
	s, _ := DecodeSource(row)
	if s.StatusLabel != "PROCESSING" {
		t.Errorf("StatusLabel = %q, want PROCESSING", s.StatusLabel)
	}
}

// TestDecodeSource_StatusReady — status code 2 → READY.
func TestDecodeSource_StatusReady(t *testing.T) {
	row := makeSourceRow(withSourceStatus(2))
	s, _ := DecodeSource(row)
	if s.StatusLabel != "READY" {
		t.Errorf("StatusLabel = %q, want READY", s.StatusLabel)
	}
}

// TestDecodeSource_StatusError — status code 3 → ERROR.
func TestDecodeSource_StatusError(t *testing.T) {
	row := makeSourceRow(withSourceStatus(3))
	s, _ := DecodeSource(row)
	if s.StatusLabel != "ERROR" {
		t.Errorf("StatusLabel = %q, want ERROR", s.StatusLabel)
	}
}

// TestDecodeSource_StatusAbsent — absent settings block degrades to
// empty StatusLabel (the "newly added, not yet polled" case).
func TestDecodeSource_StatusAbsent(t *testing.T) {
	row := makeSourceRow()
	row[3] = nil
	s, _ := DecodeSource(row)
	if s.StatusLabel != "" {
		t.Errorf("StatusLabel = %q, want empty for absent settings", s.StatusLabel)
	}
}

// TestDecodeSource_StatusUnknown — unknown status code (e.g.
// UNSPECIFIED=0, or a future backend enum value) degrades to empty
// StatusLabel rather than drift. Mirrors the Python original's
// warn-once-and-degrade pattern.
func TestDecodeSource_StatusUnknown(t *testing.T) {
	for _, code := range []int{0, 4, 99} {
		row := makeSourceRow(withSourceStatus(code))
		s, _ := DecodeSource(row)
		if s.StatusLabel != "" {
			t.Errorf("StatusLabel for code %d = %q, want empty", code, s.StatusLabel)
		}
	}
}

// TestDecodeSource_KindURL — type code 1 → "url".
func TestDecodeSource_KindURL(t *testing.T) {
	row := makeSourceRow(withSourceType(1))
	s, _ := DecodeSource(row)
	if s.Kind != "url" {
		t.Errorf("Kind = %q, want url", s.Kind)
	}
}

// TestDecodeSource_KindYouTube — type code 2 → "youtube".
func TestDecodeSource_KindYouTube(t *testing.T) {
	row := makeSourceRow(withSourceType(2))
	s, _ := DecodeSource(row)
	if s.Kind != "youtube" {
		t.Errorf("Kind = %q, want youtube", s.Kind)
	}
}

// TestDecodeSource_KindDrive — type code 3 → "drive".
func TestDecodeSource_KindDrive(t *testing.T) {
	row := makeSourceRow(withSourceType(3))
	s, _ := DecodeSource(row)
	if s.Kind != "drive" {
		t.Errorf("Kind = %q, want drive", s.Kind)
	}
}

// TestDecodeSource_KindText — type code 4 → "text".
func TestDecodeSource_KindText(t *testing.T) {
	row := makeSourceRow(withSourceType(4))
	s, _ := DecodeSource(row)
	if s.Kind != "text" {
		t.Errorf("Kind = %q, want text", s.Kind)
	}
}

// TestDecodeSource_KindFile — type code 5 → "file".
func TestDecodeSource_KindFile(t *testing.T) {
	row := makeSourceRow(withSourceType(5))
	s, _ := DecodeSource(row)
	if s.Kind != "file" {
		t.Errorf("Kind = %q, want file", s.Kind)
	}
}

// TestDecodeSource_KindAbsent — absent metadata block degrades to
// empty Kind.
func TestDecodeSource_KindAbsent(t *testing.T) {
	row := makeSourceRow()
	row[2] = nil
	s, _ := DecodeSource(row)
	if s.Kind != "" {
		t.Errorf("Kind = %q, want empty for absent metadata", s.Kind)
	}
}

// TestDecodeSource_KindUnknown — unknown type code (e.g. UNSPECIFIED=0,
// or a future backend enum value) degrades to empty Kind rather than
// drift. Mirrors the Python original's `_safe_source_type → UNKNOWN`
// fallback.
func TestDecodeSource_KindUnknown(t *testing.T) {
	for _, code := range []int{0, 6, 99} {
		row := makeSourceRow(withSourceType(code))
		s, _ := DecodeSource(row)
		if s.Kind != "" {
			t.Errorf("Kind for code %d = %q, want empty", code, s.Kind)
		}
	}
}

// TestDecodeSource_FullRow — the four-field happy-path. A row
// carrying all the typed fields decodes to all four populated.
func TestDecodeSource_FullRow(t *testing.T) {
	row := []any{
		[]any{"src-full"},
		"Full Row",
		[]any{nil, nil, nil, nil, 2, nil, nil, nil}, // kind=youtube (type=2)
		[]any{nil, 2}, // status=READY
	}
	s, _ := DecodeSource(row)
	if s.ID != "src-full" || s.Title != "Full Row" || s.Kind != "youtube" || s.StatusLabel != "READY" {
		t.Errorf("Full row decoded to %+v, want all four fields", s)
	}
}

// TestDecodeSource_FixtureFiveRows — the byte-exact 5-row
// fixture test the ticket body names. Build a small GET_NOTEBOOK
// source-list envelope and decode each entry; verify the four
// typed fields line up across all rows.
func TestDecodeSource_FixtureFiveRows(t *testing.T) {
	envelope := []any{
		// outer[0] is the source list (sourced from notebook[0][1]).
		[]any{
			// row 0: URL source, READY.
			[]any{
				[]any{"src-aaaa"},
				"Source A",
				[]any{nil, nil, nil, nil, 1, nil, nil, nil},
				[]any{nil, 2},
			},
			// row 1: YouTube source, PROCESSING.
			[]any{
				[]any{"src-bbbb"},
				"Source B",
				[]any{nil, nil, nil, nil, 2, nil, nil, nil},
				[]any{nil, 1},
			},
			// row 2: Drive source, READY.
			[]any{
				[]any{nil, true, []any{"src-cccc"}},
				"Source C",
				[]any{nil, nil, nil, nil, 3, nil, nil, nil},
				[]any{nil, 2},
			},
			// row 3: Text source, ERROR.
			[]any{
				[]any{"src-dddd"},
				"Source D",
				[]any{nil, nil, nil, nil, 4, nil, nil, nil},
				[]any{nil, 3},
			},
			// row 4: File source, READY.
			[]any{
				[]any{"src-eeee"},
				"Source E",
				[]any{nil, nil, nil, nil, 5, nil, nil, nil},
				[]any{nil, 2},
			},
		},
	}
	list, ok := envelope[0].([]any)
	if !ok || len(list) != 5 {
		t.Fatalf("envelope[0] = %v, want 5-row source list", envelope[0])
	}
	for i, raw := range list {
		s, err := DecodeSource(raw)
		if err != nil {
			t.Fatalf("DecodeSource[%d]: %v", i, err)
		}
		if s.ID == "" {
			t.Errorf("row %d: ID empty", i)
		}
		if s.Title == "" {
			t.Errorf("row %d: Title empty", i)
		}
		if s.Kind == "" {
			t.Errorf("row %d: Kind empty", i)
		}
		if s.StatusLabel == "" {
			t.Errorf("row %d: StatusLabel empty", i)
		}
	}
	// Spot-check one entry end-to-end.
	s, _ := DecodeSource(list[2])
	if s.ID != "src-cccc" || s.Title != "Source C" || s.Kind != "drive" || s.StatusLabel != "READY" {
		t.Errorf("row 2 = %+v, want src-cccc/Source C/drive/READY", s)
	}
}

// TestDecodeSource_5Rows_ByteExact — the byte-exact fixture
// test pinned against the wire.JSON-decoded form. Uses
// wire.Unmarshal so the production-path json.Number carriers are
// exercised end-to-end.
func TestDecodeSource_5Rows_ByteExact(t *testing.T) {
	const rawJSON = `[[
		[["src-a"], "Title A", [null, null, null, null, 1, null, null, null], [null, 2]],
		[["src-b"], "Title B", [null, null, null, null, 2, null, null, null], [null, 1]],
		[["src-c"], "Title C", [null, null, null, null, 3, null, null, null], [null, 2]],
		[["src-d"], "Title D", [null, null, null, null, 4, null, null, null], [null, 3]],
		[["src-e"], "Title E", [null, null, null, null, 5, null, null, null], [null, 2]]
	]]`
	var payload any
	if err := wire.Unmarshal([]byte(rawJSON), &payload); err != nil {
		t.Fatalf("wire.Unmarshal: %v", err)
	}
	outer, ok := payload.([]any)
	if !ok || len(outer) != 1 {
		t.Fatalf("outer = %T, want [[...]]", payload)
	}
	list, ok := outer[0].([]any)
	if !ok || len(list) != 5 {
		t.Fatalf("outer[0] len = %d, want 5", len(list))
	}
	for i, raw := range list {
		s, err := DecodeSource(raw)
		if err != nil {
			t.Fatalf("DecodeSource[%d]: %v", i, err)
		}
		if s.ID == "" || s.Title == "" || s.Kind == "" || s.StatusLabel == "" {
			t.Errorf("row %d incomplete: %+v", i, s)
		}
	}
	// Spot-check the production-path json.Number carriers decode
	// correctly — the kind/status enums derive from metadata[4] /
	// settings[1], both of which arrive as json.Number from
	// wire.Unmarshal.
	s, _ := DecodeSource(list[0])
	if s.Kind != "url" || s.StatusLabel != "READY" {
		t.Errorf("row 0 (json.Number path) = %+v, want url/READY", s)
	}
	s, _ = DecodeSource(list[1])
	if s.Kind != "youtube" || s.StatusLabel != "PROCESSING" {
		t.Errorf("row 1 (json.Number path) = %+v, want youtube/PROCESSING", s)
	}
}

// TestErrSourceNotFound — the typed sentinel callers branch on.
// We don't construct one manually (the sentinel is the public
// surface); just confirm it matches the package doc.
func TestErrSourceNotFound(t *testing.T) {
	if !strings.Contains(ErrSourceNotFound.Error(), "source not found") {
		t.Errorf("ErrSourceNotFound message = %q, want 'source not found' substring", ErrSourceNotFound.Error())
	}
	// IsSourceNotFound must work for the typed sentinel.
	if !IsSourceNotFound(ErrSourceNotFound) {
		t.Errorf("IsSourceNotFound(ErrSourceNotFound) = false; want true")
	}
	// A wrapped not-found also matches.
	wrapped := wrapSourceErr(ErrSourceNotFound, "decode row 0")
	if !IsSourceNotFound(wrapped) {
		t.Errorf("IsSourceNotFound(wrapped) = false; want true")
	}
	// A different error does not match.
	if IsSourceNotFound(errors.New("other")) {
		t.Errorf("IsSourceNotFound(other) = true; want false")
	}
}

// wrapSourceErr is the small helper to confirm errors.Is walks the
// chain. Mirrors `wrap` in `notebooks_test.go`.
func wrapSourceErr(inner error, _ string) error {
	return &wrappedSourceErr{inner: inner}
}

type wrappedSourceErr struct {
	inner error
}

func (w *wrappedSourceErr) Error() string { return "wrapped: " + w.inner.Error() }
func (w *wrappedSourceErr) Unwrap() error { return w.inner }
