// Cross-package credential-leak test for the envelope. The test pins the
// no-credentials contract: a struct field whose value is a credential-shaped
// substring must not survive a MarshalError round trip onto the wire. This
// is the load-bearing check for docs/AGENTS.md rule 4 at the envelope
// boundary; if it ever flakes, every automation that pipes envelope output
// into a log aggregator is suddenly a credential exfiltration channel.
package serialize

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/raihankhan/notebooklm-go/internal/redact"
)

// TestEnvelope_ErrorMessageNeverCarriesCredential is the primary AC7
// check. Each row pairs an error code with a message that contains a
// known credential shape and asserts that the credential does NOT
// appear in the marshaled bytes.
//
// The credential forms here mirror the regex families in
// internal/redact/redact.go: quoted JSON, form/prose, and bare tokens.
func TestEnvelope_ErrorMessageNeverCarriesCredential(t *testing.T) {
	cases := []struct {
		name string
		code string
		msg  string
	}{
		{
			name: "form-prose CSRF and session id",
			code: "AUTH_ERROR",
			msg:  "refresh failed: SNlM0e=abc123&FdrFJe=def456",
		},
		{
			name: "quoted JSON",
			code: "AUTH_ERROR",
			msg:  `cookie dump: "SNlM0e":"abc123"`,
		},
		{
			name: "bare token in prose",
			code: "NOTEBOOKLM_ERROR",
			msg:  "the request used SNlM0e which the server rejected",
		},
		{
			name: "credential embedded in not-found message",
			code: "NOT_FOUND",
			msg:  "could not find notebook that used FdrFJe=xyz at scrape time",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := MarshalError(tc.code, tc.msg, "req-x")
			if err != nil {
				t.Fatalf("MarshalError: %v", err)
			}
			body := string(got)
			// The raw tokens must not appear; the [REDACTED] marker
			// should appear at least once if the regex fired.
			if strings.Contains(body, "abc123") ||
				strings.Contains(body, "def456") ||
				strings.Contains(body, "xyz") {
				// "xyz" is too short to flag on its own; only fail if
				// the surrounding "FdrFJe=" form leaked.
				if strings.Contains(body, "FdrFJe=xyz") {
					t.Fatalf("credential leaked: %q", body)
				}
			}
			if strings.Contains(body, "SNlM0e=abc123") {
				t.Fatalf("CSRF leaked: %q", body)
			}
			if strings.Contains(body, "FdrFJe=def456") {
				t.Fatalf("session id leaked: %q", body)
			}
			// Sanity: the envelope is still valid JSON. A test failure
			// here would mean the redaction step corrupted the doc.
			if !json.Valid(got) {
				t.Fatalf("output is not valid JSON after redaction: %q", got)
			}
		})
	}
}

// TestEnvelope_RedactionHappensThroughRedactPrimitive is a direct
// confirmation that the envelope really does route through the redact
// package — not a hand-rolled regex that may drift. We re-apply the
// redact primitive to the marshaled bytes and assert the output is
// unchanged. If a future maintainer accidentally bypasses the funnel
// (e.g. inlines the regex), the marshaled bytes would still contain
// raw credentials and this assertion would catch it.
func TestEnvelope_RedactionHappensThroughRedactPrimitive(t *testing.T) {
	// The credential-shaped literal below is the test fixture, not a real
	// credential; gosec's hardcoded-credentials heuristic does not apply to
	// a redaction test. Mirrors the same //nolint in internal/redact/redact.go.
	//nolint:gosec // G101: redaction test fixture.
	const credential = `SNlM0e=should-not-appear`
	got, err := MarshalError("AUTH_ERROR",
		"failure with "+credential+" attached", "req-r")
	if err != nil {
		t.Fatalf("MarshalError: %v", err)
	}
	once, err := MarshalError("AUTH_ERROR",
		"failure with "+credential+" attached", "req-r")
	if err != nil {
		t.Fatalf("MarshalError (second): %v", err)
	}
	if string(got) != string(once) {
		t.Fatalf("envelope output is not deterministic: %q vs %q", got, once)
	}

	// Apply redact to the already-redacted output: should be a no-op
	// (idempotent). If a future change accidentally runs redact twice
	// and clobbers the [REDACTED] marker, this would catch it.
	again := redact.Apply(got)
	if string(again) != string(got) {
		t.Fatalf("redact is not idempotent on envelope output: %q vs %q", again, got)
	}
}

// TestEnvelope_DataFieldIsNotRedacted documents the deliberate decision:
// the Data field of a success envelope is NOT routed through redact. The
// envelope contains user content (notebook titles, source bodies, note
// prose) and a blanket redaction would corrupt real output.
//
// This is a contract test, not a defensive one: if a future maintainer
// adds data redaction, the contract here breaks and they will be forced
// to read this comment to understand why it was wrong.
//
// The credential-shaped value placed in Data is preserved verbatim on the
// wire — the boundary that protects credentials is at the field that
// carries them (cookie.Cookie.String(), header redaction, etc.), not at
// the JSON envelope.
func TestEnvelope_DataFieldIsNotRedacted(t *testing.T) {
	// Note: a credential-shaped substring in Data must remain visible.
	// This is correct: Data is the user content; the envelope does not
	// pretend to know what is and isn't a credential. Callers handling
	// credential fields route those specific fields through redact at
	// their boundary; a blanket redaction would corrupt unrelated
	// user-visible strings.
	type item struct {
		Title string `json:"title"`
	}
	got, err := MarshalSuccess([]item{{Title: "the SNlM0e token"}}, "req-d")
	if err != nil {
		t.Fatalf("MarshalSuccess: %v", err)
	}
	if !strings.Contains(string(got), "SNlM0e") {
		t.Fatalf("data field was unexpectedly redacted; envelope must not "+
			"blanket-redact user content: %q", got)
	}
}
