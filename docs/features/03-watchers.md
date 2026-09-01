# Feature 3 — Watchers

> Spec part of [docs/15-features-overview.md](../15-features-overview.md).

## Goal

Turn a static notebook into a living knowledge base by polling a trigger source (RSS / Atom feed, local directory, git repo) and automatically adding new entries as notebook sources. State is per-watch, persisted across restarts.

## User stories

1. A user wants their research notebook to stay current. They run `notebooklm watch add nb1 --source rss:https://example.com/feed.xml`. New feed entries are auto-added as `web_page` sources; last-seen GUID state persists.
2. A team puts a `papers/` directory on a shared drive. They run `notebooklm watch add nb1 --source dir:/mnt/papers --include "*.pdf"`. New PDFs are auto-uploaded as sources; changed PDFs trigger `source.refresh`.
3. An open-source maintainer keeps docs in a git repo. They run `notebooklm watch add nb1 --source git:git@github.com:org/docs.git --branch main --paths "docs/**"`. Each commit on `main` whose paths match `docs/**` adds the changed files as sources.

## CLI surface

New top-level group:

```
notebooklm watch
  add <notebook-id> --source <kind>:<spec>
                      [--include <glob>] [--exclude <glob>]
                      [--interval <duration>] [--batch <n>] [--on-commit | --on-tick]
  list [--notebook <id>] [--kind <rss|dir|git>]
  show <watch-id>
  status <watch-id>                    # poll-now output
  pause <watch-id>
  resume <watch-id>
  remove <watch-id> [--yes]
  log <watch-id> [--tail <n>] [--since <ts>]
```

The high-traffic path is `watch add`. Source kinds:

| Kind | Spec | Trigger | What gets added |
|---|---|---|---|
| `rss:` | URL of an RSS / Atom feed | Poll on `interval` | One `web_page` source per new `<item>`; dedup by `<guid>` (RSS) or `<id>` (Atom). |
| `dir:` | Absolute path to a directory | `fsnotify` (inotify on Linux, FSEvents on macOS, ReadDirectoryChangesW on Windows) | One source per new file matching `--include`; changed files trigger `source.refresh`. |
| `git:` | URL of a git repo | Poll on `interval` (5 min minimum) | One source per new file in matching `--paths` since the last-seen commit. Shallow clone. |

`--interval` defaults: `rss:` 5 min, `dir:` event-driven, `git:` 15 min. `--batch` caps how many new sources are added per cycle; excess is queued.

`watch status <watch-id>` runs the cycle once and prints what would be added without committing.

`watch log <watch-id>` tails the persisted log file (`~/.notebooklm/watches/<id>/log.jsonl`) so the user can see what happened across process restarts.

## REST surface

| Method | Path | Status | Limiter |
|---|---|---|---|
| `POST` | `/v1/notebooks/{notebook_id}/watches` | 201 | source-mutation |
| `GET` | `/v1/notebooks/{notebook_id}/watches` | 200 | — |
| `GET` | `/v1/watches/{watch_id}` | 200 | — |
| `POST` | `/v1/watches/{watch_id}/pause` | 200 | — |
| `POST` | `/v1/watches/{watch_id}/resume` | 200 | — |
| `POST` | `/v1/watches/{watch_id}/run` | 202 | — |
| `DELETE` | `/v1/watches/{watch_id}` | 204 | source-mutation |

The server runs watchers in a single goroutine per watch, with the existing `NOTEBOOKLM_SERVER_SOURCE_MUTATION_CONCURRENCY` cap.

## MCP tools

| Name | Purpose | Confirmation |
|---|---|---|
| `watch_add` | Add a watcher. | yes |
| `watch_list` | List watches for a notebook. | no |
| `watch_status` | Run one cycle, return what would be added. | no |
| `watch_remove` | Remove a watcher. | yes |

## Public SDK additions

```go
type WatchKind string
const (WatchRSS WatchKind = "rss"; WatchDir = "dir"; WatchGit = "git")

type Watch struct {
    ID, NotebookID string
    Kind           WatchKind
    Spec           string          // URL for rss/git, path for dir
    Include, Exclude []string
    Interval       time.Duration
    Batch          int
    State          string          // "running" | "paused"
    LastRunAt      *time.Time
    LastError      *string
}

type WatchAddOptions struct { Include, Exclude []string; Interval time.Duration; Batch int }

type Watches struct{ client *Client }

func (w *Watches) Add(ctx, notebookID, kindSpec string, opts WatchAddOptions) (*Watch, error)
func (w *Watches) List(ctx, filter WatchListFilter) ([]Watch, error)
func (w *Watches) Get(ctx, watchID string) (*Watch, error)
func (w *Watches) Pause(ctx, watchID string) error
func (w *Watches) Resume(ctx, watchID string) error
func (w *Watches) Run(ctx, watchID string) (*WatchRun, error)
func (w *Watches) Remove(ctx, watchID string) error
```

The `Run` method runs one poll cycle synchronously and returns the result; the REST `Run` returns 202 + run-id for server-side execution.

## Data model under `~/.notebooklm/`

| Path | Format | Purpose |
|---|---|---|
| `~/.notebooklm/watches/` | dir | one folder per watch |
| `~/.notebooklm/watches/<watch-id>/state.json` | JSON | watch config + last-seen cursor (RSS guid, dir mtime, git HEAD commit) |
| `~/.notebooklm/watches/<watch-id>/log.jsonl` | JSONL | one record per cycle: `ts, kind, items_discovered, items_added, errors` |
| `~/.notebooklm/watches/index.json` | JSON | `{ watches: [...] }`, latest-first |

The RSS cursor is the most-recent `<guid>` (or `<link>` if no guid) the watcher has seen. On cycle start, the watcher fetches the feed, deduplicates, and adds `guid > last_seen_guid` as sources.

The dir cursor is `(path, mtime, size)` per file. Files are added on first observation; changed files trigger `source.refresh` if the `refreshed_at` is older than the file's mtime.

The git cursor is `last_seen_commit_sha`. The walker diffs `last_seen_commit..HEAD` and adds each touched file matching `--paths`.

Atomic writes per `internal/atomicio`. The state file is updated under a per-watch mutex; the index is read on every `watch list` call.

## Protocol implications

**No new RPC.** Every event in the poll cycle calls an existing SDK method:

| Watcher action | SDK call | Python original |
|---|---|---|
| Fetch RSS | `net/http` (stdlib) + an XML parser (stdlib `encoding/xml`) | n/a — client-only |
| Read directory | `os.ReadDir` + `fsnotify` for events | n/a — client-only |
| Diff git | `os/exec("git", "diff", …)` shell-out | n/a — client-only |
| Add source | `client.Sources.AddURL` or `client.Sources.AddFile` | `cli/source_cmd.py::add` → `_sources.py::AddURL/AddFile` |
| Refresh source | `client.Sources.Refresh` | `cli/source_cmd.py::refresh` → `_sources.py::Refresh` |

The git kind shells out to `git` because the alternative is shipping a pure-Go git implementation, which is out of scope. The shell-out is argv-only (no shell), so it's not an injection vector. If `git` is not on `PATH`, the watcher fails closed with `git_not_found`.

## Dependencies on Phases 14–18

| Phase | What this feature needs |
|---|---|
| 14–16 | Independent of #1, #2, #4, #5, #6, #7, #8. |
| 17 | A plugin can contribute a new watch kind by registering a `Watcher` interface. |
| 18 | The skill teaches the agent when to use `watch add` vs `source add` and how to interpret `watch log`. |

## Tests

### Unit

| Test | What it asserts |
|---|---|
| `TestRSSCursor` | A feed with three new entries adds three sources; second poll adds zero (cursor advances). |
| `TestRSSGuidStability` | A `<guid>` that reappears is not re-added. |
| `TestDirFsNotify` | New file in watched dir triggers `AddFile`. |
| `TestDirIncludeExclude` | `--include "*.pdf" --exclude "*.tmp.pdf"` filters correctly. |
| `TestGitDiffShallow` | New commit on `main` adds only the changed files in `--paths`. |
| `TestGitBranchChange` | Switching `--branch` resets the cursor to the new branch's HEAD. |
| `TestWatchPause` | A paused watch does not poll; `watch resume` polls again. |
| `TestWatchRemove` | Removes the watch folder + index entry. |
| `TestIdempotencyUnderRace` | Two concurrent cycles (e.g., from a manual `status` and the scheduled poll) do not add the same item twice. |
| `TestBackpressure` | `--batch 5` against 20 new items adds 5 and queues 15 for the next cycle. |

### Cassette

A pre-recorded RSS feed cycle against a cassette of `Sources.AddURL`. Asserts dedup and guid handling.

### E2E

Live `watch add` against a real RSS feed and a real directory. Assert: new entries added as sources; logs persisted; surviving `notebooklm logout` and re-login.

## Acceptance criteria

1. `notebooklm watch add nb1 --source rss:https://example.com/feed` creates a watch; the next poll adds new entries as sources; a second poll with no new entries adds none.
2. `notebooklm watch add nb1 --source dir:/tmp/papers --include "*.pdf"` adds each new PDF and refreshes each changed PDF.
3. `notebooklm watch add nb1 --source git:git@github.com:org/repo --branch main --paths "docs/**"` adds the matching files from new commits.
4. `--batch` caps items per cycle; the remainder is queued for the next cycle.
5. `notebooklm watch pause <id>` halts polling; `watch resume` restarts.
6. `notebooklm watch log <id>` returns the last N log lines.
7. The watch survives a `notebooklm upgrade` (state file format is versioned).
8. The MCP `watch_run` tool returns the would-add list without committing. The `watch_add` and `watch_remove` tools require `confirm: true`.
