// Package transport holds the HTTP kernel, the streaming POST
// entry point, and the typed error shapes the chain surfaces to
// every layer above.
//
// errors.go — the transport-layer error vocabulary. Every error
// value returned by the kernel, the stream, or the chain is one of
// the typed shapes declared here. The rules:
//
//  1. Sentinel errors are exposed via package-level `var`s so
//     callers can `errors.Is` against them. The chain layers
//     wrap with `%w` so the predicate works through a middleware
//     chain.
//  2. Concrete typed errors carry the contextual data a caller
//     needs to act — the byte count on a too-large response, the
//     generation on a stale epoch, the parsed duration on a
//     `Retry-After`. The data lives on the error, not in a
//     parallel log line.
//  3. Error text is routed through internal/redact so a credential
//     surfaced through an error message stays masked. The
//     `Error()` method assembles a short label, never the full
//     response preview.
//
// ParseRetryAfter implements both RFC 7231 §7.1.3 forms:
//   - Delta-seconds: `30` → 30s.
//   - HTTP-date: `Wed, 21 Oct 2026 07:28:00 GMT` → duration from
//     now until that instant.
//
// Boundary: per docs/AGENTS.md rule 5, this package is
// mode=internal; it imports stdlib + internal/redact + the three
// siblings listed in boundaries.yaml (internal/web/wire,
// internal/runtime, internal/redact). The `redact` import satisfies
// AGENTS.md rule 4: error messages must not leak a credential to a
// log line or user-visible stderr.
//
// docs/04-transport.md is the design spec for this file; see
// `docs/sprint-reports/S01-tickets.md#t-p3-2` for the ticket.
package transport

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/raihankhan/notebooklm-go/internal/redact"
)

// Sentinel errors. Callers `errors.Is` against these; the concrete
// typed errors wrap with `%w` so the predicate works across a
// middleware chain.
var (
	// ErrResponseTooLarge is returned by Post and PostStream when
	// the response body exceeds the configured size cap. The
	// concrete error carries the byte count actually observed.
	ErrResponseTooLarge = errors.New("transport: response body exceeded size cap")

	// ErrStaleEpoch is returned by AssertEpoch (and therefore by
	// Post / PostStream) when the generation an in-flight call
	// was issued under has been retired by FenceEpoch. The error
	// means the request must not be retried under the new
	// generation — its envelope was built from the prior auth
	// snapshot and may not match the current cookie jar.
	ErrStaleEpoch = errors.New("transport: epoch retired before send")

	// ErrKernelClosed is returned by Post / PostStream after
	// Close has been called. It is distinct from ErrStaleEpoch:
	// Close retires every generation, so a close-after-issue
	// POST is both ErrKernelClosed and ErrStaleEpoch; a
	// FenceEpoch-then-ActivateEpoch sequence lets fresh requests
	// proceed while still rejecting calls from the retired
	// generation.
	ErrKernelClosed = errors.New("transport: kernel is closed")

	// ErrRetryAfterUnparseable is returned by ParseRetryAfter when
	// the input is neither a delta-seconds integer nor an
	// RFC 7231 HTTP-date. The chain surfaces it as a typed
	// "no retry-after hint available" signal rather than a bare
	// parse error.
	ErrRetryAfterUnparseable = errors.New("transport: Retry-After is neither delta-seconds nor HTTP-date")
)

// TooLargeError carries the byte count that triggered the cap.
// The Limit field is the configured cap (in bytes); Got is what
// the caller observed. Both are exposed so a log line that names
// the error can render "<got> bytes (limit <limit>)" without
// stringifying a large response.
type TooLargeError struct {
	Limit int64
	Got   int64
}

func (e *TooLargeError) Error() string {
	return fmt.Sprintf(
		"transport: response body %d bytes exceeded cap of %d",
		e.Got, e.Limit,
	)
}

// Unwrap lets errors.Is(err, ErrResponseTooLarge) match.
func (e *TooLargeError) Unwrap() error { return ErrResponseTooLarge }

// StaleEpochError carries the generation the call was issued
// under and the current generation the kernel observed. A stale
// generation number is positive and less than Current.
type StaleEpochError struct {
	Issued  uint64
	Current uint64
}

func (e *StaleEpochError) Error() string {
	return fmt.Sprintf(
		"transport: epoch %d retired (current %d)",
		e.Issued, e.Current,
	)
}

// Unwrap lets errors.Is(err, ErrStaleEpoch) match.
func (e *StaleEpochError) Unwrap() error { return ErrStaleEpoch }

// ClosedError is returned when a request is issued to a kernel
// that has been Close'd. The CloserEpoch is the generation that
// the kernel retired during Close; it is informational only
// (the request can never be sent).
type ClosedError struct {
	CloserEpoch uint64
}

func (e *ClosedError) Error() string {
	return fmt.Sprintf(
		"transport: kernel closed at epoch %d",
		e.CloserEpoch,
	)
}

// Unwrap lets errors.Is(err, ErrKernelClosed) match.
func (e *ClosedError) Unwrap() error { return ErrKernelClosed }

// RetryAfterError carries the raw header value and the underlying
// parse failure (if any). It is wrapped by callers that want to
// surface "we received Retry-After but could not parse it" without
// logging the unparseable value verbatim — the raw value is
// routed through internal/redact before it reaches Error().
type RetryAfterError struct {
	Raw   string
	Cause error
}

func (e *RetryAfterError) Error() string {
	return fmt.Sprintf(
		"transport: Retry-After %q unparseable: %v",
		redact.Apply([]byte(e.Raw)), e.Cause,
	)
}

// Unwrap lets errors.Is(err, ErrRetryAfterUnparseable) match.
func (e *RetryAfterError) Unwrap() error { return ErrRetryAfterUnparseable }

// ParseRetryAfter parses a `Retry-After` header value into a
// duration from "now". Per RFC 7231 §7.1.3, the value may be
// either:
//
//   - Delta-seconds: a non-negative integer number of seconds
//     (e.g. "30"). Negative or non-numeric input is rejected.
//   - HTTP-date: an IMF-fixdate (e.g. "Wed, 21 Oct 2026 07:28:00
//     GMT"). Parsed with time.Parse(time.RFC1123, ...). A date
//     in the past yields 0 (the request is overdue; the caller
//     should not retry).
//
// An empty string is treated as "no hint" and yields an
// ErrRetryAfterUnparseable wrapped in a RetryAfterError, so a
// caller that wants to distinguish "no header at all" from
// "header present but unparseable" can inspect the raw value.
//
// now is injectable for tests; production code passes time.Now.
func ParseRetryAfter(s string, now func() time.Time) (time.Duration, error) {
	if s == "" {
		return 0, &RetryAfterError{
			Raw:   s,
			Cause: errors.New("empty value"),
		}
	}
	// Fast path: delta-seconds. We accept a leading whitespace
	// strip but reject anything else non-numeric, including a
	// trailing unit ("30s"). RFC 7231 is explicit: delta-seconds
	// is a non-negative integer with no unit.
	trimmed := strings.TrimSpace(s)
	if secs, err := parseDeltaSeconds(trimmed); err == nil {
		if secs < 0 {
			return 0, &RetryAfterError{
				Raw:   s,
				Cause: errors.New("negative delta-seconds"),
			}
		}
		return time.Duration(secs) * time.Second, nil
	}
	// Slow path: HTTP-date. RFC 7231 IMF-fixdate is RFC 1123.
	t, err := time.Parse(time.RFC1123, trimmed)
	if err != nil {
		return 0, &RetryAfterError{Raw: s, Cause: err}
	}
	d := t.Sub(now())
	if d < 0 {
		// The retry deadline is in the past. Return zero rather
		// than a negative duration so callers that compare
		// "sleep for Retry-After" don't busy-loop in the past.
		return 0, nil
	}
	return d, nil
}

// parseDeltaSeconds returns (seconds, nil) for a string of
// decimal digits with optional surrounding whitespace. Any
// other input returns (0, error). The function is split from
// ParseRetryAfter so the negative-seconds rejection lives at the
// caller.
func parseDeltaSeconds(s string) (int64, error) {
	if s == "" {
		return 0, errors.New("empty")
	}
	var n int64
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("non-digit %q", r)
		}
		n = n*10 + int64(r-'0')
	}
	return n, nil
}
