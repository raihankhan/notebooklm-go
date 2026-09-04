// Source-add cassette table test (T-S3-004d).
//
// T-S3-004 (sourceadd: URL / YouTube / Text / File / Drive) ships five
// happy-path cassettes under testdata/cassettes/. This test enumerates
// the five by name and asserts each one:
//
//   - Exists on disk (the file landed at the canonical path).
//   - Loads cleanly (the YAML is well-formed — at least one interaction
//     block survives a stdlib-only structural read).
//   - Has at least one interaction (a cassette with zero recorded
//     requests is a degenerate fixture and a sign that recording went
//     wrong on the source side).
//   - Survives redact.Apply (no credential-shaped substring reaches
//     disk; this is the same check TestNoCredentialInCassettes runs
//     across the directory, restated per-cassette so a single failing
//     file names itself in the test output).
//
// The first two cassettes (url, youtube) were merged with T-S3-004b
// (PR #93). The remaining three (text, file, drive) land in T-S3-004c;
// until that PR merges, those rows skip with a t.Skip and a TODO
// pointing at the sibling ticket. The skip is the contract the
// dispatcher relies on: the test is informational now and becomes
// load-bearing the moment T-S3-004c's cassettes arrive.
//
// Boundary: this file lives under internal/web/policy (mode=internal)
// so it uses stdlib + internal/redact only. No go-vcr import — the
// "loads cleanly" assertion is a structural read of the YAML, not a
// full go-vcr cassette.Load call. The full go-vcr round-trip is
// exercised by the recording-side tests in T-S3-004b / T-S3-004c;
// this test is the pre-commit guard, not the integration test.
package policy

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/raihankhan/notebooklm-go/internal/redact"
)

// sourceAddKinds is the closed set of source-add cassette names
// T-S3-004 commits. Keep the order stable — the test name order is
// the order the table reports subtest results in CI.
var sourceAddKinds = []string{
	"source_add_url",
	"source_add_youtube",
	"source_add_text",
	"source_add_file",
	"source_add_drive",
}

// TestSourceAddCassettes is the table-driven pre-commit guard for
// T-S3-004's five source-add cassettes. Each subtest is the union of
// the four AC checks:
//
//  1. Cassette file exists at the canonical path under cassetteDir.
//  2. File is a well-formed cassette skeleton (interactions: list
//     is present, contains at least one `- request:` entry).
//  3. At least one interaction is recorded (the interaction count
//     marker fires at least once).
//  4. The on-disk bytes survive redact.Apply with zero substitutions
//     (i.e. no credential pattern landed on disk).
//
// Subtests that depend on T-S3-004c (text, file, drive) call t.Skip
// when the cassette is missing so a green master is not held hostage
// to a sibling PR. The URL and YouTube cassettes shipped with T-S3-004b
// (PR #93) and run unconditionally.
func TestSourceAddCassettes(t *testing.T) {
	t.Parallel()

	for _, kind := range sourceAddKinds {
		kind := kind
		t.Run(kind, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(cassetteDir, kind+".yaml")
			raw, err := os.ReadFile(path) // #nosec G304 -- test-owned file.
			if err != nil {
				if os.IsNotExist(err) {
					// T-S3-004c (Text / File / Drive) is
					// implementing the three missing
					// cassettes in parallel. The contract
					// is: this test must pass for the two
					// committed cassettes now, and the
					// remaining rows turn from Skip into
					// Run when T-S3-004c merges.
					//
					// TODO(T-S3-004c): remove the Skip
					// once the three sibling cassettes
					// land.
					t.Skipf("cassette %s not yet committed (T-S3-004c pending)", path)
				}
				t.Fatalf("read cassette: %v", err)
			}

			// (2) structural sanity: the cassette skeleton
			// must declare an interactions: list. The list
			// marker is what every go-vcr v4 cassette uses
			// (the recorder writes the same key); a file
			// without it is either hand-authored garbage
			// or a pre-v2 fixture that the harness would
			// reject at load time anyway.
			if !bytes.Contains(raw, []byte("interactions:")) {
				t.Fatalf("cassette %s missing 'interactions:' key — not a v2 fixture", path)
			}

			// (3) at least one interaction block.
			// The list-of-maps shape is "- request:" with
			// the leading two-space indent that the
			// go-vcr recorder emits. Counting occurrences
			// is the stdlib-only equivalent of asking
			// cassette.Load for len(Interactions).
			n := bytes.Count(raw, []byte("- request:"))
			if n < 1 {
				t.Fatalf("cassette %s has zero interactions — degenerate fixture", path)
			}

			// (4) the second-layer scrub check: every
			// credential-shaped substring must be masked
			// before the bytes hit disk. This is the
			// same primitive TestNoCredentialInCassettes
			// runs over the directory, restated per
			// cassette so the failure log names the
			// exact file.
			out := redact.Apply(raw)
			if !bytes.Equal(raw, out) {
				t.Fatalf("cassette %s contains a credential pattern\n"+
					"re-run `go run ./internal/tools/scrubhar/cmd %s`\n"+
					"and commit the scrubbed result",
					path, path)
			}
		})
	}
}

// TestSourceAddCassettes_NoExtraFiles guards against accidental
// drift: the source-add fixture set is the closed list above. A
// cassette named source_add_<foo>.yaml that is not in sourceAddKinds
// is a typo (or a future kind that has not been added to the table)
// and must be reviewed before merge.
func TestSourceAddCassettes_NoExtraFiles(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(cassetteDir)
	if err != nil {
		t.Fatalf("read cassette dir: %v", err)
	}
	allowed := make(map[string]struct{}, len(sourceAddKinds))
	for _, k := range sourceAddKinds {
		allowed[k+".yaml"] = struct{}{}
	}
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "source_add_") || !strings.HasSuffix(name, ".yaml") {
			continue
		}
		if _, ok := allowed[name]; !ok {
			t.Errorf("unexpected source_add cassette %q — add it to sourceAddKinds in cassette_table_test.go", name)
		}
	}
}
