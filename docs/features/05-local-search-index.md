# Feature 5 — Local-first search index

> Spec part of [docs/15-features-overview.md](../15-features-overview.md).

## Goal

Build a local SQLite FTS5 index over every notebook's sources + notes, so the user can search their own corpus **offline, instantly, and without burning Google quota**. A `--grounded` mode narrows chat to the top-K matched sources, hybridizing local retrieval with NotebookLM's grounded answer.

The index is client-side. It never writes to NotebookLM; it never leaves `~/.notebooklm/`. A user with thousands of indexed sources gets sub-second search on commodity hardware.

## User stories

1. A user has 12 notebooks with thousands of sources. They run `notebooklm index build`, walk away, and later run `notebooklm search "incident response"` and get ranked snippets in milliseconds.
2. A user runs `notebooklm search "incident response" --grounded -n nb1 --ask`. The local index picks the top-K sources; chat is constrained to those; the answer is grounded + cheaper than a full-corpus chat.
3. A user runs `notebooklm search "..." --notebooks nb1,nb2` to scope a search to specific notebooks.
4. A CI step runs `notebooklm index build --quiet` after a build; a downstream CI step runs `notebooklm search` to fail the build on policy violations (e.g., forbidden terms).
5. The index survives a `notebooklm upgrade` and a `notebooklm logout`; it is regenerated only when the user asks.

## CLI surface

New top-level group:

```
notebooklm index
  build [--full | --incremental] [--quiet]   # full rebuild by default
  update                                       # incremental; sources that changed are re-indexed
  status                                       # size, document count, last-built timestamp
  show <doc-id>                                # print indexed source/fragment by id
  drop [--yes]                                 # delete ~/.notebooklm/index/
  search <query>                               # the high-traffic command (see below)
```

Plus, extending the existing commands:

```
notebooklm search <query> [--notebooks <id>,…] [--kind <source|note|all>] [--limit <n>]
                          [--grounded] [--ask] [--json]
notebooklm ask "..." --grounded-sources [--limit <n>]
```

### `search`

`search` is the high-traffic command. Default mode is BM25 ranking with snippets; `--json` returns the structured form:

```json
{
  "query": "incident response",
  "elapsed_ms": 12,
  "mode": "bm25",
  "count": 8,
  "results": [
    {
      "doc_id": "src-abc123#frag-7",
      "rank": 1,
      "score": 8.41,
      "snippet": "...the on-call rotation requires <b>incident response</b> within 15 minutes...",
      "source": {"id": "src-abc123", "title": "On-call runbook", "notebook_id": "nb-xyz", "kind": "web_page"},
      "highlights": [{"start": 42, "end": 58}]
    }
  ],
  "success": true
}
```

Flags:

| Flag | Default | Meaning |
|---|---|---|
| `--notebooks` | all | comma-separated notebook ids; partial ids accepted |
| `--kind` | `all` | `source` \| `note` \| `all` |
| `--limit` | 20 | top-K results |
| `--grounded` | `false` | narrow subsequent chat to top-K sources (if `--ask` is also set) |
| `--ask` | `false` | after the local search, run `notebooklm ask` against the matched sources |
| `--json` | false | structured envelope above |

When `--ask` is set, the command becomes a hybrid: local search → matched sources → chat constrained to those sources. The cited answers still come from NotebookLM, but the corpus is narrowed to what's relevant, which is cheaper and faster.

`notebooklm ask "..." --grounded-sources [--limit <n>]` is the shorthand for `search --ask` with a query that's the question itself.

## REST surface

| Method | Path | Status | Limiter |
|---|---|---|---|
| `POST` | `/v1/index/build` | 202 | index |
| `POST` | `/v1/index/update` | 202 | index |
| `GET` | `/v1/index` | 200 | — |
| `GET` | `/v1/index/search` | 200 | — |

`/v1/index/build` returns 202 + `job_id`; poll `/v1/index/jobs/{job_id}`. The build is the longest-running operation the index exposes (linear in source count) and uses the existing `NOTEBOOKLM_SERVER_GENERATION_CONCURRENCY` slot.

## MCP tools

| Name | Purpose | Confirmation |
|---|---|---|
| `index_build` | Full rebuild. | no |
| `index_update` | Incremental update. | no |
| `index_status` | Status snapshot. | no |
| `search` | Run a query. | no (read-only) |
| `chat_ask_grounded` | Hybrid: search + ask. | no (read-only) |

## Public SDK additions

A new namespace `notebooklm.Index` plus a new method on `notebooklm.Chat`:

```go
type Index struct{ client *Client }
func (i *Index) Build(ctx, opts IndexBuildOptions) (*IndexJob, error)
func (i *Index) Update(ctx) (*IndexJob, error)
func (i *Index) Status(ctx) (*IndexStatus, error)
func (i *Index) Drop(ctx) error

type SearchResult struct{ DocID, Snippet string; Source SearchSource; Highlights []HL; Rank, Score int }
type SearchOptions struct { Notebooks []string; Kind string; Limit int }
func (i *Index) Search(ctx, query string, opts SearchOptions) ([]SearchResult, error)

type Chat struct { /* existing */ }
func (c *Chat) AskGrounded(ctx, notebookID, question string, opts AskGroundedOptions) (*AskResult, error)
```

`AskGrounded` composes the existing `Index.Search` and `Chat.Ask` with source scoping.

## Data model under `~/.notebooklm/`

| Path | Format | Purpose |
|---|---|---|
| `~/.notebooklm/index/` | dir | the SQLite database + sidecars |
| `~/.notebooklm/index/corpus.db` | SQLite (FTS5) | the index |
| `~/.notebooklm/index/manifest.json` | JSON | `{ schema_version, doc_count, last_built_at, last_updated_at }` |
| `~/.notebooklm/index/excluded.json` | JSON | notebook ids / source ids the user opted out of indexing |

The SQLite database has two virtual tables:

```sql
CREATE VIRTUAL TABLE sources_fts USING fts5(
    doc_id UNINDEXED,         -- "{source_id}#{fragment_id}"
    source_id UNINDEXED,
    notebook_id UNINDEXED,
    kind UNINDEXED,           -- 'source' | 'note'
    title,
    body,
    tokenize = 'porter unicode61 remove_diacritics 2'
);

CREATE TABLE doc_offsets (                  -- sidecar for snippet reconstruction
    doc_id TEXT PRIMARY KEY,
    source_id TEXT NOT NULL,
    notebook_id TEXT NOT NULL,
    fragment_start INTEGER, fragment_end INTEGER,
    char_offset INTEGER, char_length INTEGER
);
```

A "fragment" is a 1-KiB chunk of source content. Sources > 64 KiB are split into multiple fragments; each is a separate FTS5 row. Snippets are reconstructed from the sidecar offsets.

Atomic writes per `internal/atomicio`. The index is built into a `corpus.db.tmp`, then atomically renamed to `corpus.db`. The previous database is preserved as `corpus.db.bak` for one build cycle.

### Migrations

The `manifest.schema_version` field tracks the database schema. The first release ships schema version 1. A migration increments the field and rewrites the database in place; the old DB is the rollback target. Migrations are tested in CI against fixtures of every prior schema version.

### Excluded sources

Some sources should never be indexed (e.g., a source containing secrets). Users can mark sources as excluded with `notebooklm source add --no-index` or `notebooklm index exclude <source-id>`. Excluded sources are stored in `excluded.json`; `index build` skips them.

## Protocol implications

**No new RPC.** The index reads notebook sources via the existing `Sources.List` and `Sources.GetFulltext`, and notes via `Notes.List`. The search itself is local SQLite FTS5 (no RPC). The grounded-ask hybrid calls `Chat.Ask` with the existing source-scoping flag.

Cited reusable calls:

| Index operation | SDK call | Python original |
|---|---|---|
| `Index.Build` | iterate `Sources.List(...)` + `Sources.GetFulltext(...)` per source; write to SQLite | `cli/source_cmd.py::list` → `cli/source_cmd.py::fulltext` |
| `Search` | local SQLite query | n/a — client-only |
| `AskGrounded` | `Chat.Ask` with `-s <matched-source-ids>` | `cli/ask_cmd.py::ask` with `-s` |

The transport used for ingestion is CLI / MCP / REST depending on the user; the index never cares.

### SQLite: why pure-Go

`modernc.org/sqlite` is a pure-Go translation of SQLite (no CGo). It keeps the build static (no `gcc` required), simplifies cross-compilation, and avoids `glibc` pinning problems on Linux. The FTS5 module is built in; the `porter` and `unicode61` tokenizers are available.

The single dependency on `modernc.org/sqlite` is justified by:

- No CGo → static binary → matches `docs/01-overview.md` size and ship goals.
- FTS5 is required for BM25 ranking; rolling our own is out of scope.

## Dependencies on Phases 14–18

| Phase | What this feature needs |
|---|---|
| 14 | `search` is a valid step in `pipeline run`. |
| **15 (this phase)** | Independent for the data plane itself. |
| 16 | `workspace policy` can pin an `index.exclude` pattern per repo. |
| 17 | The REPL has a `search` sub-command. Plugins can register custom search backends. |
| 18 | The skill exposes `search --grounded --ask` as the recommended "first thing to try" pattern. |

## Tests

### Unit

| Test | What it asserts |
|---|---|
| `TestIndexBuild` | Building from N sources populates `corpus.db` with N fragments; `manifest.json` reflects counts. |
| `TestIndexIncremental` | Re-running `Index.Update` after one source changed updates only that source's fragments. |
| `TestSearchRank` | BM25 ranking is stable; a known query returns the same top-3 across two builds. |
| `TestSearchSnippetReconstruction` | `start_char` / `end_char` correctly map snippet text back to a substring of the source body. |
| `TestSearchNotebooks` | `--notebooks` filter excludes sources from other notebooks. |
| `TestSearchKind` | `--kind source` excludes notes, vice versa. |
| `TestExcludeFile` | `--no-index` on `source add` makes the source invisible to `search`. |
| `TestMigration` | A schema-version-1 DB is upgraded to v2 without losing documents. |
| `TestAtomicRename` | A crash mid-`Index.Build` leaves `corpus.db` either fully old or fully new, never half-written. |
| `TestAskGrounded` | Hybrid search + ask returns an `AskResult` whose `references` are a subset of the matched sources. |

### Cassette

`Sources.List` and `Sources.GetFulltext` replayed against a cassette; `Index.Build` materializes a real SQLite database; `Search` queries it and asserts ranked results match expectations.

### E2E

Live `Index.Build` against a real notebook pair; live `Search --grounded --ask` against the same notebooks; asserts the chat returns an answer grounded in a subset of the corpus.

## Acceptance criteria

1. `notebooklm index build` over a notebook with 200 sources completes in under 5 minutes and yields a `corpus.db` of predictable size.
2. `notebooklm search "<query>"` returns ranked results in under 100 ms on a notebook with 10 K indexed fragments.
3. `notebooklm search "..." --grounded --ask -n nb1` runs a search, narrows the chat to the top-K sources, and returns a cited `AskResult`.
4. `notebooklm index update` after adding one source indexes only that source.
5. `notebooklm index drop` removes `~/.notebooklm/index/`. A subsequent `index build` recreates it.
6. A source marked `--no-index` is absent from `search` results.
7. The MCP `index_build` returns a job id when the build is long; polling reports progress; the `search` tool is read-only.
8. The binary remains static (no CGo) and under 40 MB.
