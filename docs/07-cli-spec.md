# 07 — CLI specification (Cobra)

Normative source: `../notebooklm-py/docs/cli-reference.md` (2009 lines) and
`../notebooklm-py/docs/cli-exit-codes.md`.

**Compatibility rule:** every command, subcommand, flag name, flag shorthand, choice value,
`--json` envelope key, and exit code must match the Python CLI. A user's existing shell
script must not need editing.

## Invocation

```
notebooklm [-p PROFILE] [--storage PATH] [--backend web|android]
           [-v|-vv] [--quiet] [--version] <command> [OPTIONS] [ARGS]
```

### Persistent flags

| Flag | Effect |
|---|---|
| `-p, --profile NAME` | named profile; overrides `NOTEBOOKLM_PROFILE` |
| `--storage PATH` | override the storage location; highest-precedence auth source |
| `--backend web\|android` | namespace backend; default `web`. `android` returns `ErrBackendUnavailable` in v1. |
| `-v, --verbose` | `-v` → INFO, `-vv` → DEBUG (count flag) |
| `--quiet` | suppress status output and INFO/WARN records; only errors survive. `--json` payloads still emitted. **Mutually exclusive with `-v`** — combining exits 2. |
| `--version` | print version and exit |

Auth-source precedence: `--storage` > `NOTEBOOKLM_AUTH_JSON` > active profile storage.

Notebook resolution, for every command exposing `-n/--notebook`:
`-n` flag > `NOTEBOOKLM_NOTEBOOK` env > active context (`notebooklm use`) > error.
Empty/whitespace env values count as unset.

### Cobra wiring notes

- Root uses `cobra.Group` to reproduce Click's `SectionedGroup` binned help. A test rejects
  any command not assigned to a group — same guard as the original.
- Every ID argument accepts a **unique partial prefix**, resolved against the corresponding
  list call. Ambiguity lists the candidates and exits 1. Implement once in
  `internal/app/resolve` for all five id kinds (notebook, source, artifact, note, label /
  collection).
- `--json` is a per-command flag (not persistent) — matching the original, where only
  commands that produce structured output accept it.
- `SilenceUsage: true` and `SilenceErrors: true` on every command; `internal/cli/errors.go`
  owns all output. Cobra's own usage dump on a runtime error is wrong for this CLI.
- Shell completion: `notebooklm completion bash|zsh|fish` via Cobra's generator, plus
  dynamic `ValidArgsFunction` completers for notebook/source/artifact ids. Completers **must
  swallow every failure** — a shell must never print a diagnostic during TAB completion.

## Exit codes

| Code | Meaning |
|---|---|
| `0` | success |
| `1` | user / application error — validation, auth, rate limit, network, config, any library error |
| `2` | system / unexpected error (a bug), **and** parse-time usage errors |
| `130` | SIGINT (128 + 2) |

Error → code mapping (must match exactly):

| Error | JSON `code` | Exit |
|---|---|---|
| rate limit | `RATE_LIMITED` | 1 |
| auth | `AUTH_ERROR` | 1 |
| validation | `VALIDATION_ERROR` | 1 |
| configuration | `CONFIG_ERROR` | 1 |
| network | `NETWORK_ERROR` | 1 |
| notebook limit / quota | `NOTEBOOK_LIMIT` | 1 |
| not found (incl. domain variants) | `NOT_FOUND` | 1 |
| artifact timeout | `ARTIFACT_TIMEOUT` | 1 |
| other library error | `NOTEBOOKLM_ERROR` | 1 |
| SIGINT | `CANCELLED` | 130 |
| anything else | `UNEXPECTED_ERROR` | 2 |
| parse-time usage error | `VALIDATION_ERROR` | 2 |
| parse-time other flag error | `VALIDATION_ERROR` | 1 |

### The `--json` error envelope

```json
{ "error": true, "code": "RATE_LIMITED", "message": "…", "retry_after": 30 }
```

- Emitted on **stdout**; stderr stays empty in JSON mode.
- Extra fields per error: `retry_after`, `id`, `notebook_id`, and `method_id` only when
  `-v` is set.
- Automation should branch on `code` or the exit code, never on `message`.
- **Parse-time errors are wrapped too.** A bad flag with `--json` must still produce a
  parseable document, with the exit code preserved (2 for usage, 1 for others). Implement by
  intercepting Cobra's `FlagErrorFunc` and `Execute()`'s returned error at the root.

### Two intentional deviations

| Command | Behavior |
|---|---|
| `source stale --exit-on-stale` | inverted predicate: `0` = stale, `1` = fresh, for `if notebooklm source stale --exit-on-stale ID; then …` |
| `source wait` | three-way: `0` ready, `1` error, `2` timeout |

Without `--exit-on-stale`, `source stale` follows the standard convention and the verdict is
read from the JSON `stale`/`fresh` fields.

## Terminal theme

Colors follow the **CP AXTRA** palette. Defined once in `internal/cli/theme/theme.go`.

| Role | Palette color | Hex | RGB | Used for |
|---|---|---|---|---|
| Primary / headers / IDs | **CP AXTRA Blue** | `#306FC7` | 48, 111, 199 | table headers, notebook & artifact ids, command names in help, links |
| Warning / in-progress | **CP AXTRA Yellow** | `#F6C24A` | 246, 194, 74 | `processing` / `pending` status, spinners, `--dry-run` notices, deprecation notes |
| Success / ready | **Lotus Green** | `#43938F` | 67, 147, 143 | `ready` / `completed` status, success confirmations, `✓` |
| Error / destructive | **Makro Red** | `#DA3832` | 218, 56, 50 | errors, `failed` status, destructive-action confirmation prompts |

```go
package theme

import "github.com/charmbracelet/lipgloss"

var (
    Blue   = lipgloss.Color("#306FC7") // CP AXTRA
    Yellow = lipgloss.Color("#F6C24A") // CP AXTRA
    Green  = lipgloss.Color("#43938F") // Lotus
    Red    = lipgloss.Color("#DA3832") // Makro

    Header    = lipgloss.NewStyle().Foreground(Blue).Bold(true)
    ID        = lipgloss.NewStyle().Foreground(Blue)
    Success   = lipgloss.NewStyle().Foreground(Green)
    Warn      = lipgloss.NewStyle().Foreground(Yellow)
    ErrorText = lipgloss.NewStyle().Foreground(Red).Bold(true)
    Muted     = lipgloss.NewStyle().Faint(true)
)
```

Rules:

- Respect `NO_COLOR` and a non-TTY stdout (lipgloss auto-detects; assert it in a test).
- **All color goes to stderr in `--json` mode.** stdout must remain byte-clean JSON.
- Status→color mapping is a single function so CLI, tables, and progress lines cannot drift:
  `ready`/`completed` → Green · `processing`/`pending`/`in_progress`/`preparing` → Yellow ·
  `error`/`failed` → Red · `unknown`/`suggested` → Muted.
- A degraded Drive row appends `(drive: deleted)` to the Status cell in Yellow — **only** when
  the backend explicitly reported a non-healthy state. An unmappable code (`unknown`) is not
  flagged: it is not evidence of degradation, and the table must not cry wolf on protocol drift.

## Command tree

### Session

| Command | Notes |
|---|---|
| `login` | see doc 05 for the four auth paths and all flags |
| `use <id>` | set active notebook; verifies existence unless `--force`; `--json` → `{active_notebook_id, success, verified, notebook}` |
| `status` | current context; `--paths` shows configuration paths; `--json` |
| `clear` | clear active context |
| `completion bash\|zsh\|fish` | print completion script |
| `doctor` | environment health; `--fix`, `--json` |

### `auth`

| Command | Flags |
|---|---|
| `auth check` | `--test` (live call), `--json` |
| `auth inspect` | `--browser <sel>` — read-only account preview |
| `auth import-cookies <path\|->` | `--include-domains`, `--include-optional`, `--json`, `--quiet` |
| `auth logout` | clear cookies + cached browser profile |
| `auth refresh` | `--quiet`, `--browser-cookies <sel>` — one-shot rotation poke for cron/launchd/systemd |

### `profile`

`list` · `create <name>` · `switch <name>` · `delete <name>` · `rename <old> <new>`

### `language`

`list` · `get [--local]` · `set <code> [--local]`

Language is a **global account setting**. Say so in the output.

### Notebook (top level)

| Command | Flags |
|---|---|
| `list` | `--json`, `--limit N`, `--no-truncate` |
| `create <title>` | `--use`, `--json` |
| `delete` | `-n`, `-y/--yes`, `--json` (requires `-y`; refuses to prompt in JSON mode; adds `context_cleared: true` when deleting the active notebook) |
| `rename <title>` | `-n`, `--json` |
| `summary` | `-n`, `--topics`, `--json` |
| `metadata` | `-n`, `--json` |

### Chat (top level)

| Command | Flags |
|---|---|
| `ask [question\|-]` | `--prompt-file PATH`, `-c <conv-id>`, `--new`, `-y/--yes`, `-s <source-id>` (repeatable), `--request-timeout N` (alias `--timeout`), `--save-as-note`, `--note-title`, `--json` |
| `suggest-prompts` | `--mode 1-10` (default 4), `--query TEXT`, `-s` (repeatable), `--json` |
| `configure` | `--mode default\|learning-guide\|concise\|detailed`, `--persona TEXT` (≤10,000 chars), `--response-length default\|longer\|shorter`, `--json` |
| `history` | `-l/--limit N`, `--json`, `--clear`, `--save`, `-t/--title`, `--show-all`, `--no-truncate` |

`ask --new` is **destructive**: it permanently deletes the notebook's current server-side
conversation (turns are unrecoverable) before starting fresh. It prompts with the
conversation's short id; `--yes` skips, and `--json` **implies `--yes`** so scripted callers
never hang on stdin.

`ask -` reads the question from stdin (Unix convention). `--prompt-file` also accepts `-`.

`--save-as-note` preserves interactive hover-anchored citation links when the answer contains
`[N]` markers (see doc 04, the chat-note payload); answers without citations fall back to a
plain-text note. The server may override the requested title on citation-rich saves — report
what it actually stored.

`--no-truncate` on `history` lifts the 50-char preview cap on the Question/Answer columns;
`-l/--limit` is unrelated and caps how many turns are fetched server-side.

### `source`

| Command | Args | Flags |
|---|---|---|
| `list` | | `--json`, `--limit N`, `--no-truncate`, `--label <id\|name>`, `--status ready\|processing\|error\|preparing\|unknown` |
| `add <content\|->` | URL / file path / text | `--title`, `--type`, `--timeout`, `--mime-type`, `--follow-symlinks`, `--allow-internal`, `--json` |
| `add-drive <id> <title>` | | `--mime-type google-doc\|google-slides\|google-sheets\|pdf`, `--json` |
| `add-drive-file <id\|url>` | | `--title`, `--wait`, `--json` |
| `add-research [query]` | | `--mode fast\|deep`, `--from web\|drive`, `--import-all`, `--cited-only`, `--no-wait`, `--timeout`, `--prompt-file` |
| `get <id>` | | `--json` |
| `fulltext <id>` | | `--json`, `-o FILE`, `--force`, `--no-clobber`, `-f text\|markdown` |
| `guide <id>` | | `--json` |
| `stale <id>` | | `--exit-on-stale`, `--json` |
| `wait <id>` | | `--timeout`, `--interval`, `--json` |
| `clean` | | `--dry-run`, `-y/--yes`, `--json` |
| `rename <id> <title>` | | `--json` |
| `refresh <id>` | | `--json` |
| `delete <id>` | | `-y/--yes`, `--json` |
| `delete-by-title <title>` | exact title | `-y/--yes`, `--json` |

`--status` choices are **derived from the `SourceStatus` label map**, not restated, so a CLI
choice cannot drift from what the Status column renders. The filter is applied inside the
fetch, so `count` in `--json` always matches the rows shown. It composes with `--label`.

`source delete` accepts only full ids or unique prefixes; use `delete-by-title` for titles.
`source clean` removes duplicate, error, and access-blocked sources.

`fulltext -o FILE` **auto-renames** an existing path by default (`FILE` → `FILE (2)`).
`--force` overwrites; `--no-clobber` fails if it exists. Same three-way policy on every
download.

`--follow-symlinks` is a security gate — off by default. `--allow-internal` (URL sources
only) permits private/link-local targets, also off by default.

### `label`

`list` · `sources <ref>` · `generate [--scope all|unlabeled]` · `create <name> [--emoji]` ·
`rename <ref> <new>` · `emoji <ref> <emoji>` · `add <ref> <src>...` ·
`remove <ref> <src>...` · `delete <ref>...`

All accept `-n/--notebook` and `--json`. `<ref>` is a label id, a unique id prefix, or an
exact label name; an ambiguous name lists the matching ids.

`generate --scope all` **wipes and regenerates every label with new ids** — destructive,
requires `-y`. `--scope unlabeled` (the default) only labels currently-unlabeled sources and
needs no confirmation. `label add` appends (existing members survive; labels may overlap).
`label remove` un-assigns only — sources stay in the notebook and in other labels.
`label delete` removes the label only — its sources become unlabeled, not deleted.

### `collection`

`list` · `notebooks <ref>` · `create <name>` · `rename <ref> <new>` ·
`add <ref> <nb>...` · `remove <ref> <nb>...` · `delete <ref>...`

Collections are account-level, so — unlike `label` — these take **no** `-n/--notebook`; their
membership arguments *are* notebooks. `delete` requires `-y` and never removes member
notebooks.

### `research`

| Command | Flags |
|---|---|
| `status` | `-n`, `--run-id/--task-id`, `--json` |
| `wait` | `-n`, `--run-id/--task-id`, `--timeout`, `--interval`, `--import-all`, `--cited-only`, `--json` |
| `import` | `-n`, `--run-id`, `--cited-only`, `--timeout`, `--max-sources`, `--allow-duplicate`, `--json` |
| `cancel <run-id>` | `-n`, `--json` |

### `generate`

**Uniform flag surface on every subcommand except `mind-map`:**

`-n/--notebook` · `-s/--source` (repeatable) · `--json` · `--wait/--no-wait` (default
`--no-wait`, returns a `task_id`) · `--timeout SECONDS` · `--interval SECONDS` (default 2) ·
`--retry N` · `--prompt-file PATH`

Default `--timeout` by type: **audio 1200** · **video 1800** · **cinematic-video 3600** ·
everything else **300**. `--timeout` and `--interval` are no-ops without `--wait`.

`--prompt-file` is mutually exclusive with the positional description argument.

**Language-aware** (`--language LANG`, precedence `--language` > `NOTEBOOKLM_HL` > config >
`en`): `audio`, `video`, `cinematic-video`, `report`, `infographic`, `slide-deck`,
`data-table`, `mind-map`. **Not** accepted by `quiz`, `flashcards`, `revise-slide`.

| Subcommand | Type-specific flags |
|---|---|
| `audio [desc]` | `--format deep-dive\|brief\|critique\|debate`, `--length short\|default\|long` |
| `video [desc]` | `--format explainer\|brief\|cinematic\|short`, `--style auto\|custom\|classic\|whiteboard\|kawaii\|anime\|watercolor\|retro-print\|heritage\|paper-craft`, `--style-prompt TEXT` |
| `cinematic-video [desc]` | alias for `video --format cinematic` |
| `slide-deck [desc]` | `--format detailed\|presenter`, `--length default\|short` |
| `revise-slide <desc>` | `-a/--artifact <id>` (required), `--slide N` (required) |
| `quiz [desc]` | `--difficulty easy\|medium\|hard`, `--quantity fewer\|standard\|more` |
| `flashcards [desc]` | same as quiz |
| `infographic [desc]` | `--orientation landscape\|portrait\|square`, `--detail concise\|standard\|detailed`, `--style auto\|sketch-note\|professional\|bento-grid\|editorial\|instructional\|bricks\|clay\|anime\|kawaii\|scientific` |
| `data-table <desc>` | — |
| `report [desc]` | `--format briefing-doc\|study-guide\|blog-post\|custom`, `--append TEXT` |
| `mind-map` | `--kind interactive\|note-backed` (default `interactive`), `--instructions TEXT`. **No** `--wait`/`--timeout`/`--interval`/`--retry`/`--prompt-file`. |

`--style` is rejected with `--format cinematic` or `--format short` (those have fixed
styles). `--style-prompt` is **required** with `--style custom` and rejected with
cinematic/short.

`--quantity`/`--difficulty` flag defaults are bound to the same constants the wire builders
use (`standard`, `medium`), so the flag default and the wire default cannot drift.

Both mind-map kinds produce the same `{mind_map, note_id, kind}` output, appear in
`artifact list --type mind-map`, and download via `download mind-map`. The interactive kind
applies `--instructions` reliably; the note-backed kind passes them through but the server
may not act on them.

### `artifact`

| Command | Flags |
|---|---|
| `list` | `--type all\|audio\|video\|slide-deck\|quiz\|flashcard\|infographic\|data-table\|mind-map\|report\|fantasy-map\|file`, `--limit N`, `--no-truncate`, `--json` |
| `get <id>` | `--json` |
| `get-prompt <id>` | `--json` |
| `rename <id> <title>` | `--json` |
| `delete <id>` | `-y/--yes`, `--json` |
| `export <id>` | `--title TEXT` (required), `--type docs\|sheets`, `--json` |
| `poll <task-id>` | `--json` |
| `wait <id>` | `--timeout` (300), `--interval` (2), `--json` |
| `retry <id>` | `--wait`, `--timeout` (300), `--interval` (2), `--json` |
| `suggestions` | `--json` |

**`poll` vs `wait` — same identifier, different lifecycle stage.** The API returns one id
that serves as both the generation `task_id` and the `artifact_id`. `poll` is a single
non-blocking check that does **not** prefix-match against `artifact list`, so it works
immediately after `generate` returns, before the row exists in any listing. `wait` blocks with
backoff and **does** accept a unique prefix. Rule of thumb: just generated → `poll`; found it
in `artifact list` → `wait`. Reproduce this note in both commands' help text.

`artifact delete` on a **Mind Map** clears its content rather than removing the entry — mind
maps live alongside notes and Google may garbage-collect cleared entries later. Print
`Cleared mind map:` instead of `Deleted artifact:` when that happens.

`artifact retry` re-runs a *failed* artifact in place; the id is preserved and the artifact
is not deleted first. Default returns immediately (`Retry started: <id>`). A synchronous
refusal (rate limit / quota / not retryable) exits non-zero with a typed error rather than
reporting a started task.

### `download`

**Uniform flags on every subcommand:** `-n/--notebook` · `-a/--artifact <id>` · `--all` ·
`--latest` (default) · `--earliest` · `--name TEXT` (fuzzy title match) · `--dry-run` ·
`--force` · `--no-clobber` · `--json`

Default is auto-rename on collision. `--no-clobber` fails a single download and **skips** on
`--all`.

| Subcommand | Default output | Type-specific |
|---|---|---|
| `audio [path]` | `./audio/*.m4a` | — |
| `video [path]` | `./video/*.mp4` | — |
| `cinematic-video [path]` | `./video/*.mp4` | alias for `video` — cinematic and standard share the artifact type |
| `slide-deck [path]` | `./slide-decks/*.pdf` | `--format pdf\|pptx` |
| `infographic [path]` | `./infographic/*.png` | — |
| `report [path]` | `./reports/*.md` | — |
| `mind-map [path]` | `./mind-maps/*.json` | handles both kinds |
| `data-table [path]` | `./data-tables/*.csv` | — |
| `quiz [path]` | `./quizzes/*.json` | `--format json\|markdown\|html` |
| `flashcards [path]` | `./flashcards/*.json` | `--format json\|markdown\|html` |

Audio bytes are **AAC in an MP4 container**, hence `.m4a`, MIME `audio/mp4` — not MP3.
Verified WAV output is corrected to `.wav`.

### `note`

`list` · `create [content|-] [--content TEXT] [-t/--title]` · `get <id>` ·
`save <id> [--title] [--content]` · `rename <id> <title>` · `delete <id> [-y]`

All accept `-n/--notebook` and `--json`.

**`source get` / `artifact get` / `note get` exit `1` on not-found**, with the standard typed
envelope under `--json`:
`{"error": true, "code": "NOT_FOUND", "message": "…", "id": "…", "notebook_id": "…"}`.

### `share`

`status` · `set-public` · `set-restricted` · `view-level <full|chat>` ·
`add <email> [--permission editor|viewer] [--notify] [--message]` ·
`update <email> --permission <p>` · `remove <email>`

Widening restricted → public requires confirmation.

### `mcp`

`mcp install claude-desktop|claude-code|cursor|windsurf [--config-path PATH]`

Idempotent; preserves unrelated servers in the same config file. Config locations in doc 08.

### `skill`

`install` · `status` · `uninstall` · `show` · `package -o DIR`

Defaults `--scope user --target all`. Targets: `claude` →
`.claude/skills/notebooklm/SKILL.md`, `agents` → `.agents/skills/notebooklm/SKILL.md`.
`show --target source` prints the packaged skill. Project-scope installs support
`--dry-run`/`--no-clobber`/`--force`; those flags are **rejected** for user scope.

Embed `SKILL.md` with `go:embed` so the binary carries it — better than the Python wheel's
data-file approach.

### `agent`

`agent show codex` · `agent show claude`

`show codex` prefers a repo-root `AGENTS.md` when run from a source checkout.

## Rendering rules

1. **stdout is the payload; stderr is the narration.** Status lines, spinners, progress bars,
   warnings, and confirmation prompts all go to stderr. `notebooklm list --json | jq` must
   never see a spinner frame.
2. **Tables truncate by default, `--no-truncate` lifts it.** Title columns cap at the
   terminal width; `--json` never truncates.
3. **Confirmations name what will be lost.** Not "Are you sure?" but "Delete notebook
   'Research' and its 12 sources? [y/N]". Destructive prompts render in Makro Red.
4. **`--dry-run` prints exactly what would happen and exits 0**, in Yellow, with no side
   effects — including no baseline reads that would bump `lastViewedTime`.
5. **Progress for long operations.** `--wait` generations and `--all` downloads print a
   stderr progress line with elapsed time and current status. Under `--quiet` or a non-TTY,
   degrade to nothing.
6. **`--json` is a complete envelope, not a fragment.** Include the ids the caller needs to
   continue (`task_id`, `notebook_id`, `source_id`), a `count` when returning a list, and
   `success: true` on mutations.
