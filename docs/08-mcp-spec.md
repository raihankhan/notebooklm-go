# 08 — MCP server specification

Normative source: `../notebooklm-py/docs/mcp-guide.md` and
`../notebooklm-py/src/notebooklm/mcp/`.

Binary: `notebooklm-mcp`. Library: `github.com/modelcontextprotocol/go-sdk`.

**33 tools.** Names and parameter names must match the Python server exactly — an existing
Claude Desktop / Cursor / Windsurf setup and any agent prompt referencing a tool name must
keep working.

## Transport and flags

```
notebooklm-mcp                          # stdio (default) — for subprocess hosts
notebooklm-mcp --profile work           # bind a specific auth profile
notebooklm-mcp --transport http         # loopback streamable HTTP on 127.0.0.1:9420
notebooklm-mcp --transport http --port 9000
```

| Flag | Default | Notes |
|---|---|---|
| `--profile` | active profile | which stored auth profile the process binds at startup |
| `--transport` | `stdio` | `stdio` or `http` |
| `--host` | `127.0.0.1` | http only |
| `--port` | `9420` | http only |
| `--log-level` | `INFO` | logs go to **stderr**; stdout stays pure JSON-RPC |

**There is no `--token` flag.** The HTTP bearer token is env-only
(`NOTEBOOKLM_MCP_TOKEN`) so it cannot leak via `ps aux`. Keep that omission.

### Fail-closed network binding

The server fronts a full Google account. Binding to a non-loopback address requires
**both**:

1. `NOTEBOOKLM_MCP_ALLOW_EXTERNAL_BIND=1`, and
2. a non-empty `NOTEBOOKLM_MCP_TOKEN`.

A network bind without a token must **refuse to start**, not warn. Bearer comparison uses
`crypto/subtle.ConstantTimeCompare`.

`NOTEBOOKLM_MCP_TRUST_PROXY` enables `X-Forwarded-*` handling for the tunnel deployment;
off by default.

### stdio hygiene

The single hardest thing to get right. On stdio transport, **anything** written to stdout
that is not framed JSON-RPC corrupts the session — and the host usually reports it as an
opaque connection failure.

- Route `log/slog` to stderr at process start, before any other initialization.
- No `fmt.Print*` anywhere reachable from the MCP code path. `boundarycheck` enforces this
  for `internal/mcpsrv`.
- Third-party libraries that log to stdout must be silenced or wrapped.

## Auth model

The server **reuses the CLI's stored credentials** and does **not** log in on its own.
Authenticate once with `notebooklm login`, then start the server. It binds the active
profile at startup and opens **one long-lived client**.

For unattended remote deployment, use master-token auth (doc 05, path D): it self-heals
expired sessions without a browser, which is what makes a remote connector viable.

**L3 headless re-auth is never enabled on the MCP path** — it is local-unattended only.

## Client installation

`notebooklm mcp install <client>` writes an idempotent server block:

| Client | Config file |
|---|---|
| `claude-desktop` | `claude_desktop_config.json` (per-OS location) |
| `claude-code` | `~/.claude.json` (user scope) |
| `cursor` | `~/.cursor/mcp.json` |
| `windsurf` | `~/.codeium/windsurf/mcp_config.json` |

The Python installer launches via `uvx`. **Go's block is simpler and better** — a single
binary on `PATH`:

```jsonc
{
  "mcpServers": {
    "notebooklm": { "command": "notebooklm-mcp", "args": [] }
  }
}
```

Preserve unrelated servers in the same file; never rewrite the whole document. Write
atomically with a `.bak`.

## Tool surface

`READ_ONLY` and `DESTRUCTIVE` are MCP annotations. A host that honors them can auto-allow
reads and gate destructive calls. The four destructive tools (`notebook_delete`,
`source_delete`, `studio_delete`, `share_remove_user`) plus the confirmation-gated sharing
tools require an explicit `confirm: true`; without it they return a **`needs_confirmation`
preview** describing exactly what would happen.

### Notebooks (5)

| Tool | Params |
|---|---|
| `notebook_list` | `limit?`, `offset?` |
| `notebook_create` | `title` |
| `notebook_describe` | `notebook`, `include_metadata?` — AI description; `include_metadata=true` adds a `metadata` block with notebook details + source list |
| `notebook_rename` | `notebook`, `new_title` |
| `notebook_delete` | `notebook`, `confirm` — DESTRUCTIVE |

### Sources (6)

| Tool | Params |
|---|---|
| `source_list` | `notebook`, `status?`, `label?`, `detail?`, `limit?`, `offset?` |
| `source_read` | `notebook`, `source`, `detail?`, `output_format?`, `max_chars?`, `offset?` |
| `source_rename` | `notebook`, `source`, `new_title` |
| `source_delete` | `notebook`, `source`, `confirm` — DESTRUCTIVE |
| `source_wait` | `notebook`, `source?`, `timeout`, `interval` |
| `source_add` | single: `notebook`, `source_type`, …, `bytes_base64?`, `filename?`, `wait?`, `timeout?`, `interval?`, `allow_internal?` · batch: `notebook`, `urls=[…]`, `allow_internal?` |
| `source_add_drive_file` | `notebook`, `document_id`, `title?`, `wait?` |
| `await_upload` | `upload_link`, `timeout?` |

Behavioral contracts to port precisely:

- Every source row carries string `kind` / `status_label`, plus `drive_status_label` and
  `is_drive_degraded` — a **separate axis**, `null`/`false` on non-Drive sources.
- `status` filters to one of `unknown|processing|ready|error|preparing`. `status="error"`
  finds failed imports; `status="preparing"` finds orphaned rows.
- `detail=compact` returns a low-token roster: `id`/`title`/`kind`/`status_label`/
  `drive_status_label`/`created_at`.
- `source_read` `detail=full` (default) → metadata + a bounded slice of indexed text:
  `max_chars` caps `content` (default 10,000), `offset` pages, plus a `truncated` flag and
  the full `char_count`. `detail=summary` → low-token triage: AI summary **plus keywords**,
  not the body.
- `source_wait` attaches a **non-blocking `warning`** when a READY web page has thin/empty
  text, or a short body matching a dead-link / soft-404 / bot-challenge / WAF interstitial
  pattern. Port the pattern list from `mcp/tools/_content_sanity.py`.
- `source_add` echoes `kind`/`status_label` and flags a failed import inline with a
  `warning`. `bytes_base64` + `filename` add a small file **in-channel** with no signed URL.
  `wait=true` folds in `source_wait` and adds a top-level `source_id`; single-source only,
  never for a remote `file` upload.
- Batch `urls=[…]` returns per-item `results`; a synchronously-ready web page may carry the
  same content-sanity warning.
- `source_add_drive_file` downloads an **upload-only** Drive file (epub/docx/pptx/txt/md/
  rtf/odt/csv/tsv/pdf) server-side and uploads it. A Google-native Doc/Slides/Sheet returns a
  **pointer error** telling the caller to use `source_add(source_type='drive')`.

### Chat (3)

| Tool | Params |
|---|---|
| `chat_ask` | `notebook`, `question?`, `conversation_id?`, `references?`, `source_ids?`, `history?`, `suggest_followups?` |
| `chat_configure` | `notebook`, `chat_mode?`, `goal?`, `response_length?` |
| `suggest_prompts` | `notebook`, `surface?`, `source_ids?`, `query?` — READ_ONLY |

- `references`: `lite` | `full`. **Never return the raw debug blob.**
- `source_ids` accepts a list, a JSON-array string, or a comma string; omit for all sources.
- `history` > 0 also returns up to N prior `{question, answer}` pairs. Omit `question` to
  recall only.
- `suggest_followups=true` adds `suggested_prompts` (3 questions) — works question-less too.
- `chat_configure`: `chat_mode` is a preset, **mutually exclusive** with `goal`/
  `response_length`. A partial custom call sets just one field and **merges** — the omitted
  field is preserved. Only a bare call with no preset and neither field is rejected.
- `suggest_prompts` `surface`: `ask` (default) | `audio-deep-dive` | `audio-brief` |
  `audio-critique` | `audio-debate` | `video-explainer` | `video-short` | `quiz` |
  `flashcards`. Returns `{title, prompt}` pairs. These map onto the integer modes in doc 04.

### Notes (1)

| Tool | Params |
|---|---|
| `note_save` | `notebook`, `note?`, `title?`, `content?` |

An **upsert**: omit `note` to create (both `title` and `content` required); pass a `note` ref
to update (`title` and/or `content`; title-only = rename). Reading and deleting notes fold
into the Studio row below — that unification is deliberate.

### Studio (7) — the unified notes + artifacts panel

| Tool | Params |
|---|---|
| `studio_list` | `notebook`, `item?`, `kind?`, `detail?`, `limit?`, `offset?` |
| `studio_generate` | `notebook`, `artifact_type`, … (per-type options) |
| `studio_status` | `notebook`, `task_id` |
| `studio_download` | `notebook`, `artifact?` \| `artifact_type?`, `path?`, `output_format?`, `artifact_id?` |
| `studio_rename` | `notebook`, `item`, `new_title` — cross-type |
| `studio_retry` | `notebook`, `artifact` |
| `studio_delete` | `notebook`, `item`, `confirm` — DESTRUCTIVE, cross-type |

`studio_list` merges **notes AND artifacts** into one `items` list. Each item has
`id`/`title`/`type`, where `type` is `note` or a hyphenated artifact kind. Artifacts add
`status_label`/`url`.

| `detail` | Notes get | Artifacts get |
|---|---|---|
| `summary` (default) | bounded `content_preview` + full-body `char_count` | `created_at` + `generation_prompt` (`null` if none) |
| `full` | the whole `content` | same as summary |
| `compact` | `id`/`title`/`type`/`status_label`/`created_at` | same |

`kind` filters to one `type`. `item` fetches one note-or-artifact by ref as a **1-element
list**, always with the note's full `content` — or, for an artifact, its `generation_prompt`.

`studio_download` targets either by `artifact` (name-or-id ref) **or** by `artifact_type`
(+ `artifact_id` for a specific one, else latest).

`studio_retry`: task_id == artifact_id.

`studio_rename` / `studio_delete` are cross-type — they resolve a ref against the merged list
and act on whichever it is.

### Research (4)

| Tool | Params |
|---|---|
| `research_start` | `notebook`, `query`, `source`, `mode` → returns `poll_task_id` |
| `research_status` | `notebook`, `poll_task_id?`, `include_report?`, `report_max_chars?`, `source_limit?`, `source_offset?` |
| `research_import` | `notebook`, `poll_task_id?`, `max_sources?`, `cited_only?`, `allow_duplicate?` |
| `research_cancel` | `notebook`, `poll_task_id` |

`poll_task_id` is the **one** id that status/import/cancel drive off. The old `task_id` /
`run_id` names are deprecated aliases, removed in v0.9.0 — accept them with a deprecation
note in v1.

`research_status` omits the report and per-source `report_markdown` unless
`include_report`. It **always** returns `status_code` and `termination_reason`:

- `termination_reason` ∈ `no_results` | `cancelled` | `unknown` — these partition the coarse
  `failed`, alongside `completed` | `in_progress`.
- **Both are `null`** when the poll carries no backend code (a `no_research` / `not_found`
  response). That is a third state, not an error.
- Adds `reason_message` / `hint` when the run did not succeed — so an empty Drive search is
  distinguishable from a real error.

`research_import` is timeout-tolerant and **idempotent for URL-addressed entries when the
baseline snapshot succeeds**. `cited_only` narrows to report-cited sources; `max_sources`
caps the count; `allow_duplicate` re-adds sources already present instead of skipping them as
`already_present`.

`research_cancel` sends the cancel unless the run is already terminal → `cancel_requested`.

### Sharing (4)

| Tool | Params |
|---|---|
| `share_status` | `notebook` — READ_ONLY |
| `share_set_access` | `notebook`, `public?`, `view_level?`, `confirm` |
| `share_set_user` | `notebook`, `email`, `permission?`, `notify?`, `message?`, `confirm` |
| `share_remove_user` | `notebook`, `email`, `confirm` — DESTRUCTIVE |

`share_status` returns `is_public`, `access`, `share_url`, `shared_users` (enums as string
labels), `max_individuals_share_limit` (the enforced collaborator cap), and
`is_public_sharing_allowed` (the tenant policy gate). **`view_level` is omitted** — the read
API cannot report it; do not guess. Both limit fields are `null` when the backend made no
claim, so test `is_public_sharing_allowed === false` for a real denial.

`share_set_access`: `view_level` ∈ `full` | `chat`, echoed back only when set. `confirm`
gates widening restricted → public.

`share_set_user`: `permission` ∈ `editor` | `viewer`; `notify` defaults **false**; `confirm`
gates **every** grant.

### Server (1)

| Tool | Params |
|---|---|
| `server_info` | `include_account?` |

Returns version + local auth health. `include_account=true` adds an `account` block: signed-in
identity (`email`, `authuser`), notebook/source limits, the subscription `tier` (opaque enum —
`1` Free, `2` Pro, `null` on legacy responses), and the global `output_language` for quota
pacing and language context.

`output_language` is `null` with `output_language_is_default: true` when the account uses
NotebookLM's default rather than an explicit code — best effort.

Identity is **network-free** from the profile; the quota fields need a live session.

`email` is real account PII and is returned **only** under this opt-in flag.

## Errors

Project `internal/app/errors.Classify` onto `CODE: message` content strings. Never surface an
obfuscated RPC id or a raw numeric gRPC code — MCP output goes straight into an LLM context
where volatile internal detail is noise at best and a leaked-detail vector at worst.

Message truncation is **300 characters**. This is why error messages that carry an
instruction must **front-load the action**: a realistic URL in the narrative pushed the
closing instruction past the cut in the original.

## Remote deployment

The HTTP transport can run as a remote connector reachable from Claude Code, Claude Desktop,
claude.ai (web and mobile), and ChatGPT (Developer Mode).

- **Claude Code / Desktop** → the static `NOTEBOOKLM_MCP_TOKEN` bearer in an `Authorization`
  header. **v1.**
- **claude.ai and ChatGPT connectors** → self-hosted OAuth. **v1.1** — the bearer path ships
  first because it covers the desktop clients and needs no OAuth server.

Docker + tunnel sidecar (Cloudflare or Tailscale Funnel) in doc 13. A tunnel gives HTTPS with
no public IP, no open ports, and no TLS certificate to manage.

Remote file transfer: a download tool cannot write to the caller's filesystem, so the remote
mode serves generated files over short-lived signed links from the server's own file routes
(port `mcp/_filelink.py` and `mcp/_fileroutes.py`). Links are single-use and expire.

## Go SDK sketch

```go
func main() {
    logging.ToStderr()          // FIRST — before anything can touch stdout

    cfg := parseFlags()
    client, err := notebooklm.FromStorage(ctx, notebooklm.WithProfile(cfg.Profile))
    if err != nil { fatal(err) }
    defer client.Close(context.WithoutCancel(ctx))

    srv := mcp.NewServer(&mcp.Implementation{
        Name: "notebooklm", Version: buildinfo.Version,
    }, nil)

    tools.RegisterNotebooks(srv, client)
    tools.RegisterSources(srv, client)
    tools.RegisterChat(srv, client)
    tools.RegisterNotes(srv, client)
    tools.RegisterStudio(srv, client)
    tools.RegisterResearch(srv, client)
    tools.RegisterSharing(srv, client)
    tools.RegisterMeta(srv, client)

    switch cfg.Transport {
    case "stdio":
        srv.Run(ctx, &mcp.StdioTransport{})
    case "http":
        serveHTTP(ctx, srv, cfg)   // bearer + fail-closed bind guard
    }
}
```

One tool registration, for shape:

```go
type SourceListArgs struct {
    Notebook string `json:"notebook" jsonschema:"the notebook id or unique prefix"`
    Status   string `json:"status,omitempty" jsonschema:"enum=unknown,enum=processing,enum=ready,enum=error,enum=preparing"`
    Label    string `json:"label,omitempty"`
    Detail   string `json:"detail,omitempty" jsonschema:"enum=full,enum=compact"`
    Limit    int    `json:"limit,omitempty"`
    Offset   int    `json:"offset,omitempty"`
}

mcp.AddTool(srv, &mcp.Tool{
    Name:        "source_list",
    Description: "List a notebook's sources with status and Drive health.",
    Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
}, func(ctx context.Context, req *mcp.CallToolRequest, args SourceListArgs) (
    *mcp.CallToolResult, any, error,
) {
    out, err := app.SourceList(ctx, client, args.toRequest())
    if err != nil { return mcpErr(err), nil, nil }
    return nil, out, nil
})
```

The `status` enum must be **generated from the `SourceStatus` label map**, not hand-written —
the Python original needed a dedicated parity test because its MCP `Literal` could not be
derived. In Go, generate the schema from the map at init and the test becomes unnecessary.
