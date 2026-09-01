# 11 — Testing strategy

Normative source: `../notebooklm-py/docs/development.md` and `tests/vcr_config.py`.

The Python original runs **15,478 tests at 96.6% coverage**. That number is not the goal; the
*shape* of its suite is. Three tiers, hard-separated:

| Tier | Location | Network | Runs in CI |
|---|---|---|---|
| **Unit** | `*_test.go` beside the code | none | always |
| **Cassette** | `internal/.../testdata/cassettes/` | replayed | always |
| **E2E** | `test/e2e/` | live Google | manual + nightly, gated |

## Tier 1 — Unit

Pure encode/decode, no I/O. This is where the protocol correctness lives.

**Golden tables are the primary tool.** For every RPC:

```go
func TestBuildAudioParams(t *testing.T) {
    for _, tc := range []struct {
        name string
        in   AudioOptions
        want string   // exact JSON bytes
    }{
        {
            name: "defaults",
            in:   AudioOptions{NotebookID: "nb1", SourceIDs: []string{"s1"}, Language: "en"},
            want: `[[2,null,null,[1,null,null,null,null,null,null,null,null,null,[1]],[[1,4,8,2,3,6]]],"nb1",[null,null,1,[[["s1"]]],null,null,[null,[null,2,null,[["s1"]],"en",null,1]]]]`,
        },
        // …
    } {
        t.Run(tc.name, func(t *testing.T) {
            got, err := wire.Marshal(params.BuildAudio(tc.in))
            require.NoError(t, err)
            require.JSONEq(t, tc.want, string(got))       // structural
            require.Equal(t, tc.want, string(got))        // AND byte-exact
        })
    }
}
```

Assert **both** `JSONEq` and byte equality. `JSONEq` catches a wrong value; byte equality
catches a wrong escape, a wrong number format, and a reordered element — the failures that
reach the wire as a rejected request.

### Generating the expected values

Do not hand-write golden bytes. Generate them once from the Python original:

```bash
cd ../notebooklm-py && uv run python - <<'PY'
import json
from notebooklm._web.params.artifacts import build_audio_artifact_params
from notebooklm._types.enums import AudioFormat, AudioLength
p = build_audio_artifact_params("nb1", ["s1"], language="en",
        instructions=None, audio_format=None, audio_length=None)
print(json.dumps(p, separators=(",", ":")))
PY
```

Commit a `internal/web/params/testdata/golden.json` mapping case names to expected bytes, and
a `tools/gengolden` script that regenerates it. When a shape legitimately changes, regenerate
rather than editing by hand.

### What else gets unit-tested

- The decoder against every fixture in `internal/web/wire/testdata/responses/`.
- Every row decoder against a captured (scrubbed) response.
- The cookie jar (see the fuzz target below).
- Enum mappings, both directions, including unmapped-code degradation.
- `internal/app/errors.Classify` for every error type.
- The `--json` envelope shapes.
- UTF-16 offset arithmetic.

## Tier 2 — Cassette replay

`gopkg.in/dnaeon/go-vcr.v4`. Record once against live Google, replay forever.

### Match tuple — port exactly

The Python VCR matches on
`["method", "scheme", "host", "port", "path", "rpcids", "freq"]`:

- `rpcids` — the URL query param, so two RPCs to the same path do not collide.
- `freq` — the **decoded `f.req` form body**, so a changed payload misses the cassette
  instead of silently replaying the wrong recording.
- `host` is part of the tuple, so a cassette is pinned to the host it was recorded against.
  A base-URL flip needs re-recording; that is intentional.

```go
func newRecorder(t *testing.T, name string) *recorder.Recorder {
    r, err := recorder.New("testdata/cassettes/" + name)
    require.NoError(t, err)
    r.SetMatcher(func(req *http.Request, i cassette.Request) bool {
        if req.Method != i.Method { return false }
        u, _ := url.Parse(i.URL)
        if req.URL.Scheme != u.Scheme || req.URL.Host != u.Host ||
           req.URL.Path != u.Path { return false }
        if req.URL.Query().Get("rpcids") != u.Query().Get("rpcids") { return false }
        return decodedFReq(req) == decodedFReqFromBody(i.Body)
    })
    r.AddHook(scrubHook, recorder.AfterCaptureHook)   // MANDATORY
    t.Cleanup(func() { require.NoError(t, r.Stop()) })
    return r
}
```

### Scrubbing is mandatory and belt-and-braces

A cassette recorded from a live session contains **full-account credentials**: the `Cookie`
request header, `Set-Cookie` response headers, the `at=` CSRF token and `f.sid` session id,
the account email, real notebook/source ids, and signed download URLs.

Two independent layers, both required:

1. **An `AfterCaptureHook`** that rewrites cookies, tokens, emails, and signed URL query
   parameters to fixed placeholders before anything is written to disk.
2. **A pre-commit guard test** that walks every cassette file and fails on a match against
   the credential regexes. A hook can be forgotten; a test over committed files cannot.

Also scrub in a stable way: replace a real id with a deterministic placeholder derived from
its hash, so re-recording the same flow produces a minimal diff.

### Recording

```bash
NOTEBOOKLM_VCR_MODE=record go test ./internal/web/... -run TestAudioGeneration
```

Recording requires live auth. Record the narrowest possible flow. Review the resulting YAML
by hand before committing — every time.

## Tier 3 — E2E

Real API, real account, real cost. Build-tagged and env-gated so they can never run by
accident:

```go
//go:build e2e

func TestE2E_FullLifecycle(t *testing.T) {
    if os.Getenv("NOTEBOOKLM_E2E") != "1" { t.Skip("set NOTEBOOKLM_E2E=1") }
    // …
}
```

```bash
NOTEBOOKLM_E2E=1 go test -tags=e2e ./test/e2e/... -timeout 60m
```

**The canonical E2E run** (the v1 acceptance gate from doc 01):

login → create notebook → add a URL, a local PDF, and a YouTube link → wait for ready →
ask a question and verify citations resolve → generate audio + quiz + interactive mind map →
poll to completion → download all three → verify file sizes and magic bytes →
share public → add a collaborator → remove them → delete the notebook.

Rules: use a dedicated throwaway Google account · always clean up in `t.Cleanup`, including
on failure · pace calls to respect rate limits · never assert on generated *content*, only on
structure and status.

## Guardrail tests

The Python original's most valuable and least conventional category: tests that guard
architecture rather than behavior. Port all of these.

| Guardrail | Asserts |
|---|---|
| `boundarycheck` | the import matrix in doc AGENTS.md rule 5 |
| encoding/json exclusivity | only `internal/web/wire/json.go` imports `encoding/json` |
| chain order | the middleware sequence, by name |
| idempotency coverage | every `Method` has a registry entry; every `ProbeThenCreate` has a rationale |
| status-label coverage | every `ArtifactStatus`/`SourceStatus`/`DriveSourceStatus` member has a label; a new member fails the test rather than degrading silently |
| CLI group binning | every Cobra command is assigned to a group |
| JSON stdout purity | no `fmt.Print*` reachable from `internal/mcpsrv`; `--json` CLI output parses with a spinner active |
| cassette scrubbing | no credential pattern in any committed cassette |
| parity audit | every Python CLI command/flag and MCP tool exists in Go |
| enum-schema derivation | MCP `status` enums are generated from the label map, not restated |
| wire-contract pinning | enum integers match the values recorded in `../notebooklm-py/docs/android/enums.txt` |
| no-await-before-POST | no synchronization operation between the envelope rebuild and `Kernel.Post` (AST inspection) |
| module size | a soft ratchet warning over ~1200 lines, to catch accretion in composition roots |

## Fuzzing

Four targets, run 5 minutes each in CI and longer nightly:

```go
func FuzzParseChunked(f *testing.F)      // must never panic; error or result only
func FuzzCookieJarRoundTrip(f *testing.F) // write→read must be lossless or an error
func FuzzEscapeAll(f *testing.F)          // must match a reference implementation
func FuzzSafeIndex(f *testing.F)          // arbitrary nesting must never panic
```

The chunked parser and `safeIndex` targets matter most: both consume **server-controlled**
input, and the Python original had to add explicit `RecursionError` handling after a
pathologically deep payload escaped its decode pipeline.

## Race and repetition

Concurrency-sensitive packages run with `-race -count=5` in CI:
`internal/runtime`, `internal/web/transport`, `internal/auth/singleflight`,
`internal/auth/keepalive`.

The single-refresh, epoch-fencing, and keepalive-stampede tests get `-count=20` — they are
the tests most likely to pass once by luck.

## Coverage gates

| Package | Gate | Why |
|---|---|---|
| `internal/web/wire` | **90%** | protocol correctness; untested branches are wire breaks |
| `internal/auth` | **85%** | credential handling; untested branches are security holes |
| `internal/web/transport` | 80% | |
| `internal/app` | 80% | |
| `notebooklm` | 75% | |
| `internal/cli`, `mcpsrv`, `restsrv` | 70% | thin adapters, mostly wiring |
| overall | **80%** | |

A gate is a floor, not a target. The Python original's own experience is the caution worth
carrying: an incomplete extras install measured 84% and passed a 90%-looking run while
hiding ~1,500 skipped tests and a real MCP-adapter defect through eight red CI jobs. In Go,
build tags are the equivalent hazard — **assert the expected test count**, not just the
coverage percentage, so a silently skipped tag cannot masquerade as a pass.

Check the exit status directly rather than through a pipe. Piping `go test` into `tail` or
`grep` reports the *pipeline's* status, which has masked real failures before.
