package wire

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"
)

// MaxStatusMessageChars caps the length of any sanitized server status
// message we surface to the user. Mirrors
// “_MAX_STATUS_MESSAGE_CHARS“ in
// notebooklm-py/src/notebooklm/_web/wire/decoder.py.
const MaxStatusMessageChars = 300

// userDisplayableErrorMarker is the substring Google's API embeds into a
// “google.rpc.Status“ details block when an error is meant for end-user
// display (rate-limit / quota rejections, mostly). Depth-capped scan
// constraints keep a server-controlled payload from blowing the stack —
// see doc 03 "Extract the frame for our RPC id" and the parallel
// “_contains_user_displayable_error“ in the Python decoder.
const userDisplayableErrorMarker = "UserDisplayableError"

// UserDisplayableErrorDepth is the maximum recursion depth the marker
// scan descends before giving up. Mirrors the Python default (20).
// Generous on purpose: real markers live a handful of levels deep, and
// the cap is a recursion-safety net rather than a precision tool.
const UserDisplayableErrorDepth = 20

// GrpcStatusCode is one canonical gRPC status value from
// “google.rpc.Code“. The numbers are normative — they map 1:1 to the
// Python enum (notebooklm-py/src/notebooklm/_types/enums.py::GrpcStatusCode)
// and to the wire byte the backend embeds at index 5 of a “wrb.fr“
// frame when “resultData“ is null.
//
// Distinct from any HTTP status code (NOT_FOUND on the gRPC axis is 5, on
// the HTTP axis is 404). Use a label, not the bare integer, anywhere the
// value crosses a transport boundary.
type GrpcStatusCode int

// GrpcStatus* constants enumerate the canonical gRPC status codes
// (google.rpc.Code, 0..16). Values are normative: changing them would
// drift away from the wire byte the backend embeds. Labels for the same
// set live in grpcStatusLabels below; the "canceled" label is spelled
// per the gRPC project's published status-code table
// (https://github.com/grpc/grpc/blob/master/doc/statuscodes.md) so
// operators correlating with grpc-go / grpc-python output get the exact
// string they search for.
const (
	GrpcStatusOK                 GrpcStatusCode = 0
	GrpcStatusCancelled          GrpcStatusCode = 1
	GrpcStatusUnknown            GrpcStatusCode = 2
	GrpcStatusInvalidArgument    GrpcStatusCode = 3
	GrpcStatusDeadlineExceeded   GrpcStatusCode = 4
	GrpcStatusNotFound           GrpcStatusCode = 5
	GrpcStatusAlreadyExists      GrpcStatusCode = 6
	GrpcStatusPermissionDenied   GrpcStatusCode = 7
	GrpcStatusResourceExhausted  GrpcStatusCode = 8
	GrpcStatusFailedPrecondition GrpcStatusCode = 9
	GrpcStatusAborted            GrpcStatusCode = 10
	GrpcStatusOutOfRange         GrpcStatusCode = 11
	GrpcStatusUnimplemented      GrpcStatusCode = 12
	GrpcStatusInternal           GrpcStatusCode = 13
	GrpcStatusUnavailable        GrpcStatusCode = 14
	GrpcStatusDataLoss           GrpcStatusCode = 15
	GrpcStatusUnauthenticated    GrpcStatusCode = 16
)

// grpcStatusLabels maps every known gRPC status to its canonical, user-safe
// label. The map keys are the canonical integer codes (0..16); lookups for
// codes outside that range return ("", false) — caller decides what to do
// with an unknown value (typically treat as INTERNAL).
var grpcStatusLabels = map[GrpcStatusCode]string{
	GrpcStatusOK: "OK",
	//nolint:misspell // CANCELLED is the canonical gRPC label spelling (see comment above).
	GrpcStatusCancelled:          "CANCELLED",
	GrpcStatusUnknown:            "UNKNOWN",
	GrpcStatusInvalidArgument:    "INVALID_ARGUMENT",
	GrpcStatusDeadlineExceeded:   "DEADLINE_EXCEEDED",
	GrpcStatusNotFound:           "NOT_FOUND",
	GrpcStatusAlreadyExists:      "ALREADY_EXISTS",
	GrpcStatusPermissionDenied:   "PERMISSION_DENIED",
	GrpcStatusResourceExhausted:  "RESOURCE_EXHAUSTED",
	GrpcStatusFailedPrecondition: "FAILED_PRECONDITION",
	GrpcStatusAborted:            "ABORTED",
	GrpcStatusOutOfRange:         "OUT_OF_RANGE",
	GrpcStatusUnimplemented:      "UNIMPLEMENTED",
	GrpcStatusInternal:           "INTERNAL",
	GrpcStatusUnavailable:        "UNAVAILABLE",
	GrpcStatusDataLoss:           "DATA_LOSS",
	GrpcStatusUnauthenticated:    "UNAUTHENTICATED",
}

// GrpcStatusLabel returns the canonical label for code, or the empty
// string when the code is outside the gRPC “Code“ namespace (e.g. a
// raw HTTP status slipped in). Use a label in user-facing messages and
// log records; never print the bare integer.
func GrpcStatusLabel(code GrpcStatusCode) string {
	return grpcStatusLabels[code]
}

// AllGrpcStatusCodes returns a snapshot of every (code, label) pair in the
// table, sorted by numeric code. The slice is fresh per call so callers may
// iterate it freely.
func AllGrpcStatusCodes() []GrpcStatusEntry {
	out := make([]GrpcStatusEntry, 0, len(grpcStatusLabels))
	for code, label := range grpcStatusLabels {
		out = append(out, GrpcStatusEntry{Code: code, Label: label})
	}
	// Stable sort by numeric code; the map walk order is otherwise
	// nondeterministic, so the slice's published order must not be.
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

// GrpcStatusEntry is one (code, label) row of the gRPC status table.
type GrpcStatusEntry struct {
	Code  GrpcStatusCode
	Label string
}

// AccountRoutingHint is the hint text appended to NOT_FOUND /
// PERMISSION_DENIED error messages. Deliberately worded to avoid the
// substrings the auth-error detector matches on, so a NOT_FOUND cannot
// trigger a spurious token refresh (see doc 03 "Classify a null
// result"). Keep the wording stable: the substring “"account index 0"“
// and “"multiple Google accounts"“ are part of the contract.
const AccountRoutingHint = " If you have multiple Google accounts signed in, this is commonly an account-routing mismatch — the request defaults to account index 0 when no authuser is set."

// SanitizeStatusMessage normalizes a raw “google.rpc.Status.message“
// slot into display text. Used at two layers — the batchexecute frame
// decoder and the streamed-chat envelope — so the two cannot drift in
// what users see for the same server rejection.
//
// Rules (port of notebooklm-py's “sanitize_status_message“):
//
//   - nil -> "" (callers treat empty as "no server message"; they fall
//     back to the client-authored message).
//   - a non-string, non-nil value is the drift shape (#2134). The
//     caller cannot have meant “42“ to be a reason, so we degrade
//     to a deterministic sentinel AND log a WARNING so the drift is
//     not silent. The drift warning never panics; this runs while
//     reporting an error, and crashing here would replace a real
//     server rejection with a decoder panic.
//   - a string is whitespace-collapsed (“" ".join(value.split())“)
//     and capped at MaxStatusMessageChars, with a trailing ellipsis
//     appended to any truncation so a reader sees the message was cut.
//
// The function never panics.
func SanitizeStatusMessage(value any) string {
	if value == nil {
		return ""
	}
	if s, ok := value.(string); ok {
		return sanitizeStringMessage(s)
	}
	slog.Warn("SanitizeStatusMessage received non-string value; degrading",
		slog.String("type", fmt.Sprintf("%T", value)),
	)
	return degradedStatusMessage()
}

// sanitizeStringMessage implements the whitespace-collapse + cap rules
// from the documentation on SanitizeStatusMessage. Split out so the
// hot-path non-nil case stays a single string op.
func sanitizeStringMessage(s string) string {
	collapsed := strings.Join(strings.Fields(s), " ")
	if collapsed == "" {
		return ""
	}
	if len(collapsed) > MaxStatusMessageChars {
		// Truncate, then rstrip whitespace so we never emit "…" right
		// after a stray space, and append a Unicode horizontal
		// ellipsis to mark the cut.
		truncated := strings.TrimRight(collapsed[:MaxStatusMessageChars], " \t\r\n") + "…"
		return truncated
	}
	return collapsed
}

// degradedStatusMessage returns the sentinel placed in a status message
// when the slot held a non-string value. Kept distinct from any real
// server message so a sweep of error strings can tell them apart at a
// glance.
func degradedStatusMessage() string {
	return "[invalid server status message type]"
}

// ContainsUserDisplayableError reports whether the payload anchored at v
// carries a “UserDisplayableError“ marker anywhere within
// UserDisplayableErrorDepth levels of nesting. The result is a boolean;
// callers wrap it in their own rate-limit/quota error type.
//
// The scan is depth-bounded because v is server-controlled — a
// maliciously or accidentally deep payload must not blow the stack
// (#2107 in the Python tracker). At the depth cap we treat the payload
// as carrying no marker; the caller's null-result handler falls through
// to its regular classification instead of synthesizing a false
// rate-limit signal.
//
// The scan walks maps and slices recursively, matching the Python
// implementation's behavior.
func ContainsUserDisplayableError(v any) bool {
	return containsUserDisplayableError(v, UserDisplayableErrorDepth)
}

// containsUserDisplayableError is the recursive worker. Split out so
// the public surface takes a sane (no-depth) argument and tests can
// drive the depth counter directly.
func containsUserDisplayableError(v any, depth int) bool {
	if depth <= 0 {
		return false
	}
	if s, ok := v.(string); ok {
		return strings.Contains(s, userDisplayableErrorMarker)
	}
	switch t := v.(type) {
	case []any:
		if depth == 1 {
			// Stop at the container so a wide slice at the boundary
			// reports a single "no marker found" rather than descending
			// once-per-element past our guard.
			return false
		}
		for _, item := range t {
			if containsUserDisplayableError(item, depth-1) {
				return true
			}
		}
		return false
	case map[string]any:
		if depth == 1 {
			return false
		}
		for _, val := range t {
			if containsUserDisplayableError(val, depth-1) {
				return true
			}
		}
		return false
	default:
		return false
	}
}
