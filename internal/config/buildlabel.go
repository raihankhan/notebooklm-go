// Package config — buildlabel.go.
//
// DEFAULT_BL is the pinned-default build label the chat endpoint
// accepts when NOTEBOOKLM_BL is unset. The build label is a
// Google-internal frontend version string (the "boq_…xxx…js"
// fingerprint) the server uses to choose the JS shim that parses
// the chat response. It drifts occasionally; the staleness helper
// IsBuildLabelStale compares the running binary's build-time
// buildlabel against the operator-supplied (or env-supplied) one
// and reports whether the binary is older than the override.
//
// Boundary: per docs/AGENTS.md rule 5, this package is mode=internal;
// it imports stdlib + internal/buildinfo + internal/redact only.
// The redact import exists so the build-label regex never accidentally
// echoes a credential-shaped substring in a warning log.
//
// docs/13-operations.md is the design spec for this file.
package config

import (
	"regexp"
	"time"

	"github.com/raihankhan/notebooklm-go/internal/buildinfo"
)

// DEFAULT_BL is the acceptable-default build-label string the chat
// RPC falls back to when NOTEBOOKLM_BL is unset. The format matches
// the canonical Google fingerprint shape:
//
//	boq_labs-tailwind-orchestration_YYYYMMDD.RR-XXX.0_p1
//
// where YYYYMMDD is a UTC date, RR is a build revision counter, and
// the trailing "0_p1" is the channel suffix. The exact value here
// is a placeholder that the binary's build-time buildlabel
// (injected via -ldflags in a later phase) replaces when the
// operator has not pinned their own.
const DEFAULT_BL = "boq_labs-tailwind-orchestration_placeholder.000000.0_p0"

// buildLabelPattern matches the canonical build-label shape. The
// regex is intentionally permissive about the date and revision so
// a future Google bump does not silently break the parser. Anchored
// at start/end to reject a stray substring.
//
//	^boq_labs-tailwind-orchestration_(\d{8})\.(\d+)\.0_p(\d+)$
var buildLabelPattern = regexp.MustCompile(
	`^boq_labs-tailwind-orchestration_(\d{8})\.(\d+)\.0_p(\d+)$`,
)

// BuildLabelParts is the parsed-out view of a build-label string.
// Date is the embedded UTC date as time.Time in UTC; Revision and
// Channel are the integer counter and channel suffix Google uses
// to choose the JS shim. An empty Date with a non-nil error means
// the input did not match the canonical shape.
type BuildLabelParts struct {
	Raw      string
	Date     time.Time
	Revision int
	Channel  int
}

// ParseBuildLabel splits a build label into its parts. The returned
// BuildLabelParts is zero-valued on error. The function never
// panics; any input that does not match the canonical pattern
// yields a typed error that the caller can route to a log line or
// to the runtime diagnostics counter.
func ParseBuildLabel(s string) (BuildLabelParts, error) {
	if s == "" {
		return BuildLabelParts{}, &BuildLabelError{Raw: s, Reason: "empty"}
	}
	m := buildLabelPattern.FindStringSubmatch(s)
	if m == nil {
		return BuildLabelParts{}, &BuildLabelError{
			Raw:    s,
			Reason: "does not match canonical boq_labs-tailwind-orchestration_YYYYMMDD.RR.0_pC shape",
		}
	}
	// m[1] is the date; parse it as YYYYMMDD. RFC 3339 is not the
	// shape we want; time.ParseInLocation with "20060102" gives us
	// the embedded date in UTC midnight.
	date, err := time.ParseInLocation("20060102", m[1], time.UTC)
	if err != nil {
		return BuildLabelParts{}, &BuildLabelError{
			Raw:    s,
			Reason: "embedded date is not YYYYMMDD: " + err.Error(),
		}
	}
	var rev, chanNum int
	if _, err := scanInt(m[2], &rev); err != nil {
		return BuildLabelParts{}, &BuildLabelError{
			Raw:    s,
			Reason: "revision is not an integer: " + err.Error(),
		}
	}
	if _, err := scanInt(m[3], &chanNum); err != nil {
		return BuildLabelParts{}, &BuildLabelError{
			Raw:    s,
			Reason: "channel is not an integer: " + err.Error(),
		}
	}
	return BuildLabelParts{
		Raw:      s,
		Date:     date,
		Revision: rev,
		Channel:  chanNum,
	}, nil
}

// BuildLabelError is returned by ParseBuildLabel on shape mismatch.
// The Raw field is the original input; Reason is a short label
// safe to surface to operators. The error wraps ErrBuildLabelShape
// so errors.Is(err, ErrBuildLabelShape) is the canonical check.
type BuildLabelError struct {
	Raw    string
	Reason string
}

func (e *BuildLabelError) Error() string {
	return "config: build label " + e.Raw + ": " + e.Reason
}

// Unwrap returns the sentinel so errors.Is works.
func (e *BuildLabelError) Unwrap() error { return ErrBuildLabelShape }

// ErrBuildLabelShape is the sentinel callers can match.
var ErrBuildLabelShape = errBuildLabelShape{}

type errBuildLabelShape struct{}

func (errBuildLabelShape) Error() string {
	return "config: build label does not match canonical shape"
}

// scanInt parses s as a base-10 integer without pulling in
// strconv on the hot path (the build-label parser is small enough
// that an inline scanner is cheaper than a function call).
func scanInt(s string, dst *int) (int, error) {
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, &buildLabelIntError{raw: s}
		}
		n = n*10 + int(c-'0')
	}
	*dst = n
	return n, nil
}

type buildLabelIntError struct{ raw string }

func (e *buildLabelIntError) Error() string {
	return "non-digit in " + e.raw
}

// IsBuildLabelStale compares the operator-supplied (or env-supplied)
// build label against the build-time injection captured by the
// binary. It returns true when the supplied label is OLDER than
// the build-time label — meaning the operator's override has
// lagged behind a frontend bump the binary already knows about.
//
// The comparison is a tuple (Date, Revision, Channel) with Date
// as the primary key. The boolean is the freshness direction:
// true means "the supplied label is older / stale", false means
// "the supplied label is at least as new as the build-time one".
//
// When the supplied label fails to parse, the function returns
// (true, parseErr) so the caller can surface both signals.
//
// When the build-time injection is the dev default ("unknown"),
// the function always returns (false, nil) — there is nothing to
// compare against.
func IsBuildLabelStale(supplied string) (bool, error) {
	suppliedParts, err := ParseBuildLabel(supplied)
	if err != nil {
		return true, err
	}
	bl := buildinfo.BuildLabel
	if bl == "" || bl == "unknown" {
		return false, nil
	}
	builtParts, err := ParseBuildLabel(bl)
	if err != nil {
		// A malformed build-time injection is a release bug, not a
		// runtime condition we can recover from. Report stale so the
		// operator sees the warning but do not block startup.
		return true, err
	}
	return olderThan(suppliedParts, builtParts), nil
}

// olderThan returns true when a is strictly older than b. The
// comparison key is (Date, Revision, Channel) in that order.
//
//	older == more stale == needs update
func olderThan(a, b BuildLabelParts) bool {
	if !a.Date.Equal(b.Date) {
		return a.Date.Before(b.Date)
	}
	if a.Revision != b.Revision {
		return a.Revision < b.Revision
	}
	return a.Channel < b.Channel
}
