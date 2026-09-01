# Sprint 1 — Ticket Breakdown (Phases 0–3)

## Sprint goal
Stand up the `notebooklm-go` repository skeleton, the `internal/web/wire` + `internal/web/policy` wire layer, the `internal/auth` cookie/storage/paths stack, and the running HTTP transport kernel that ties them together — so that by end of Sprint 1 the library can talk to NotebookLM at the protocol level and `make check` is green on `main`. After S1 nothing past it talks RPC by hand; everything downstream consumes `notebooklm.SDK`.

## Tickets

### Phase 0 — Repository foundation
Phase issue: #<phase-issue>

#### T-P0-1: scaffold — module, Makefile, CI matrix, `.gitignore`
- **Parent phase issue:** #<phase-issue>
- **One-line description:** Create `github.com/raihankhan/notebooklm-go` Go 1.25 module with the full `Makefile` target set, the `.golangci.yml` lint config, the 6-platform CI matrix, and a `.gitignore` that includes `.worktrees/`.
- **Files touched:** `go.mod`, `Makefile`, `.golangci.yml`, `.github/workflows/ci.yml`, `.gitignore`, root `AGENTS.md`, root `CLAUDE.md`, `README.md`, `cmd/notebooklm/.gitkeep` (so `go build ./...` succeeds before any code lands).
- **Acceptance criteria:**
  - [ ] `go.mod` declares `module github.com/raihankhan/notebooklm-go` with `go 1.25`.
  - [ ] `make build`, `make check`, `make fmt`, `make vet`, `make lint`, `make test`, `make test-e2e`, `make cover`, `make boundarycheck`, `make clean`, `make release` are all wired (even if some are stubs that call through to `go …` for now).
  - [ ] `.golangci.yml` enables the 11 linters listed in Phase 0 deliverables.
  - [ ] CI matrix covers `{linux,darwin,windows} × {amd64,arm64}` builds and tests on `linux/amd64` + `darwin/arm64`.
  - [ ] `.gitignore` contains `.worktrees/` (verified by `grep -F '.worktrees/' .gitignore`).
  - [ ] `go run ./cmd/notebooklm --help` exits 0 (even if the help text is "not implemented").
- **Tests required:** A smoke test that asserts `make check` exits 0 on an empty module (only `fmt`, `vet`, `lint`, `build` matter at this point; `test`/`cover`/`boundarycheck` are stubbed until T-P0-2 / T-P0-3 land).
- **Risk:** low — pure scaffolding, no runtime logic.
- **Dependencies:** none.
- **Suggested commit message:** `scaffold: bootstrap module, Makefile, CI matrix, and worktree gitignore (#<ticket>)`
- **Suggested PR title:** `scaffold: bootstrap module, Makefile, CI matrix, and worktree gitignore`

#### T-P0-2: buildinfo + logging — version injection and `slog` handler with redaction hook
- **Parent phase issue:** #<phase-issue>
- **One-line description:** Land `internal/buildinfo` (version/commit/date via `-ldflags`) and `internal/logging` (stderr-only `log/slog` handler, level from `NOTEBOOKLM_LOG_LEVEL` / `-v`, context-bound request-id correlation, and a `slog.ReplaceAttr` hook that routes every attribute through `internal/redact`).
- **Files touched:** `internal/buildinfo/buildinfo.go`, `internal/buildinfo/buildinfo_test.go`, `internal/logging/logger.go`, `internal/logging/handler.go`, `internal/logging/redact_hook.go`, `internal/logging/logger_test.go`; `Makefile` updated to pass `-ldflags` to `make build`.
- **Acceptance criteria:**
  - [ ] `internal/buildinfo` exposes `Version`, `Commit`, `Date` populated from `-ldflags "-X …/buildinfo.version=…"`.
  - [ ] `internal/logging.New(...)` writes to `stderr` only — never `stdout`.
  - [ ] Setting `NOTEBOOKLM_LOG_LEVEL=debug` flips level to debug; `-v` (count) raises it from `info` upward.
  - [ ] Context-carried request id propagates to every log record under the same key.
  - [ ] A test that logs a struct holding a redacted value asserts the on-wire line is masked (the redaction hook ran).
  - [ ] `go vet ./...` and `golangci-lint run ./...` pass on the new packages.
- **Tests required:** Unit tests for level parsing, stderr-only write, request-id propagation, and redaction hook.
- **Risk:** low — well-bounded packages, no I/O outside stderr.
- **Dependencies:** T-P0-1 (needs `go.mod` + Makefile); the redaction hook calls into `internal/redact` added in T-P0-3, so this ticket must declare a small interface (`type redactor interface { Apply([]byte) []byte }`) that `internal/redact` satisfies later — no direct import of `internal/redact` from `internal/logging` yet.
- **Suggested commit message:** `logging: add internal/buildinfo and stderr-only slog handler with redaction hook (#<ticket>)`
- **Suggested PR title:** `logging: add internal/buildinfo and stderr-only slog handler with redaction hook`

#### T-P0-3: redact — port `_logging.py` regex families and URL redactors
- **Parent phase issue:** #<phase-issue>
- **One-line description:** Port the four credential-regex families from `notebooklm-py/_logging.py` (quoted JSON, HTML-escaped JSON, form/prose, bare tokens) plus URL redactors for `?secret=`, `;session=`, cookies, and `Authorization` headers into `internal/redact`, satisfying Phase 0's redaction table test.
- **Files touched:** `internal/redact/redact.go`, `internal/redact/redact_test.go`, `internal/redact/testdata/fixtures.json` (the 12+ fixture values referenced in the test).
- **Acceptance criteria:**
  - [ ] `internal/redact.Apply([]byte) []byte` masks values for all four regex families.
  - [ ] `SNlM0e`, `FdrFJe`, and a `Cookie:` value are masked in all four shapes (table test, one row per shape).
  - [ ] URL redactors cover query `?secret=`, semicolon `;session=`, `Cookie:` header, and `Authorization:` header forms.
  - [ ] `internal/logging` now imports `internal/redact` (replacing the stub interface from T-P0-2); an end-to-end test logs a known-secret and asserts the on-wire byte is masked.
  - [ ] 100% line coverage on `internal/redact` (small surface; the gate is strict).
- **Tests required:** Table-driven redaction fixtures; end-to-end logging integration test.
- **Risk:** low — pure-function package, but it is the credential-handling primitive the whole project will lean on, so the table test is non-negotiable.
- **Dependencies:** T-P0-1 (module); soft-depends on T-P0-2 (interface forward-decl) — the merge of T-P0-3 makes `internal/logging` resolve its redaction hook to the real `internal.redact` package.
- **Suggested commit message:** `wire: port credential redaction regexes from notebooklm-py/_logging (#<ticket>)`
- **Suggested PR title:** `wire: port credential redaction regexes from notebooklm-py/_logging`

#### T-P0-4: boundarycheck — declarative import-graph linter with planted-failure test
- **Parent phase issue:** #<phase-issue>
- **One-line description:** Land `internal/tools/boundarycheck` reading a declarative `boundaries.yaml` that mirrors AGENTS.md rule 5 (with F3 rows added in Phase 14), and a CI test that proves a deliberately planted bad import is rejected.
- **Files touched:** `internal/tools/boundarycheck/main.go`, `internal/tools/boundarycheck/main_test.go`, `boundaries.yaml`, `Makefile` (wire `boundarycheck` target to `go run ./internal/tools/boundarycheck`).
- **Acceptance criteria:**
  - [ ] `boundaries.yaml` encodes every row from the Phase 0/1/2 boundary table: `internal/app`, `internal/cli`, `internal/mcpsrv`, `internal/restsrv`, `internal/web/wire` (stdlib-only).
  - [ ] `go run ./internal/tools/boundarycheck` exits 0 on the current tree.
  - [ ] A test plants a bad import (e.g., `internal/web/wire` importing `notebooklm`) and asserts the linter exits non-zero with a message naming the violated rule.
  - [ ] The test then removes the planted import and re-asserts exit 0.
  - [ ] `make boundarycheck` is wired into `make check`.
- **Tests required:** The planted-import positive/negative test; a small unit test that `LoadRules` parses the YAML into the expected struct shape.
- **Risk:** medium — the linter is the gate that every later PR has to pass; a misrule here blocks the whole loop. Mitigated by the planted-failure test.
- **Dependencies:** T-P0-1 (Makefile + module).
- **Suggested commit message:** `tools: add internal/tools/boundarycheck with declarative rules + planted-failure test (#<ticket>)`
- **Suggested PR title:** `tools: add internal/tools/boundarycheck with declarative rules + planted-failure test`

---

### Phase 1 — The wire layer
Phase issue: #<phase-issue>

> Phase 1 is the highest-leverage phase in the whole roadmap. Every later phase assumes the byte-level fidelity of these packages. Tickets are sequenced to lock the encoding primitive first (T-P1-1), then the JSON/encoding-error surface that everything in `internal/web` will pass through, then the per-method decode/encode work, then the policy registry.

#### T-P1-1: wire/json + wire/escape — `Marshal`, `Unmarshal`, `escapeAll`
- **Parent phase issue:** #<phase-issue>
- **One-line description:** Land `internal/web/wire/json.go` with `Marshal` (`SetEscapeHTML(false)`, newline-trimmed) and `Unmarshal` (`UseNumber()`), and `internal/web/wire/escape.go` with `escapeAll` (Python `quote(s, safe="")`-equivalent; space encodes as `%20`, never `+`). These are the only `encoding/json` importers in the module.
- **Files touched:** `internal/web/wire/json.go`, `internal/web/wire/json_test.go`, `internal/web/wire/escape.go`, `internal/web/wire/escape_test.go`, `internal/web/wire/doc.go` (notes the "only encoder/decoder" contract from AGENTS.md rule 3), `boundaries.yaml` (add `internal/web/wire` row — stdlib-only).
- **Acceptance criteria:**
  - [ ] `wire.Marshal(map[string]any{"<": "&"})` returns bytes containing the literal `<` and `&`, not `\u003c` / `\u0026`.
  - [ ] `wire.Marshal` output has no trailing newline (use `bytes.HasSuffix(b, []byte{'\n'})` to assert).
  - [ ] `wire.Unmarshal(b, &v)` decodes large integer ids as `json.Number`, not `float64` (assert `UseNumber` is set).
  - [ ] `wire.escapeAll(" ")` returns `"%20"`; `wire.escapeAll("/")` returns `"%2F"`; `wire.escapeAll("+")` returns `"%2B"`.
  - [ ] `escapeAll` table covers space, `/`, `:`, `,`, `[`, `]`, `+`, `%`, and a UTF-8 multi-byte sequence.
  - [ ] `boundarycheck` green with the `internal/web/wire` row active.
- **Tests required:** Byte-for-byte comparison against Python `json.dumps(separators=(",",":"))` for payloads containing `<`, `>`, `&`, non-ASCII, emoji, and `null`; escapeAll table; `UseNumber` assertion.
- **Risk:** high — every byte the wire layer emits is load-bearing; a wrong default here silently corrupts every later RPC.
- **Dependencies:** T-P0-1 (module), T-P0-4 (boundarycheck must be live so we can prove `wire/` stays stdlib-only).
- **Suggested commit message:** `wire: add internal/web/wire json and escapeAll primitives (#<ticket>)`
- **Suggested PR title:** `wire: add internal/web/wire json and escapeAll primitives`

#### T-P1-2: wire/methods + wire/urls + wire/status — method table, allowlist, gRPC label layer
- **Parent phase issue:** #<phase-issue>
- **One-line description:** Land `internal/web/wire/methods.go` (full `Method` table from doc 03, with `ResolveID` honoring `NOTEBOOKLM_RPC_OVERRIDES`), `internal/web/wire/urls.go` (endpoint builders + the three-host base-URL allowlist), and `internal/web/wire/status.go` (`GrpcStatusCode` labels, `SanitizeStatusMessage` with the 300-char cap, `UserDisplayableError` depth-capped marker scan, and the account-routing hint text).
- **Files touched:** `internal/web/wire/methods.go`, `internal/web/wire/methods_test.go`, `internal/web/wire/urls.go`, `internal/web/wire/urls_test.go`, `internal/web/wire/status.go`, `internal/web/wire/status_test.go`, `internal/web/wire/testdata/method_overrides.json` (override-fixture for `ResolveID`).
- **Acceptance criteria:**
  - [ ] Every method id from doc 03's table is present in `methods.go` (asserted by a test reading a committed list of expected `(method_name, rpc_id)` pairs).
  - [ ] `ResolveID("SomeName")` returns the table id; with `NOTEBOOKLM_RPC_OVERRIDES` set to a JSON file overriding `SomeName`, returns the override id and emits exactly one log line containing the override set's hash (never the values).
  - [ ] `urls.go` exposes builders for the three hosts; `IsAllowedHost(url)` returns false for any host not on the allowlist.
  - [ ] `SanitizeStatusMessage("  hello  world  ")` returns the whitespace-collapsed form, capped at 300 chars.
  - [ ] `SanitizeStatusMessage(42)` (a non-string) returns a degraded message and emits a `warn` log (no panic).
  - [ ] `UserDisplayableError` marker scan caps recursion at the documented depth and returns the marker text without unwrapping.
- **Tests required:** Method-table presence test; override + hash-log test; allowlist table; status-sanitize table including the non-string degradation; depth-cap test for the marker scan.
- **Risk:** high — method ids are the #1 breakage class per AGENTS.md; the override path is the operator's only escape hatch when they change.
- **Dependencies:** T-P1-1 (the wire package itself exists).
- **Suggested commit message:** `wire: port Method table, host allowlist, and gRPC status layer from notebooklm-py (#<ticket>)`
- **Suggested PR title:** `wire: port Method table, host allowlist, and gRPC status layer from notebooklm-py`

#### T-P1-3: wire/encode + wire/decode + wire/index — encode/decode with golden bytes and chunked-parser fixtures
- **Parent phase issue:** #<phase-issue>
- **One-line description:** Land `internal/web/wire/encode.go` (`EncodeRequest`, `BuildRequestBody`, `NestSourceIDs`, `TemplateBlock`, `ArtifactOptions`) and `internal/web/wire/decode.go` (`StripAntiXSSI`, `ParseChunked` with the three 10% malformed-rate gates and `byteCountMismatchTotal`, `CollectRPCIDs`, `ExtractResult`, `DecodeResponse` with the null-result classification tree), plus `internal/web/wire/index.go` (`At`/`Str`/`Int`/`Bool`/`List` + `Opt*` variants, all returning `*ShapeDriftError`).
- **Files touched:** `internal/web/wire/encode.go`, `internal/web/wire/encode_test.go`, `internal/web/wire/decode.go`, `internal/web/wire/decode_test.go`, `internal/web/wire/index.go`, `internal/web/wire/index_test.go`, `internal/web/wire/testdata/golden/*.json` (Python-generated golden bytes, one per RPC), `internal/web/wire/testdata/chunked/*.txt` (the 10 chunked-parser fixtures).
- **Acceptance criteria:**
  - [ ] For every RPC in doc 03, `EncodeRequest(method, params, resolvedID)` produces bytes that match the committed Python-generated golden file byte-for-byte.
  - [ ] `NestSourceIDs([]string{"a"}, 1)` returns `["a"]`; depth 2 returns `[["a"]]`; depth 3 returns `[[["a"]]]`; nil/empty inputs return `nil` (no panic).
  - [ ] `ParseChunked` handles all 10 documented fixtures: single frame · multi-frame with null placeholder first · byte-count mismatch · `\r\n` framing · malformed-under-10% · malformed-over-10% · framing error · `er` frame · bare `[5]` status · `[8, null, [[UserDisplayableError…]]]`, plus absent rpc id, zero rpc ids, and a deeply nested JSON case.
  - [ ] The `byteCountMismatchTotal` counter increments exactly when expected (verified with a fake `metrics.Sink`).
  - [ ] `At`/`Str`/`Int`/`Bool`/`List` return `*ShapeDriftError` on type mismatch; `Opt*` variants return zero values without error on missing keys.
  - [ ] `DecodeResponse` correctly classifies every null-result branch in the documented tree.
  - [ ] `make cover` on `internal/web/wire` reports ≥90%.
- **Tests required:** Golden-encode table; `NestSourceIDs` table; chunked-parser fixture set; `At`/`Opt*` type-mismatch table; null-result classification table.
- **Risk:** high — every later RPC test will replay cassettes against this code; the golden-bytes table is the single most valuable test file in the repo.
- **Dependencies:** T-P1-1 (json primitives), T-P1-2 (method ids).
- **Suggested commit message:** `wire: implement encode, chunked decode, and positional index helpers with golden tests (#<ticket>)`
- **Suggested PR title:** `wire: implement encode, chunked decode, and positional index helpers with golden tests`

#### T-P1-4: policy — five-class `IdempotencyRegistry` with startup assertion
- **Parent phase issue:** #<phase-issue>
- **One-line description:** Land `internal/web/policy` with the five-class `IdempotencyRegistry` keyed by `(Method, variant)` and a startup assertion that every declared `Method` has an entry (an unregistered RPC fails `init` of the registry, by design).
- **Files touched:** `internal/web/policy/registry.go`, `internal/web/policy/registry_test.go`, `internal/web/policy/doc.go`, `boundaries.yaml` (extend `internal/web/wire` row to also cover `internal/web/policy`).
- **Acceptance criteria:**
  - [ ] `policy.MustRegister(...)` panics on duplicate registration; `policy.NewRegistry(methods)` panics on any `Method` lacking an entry — asserted by a unit test that builds a registry over the Phase 1 method table and asserts it succeeds, then drops one method and asserts the panic.
  - [ ] Five idempotency classes are defined per doc 03 (`safe` / `read-only` / `probe-then-create` / `idempotent-mutation` / `unsafe-mutation`); `Classify("SomeMethod")` returns the class.
  - [ ] A `ProbeThenCreate` entry registered without a rationale fails a `Validate()` call (the test asserts the failure message names the missing rationale field).
  - [ ] `make cover` on `internal/web/policy` reports 100% (the registry is small and gating).
- **Tests required:** Coverage-exhaustion test (every `Method` has an entry); rationale-required test; classification test.
- **Risk:** medium — the registry is the gate the transport uses to decide whether to retry; getting the class wrong either retries a destructive call (duplicates) or fails to retry a safe one (visible flake).
- **Dependencies:** T-P1-2 (the `Method` table this ticket indexes over).
- **Suggested commit message:** `policy: add five-class IdempotencyRegistry with startup assertion (#<ticket>)`
- **Suggested PR title:** `policy: add five-class IdempotencyRegistry with startup assertion`

---

### Phase 2 — Auth + session
Phase issue: #<phase-issue>

> Phase 2 owns every credential the user owns. The packages in this phase must use `internal/atomicio` for any write to `storage_state.json` (rule 4 + F2) and must round-trip with the Python CLI's `storage_state.json` byte-for-byte on the parity attributes. The split is: cookie jar (RFC 6265), storage I/O + cross-compat normalization, atomic-write primitive + paths, and the policy layer (allowlist + minimum-required set + binding matrix).

#### T-P2-1: cookiejar — RFC 6265 `http.CookieJar` with `__Secure-`/`__Host-` enforcement
- **Parent phase issue:** #<phase-issue>
- **One-line description:** Land `internal/auth/cookiejar` with the `Cookie` struct and `Jar` (implementing `http.CookieJar`), RFC 6265 §5.3 storage keyed by `(name, domain, path)`, §5.4 selection ordering, `Secure`-awareness, public-suffix rejection, and `__Secure-`/`__Host-` prefix enforcement, plus `All()`, `Snapshot()`, `HeaderFor(url)`.
- **Files touched:** `internal/auth/cookiejar/jar.go`, `internal/auth/cookiejar/cookie.go`, `internal/auth/cookiejar/jar_test.go`, `internal/auth/cookiejar/testdata/selections.json` (§5.4 selection matrix fixture).
- **Acceptance criteria:**
  - [ ] Setting then getting a cookie on `(name, domain, path)` round-trips through `Cookies(url)`.
  - [ ] §5.4 selection table passes — including the `OSID`-on-two-hosts case that must **not** collapse into one entry (the test asserts two distinct cookies with the same name return the longest-path match for each host).
  - [ ] `Secure`-flagged cookies are not returned for `http://` URLs.
  - [ ] A cookie set with `Domain: example.com` from a domain on the public-suffix list is rejected (`ErrPublicSuffix`).
  - [ ] `__Secure-` and `__Host-` prefix violations are rejected with typed errors (`ErrInsecurePrefix`, `ErrHostPrefixOnSubpath`).
  - [ ] `HeaderFor(url)` returns a `Cookie:` header whose values are redacted by the standard `String()` on the cookie (no value leak).
  - **Tests required:** Round-trip fuzz over random cookie sets (weird domains, dotted/undotted, all `SameSite` values, session and persistent, prefix cookies) — 100k iterations; §5.4 selection table including the `OSID`-on-two-hosts case.
- **Risk:** high — the cookie jar is the foundation of every authenticated call; a selection-order bug silently logs the user out without an error.
- **Dependencies:** T-P0-3 (redaction) — `Cookie.String()` must mask the value.
- **Suggested commit message:** `auth: add RFC 6265 cookie jar with prefix and public-suffix enforcement (#<ticket>)`
- **Suggested PR title:** `auth: add RFC 6265 cookie jar with prefix and public-suffix enforcement`

#### T-P2-2: storage — lossless `storage_state.json` round-trip with cross-CLI normalization
- **Parent phase issue:** #<phase-issue>
- **One-line description:** Land `internal/auth/storage` with `storage_state.json` read/write (lossless attribute round-trip, including `expires: -1` ⇄ `nil` and `sameSite`), import normalizers (`expirationDate` → `expires`, `hostOnly`, `sameSite` case forms, bare cookie arrays, `origins` dropped), the `notebooklm.account` in-band namespace, and `context.json` legacy read + in-band promotion.
- **Files touched:** `internal/auth/storage/storage.go`, `internal/auth/storage/normalizers.go`, `internal/auth/storage/storage_test.go`, `internal/auth/storage/testdata/python_storage_state.json` (a fixture produced by the Python CLI), `internal/auth/storage/testdata/legacy_context.json` (legacy context.json fixture).
- **Acceptance criteria:**
  - [ ] Write a cookie set with `Expires: -1` (session cookie) and read it back as `Expires: nil` and back to `-1` on rewrite (no attribute loss).
  - [ ] All four `SameSite` enum case forms (`Strict`/`strict`/`Lax`/`lax`) normalize to the canonical form on read.
  - [ ] Read the committed Python-produced `storage_state.json` fixture and assert every attribute survives (round-trip table test).
  - [ ] Write a Go-produced `storage_state.json`, parse it with the Python loader's expectations (asserted by reading it back through this same Go reader), and assert byte-identical output for the no-op open/close case (cross-compat).
  - [ ] A bare cookie array (no `cookies` key wrapper) normalizes into the documented shape.
  - [ ] The `origins` key in the input is dropped on read (not echoed to output).
  - [ ] `context.json` legacy format is read into the in-band namespace; a second write promotes it and stops reading the legacy file.
- **Tests required:** Round-trip table; cross-CLI compatibility test (commit a Python-produced fixture); legacy-context promotion test.
- **Risk:** high — credential files are account-takeover targets; an attribute loss here produces a logged-out user with no error.
- **Dependencies:** none within Phase 2; logically depends on T-P2-4 (atomicio) but only at write time, and that wiring is straightforward (one `WriteFile` call site to swap). Acceptable to land atomicio-aware writes in a follow-up.
- **Suggested commit message:** `auth: add lossless storage_state read/write with Python-CLI normalization (#<ticket>)`
- **Suggested PR title:** `auth: add lossless storage_state read/write with Python-CLI normalization`

#### T-P2-3: policy — domain allowlist, `MinimumRequiredCookies`, and binding matrix
- **Parent phase issue:** #<phase-issue>
- **One-line description:** Land `internal/auth/policy` with the host allowlist (required + optional labels + regional ccTLDs), the `MinimumRequiredCookies` set, the Tier 2 binding matrix, the typed validation reasons (`missing_cookie`, `psidts_unroutable`, `no_secondary_binding`), and the two-host diagnostic messages.
- **Files touched:** `internal/auth/policy/allowlist.go`, `internal/auth/policy/minimum.go`, `internal/auth/policy/binding.go`, `internal/auth/policy/policy.go`, `internal/auth/policy/policy_test.go`, `internal/auth/policy/testdata/hosts.txt` (committed allowlist snapshot for drift detection).
- **Acceptance criteria:**
  - [ ] `IsAllowedHost("notebooklm.google.com")` returns true; `"evil.example"` returns false.
  - [ ] Each required + optional + regional ccTLD host from doc 05 is enumerated and asserted present by a test.
  - [ ] `Validate(jar)` returns `missing_cookie` (typed) when `__Secure-1PSID` is absent; `psidts_unroutable` when present but `__Secure-3PSIDTS` is missing; `no_secondary_binding` when the Tier 2 secondary cookie is missing.
  - [ ] Two-host diagnostic messages contain the exact strings from doc 05 (asserted via a committed `want` table).
  - [ ] `hosts.txt` drift test fails CI if a host is added without bumping a `schema_version` line at the top of the file.
- **Tests required:** Typed-validation-reason table; two-host diagnostic message table; drift test.
- **Risk:** medium — wrong typed errors here would cause the refresh ladder to take the wrong branch in Phase 4; the typed reasons are the contract.
- **Dependencies:** none within Phase 2 (this ticket only imports stdlib + `internal/redact` for the diagnostic messages).
- **Suggested commit message:** `auth: add policy allowlist, minimum-cookie set, and binding-matrix diagnostics (#<ticket>)`
- **Suggested PR title:** `auth: add policy allowlist, minimum-cookie set, and binding-matrix diagnostics`

#### T-P2-4: atomicio + paths — atomic-write primitive, `0700` dirs, `NOTEBOOKLM_HOME` resolution
- **Parent phase issue:** #<phase-issue>
- **One-line description:** Land `internal/atomicio` (temp + chmod 0600 + sync + rename, `0700` directory creation with the Windows ACL carve-out, `.bak` rollback, the four `flock` derivations from one path function plus per-path in-process mutexes) and `internal/paths` (`NOTEBOOKLM_HOME`, profile dirs, legacy fallback for the `default` profile only, `config.json` with an mtime cache, `PathInfo` for `status --paths`).
- **Files touched:** `internal/atomicio/atomic.go`, `internal/atomicio/flock.go`, `internal/atomicio/atomic_test.go`, `internal/atomicio/flock_test.go`, `internal/paths/home.go`, `internal/paths/profile.go`, `internal/paths/config.go`, `internal/paths/paths_test.go`, `boundaries.yaml` (add `internal/atomicio` and `internal/paths` rows: stdlib + `internal/redact`).
- **Acceptance criteria:**
  - [ ] `atomicio.WriteFile(path, data, 0o600)` writes a temp file, fsyncs, renames over the destination, and the destination has mode `0600` on POSIX.
  - [ ] Crash-simulation test: `atomicio.WriteFile` is interrupted between temp-write and rename (test writes temp, skips rename); the destination remains the previous contents.
  - [ ] `atomicio.MkdirAll(path, 0o700)` creates parent dirs at mode `0700` on POSIX.
  - [ ] Four `flock` derivations (RD/WR + blocking/non-blocking) are implemented from one path function and unit-tested with two concurrent goroutines + two concurrent processes.
  - [ ] `paths.Home()` returns `~/.notebooklm/` by default and honors `NOTEBOOKLM_HOME`.
  - [ ] `paths.ProfileDir("default")` returns `~/.notebooklm/profiles/default/`; `paths.ProfileDir("work")` returns `~/.notebooklm/profiles/work/`; the legacy `~/.notebooklm/` (no profile) is read as the `default` profile only.
  - [ ] `paths.Config()` returns `~/.notebooklm/config.json` and caches its mtime; cache is invalidated on write.
  - [ ] Windows-specific code is gated by `//go:build windows` and skip-gated in non-Windows CI.
- **Tests required:** Crash-simulation test; concurrent `flock` test (2 goroutines + 2 processes via `exec`); POSIX permission assertions; legacy-fallback test.
- **Risk:** medium — this is the primitive every later write goes through; a partial write that survives here corrupts credentials silently.
- **Dependencies:** none within Phase 2; this ticket is the foundation T-P2-2's storage writes will migrate onto.
- **Suggested commit message:** `auth: add internal/atomicio (atomic 0600/0700 writes + flock) and internal/paths (#<ticket>)`
- **Suggested PR title:** `auth: add internal/atomicio (atomic 0600/0700 writes + flock) and internal/paths`

---

### Phase 3 — Public library API (transport + runtime + config)
Phase issue: #<phase-issue>

> Phase 3 wires the wire layer (Phase 1) + the auth stack (Phase 2) into a runnable HTTP transport. The split keeps the runtime lifecycle primitive separate from the transport kernel (so Phase 4 can build the refresh ladder on top of a stable `Supervisor`), and the middleware wiring in its own ticket (so the four-middleware order is reviewed in isolation as a small, reviewable PR).

#### T-P3-1: runtime — `Lifecycle`, `Supervisor`, `Metrics`, `deadline.Budget`
- **Parent phase issue:** #<phase-issue>
- **One-line description:** Land `internal/runtime` with `Lifecycle` (open/close waves, monotonic resource generations, phased participant ordering, rollback on a failed open, deterministic teardown failure precedence), `Supervisor` (drain admission → metrics → semaphore; call and operation leases; cancellation-safe settlement; race-free admitted child spawning), `Metrics` (atomic counters `rpcCallsStarted/Succeeded/Failed`, `rpcAuthRetries`, `rpcDecodeErrors`, `queueWaitSeconds`, `byteCountMismatchTotal` + the `RPCEvent` callback fan-out), and `deadline.Budget` (aggregate deadline with `Remaining()` and `Expired()`).
- **Files touched:** `internal/runtime/lifecycle.go`, `internal/runtime/supervisor.go`, `internal/runtime/metrics.go`, `internal/runtime/deadline.go`, `internal/runtime/runtime_test.go`, `internal/runtime/lifecycle_test.go`, `internal/runtime/supervisor_test.go`, `internal/runtime/deadline_test.go`, `boundaries.yaml` (add `internal/runtime` row: stdlib + `internal/redact`).
- **Acceptance criteria:**
  - [ ] `Lifecycle.Open()` registers participants in the declared phase order; if phase 2 fails, phases already opened are rolled back in reverse order (test asserts the call sequence).
  - [ ] `Metrics.Snapshot()` is lock-free (atomic loads only); `RPCEvent` callbacks are invoked exactly once per event, fan-out safe under concurrent emitters.
  - [ ] `Supervisor.Admit(ctx, op)` returns a lease that, when `Release()`d, decrements the in-flight counter exactly once even under concurrent release.
  - [ ] `deadline.Budget.Remaining()` decreases monotonically; `Expired()` flips true at zero; a decode-time retry does not extend the T0-anchored budget (test asserts the same `Remaining()` across a 200 ms retry).
  - [ ] `go test -race -count=20 ./internal/runtime/...` is green.
- **Tests required:** Lifecycle open/rollback order test; supervisor concurrent admit/release race test; metrics atomicity test; deadline-budget non-extension test.
- **Risk:** medium — every transport call goes through this; a deadlock here stops the whole binary.
- **Dependencies:** none within Phase 3 (this ticket depends only on stdlib + `internal/redact`).
- **Suggested commit message:** `runtime: add Lifecycle, Supervisor, Metrics, and aggregate deadline.Budget (#<ticket>)`
- **Suggested PR title:** `runtime: add Lifecycle, Supervisor, Metrics, and aggregate deadline.Budget`

#### T-P3-2: transport/kernel + transport/errors + transport/stream — HTTP kernel with epoch fencing
- **Parent phase issue:** #<phase-issue>
- **One-line description:** Land `internal/web/transport` `Kernel` (owns `*http.Client` + `Jar`, `Post` with response size cap, `ActivateEpoch`/`FenceEpoch`/`AssertEpoch`, `Close`), `errors.go` (transport error shapes and `Retry-After` parsing — both delta-seconds and HTTP-date forms), and `stream.go` (streaming POST with the size cap).
- **Files touched:** `internal/web/transport/kernel.go`, `internal/web/transport/errors.go`, `internal/web/transport/stream.go`, `internal/web/transport/kernel_test.go`, `internal/web/transport/errors_test.go`, `internal/web/transport/stream_test.go`, `boundaries.yaml` (extend `internal/web/wire` row coverage to `internal/web/transport`: `internal/web/wire` + `internal/runtime` + `internal/redact`).
- **Acceptance criteria:**
  - [ ] `Kernel.Post(ctx, url, body)` returns `ErrResponseTooLarge` when the response body exceeds the configured cap, with the actual byte count on the error.
  - [ ] `ParseRetryAfter("30")` returns `30 * time.Second`; `ParseRetryAfter("Wed, 21 Oct 2026 07:28:00 GMT")` returns the duration until that instant.
  - [ ] `ActivateEpoch` bumps the generation; `FenceEpoch` retires the prior generation; an in-flight POST issued under the prior generation is rejected by `AssertEpoch` with a typed error, and the request never touches the jar (test asserts the jar's `Cookies(url)` is not called after fencing).
  - [ ] Streaming POST respects the same size cap as the buffered path.
  - [ ] Epoch fencing test starts 10 RPCs, closes mid-flight, asserts every late POST is rejected with the retired-generation error and that the jar is untouched.
- **Tests required:** Response-cap test; `Retry-After` both-form parsing test; epoch-fencing concurrency test (10 goroutines, `close()` mid-flight); streaming cap test.
- **Risk:** high — the kernel is the line of defense against the stale-envelope and credential-leak bug classes.
- **Dependencies:** T-P3-1 (runtime metrics counters), T-P2-1 (the `Jar` interface kernel will hold).
- **Suggested commit message:** `transport: add Kernel with epoch fencing, size cap, and Retry-After parsing (#<ticket>)`
- **Suggested PR title:** `transport: add Kernel with epoch fencing, size cap, and Retry-After parsing`

#### T-P3-3: transport/Executor + transport/Runtime — RPC orchestration and the four-middleware chain
- **Parent phase issue:** #<phase-issue>
- **One-line description:** Land `internal/web/transport` `Executor` (one logical RPC: mint request id → consult the idempotency registry → resolve the method id → encode → dispatch through the chain → decode → map errors; owns the decode-time auth-refresh-and-retry leg and the shared `RefreshBudget`), `Runtime` (authed POST entry: loop-free epoch check, auth snapshot capture, envelope materialization, chain dispatch, **unconditional pre-POST rebuild in `terminal`**, the four middlewares wired in pinned order), and `internal/config` (env resolution, base-URL allowlist, `DEFAULT_BL` + build-label regex/staleness helpers, `NOTEBOOKLM_HL`).
- **Files touched:** `internal/web/transport/executor.go`, `internal/web/transport/runtime.go`, `internal/web/transport/middleware.go`, `internal/web/transport/refresh_budget.go`, `internal/web/transport/executor_test.go`, `internal/web/transport/runtime_test.go`, `internal/web/transport/middleware_test.go`, `internal/config/config.go`, `internal/config/buildlabel.go`, `internal/config/config_test.go`, `internal/web/transport/testdata/scenarios/*.json` (httptest scenario fixtures).
- **Acceptance criteria:**
  - [ ] `httptest` server fixtures cover: 429 with `Retry-After` (both forms) · 429 with retries disabled · 500 → success on retry · 5xx budget exhaustion · 401 → refresh → success · 401 → refresh → 429 → retry with the **rebuilt** envelope (the stale-envelope regression) · response over the size cap · connect timeout · read timeout.
  - [ ] Chain order pinned by a test asserting the constructed middleware names in sequence (regression-proofs the documented order).
  - [ ] Concurrency: 50 goroutines issuing RPCs while one refresh fires → exactly one refresh (asserted by a counter on a fake refresher).
  - [ ] `RefreshBudget`: a `wire-401 → refresh → decoded-auth-error` sequence performs exactly one refresh (not two).
  - [ ] **AST test**: an AST walk over `Executor` asserts no channel/lock/await-shaped operation sits between the envelope rebuild and `Kernel.Post` (this is the regression guard for the stale-envelope bug).
  - [ ] `internal/config` resolves `NOTEBOOKLM_HOME`, `NOTEBOOKLM_LOG_LEVEL`, `NOTEBOOKLM_RPC_OVERRIDES`, `NOTEBOOKLM_HL`, and the base-URL allowlist; `DEFAULT_BL` regex + staleness helper pass the committed fixture.
  - [ ] `go test -race -count=20 ./internal/web/transport/...` is green.
- **Tests required:** All nine httptest scenarios; chain-order assertion test; 50-goroutine single-refresh test; RefreshBudget one-refresh test; AST regression test; config env-override table.
- **Risk:** high — the stale-envelope regression is the most expensive bug class in the loop's history; the AST test is the guard against its return.
- **Dependencies:** T-P3-2 (Kernel + Retry-After parser), T-P1-4 (idempotency registry), T-P3-1 (`Metrics` counters the chain populates).
- **Suggested commit message:** `transport: add Executor, Runtime, and four-middleware chain with AST regression guard (#<ticket>)`
- **Suggested PR title:** `transport: add Executor, Runtime, and four-middleware chain with AST regression guard`

---

## Cross-phase dependency graph

```
Phase 0 — Foundation
  T-P0-1 (scaffold) ────────────────┐
       │                            │
       ▼                            ▼
  T-P0-2 (buildinfo+logging)   T-P0-4 (boundarycheck)
       │  (declares redactor         │
       │   interface, no real        │
       │   import yet)               │
       ▼                            │
  T-P0-3 (redact) ◄── merges ◄──────┘
       │  (replaces the stub interface
       │   in T-P0-2)

Phase 1 — Wire layer
  T-P0-1 ──► T-P1-1 (json+escape)
                │
                ▼
              T-P1-2 (methods+urls+status)
                │
                ▼
              T-P1-3 (encode+decode+index)         T-P1-4 (policy registry)
                   └────────────────┬───────────────┘
                                    ▼

Phase 2 — Auth + session
  T-P0-3 ──► T-P2-1 (cookiejar)         T-P2-3 (policy/allowlist)
                │                                  │
                ▼                                  │
            T-P2-2 (storage)         T-P2-4 (atomicio + paths)
                │  (writes will migrate to         │
                │   atomicio; can land first)      │
                └──────────────┬───────────────────┘
                               ▼

Phase 3 — Public library API
  T-P3-1 (runtime) ◄── stdlib only
       │
       ▼
  T-P2-1 ──► T-P3-2 (transport kernel) ◄── T-P3-1
                                     │
                                     ▼
              T-P1-4 + T-P3-1 + T-P3-2 ──► T-P3-3 (Executor + Runtime + chain)
```

**Hard cross-phase gates:**
- Phase 0 must complete (T-P0-3 merged, boundarycheck live) before any Phase 1 ticket opens.
- Phase 1 must complete (T-P1-3 + T-P1-4 merged) before any Phase 2 ticket opens that depends on the wire package — Phase 2 has no such dependency, but Phase 3's `T-P3-3` depends on `T-P1-4`.
- Phase 2 must complete (T-P2-1 + T-P2-4 merged) before `T-P3-2` (which holds the `Jar`).
- Phase 3 must complete before any S2 ticket.

## Parallelization plan

**Phase 0 (4 tickets):**
- **Wave 1 (1 parallel):** `T-P0-1` (scaffold) — must land first; every other Phase 0 ticket imports from the module it creates.
- **Wave 2 (3 parallel):** `T-P0-2`, `T-P0-3`, `T-P0-4` can all run in parallel from the same `main` tip after `T-P0-1` merges. They touch disjoint files (`internal/buildinfo`+`internal/logging`, `internal/redact`, `internal/tools/boundarycheck`+`boundaries.yaml`).
  - Caveat: `T-P0-2` and `T-P0-3` share `internal/logging` (T-P0-3 replaces the stub interface with the real `internal/redact` import). The Scrum-master serializes the merge: `T-P0-2` lands first with the stub interface; `T-P0-3` lands second and triggers a re-review of `internal/logging`'s redaction hook only.

**Phase 1 (4 tickets):**
- **Wave 1 (1 parallel):** `T-P1-1` (json+escape+boundarycheck row) — must land first.
- **Wave 2 (2 parallel):** `T-P1-2` (methods+urls+status) and `T-P1-4` (policy registry) both run from the `T-P1-1` tip. They share `internal/web/wire` but at disjoint files. `T-P1-4` reads `methods.go` for the coverage-exhaustion test, so it must rebase onto `T-P1-2` before opening its PR (one rebase, no conflict expected since `methods.go` is append-only).
- **Wave 3 (1 parallel):** `T-P1-3` (encode+decode+index) — depends on `T-P1-1` + `T-P1-2`. Largest ticket; gets the full attention of one Code agent while Phase 2 begins below.

**Phase 2 (4 tickets):**
- **Wave 1 (3 parallel):** `T-P2-1` (cookiejar), `T-P2-3` (auth policy), `T-P2-4` (atomicio+paths) — disjoint files, disjoint dependencies, all can run from `main` immediately after Phase 0+1 land.
- **Wave 2 (1 parallel):** `T-P2-2` (storage) — runs from the `T-P2-4` tip so its writes can use `atomicio.WriteFile` from day one. Can also be parallelized with the wave-1 trio by landing with a TODO and a follow-up PR; preferred is wave-2 to avoid the TODO churn.

**Phase 3 (3 tickets):**
- **Wave 1 (1 parallel):** `T-P3-1` (runtime) — must land first; no transport code can run without the metrics counters and `Lifecycle`.
- **Wave 2 (2 parallel):** `T-P3-2` (kernel) and the begin of `T-P3-3` prep — but `T-P3-3` cannot open its PR until `T-P3-2` and `T-P1-4` both merge. In practice `T-P3-2` is one Code agent and `T-P3-3` waits. If capacity allows, a third agent scaffolds `internal/config` (env resolution only) as a sub-ticket of `T-P3-3` and the merge pulls it in.
- **Wave 3 (1 parallel):** `T-P3-3` (Executor + Runtime + chain) — final and largest Phase 3 ticket; runs from the `T-P3-2` + `T-P1-4` tip.

**Critical-path summary:** `T-P0-1 → T-P1-1 → T-P1-2 → T-P1-3` is the longest dependency chain (the wire layer is the schedule's spine). Phase 2 and Phase 3 fan out from this spine.

## Notes

- **Phase 0 splits cleanly** into 4 tickets; no granularity fabrication needed. The natural units are: scaffold, buildinfo+logging, redact, boundarycheck.
- **Phase 1 does not split into 5 tickets** even though it has more deliverables than Phase 0. `internal/web/wire/json.go` + `escape.go` are too tightly coupled to be separate PRs (every later package imports both), and `encode.go` + `decode.go` + `index.go` share the golden-bytes test infrastructure. The 4-ticket split above is the natural one; collapsing `methods+urls+status` into one ticket is the right call because they are all read-only reference data + small helpers, not implementation.
- **Phase 2 splits cleanly** into 4 tickets. The `atomicio + paths` pairing is intentional: they ship together because `paths.Home()` is what `atomicio`'s `WithHome` option will resolve against in Phase 14+. Splitting them would force a no-op import in the interim.
- **Phase 3 splits into 3 tickets, not 4.** The temptation is to split `transport/Executor` from `transport/Runtime` from the middleware chain; they all live in the same package, share types, and the AST regression test (`T-P3-3`) requires the full chain present. Splitting them creates a "stub the AST test, fill it in later" path that is harder to review than the merged PR.
- **No ticket in any phase introduces a new RPC, payload shape, or wire method.** Phase 1's `methods.go` ports the existing table from doc 03; nothing is added. Phase 2's policy types are new *types* but they consume the methods table — no new RPC. Phase 3's middleware chain operates on the existing `Method` lookup, never on a new id.
- **No ticket crosses a package boundary upward.** `internal/logging` is leaf (stdlib + `internal/redact`). `internal/redact` is leaf (stdlib). `internal/web/wire` is leaf (stdlib only — enforced by boundarycheck). `internal/web/transport` imports down into `internal/web/wire`, `internal/runtime`, `internal/redact` — never up.
- **Atomic-write discipline is enforced in three tickets, not one.** `T-P0-2` declares the redaction-hook interface, `T-P2-4` ships the primitive, `T-P2-2` is the first consumer. This sequencing lets every later write (Phase 14 features) reach for a stable, reviewed primitive.
- **The bot identity from §5.2 of AGENTIC_LOOP applies to every commit.** Every commit message and PR title suggested above is followed by `Co-authored-by: Raihan Khan <raihankhanraka@gmail.com>` trailer in the actual commit body (omitted here for brevity).
- **Phase 3's "Public library API" header is a slight misnomer** — the *true* public library API (`notebooklm.SDK`) ships in Phase 5. Phase 3 ships the transport kernel that Phase 5 wraps. The Sprint 1 deliverable is "the library can talk to NotebookLM at the protocol level"; the ergonomics land in Sprint 2.
- **One ticket that looks like it might need a new RPC, but doesn't:** `T-P1-2` adds the `NOTEBOOKLM_RPC_OVERRIDES` mechanism. That looks like a new "method registration" surface, but it's a runtime patch on the existing `Method` table — no new id, no new wire shape. Confirmed not to violate F1.
