# Cassette format and scrubbing policy

This directory holds committed VCR cassettes used by the unit tests
that exercise the policy registry's behavior against recorded
HTTP exchanges.

## File format

Each `.yaml` file is a [go-vcr v4 cassette][go-vcr]. The recorder
writes them through the harness in `internal/tools/cassette`, which
applies the project-wide match tuple and the scrub-after-capture
hook on every interaction.

[go-vcr]: https://github.com/dnaeon/go-vcr

## Naming

Cassettes are named by the test they support. A test
`TestListNotebooks` recording two interactions lives at
`list_notebooks.yaml`. A multi-file suite (e.g. a long lifecycle
test) can split into one cassette per phase; each phase's cassette
must independently round-trip.

## Scrubbing policy

A cassette recorded from a live session contains **full-account
credentials**. The harness's `AfterCaptureHook` rewrites every
credential shape to a fixed sentinel before anything reaches disk;
`internal/tools/scrubhar` is the byte-level redaction primitive that
implements that rewrite.

Two independent layers guard the on-disk files:

1. **The `AfterCaptureHook` in `internal/tools/cassette`** rewrites
   `Cookie:` headers, `SNlM0e`/`FdrFJe` tokens, `at=`/`f.sid`
   query parameters, account emails, and signed download URL params
   to `SCRUBBED` (or `SCRUBBED@example.com` for emails).
2. **`TestNoCredentialInCassettes` in `internal/web/policy`** walks
   this directory on every CI run and fails the build if any
   credential pattern survives. A hook that drifts out of step
   with the test is caught at PR review time, not in a quiet log
   line.

## Recording

```bash
NOTEBOOKLM_VCR_RECORD=1 go test ./internal/web/policy/... -run TestWhatever
```

After recording, the new file lands here with credentials already
scrubbed. Review the diff by hand before committing — every time.

## Re-scrubbing

If a scrub pattern improves after a file is committed (e.g. a new
token shape is added), bulk re-scrub the directory:

```bash
go run ./internal/tools/scrubhar/cmd internal/web/policy/testdata/cassettes/*.yaml
```

The CLI is idempotent: running it on a clean tree is a no-op.

## What never appears in a committed cassette

- `Cookie:` request header values (rewritten to `Cookie: SCRUBBED`)
- `SNlM0e=…`, `FdrFJe=…`, `at=…`, `f.sid=…` tokens
- Account email addresses (rewritten to `SCRUBBED@example.com`)
- `x-goog-algorithm`, `x-goog-credential`, `x-goog-date`,
  `x-goog-expires`, `x-goog-signedheaders`, `x-goog-signature`
  query parameters on signed-URL hosts
- Notebook UUIDs in URL paths (these are scrubbed by the
  ResourceIdCassetteScrubber; see T-P5-9 follow-up for the Go port
  if it lands in a later phase)

If you find a credential pattern that survived into a committed
cassette, do **not** rewrite the file in place and re-commit it;
that only papers over the missing scrub. Fix the
`internal/tools/scrubhar` primitive (or the harness hook that calls
it), then re-record or re-scrub the file with the fixed tool.
