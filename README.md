# notebooklm-go

**An unofficial Go client, CLI, MCP server, and REST server for Google Gemini Notebook (formerly NotebookLM)** — full programmatic access from a single static binary.

[![Go 1.25+](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go)](https://go.dev)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Status: In Development](https://img.shields.io/badge/Status-In%20Development-orange)]()

One binary, four surfaces, one Google account: drive notebooks, sources, chat, notes, research, sharing, and the full Studio artifact panel (audio, video, slides, quiz, flashcards, infographic, report, data table, mind map) without touching a browser UI.

> **⚠️ Unofficial — use at your own risk.**
> This project uses **undocumented Google APIs** that can change without notice.
> Not affiliated with Google · APIs may break · rate limits apply.
> Best for prototypes, research, and personal projects.

---

## Why Go matters here

| | Python ecosystem | This project |
|---|---|---|
| Install | `uv tool install`, `pipx`, or a venv; PEP 668 friction | one static binary, `brew install` or `go install` |
| Cold start | ~250 ms (interpreter + imports) | **~5 ms** |
| Container | ~250 MB | **under 40 MB**, distroless-friendly |
| Browser login | bundled Chromium, ~170 MB download | system Chrome over CDP, or no browser at all |
| Optional extras | `[browser]` `[cookies]` `[mcp]` `[server]` `[headless]` `[markdown]` | none — everything is in the binary |
| Cross-compile | per-platform wheels | one machine, six platform pairs |

Cold start matters more than it looks: agents invoke a CLI in a loop, once per tool call. A 250 ms start is 12 seconds of waste per session of a dozen calls; the Go binary is well under a second for the same workload.

---

## What it is

A Go module that drives the same internal `batchexecute` RPC protocol the NotebookLM web UI uses, and exposes it through four interchangeable surfaces:

- **Library** — `notebooklm.Client` with typed namespaces for every domain
- **CLI** — Cobra-based, byte-clean JSON output, ship-in-a-script
- **MCP server** — 33 tools for Claude Code, Claude Desktop, Cursor, Windsurf, or any MCP-aware agent
- **REST server** — 43 guarded `/v1` routes on loopback for local automation

`notebooklm.Client` is safe for concurrent use by multiple goroutines. One client serves one Google account.

---

## Key benefits

- **5 ms cold start** — agents can call this in tight loops without paying Python's interpreter tax.
- **One binary, four surfaces** — the same engine powers the library, CLI, MCP server, and REST server.
- **Distroless-friendly** — under 40 MB container, no Chromium, no venv, no PEP 668.
- **Reuses your browser session** — import cookies from Chrome, Edge, Brave, Arc, Firefox, or Safari instead of a separate auth flow.
- **Drives the full Studio panel** — audio overviews, video, slide decks, quizzes, flashcards, infographics, reports, data tables, mind maps, and back.
- **Cited grounded chat** — every answer resolves to real source ids; programmatic history and follow-ups.
- **Multi-account profiles** — work, personal, and shared notebooks on one machine.
- **Cross-platform from one machine** — `GOOS`/`GOARCH` matrix for Linux, macOS, Windows on amd64 and arm64.

---

## Features

### Working today (Sprints 1–2)

| Surface | Capability |
|---|---|
| Library | `notebooklm.Client`, `FromStorage`, functional options (`WithStoragePath`, `WithBackend`, `WithLogger`, `WithMetrics`, `WithEpoch`, `WithHTTPClient`) |
| Library | `NotebooksAPI` — list, create, get, delete, rename, summary, metadata, copy, share/unshare, remove collaborator, recent/starred/shared-with-me/project-filtered listing |
| Library | Minimum-viable `SourcesAPI` — list sources and add a URL source |
| CLI | 11 leaf commands: `notebook list`/`create`/`delete`/`rename`/`summary`/`metadata`, `session use`/`status`/`clear`, `profile list`/`create`/`switch`/`delete`/`rename`, `auth check` |
| CLI | Cobra with grouped help, theme (CP AXTRA palette), table renderer, JSON interceptor |
| Auth | Tokens, L1 reload, profile read path, `WIZ_global_data` extractor, four-branch failure classifier |
| Testing | `go-vcr` cassette harness with a 7-field match tuple |

### Planned for the S3 mega-sprint and beyond

#### Notebooks
- create · list · copy · rename · set emoji · delete
- summary · AI description · metadata export

#### Sources
- URLs · YouTube · local files (PDF, text, Markdown, Word, EPUB, audio, video, images)
- Google Drive (native docs and upload-only files) · pasted text
- refresh · freshness check · guide · fulltext (text + Markdown) · clean

#### Chat
- grounded questions with citations · conversation history
- source scoping · custom personas · suggested prompts

#### Notes
- create · list · rename · delete
- save a chat answer or a whole conversation as a note

#### Labels & collections
- AI-generated or manual topic labels within a notebook
- account-level notebook collections

#### Research
- web and Drive research agents (fast / deep) with auto-import

#### Sharing
- public/private links · per-user viewer/editor permissions · view-level control

#### Studio (the artifact generator panel)

| Artifact | Generate | Download |
|---|---|---|
| Audio Overview | yes | `.m4a` |
| Video Overview | yes | `.mp4` |
| Cinematic Video | yes | `.mp4` |
| Slide Deck | yes (+ per-slide revision) | `.pdf` / `.pptx` |
| Infographic | yes | `.png` |
| Quiz | yes | `.json` / `.md` / `.html` |
| Flashcards | yes | `.json` / `.md` / `.html` |
| Report | yes | `.md` |
| Data Table | yes | `.csv` |
| Mind Map (interactive, note-backed) | yes | `.json` |

#### Export
MP4 · M4A · PDF · PPTX · PNG · CSV · JSON · Markdown · HTML — plus batch downloads with `--all`/`--latest`/`--earliest`/`--name` selectors and Google Docs/Sheets export.

#### Surfaces in the pipeline

- **MCP server** (`notebooklm-mcp`) — 33 tools over stdio or streamable HTTP, with an `install` command for Claude Code, Claude Desktop, Cursor, and Windsurf.
- **REST server** (`notebooklm-server`) — 43 guarded `/v1` routes on loopback, single-bearer-token, fail-closed on external bind.
- **Skill installer** — `notebooklm skill install` writes the bundled `SKILL.md` so MCP-aware agents can teach themselves the surface.

---

## Install

```bash
go install github.com/raihankhan/notebooklm-go/cmd/notebooklm@latest
```

Or build all three binaries from source:

```bash
git clone https://github.com/raihankhan/notebooklm-go
cd notebooklm-go
make build   # writes ./bin/notebooklm, ./bin/notebooklm-mcp, ./bin/notebooklm-server
```

A Homebrew tap will land alongside the v1 release.

---

## Quickstart

### 1. Authenticate

```bash
# Reuse a signed-in browser session — no separate login flow
notebooklm auth check
```

Cookie import from Chrome, Edge, Brave, Arc, Firefox, or Safari; inline env auth via `NOTEBOOKLM_AUTH_JSON`; and headless master-token re-mint are all supported.

### 2. Drive a notebook from the CLI

```bash
notebooklm notebook list
notebooklm notebook create "My Research" --use
notebooklm notebook summary
```

### 3. Add a source

```bash
notebooklm source add https://en.wikipedia.org/wiki/Artificial_intelligence
notebooklm source add ./paper.pdf
```

### 4. Ask a grounded question

```bash
notebooklm ask "What are the key themes?"
```

### 5. Generate and download Studio artifacts

```bash
notebooklm generate audio "make it engaging" --wait
notebooklm generate quiz --difficulty hard
notebooklm generate mind-map

notebooklm download audio ./podcast.m4a
notebooklm download quiz --format markdown ./quiz.md
notebooklm download mind-map ./mindmap.json
```

### Use the Go library

```go
package main

import (
    "context"
    "fmt"

    "github.com/raihankhan/notebooklm-go/notebooklm"
)

func main() {
    ctx := context.Background()

    client, err := notebooklm.FromStorage(ctx)
    if err != nil {
        panic(err)
    }
    defer client.Close(context.WithoutCancel(ctx))

    nb, err := client.Notebooks.Create(ctx, "Research")
    if err != nil {
        panic(err)
    }

    if _, err := client.Sources.AddURL(ctx, nb.ID,
        "https://example.com"); err != nil {
        panic(err)
    }

    fmt.Println("Created notebook:", nb.ID, nb.Title)
}
```

The same `*notebooklm.Client` is safe to share across goroutines; every method takes a `context.Context`.

---

## Surfaces

| Surface | Binary | Use when |
|---|---|---|
| **Library** | `import "github.com/raihankhan/notebooklm-go/notebooklm"` | Embedding in a Go service or tool |
| **CLI** | `notebooklm` | Shell scripts, cron, ad-hoc queries, byte-clean JSON piping |
| **MCP server** | `notebooklm-mcp` | Wiring into Claude Code, Claude Desktop, Cursor, Windsurf, claude.ai (remote connector), or any MCP-aware agent |
| **REST server** | `notebooklm-server` | Local automation that prefers HTTP over spawning a subprocess |

The CLI is the test bed; the library is what you embed; the MCP server is what your agent speaks; the REST server is what your scripts can hit when they would rather not fork.

---

## Use cases

**Agent loops that don't waste time on cold starts.** A Claude Code session might invoke a notebook tool a dozen times. A 250 ms cold start per call is over 3 seconds of pure interpreter overhead per session; the Go binary lands under 100 ms for the same workload. At scale, that's the difference between a snappy agent and a sluggish one.

**Distroless containers without Chromium.** Ship a 40 MB container and run the CLI, MCP server, or REST server inside it. No venv, no PEP 668, no bundled browser, no Node driver — one binary per process.

**Self-hosted MCP for claude.ai on a phone.** Run the MCP server behind a Cloudflare or Tailscale tunnel and the binary becomes a remote connector that claude.ai — including the mobile app — can reach. Master-token auth self-heals expired sessions without a browser, which is what makes an unattended remote connector viable.

**Local automation over HTTP.** Some tools prefer HTTP over spawning a CLI per call. The REST server is bound to loopback by default, requires a bearer token, and exposes 43 `/v1` routes that map one-to-one onto the namespaces.

**Reuse your existing browser session.** No separate Google sign-in: import cookies from the browser you're already signed into. The auth ladder handles L1 token refresh, L2 cookie rotation, L3 headless re-auth, and L4 master-token re-mint on its own.

**Bulk Studio artifact pipelines.** Generate audio overviews, quizzes, flashcards, mind maps, and reports across a corpus of notebooks. Download in the format you need (MP4, M4A, PDF, PPTX, JSON, Markdown, HTML) with `--all`, `--latest`, `--earliest`, or `--name` selectors and collision-safe writes.

**Programmatic sharing.** Public links, per-user viewer or editor permissions, and chat-only vs full-notebook view levels — all without opening the UI. Confirmation gates prevent accidental widening of access.

**Source ingest at scale.** URLs, YouTube, PDFs, Word documents, EPUBs, audio, video, images, Google Drive, and pasted text all go through the same API. Status filters (`ready` / `processing` / `error` / `preparing`) make it easy to find orphaned rows.

---

## Documentation

| Doc | |
|---|---|
| [Doc index](docs/README.md) | start here |
| [Overview and parity matrix](docs/01-overview.md) | scope, non-goals, what ships when |
| [Architecture](docs/02-architecture.md) | packages, concurrency model, dependencies |
| [The batchexecute protocol](docs/03-protocol-batchexecute.md) | wire format, method table, decoding |
| [Authentication](docs/05-auth.md) | cookies, storage format, refresh ladder, master token |
| [CLI spec](docs/07-cli-spec.md) | full command tree, exit codes, theme |
| [MCP spec](docs/08-mcp-spec.md) | 33 tools, transports, install targets |
| [REST spec](docs/09-rest-spec.md) | 43 routes, security posture, error envelope |
| [Implementation plan](docs/10-implementation-plan.md) | 13 phases with acceptance criteria |
| [Operations](docs/13-operations.md) | env vars, packaging, deployment |
| [Features overview](docs/15-features-overview.md) | the eleven net-new features layered on parity |

---

## Status

**In development.** Sprints 1–2 ship the public SDK entry point, `NotebooksAPI` (17 methods), the minimum-viable `SourcesAPI` (list + add URL), eleven CLI leaf commands, the auth tokens and reload path, and the cassette harness. The S3 mega-sprint lands the chat, full sources namespace, Studio artifacts, research, sharing, REST, MCP, and skill installer. A live end-to-end run (login → create → add 3 source kinds → ask → generate audio + quiz + mind-map → download all → share → delete) is the acceptance criterion for v1.

What works today and what is coming is itemized under [Features](#features) above; the design and build order live in [`docs/10-implementation-plan.md`](docs/10-implementation-plan.md).

---

## Disclaimer

This project uses undocumented Google APIs that can change without notice. Not affiliated with Google. APIs may break. Rate limits apply. Best for prototypes, research, and personal projects.

---

## License

MIT. See [LICENSE](LICENSE).
