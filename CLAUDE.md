# CLAUDE.md

Guidance for Claude Code in `notebooklm-go`.

## Project

A Go port of `notebooklm-py` (v0.8.1, ~137k lines Python) — an unofficial client for Google
Gemini Notebook (NotebookLM) driving Google's internal `batchexecute` RPC protocol. One
module ships a library, a Cobra CLI (`notebooklm`), an MCP server (`notebooklm-mcp`), and a
REST server (`notebooklm-server`).

Module path: `github.com/raihankhan/notebooklm-go`

**Critical constraint:** the obfuscated RPC method IDs in `internal/web/wire/methods.go` and
the positional array payloads in `internal/web/params/` are undocumented and can break
whenever Google changes them — the #1 breakage class. The odd shapes *are* the contract.

## Start here

| Doc | What it answers |
|---|---|
| [`docs/AGENTS.md`](docs/AGENTS.md) | **Read first.** Seven rules of engagement. |
| [`docs/10-implementation-plan.md`](docs/10-implementation-plan.md) | The build order — 13 phases with acceptance criteria. |
| [`docs/README.md`](docs/README.md) | Full doc index. |
| [`docs/02-architecture.md`](docs/02-architecture.md) | Package layout, concurrency model, dependency choices. |
| [`docs/03-protocol-batchexecute.md`](docs/03-protocol-batchexecute.md) | Wire format, method table, decode rules. |
| [`docs/04-rpc-payloads.md`](docs/04-rpc-payloads.md) | Positional payloads, per RPC. |
| [`docs/05-auth.md`](docs/05-auth.md) | Cookies, storage format, the five-rung refresh ladder, master token. |
| [`docs/14-risk-register.md`](docs/14-risk-register.md) | What is most likely to go wrong, and where it is mitigated. |

## Development commands

```bash
make build            # ./bin/{notebooklm,notebooklm-mcp,notebooklm-server}
make check            # fmt + vet + golangci-lint + boundarycheck + test
make test             # unit + cassette
make test-e2e         # live; needs auth + NOTEBOOKLM_E2E=1
make cover            # gates: 90% internal/web/wire, 85% internal/auth, 80% overall
go run ./cmd/notebooklm --help
```

`make check` must pass before a phase is considered done. Check the exit status directly —
piping `go test` into `tail` or `grep` reports the *pipeline's* status and has masked real
failures before.

## Architecture

`internal/cli` · `internal/mcpsrv` · `internal/restsrv` (three thin adapters)
→ `internal/app` (transport-neutral business logic)
→ `notebooklm` (public SDK)
→ `internal/runtime` + `internal/web/transport` + `internal/auth`
→ `internal/web/wire` (encode/decode)

Boundaries are lint-enforced by `internal/tools/boundarycheck`. If you need to cross one, move
logic **down** into `internal/app` rather than adding an import.

## Ground truth

`../notebooklm-py/src/notebooklm/` is normative for every wire shape, enum value, default, and
error message. Port position for position and cite the Python symbol:

```go
// Port of _web/params/artifacts.py::build_audio_artifact_params.
// Positional shape is load-bearing — see docs/04-rpc-payloads.md#audio.
```

If you cannot find the Python original for a shape, **stop and ask** rather than inventing one.

## Common pitfalls

1. **RPC method IDs change.** Re-capture traffic (doc 03, "Re-capturing the protocol") and
   update `internal/web/wire/methods.go`. `NOTEBOOKLM_RPC_OVERRIDES` patches an id without a
   release.
2. **Source-id nesting depth varies per RPC**, sometimes per slot within one payload —
   `[id]` / `[[id]]` / `[[[id]]]` / `[[[[id]]]]`. Copy the depth from the Python builder.
3. **Go's `encoding/json` breaks byte-compatibility** — HTML escaping, a trailing newline,
   `float64` numbers. Use `wire.Marshal` / `wire.Unmarshal` only.
4. **UTF-16 code units**, not bytes and not runes, for every chat/note character offset.
5. **CSRF tokens expire.** The refresh ladder handles it; do not add ad-hoc retries.
6. **Rate limiting is real.** Pace bulk operations; honor `Retry-After`.
7. **`status` and `drive_status` are different axes** with colliding code spaces. A deleted
   Drive file still reads `status: ready`.
8. **A file add that fails after registering its row leaves the row at `preparing`, not
   `error`** — deliberate evidence, and it counts against quota.
9. **Credentials leak more easily in Go than in Python.** `%v` prints unexported fields and
   `slog` will serialize a whole struct. Implement `LogValue() slog.Value`, not just
   `String()`.

## Terminal theme (CP AXTRA)

Defined once in `internal/cli/theme`. Do not introduce ad-hoc colors.

| Role | Color | Hex |
|---|---|---|
| Primary / headers / IDs | CP AXTRA Blue | `#306FC7` |
| Warning / in-progress | CP AXTRA Yellow | `#F6C24A` |
| Success / ready | Lotus Green | `#43938F` |
| Error / destructive | Makro Red | `#DA3832` |

All color goes to **stderr** in `--json` mode; stdout stays byte-clean JSON.
