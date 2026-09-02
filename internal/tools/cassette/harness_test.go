// Tests for the cassette harness. The match tuple is the load-bearing
// contract — every field the Python VCR matched on must be enforced
// here, in the same order, with the same semantics. A drift is a
// silent replay bug: a test passes by replaying the wrong recording.
//
// Boundary: this test file lives in the cassette package so it can
// call matchAll directly without exporting it for tests. As a tool
// under internal/tools/, cassette is exempt from the layer-boundary
// rules in docs/AGENTS.md rule 5.
package cassette

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"gopkg.in/dnaeon/go-vcr.v4/pkg/cassette"
)

// makeRequest is a tiny constructor that builds an *http.Request with
// the fields the match tuple inspects. Body is set via the request
// line so a future change to the match tuple does not silently lose
// coverage.
func makeRequest(method, rawURL, body string) *http.Request {
	u, _ := url.Parse(rawURL)
	var bodyReader io.ReadCloser
	if body != "" {
		bodyReader = io.NopCloser(strings.NewReader(body))
	}
	return &http.Request{
		Method: method,
		URL:    u,
		Body:   bodyReader,
		Header: http.Header{},
	}
}

// makeCassetteRequest is the matching constructor for cassette.Request.
// Fields default to sensible values so each test only fills in what
// matters.
func makeCassetteRequest(method, rawURL, body string) cassette.Request {
	return cassette.Request{
		Method: method,
		URL:    rawURL,
		Body:   body,
	}
}

// TestMatchTuplePinned is the contract test for the seven-field match
// tuple. Each subtest mutates exactly one field from a "match" baseline
// and asserts the matcher now returns false. A green subtest proves the
// matcher enforces that field; a red subtest pins the field the harness
// forgot to wire.
func TestMatchTuplePinned(t *testing.T) {
	t.Parallel()

	const baseline = "https://notebooklm.google.com/_/LabsTailwindUi/data/batchexecute?rpcids=CCqFvf&f.sid=SCRUBBED&at=SCRUBBED"
	const baselineBody = "f.req=%5B%5B%22CCqFvf%22%2C%5B%22nb1%22%5D%5D%5D"

	baseReq := makeRequest("POST", baseline, baselineBody)
	baseCassette := makeCassetteRequest("POST", baseline, baselineBody)
	baseBody := baselineBody

	if !matchAll(baseReq, baseCassette) {
		t.Fatalf("baseline must match itself; matcher is broken")
	}

	tests := []struct {
		name    string
		mutate  func(*http.Request, *cassette.Request)
		explain string
	}{
		{
			name: "method_mismatch",
			mutate: func(r *http.Request, c *cassette.Request) {
				r.Method = "GET"
			},
			explain: "a GET request must not replay a POST cassette",
		},
		{
			name: "scheme_mismatch",
			mutate: func(r *http.Request, c *cassette.Request) {
				r.URL.Scheme = "http"
			},
			explain: "an http request must not replay an https cassette",
		},
		{
			name: "host_mismatch",
			mutate: func(r *http.Request, c *cassette.Request) {
				r.URL.Host = "notebook.google.com"
			},
			explain: "a request to the secondary personal host must not replay a primary host cassette",
		},
		{
			name: "port_mismatch",
			mutate: func(r *http.Request, c *cassette.Request) {
				// Mutate the request URL to an explicit
				// non-default port. The cassette URL stays at
				// the implicit :443. Note: Host comparison is
				// before Port comparison in the matcher, so
				// changing the host part is the only way to
				// reach the Port check. We replace the entire
				// host (including the port) so the matcher's
				// Host check passes (Host still equals Host
				// after the replacement below is applied),
				// but Port() now returns "8443" instead of "".
				//
				// Concretely: a port-only change has to alter
				// both the request URL and the parsed Host
				// so that the parsed Host string is identical
				// but the parsed Port() differs. The easiest
				// way is to give both sides the same explicit
				// port that the OTHER side lacks.
				//
				// We add :8443 to the request URL's Host
				// (making reqURL.Host = "host:8443") and
				// leave the cassette at the implicit :443.
				r.URL.Host = r.URL.Host + ":8443"
			},
			explain: "a request with an explicit non-default port must not replay a default-port cassette",
		},
		{
			name: "path_mismatch",
			mutate: func(r *http.Request, c *cassette.Request) {
				r.URL.Path = "/_/LabsTailwindUi/data/different"
			},
			explain: "a request to a different path must not replay this cassette",
		},
		{
			name: "rpcids_mismatch",
			mutate: func(r *http.Request, c *cassette.Request) {
				q := r.URL.Query()
				q.Set("rpcids", "wXbhsf")
				r.URL.RawQuery = q.Encode()
			},
			explain: "two POSTs to the same path with different rpcids must not collide",
		},
		{
			name: "freq_mismatch",
			mutate: func(r *http.Request, c *cassette.Request) {
				c.Body = "f.req=%5B%5B%22izAoDd%22%2C%5B%22nb2%22%5D%5D%5D"
			},
			explain: "a different f.req body must not replay the original cassette",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Deep-copy the baseline request so each subtest gets
			// its own Body reader. Sharing the io.ReadCloser
			// between parallel subtests triggers a race detector
			// hit when matchAll reads the body.
			r := makeRequest(baseReq.Method, baseReq.URL.String(), baseBody)
			c := baseCassette
			tc.mutate(r, &c)

			if matchAll(r, c) {
				t.Errorf("matcher must reject %s, but it accepted it",
					tc.explain)
			}
		})
	}
}

// TestMatchTuple_BothEmptyFreq verifies that two requests without
// f.req bodies (a GET-style interaction) match on every field except
// freq. The matcher must NOT reject "no body on either side" as a
// mismatch — that would force every GET cassette to ship with an
// empty f.req= token, which is not what VCR emits.
func TestMatchTuple_BothEmptyFreq(t *testing.T) {
	t.Parallel()

	r := makeRequest("GET",
		"https://notebooklm.google.com/health?rpcids=ping",
		"")
	c := makeCassetteRequest("GET",
		"https://notebooklm.google.com/health?rpcids=ping",
		"")

	if !matchAll(r, c) {
		t.Fatalf("matcher must accept two empty-body GETs")
	}
}

// TestMatchTuple_OneEmptyFreqMismatch verifies that a body on exactly
// one side is a mismatch — the two requests are structurally
// different, even when every URL field agrees.
func TestMatchTuple_OneEmptyFreqMismatch(t *testing.T) {
	t.Parallel()

	r := makeRequest("POST",
		"https://notebooklm.google.com/_/LabsTailwindUi/data/batchexecute?rpcids=CCqFvf",
		"f.req=hello")
	c := makeCassetteRequest("POST",
		"https://notebooklm.google.com/_/LabsTailwindUi/data/batchexecute?rpcids=CCqFvf",
		"")

	if matchAll(r, c) {
		t.Fatalf("matcher must reject an empty-body cassette for a body-bearing request")
	}
}
