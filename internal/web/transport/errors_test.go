// Package transport — errors_test.go.
//
// Tests for the transport error vocabulary and ParseRetryAfter.
// Table-driven; each test asserts a single behavioral property
// of the parser or the typed error wrapping.
package transport

import (
	"errors"
	"testing"
	"time"
)

// fixedNow returns a deterministic time suitable for the
// HTTP-date parser tests. The instant is 2026-10-21 07:28:00 UTC,
// matching the date in the AC2 test fixture.
func fixedNow() time.Time {
	return time.Date(2026, 10, 21, 7, 28, 0, 0, time.UTC)
}

// fixedEarlier returns 30 seconds before fixedNow. Used as the
// "now" for the delta-seconds test so the parsed HTTP-date can
// be cross-checked.
func fixedEarlier() time.Time {
	return fixedNow().Add(-30 * time.Second)
}

func TestParseRetryAfter_DeltaSeconds(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want time.Duration
	}{
		{"zero", "0", 0},
		{"small", "1", time.Second},
		{"doc-fixture", "30", 30 * time.Second},
		{"large", "3600", time.Hour},
		{"with-spaces", "  42  ", 42 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseRetryAfter(tt.in, fixedNow)
			if err != nil {
				t.Fatalf("ParseRetryAfter(%q) error = %v, want nil", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("ParseRetryAfter(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseRetryAfter_HTTPDate(t *testing.T) {
	// Per AC2: ParseRetryAfter("Wed, 21 Oct 2026 07:28:00 GMT")
	// must return the duration from "now" until that instant.
	// Run the parser with `now = 30 seconds before the date` so
	// the expected delta is 30 seconds, easy to assert.
	in := "Wed, 21 Oct 2026 07:28:00 GMT"
	got, err := ParseRetryAfter(in, fixedEarlier)
	if err != nil {
		t.Fatalf("ParseRetryAfter(%q) error = %v, want nil", in, err)
	}
	if got != 30*time.Second {
		t.Errorf("ParseRetryAfter(%q) = %v, want %v", in, got, 30*time.Second)
	}
}

func TestParseRetryAfter_HTTPDate_PastReturnsZero(t *testing.T) {
	// A Retry-After date in the past must yield zero, not a
	// negative duration. This prevents busy-loops on a
	// perpetually overdue retry.
	in := "Wed, 21 Oct 2026 07:28:00 GMT"
	got, err := ParseRetryAfter(in, fixedNow)
	if err != nil {
		t.Fatalf("ParseRetryAfter(%q) error = %v, want nil", in, err)
	}
	if got != 0 {
		t.Errorf("ParseRetryAfter(%q) past = %v, want 0", in, got)
	}
}

func TestParseRetryAfter_Unparseable(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"garbage", "not-a-number"},
		{"negative", "-5"},
		{"unit-suffix", "30s"},
		{"date-bad-format", "2026-10-21 07:28:00"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseRetryAfter(tt.in, fixedNow)
			if err == nil {
				t.Fatalf("ParseRetryAfter(%q) error = nil, want ErrRetryAfterUnparseable", tt.in)
			}
			if !errors.Is(err, ErrRetryAfterUnparseable) {
				t.Errorf("ParseRetryAfter(%q) error = %v, want errors.Is ErrRetryAfterUnparseable", tt.in, err)
			}
			// The concrete RetryAfterError must be unwrappable
			// to ErrRetryAfterUnparseable AND carry the raw
			// value (so a log line names the offending header).
			var rae *RetryAfterError
			if !errors.As(err, &rae) {
				t.Errorf("ParseRetryAfter(%q) error = %v, want errors.As RetryAfterError", tt.in, err)
			}
			if rae != nil && rae.Raw != tt.in {
				t.Errorf("RetryAfterError.Raw = %q, want %q", rae.Raw, tt.in)
			}
		})
	}
}

func TestTooLargeError_Unwrap(t *testing.T) {
	err := &TooLargeError{Limit: 1024, Got: 2048}
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Errorf("TooLargeError does not unwrap to ErrResponseTooLarge")
	}
	if err.Error() == "" {
		t.Errorf("TooLargeError.Error() empty")
	}
	if err.Got != 2048 || err.Limit != 1024 {
		t.Errorf("TooLargeError fields: got %+v", err)
	}
}

func TestStaleEpochError_Unwrap(t *testing.T) {
	err := &StaleEpochError{Issued: 1, Current: 3}
	if !errors.Is(err, ErrStaleEpoch) {
		t.Errorf("StaleEpochError does not unwrap to ErrStaleEpoch")
	}
	if err.Error() == "" {
		t.Errorf("StaleEpochError.Error() empty")
	}
}

func TestClosedError_Unwrap(t *testing.T) {
	err := &ClosedError{CloserEpoch: 5}
	if !errors.Is(err, ErrKernelClosed) {
		t.Errorf("ClosedError does not unwrap to ErrKernelClosed")
	}
	if err.Error() == "" {
		t.Errorf("ClosedError.Error() empty")
	}
}

func TestRetryAfterError_Unwrap(t *testing.T) {
	inner := errors.New("bad")
	err := &RetryAfterError{Raw: "garbage", Cause: inner}
	if !errors.Is(err, ErrRetryAfterUnparseable) {
		t.Errorf("RetryAfterError does not unwrap to ErrRetryAfterUnparseable")
	}
	if err.Error() == "" {
		t.Errorf("RetryAfterError.Error() empty")
	}
}
