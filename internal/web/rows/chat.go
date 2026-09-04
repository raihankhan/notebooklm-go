// Package rows — chat.go: typed view of a decoded chat history payload.
//
// This is the Go-side port of
// notebooklm-py/src/notebooklm/_web/rows/chat.py (the 1199-line Python
// history decoder). The decoder turns a `GetConversation` RPC response
// (method `khqZz`) into a typed `Conversation` value: a stable id plus a
// sequence of `Turn` rows.
//
// Per AGENTS.md rule 1 the Python original is normative. The wire shape
// this file consumes is positional — every index below is documented and
// pinned by the Python decoder's own position-contract table. A drift on
// any of those indices is the silent-failure class the rest of the
// package is built to surface (see internal/web/wire/index.go).
//
// Per AGENTS.md rule 3, JSON parsing routes through wire.Unmarshal (so
// large integer ids arrive as json.Number rather than losing precision
// through float64).
//
// Per AGENTS.md rule 5 this package is mode=internal: stdlib only. The
// unicode/utf16 primitive lives in this same package (utf16.go), so we
// do not need to cross a boundary to compute UTF-16 slice offsets.
package rows

import (
	"encoding/json"
	"fmt"

	"github.com/raihankhan/notebooklm-go/internal/web/wire"
)

// Conversation is the decoded chat history.
//
// ID is the conversation id recovered from the wire payload. The
// GetConversation response occasionally omits the id slot when the SDK
// asks for a fresh conversation; in that case ExtractConversationID
// recovers it from the deepest stable position in the payload. The
// decoder always returns a non-empty ID — an empty ID is treated as a
// shape drift.
//
// Turns is the ordered sequence of turns the conversation contains. The
// list is non-nil (an empty history decodes to a zero-length slice, not
// nil) so callers can range over it without a nil-check.
type Conversation struct {
	ID    string
	Turns []Turn
}

// Turn is the typed view of one chat turn (one user prompt or one model
// reply).
//
// Author is the wire author tag: "user" for human prompts, "model" for
// assistant replies. The decoder preserves the exact byte sequence the
// server emitted (no lowercasing); callers comparing against canonical
// labels should be case-insensitive or normalize on read.
//
// Text is the turn's content. For model turns the text is the full
// answer (without slicing); the slice-by-citation-anchor view is
// performed by the streaming parser (T-S3-007c) on the wire response.
// For user turns Text is the prompt text.
//
// Citations is the list of source ids this turn cites. The list is
// non-nil (zero-length when the turn has no citations, missing citations
// are filtered, malformed citations are skipped). The wire format wraps
// each citation as a triple; the decoder flattens that to the source
// id string only.
//
// CreatedAt is the wall-clock creation timestamp the server stamped on
// the turn. nil when absent or zero.
//
// RawCitations is the typed view of the per-turn reference block —
// kept so future tickets (T-S3-007e and the SDK layer) can surface
// citation metadata (chunk text, source title, span offsets) without
// having to re-decode the wire. The field is best-effort: nil when the
// reference block is absent.
type Turn struct {
	Author       string
	Text         string
	Citations    []string
	CreatedAt    *float64   `json:",omitempty"`
	RawCitations []Citation `json:",omitempty"`
}

// Citation is the typed view of one citation reference inside a model
// turn.
//
// The wire format the Python original parses is roughly:
//
//	[citation_kind, source_id, [chunk_text, [start_utf16, end_utf16]]]
//
// where the inner triple carries the cited-text window as a UTF-16 code
// unit pair (the same primitive T-S3-007a owns). The decoder surfaces
// only the typed fields the chat SDK layer cares about today; deeper
// fields (chunk text, source title) land in later tickets.
//
// SourceID is the stable source id this citation points at. Empty when
// the slot is malformed or absent.
//
// StartUTF16 / EndUTF16 are the UTF-16 code-unit window into the source's
// cited passage. Both are best-effort: zero when absent.
type Citation struct {
	SourceID   string
	StartUTF16 int
	EndUTF16   int
}

// DecodeChatHistory parses a GetConversation RPC response body into a
// typed Conversation value.
//
// The input is the raw HTTP body returned by the
// MethodGetConversationTurns RPC. The wire shape, per the Python
// position-contract table:
//
//	[0]  wrb.fr tag
//	[1]  RPC id ("khqZz")
//	[2]  status / null
//	[3]  result data — the history envelope (or, in the rare wrapped
//	     form, the conversation id sits at this slot with the turns
//	     nested one level deeper)
//	[4]  conversation id (when [3] is the history envelope) OR
//	     the history envelope itself (when [3] is the conversation id)
//
// Where the history envelope is:
//
//	[conv_id, [turn1, turn2, ...]]
//
// and each turn is:
//
//	[author, [text, refs_block, ...], created_at, ...]
//
// The decoder is tolerant: a missing top-level slot degrades to an
// empty Conversation rather than erroring out. A payload that cannot be
// recognized as a chat-history response at all (no recognizable
// envelope) returns an error so callers can route it to the schema-
// drift metric.
func DecodeChatHistory(body []byte) (*Conversation, error) {
	payload, err := unmarshalChatPayload(body)
	if err != nil {
		return nil, err
	}

	conv := &Conversation{Turns: []Turn{}}

	// The wire envelope has two shapes; we accept both.
	//
	//   Shape A (typical): payload[3] is the history envelope
	//       [conv_id, [turn1, turn2, ...]]
	//   Shape B (alt wrap): payload[3] is the conversation id alone, and
	//       the history envelope lives one slot deeper (payload[4]).
	//
	// We detect by trying Shape A first and falling back to Shape B
	// when the first slot is not a list-of-turns. The shape-detect
	// path is best-effort: Shape A always wins when it parses.

	root, err := wire.List(payload, 3)
	if err == nil {
		if id, turns, ok := tryHistoryEnvelope(root); ok {
			conv.ID = id
			conv.Turns = turns
			return conv, nil
		}
		// [3] is a list whose shape we did not recognize. Try the
		// direct-history-envelope fallback below.
	}

	// Shape B: payload[3] is the id alone (string at slot 3).
	if rootList, ok := payload.([]any); ok && len(rootList) > 3 {
		if idStr, ok := rootList[3].(string); ok && idStr != "" {
			return convWithIDFromSlotB(payload, idStr, conv)
		}
	}

	// Both shapes rejected. If the payload itself looks like a history
	// envelope (rare but observed in captures), use it directly.
	if innerID, turns, ok := tryHistoryEnvelope(payload); ok {
		conv.ID = innerID
		conv.Turns = turns
		return conv, nil
	}

	return nil, fmt.Errorf("rows: DecodeChatHistory: unrecognized chat-history envelope shape (no conversation id or turns found)")
}

// convWithIDFromSlotB recovers a conversation when payload[3] carries
// just the conversation id (the alt-wrap shape). It then walks payload[4]
// for the history envelope and returns the assembled Conversation.
//
// The slot-3 id is authoritative for this shape: the envelope id at
// slot-4[0] is informational (sometimes the server omits the inner
// id, sometimes it echoes the outer one). When the inner envelope
// has a different id we keep the slot-3 id, since the alt-wrap
// shape's whole purpose is to surface the conversation id directly.
func convWithIDFromSlotB(payload any, id string, conv *Conversation) (*Conversation, error) {
	conv.ID = id
	// The envelope at slot 4 is optional for this shape: when the SDK
	// only asked for the id the server is allowed to omit the turns
	// slot entirely. We try the slot directly rather than going through
	// wire.List so the "slot absent" case is a typed nil-check, not an
	// error swallowed silently.
	rootList, ok := payload.([]any)
	if !ok || len(rootList) <= 4 {
		return conv, nil
	}
	if envelope, ok := rootList[4].([]any); ok {
		if _, turns, ok := tryHistoryEnvelope(envelope); ok {
			conv.Turns = turns
		}
	}
	return conv, nil
}

// ExtractConversationID recovers the conversation id from a wrapped
// RPC response payload even when the response field does not echo it
// directly. The id lives at one of two slots depending on the wrap:
//
//   - Typical wrap: payload[3][0]
//   - Alt wrap:     payload[3] as a bare string
//
// The function is a thin, public-side companion to DecodeChatHistory.
// T-S3-007e's ChatAPI.GetConversationID calls it on the raw body
// before the SDK has decided whether it needs the full turn list.
//
// An id that cannot be recovered (no recognizable id slot) returns an
// error so the SDK can fail loudly rather than silently treating an
// empty string as a fresh-conversation sentinel.
func ExtractConversationID(body []byte) (string, error) {
	payload, err := unmarshalChatPayload(body)
	if err != nil {
		return "", err
	}

	// Shape A: payload[3] is the history envelope [id, turns].
	if envelope, err := wire.List(payload, 3); err == nil {
		if id, _, ok := tryHistoryEnvelope(envelope); ok && id != "" {
			return id, nil
		}
	}

	// Shape B: payload[3] is the id alone.
	if id, ok := payload.([]any); ok && len(id) > 3 {
		if idStr, ok := id[3].(string); ok && idStr != "" {
			return idStr, nil
		}
	}

	return "", fmt.Errorf("rows: ExtractConversationID: no conversation id found in payload")
}

// tryHistoryEnvelope inspects v and, if it looks like a history
// envelope ([id, [turn1, turn2, ...]]), returns the id plus the
// decoded turns. Returns ("", nil, false) when v does not match.
//
// A genuine envelope has TWO positional slots:
//
//	list[0]    = conversation id (string)
//	list[1]    = list of turns ([]any — may be empty)
//
// A list whose first two elements are both strings is rejected, since
// the typical wrb.fr wrapper ["wrb.fr", rpc_id, ...] would otherwise
// be mistaken for an envelope. The function ALSO accepts the rare
// reversed shape [turns, id] but only when list[0] is a list of
// turns (a further guard against false matches on the wrb.fr
// wrapper).
func tryHistoryEnvelope(v any) (string, []Turn, bool) {
	list, ok := v.([]any)
	if !ok || len(list) < 2 {
		return "", nil, false
	}
	// Fast shape check: list[1] must be a list of turns for the
	// standard shape. We require that to filter out the wrb.fr
	// wrapper, whose list[1] is the rpc id (a string).
	if id, ok := list[0].(string); ok && id != "" {
		turnsList, ok := list[1].([]any)
		if !ok {
			return "", nil, false
		}
		turns, err := decodeTurns(turnsList)
		if err != nil {
			return id, nil, false
		}
		return id, turns, true
	}
	// Reversed shape: [turns, id] — only when list[0] is a list of
	// turns and list[1] is a string id.
	if turnsList, ok := list[0].([]any); ok {
		if idAlt, okAlt := list[1].(string); okAlt && idAlt != "" {
			turns, err := decodeTurns(turnsList)
			if err != nil {
				return "", nil, false
			}
			return idAlt, turns, true
		}
	}
	return "", nil, false
}

// decodeTurns walks a list-of-turns slot and returns the typed slice.
// Each turn is a 3-or-more element positional list:
//
//	[author, [text, refs], created_at, ...]
//
// A turn whose author is not a string or whose text slot is missing
// is skipped (the Python original silently drops malformed turns).
// An entirely-unrecognizable turns slot returns an error so the SDK
// can count the drift.
func decodeTurns(list []any) ([]Turn, error) {
	out := make([]Turn, 0, len(list))
	for _, raw := range list {
		t, ok := raw.([]any)
		if !ok {
			continue
		}
		if len(t) < 1 {
			continue
		}
		author, ok := t[0].(string)
		if !ok {
			continue
		}

		turn := Turn{
			Author:    author,
			Text:      "",
			Citations: []string{},
		}

		// Text + citations slot.
		if len(t) > 1 {
			if content, ok := t[1].([]any); ok {
				turn.Text, turn.RawCitations = decodeTurnContent(content)
				turn.Citations = flattenCitations(turn.RawCitations)
			}
		}

		// Created-at slot (best-effort float).
		if len(t) > 2 {
			if f, ok := asFloat(t[2]); ok {
				turn.CreatedAt = &f
			}
		}

		out = append(out, turn)
	}
	return out, nil
}

// decodeTurnContent extracts the text and citation references from the
// turn-content slot.
//
// The wire shape is roughly:
//
//	[text_string, [citation1, citation2, ...], ...]
//
// where each citation is itself a list:
//
//	[kind, source_id, [chunk_text, [start_utf16, end_utf16]], ...]
//
// The decoder returns the text verbatim and the typed citation list;
// flattenCitations is the convenience for callers that only need the
// source-id strings.
func decodeTurnContent(content []any) (string, []Citation) {
	if len(content) == 0 {
		return "", nil
	}
	text, _ := content[0].(string)

	var citations []Citation
	if len(content) > 1 {
		refs, ok := content[1].([]any)
		if ok {
			citations = decodeCitations(refs)
		}
	}
	return text, citations
}

// decodeCitations walks the per-turn references block. Each reference
// is a positional list; the source id sits at slot 1 of the inner list
// (slot 0 is the kind tag). References whose source id is not a string
// are skipped.
func decodeCitations(refs []any) []Citation {
	if len(refs) == 0 {
		return nil
	}
	out := make([]Citation, 0, len(refs))
	for _, raw := range refs {
		c, ok := raw.([]any)
		if !ok || len(c) < 2 {
			continue
		}
		sourceID, ok := c[1].(string)
		if !ok {
			continue
		}
		cit := Citation{SourceID: sourceID}
		// Slot 2 carries the chunk + UTF-16 offset pair when present.
		if len(c) > 2 {
			if span, ok := c[2].([]any); ok {
				if start, ok := asFloat(safeIndex(span, 0)); ok {
					cit.StartUTF16 = int(start)
				}
				if end, ok := asFloat(safeIndex(span, 1)); ok {
					cit.EndUTF16 = int(end)
				}
			}
		}
		out = append(out, cit)
	}
	return out
}

// flattenCitations returns just the source ids from a citation slice.
// Preserves order; skips citations with empty source ids.
func flattenCitations(citations []Citation) []string {
	if len(citations) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(citations))
	for _, c := range citations {
		if c.SourceID == "" {
			continue
		}
		out = append(out, c.SourceID)
	}
	return out
}

// SliceAnswerByCitation returns the cited-text window for a turn's
// answer. It uses the UTF-16 offset primitive to slice the model text
// using the (start, end) pair from a citation.
//
// This helper is exported so the SDK layer (T-S3-007e) and the studio
// projection can surface cited passages without having to know about
// the UTF-16 conversion themselves. Empty / out-of-range offsets
// return ""; the wire format never delivers a citation whose start is
// greater than the answer length.
func SliceAnswerByCitation(answer string, c Citation) string {
	if answer == "" || c.SourceID == "" {
		return ""
	}
	return UTF16Slice(answer, c.StartUTF16, c.EndUTF16)
}

// unmarshalChatPayload runs wire.Unmarshal on body. Returns the
// decoded root value (a map[string]any for the standard wrb.fr
// envelope or []any for the bare-array variant).
func unmarshalChatPayload(body []byte) (any, error) {
	var v any
	if err := wire.Unmarshal(body, &v); err != nil {
		return nil, fmt.Errorf("rows: chat payload: json: %w", err)
	}
	return v, nil
}

// safeIndex returns list[idx] when idx is in range, nil otherwise.
// The wire payloads occasionally have shorter sub-arrays than the
// Python original expects; safeIndex keeps the decoder robust without
// panicking on a length mismatch.
func safeIndex(list []any, idx int) any {
	if idx < 0 || idx >= len(list) {
		return nil
	}
	return list[idx]
}

// asFloat coerces a json.Number / int / float64 to float64. Returns
// false when v is not a numeric type. The wire format uses json.Number
// for large integer ids (see wire.Unmarshal + UseNumber); the citation
// offsets are small enough to fit in float64 without loss.
func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	}
	return 0, false
}
