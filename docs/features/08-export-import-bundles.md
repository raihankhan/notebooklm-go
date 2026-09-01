# Feature 8 — Export and import bundles

> Spec part of [docs/15-features-overview.md](../15-features-overview.md).

## Goal

Ship a single signed zip that captures an entire notebook — sources, fulltext, notes, chat history, artifacts, metadata — for legal hold, tenant migration, and team handoff. The same file can be re-imported into any `~/.notebooklm/` instance to recreate the notebook.

## User stories

1. A legal team needs to preserve a notebook for a litigation hold. They run `notebooklm bundle create <notebook-id> --out ./hold.zip --include sources,fulltext,notes,chat,artifacts,metadata --sign`. The zip is timestamped, content-hashed, and ready to be archived.
2. A user is migrating from one Google account to another. They export the notebook; on the new account they import it; sources are re-added via the existing `Sources.AddURL`, fulltext is restored from the local copy, chat history is rebuilt from the JSONL.
3. A team archives a notebook at the end of a project. Six months later, an analyst runs `notebooklm bundle import ./archive.zip` and reads the project notebook offline.

## CLI surface

Extends the existing `bundle` group (currently used for source bundles under feature #4). The two are kept distinct by subcommand prefix:

```
notebooklm bundle export <notebook-id> --out <path>
                            [--include <csv>] [--exclude <csv>]
                            [--sign] [--compression <deflate|zstd|none>]
notebooklm bundle import <archive.zip> [--into-notebook <new-title>] [--yes]
notebooklm bundle verify <archive.zip>        # check signature + manifest
notebooklm bundle inspect <archive.zip>       # print manifest, no side effects
```

The high-traffic paths are `bundle export` and `bundle import`.

### Bundle file format

A bundle is a zip with a top-level `manifest.json`:

```json
{
  "version": 1,
  "kind": "notebook-export",
  "exported_at": "2026-01-01T10:00:00Z",
  "exported_by": "user@example.com",
  "notebook": {
    "id": "abc-123",
    "title": "Research: AI safety",
    "created_at": "...",
    "modified_at": "..."
  },
  "includes": ["sources", "fulltext", "notes", "chat", "artifacts", "metadata"],
  "content_hash": "sha256:...",                  // over the zip minus signature
  "files": [
      {"path": "sources.json", "sha256": "...", "size": 12345},
      {"path": "fulltext/src-1.txt", "sha256": "...", "size": 5678},
      ...
    ],
  "signature": "ed25519:..."                     // only when --sign is passed
}
```

Layout inside the zip:

```
hold.zip
├── manifest.json
├── notebook.json                              # metadata
├── sources.json                               # source list
├── fulltext/
│   ├── src-1.txt
│   └── src-2.md
├── notes.json                                 # all notes
├── chat.jsonl                                 # conversation history
├── artifacts.json                             # artifact list (URLs only; binaries not bundled by default)
├── artifacts/                                 # optional: binaries via --include artifacts.bin
│   ├── audio-1.m4a
│   └── quiz-1.json
└── signature.ed25519                           # only when --sign is passed
```

### `--include` flags

| Flag | Default | What it captures |
|---|---|---|
| `sources` | yes | the source list with metadata |
| `fulltext` | yes | every source's `GetFulltext` content |
| `notes` | yes | every note's content |
| `chat` | yes | conversation history JSONL |
| `artifacts` | yes | artifact list + URLs (binaries skipped unless `artifacts.bin` also set) |
| `artifacts.bin` | no | artifact byte contents (audio, video, slide decks, etc.) |
| `metadata` | yes | notebook description, summary, AI description |

`artifacts.bin` is large; the user must opt in. With `--include artifacts,artifacts.bin`, a notebook with 10 audio overviews can produce a 500 MB zip.

### Signing

`--sign` prompts for an Ed25519 signing key (path or stdin). The signature is written to `signature.ed25519` inside the zip and to `manifest.signature`. `bundle verify` checks the signature against the user-supplied public key.

If `--sign` is not passed, `bundle verify` checks only the content hashes; an unsigned bundle is reported as `signature: missing`.

### Import

`notebooklm bundle import <archive.zip>`:

1. Reads `manifest.json`, verifies `content_hash` (and `signature` if present).
2. Creates a new notebook with `--into-notebook` (default: original title).
3. For each source in `sources.json`:
   - If `fulltext` is included and the source kind is one we can recreate offline (URL, YouTube, Drive with cached content), re-add it via `Sources.AddURL`.
   - Otherwise, add a placeholder note describing what was lost.
4. Restores notes verbatim (`Notes.Create`).
5. Restores chat history by re-creating each conversation with the same messages (the underlying RPC supports conversation replay).
6. Restores artifacts by URL (re-downloading them with `Downloads.Get` if `--redownload-artifacts` is set; otherwise records the URL for the user to fetch manually).

The import is **idempotent** under `manifest.uuid` (a per-export UUID): a second import of the same archive is a no-op.

## REST surface

| Method | Path | Status | Limiter |
|---|---|---|---|
| `POST` | `/v1/notebooks/{notebook_id}/bundle/export` | 200 | download |
| `POST` | `/v1/notebooks/bundle/import` | 202 | source-mutation |
| `POST` | `/v1/bundles/verify` | 200 | — |

`/export` returns the binary zip directly (Content-Type: `application/zip`). `/import` takes `multipart/form-data` with the archive as the file part and returns 202 + `run_id`; poll `/v1/notebooks/{new_notebook_id}` to track progress.

## MCP tools

| Name | Purpose | Confirmation |
|---|---|---|
| `bundle_export` | Export a notebook. | yes (writes to disk) |
| `bundle_import` | Import an archive. | yes (creates a notebook) |
| `bundle_verify` | Verify signature + hashes. | no |
| `bundle_inspect` | Print the manifest. | no |

## Public SDK additions

```go
type BundleManifest struct { Version, Kind int; ExportedAt time.Time; ExportedBy string; Notebook Notebook; Includes []string; Files []BundleFile; ContentHash, Signature string }
type Bundle struct{ client *Client }
func (b *Bundle) Export(ctx, notebookID string, opts BundleExportOptions) (*BundleManifest, error)
func (b *Bundle) Import(ctx, archivePath string, opts BundleImportOptions) (*Notebook, error)
func (b *Bundle) Verify(ctx, archivePath string, publicKey ed25519.PublicKey) error
func (b *Bundle) Inspect(ctx, archivePath string) (*BundleManifest, error)
```

## Data model under `~/.notebooklm/`

| Path | Format | Purpose |
|---|---|---|
| `~/.notebooklm/bundles/` | dir | one folder per import |
| `~/.notebooklm/bundles/<bundle-uuid>/manifest.json` | JSON | the original manifest |
| `~/.notebooklm/bundles/index.json` | JSON | `{ imports: [...] }` |

Exports are not stored locally unless the user passes `--out ~/.notebooklm/bundles/exports/`. By default the export goes to wherever `--out` points; no implicit copy.

## Protocol implications

**No new RPC.** Export calls the existing `Sources.List`, `Sources.GetFulltext`, `Notes.List`, `Notes.Get`, `Chat.GetHistory`, `Artifacts.List`. The Ed25519 signing uses the standard library (`crypto/ed25519`); no third-party crypto dependency.

Cited reusable calls:

| Bundle operation | SDK call | Python original |
|---|---|---|
| Export sources | `Sources.List` + `Sources.GetFulltext` | `cli/source_cmd.py::list` / `fulltext` |
| Export notes | `Notes.List` + `Notes.Get` | `cli/note_cmd.py::list` / `get` |
| Export chat | `Chat.GetHistory` | `cli/history_cmd.py::history` |
| Export artifacts | `Artifacts.List` + `Artifacts.GetPrompt` | `cli/artifact_cmd.py::list` |
| Import source | `Sources.AddURL` (re-fetched) | `cli/source_cmd.py::add` |
| Import note | `Notes.Create` | `cli/note_cmd.py::create` |

## Dependencies on Phases 14–18

| Phase | What this feature needs |
|---|---|
| 14 | `bundle export` can be a pipeline step. |
| **15 (this phase)** | Independent. |
| 16 | The workspace policy can require bundle export before notebook deletion. |
| 17 | Plugins can register custom bundle `includes`. |
| 18 | The skill teaches the agent to bundle-export before destructive operations. |

## Tests

### Unit

| Test | What it asserts |
|---|---|
| `TestBundleManifest` | Valid manifest round-trips through JSON. |
| `TestBundleContentHash` | Hashing the zip minus the signature file produces the documented hash. |
| `TestBundleSignVerify` | An Ed25519-signed bundle verifies; a tampered bundle fails `verify`. |
| `TestBundleExportIncludes` | `--include sources,notes` produces a zip with the right files; `--exclude fulltext` skips them. |
| `TestBundleImportIdempotent` | A second import with the same `manifest.uuid` is a no-op. |
| `TestBundleImportRecreatesNotebook` | A round-trip (export → import) yields a new notebook with the same title, sources, notes, chat history. |
| `TestBundleArtifactsBin` | `--include artifacts,artifacts.bin` includes binary content; without, only URLs. |
| `TestBundleVerifyMissingSig` | An unsigned bundle reports `signature: missing`, not an error. |
| `TestBundleAtomicWrite` | A crashed export leaves no `*.tmp` file behind. |

### Cassette

A pre-recorded `Sources.List` + `Notes.List` + `Chat.GetHistory`; `Bundle.Export` produces a real zip; `Bundle.Inspect` parses it back; the manifest matches the cassette.

### E2E

A live `bundle export` of a real notebook, `bundle import` on the same machine, and verification that the new notebook has the same title, sources count, and notes count as the original.

## Acceptance criteria

1. `notebooklm bundle export <nb-id> --out ./hold.zip` produces a zip whose `manifest.json` enumerates every included file with sha256 hashes.
2. `notebooklm bundle verify ./hold.zip` (unsigned) reports `signature: missing`; a signed bundle reports `signature: valid`; a tampered bundle reports `signature: invalid`.
3. `notebooklm bundle import ./hold.zip --into-notebook "Restored nb"` creates a new notebook; a second import of the same archive is a no-op.
4. Round-trip export → import preserves title, source count, note count, chat history (by message count and order).
5. `--include artifacts.bin` is opt-in; without it, audio / video binaries are not in the zip.
6. The MCP `bundle_export` and `bundle_import` tools require `confirm: true`; `bundle_inspect` is read-only.
7. A 100 MB notebook bundle exports in under 60 s on a developer laptop; import is linear in source count.