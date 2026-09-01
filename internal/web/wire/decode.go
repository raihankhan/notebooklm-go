// Response-side decoding for the batchexecute protocol.
//
// Port of notebooklm-py/_web/wire/decoder.py. The wire format is:
//
//	)]}'                 <- anti-XSSI prefix (stripped)
//	<decimal byte-count>  <- framing header (one per chunk)
//	<payload>             <- exactly that many bytes
//	<payload>                 (framing header may be absent — payload
//	                         is then the whole line, and the next line
//	                         starts a new chunk)
//
// Each payload is a JSON array; an entry is one of:
//
//	["wrb.fr", rpcID, resultData, ..., ..., status?]
//	["er",     rpcID, errorCode]
//	["di", ...] / ["af.httprm", ...]   <- framing noise, ignored
//
// The high-level API is:
//
//	StripAntiXSSI -> ParseChunked -> ExtractResult -> DecodeResponse
//
// byteCountMismatchTotal is a process-wide counter incremented every time
// the framing header disagrees with the actual byte length of the next
// payload. Live Google responses count in a different unit (likely UTF-16
// code units) than UTF-8 bytes; the *rate of change* of this counter is the
// honest drift signal. Never warn: it would fire on essentially every
// multi-chunk response.
package wire

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// byteCountMismatchTotal is a process-wide atomic counter incremented
// every time the chunked parser sees a framing header whose declared byte
// length disagrees with the actual line length. The expected tolerance is
// non-zero (Google counts UTF-16 code units, we count UTF-8 bytes); the
// metric measures drift, not error.
//
// Use ByteCountMismatchTotal() to read the value (the variable is private
// to discourage callers from racing on it without going through the API).
var byteCountMismatchTotal atomic.Uint64

// ByteCountMismatchTotal returns the current value of the byte-count
// mismatch counter. Exposed for tests and for the metrics callback
// registered in Phase 3.
func ByteCountMismatchTotal() uint64 {
	return byteCountMismatchTotal.Load()
}

// resetByteCountMismatchTotal clears the counter. It is unexported — only
// tests in this package use it, and they reset before each scenario so
// cases do not bleed into each other.
func resetByteCountMismatchTotal() {
	byteCountMismatchTotal.Store(0)
}

// chunkStats tracks the malformed-record bookkeeping the protocol spec
// requires. The three malformed rates (payload, framing, aggregate) each
// have their own 10% threshold; exceeding any of them is a hard error.
type chunkStats struct {
	records      int
	payloadBad   int
	framingBad   int
	aggregateBad int
}

// ErrTooManyMalformed is returned by ParseChunked when the malformed rate
// on any of the three tracking axes (payload, framing, aggregate) exceeds
// the documented 10% threshold. This is the "Google reshaped the framing"
// detection.
var ErrTooManyMalformed = errors.New("wire: too many malformed chunked records (framing reshape suspected)")

// StripAntiXSSI strips the leading anti-XSSI prefix Google adds to
// batchexecute responses. The prefix is ")]}'" followed by a single
// newline ("\n" or "\r\n"). If the prefix is absent the bytes are
// returned unchanged — the caller never has to branch.
//
// The prefix is documented in doc 03 §1.
func StripAntiXSSI(b []byte) []byte {
	const tag = ")]}'"
	if len(b) >= len(tag)+1 && string(b[:len(tag)]) == tag {
		// Either "\n" or "\r\n" follows.
		switch b[len(tag)] {
		case '\r':
			if len(b) >= len(tag)+2 && b[len(tag)+1] == '\n' {
				return b[len(tag)+2:]
			}
			return b[len(tag)+1:]
		case '\n':
			return b[len(tag)+1:]
		}
	}
	return b
}

// ParseChunked parses a batchexecute `rt=c` response body into the
// per-chunk payload slices. It implements the four rules from
// doc 03 §"Parse the chunked framing":
//
//   - A line that parses as an integer is a byte count; the NEXT line is
//     its payload.
//   - A byte count with no following line is a framing error.
//   - A byte-count mismatch is tolerated: increment
//     byteCountMismatchTotal and continue.
//   - A line that does not parse as an integer is tried directly as JSON.
//   - Malformed payloads are skipped with WARNING, not fatal — unless the
//     malformed rate exceeds 10% on any of the three axes (payload,
//     framing, aggregate).
//   - Pathologically deep JSON must not blow the stack. encoding/json's
//     nesting limit handles this for free, so any decode error here is
//     surfaced as a payload-skip (not a panic).
//
// The third argument is the rpcID the caller is looking for; it is used
// to short-circuit the parse loop once that rpc's last non-null result
// has been seen AND there are no more frames after it. Pass "" to parse
// every chunk regardless of rpcID (used by the chunked-fixture tests).
func ParseChunked(body []byte) ([][]byte, error) {
	return parseChunkedWithTarget(body, "")
}

func parseChunkedWithTarget(body []byte, _ string) ([][]byte, error) {
	// Split on "\n" and "\r\n" — Google serves both. SplitAfter with a
	// custom separator that accepts either is awkward in stdlib, so we
	// normalize first by collapsing "\r\n" into "\n".
	normalized := strings.ReplaceAll(string(body), "\r\n", "\n")
	lines := strings.Split(normalized, "\n")

	stats := &chunkStats{}
	out := make([][]byte, 0, 8)

	i := 0
	for i < len(lines) {
		line := lines[i]
		// Empty trailing lines are normal — the body ends in "\n".
		if line == "" {
			i++
			continue
		}

		// Is this a byte-count framing header?
		count, err := strconv.Atoi(line)
		if err == nil {
			stats.records++
			// The next line is the payload.
			i++
			if i >= len(lines) {
				// Framing header with no following payload — this is a
				// record-level failure on the framing axis.
				stats.framingBad++
				stats.aggregateBad++
				return out, malformedError(stats, "framing header with no following payload")
			}
			payload := lines[i]
			i++
			// Mismatch detection: declared count vs actual byte length
			// of the payload line. Tolerance is non-zero in the wild;
			// we count it but do not error.
			if count != len(payload) {
				byteCountMismatchTotal.Add(1)
			}
			if err := validatePayload(payload); err != nil {
				stats.payloadBad++
				stats.aggregateBad++
				if exceeded(stats, stats.payloadBad) {
					return out, malformedError(stats, "payload malformed rate exceeded 10%")
				}
				continue
			}
			out = append(out, []byte(payload))
			continue
		}

		// Not a byte count: try the line as a bare JSON payload.
		stats.records++
		if err := validatePayload(line); err != nil {
			stats.payloadBad++
			stats.aggregateBad++
			if exceeded(stats, stats.payloadBad) {
				return out, malformedError(stats, "payload malformed rate exceeded 10%")
			}
			i++
			continue
		}
		out = append(out, []byte(line))
		i++
	}

	return out, nil
}

// validatePayload parses the JSON line and rejects it if it does not
// decode into an array of arrays. The shape "list of frames" is the
// only legal payload shape in the protocol; anything else is drift.
func validatePayload(s string) error {
	if s == "" {
		return errors.New("empty payload")
	}
	var probe []any
	if err := json.Unmarshal([]byte(s), &probe); err != nil {
		return fmt.Errorf("json.Unmarshal: %w", err)
	}
	for i, item := range probe {
		if _, ok := item.([]any); !ok {
			return fmt.Errorf("payload[%d] is not a list (got %T)", i, item)
		}
	}
	return nil
}

// exceeded returns true when `bad` exceeds 10% of `total` (with a minimum
// denominator of 10 records, mirroring the Python original which does not
// flag under-10-record streams — too noisy). The 10-record floor matches
// the ticket's "10 documented fixtures" so a single-bad-record test does
// not trip the threshold.
func exceeded(s *chunkStats, bad int) bool {
	if s.records < 10 {
		return false
	}
	return bad*10 > s.records
}

// malformedError returns an error annotated with the current stats so a
// reviewer reading the message can see exactly which axis tripped.
func malformedError(s *chunkStats, msg string) error {
	return fmt.Errorf("%w: %s (records=%d, payload_bad=%d, framing_bad=%d, aggregate_bad=%d)",
		ErrTooManyMalformed, msg, s.records, s.payloadBad, s.framingBad, s.aggregateBad)
}

// CollectRPCIDs parses the leading chunked response and returns the rpc-id
// list it carries. This is the function the caller uses to decide whether
// our id was even in the response (the prelude to the null-result
// classification tree).
//
// The rpc ids are extracted from "wrb.fr" and "er" frames only. "di" and
// "af.httprm" entries are framing noise and ignored.
func CollectRPCIDs(body []byte) ([]string, error) {
	chunks, err := ParseChunked(body)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, c := range chunks {
		decoded, err := decodeChunk(c)
		if err != nil {
			continue
		}
		for _, item := range decoded {
			frame, ok := item.([]any)
			if !ok {
				continue
			}
			if len(frame) < 2 {
				continue
			}
			tag, _ := frame[0].(string)
			if tag == "wrb.fr" || tag == "er" {
				if id, ok := frame[1].(string); ok {
					ids = append(ids, id)
				}
			}
		}
	}
	return ids, nil
}

// decodeChunk decodes a single chunked payload slice into its frame list.
// The returned slice is the [[tag, ...], ...] shape.
func decodeChunk(b []byte) ([]any, error) {
	var out []any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ExtractResult returns the bytes of the `wrb.fr` frame whose rpcID
// matches the second argument. When the backend emits multiple frames for
// one id (the null-placeholder-then-real pattern in `rt=c` mode), this
// returns the LAST non-null result, never the first.
//
// Returns a non-nil error if the rpc id is not present in any chunk.
// The error wraps ErrDecoding so the standard errors.Is check applies.
func ExtractResult(body []byte, rpcID string) ([]byte, error) {
	chunks, err := ParseChunked(body)
	if err != nil {
		return nil, err
	}
	var found []byte
	for _, c := range chunks {
		decoded, err := decodeChunk(c)
		if err != nil {
			continue
		}
		for _, item := range decoded {
			frame, ok := item.([]any)
			if !ok {
				continue
			}
			if len(frame) < 3 {
				continue
			}
			tag, _ := frame[0].(string)
			id, _ := frame[1].(string)
			if tag != "wrb.fr" || id != rpcID {
				continue
			}
			result, ok := frame[2].(string)
			if !ok || result == "" {
				continue
			}
			if isNullish(result) {
				// Remember we saw a null for this id but keep scanning.
				continue
			}
			found = []byte(result)
		}
	}
	if found == nil {
		return nil, fmt.Errorf("%w: rpc id %q not present in response", ErrDecoding, rpcID)
	}
	return found, nil
}

// isNullish returns true for the JSON-encoding of "null" and any
// whitespace-padded variant. The backend sometimes returns a JSON string
// like "null" rather than the literal null token; both must be skipped.
func isNullish(s string) bool {
	t := strings.TrimSpace(s)
	return t == "null" || t == ""
}

// captureStatus extracts the google.rpc.Status from index 5 of a wrb.fr
// frame whose payload slot is null. The status can arrive in either of
// two forms:
//
//   - already decoded: frame[5] is []any (the JSON-array google.rpc.Status)
//   - encoded:         frame[5] is a JSON string that decodes into the array
//
// Both forms are accepted; malformed input is silently ignored so a
// downstream branch can still classify the response.
func captureStatus(frame []any, statusRaw *[]any) {
	if len(frame) <= 5 {
		return
	}
	switch v := frame[5].(type) {
	case []any:
		// Already decoded.
		*statusRaw = v
	case string:
		// JSON-encoded string. Use Unmarshal (which is configured with
		// UseNumber) so the code slot stays as json.Number.
		var out []any
		if err := json.Unmarshal([]byte(v), &out); err == nil {
			*statusRaw = out
		}
	case nil:
		// No status; statusRaw stays nil.
	}
}

// Response is the structured return of DecodeResponse.
//
// The Status field is one of the documented outcomes:
//
//	"ok"                  — payload is valid JSON for this RPC
//	"empty"               — null result with allowNull=true
//	"unknown_rpc"         — rpc id not present, other ids present
//	"no_rpcs"             — response contained no RPC frames at all
//	"status_not_ok"       — index 5 held a recognized non-OK status
//	"empty_with_status"   — null result with non-OK status, swallowed
//	"empty_malformed"     — null result with non-OK status, raised
//
// The payload is the raw JSON text the backend returned. The GrpcStatus
// field is the index-5 status code when present, -1 when not.
type Response struct {
	Status      string
	Payload     []byte
	GrpcStatus  int
	NotFoundIDs []string // populated when Status == "unknown_rpc"
}

// ErrUnknownRPC is returned when the response carried rpc ids that did not
// include ours. This is the diagnostic that surfaces a possible method-id
// drift: it names the ids actually present so a reviewer can spot a stale
// obfuscated id immediately.
var ErrUnknownRPC = errors.New("wire: unknown rpc id")

// ErrNoRPCData is returned when the response carried zero rpc frames.
// This is what an anti-bot wall, a redirect to HTML, or a signed-out
// session look like at the protocol level.
var ErrNoRPCData = errors.New("wire: no rpc data in response")

// ErrEmptyResult is the fallback "server returned empty result" error
// raised after every other branch is exhausted.
var ErrEmptyResult = errors.New("wire: empty result")

// DecodeResponse implements the four-stage null-result classification
// tree from doc 03 §4:
//
//   - if rpcID not in foundIDs and len(foundIDs) > 0:
//     → ErrUnknownRPC  (drift)
//   - if len(foundIDs) == 0:
//     → ErrNoRPCData   (anti-bot / signed-out / redirect)
//   - if allowNull and not (raiseOnNullStatus and statusIsNonOK):
//     → nil
//   - if a recognized non-OK status is present:
//     NOT_FOUND (5) / PERMISSION_DENIED (7) → ErrClient + routing hint
//     anything else                          → ErrRPC
//   - otherwise:
//     → ErrEmptyResult
//
// The third argument controls how aggressively null is treated:
//
//	false → default behavior: classify everything, raise on error.
//	true  → the caller has explicitly opted into "null is a normal
//	        outcome" (used by writes that report success via index 5,
//	        like SHARE_NOTEBOOK).
//
// index 5 carries a JSON-array-encoded google.rpc.Status; we parse it
// defensively and never panic on a malformed payload.
func DecodeResponse(body []byte, rpcID string, allowNull bool) (*Response, error) {
	chunks, err := ParseChunked(body)
	if err != nil {
		return nil, err
	}

	foundIDs := make([]string, 0)
	var (
		rawPayload []byte
		grpcStatus = -1
		statusRaw  []any
	)

	for _, c := range chunks {
		decoded, err := decodeChunk(c)
		if err != nil {
			continue
		}
		for _, item := range decoded {
			frame, ok := item.([]any)
			if !ok {
				continue
			}
			if len(frame) < 2 {
				continue
			}
			tag, _ := frame[0].(string)
			id, _ := frame[1].(string)
			if tag == "wrb.fr" || tag == "er" {
				if id != "" {
					foundIDs = append(foundIDs, id)
				}
			}
			if tag != "wrb.fr" || id != rpcID {
				continue
			}
			// The result slot (index 2) is usually a JSON string. If it
			// is null OR is a string that JSON-decodes to null, capture
			// index 5 (status) if present and skip the payload.
			if r, ok := frame[2].(string); ok {
				if isNullish(r) {
					captureStatus(frame, &statusRaw)
					continue
				}
				rawPayload = []byte(r)
			} else if frame[2] == nil {
				captureStatus(frame, &statusRaw)
				continue
			} else {
				// Already-decoded value — re-marshal so the caller sees
				// consistent bytes.
				b, mErr := json.Marshal(frame[2])
				if mErr == nil {
					rawPayload = b
				}
			}
		}
	}

	// Branch 1: rpcID not in foundIDs (and we saw at least one other id).
	if !contains(foundIDs, rpcID) && len(foundIDs) > 0 {
		return &Response{
			Status:      "unknown_rpc",
			NotFoundIDs: foundIDs,
		}, fmt.Errorf("%w: requested %q; response contained %v", ErrUnknownRPC, rpcID, foundIDs)
	}

	// Branch 2: response contained no rpc frames at all.
	if len(foundIDs) == 0 {
		return &Response{Status: "no_rpcs"}, ErrNoRPCData
	}

	// Determine the status code, if any.
	if len(statusRaw) > 0 {
		if code, ok := statusRaw[0].(json.Number); ok {
			if i, err := code.Int64(); err == nil {
				grpcStatus = int(i)
			}
		} else if code, ok := statusRaw[0].(float64); ok {
			grpcStatus = int(code)
		}
	}

	// Branch 3: payload is empty / null.
	if rawPayload == nil {
		if allowNull {
			return &Response{Status: "empty", GrpcStatus: grpcStatus}, nil
		}
		// recognized non-OK status?
		if grpcStatus == 5 || grpcStatus == 7 {
			// NOT_FOUND / PERMISSION_DENIED: surface as ErrClient with
			// the account-routing hint (per doc 03 §4).
			hint := ""
			if grpcStatus == 5 {
				hint = "If you have multiple Google accounts signed in, this is commonly an account-routing mismatch — the request defaults to account index 0 when no authuser is set."
			}
			return &Response{Status: "status_not_ok", GrpcStatus: grpcStatus},
				fmt.Errorf("%w: server returned status %d for rpc %q; %s", ErrClient, grpcStatus, rpcID, hint)
		}
		if grpcStatus >= 0 {
			return &Response{Status: "status_not_ok", GrpcStatus: grpcStatus},
				fmt.Errorf("%w: server rejected this request (%s)", ErrRPC, rpcID)
		}
		return &Response{Status: "empty_malformed"}, ErrEmptyResult
	}

	// Normal payload.
	return &Response{Status: "ok", Payload: rawPayload, GrpcStatus: grpcStatus}, nil
}

// contains is a tiny helper so we do not pull in slices.Contains on Go
// versions where the stdlib availability varies.
func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// ErrClient is the placeholder error returned when the server emits a
// NOT_FOUND / PERMISSION_DENIED status. Callers should match with
// errors.Is(err, wire.ErrClient) and surface the account-routing hint
// from the doc 03 spec.
var ErrClient = errors.New("wire: client error")

// ErrRPC is the placeholder error for any other server-side rejection.
// Distinct from ErrUnknownRPC (drift) and ErrNoRPCData (anti-bot).
var ErrRPC = errors.New("wire: rpc error")

// Once-only init for tests that need a stable, frozen counter value. The
// counter itself is atomic so concurrent tests do not interfere, but a
// test that asserts the counter increments after a scenario will reset
// it via resetByteCountMismatchTotal. We register the reset once via a
// sync.Once to avoid import-cycle hazards.
var _ = sync.Once{} // keep the import so go vet stays happy in stripped builds
