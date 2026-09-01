# AGENTS.md

`notebooklm-go` — a Go port of [`notebooklm-py`](https://github.com/teng-lin/notebooklm-py):
an unofficial client for Google Gemini Notebook (NotebookLM) over Google's internal
`batchexecute` RPC protocol. Ships a library, a Cobra CLI, an MCP server, and a REST server.

## Read this first

**→ [`docs/AGENTS.md`](docs/AGENTS.md)** — the seven rules of engagement. Read it before your
first edit. It is short.

**→ [`docs/10-implementation-plan.md`](docs/10-implementation-plan.md)** — the build order.
13 phases with acceptance criteria. Work a phase to completion, tests included, before
starting the next.

**→ [`docs/README.md`](docs/README.md)** — the full doc index and which doc answers which
question.

## The two things most likely to trip you up

1. **The Python source at `../notebooklm-py/src/notebooklm/` is normative** for every wire
   shape, enum value, and default. Never guess a positional payload — port it index for index
   and cite the Python symbol in a comment. If you cannot find the original, stop and ask.

2. **Go's `encoding/json` is not Python-compatible.** It HTML-escapes `<`, `>`, `&`, appends a
   newline from `Encoder`, and decodes numbers to `float64`. All three break this protocol.
   Only `internal/web/wire/json.go` may import `encoding/json`; everything else goes through
   `wire.Marshal` / `wire.Unmarshal`.

## Commands

```bash
make build      # all three binaries into ./bin
make check      # fmt, vet, golangci-lint, boundarycheck, test — must pass before a phase is done
make test       # unit + cassette tests
make test-e2e   # live tests; requires auth + NOTEBOOKLM_E2E=1
make cover      # coverage report
```

## Non-negotiables

- Credentials never reach a log, an error, a `%v`, or a committed cassette.
  See `docs/AGENTS.md` rule 4 and `docs/05-auth.md`.
- Every mutating RPC declares its retry safety in `internal/web/policy`. An unregistered
  method fails a startup assertion — a `batchexecute` mutation replayed after a lost response
  duplicates the write.
- Destructive operations confirm. `--json` stdout stays byte-clean; narration goes to stderr.
- Import boundaries are lint-enforced by `internal/tools/boundarycheck`, not merely
  documented.
