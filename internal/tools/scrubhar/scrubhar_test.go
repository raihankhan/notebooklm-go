// Tests for the scrubhar library's byte-level redaction logic. The
// CLI driver (processFile + main) is exercised by tests under
// ./cmd against a tmpfile containing embedded cookie + token shapes.
//
// Boundary: the test file lives inside the scrubhar package so it can
// call unexported helpers directly without an export-everything-for-test
// surface. As a tool under internal/tools/, scrubhar is exempt from the
// layer-boundary rules in docs/AGENTS.md rule 5.
package scrubhar

import (
	"bytes"
	"os"
	"testing"
)

// TestScrubHarIdempotent asserts that running the rewrite twice produces
// a byte-identical output the second time. This is the load-bearing
// property that makes the tool safe to wire into `git add`-time and
// CI without "the test environment is dirty after every commit" pain.
//
// We exercise the property by running ScrubBytes twice on the input
// fixture and asserting equality. The CLI driver wraps the same
// helper, so a green TestScrubHarIdempotent is sufficient evidence.
func TestScrubHarIdempotent(t *testing.T) {
	t.Parallel()

	once := ScrubBytes(loadedFixture(t))
	twice := ScrubBytes(once)
	if !bytes.Equal(once, twice) {
		t.Fatalf("scrubhar is not idempotent\nfirst:\n%s\n\nsecond:\n%s",
			once, twice)
	}
}

// TestScrubHarRemovesAllCredentials is the contract test: every known
// credential pattern in the fixture must be replaced by the SCRUBBED
// sentinel. The fixture is committed to disk so a reviewer can eyeball
// the input the test asserts on.
func TestScrubHarRemovesAllCredentials(t *testing.T) {
	t.Parallel()

	out := ScrubBytes(loadedFixture(t))

	forbidden := []string{
		"AEC=AVs14eFakeCookieValue",
		"SNlM0e=R2hhY2t5VG9rZW5WYWx1ZQ",
		"FdrFJe=RmFrZVNlc3Npb25JZA",
		"at=SCRUBBEDME_CSRF_TOKEN",
		"f.sid=FAKE_SESSION_ID",
		"user@example.com",
		"x-goog-signature=fakeSignatureValue123",
	}
	for _, f := range forbidden {
		if bytes.Contains(out, []byte(f)) {
			t.Errorf("ScrubBytes left credential on the wire: %q\nfull output:\n%s",
				f, out)
		}
	}

	if !bytes.Contains(out, []byte(Sentinel)) {
		t.Errorf("ScrubBytes did not emit the SCRUBBED sentinel; full output:\n%s", out)
	}

	if !bytes.Contains(out, []byte(PlaceholderEmail)) {
		t.Errorf("ScrubBytes did not emit %q; full output:\n%s",
			PlaceholderEmail, out)
	}
}

// TestScrubQueryValue_Idempotent asserts that running ScrubQueryValue
// twice on the same URL produces a byte-identical result. The signed-
// URL allowlist is the only externally visible policy here, and a
// regression on idempotence would clobber committed cassettes on every
// `git add`.
func TestScrubQueryValue_Idempotent(t *testing.T) {
	t.Parallel()

	const url = "https://lh3.googleusercontent.com/a/AVs14eFakeToken?x-goog-algorithm=GOOG4-RSA-SHA256&x-goog-signature=fakeSigValue"
	once, _ := ScrubQueryValue(url)
	twice, _ := ScrubQueryValue(once)
	if once != twice {
		t.Fatalf("ScrubQueryValue is not idempotent\nonce:   %s\ntwice:  %s",
			once, twice)
	}
}

// TestScrubQueryValue_NonSignedHostNoOp asserts that non-signed hosts
// (e.g. notebooklm.google.com) pass through unchanged so we do not
// accidentally rewrite query params that the caller actually cares
// about.
func TestScrubQueryValue_NonSignedHostNoOp(t *testing.T) {
	t.Parallel()

	const url = "https://notebooklm.google.com/_/LabsTailwindUi/data/batchexecute?rpcids=CCqFvf&f.sid=SCRUBBED"
	out, changed := ScrubQueryValue(url)
	if changed {
		t.Errorf("ScrubQueryValue changed a non-signed URL: %s -> %s", url, out)
	}
	if out != url {
		t.Errorf("ScrubQueryValue rewrote a non-signed URL: %s -> %s", url, out)
	}
}

// loadedFixture reads the testdata fixture (a YAML-ish blob containing
// every credential shape the scrubber is supposed to redact) into a
// byte slice. The file is committed to the repo so a reviewer can
// eyeball what the contract test asserts on.
func loadedFixture(t *testing.T) []byte {
	t.Helper()
	const path = "testdata/fixture.yaml"
	b, err := os.ReadFile(path) // #nosec G304 -- committed fixture path.
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return b
}
