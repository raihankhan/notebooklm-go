// Tests for the chat history row decoder.
//
// Coverage targets:
//   - history round-trip: encode a Conversation, decode it, assert equality
//   - per-turn UTF-16 offset table (emoji + surrogate pairs)
//   - multiple conversations (each with multiple turns)
//   - empty history (no turns)
//   - conversation-id recovery on the alt-wrap payload shape
//   - malformed-payload tolerance (missing slot, wrong type, etc.)
//   - ExtractConversationID public companion
package rows

import (
	"strings"
	"testing"

	"github.com/raihankhan/notebooklm-go/internal/web/wire"
)

// wireMarshal is a thin alias to wire.MarshalSorted (the canonical
// wire encoder; see internal/web/wire/json.go).
func wireMarshal(v any) ([]byte, error) {
	return wire.Marshal(v)
}

// wrapEnvelope produces a wrb.fr-style payload the decoder recognizes.
// The shape is the typical Shape A:
//
//	["wrb.fr", "khqZz", null, [conv_id, [turn1, turn2, ...]]]
//
// `convID` may be empty (the decoder still parses the turns and reports
// an empty id, matching the Python original's tolerance).
// `turns` is the inner list-of-turns slot.
func wrapEnvelope(convID string, turns []any) []byte {
	return encodeJSON([]any{
		"wrb.fr",
		"khqZz",
		nil,
		[]any{convID, turns},
	})
}

// wrapEnvelopeSlotB produces the alt-wrap shape:
//
//	["wrb.fr", "khqZz", null, conv_id, [conv_id, [turn1, ...]]]
//
// `slot3` is the bare-id slot at index 3 (so the decoder must fall
// through to slot 4 for the history envelope).
func wrapEnvelopeSlotB(slot3ID, envID string, turns []any) []byte {
	return encodeJSON([]any{
		"wrb.fr",
		"khqZz",
		nil,
		slot3ID,
		[]any{envID, turns},
	})
}

// makeTurn assembles one turn in the wire shape:
//
//	[author, [text, [ref1, ref2, ...]], created_at]
//
// where each ref is [kind, source_id, [start_utf16, end_utf16]].
//
// All slots beyond the first three are optional; pass nil for refs or
// created-at to omit.
func makeTurn(author, text string, refs []any, createdAt any) []any {
	turn := []any{
		author,
		[]any{text, refs},
	}
	if createdAt != nil {
		turn = append(turn, createdAt)
	}
	return turn
}

// makeRef assembles one citation reference:
//
//	[kind, source_id, [start_utf16, end_utf16]]
func makeRef(kind, sourceID string, start, end int) []any {
	return []any{kind, sourceID, []any{start, end}}
}

// encodeJSON serializes v via the project wire encoder. The decoder
// accepts anything wire.Unmarshal can parse; round-trip tests want the
// canonical encoding (no HTML escapes, no trailing newline) so the
// decoder receives the same bytes the production path produces.
func encodeJSON(v any) []byte {
	out, err := wireMarshal(v)
	if err != nil {
		panic(err)
	}
	return out
}

// TestDecodeChatHistory_Typical — two turns (user + model), two
// citations on the model turn, conversation id echoed.
func TestDecodeChatHistory_Typical(t *testing.T) {
	turns := []any{
		makeTurn("user", "what is X?", nil, 1700000000.0),
		makeTurn("model", "X is a foo.",
			[]any{makeRef("chunk", "src-1", 0, 4), makeRef("chunk", "src-2", 5, 12)},
			1700000001.0,
		),
	}
	body := wrapEnvelope("conv-abc", turns)

	conv, err := DecodeChatHistory(body)
	if err != nil {
		t.Fatalf("DecodeChatHistory: %v", err)
	}
	if conv.ID != "conv-abc" {
		t.Errorf("ID = %q, want conv-abc", conv.ID)
	}
	if got := len(conv.Turns); got != 2 {
		t.Fatalf("len(Turns) = %d, want 2", got)
	}
	if conv.Turns[0].Author != "user" || conv.Turns[0].Text != "what is X?" {
		t.Errorf("turn[0] = %+v", conv.Turns[0])
	}
	if conv.Turns[1].Author != "model" || conv.Turns[1].Text != "X is a foo." {
		t.Errorf("turn[1] = %+v", conv.Turns[1])
	}
	if got := conv.Turns[1].Citations; len(got) != 2 || got[0] != "src-1" || got[1] != "src-2" {
		t.Errorf("citations = %v, want [src-1 src-2]", got)
	}
	if conv.Turns[1].RawCitations[0].StartUTF16 != 0 || conv.Turns[1].RawCitations[0].EndUTF16 != 4 {
		t.Errorf("raw citation offsets: %+v", conv.Turns[1].RawCitations[0])
	}
}

// TestDecodeChatHistory_Empty — a conversation with zero turns
// decodes to a non-nil empty slice. The id is still recovered.
func TestDecodeChatHistory_Empty(t *testing.T) {
	body := wrapEnvelope("conv-empty", []any{})

	conv, err := DecodeChatHistory(body)
	if err != nil {
		t.Fatalf("DecodeChatHistory: %v", err)
	}
	if conv.ID != "conv-empty" {
		t.Errorf("ID = %q, want conv-empty", conv.ID)
	}
	if conv.Turns == nil {
		t.Errorf("Turns is nil; want non-nil empty slice")
	}
	if len(conv.Turns) != 0 {
		t.Errorf("len(Turns) = %d, want 0", len(conv.Turns))
	}
}

// TestDecodeChatHistory_MultipleConversations — decoding two
// independent payloads produces two independent Conversations; the
// decoder does not retain cross-call state.
func TestDecodeChatHistory_MultipleConversations(t *testing.T) {
	mkBody := func(id string, prompts []string) []byte {
		turns := make([]any, 0, len(prompts)*2)
		for i, p := range prompts {
			turns = append(turns,
				makeTurn("user", p, nil, float64(1700000000+i*10)),
				makeTurn("model", "answer to "+p, nil, float64(1700000000+i*10+1)),
			)
		}
		return wrapEnvelope(id, turns)
	}

	body1 := mkBody("conv-1", []string{"hi", "how are you?", "goodbye"})
	body2 := mkBody("conv-2", []string{"alpha", "beta"})

	c1, err := DecodeChatHistory(body1)
	if err != nil {
		t.Fatalf("DecodeChatHistory body1: %v", err)
	}
	c2, err := DecodeChatHistory(body2)
	if err != nil {
		t.Fatalf("DecodeChatHistory body2: %v", err)
	}

	if c1.ID != "conv-1" || len(c1.Turns) != 6 {
		t.Errorf("c1 = %q/%d turns", c1.ID, len(c1.Turns))
	}
	if c2.ID != "conv-2" || len(c2.Turns) != 4 {
		t.Errorf("c2 = %q/%d turns", c2.ID, len(c2.Turns))
	}
	// Cross-check: re-decoding body1 again still yields c1.
	c1Again, err := DecodeChatHistory(body1)
	if err != nil {
		t.Fatalf("DecodeChatHistory body1 (re-decode): %v", err)
	}
	if c1Again.ID != c1.ID || len(c1Again.Turns) != len(c1.Turns) {
		t.Errorf("re-decode drift: c1 = %+v, c1Again = %+v", c1, c1Again)
	}
}

// TestDecodeChatHistory_ConversationIDRecovery — the alt-wrap shape
// (slot 3 is a bare id) yields the conversation id from slot 3.
// When both slot 3 (bare id) and slot 4 (envelope) are present the
// decoder prefers slot 3 (the more direct signal).
func TestDecodeChatHistory_ConversationIDRecovery(t *testing.T) {
	turns := []any{
		makeTurn("user", "hi", nil, 1700000000.0),
		makeTurn("model", "hello", nil, 1700000001.0),
	}
	body := wrapEnvelopeSlotB("conv-recovered", "unused", turns)

	conv, err := DecodeChatHistory(body)
	if err != nil {
		t.Fatalf("DecodeChatHistory: %v", err)
	}
	if conv.ID != "conv-recovered" {
		t.Errorf("ID = %q, want conv-recovered", conv.ID)
	}
	if len(conv.Turns) != 2 {
		t.Errorf("len(Turns) = %d, want 2", len(conv.Turns))
	}
}

// TestExtractConversationID_Typical — the public-side companion
// recovers the id without needing the full turn list.
func TestExtractConversationID_Typical(t *testing.T) {
	body := wrapEnvelope("conv-extracted", []any{
		makeTurn("user", "hi", nil, nil),
	})
	id, err := ExtractConversationID(body)
	if err != nil {
		t.Fatalf("ExtractConversationID: %v", err)
	}
	if id != "conv-extracted" {
		t.Errorf("id = %q, want conv-extracted", id)
	}
}

// TestExtractConversationID_SlotB — the alt-wrap shape.
func TestExtractConversationID_SlotB(t *testing.T) {
	body := wrapEnvelopeSlotB("conv-from-slot-b", "unused", []any{})
	id, err := ExtractConversationID(body)
	if err != nil {
		t.Fatalf("ExtractConversationID: %v", err)
	}
	if id != "conv-from-slot-b" {
		t.Errorf("id = %q, want conv-from-slot-b", id)
	}
}

// TestExtractConversationID_Missing — a payload with no id anywhere
// returns an error rather than silently returning "".
func TestExtractConversationID_Missing(t *testing.T) {
	body := encodeJSON([]any{"wrb.fr", "khqZz", nil, nil})
	_, err := ExtractConversationID(body)
	if err == nil {
		t.Errorf("ExtractConversationID: expected error on missing id")
	}
}

// TestDecodeChatHistory_UTF16OffsetTable — the per-turn citation
// offsets are in UTF-16 code units, not bytes. Slice the model text
// using SliceAnswerByCitation and verify the slice covers the
// expected rune span for an emoji-containing answer.
//
// Model text (14 UTF-16 code units):
//
//	"X is 🎉 a foo."
//	  X       = 1 unit
//	  ' '     = 1 unit
//	  i       = 1 unit
//	  s       = 1 unit
//	  ' '     = 1 unit
//	  🎉     = 2 units  ← surrogate pair
//	  ' '     = 1 unit
//	  a       = 1 unit
//	  ' '     = 1 unit
//	  f       = 1 unit
//	  o       = 1 unit
//	  o       = 1 unit
//	  .       = 1 unit
//	  total   = 14 units
//
// We cite "🎉 a foo." (UTF-16 [5, 14)). The slice must contain the
// emoji and the trailing text but NOT the "X is " prefix.
func TestDecodeChatHistory_UTF16OffsetTable(t *testing.T) {
	modelText := "X is 🎉 a foo."
	refs := []any{makeRef("chunk", "src-emoji", 5, 14)}

	turns := []any{
		makeTurn("user", "what is X?", nil, nil),
		makeTurn("model", modelText, refs, nil),
	}
	body := wrapEnvelope("conv-utf16", turns)

	conv, err := DecodeChatHistory(body)
	if err != nil {
		t.Fatalf("DecodeChatHistory: %v", err)
	}
	if len(conv.Turns) != 2 {
		t.Fatalf("len(Turns) = %d, want 2", len(conv.Turns))
	}
	cit := conv.Turns[1].RawCitations[0]
	if cit.StartUTF16 != 5 || cit.EndUTF16 != 14 {
		t.Errorf("offsets = (%d, %d), want (5, 14)", cit.StartUTF16, cit.EndUTF16)
	}

	// Slice using the UTF-16 primitive; this is the round-trip the
	// SDK layer relies on.
	got := SliceAnswerByCitation(conv.Turns[1].Text, cit)
	want := "🎉 a foo."
	if got != want {
		t.Errorf("SliceAnswerByCitation = %q, want %q", got, want)
	}

	// Sanity: UTF16Len agrees on the slice lengths.
	if got := UTF16Len(modelText); got != 14 {
		t.Errorf("UTF16Len(%q) = %d, want 14", modelText, got)
	}
	if got := UTF16Len(want); got != 9 {
		t.Errorf("UTF16Len(%q) = %d, want 9", want, got)
	}
}

// TestDecodeChatHistory_SurrogatePairStartEnd — the UTF-16 slice
// primitive handles half-cuts on a surrogate pair. We cite only the
// low surrogate of the first emoji (the [1, 2) cut of "🎉🎉");
// the slice must contain the canonical 3-byte UTF-8 encoding for
// U+DF89 (the low surrogate of the 🎉 pair).
func TestDecodeChatHistory_SurrogatePairStartEnd(t *testing.T) {
	modelText := "🎉🎉" // two emojis = 4 UTF-16 units
	refs := []any{
		makeRef("chunk", "src-sur", 1, 2), // low surrogate of pair #1
	}
	turns := []any{
		makeTurn("user", "?", nil, nil),
		makeTurn("model", modelText, refs, nil),
	}
	body := wrapEnvelope("conv-sur", turns)
	conv, err := DecodeChatHistory(body)
	if err != nil {
		t.Fatalf("DecodeChatHistory: %v", err)
	}
	got := SliceAnswerByCitation(conv.Turns[1].Text, conv.Turns[1].RawCitations[0])
	// U+DF89 = low surrogate of U+1F389 (🎉) → UTF-8 0xED 0xBE 0x89.
	wantBytes := []byte{0xED, 0xBE, 0x89}
	if got != string(wantBytes) {
		t.Errorf("SliceAnswerByCitation = % x, want % x", got, wantBytes)
	}
}

// TestDecodeChatHistory_MalformedJSON — invalid JSON returns an error
// from the wire layer rather than panicking.
func TestDecodeChatHistory_MalformedJSON(t *testing.T) {
	body := []byte("{not json at all")
	_, err := DecodeChatHistory(body)
	if err == nil {
		t.Errorf("expected error on malformed JSON")
	}
}

// TestDecodeChatHistory_EmptyBody — a zero-length payload returns
// an error.
func TestDecodeChatHistory_EmptyBody(t *testing.T) {
	_, err := DecodeChatHistory(nil)
	if err == nil {
		t.Errorf("expected error on empty body")
	}
	_, err = DecodeChatHistory([]byte{})
	if err == nil {
		t.Errorf("expected error on empty body ([]byte{})")
	}
}

// TestDecodeChatHistory_UnknownEnvelope — a wrb.fr-shaped payload
// whose slot 3 is neither a history envelope nor a string id returns
// an error rather than silently returning an empty Conversation.
func TestDecodeChatHistory_UnknownEnvelope(t *testing.T) {
	body := encodeJSON([]any{"wrb.fr", "khqZz", nil, 42})
	_, err := DecodeChatHistory(body)
	if err == nil {
		t.Errorf("expected error on unknown envelope shape")
	}
}

// TestDecodeChatHistory_MalformedTurn — a turn missing the author
// slot is silently skipped (the Python original's tolerance); the
// surrounding turns still decode.
func TestDecodeChatHistory_MalformedTurn(t *testing.T) {
	turns := []any{
		makeTurn("user", "ok", nil, nil),
		// Malformed: not a list.
		"not-a-turn",
		makeTurn("model", "yes", nil, nil),
	}
	body := wrapEnvelope("conv-malformed", turns)
	conv, err := DecodeChatHistory(body)
	if err != nil {
		t.Fatalf("DecodeChatHistory: %v", err)
	}
	if len(conv.Turns) != 2 {
		t.Errorf("len(Turns) = %d, want 2 (malformed turn skipped)", len(conv.Turns))
	}
	if conv.Turns[0].Author != "user" || conv.Turns[1].Author != "model" {
		t.Errorf("turns drifted: %+v", conv.Turns)
	}
}

// TestDecodeChatHistory_TurnMissingText — a turn whose content slot
// is missing decodes to an empty Text field (not an error).
func TestDecodeChatHistory_TurnMissingText(t *testing.T) {
	turns := []any{
		// Author only — no content slot.
		[]any{"user"},
	}
	body := wrapEnvelope("conv-notext", turns)
	conv, err := DecodeChatHistory(body)
	if err != nil {
		t.Fatalf("DecodeChatHistory: %v", err)
	}
	if len(conv.Turns) != 1 || conv.Turns[0].Text != "" {
		t.Errorf("turn[0] = %+v, want empty Text", conv.Turns[0])
	}
}

// TestDecodeChatHistory_RoundTrip — build a Conversation, encode it
// in the canonical wire shape, decode it back, and assert equality on
// every scalar field. The encoder is hand-rolled (see wireMarshal)
// to mimic the canonical wire format the server emits.
func TestDecodeChatHistory_RoundTrip(t *testing.T) {
	original := &Conversation{
		ID: "conv-roundtrip",
		Turns: []Turn{
			{
				Author:    "user",
				Text:      "hello world",
				Citations: []string{},
			},
			{
				Author:    "model",
				Text:      "hi there 👋",
				Citations: []string{"src-1", "src-2"},
				RawCitations: []Citation{
					{SourceID: "src-1", StartUTF16: 0, EndUTF16: 2},
					{SourceID: "src-2", StartUTF16: 3, EndUTF16: 9},
				},
			},
		},
	}
	body := encodeConversation(original)
	decoded, err := DecodeChatHistory(body)
	if err != nil {
		t.Fatalf("DecodeChatHistory: %v", err)
	}
	if decoded.ID != original.ID {
		t.Errorf("ID = %q, want %q", decoded.ID, original.ID)
	}
	if len(decoded.Turns) != len(original.Turns) {
		t.Fatalf("len(Turns) = %d, want %d", len(decoded.Turns), len(original.Turns))
	}
	for i := range original.Turns {
		if decoded.Turns[i].Author != original.Turns[i].Author {
			t.Errorf("turn[%d].Author = %q, want %q", i, decoded.Turns[i].Author, original.Turns[i].Author)
		}
		if decoded.Turns[i].Text != original.Turns[i].Text {
			t.Errorf("turn[%d].Text = %q, want %q", i, decoded.Turns[i].Text, original.Turns[i].Text)
		}
		if !equalStrings(decoded.Turns[i].Citations, original.Turns[i].Citations) {
			t.Errorf("turn[%d].Citations = %v, want %v", i, decoded.Turns[i].Citations, original.Turns[i].Citations)
		}
	}
}

// TestDecodeChatHistory_RejectsOffSchemaJSON — when the JSON parses
// but is not an array at root, the decoder fails (the wire format
// requires a top-level list).
func TestDecodeChatHistory_RejectsOffSchemaJSON(t *testing.T) {
	body := encodeJSON(map[string]any{"not": "an array"})
	_, err := DecodeChatHistory(body)
	if err == nil {
		t.Errorf("expected error on off-schema (non-array) payload")
	}
}

// TestSliceAnswerByCitation_OutOfRange — out-of-range offsets clamp
// rather than panicking; an empty source id returns "" rather than
// slicing arbitrary text.
func TestSliceAnswerByCitation_OutOfRange(t *testing.T) {
	got := SliceAnswerByCitation("hello", Citation{SourceID: "x", StartUTF16: -5, EndUTF16: 100})
	if got != "hello" {
		t.Errorf("clamped slice = %q, want %q", got, "hello")
	}

	got = SliceAnswerByCitation("hello", Citation{SourceID: "", StartUTF16: 0, EndUTF16: 5})
	if got != "" {
		t.Errorf("empty source id should return empty slice, got %q", got)
	}

	got = SliceAnswerByCitation("", Citation{SourceID: "x", StartUTF16: 0, EndUTF16: 5})
	if got != "" {
		t.Errorf("empty text should return empty slice, got %q", got)
	}
}

// TestDecodeChatHistory_ErrOnNonWrb — when the payload does not start
// with the wrb.fr tag the decoder does NOT match (the bare-envelope
// fallback would reject it). This is documented behavior: the
// decoder expects the framing layer to have stripped anti-XSSI and
// parsed the per-chunk RPC envelope before the payload arrives.
func TestDecodeChatHistory_ErrOnNonWrb(t *testing.T) {
	// A non-wrb.fr-tagged top-level array whose shape happens to be
	// id-list... no, wait: the fallback DOES accept that. So we test
	// the truly unknown case (an integer at slot 3).
	body := encodeJSON([]any{"er", "khqZz", nil, 7})
	_, err := DecodeChatHistory(body)
	if err == nil {
		t.Errorf("expected error on integer slot-3 envelope")
	}
}

// TestDecodeChatHistory_FlatEnvelopeFallback — a payload whose root is
// itself the history envelope (no wrb.fr wrapper) is still decoded.
// This shape appears when the framing layer already unwrapped.
func TestDecodeChatHistory_FlatEnvelopeFallback(t *testing.T) {
	turns := []any{makeTurn("user", "hi", nil, nil)}
	body := encodeJSON([]any{"conv-flat", turns})
	conv, err := DecodeChatHistory(body)
	if err != nil {
		t.Fatalf("DecodeChatHistory: %v", err)
	}
	if conv.ID != "conv-flat" {
		t.Errorf("ID = %q, want conv-flat", conv.ID)
	}
	if len(conv.Turns) != 1 {
		t.Errorf("len(Turns) = %d, want 1", len(conv.Turns))
	}
}

// TestDecodeChatHistory_EscapedQuotes — JSON-escape round-trip
// preserves backslashes and quotes inside user/model text. The wire
// transport is responsible for encoding; the decoder must surface the
// unescaped form.
func TestDecodeChatHistory_EscapedQuotes(t *testing.T) {
	turns := []any{
		makeTurn("user", `she said "hi" and walked away`, nil, nil),
		makeTurn("model", `path "C:\\Users\\me" works`, nil, nil),
	}
	body := wrapEnvelope("conv-escapes", turns)
	conv, err := DecodeChatHistory(body)
	if err != nil {
		t.Fatalf("DecodeChatHistory: %v", err)
	}
	if conv.Turns[0].Text != `she said "hi" and walked away` {
		t.Errorf("user text = %q", conv.Turns[0].Text)
	}
	if conv.Turns[1].Text != `path "C:\\Users\\me" works` {
		t.Errorf("model text = %q", conv.Turns[1].Text)
	}
}

// TestDecodeChatHistory_NoCitations — a turn with refs == nil
// decodes to an empty Citations slice (not nil) so callers can
// range without a nil-check.
func TestDecodeChatHistory_NoCitations(t *testing.T) {
	turns := []any{
		makeTurn("model", "no citations here", nil, nil),
	}
	body := wrapEnvelope("conv-nocite", turns)
	conv, err := DecodeChatHistory(body)
	if err != nil {
		t.Fatalf("DecodeChatHistory: %v", err)
	}
	if conv.Turns[0].Citations == nil {
		t.Errorf("Citations is nil; want non-nil empty slice")
	}
	if len(conv.Turns[0].Citations) != 0 {
		t.Errorf("len(Citations) = %d, want 0", len(conv.Turns[0].Citations))
	}
	if conv.Turns[0].RawCitations != nil {
		t.Errorf("RawCitations = %+v, want nil", conv.Turns[0].RawCitations)
	}
}

// equalStrings is a small test helper.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestDecodeChatHistory_NoErrorOnEmptyEnvelopeAt_3 — when payload[3]
// is the empty envelope [conv_id, []], the decoder returns a
// Conversation with the id and zero turns (the empty-history case
// covered above). This is the regression guard for a panic that the
// v1 implementation hit when the empty-list was malformed.
func TestDecodeChatHistory_NoErrorOnEmptyEnvelopeAt_3(t *testing.T) {
	body := encodeJSON([]any{"wrb.fr", "khqZz", nil, []any{"conv-x", []any{}}})
	conv, err := DecodeChatHistory(body)
	if err != nil {
		t.Fatalf("DecodeChatHistory: %v", err)
	}
	if conv.ID != "conv-x" || len(conv.Turns) != 0 {
		t.Errorf("conv = %+v, want id=conv-x, 0 turns", conv)
	}
}

// TestDecodeChatHistory_TurnWithMissingCitationSlots — a citation
// whose source id slot is malformed (not a string) is silently
// dropped; the remaining citations are preserved.
func TestDecodeChatHistory_TurnWithMissingCitationSlots(t *testing.T) {
	refs := []any{
		// First citation: source id missing.
		[]any{"chunk", 42},
		makeRef("chunk", "src-good", 0, 4),
	}
	turns := []any{
		makeTurn("model", "mixed refs", refs, nil),
	}
	body := wrapEnvelope("conv-mixed-refs", turns)
	conv, err := DecodeChatHistory(body)
	if err != nil {
		t.Fatalf("DecodeChatHistory: %v", err)
	}
	if got := conv.Turns[0].Citations; len(got) != 1 || got[0] != "src-good" {
		t.Errorf("Citations = %v, want [src-good]", got)
	}
}

// TestDecodeChatHistory_LongText — a turn whose text is 10 KB
// decodes correctly. Guards against accidental O(n^2) loops in
// the citation-flattening path.
func TestDecodeChatHistory_LongText(t *testing.T) {
	long := strings.Repeat("lorem ipsum dolor sit amet ", 350) // ~10 KB
	turns := []any{
		makeTurn("model", long, nil, nil),
	}
	body := wrapEnvelope("conv-long", turns)
	conv, err := DecodeChatHistory(body)
	if err != nil {
		t.Fatalf("DecodeChatHistory: %v", err)
	}
	if conv.Turns[0].Text != long {
		t.Errorf("long text round-trip drift: len(got)=%d, len(want)=%d",
			len(conv.Turns[0].Text), len(long))
	}
}

// TestDecodeChatHistory_ExtractIDError — ExtractConversationID on an
// invalid body returns an error, not a panic.
func TestDecodeChatHistory_ExtractIDError(t *testing.T) {
	_, err := ExtractConversationID([]byte("garbage"))
	if err == nil {
		t.Errorf("ExtractConversationID: expected error on garbage input")
	}
}

// encodeConversation serializes c in the canonical wire shape.
// Mirrors the structure the server emits (wrb.fr wrapper, history
// envelope at slot 3).
func encodeConversation(c *Conversation) []byte {
	turns := make([]any, 0, len(c.Turns))
	for _, t := range c.Turns {
		refs := make([]any, 0, len(t.RawCitations))
		for _, r := range t.RawCitations {
			refs = append(refs, []any{"chunk", r.SourceID, []any{r.StartUTF16, r.EndUTF16}})
		}
		turn := []any{
			t.Author,
			[]any{t.Text, refs},
		}
		turns = append(turns, turn)
	}
	return wrapEnvelope(c.ID, turns)
}
