# 12 — Porting map: Python module → Go package

Answers "where did Python file X go?" — and, read in reverse, "what is the normative source
for this Go file?"

Line counts are the Python originals, as a rough weight indicator.

## Wire / RPC

| Python | Lines | Go |
|---|---|---|
| `rpc/types.py` | 215 | `internal/web/wire/methods.go` + `urls.go` |
| `_web/wire/encoder.py` | 111 | `internal/web/wire/encode.go` |
| `_web/wire/decoder.py` | 1031 | `internal/web/wire/decode.go` + `status.go` |
| `_web/wire/safe_index.py` | — | `internal/web/wire/index.go` |
| `_web/wire/overrides.py` | — | `internal/web/wire/methods.go` (`ResolveID`) |
| `_web/policy.py` | — | `internal/web/policy/registry.go` |
| `rpc/__init__.py` | — | *(dropped — Python re-export compatibility layer)* |

## Transport / runtime

| Python | Lines | Go |
|---|---|---|
| `_web/transport/kernel.py` | 251 | `internal/web/transport/kernel.go` |
| `_web/transport/executor.py` | 714 | `internal/web/transport/executor.go` |
| `_web/transport/runtime.py` | 431 | `internal/web/transport/runtime.go` |
| `_web/transport/middleware/chain.py`, `chain_host.py` | — | `internal/web/transport/chain.go` |
| `_web/transport/middleware/retry.py` | — | `mw_retry.go` |
| `_web/transport/middleware/auth_refresh.py` | — | `mw_authrefresh.go` |
| `_web/transport/middleware/error_injection.py` | — | `mw_errorinject.go` |
| `_web/transport/middleware/tracing.py` | — | `mw_tracing.go` |
| `_web/transport/middleware/{drain,metrics,semaphore}.py` | — | folded into `internal/runtime/supervisor.go` |
| `_web/transport/middleware/{core,context}.py` | — | `internal/web/transport/request.go` |
| `_web/transport/request_types.py` | 117 | `internal/web/transport/request.go` |
| `_web/transport/errors.py` | — | `internal/web/transport/errors.go` |
| `_web/transport/streaming_post.py` | — | `internal/web/transport/stream.go` |
| `_web/transport/auth.py` | — | `internal/web/transport/authcoord.go` |
| `_web/transport/auth_refresh_retry.py` | — | `internal/web/transport/refreshbudget.go` |
| `_web/transport/cookie_persistence.py` | 457 | `internal/auth/profile/persistence.go` |
| `_web/transport/lifecycle.go` | — | `internal/web/transport/lifecycle.go` |
| `_web/transport/reqid_counter.py` | — | `internal/web/transport/reqid.go` |
| `_web/transport/chat.py` | — | `internal/web/transport/chatpost.go` |
| `_web/transport/seams.py` | — | *(dropped — Python test-seam machinery; Go uses interfaces)* |
| `_runtime/lifecycle.py` | 628 | `internal/runtime/lifecycle.go` |
| `_runtime/call_supervisor.py` | — | `internal/runtime/supervisor.go` |
| `_runtime/{init,config,contracts,helpers}.py` | 618+ | `notebooklm/client.go`, `internal/config` |
| `_client_metrics.py` | 147 | `internal/runtime/metrics.go` |
| `_transport_drain.py` | 340 | folded into `internal/runtime/supervisor.go` |
| `_deadline.py` | 66 | `internal/runtime/deadline.go` |
| `_backoff.py` | 59 | `internal/runtime/backoff.go` |
| `_idempotency.py` | 252 | `internal/app/idempotent/create.go` (probe-then-create) |
| `_polling_registry.py` | 94 | `internal/web/artifact/polling.go` |
| `_curl_cffi_transport.py` | 691 | `internal/web/transport/utls.go` — **v1.1, optional** |
| `_loop_affinity.py`, `_loop_bound.py` | 135 | **dropped** — Python-runtime hazard; see doc 02 |
| `_client_assembly.py`, `_client_composed.py` | 738 | `notebooklm/client.go` |
| `_callbacks.py`, `_lookup.py`, `_hop_credentials.py` | 92 | small helpers, inlined |

## Auth

| Python | Lines | Go |
|---|---|---|
| `_auth/tokens.py` | 895 | `internal/auth/tokens.go` |
| `_auth/cookie_types.py` | — | `internal/auth/cookiejar/cookie.go` |
| `_auth/cookies.py` | — | `internal/auth/cookiejar/jar.go` |
| `_auth/cookie_policy.py` | — | `internal/auth/policy/domains.go` + `required.go` |
| `_auth/cookie_semantics.py`, `cookie_merge.py`, `cookie_filter.py` | — | `internal/auth/cookiejar/semantics.go`, `internal/auth/policy/filter.go` |
| `_auth/storage.py` | 1090 | `internal/auth/storage/state.go` |
| `_auth/profile_store.py` | 876 | `internal/auth/profile/store.go` |
| `_auth/profile_document.py`, `profile_account.py` | — | `internal/auth/profile/document.go` |
| `_auth/profile_migration.py` | 602 | `internal/auth/profile/migration.go` |
| `_auth/credential_io.py`, `atomic_io.py` | 445 | `internal/atomicio` |
| `_auth/storage_lock.py` | — | `internal/atomicio/lock.go` |
| `_auth/paths.py` | — | `internal/paths` + `internal/atomicio/lockpaths.go` |
| `_auth/extraction.py` | 454 | `internal/auth/extract/wiz.go` + `failure.go` |
| `_auth/session.py` | 318 | `internal/auth/refresh/l1.go` |
| `_auth/refresh.py` | — | `internal/auth/refresh/ladder.go` |
| `_auth/recovery.py` | — | `internal/auth/refresh/recovery.go` |
| `_auth/psidts_recovery.py` | — | `internal/auth/keepalive/psidts.go` |
| `_auth/keepalive.py` | — | `internal/auth/keepalive/rotate.go` |
| `_auth/single_flight.py` | — | `internal/auth/singleflight` |
| `_auth/headless_reauth.py` | — | `internal/auth/browser/headless.go` |
| `_auth/browser_capture.py` | — | `internal/auth/browser/cdp.go` |
| `_auth/browser_launch_errors.py`, `navigation_errors.py` | — | `internal/auth/browser/errors.go` |
| `_auth/mint_service.py` | — | `internal/auth/mastertoken/mint.go` |
| `_auth/master_token*.py` (5 files) | — | `internal/auth/mastertoken/{token,file,bootstrap}.go` |
| `_auth/account*.py` (5 files) | — | `internal/auth/account` |
| `_auth/storage_writer.py`, `storage_transaction.py`, `browser_cookie_recovery.py`, `browser_state_validation.py`, `_browser_cookie_filter.py`, `login_wait_trace.py` | — | **dropped** — Python deprecation shims that define nothing |

## Public SDK

| Python | Lines | Go |
|---|---|---|
| `client.py` | 958 | `notebooklm/client.go` + `options.go` |
| `_notebooks.py` + `_web/notebooks.py` | 376+ | `notebooklm/notebooks.go`, `internal/web/features/notebooks.go` |
| `_sources.py` + `_web/sources/*` | 397+3084 | `notebooklm/sources.go`, `internal/web/sources/*` |
| `_artifacts.py` + `_web/artifacts.py` + `_web/artifact/*` | 762+ | `notebooklm/artifacts.go`, `internal/web/artifact/*` |
| `_chat.py` + `_web/chat.py` | 690+ | `notebooklm/chat.go`, `internal/web/features/chat.go` |
| `_notes.py` + `_web/notes.py` | 185+ | `notebooklm/notes.go` |
| `_mind_maps_api.py` + `_web/mind_maps.py` | 382+ | `notebooklm/mindmaps.go` |
| `_research.py` + `_web/research*.py` | 513+ | `notebooklm/research.go` |
| `_sharing.py` + `_web/sharing.py` | 106+ | `notebooklm/sharing.go` |
| `_labels.py` + `_web/labels.py` | 106+ | `notebooklm/labels.go` |
| `_collections.py` + `_web/collections.py` | 80+ | `notebooklm/collections.go` |
| `_settings.py` + `_web/settings.py` | 28+ | `notebooklm/settings.go` |
| `_conversation_cache.py` | 120 | `notebooklm/chat.go` (cache) |
| `_notebook_metadata.py` | 92 | `internal/app/metadata` |
| `exceptions.py` | 1580 | `notebooklm/errors.go` |
| `types.py` + `_types/*` | 384+5092 | `notebooklm/types_*.go` + `enums.go` |
| `_artifact/downloads.py` | 443 | `internal/web/assets/download.go` |
| `_artifact/_download_client.py`, `_redirect_guard.py` | 215 | `internal/web/assets/guard.go` |
| `_artifact/polling.py` | 498 | `internal/web/artifact/polling.go` |
| `_artifact/formatters.py` | 121 | `internal/artifact/formatters` |
| `_artifact/validation.py` | 52 | `internal/web/artifact/validate.go` |
| `artifacts.py`, `auth.py`, `research.py`, `io.py`, `log.py`, `urls.py`, `config.py`, `migration.py`, `_deprecation.py`, `_version_*.py` | ~800 | **mostly dropped** — Python facade/deprecation layers. `config.py` → `internal/config`; `_version_*` → `internal/buildinfo` |

## Params and rows

Mechanical 1:1. `_web/params/X.py` → `internal/web/params/X.go`;
`_web/rows/X.py` → `internal/web/rows/X.go`.

| Python | Lines | Notes |
|---|---|---|
| `params/artifacts.py` | 654 | all ten generate builders |
| `params/notebooks.py` | 161 | |
| `params/chat_note.py` | 184 | UTF-16 offsets, integer render flags |
| `params/collections.py` | 120 | |
| `params/sources.py` | 96 | + the Scotty start-request builder |
| `params/labels.py` | 88 | |
| `params/chat_stream.py` | 88 | |
| `rows/sources.py` | 1385 | the largest row decoder |
| `rows/artifacts.py` | 1244 | type + variant resolution |
| `rows/chat.py` | 1199 | history and turns |
| `rows/chat_stream.py` | 1033 | the streaming parser |
| `rows/research.py` + `research_task.py` | 1034 | |
| `rows/documents.py` | 653 | structured document / fulltext |
| `rows/notebooks.py` | 385 | |
| `rows/notes.py` | 358 | |
| `rows/sharing.py` | 233 | |
| `rows/labels.py`, `collections.py` | 157 | |

## Application layer

`_app/X.py` → `internal/app/X/` (grouped where the Python file split was size-driven rather
than conceptual).

| Python | Go |
|---|---|
| `_app/errors.py` | `internal/app/errors` |
| `_app/resolve.py` | `internal/app/resolve` |
| `_app/generate.py`, `generate_plans.py`, `generate_retry.py` | `internal/app/generate` |
| `_app/download.py`, `download_specs.py` | `internal/app/download`, `internal/app/downloadspec` |
| `_app/source_add.py`, `source_batch.py`, `source_clean.py`, `source_content.py`, `source_listing.py`, `source_mutations.py`, `source_research.py`, `source_wait.py` | `internal/app/sourceadd`, `internal/app/sourcewait`, `internal/app/sources` |
| `_app/artifacts.py`, `chat.py`, `notebooks.py`, `notes.py`, `labels.py`, `collections.py`, `research.py`, `sharing.py` | `internal/app/<domain>` |
| `_app/auth_check.py`, `doctor.py`, `login_cookie.py`, `master_token.py`, `profile.py`, `session.py` | `internal/app/authcheck`, `internal/app/doctor`, `internal/app/session` |
| `_app/serialize.py`, `views.py`, `events.py`, `pagination.py` | `internal/app/serialize`, `internal/app/paginate` |
| `_app/skill.py`, `mcp_install.py`, `language.py` | `internal/app/skill`, `internal/app/mcpinstall`, `internal/app/language` |

## Adapters

| Python | Lines | Go |
|---|---|---|
| `notebooklm_cli.py` | 330 | `cmd/notebooklm/main.go` + `internal/cli/root.go` |
| `cli/*_cmd.py` (17 files) | ~4000 | `internal/cli/cmd_<domain>.go` |
| `cli/services/*` (25 files) | ~3000 | **mostly dropped** — this logic belongs in `internal/app`. Only genuinely CLI-shaped concerns (rendering, prompts, progress) stay in `internal/cli`. |
| `cli/rendering.py`, `grouped.py`, `polling_ui.py`, `_session_render.py`, `_source_render.py` | — | `internal/cli/render`, `internal/cli/theme` |
| `cli/error_handler.py` | — | `internal/cli/errors.go` |
| `cli/context.py` | — | `internal/cli/context.go` |
| `cli/resolve.py` | — | thin wrapper over `internal/app/resolve` |
| `cli/options.py`, `completion.py` | — | `internal/cli/flags.go`, `internal/cli/completion.go` |
| `cli/services/login/*` (13 files) | — | `internal/auth/browser/import.go` (kooky) |
| `cli/_chromium_profiles.py`, `_firefox_containers.py`, `_cookie_import.py` | — | folded into `internal/auth/browser/import.go` — **kooky replaces most of this** |
| `cli/auth_runtime.py`, `runtime.py`, `helpers.py` | — | `internal/cli/bootstrap.go` |
| `mcp/server.py` + `mcp/tools/*` | ~2500 | `internal/mcpsrv/*` |
| `mcp/_auth.py`, `_host_guard.py`, `_oauth.py` | — | `internal/mcpsrv/auth.go` |
| `mcp/_confirm.py`, `_coerce.py`, `_resolve.py`, `_paginate.py`, `_errors.py` | — | `internal/mcpsrv/helpers.go` |
| `mcp/_filelink.py`, `_fileroutes.py`, `_uploadwidget.py`, `_urlcheck.py` | — | `internal/mcpsrv/files.go` |
| `server/app.py` + `server/routes/*` | ~2000 | `internal/restsrv/*` |
| `server/_auth.py`, `_limits.py`, `_pending.py`, `_pagination.py`, `_errors.py`, `_context.py` | — | `internal/restsrv/{auth,limits,pending}.go` |

## Deferred to v2

`_android/` — 90 files including generated protobuf stubs for
`google.internal.labs.tailwind.orchestration.v1`. Not ported in v1. See doc 01 for the
rationale and doc 02 for the `Backend` interface seam that lets it land without a refactor.

## Tooling

| Python | Go |
|---|---|
| `scripts/check_rpc_health.py` | `internal/tools/rpchealth` (nightly canary) |
| `scripts/scrub_rpc_har.py`, `rescrub-cassettes.py` | `internal/tools/scrubhar` |
| `scripts/capture_rpc_registry.py` | `internal/tools/rpchealth` |
| `scripts/audit_public_api_compat.py` | `internal/tools/parityaudit` |
| `scripts/check_docs_module_refs.py`, `check_claude_md_freshness.py` | `internal/tools/docscheck` |
| `tests/_guardrails/test_*_boundary.py` | `internal/tools/boundarycheck` |
| `scripts/regenerate_android_protos.py`, `decode_mobile_grpc.py`, `capture_mobile_grpc.js`, `extract_blutter_grpc_signatures.py`, `android_grpc_canary.py`, `parse_pbschema.py` | **v2** |
| the remaining ~20 one-off audit scripts | **dropped** — artifacts of the Python refactor program |

## What shrinks, and why

The Python original is ~137k lines. A faithful Go port lands around **45–55k lines**, and the
reduction is not from cutting features:

| Source of reduction | Approx. |
|---|---|
| `_android/` deferred (mostly generated protobuf) | ~25k |
| Deprecation shims, re-export facades, compatibility aliases | ~8k |
| The loop-affinity contract and its enforcement across every seam | ~2k |
| `cli/services/` logic that belongs in `internal/app` and is deduplicated there | ~2k |
| Docstrings that are genuinely narrative rather than reference — this doc set carries that weight instead | ~15k |
| Python-specific test-seam machinery (`ClientSeams`, `FakeSession`, monkeypatch policy) | ~2k |

What does **not** shrink: the wire shapes, the enums, the row decoders, the auth ladder, and
the guardrails. Those are the value.
