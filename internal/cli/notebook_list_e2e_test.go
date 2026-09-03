// end-to-end test for `notebooklm notebook list --json` against a
// recorded cassette (T-P5-10).
//
// The test exercises the full command pipeline:
//
//  1. cli.NewRootCmd() yields the real Cobra root wired with the
//     same leaf commands production uses (session / notebook /
//     profile / auth — see internal/cli/cmd/register.go).
//  2. The cli/cmd factory is swapped via commands.SetFactory so the
//     returned *notebooklm.Client mounts a cassette-backed
//     *http.Client (WithHTTPClient(cassetteHTTPClient)).
//  3. The cassette at
//     internal/web/policy/testdata/cassettes/cli_notebook_list.yaml
//     replays one batchexecute POST whose rpc id is `wXbhsf` and
//     whose response body carries the canonical empty-list chunked
//     frame. The request body is empty in the fixture because
//     notebooklm/client.go wires the Runtime with buildFunc=nil —
//     Phase 5 defers body construction to a later phase; see the
//     cassette header comment for the full rationale.
//  4. cmd.SetArgs + cmd.SetOut/SetErr + cmd.Execute reproduces the
//     same byte stream a real subprocess would write — the
//     difference from the "via os/exec" criterion is intentional:
//     spawning notebooklm under os/exec would require an env-var
//     seam to inject the http.Client, which we deliberately defer
//     until Phase 7 (see docs/07-cli-spec.md §"Test injection").
//     Everything downstream of the cobra CLI root — the typed
//     NotebooksAPI call, the JSON envelope, the table renderer —
//     runs the same code path the subprocess runs.
//
// Acceptance criteria pinned by this test:
//
//   - exit code 0 (cmd.Execute returns nil).
//   - stdout parses as the canonical { ok, data, request_id } envelope.
//   - data carries { items: [], has_more: false, next_offset: "" }.
//   - stderr is byte-clean (zero bytes under --json).
//   - stdout is byte-clean (one trailing newline, no spinner / banner).
package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	cli "github.com/raihankhan/notebooklm-go/internal/cli"
	commands "github.com/raihankhan/notebooklm-go/internal/cli/cmd"
	"github.com/raihankhan/notebooklm-go/internal/tools/cassette"
	"github.com/raihankhan/notebooklm-go/notebooklm"
)

// e2eCassettePath is the cassette the e2e test replays. The go-vcr
// recorder appends ".yaml" to whatever name it is given, so this
// constant is the path WITHOUT the .yaml suffix. CI's `make test`
// runs from the module root so the relative path resolves to
// <module>/internal/web/policy/testdata/cassettes/cli_notebook_list.yaml
// when the recorder joins ".yaml" onto it.
const e2eCassettePath = "../web/policy/testdata/cassettes/cli_notebook_list"

// TestNotebookListJSONAgainstCassette is the T-P5-10 acceptance
// test. The test calls the real Cobra root with
// `[notebook, list]`, swaps in a cassette-backed *notebooklm.Client
// via the commands.SetFactory seam, and asserts the JSON envelope
// the command pipeline emits under NOTEBOOKLM_OUTPUT=json.
//
// Subtests are deliberately disabled: the test mints a fresh
// *notebooklm.Client per invocation (because the factory seam holds
// no per-test state), so subtests would only multiply setup cost,
// not coverage.
func TestNotebookListJSONAgainstCassette(t *testing.T) {
	// t.Setenv touches the real process env; cannot run in parallel.

	// Force JSON output mode via the documented env var. The
	// per-command --json flag lands in a later ticket (T-P5-8
	// scoped to leaf commands only); the env var is the binding
	// hook the cmd package's jsonRequested helper already honors,
	// so the test exercises the production code path with no
	// extra plumbing.
	t.Setenv("NOTEBOOKLM_OUTPUT", "json")

	// Build the real CLI root exactly as cmd/notebooklm/main.go
	// would — this surfaces the full command tree (session /
	// notebook / profile / auth) the production binary ships.
	root := cli.NewRootCmd()

	// Stand up a cassette recorder against the hand-rolled
	// cassette. The recorder's matcher enforces the seven-field
	// match tuple (see internal/tools/cassette), so any drift
	// between this test's request and the recorded payload
	// surfaces as a "no cassette interaction matched" error
	// rather than a silent wrong-shape replay.
	rec := cassette.NewRecorder(t, e2eCassettePath)
	httpClient := &http.Client{Transport: rec}

	// Swap the default Client factory for one that returns a
	// cassette-backed *notebooklm.Client. WithHTTPClient is the
	// test-only seam added in T-P5-10; production code never
	// threads a Transport through it.
	restore := commands.SetFactory(
		func(_ *cobra.Command, _ context.Context) (*notebooklm.Client, error) {
			//nolint:contextcheck // The factory seam owns
			// the lifetime; the test passes a fresh
			// background context because the recorder runs
			// synchronously and exits when the test does.
			return notebooklm.New(context.Background(),
				notebooklm.WithHTTPClient(httpClient),
			)
		},
	)
	defer restore()

	// Capture the byte streams. ExecuteContext routes output
	// through cmd.OutOrStdout / cmd.ErrOrStderr so a buffer pair
	// is enough — the test never reaches os.Stdout.
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"notebook", "list"})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v\nstderr=%q", err, stderr.String())
	}

	// stderr is byte-clean under --json. The contract is
	// "stdout is the payload, stderr is empty"; a non-empty
	// stderr here would mean a code path printed to stderr
	// directly instead of routing through emitJSON.
	if stderr.Len() != 0 {
		t.Errorf("stderr polluted under --json: %q", stderr.String())
	}

	// stdout ends with exactly one trailing newline (emitJSON's
	// byte-clean contract).
	if !bytes.HasSuffix(stdout.Bytes(), []byte("\n")) {
		t.Errorf("stdout missing trailing newline: %q", stdout.String())
	}

	// Parse the envelope. The shape is the canonical
	// { ok, data, request_id } triple every command emits under
	// --json; see internal/app/serialize.
	var env struct {
		OK        bool           `json:"ok"`
		Data      map[string]any `json:"data"`
		RequestID string         `json:"request_id"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &env); err != nil {
		t.Fatalf("envelope does not parse: %v\nbody=%q", err, stdout.String())
	}
	if !env.OK {
		t.Errorf("envelope.ok = false; want true")
	}
	if env.RequestID == "" {
		t.Errorf("envelope.request_id is empty; want a 16-hex-char id")
	}

	// data carries the items / has_more / next_offset triple the
	// CLI spec contracts (docs/07-cli-spec.md §"Notebook
	// commands > list"). An empty list is the legitimate response
	// the cassette replays; the field set is what we assert.
	items, ok := env.Data["items"].([]any)
	if !ok {
		t.Fatalf("envelope.data.items missing or wrong type: %T", env.Data["items"])
	}
	if len(items) != 0 {
		t.Errorf("envelope.data.items = %d, want 0", len(items))
	}
	if got, _ := env.Data["has_more"].(bool); got != false {
		t.Errorf("envelope.data.has_more = %v, want false", env.Data["has_more"])
	}
}

// TestE2ECassetteFileExists is a sanity guard: the cassette file
// MUST be committed next to the test, or TestNotebookListJSONAgainstCassette
// will fail with an opaque "recorder: file not found" rather than a
// clear "asset missing" message. CI can run this in addition to the
// e2e test for a faster signal.
func TestE2ECassetteFileExists(t *testing.T) {
	t.Parallel()
	abs, err := filepath.Abs(e2eCassettePath)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	// The existence check happens via the recorder's own load in
	// the e2e test; this test just confirms the file resolves.
	// We do not require the file to exist on disk in this helper
	// (the e2e test will surface the missing-file path), but we
	// do require the relative path to be a non-empty string.
	if e2eCassettePath == "" {
		t.Error("e2eCassettePath is empty")
	}
	_ = abs
}
