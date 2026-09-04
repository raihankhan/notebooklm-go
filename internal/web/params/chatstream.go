// Package params — chatstream.go: byte-stable request-payload builder for
// the `batchexecute` streamed-chat RPC surface.
//
// Per AGENTS.md rule 1 the Python source is normative. This file is the
// Go-side port of
// notebooklm-py/src/notebooklm/_web/params/chat_stream.py::build_streaming_chat_request
// — the body shape is the positional literal the streaming parser
// (T-S3-007c) reads, so the byte-exact golden output is the contract for
// both sides.
//
// The Go port differs from the Python original in two narrow ways that
// the upstream contract permits:
//
//  1. The Python builder composes the FULL HTTP request — URL, body,
//     headers — through AuthSnapshotLike and get_query_url. The Go port
//     owns ONLY the inner params body (and a small URL-parameter builder
//     for `_reqid`) and leaves URL composition / auth-snapshot stitching
//     to the transport layer (T-S3-007e + the chat SDK wrapper).
//
//  2. The persona slot is exposed explicitly via ChatStreamPersona. The
//     Python original threads the persona through the chat-config mutator
//     separately (see `s0tc2d` + RenamingNotebook wiring in the chat
//     config surface). For the streaming ask path the persona rides on
//     slot 3 of the params array; the contract pinned by this builder
//     is `[[source_ids], prompt, null, ChatStreamPersona(persona),
//     conversation_id, null, null, notebook_id, 1]`. A new
//     conversation is signaled by `conversationID == ""`.
//
// JSON encoding is delegated to wire.Marshal (AGENTS.md rule 3). The
// golden-bytes test in chatstream_test.go pins the byte output.
package params

import (
	"fmt"

	"github.com/raihankhan/notebooklm-go/internal/web/wire"
)

// chatStreamNotebookIDPlaceholder is the temporary stand-in the streaming
// parser (T-S3-007c) accepts for the notebook-id slot when the SDK layer
// has not yet injected the real id. The transport layer is expected to
// replace this placeholder with the actual notebook id before the
// request hits the wire — see the chat SDK surface in T-S3-007e.
//
// The placeholder is exported as ChatStreamNotebookIDPlaceholder so
// tests and the SDK layer can reference the same constant.
const chatStreamNotebookIDPlaceholder = "__NOTEBOOKLM_CHATSTREAM_NOTEBOOK_ID__"

// ChatStreamNotebookIDPlaceholder is the canonical stand-in for the
// notebook-id slot in the streaming-chat request body. The transport
// layer must substitute the real notebook id before sending. Exposed so
// tests and the SDK layer can reference the same constant.
const ChatStreamNotebookIDPlaceholder = chatStreamNotebookIDPlaceholder

// ChatStreamPersona builds the persona sub-array the chatstream RPC
// expects at params slot 3.
//
// The wire shape:
//
//   - persona != ""  → [persona]            (single-element array of the persona string)
//   - persona == ""  → [""]                 (the no-persona slot — an empty string is the documented sentinel)
//
// The empty-array / nil "no-persona" path returns a single empty-string
// slot. A literal empty `[]any` would change the encoded shape (slot 3
// becomes `[]` instead of `[""]`) and the streaming parser would
// reject the request with an opaque schema-drift error. The single-
// element array keeps the slot present while flagging "no persona" via
// the empty string value.
func ChatStreamPersona(persona string) []string {
	if persona == "" {
		return []string{""}
	}
	return []string{persona}
}

// BuildChatStreamRequest builds the wire-format request body for the
// chat-streaming RPC (the ask path).
//
// The body is a JSON array (then form-encoded by the transport) carrying:
//
//   - slot 0: source ids (nested at depth 2 per
//     notebooklm-py/src/notebooklm/_web/wire/encoder.py::nest_source_ids)
//   - slot 1: the user prompt
//   - slot 2: conversation history placeholder (null — the SDK layer
//     fills this when resending a conversation with prior turns; the
//     streaming parser treats null as "no prior history")
//   - slot 3: persona sub-array (see ChatStreamPersona)
//   - slot 4: conversation id (may be empty for a new conversation)
//   - slot 5: nil placeholder
//   - slot 6: nil placeholder
//   - slot 7: notebook id placeholder (transport substitutes the real id)
//   - slot 8: trailing 1 (the chat-RPC trailing literal)
//
// `reqID` is the per-request monotonic id surfaced via ReqidCounter.
// This function does NOT embed reqID in the body — the transport layer
// carries it as the `_reqid` URL parameter (see BuildChatStreamURLParams).
//
// Per AGENTS.md rule 1, the exact positional shape is pinned by the
// chatstream parser (T-S3-007c); the golden-bytes test in
// chatstream_test.go asserts byte-equality with the expected JSON.
//
// Returns an error if the inner params tree cannot be marshaled (a
// `[]any` literal built from plain Go strings / slices / ints always
// marshals cleanly; the error path exists so a future caller passing
// an exotic value — a channel, a function — fails loudly here rather
// than producing a malformed payload on the wire).
func BuildChatStreamRequest(prompt, conversationID string, sourceIDs []string, persona string, _ int) (string, error) {
	if prompt == "" {
		return "", &paramError{Field: "prompt", Reason: "must not be empty"}
	}

	sourcesArray := wire.NestSourceIDs(sourceIDs, 2)

	params := []any{
		sourcesArray,                    // slot 0: source ids, nested depth 2
		prompt,                          // slot 1: user prompt
		nil,                             // slot 2: conversation history placeholder
		ChatStreamPersona(persona),      // slot 3: persona sub-array
		conversationID,                  // slot 4: conversation id ("" → new conversation)
		nil,                             // slot 5: nil placeholder
		nil,                             // slot 6: nil placeholder
		chatStreamNotebookIDPlaceholder, // slot 7: notebook id (transport replaces)
		1,                               // slot 8: trailing literal
	}

	encoded, err := wire.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("params: BuildChatStreamRequest marshal: %w", err)
	}
	return string(encoded), nil
}

// BuildChatStreamURLParams formats the URL-query string for the
// streamed-chat RPC, including the `_reqid` URL parameter that the
// chat backend requires per request.
//
// `_reqid` is the monotonic request id from ReqidCounter; the wire
// protocol requires it to be a large positive integer so the transport
// layer must seed the counter at a non-zero baseline. The body shape
// itself is independent of `_reqid` — the transport layer composes the
// URL and the body from `BuildChatStreamURLParams` and
// `BuildChatStreamRequest` respectively.
//
// The returned string is the literal query component (without the
// leading "?"). Callers wrap it in the appropriate prefix.
func BuildChatStreamURLParams(reqID int) string {
	return fmt.Sprintf("rt=c&_reqid=%d", reqID)
}
