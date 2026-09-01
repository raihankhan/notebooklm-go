# notebooklm-go

**An unofficial Go client for Google Gemini Notebook (formerly NotebookLM)** — full
programmatic access via a library, a CLI, an MCP server, and a REST server, from a single
static binary.

A Go port of [`notebooklm-py`](https://github.com/teng-lin/notebooklm-py), keeping the
protocol knowledge byte-identical and the CLI/MCP/REST surfaces drop-in compatible.

> **⚠️ Unofficial — use at your own risk.**
> This uses **undocumented Google APIs** that can change without notice.
> Not affiliated with Google · APIs may break · rate limits apply.
> Best for prototypes, research, and personal projects.

---

## Status

**Pre-implementation.** The complete design and phased build plan live in [`docs/`](docs/).
Start with [`docs/README.md`](docs/README.md) for the index, or
[`docs/10-implementation-plan.md`](docs/10-implementation-plan.md) for the build order.

## Why a Go port

| | Python original | This port |
|---|---|---|
| Install | `uv tool install`, `pipx`, or a venv; PEP 668 friction on macOS/Debian | one static binary, `brew install` or `curl \| sh` |
| Browser login | bundled Chromium, ~170 MB download, Node driver | system Chrome over CDP, or no browser at all via cookie import |
| Optional extras | `[browser]` `[cookies]` `[mcp]` `[server]` `[headless]` `[markdown]` `[impersonate]` | none — it is all in the binary |
| Cold start | ~250 ms interpreter + imports | ~5 ms |
| Container | ~250 MB | under 40 MB, distroless |
| Cross-compile | per-platform wheels | one machine, six platform pairs |

Cold start matters more than it looks: agents call this CLI in loops, once per tool
invocation.

## What it does

| Category | Capabilities |
|---|---|
| **Notebooks** | create, list, copy, rename, set emoji, delete, summary, AI description, metadata export |
| **Sources** | URLs, YouTube, local files (PDF, text, Markdown, Word, EPUB, audio, video, images), Google Drive, pasted text; refresh, freshness check, guide, fulltext, clean |
| **Chat** | grounded questions with citations, conversation history, source scoping, custom personas, suggested prompts |
| **Notes** | create, list, rename, delete, save a chat answer or a whole conversation as a note |
| **Labels & collections** | AI-generated or manual topic labels within a notebook; account-level notebook collections |
| **Research** | web and Drive research agents (fast / deep) with auto-import |
| **Sharing** | public/private links, per-user viewer/editor permissions, view-level control |
| **Studio** | Audio Overview · Video Overview · Cinematic Video · Slide Deck (+ per-slide revision) · Infographic · Quiz · Flashcards · Report · Data Table · Mind Map (interactive and note-backed) |
| **Export** | MP4, M4A, PDF, PPTX, PNG, CSV, JSON, Markdown, HTML — plus batch downloads and Google Docs/Sheets export |

### Beyond the web UI

Batch downloads · quiz and flashcard export as JSON/Markdown/HTML · mind-map JSON extraction ·
data tables as CSV · slide decks as editable PPTX · individual slide revision by prompt ·
report template customization · saving a whole Q&A conversation as a note · source fulltext
access · programmatic sharing.

## Planned usage

### CLI

```bash
notebooklm login --browser-cookies chrome     # reuse your signed-in browser session
notebooklm create "My Research" --use
notebooklm source add "https://en.wikipedia.org/wiki/Artificial_intelligence"
notebooklm source add ./paper.pdf
notebooklm ask "What are the key themes?"

notebooklm generate audio "make it engaging" --wait
notebooklm generate quiz --difficulty hard
notebooklm generate mind-map

notebooklm download audio ./podcast.m4a
notebooklm download quiz --format markdown ./quiz.md
notebooklm download mind-map ./mindmap.json
```

### Library

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
    if err != nil { panic(err) }
    defer client.Close(context.WithoutCancel(ctx))

    nb, err := client.Notebooks.Create(ctx, "Research")
    if err != nil { panic(err) }

    if _, err := client.Sources.AddURL(ctx, nb.ID, "https://example.com",
        notebooklm.WithWait(true)); err != nil { panic(err) }

    answer, err := client.Chat.Ask(ctx, nb.ID, "Summarize this")
    if err != nil { panic(err) }
    fmt.Println(answer.Answer)

    task, err := client.Artifacts.GenerateAudio(ctx, nb.ID,
        notebooklm.WithInstructions("make it fun"))
    if err != nil { panic(err) }
    if err := client.Artifacts.WaitForCompletion(ctx, nb.ID, task.TaskID); err != nil { panic(err) }
    if err := client.Artifacts.DownloadAudio(ctx, nb.ID, "podcast.m4a"); err != nil { panic(err) }
}
```

A `*notebooklm.Client` is safe for concurrent use by multiple goroutines. Every method takes a
`context.Context`. One client serves one Google account.

### MCP server

```bash
notebooklm mcp install claude-code     # or claude-desktop | cursor | windsurf
```

33 tools over stdio or streamable HTTP: notebooks, sources, chat, notes, the unified studio
panel, research, sharing. Can be self-hosted behind a Cloudflare or Tailscale tunnel and used
as a remote connector from claude.ai — including mobile.

### REST server

```bash
NOTEBOOKLM_SERVER_TOKEN=$(openssl rand -hex 32) notebooklm-server
```

43 guarded `/v1` routes on loopback, for local automation that would rather not spawn a
process per call.

## Migrating from `notebooklm-py`

Your existing `~/.notebooklm/` directory works unchanged — same profiles, same
`storage_state.json`, same `master_token.json`. Commands, flags, `--json` keys, exit codes, MCP
tool names, and REST routes are all preserved.

```bash
brew install raihankhan/tap/notebooklm
notebooklm auth check --test --json      # expect "status": "ok"
```

Differences are listed in [`docs/13-operations.md`](docs/13-operations.md#migrating-from-notebooklm-py).
The short version: interactive login uses your system Chrome instead of a bundled Chromium,
optional extras are gone, and the Android backend is deferred to v2.

## Documentation

| Doc | |
|---|---|
| [Doc index](docs/README.md) | start here |
| [Agent guide](docs/AGENTS.md) | rules of engagement for contributors and agents |
| [Overview & parity matrix](docs/01-overview.md) | scope, non-goals, what ships when |
| [Architecture](docs/02-architecture.md) | packages, concurrency model, dependencies |
| [The batchexecute protocol](docs/03-protocol-batchexecute.md) | wire format, method table, decoding |
| [RPC payloads](docs/04-rpc-payloads.md) | positional shapes, per RPC |
| [Authentication](docs/05-auth.md) | cookies, storage format, refresh ladder, master token |
| [Domain model](docs/06-domain-model.md) | types and enums |
| [CLI spec](docs/07-cli-spec.md) | full command tree, exit codes, theme |
| [MCP spec](docs/08-mcp-spec.md) | 33 tools |
| [REST spec](docs/09-rest-spec.md) | 43 routes |
| [Implementation plan](docs/10-implementation-plan.md) | 13 phases with acceptance criteria |
| [Testing strategy](docs/11-testing-strategy.md) | three tiers, guardrails, fuzzing |
| [Porting map](docs/12-porting-map.md) | Python module → Go package |
| [Operations](docs/13-operations.md) | env vars, packaging, deployment |
| [Risk register](docs/14-risk-register.md) | what breaks, and where it is mitigated |

## Credits

This project would not exist without
[`notebooklm-py`](https://github.com/teng-lin/notebooklm-py) by
[Teng Lin](https://github.com/teng-lin) and its contributors, who reverse-engineered the
entire protocol, the auth lifecycle, and every wire shape this port depends on. All protocol
knowledge here is derived from that work.

## License

MIT. See [LICENSE](LICENSE).
