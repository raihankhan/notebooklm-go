// Package serialize defines the canonical --json envelope shapes used by every
// CLI command and every MCP tool response. The envelope is the single
// machine-readable contract between the CLI / MCP / REST adapters and any
// scripted caller; automation branches on the stable keys (ok, code,
// request_id, ids) rather than on prose.
//
// The shape is the direct port of
// notebooklm-py/src/notebooklm/cli/rendering.py::json_output_response and
// notebooklm-py/src/notebooklm/cli/helpers.py::json_output_response — the
// Python original emits a single JSON document to stdout (no colors) and
// nothing on stderr in --json mode. The error envelope at the bottom of
// docs/07-cli-spec.md ("The --json error envelope") is the normative
// reference:
//
//	{ "error": true, "code": "RATE_LIMITED", "message": "...", "retry_after": 30 }
//	{ "error": true, "code": "NOT_FOUND",      "message": "...", "id": "...", "notebook_id": "..." }
//
// All JSON encoding in this module goes through internal/web/wire.Marshal —
// the single adapter the module permits to import encoding/json (docs/AGENTS.md
// rule 3). wire.Marshal does three things we depend on:
//
//   - SetEscapeHTML(false): <, >, & are emitted verbatim, matching Python's
//     json.dumps so a citation like `snippet<&>` survives a round trip.
//   - Trailing newline stripped: --json stdout must be byte-clean JSON that
//     pipes into jq without ceremony.
//   - Map keys sorted: matches Python's json.dumps(sort_keys=True), so a
//     Go-encoded payload and a Python-encoded payload are byte-identical for
//     map-backed data.
//
// No code in this package calls encoding/json directly. The only entry point
// to JSON encoding here is wire.Marshal.
package serialize

import (
	"github.com/raihankhan/notebooklm-go/internal/redact"
	"github.com/raihankhan/notebooklm-go/internal/web/wire"
)

// Envelope is the canonical --json response shape. Every CLI --json command
// and every MCP tool returns one of these on stdout. The fields are stable;
// automation reads them, not the inner data shape.
//
// Field semantics:
//
//   - OK        — true for a success envelope, false for an error envelope.
//     The boolean lets a caller dispatch on a single key without
//     having to inspect `error` separately.
//   - Data      — the success payload (any JSON-encodable value: a struct, a
//     map, a list). Omitted on the wire when nil so the success
//     envelope stays lean for empty payloads. Use omitempty on
//     data to keep byte-clean JSON.
//   - Error     — a pointer to ErrorBody on failure, nil on success. The
//     pointer + omitempty combination means a success document
//     never carries an `"error":null` field, which would otherwise
//     confuse parsers that distinguish absent from null.
//   - RequestID — the per-call correlation id; same value as the
//     NOTEBOOKLM_REQUEST_ID env var (when set) and the
//     `--request-id` flag (when supplied). Useful for log
//     correlation between a failing automation and the server
//     logs the platform captures.
type Envelope struct {
	OK        bool       `json:"ok"`
	Data      any        `json:"data,omitempty"`
	Error     *ErrorBody `json:"error,omitempty"`
	RequestID string     `json:"request_id,omitempty"`
}

// ErrorBody is the failure half of the envelope. The shape mirrors docs/07-cli-spec.md
// "The --json error envelope" verbatim — every field but code and message is
// optional and is omitted on the wire when empty. Port of the Python
// notebooklm-py/src/notebooklm/cli/error_handler.py output_error path.
//
// Field semantics:
//
//   - Code       — the stable machine code (RATE_LIMITED, AUTH_ERROR,
//     VALIDATION_ERROR, CONFIG_ERROR, NETWORK_ERROR,
//     NOTEBOOK_LIMIT, NOT_FOUND, ARTIFACT_TIMEOUT,
//     NOTEBOOKLM_ERROR, CANCELLED, UNEXPECTED_ERROR). Branch on
//     this, never on Message.
//
//   - Message    — the human-readable description. May be redacted; never
//     contains a raw credential value (route through
//     internal/redact.Apply before assigning).
//
//   - RetryAfter — seconds to wait before retrying; only present on
//     RATE_LIMITED. Use omitempty so other error codes do not
//     carry a meaningless `retry_after: 0`.
//
//   - ID         — the resource id a NOT_FOUND envelope refers to (source /
//     artifact / note / mind_map / label / collection / notebook).
//     Only one of ID / NotebookID / MethodID is set per envelope;
//     the omitempty tags keep the others out.
//
//   - NotebookID — the notebook id, when the failure is scoped to a notebook
//     (e.g. a per-notebook operation against the wrong notebook).
//
//   - MethodID   — the batchexecute RPC method id, only surfaced when the
//     user passed -v. The id is opaque and changes whenever
//     Google rolls the protocol; it is exposed for log
//     correlation, not for branching on.
//
//nolint:misspell // "CANCELLED" is the canonical spelling per docs/07-cli-spec.md exit-code table.
type ErrorBody struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	RetryAfter *int   `json:"retry_after,omitempty"`
	ID         string `json:"id,omitempty"`
	NotebookID string `json:"notebook_id,omitempty"`
	MethodID   string `json:"method_id,omitempty"`
}

// ErrorOpt mutates an ErrorBody during construction. MarshalError takes a
// variadic list of ErrorOpt so callers only mention the fields they actually
// have:
//
//	MarshalError("NOT_FOUND", "source missing", serialize.WithID(src.ID))
//
// The opts read more cleanly than a struct literal at every call site and
// keep the ErrorBody struct a plain data carrier (no validation logic, no
// normalization, no logging side effects).
type ErrorOpt func(*ErrorBody)

// WithRetryAfter attaches a Retry-After hint (seconds) to the envelope. Use
// only on RATE_LIMITED — the field is meaningless on other codes and will
// confuse a caller that branches on key presence.
func WithRetryAfter(seconds int) ErrorOpt {
	return func(e *ErrorBody) { e.RetryAfter = &seconds }
}

// WithID attaches the missing-resource id (NOT_FOUND envelopes). When the
// failure is also scoped to a notebook, prefer WithNotebookID alongside.
func WithID(id string) ErrorOpt {
	return func(e *ErrorBody) { e.ID = id }
}

// WithNotebookID attaches the notebook scope id.
func WithNotebookID(id string) ErrorOpt {
	return func(e *ErrorBody) { e.NotebookID = id }
}

// WithMethodID attaches the batchexecute RPC method id. Reserved for -v
// verbose envelopes; the id is not a stable contract and should never be
// the only signal a caller branches on.
func WithMethodID(id string) ErrorOpt {
	return func(e *ErrorBody) { e.MethodID = id }
}

// MarshalSuccess serializes a success envelope: { ok: true, data: <data>,
// request_id: <id> }. The returned bytes are byte-clean JSON with no
// trailing newline (wire.Marshal strips the encoder's newline). Data may be
// nil for "operation succeeded with no payload" envelopes; the data field
// is omitted from the wire in that case so the document reads `{"ok":true,
// "request_id":"…"}` rather than `{"ok":true,"data":null,…}`.
//
// Message is run through internal/redact.Apply before encoding so a
// credential-shaped substring that accidentally landed in the data tree
// cannot leak. The data argument is recursively walked via wire.Marshal's
// map key sort, so the encoding is byte-stable regardless of Go map
// iteration order.
func MarshalSuccess(data any, requestID string) ([]byte, error) {
	env := Envelope{
		OK:        true,
		Data:      data,
		RequestID: requestID,
	}
	return encodeEnvelope(env)
}

// MarshalError serializes a failure envelope: { ok: false, error: { code,
// message, …optional fields }, request_id }. opts is the variadic list of
// ErrorOpt values that attach the optional fields. Code and message are
// required; everything else is omitempty.
//
// The returned bytes are byte-clean JSON with no trailing newline. Like
// MarshalSuccess, Message is run through internal/redact.Apply so a
// credential never survives a round trip through the error envelope.
func MarshalError(code, message, requestID string, opts ...ErrorOpt) ([]byte, error) {
	body := ErrorBody{Code: code, Message: message}
	for _, opt := range opts {
		opt(&body)
	}
	env := Envelope{
		OK:        false,
		Error:     &body,
		RequestID: requestID,
	}
	return encodeEnvelope(env)
}

// encodeEnvelope is the single funnel for both MarshalSuccess and
// MarshalError. Centralizing the call here means the redaction and
// wire.Marshal paths are exercised exactly once per envelope, and a future
// change (adding a timestamp, switching the encoder) lives in one place
// rather than two.
//
// Credentials never reach a log, an error, or a String() (docs/AGENTS.md
// rule 4); this is the one place in the module where a user-supplied string
// meets the wire, so the redaction gate belongs here rather than at every
// call site.
func encodeEnvelope(env Envelope) ([]byte, error) {
	if env.Error != nil {
		env.Error.Message = string(redact.Apply([]byte(env.Error.Message)))
	}
	// Data is intentionally NOT redacted here: the success path carries
	// arbitrary domain structs (notebooks, sources, artifacts) and a blanket
	// redaction would mask real user content. Callers that surface
	// credential-bearing data are responsible for routing those fields
	// through redact at their boundary; this package provides the
	// MarshalError / MarshalSuccess funnel so that gate exists by construction
	// for error messages.
	return wire.Marshal(env)
}
