# 01 — Overview, scope, and parity matrix

## Goal

Ship a Go module that is a **drop-in functional replacement** for `notebooklm-py` v0.8.1:
same feature set, same CLI command surface and exit codes, same MCP tool names, same REST
routes, same on-disk credential format — so an existing user can swap the binary and keep
their `~/.notebooklm/` directory, their shell scripts, and their MCP client config.

## Why Go

| Motivation | What it buys |
|---|---|
| Single static binary | No Python, no venv, no `uv tool install`, no PEP 668 friction. `curl \| tar` and run. Solves the largest installation-support surface of the original. |
| Fast cold start | The CLI is invoked once per shell command and once per agent tool call. Python's ~250 ms interpreter + import cost becomes ~5 ms. This matters: agents call it in loops. |
| Real concurrency | Bulk import, `--all` downloads, and multi-notebook fan-out become goroutine + `errgroup` work with no event-loop-affinity contract to violate. |
| Cross-compilation | `GOOS`/`GOARCH` matrix from one machine; ARM Linux servers and Windows get first-class binaries. |
| Small container | `FROM scratch` / distroless remote-MCP image, ~15 MB vs ~250 MB. |

## Non-goals (v1)

| Not doing | Why |
|---|---|
| **Android/mobile gRPC backend** (`_android/`, ~90 files, protobuf stubs) | The web `batchexecute` backend covers 100% of the user-facing feature set. The Android path exists in the original as protocol research and a redundancy hedge. Deferred to v2; the `Backend` interface seam is designed in from day one so it can land without a refactor. |
| **Playwright-driven interactive login with bundled Chromium** | The Go Playwright bindings require a Node driver, which defeats the single-binary goal. Replaced by three equivalent paths: CDP attach to a user's running Chrome, direct browser-cookie import (`kooky`), and master-token headless auth. See doc 05. |
| **Python API compatibility shim** | Different language. The Go SDK mirrors the *shape* of the Python namespaces (`client.Notebooks.List(...)`) but is idiomatic Go. |
| **VCR cassette binary compatibility** | We re-record with `go-vcr`. Matching rules are ported; the YAML files are not shared. |
| `desktop-extension/` `.mcpb` bundle | Nice-to-have; scheduled after v1 in doc 10, Phase 13. |

## Parity matrix

Legend: **✅ v1** = in scope for first release · **🟡 v1.1** = fast follow · **⬜ v2** = deferred

### Core domain

| Capability | Status | Notes |
|---|---|---|
| Notebooks: list, create, copy, get, rename, set-emoji, delete, remove-from-recent | ✅ v1 | |
| Notebook summary / AI description / metadata export | ✅ v1 | |
| Sources: add URL, YouTube, pasted text, local file, Google Drive doc, Drive upload-only file | ✅ v1 | |
| Sources: list (+ status/label filters), get, rename, refresh, freshness check, delete, delete-by-title, clean | ✅ v1 | |
| Sources: guide, fulltext (text + markdown) | ✅ v1 | Markdown conversion needs an HTML→MD converter; see doc 13. |
| Sources: wait-until-ready, wait-all, wait-until-registered | ✅ v1 | |
| Source labels: list, sources, AI-generate, create, rename, emoji, add, remove, delete | ✅ v1 | |
| Collections (account-level notebook grouping) | ✅ v1 | Reuses the four label RPCs; a collection is a type-3 label with a null notebook parent. |
| Chat: ask (streaming), citations/references, source scoping, conversation resume | ✅ v1 | |
| Chat: history, delete conversation, save answer as note, save history as note | ✅ v1 | |
| Chat: configure (mode presets, custom persona, response length), suggested prompts | ✅ v1 | |
| Notes: list, create, get, save, rename, delete | ✅ v1 | |
| Research: fast + deep, web + Drive, poll, wait, import (cited-only, max-sources), cancel | ✅ v1 | |
| Sharing: status, public/restricted, view level, add/update/remove user | ✅ v1 | |
| Settings: output language get/set, account limits + tier | ✅ v1 | |

### Studio artifacts

| Type | Generate | Download | Notes |
|---|---|---|---|
| Audio Overview | ✅ v1 | ✅ `.m4a` | 4 formats, 3 lengths, 50+ languages |
| Video Overview | ✅ v1 | ✅ `.mp4` | 4 formats, 10 styles (+ custom style prompt) |
| Cinematic Video | ✅ v1 | ✅ `.mp4` | CLI alias for `video --format cinematic` |
| Slide Deck | ✅ v1 | ✅ `.pdf` / `.pptx` | + per-slide revision |
| Infographic | ✅ v1 | ✅ `.png` | 3 orientations, 3 detail levels, 11 styles |
| Quiz | ✅ v1 | ✅ `.json` / `.md` / `.html` | |
| Flashcards | ✅ v1 | ✅ `.json` / `.md` / `.html` | |
| Report | ✅ v1 | ✅ `.md` | briefing-doc, study-guide, blog-post, custom, `--append` |
| Data Table | ✅ v1 | ✅ `.csv` | |
| Mind Map — interactive | ✅ v1 | ✅ `.json` | studio artifact, type 4 / variant 4, polled |
| Mind Map — note-backed | ✅ v1 | ✅ `.json` | JSON tree stored as a note, synchronous |
| Artifact lifecycle: list, get, get-prompt, rename, delete, poll, wait, retry, export to Docs/Sheets, suggestions | ✅ v1 | | |

### Adapters

| Adapter | Status | Notes |
|---|---|---|
| CLI (`notebooklm`) — full command tree, `--json` everywhere, exit-code contract, shell completion | ✅ v1 | Cobra. Doc 07. |
| MCP server (`notebooklm-mcp`) — 33 tools, stdio + streamable HTTP, bearer auth | ✅ v1 | Official Go SDK. Doc 08. |
| REST server (`notebooklm-server`) — `/v1`, bearer + loopback guard, poll-the-resource | ✅ v1 | stdlib `net/http`. Doc 09. |
| MCP remote deploy (Docker + Cloudflare/Tailscale tunnel) | ✅ v1 | Doc 13. |
| MCP self-hosted OAuth (for claude.ai / ChatGPT connectors) | 🟡 v1.1 | Bearer-token path ships in v1. |
| `mcp install <client>` config writer | ✅ v1 | |
| Agent skill install (`skill install/status/show/package`) | ✅ v1 | |
| `.mcpb` desktop extension bundle | 🟡 v1.1 | |

### Auth

| Path | Status | Notes |
|---|---|---|
| Import cookies from an installed browser (Chrome/Edge/Brave/Arc/Firefox/Safari, per-profile, Firefox containers) | ✅ v1 | via `kooky`. Replaces the original's `rookiepy` extra. |
| Import cookies from a JSON file / stdin (`auth import-cookies`) | ✅ v1 | |
| Inline env auth (`NOTEBOOKLM_AUTH_JSON`) | ✅ v1 | |
| Master-token headless auth (mint cookies on demand, self-healing) | ✅ v1 | Direct `android.clients.google.com/auth` implementation; no `gpsoauth` equivalent needed. |
| CDP attach to a running Chrome for interactive login + `oauth_token` capture | ✅ v1 | `chromedp`. |
| Headed browser login launching a system Chrome/Edge under CDP | ✅ v1 | Replaces Playwright. |
| Multi-account profiles, per-account routing (`authuser`) | ✅ v1 | |
| L1 token refresh, L2 `RotateCookies` keepalive, L2.5 `NOTEBOOKLM_REFRESH_CMD`, L3 headless re-auth, L4 master-token re-mint | ✅ v1 | Full ladder. Doc 05. |
| Bundled-Chromium auto-download | ⬜ never | Explicit non-goal; conflicts with single-binary. |

### Runtime behaviors carried over

| Behavior | Status |
|---|---|
| Middleware chain: retry (429/5xx + `Retry-After`) → auth-refresh → error-injection → tracing | ✅ v1 |
| Idempotency taxonomy (5 classes) consulted per call | ✅ v1 |
| Single-flight auth refresh across concurrent callers, cross-process file lock | ✅ v1 |
| Once-per-logical-call refresh budget shared by the HTTP-status and decode layers | ✅ v1 |
| Aggregate retry deadline anchored at T0 | ✅ v1 |
| Strict positional decoding with typed shape-drift errors | ✅ v1 |
| Response size cap; chunked `rt=c` parser with malformed-record rate limits | ✅ v1 |
| Download SSRF guard: per-redirect-hop host allowlist, credential stripping off-allowlist | ✅ v1 |
| Atomic credential writes, `0600`/`0700`, `.bak` rollback | ✅ v1 |
| Structured logging with credential redaction + request-id correlation | ✅ v1 |
| Client metrics snapshot + `on_rpc_event` callback | ✅ v1 |
| Drain-on-close lifecycle with resource generations (epoch fencing) | ✅ v1 |
| Loop-affinity contract (ADR-0004) | ⬜ n/a | Python-specific. Replaced by "safe for concurrent use". See doc 02. |
| TLS/JA3 browser impersonation | 🟡 v1.1 | Optional `utls` transport behind `NOTEBOOKLM_TRANSPORT=utls`. Default `net/http`. |

## Success criteria for v1

1. Every CLI command in `../notebooklm-py/docs/cli-reference.md` exists, accepts the same
   flags, and produces the same `--json` envelope keys and the same exit code.
2. All 33 MCP tools present with matching names and parameter names.
3. All 43 REST routes present with matching paths, methods, and status codes.
4. A `~/.notebooklm/` directory written by the Python CLI works unmodified, and vice versa.
5. Cassette suite covers every RPC in the method table; `make check` green.
6. One live end-to-end run: login → create → add 3 source kinds → ask → generate audio +
   quiz + mind-map → download all → share → delete.
