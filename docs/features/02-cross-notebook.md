# Feature 2 — Cross-notebook ask and synth

> Spec part of [docs/15-features-overview.md](../15-features-overview.md).

## Goal

Let a configured "Master Brain" notebook be queried as a single grounded engine, and let ad-hoc queries span N notebooks with merged citations. NotebookLM itself locks chat to one notebook at a time; this feature composes the existing `Chat.Ask` primitive per notebook to give the user the cross-notebook answer that the web UI does not provide.

## User stories

1. A user maintains a "Master Brain" notebook that holds the team's accumulated notes. They set `NOTEBOOKLM_BRAIN_NOTEBOOK=<uuid>` and run `notebooklm query "What's our incident response procedure for X?"` to get a grounded answer without naming the notebook each time.
2. A user asks a one-off question that spans three notebooks: `notebooklm ask "Compare our pricing models" --across nb1,nb2,nb3 --json`. They get one answer whose `[1]` `[2]` `[3]` citations point into whichever notebook each fact came from.
3. A user runs `notebooklm synth "AI safety arguments" --notebooks nb1,nb2,nb3 --output notebook "Synth: AI safety"` — a fresh notebook is created, the relevant sources from the three notebooks are copied in, a one-shot ask is performed, the answer is saved as a note, and the new notebook id is returned.
4. An agent loop calls `ask --across` over hundreds of notebooks (capped by the per-tenant concurrency limit) to produce a corporate knowledge base answer in one round-trip.

## CLI surface

Extends `ask` with two new flags, plus two new top-level commands:

```
notebooklm ask "..." [--across <notebook-id>,…] [--merge-citations] [--max-per-notebook <n>]
notebooklm synth <topic> --notebooks <id>,… [--output <new-notebook-title>] [--save-as-note]
notebooklm query <question> [--brain <notebook-id>]   # uses NOTEBOOKLM_BRAIN_NOTEBOOK by default
```

### `ask --across`

`--across` is a comma-separated list of notebook ids or partial ids (resolved via `internal/app/resolve`). The command fans out `Chat.Ask` in parallel per notebook, then merges:

- **Answer text** — concatenated, prefixed with the notebook title, dedup-ed by similarity threshold (configurable via `--merge-similarity`; default 0.85).
- **References** — `[1]` `[2]` … renumbered globally, with the source's notebook id baked into the returned reference.

`--json` envelope:

```json
{
  "question": "Compare our pricing models",
  "answer": "Notebook A says X. [1] Notebook B says Y. [2]",
  "conversation_id": null,
  "per_notebook": [
    {"notebook_id": "nb1", "answer": "...", "references": [...]},
    {"notebook_id": "nb2", "answer": "...", "references": [...]}
  ],
  "references": [
    {"source_id": "src-1", "notebook_id": "nb1", "citation_number": 1, "cited_text": "..."},
    {"source_id": "src-2", "notebook_id": "nb2", "citation_number": 2, "cited_text": "..."}
  ],
  "success": true
}
```

Without `--json`, the printed answer is the merged string; `--merge-citations` is implicit (off would print each per-notebook answer separately).

`--max-per-notebook` (default 5) caps the per-notebook top-K retrieval the underlying NotebookLM does, reducing token cost on huge corpora.

### `synth`

`notebooklm synth <topic>` is the higher-level helper. It:

1. Creates a new notebook with `--output` (default `Synth: <topic>`).
2. Selects top-K sources per `--notebooks` (default K=10, configurable). Selection is by recency × word count × `synth --include-labels <label-id>,…` filter.
3. Adds the selected sources to the new notebook.
4. Waits for sources to be `ready`.
5. Calls `ask` once on the new notebook.
6. Optionally saves the answer as a note (`--save-as-note`).
7. Returns the new notebook id; the user is now expected to drive further exploration manually.

### `query`

Thin wrapper: `notebooklm query "..."` ≡ `notebooklm ask "..." -n "$NOTEBOOKLM_BRAIN_NOTEBOOK"`. The `--brain` flag overrides the env var. If neither is set, `query` fails with an actionable error pointing at the env var.

## REST surface

| Method | Path | Status | Limiter |
|---|---|---|---|
| `POST` | `/v1/notebooks/{notebook_id}/chat` (existing) | 200 | chat |
| `POST` | `/v1/notebooks/chat/across` | 200 | chat |
| `POST` | `/v1/notebooks/synth` | 202 | research |

`/chat/across` takes `{ question, notebook_ids, max_per_notebook, merge_citations, merge_similarity }`. `/synth` is long-running; returns 202 + `run_id`; poll `/v1/research/{run_id}` (existing route, reused with a `kind: synth` discriminant).

## MCP tools

| Name | Purpose | Confirmation |
|---|---|---|
| `chat_ask_across` | Cross-notebook ask. | no |
| `chat_synth` | One-shot synth into a new notebook. | yes (creates a notebook) |
| `chat_query` | Shorthand for the configured Master Brain. | no |

## Public SDK additions

```go
type CrossNotebookResult struct {
    Question    string
    Answer      string
    PerNotebook []PerNotebookAnswer
    References  []CrossReference
}
type PerNotebookAnswer struct { NotebookID, Answer string; References []Reference }
type CrossReference struct { SourceID, NotebookID, CitedText string; CitationNumber int }

type SynthOptions struct { Topic string; NotebookIDs []string; OutputTitle string; TopK int; IncludeLabels []string; SaveAsNote bool }

func (c *Chat) AskAcross(ctx, question string, notebookIDs []string, opts AskAcrossOptions) (*CrossNotebookResult, error)
func (c *Chat) Synth(ctx, opts SynthOptions) (*Notebook, error)
func (c *Chat) Query(ctx, question string) (*AskResult, error) // uses cfg.BrainNotebook
```

`Chat.AskAcross` fans out `Chat.Ask` per notebook under the existing concurrency limits; merges locally.

## Data model under `~/.notebooklm/`

| Path | Format | Purpose |
|---|---|---|
| `~/.notebooklm/config.json` (existing) | JSON | adds `brain_notebook: <id> \| null`, `cross_notebook_max_per_notebook: <int>`, `cross_notebook_merge_similarity: <float>`. Read on every `query` / `ask --across`. |

No new folders. The cross-notebook result of `synth` lives in the new notebook (sources copied, note saved) — the user owns that notebook as if they had created it by hand.

## Protocol implications

**No new RPC.** Every operation is `Chat.Ask` against an existing notebook. The cross-notebook merge is purely client-side:

- `ask --across` → N parallel `Chat.Ask` + local string assembly.
- `synth` → `Notebooks.Create` + N parallel `Sources.AddURL` (with the source content read locally, not via `Sources.AddURL`'s URL-fetch path) + `Chat.Ask` + optional `Notes.Create`.

The `Sources.AddURL` call uses the existing per-source upload path (Phase 6). The merged answer is **not** written back to NotebookLM unless the user explicitly `--save-as-note`s it (and then only the merged answer goes to a single note).

Cited reusable RPCs (per Python original):

| Operation | Python source |
|---|---|
| `Chat.Ask` | `cli/ask_cmd.py::ask` → `_chat.py::Ask.ask` → `_web/chat.py::ask_question` |
| `Notebooks.Create` | `cli/notebook_cmd.py::create` → `_notebooks.py::create` → `_web/notebooks.py::create_notebook` |
| `Sources.AddURL` | `cli/source_cmd.py::add` → `_sources.py::AddURL` → `_web/sources/add.py` |
| `Notes.Create` | `cli/note_cmd.py::create` → `_notes.py::Create` → `_web/notes.py::create_note` |

## Dependencies on Phases 14–18

| Phase | What this feature needs |
|---|---|
| **14 (this phase)** | Independent. |
| 15 | `ask --across --grounded` uses the local search index (#5) to narrow per-notebook sources before fanning out. |
| 16 | The workspace feature can pin a Master Brain per project via `.notebooklm-workspace/config.yaml`. |
| 17 | A plugin can add a custom merge strategy (e.g., LLM-based dedup instead of similarity). |
| 18 | The skill teaches the agent when to use `query` vs `ask` vs `ask --across` vs `synth`. |

## Tests

### Unit

| Test | What it asserts |
|---|---|
| `TestAskAcrossMerge` | Two per-notebook answers are merged by similarity; references are renumbered. |
| `TestAskAcrossCitationStability` | A reference with `notebook_id: nb1, source_id: src-1` survives merge with stable numbering. |
| `TestAskAcrossPartialFailure` | One notebook fails; the others succeed; the envelope reports the failure without dropping successful answers. |
| `TestSynthNewNotebook` | `synth` creates a notebook, copies sources, runs ask, returns the new id. |
| `TestQueryDefaultsToBrain` | `NOTEBOOKLM_BRAIN_NOTEBOOK` is honored; `--brain` overrides. |
| `TestQueryMissingBrain` | Without the env var, `query` exits 1 with the documented error. |
| `TestSynthCopiesOnlySelectedSources` | `synth` respects `--top-k` and `--include-labels` filters. |
| `TestMergeSimilarityConfig` | The config knob `cross_notebook_merge_similarity` is honored. |

### Cassette

A pre-recorded `ask --across` against two notebooks: one cassette per notebook's `Chat.Ask` response. The merge is exercised against the cassette outputs.

### E2E

A live `synth` against a real notebook pair: `--notebooks nb1,nb2 --output "Synth: test"`. Asserts the new notebook exists, has the expected sources, and the saved note contains the merged answer.

## Acceptance criteria

1. `notebooklm query "..."` with `NOTEBOOKLM_BRAIN_NOTEBOOK` set runs a single `Chat.Ask` and prints the answer; without the env var, exits 1 with the documented message.
2. `notebooklm ask "..." --across nb1,nb2 --json` returns a single envelope with `answer`, `per_notebook`, and `references` populated; the citation numbers in `answer` correspond to the `references` array order.
3. A failure in one notebook's `Chat.Ask` does not drop the others; the envelope reports per-notebook state.
4. `notebooklm synth "topic" --notebooks nb1,nb2 --output "X"` creates a notebook named `X`, copies sources per `--top-k`, asks, optionally saves a note, and returns the new id within the configured timeout.
5. The `--max-per-notebook` and `--merge-similarity` knobs are respected.
6. The REST `/v1/notebooks/chat/across` route returns the same shape as the CLI's `--json` envelope.
7. The MCP `chat_ask_across` tool returns the same shape as the CLI; `chat_synth` refuses without `confirm: true` (creates a notebook).