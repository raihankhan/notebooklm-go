# Feature 4 — Templates, personas, and source bundles

> Spec part of [docs/15-features-overview.md](../15-features-overview.md).

## Goal

Make prompt engineering a versioned, shareable asset. Templates, personas, and source bundles live under `~/.notebooklm/templates/`, `~/.notebooklm/personas/`, `~/.notebooklm/bundles/`. They are invocable by name from the CLI / MCP / REST, and they can be pushed to / pulled from a git workspace (feature #7) for team sharing.

## User stories

1. A user creates `~/.notebooklm/templates/podcast-deep-dive.md` once. Every subsequent `notebooklm generate audio --template podcast-deep-dive` uses it; the team can `git pull` it.
2. A user creates `~/.notebooklm/personas/research-analyst.md`. They run `notebooklm chat configure --persona-file research-analyst` and every `ask` afterward uses that persona.
3. A user creates `~/.notebooklm/bundles/rfc-set/` with a `bundle.yaml` referencing a curated set of URLs. They run `notebooklm source add --bundle rfc-set -n nb1` to ingest them all.
4. A team shares `~/.notebooklm/templates/` via a git-backed workspace (feature #7). New team members clone the repo and run `notebooklm workspace sync` to materialize.

## CLI surface

Three new top-level groups, sharing an identical layout:

```
notebooklm template
  list   show <name>   create <name> [--from-file <path>]   edit <name>   remove <name> [--yes]
notebooklm persona
  list   show <name>   create <name>   edit <name>   remove <name> [--yes]   set-default <name>
notebooklm bundle
  list   show <name>   create <name> [--from <sources.yaml>]   add <name> -n <notebook>   refresh <name>   remove <name>
```

Plus integration with existing commands:

```
notebooklm generate audio|video|quiz|flashcards|report|... --template <name>
notebooklm chat configure --persona-file <name> | --persona-default
notebooklm source add --bundle <name> -n <notebook> [--refresh]
```

### Template file format

A template is a Markdown file with optional frontmatter:

```markdown
---
name: podcast-deep-dive
description: Two-host deep dive, conversational tone, target 8 minutes.
applies_to: [audio]               # one of: audio, video, report, quiz, flashcards, infographic, mind_map, slide_deck
format: deep-dive
length: default
language: en
---

Focus on the following angles:

{{#each sources}}
- {{title}} ({{kind}})
{{/each}}

Topic: {{vars.topic}}

Style guide:
- Conversational, two-host dynamic
- ...
```

The body is a Go `text/template` rendered with these inputs: `sources[]` (the selected sources for the operation), `vars` (the CLI's `--vars` key/value pairs and the global `NOTEBOOKLM_*` env vars), `notebook` (title, id, source count). Interpolation is bounded — a template that references an undefined var fails closed with a typed error, never silently emits `""`.

`applies_to` is enforced: `notebooklm generate audio --template podcast-deep-dive` works; `notebooklm generate quiz --template podcast-deep-dive` fails with `template_not_applicable`.

### Persona file format

A persona is plain Markdown with frontmatter:

```markdown
---
name: research-analyst
description: Concise, citation-heavy, neutral tone.
mode: default                     # default | learning-guide | concise | detailed
response_length: default
goal: 1                           # wire ChatGoal value (1=default, 2=custom, 3=learning-guide)
---

You are a research analyst. You answer with citations whenever possible.
...
```

`chat configure --persona-file research-analyst` sets the active persona globally on the notebook; subsequent `ask` calls use it. The persona body is concatenated into the chat configuration payload at the slot the existing RPC expects (per Python `chat_configure.py`).

### Bundle file format

A bundle is a directory with a `bundle.yaml`:

```yaml
name: rfc-set
description: Curated RFCs we always include in networking notebooks.
refresh_strategy: refresh-on-add       # one of: refresh-on-add, no-refresh
deduplication: skip-already-present     # matches the existing source-add idempotency

sources:
  - url: https://www.rfc-editor.org/rfc/rfc793.txt
    title: "RFC 793 — TCP"
  - url: https://www.rfc-editor.org/rfc/rfc9293.txt
    title: "RFC 9293 — TCP (2022)"
  - url: https://www.rfc-editor.org/rfc/rfc9000.txt
    title: "RFC 9000 — QUIC"
```

`notebooklm bundle add rfc-set -n nb1` adds every entry as a source via the existing `Sources.AddURL`. `--refresh` triggers `source.refresh` on every existing source that matches the bundle's URL set.

## REST surface

| Method | Path | Status | Limiter |
|---|---|---|---|
| `GET` | `/v1/templates` | 200 | — |
| `POST` | `/v1/templates` | 201 | — |
| `GET` | `/v1/templates/{name}` | 200 | — |
| `DELETE` | `/v1/templates/{name}` | 204 | — |
| `GET` | `/v1/personas` | 200 | — |
| `POST` | `/v1/personas` | 201 | — |
| `DELETE` | `/v1/personas/{name}` | 204 | — |
| `GET` | `/v1/bundles` | 200 | — |
| `POST` | `/v1/bundles` | 201 | — |
| `POST` | `/v1/bundles/{name}/add` | 202 | source-mutation |

## MCP tools

| Name | Purpose | Confirmation |
|---|---|---|
| `template_list` | List installed templates. | no |
| `template_show` | Print the resolved template body. | no |
| `template_apply` | Run a `generate` with a template by name. | yes |
| `persona_list` | List installed personas. | no |
| `persona_set` | Set the active persona on a notebook. | yes |
| `bundle_list` | List installed bundles. | no |
| `bundle_add` | Add a bundle's sources to a notebook. | yes |

## Public SDK additions

```go
type Template struct { Name, Description string; AppliesTo []string; Body string }
type Persona  struct { Name, Description, Body string; Mode string; ResponseLength *int; Goal int }
type Bundle   struct { Name, Description string; Sources []BundleSource; RefreshStrategy string; Deduplication string }

type Templates struct{ client *Client }
func (t *Templates) List(ctx) ([]Template, error)
func (t *Templates) Get(ctx, name string) (*Template, error)
func (t *Templates) Apply(ctx, opts TemplateApplyOptions) (*ArtifactGenerationStatus, error)

type Personas struct{ client *Client }
// similar shape

type Bundles struct{ client *Client }
func (b *Bundles) Add(ctx, name, notebookID string, opts BundleAddOptions) ([]Source, error)
```

`Templates.Apply` composes `Artifacts.Generate*` with the template's body as the `instructions` payload; no new RPC.

## Data model under `~/.notebooklm/`

| Path | Format | Purpose |
|---|---|---|
| `~/.notebooklm/templates/<name>.md` | Markdown + frontmatter | one file per template |
| `~/.notebooklm/personas/<name>.md` | Markdown + frontmatter | one file per persona |
| `~/.notebooklm/bundles/<name>/bundle.yaml` | YAML | bundle manifest |
| `~/.notebooklm/bundles/index.json` | JSON | `{ bundles: [...] }` for fast listing |

Atomic writes per `internal/atomicio`. Templates and personas are user-edited files — the CLI never modifies them in place. `template create --from-file` copies an arbitrary file into the templates folder; `template edit` opens `$EDITOR`.

## Protocol implications

**No new RPC.** Every template / persona / bundle operation calls an existing method:

| Operation | SDK call | Python original |
|---|---|---|
| `template apply` | `client.Artifacts.GenerateAudio(..., opts.Instructions = templateBody)` | `cli/generate_cmd.py::audio` → `_artifacts.generate_audio` |
| `persona set` | `client.Chat.Configure(...)` | `cli/configure_cmd.py::configure` → `_chat.configure` |
| `bundle add` | N × `client.Sources.AddURL(...)` | `cli/source_cmd.py::add` → `_sources.add_url` |
| `bundle refresh` | N × `client.Sources.Refresh(...)` | `cli/source_cmd.py::refresh` → `_sources.refresh` |

The only "new" piece is the `text/template` renderer for templates, which is pure client-side.

## Dependencies on Phases 14–18

| Phase | What this feature needs |
|---|---|
| **14 (this phase)** | Independent. |
| 15 | `bundle add` can reference local search index hits (#5). |
| 16 | Templates and personas live in the workspace's `.notebooklm-workspace/templates/` for team sharing (#7). |
| 17 | Plugins can contribute custom template kinds via the plugin SDK. |
| 18 | The skill teaches the agent when to invoke a template vs raw instructions. |

## Tests

### Unit

| Test | What it asserts |
|---|---|
| `TestTemplateFrontmatterParse` | Valid frontmatter parses; missing `applies_to` is rejected. |
| `TestTemplateRender` | `{{vars.topic}}`, `{{#each sources}}` interpolates correctly. |
| `TestTemplateAppliesToEnforcement` | `--template` rejected when `applies_to` does not match the command. |
| `TestTemplateRenderFailure` | Undefined var fails closed (typed error, no partial render). |
| `TestPersonaSet` | `chat configure --persona-file` produces the documented wire payload. |
| `TestBundleAddDedup` | `deduplication: skip-already-present` skips re-add. |
| `TestBundleRefresh` | `--refresh` calls `Sources.Refresh` for each existing source. |
| `TestTemplateListOrdering` | Templates list is alphabetical; `latest_used` per notebook is sticky. |
| `TestAtomicWrite` | A crashed write leaves the previous template intact. |

### Cassette

`bundle add` replayed against N cassettes (one per source URL). Asserts: every source is added; dedup works against the cassette's `already_present` response.

### E2E

A live `template apply` against a real notebook. Asserts: artifact is generated with the rendered instructions; the rendered instructions are visible in `artifact get-prompt <id>`.

## Acceptance criteria

1. `notebooklm template create podcast-deep-dive` opens `$EDITOR`; on save, the template is listed.
2. `notebooklm generate audio --template podcast-deep-dive` produces an audio artifact whose `generation_prompt` equals the rendered template.
3. A template referenced by `--template` whose `applies_to` does not match the command exits 1 with `template_not_applicable`.
4. `notebooklm chat configure --persona-file research-analyst` sets the persona; a subsequent `ask` uses it.
5. `notebooklm bundle add rfc-set -n nb1` adds every entry; `--refresh` re-fetches existing entries.
6. `notebooklm template remove <name>` and `bundle remove <name>` require `--yes` non-interactively.
7. The MCP `template_apply` and `bundle_add` tools require `confirm: true`.
8. Templates and personas survive `notebooklm upgrade` (files are user-owned; the upgrade never overwrites them).
