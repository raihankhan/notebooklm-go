// Package cassette is the Go port of the Python VCR harness used to
// record and replay batchexecute RPC interactions in CI. The match
// tuple is ported verbatim from tests/vcr_config.py in notebooklm-py:
//
//	["method", "scheme", "host", "port", "path", "rpcids", "freq"]
//
// where "rpcids" is the URL query parameter and "freq" is the decoded
// form-body f.req value. Together they let a single cassette carry
// multiple RPCs to the same /batchexecute path without false matches.
//
// The scrubber is wired via an AfterCaptureHook that runs every
// recorded interaction through the scrubhar CLI's redaction primitive
// before anything reaches disk. The same primitive is re-applied by a
// pre-commit guard test (TestNoCredentialInCassettes in
// internal/web/policy) so a hook that drifts out of step with the
// guard is caught at PR review time.
//
// Boundary: this package lives under internal/tools/ and is therefore
// exempt from the import-boundary rules in docs/AGENTS.md rule 5.
// It imports gopkg.in/dnaeon/go-vcr.v4 freely.
package cassette

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/raihankhan/notebooklm-go/internal/tools/scrubhar"
	"gopkg.in/dnaeon/go-vcr.v4/pkg/cassette"
	"gopkg.in/dnaeon/go-vcr.v4/pkg/recorder"
)

// NewRecorder constructs a go-vcr recorder pinned to the named cassette
// file, with the project's full match tuple and the scrub-after-capture
// hook. The returned recorder must be stopped with r.Stop(); the
// helper wires t.Cleanup for you.
//
// Cassette file layout: place each fixture under
// internal/web/policy/testdata/cassettes/<name>.yaml so the
// pre-commit guard walks it. Tests in other packages should pass the
// full path so they do not silently miss the convention:
//
//	rec := cassette.NewRecorder(t,
//		"../../web/policy/testdata/cassettes/my_test.yaml")
//
// To record instead of replay, set the env var
// NOTEBOOKLM_VCR_RECORD=1; the recorder still uses the same matcher
// and hook so a recorded file round-trips correctly through scrub.
//
// Every call logs the resolved cassette path via t.Logf so the test
// output shows exactly which fixture the recorder opened. The Sprint 2
// retro surfaced a "peek test" that passed because its relative path
// silently landed in a sibling module of the project root and the
// go-vcr default ModeRecordOnce auto-recorded against the live Google
// server. The Logf line + the existence assertion below are the
// regression guards that catch the same shape next time.
//
// The existence assertion only fires in replay mode (when the
// NOTEBOOKLM_VCR_RECORD env var is unset) so a CI run that does not
// record cassettes still gets a clear "asset missing" failure rather
// than an opaque "no cassette interaction matched" error from go-vcr.
func NewRecorder(t *testing.T, name string) *recorder.Recorder {
	t.Helper()

	path := resolveCassettePath(t, name)

	// Always log the resolved path the recorder will open. This is
	// the regression guard the Sprint 2 retro called out: a relative
	// path can silently land in a sibling module of the project
	// root, and the Logf line is what surfaces that case in the
	// test output rather than letting it pass.
	t.Logf("cassette path: %s", path)

	// In replay mode (NOTEBOOKLM_VCR_RECORD unset), assert the
	// cassette actually exists on disk. Without this check, a
	// relative path that resolves to a non-existent file falls
	// back to go-vcr's ModeRecordOnce which silently hits the live
	// Google server — the exact Sprint 2 peek-test failure mode.
	if os.Getenv("NOTEBOOKLM_VCR_RECORD") == "" {
		assertCassetteExists(t, path, name)
	}

	// matcher returns true iff req is "the same interaction" as the
	// recorded i. The match tuple is ported verbatim from
	// notebooklm-py's tests/vcr_config.py:
	//   ["method", "scheme", "host", "port", "path", "rpcids", "freq"]
	// where "rpcids" is the URL query parameter and "freq" is the
	// decoded form-body f.req value.
	matcher := recorder.WithMatcher(func(req *http.Request, i cassette.Request) bool {
		return matchAll(req, i)
	})

	// AfterCaptureHook: every recorded interaction runs through the
	// scrubhar redaction primitive so the on-disk file is safe to
	// commit. The same primitive is re-applied by the pre-commit
	// guard (TestNoCredentialInCassettes) — a hook drift fails at
	// PR review time, not in a quiet log line.
	hook := recorder.WithHook(scrubHook, recorder.AfterCaptureHook)

	r, err := recorder.New(path, matcher, hook)
	if err != nil {
		t.Fatalf("cassette.NewRecorder(%q): %v", name, err)
	}

	t.Cleanup(func() {
		if err := r.Stop(); err != nil {
			t.Errorf("recorder.Stop: %v", err)
		}
	})
	return r
}

// matchAll implements the seven-field match tuple. Each check is
// independent so a failing assertion prints the exact field that
// disagreed.
//
// The "freq" field is the URL-decoded f.req form-body. When the
// recorded body is empty (a GET-style interaction) the matcher falls
// back to a structural "no body on either side" check; when exactly
// one side has a body the request does not match (structurally
// different).
func matchAll(req *http.Request, i cassette.Request) bool {
	// Method
	if req.Method != i.Method {
		return false
	}
	// Scheme / host / port / path. The recorded URL is stored as a
	// fully-qualified string; we re-parse it on each call so a
	// future change to the recording format does not silently
	// regress matching.
	cassetteURL, cassetteErr := url.Parse(i.URL)
	if cassetteErr != nil {
		// If the URL is malformed we cannot do better than the
		// default matcher; fail closed.
		return false
	}
	reqURL := req.URL
	if reqURL == nil {
		return false
	}
	if reqURL.Scheme != cassetteURL.Scheme {
		return false
	}
	// Hostname (without port). The next check compares ports
	// separately so the Python match tuple's "port" field is
	// exercised even though Go's Host field subsumes port in the
	// string representation.
	if reqURL.Hostname() != cassetteURL.Hostname() {
		return false
	}
	if reqURL.Port() != cassetteURL.Port() {
		return false
	}
	if reqURL.Path != cassetteURL.Path {
		return false
	}
	// rpcids
	if reqURL.Query().Get("rpcids") != cassetteURL.Query().Get("rpcids") {
		return false
	}
	// freq — the URL-decoded f.req form body. We must read req.Body
	// then restore it so the downstream transport can read it too.
	var reqBody string
	if req.Body != nil {
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(req.Body); err != nil {
			return false
		}
		req.Body = io.NopCloser(bytes.NewReader(buf.Bytes()))
		reqBody = buf.String()
	}
	if !matchFreq(reqBody, i.Body) {
		return false
	}
	return true
}

// matchFreq compares two request bodies by their f.req form value.
//
//   - Both bodies missing the field: match.
//   - Exactly one body has f.req: mismatch (structurally different).
//   - Both bodies have f.req: the URL-decoded values must be equal.
//     A URL-encoded body and a URL-decoded body that decode to the
//     same payload still match — the matcher is the "are these the
//     same logical request" question, not the byte-level question.
func matchFreq(reqBody, cassetteBody string) bool {
	reqFReq := extractFReq(reqBody)
	cassetteFReq := extractFReq(cassetteBody)
	if reqFReq == "" && cassetteFReq == "" {
		return true
	}
	if reqFReq == "" || cassetteFReq == "" {
		return false
	}
	return reqFReq == cassetteFReq
}

// extractFReq extracts the URL-decoded value of the first `f.req=`
// form parameter from a body string. Returns "" if the body has no
// f.req at all. Bodies are decoded using net/url QueryUnescape so a
// literal `f.req=%5B%5B%22foo%22%5D%5D` and a JSON-encoded
// `f.req=[["foo"]]` compare equal.
func extractFReq(body string) string {
	if body == "" {
		return ""
	}
	// The body is `key=value&key=value`. Split on '&' and scan for
	// `f.req=`. We tolerate either raw or URL-encoded values.
	for _, pair := range strings.Split(body, "&") {
		eq := strings.IndexByte(pair, '=')
		if eq < 0 {
			continue
		}
		k := pair[:eq]
		v := pair[eq+1:]
		if k == "f.req" {
			decoded, err := url.QueryUnescape(v)
			if err != nil {
				// Fall back to the raw value; the
				// matcher still distinguishes a mismatch.
				return v
			}
			return decoded
		}
	}
	return ""
}

// scrubHook is the AfterCaptureHook. It rewrites every credential-
// shaped substring in the interaction (request URL, headers, body,
// response headers, body) so the on-disk cassette is safe to commit.
//
// The hook is intentionally routed through the same scrubhar package
// the CLI exposes: the on-disk driver, the in-memory driver, and the
// pre-commit guard all share a single redaction primitive.
func scrubHook(i *cassette.Interaction) error {
	if i == nil {
		return nil
	}
	// Apply scrubhar.ScrubBytes (the canonical redaction primitive
	// for cassettes) to every byte field. The single primitive
	// handles every credential shape — Cookie: line, SNlM0e /
	// FdrFJe, at=, f.sid, signed URL params, email — so the hook is
	// one consistent pass over the same code path the CLI runs.
	i.Request.URL = string(scrubhar.ScrubBytes([]byte(i.Request.URL)))
	i.Request.Body = string(scrubhar.ScrubBytes([]byte(i.Request.Body)))
	if i.Request.Headers != nil {
		i.Request.Headers = scrubHeaderMap(i.Request.Headers)
	}
	i.Response.Body = string(scrubhar.ScrubBytes([]byte(i.Response.Body)))
	if i.Response.Headers != nil {
		i.Response.Headers = scrubHeaderMap(i.Response.Headers)
	}
	// Signed URL params on the response Location header are scrubbed
	// by scrubhar.ScrubQueryValue — apply it via the package's
	// exported helper to keep the allowlist in one file.
	if loc := i.Response.Headers.Get("Location"); loc != "" {
		if rewritten, ok := scrubhar.ScrubQueryValue(loc); ok {
			i.Response.Headers.Set("Location", rewritten)
		}
	}
	return nil
}

// scrubHeaderMap rewrites every header value through scrubhar.ScrubBytes
// so Cookie: / Authorization: / Set-Cookie: lines get scrubbed by the
// header-line regexes. Returns a new map; the recorder's hook contract
// lets us mutate the interaction in place but every other caller in
// this file works on a fresh value so the in-place semantics are only
// used by the recorder path itself.
func scrubHeaderMap(h map[string][]string) map[string][]string {
	out := make(map[string][]string, len(h))
	for k, vs := range h {
		nvs := make([]string, len(vs))
		for i, v := range vs {
			nvs[i] = string(scrubhar.ScrubBytes([]byte(v)))
		}
		out[k] = nvs
	}
	return out
}

// resolveCassettePath turns the caller's cassette name into an
// absolute path. Relative paths are taken from the test's working
// directory (the package the test lives in), so the convention is:
//
//	internal/web/policy/testdata/cassettes/<name>.yaml
//
// Tests outside that package pass the relative path up to that file
// (e.g. `../../web/policy/testdata/cassettes/my_test.yaml`).
//
// If name is already absolute, it is used verbatim.
func resolveCassettePath(t *testing.T, name string) string {
	t.Helper()
	if filepath.IsAbs(name) {
		return name
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("cassette: getwd: %v", err)
	}
	return filepath.Join(wd, name)
}

// assertCassetteExists is the regression guard the Sprint 2 retro
// called out: a relative path that resolves to a non-existent file
// used to silently fall back to go-vcr's ModeRecordOnce, which hits
// the live Google server and records a brand-new cassette against
// the real RPC. The test would then pass for the wrong reason.
//
// The helper is split out from NewRecorder so tests can invoke it
// directly without dealing with the Fatalf side-effect of the
// recorder construction path.
//
// go-vcr appends ".yaml" to whatever cassette name it is given
// (see cassette.New in gopkg.in/dnaeon/go-vcr.v4/pkg/cassette), so
// the on-disk file is "<name>.yaml". The helper mirrors that
// behavior: if name already ends in .yaml, stat it as-is;
// otherwise stat "<name>.yaml".
func assertCassetteExists(t *testing.T, path, name string) {
	t.Helper()
	diskPath := path
	if !strings.HasSuffix(strings.ToLower(name), ".yaml") {
		diskPath = path + ".yaml"
	}
	if _, err := os.Stat(diskPath); err != nil {
		t.Fatalf("cassette fixture missing at resolved path %q (name=%q): %v",
			diskPath, name, err)
	}
}
