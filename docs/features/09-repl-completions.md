# Feature 9 — CLI REPL and shell completions

> Spec part of [docs/15-features-overview.md](../15-features-overview.md).

## Goal

Two quality-of-life additions for human operators:

1. A persistent-notebook REPL where every command runs in the context of the active notebook. History is preserved across sessions and never written to `~/.bash_history` (which may be world-readable).
2. First-class shell completions for `bash`, `zsh`, `fish`, and `powershell`, installable with one command.

## User stories

1. A user runs `notebooklm repl`. They get a prompt `notebooklm [research:ai-safety]>`; every command runs against the bound notebook. `Ctrl-D` exits; history is saved.
2. A user types `notebooklm generate aud<TAB>` and gets `audio`. They type `notebooklm generate audio --form<TAB>` and gets `--format=`. They type `notebooklm generate audio --format <TAB>` and gets the documented format values.
3. A team installs completions in their dotfiles repo via `notebooklm completion install zsh`. Every team member gets the same completions.

## CLI surface

Two new top-level commands:

```
notebooklm repl [--notebook <id>] [--profile <name>] [--history <path>]
                [--prompt <string>]

notebooklm completion
  install <bash|zsh|fish|powershell> [--path <dir>]
  uninstall <bash|zsh|fish|powershell>
  show <bash|zsh|fish|powershell>
  generate <bash|zsh|fish|powershell>  # print to stdout, e.g. for `eval "$(...)"`
```

### `repl`

The REPL is a thin shell over the existing CLI. It supports:

- **Persistent notebook context** — the first `notebooklm use <id>` in the REPL binds the prompt; every subsequent command runs with `-n <id>` implicitly.
- **History** — `~/.notebooklm/repl_history/<profile>.txt`, atomic-append, capped at 10 000 lines (rotated).
- **Tab completion** — uses the same completion data as the shell-level completion; works inside the REPL via `cobra.Command.GenBashCompletion` / fish / zsh engines.
- **Slash commands** for REPL-only niceties:

| Slash command | Action |
|---|---|
| `/notebook <id\|title>` | Bind the active notebook (equivalent to `use`). |
| `/clear` | Clear the screen. |
| `/history` | Print the in-memory history. |
| `/exit` | Exit (Ctrl-D does the same). |
| `/help [topic]` | Show REPL-level help; `/help generate` lists the `generate` group. |

- **Prompt string** — `--prompt "notebooklm [{{.NotebookTitle}}]> "` is the default. Variables: `{{.NotebookTitle}}`, `{{.NotebookID}}`, `{{.Profile}}`, `{{.User}}`.

- **Color** — uses the existing CP AXTRA theme via `lipgloss` styles. The prompt is CP AXTRA Blue, errors are Makro Red, completions highlight in Lotus Green.

- **TUI dependencies** — uses `github.com/charmbracelet/bubbletea` for the prompt + `lipgloss` for styling. Falls back to `readline` (a pure-Go readline implementation, e.g. `github.com/chzyer/readline`) when stdout is not a TTY.

### `completion`

`completion install zsh --path ~/.zsh/completions` writes `_notebooklm` to the path and prints a one-liner the user adds to `.zshrc`:

```bash
fpath=(~/.zsh/completions $fpath); autoload -U compinit; compinit
```

`completion generate zsh` prints the script to stdout for users who prefer `eval "$(notebooklm completion generate zsh)"` in `.zshrc`.

Completions are generated at install time from the live Cobra command tree, so they always match the installed CLI's flags and subcommands.

### Acceptance for the existing CLI

Every existing top-level command must be completable:

- `notebooklm <TAB>` lists the top-level groups.
- `notebooklm generate <TAB>` lists `audio`, `video`, `quiz`, `flashcards`, `report`, `data-table`, `mind-map`, `slide-deck`, `infographic`, `revise-slide`.
- `notebooklm generate audio --<TAB>` lists every flag from the audio group.
- `notebooklm generate audio --format <TAB>` lists the enum values: `deep-dive`, `brief`, `critique`, `debate`.
- `notebooklm share add <TAB>` lists emails already on the share list (dynamic completion; populated by a one-shot RPC).

## REST surface

No new routes. The REPL is a CLI feature only; the completions are CLI-only. The REST server doesn't need to know they exist.

## MCP tools

No new tools. The REPL is for humans; agents use the MCP / REST surface already.

## Public SDK additions

No new SDK methods. The REPL is a thin wrapper around the existing `Client`. The completion script generator is a Cobra utility; no SDK surface.

## Data model under `~/.notebooklm/`

| Path | Format | Purpose |
|---|---|---|
| `~/.notebooklm/repl_history/` | dir | one file per profile |
| `~/.notebooklm/repl_history/<profile>.txt` | text | newline-separated history, capped at 10 000 lines |

Atomic-append per `internal/atomicio`. The file is `0600`; the directory is `0700`. The REPL never writes history outside `~/.notebooklm/`.

## Protocol implications

**No new RPC.** The REPL invokes the existing SDK methods through the existing CLI subcommands. Completion generation is a Cobra utility over the live command tree; no RPC involved.

The REPL's dynamic completion (`share add <TAB>` → list of known emails) does make a small RPC call to populate the list; this is `Sharing.GetStatus`, an existing call.

## Dependencies on Phases 14–18

| Phase | What this feature needs |
|---|---|
| 14–17 | Independent. The REPL surfaces every existing command; nothing new is needed. |
| 18 | The skill tells the user `notebooklm repl` exists; this is the recommended entry point for ad-hoc exploration. |

## Tests

### Unit

| Test | What it asserts |
|---|---|
| `TestReplBindNotebook` | `/notebook <id>` binds the prompt; subsequent commands inherit `-n`. |
| `TestReplHistory` | Commands typed in the REPL appear in `repl_history/<profile>.txt`. |
| `TestReplHistoryRotation` | A 10 001-line history is rotated; the oldest line is dropped. |
| `TestReplHistoryPermissions` | The history file is `0600`; the directory is `0700`. |
| `TestReplSlashCommands` | `/help`, `/history`, `/clear`, `/exit` behave per the spec. |
| `TestCompletionBash` | `completion generate bash` produces a syntactically valid bash completion script. |
| `TestCompletionZsh` | `completion generate zsh` produces a valid zsh completion script. |
| `TestCompletionFish` | `completion generate fish` produces a valid fish completion script. |
| `TestCompletionPowershell` | `completion generate powershell` produces a valid powershell completion script. |
| `TestCompletionDynamic` | `share add <TAB>` calls `Sharing.GetStatus` and lists known emails. |

### Cassette

A scripted REPL session against a cassette. Asserts the REPL parses input, invokes the right subcommand, and writes history correctly.

### E2E

A live REPL session: `/notebook <real-id>`, then `ask "..."`, then `generate audio "..." --wait`, then `Ctrl-D`. Asserts the history file is populated and the artifacts are real.

## Acceptance criteria

1. `notebooklm repl` opens with the prompt `notebooklm [{{.NotebookTitle}}]>` when a notebook is bound; the title updates on `/notebook`.
2. History persists across REPL sessions.
3. Tab completion works inside the REPL for every documented command and flag.
4. `completion install zsh --path ~/.zsh/completions` writes a working `_notebooklm` script and prints the one-liner to add to `.zshrc`.
5. `completion generate bash` outputs a script that `bash --noprofile --norc -c "source <(notebooklm completion generate bash)"` accepts.
6. The history file is `0600`; the directory is `0700`. A REPL crash never writes a partial line.
7. The REPL works under `script(1)` and inside tmux; falls back to non-TUI readline when stdout is not a TTY.
8. The dynamic completion for `share add` calls `Sharing.GetStatus` and lists known emails; a network failure is non-fatal (the completion falls back to the static list).
