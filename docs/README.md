# notebooklm-go — Documentation

Production port of [`notebooklm-py`](https://github.com/teng-lin/notebooklm-py) (v0.8.1,
~137k LOC Python) to Go: an unofficial client for Google **Gemini Notebook** (formerly
NotebookLM) driving Google's internal `batchexecute` RPC protocol.

Deliverables: one Go module shipping a **library**, a **Cobra CLI** (`notebooklm`), an
**MCP server** (`notebooklm-mcp`), and a **REST server** (`notebooklm-server`) — feature-for-feature
equivalent to the Python original.

Module path: `github.com/raihankhan/notebooklm-go`

---

## Reading order

| # | Doc | Read it when |
|---|-----|--------------|
| 00 | [AGENTS.md](AGENTS.md) | **First.** Rules of engagement for any agent or human touching this repo. |
| 01 | [01-overview.md](01-overview.md) | You need scope, non-goals, and the parity matrix. |
| 02 | [02-architecture.md](02-architecture.md) | You need the Go package layout and concurrency model. |
| 03 | [03-protocol-batchexecute.md](03-protocol-batchexecute.md) | You are touching the wire: encode, URL, headers, decode. |
| 04 | [04-rpc-payloads.md](04-rpc-payloads.md) | You are adding or fixing one RPC's positional payload. |
| 05 | [05-auth.md](05-auth.md) | You are touching cookies, login, refresh, or the master token. |
| 06 | [06-domain-model.md](06-domain-model.md) | You need the type/enum reference. |
| 07 | [07-cli-spec.md](07-cli-spec.md) | You are implementing a Cobra command. |
| 08 | [08-mcp-spec.md](08-mcp-spec.md) | You are implementing an MCP tool. |
| 09 | [09-rest-spec.md](09-rest-spec.md) | You are implementing a REST route. |
| 10 | [10-implementation-plan.md](10-implementation-plan.md) | **The build order.** 13 phases, each with acceptance criteria. |
| 11 | [11-testing-strategy.md](11-testing-strategy.md) | You are writing tests or wiring CI. |
| 12 | [12-porting-map.md](12-porting-map.md) | You need "where did Python file X go?" |
| 13 | [13-operations.md](13-operations.md) | You are packaging, deploying, or configuring. |
| 14 | [14-risk-register.md](14-risk-register.md) | You are planning, estimating, or reviewing risk. |

## The one-paragraph summary

Google's NotebookLM web app talks to its backend over `batchexecute`, an undocumented
Google-internal RPC transport: obfuscated 6-character method IDs, positional (index-addressed)
JSON arrays for both request and response, form-encoded bodies, chunked `rt=c` response framing,
and cookie-based Google account auth with a CSRF token scraped out of the app shell's
`WIZ_global_data` blob. `notebooklm-py` reverse-engineered all of it. This port keeps the
protocol knowledge **byte-identical** and rebuilds the surrounding runtime idiomatically in Go:
goroutines + `context.Context` instead of asyncio, a custom introspectable cookie jar instead of
`httpx.Cookies`, Cobra instead of Click, the official MCP Go SDK instead of FastMCP, and stdlib
`net/http` instead of FastAPI.

## Ground truth

The Python source tree is the **normative reference** for every wire shape. It lives at
`../notebooklm-py` relative to this repo. When this documentation and that source disagree,
**the Python source wins** — file an issue and fix the doc.

Canonical Python files to consult, by topic:

| Topic | Python file |
|---|---|
| RPC method IDs | `src/notebooklm/rpc/types.py` |
| Request encoding | `src/notebooklm/_web/wire/encoder.py` |
| Response decoding | `src/notebooklm/_web/wire/decoder.py` |
| Positional payloads | `src/notebooklm/_web/params/*.py` |
| Response row decoding | `src/notebooklm/_web/rows/*.py` |
| Enums | `src/notebooklm/_types/enums.py` |
| Auth ladder | `src/notebooklm/_auth/session.py`, `_auth/refresh.py` |
| Master token | `src/notebooklm/_auth/mint_service.py` |
| Upload (Scotty) | `src/notebooklm/_web/params/sources.py`, `_web/sources/upload.py` |
| Download allowlist | `src/notebooklm/_artifact/_download_client.py` |
| Download registry | `src/notebooklm/_app/download_specs.py` |
| CLI surface | `docs/cli-reference.md` |
| RPC payload reference | `docs/rpc-reference.md` |
