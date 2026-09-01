# 14 — Risk register

Ordered by expected cost, not by likelihood alone. Each risk names the phase that mitigates
it, so the plan and the register stay coupled.

## R1 — Wire-shape drift (obfuscated method ids and positional payloads)

**Likelihood:** certain, on a timescale of weeks to months.
**Impact:** total — every affected feature stops working.

This is the #1 breakage class and it is inherent to the project, not to the port. Google
re-obfuscates method ids and reshapes positional payloads without notice. The Python
original's own history records a Gemini-3.5 rollout that made a previously-optional template
block mandatory, breaking every create and source-add on migrated accounts.

**Mitigations**

| Mitigation | Phase |
|---|---|
| `NOTEBOOKLM_RPC_OVERRIDES` lets an operator patch an id without waiting for a release | 1 |
| Strict positional decoding with typed `ShapeDriftError`, so drift is diagnosed rather than silently mis-decoded | 1 |
| The absent-rpc-id branch raises `ErrUnknownRPCMethod` **naming the ids actually present** — the single most useful diagnostic when an id changes | 1 |
| `rpcDecodeErrors` metric, alerted on rate of change | 3 |
| Nightly canary probing every method id + the build-label staleness gap | 12 |
| Golden-byte tests, so a shape change is caught by our own suite the moment we touch it | 1 |

**Residual risk:** real and permanent. The honest posture is fast detection and a fast patch
path, not prevention. Set expectations in the README the way the original does.

## R2 — The cookie jar

**Likelihood:** high during Phase 2. **Impact:** high — auth is the gate to everything.

Go's `net/http/cookiejar` cannot do what this project needs (doc 02). Writing an RFC 6265 jar
from scratch is genuinely subtle: domain-matching rules, path-matching, `Secure`-awareness,
selection ordering, the public-suffix rule, and lossless attribute round-tripping including
`SameSite`, which no standard jar preserves.

Getting it subtly wrong presents as intermittent auth failures on *some* operations — the
hardest class of bug to diagnose, because the obvious hypothesis ("my cookies expired") is
wrong.

**Mitigations**

- Its own phase, ahead of everything that depends on it (Phase 2).
- A round-trip **fuzz** target at 100k iterations.
- A cross-compatibility test against a `storage_state.json` written by the Python CLI,
  asserting zero attribute loss in both directions.
- The specific known-hard case gets a named test: `OSID` present on both the app host and
  `accounts.google.com` with different values must **not** collapse to one arbitrary winner.

**Fallback:** if the from-scratch jar proves unstable, wrap `net/http/cookiejar` for
*selection* and keep a parallel attribute map for *persistence*. Uglier, less correct, but it
decouples the two responsibilities. Prefer the from-scratch jar; keep this in reserve.

## R3 — Interactive browser login without Playwright

**Likelihood:** medium. **Impact:** medium — three alternative auth paths exist.

`chromedp` requires a Chrome/Chromium/Edge installed on the host. Playwright's bundled
Chromium removed that requirement at the cost of a 170 MB download and a Node driver — the
trade this port deliberately reverses.

Specific failure modes: no Chromium-family browser installed · a corporate policy
(AppLocker/WDAC/Defender) vetoing browser launch · an SSO flow that behaves differently under
an automated profile · CDP unavailable because Chrome is already running without a debug port.

**Mitigations**

- Three paths that need no browser launch at all: `--browser-cookies` (read the existing
  store), `auth import-cookies` (JSON), and master-token headless auth.
- `--cdp-url` attaches to a Chrome the user started themselves, which sidesteps both the
  launch veto and the "already running" problem.
- Classify launch failures into actionable messages naming the specific cause, ported from the
  original.
- Document `--browser-cookies` as the **recommended** path, not the fallback. It is faster,
  needs nothing installed beyond a browser the user already uses, and avoids the whole class.

## R4 — Master-token flow without `gpsoauth`

**Likelihood:** medium. **Impact:** high for headless and remote users — it is their only
unattended auth.

Reimplementing `exchange_token` / `perform_oauth` means depending on undocumented
`android.clients.google.com/auth` behavior. Google could change the response format, tighten
the client-signature check, or require a device-registration step.

**Mitigations**

- The flow is plain form POSTs with no crypto (password encryption is the only crypto in
  `gpsoauth`, and this flow does not use it), so the port surface is small and auditable.
- Fake-server tests covering every documented failure shape (Phase 4).
- Verify the minted jar carries `SID`/`APISID`/`SAPISID` and **fail immediately** with that
  explanation if not — rather than persisting a jar that will break mysteriously during a
  later PSIDTS recovery.
- A live smoke test in CI, gated on a secret, so a Google-side change is detected within a day.

**Residual:** if Google changes this endpoint, headless auth breaks for the Python original
too. Not a port-specific risk.

## R5 — TLS/HTTP-2 fingerprint

**Likelihood:** low today. **Impact:** total if it fires.

Go's `net/http` TLS ClientHello and HTTP/2 SETTINGS frame differ from Chrome's. If Google
adds fingerprint-based bot detection to `batchexecute`, Go requests could be rejected where
Python's `httpx` currently is not — though `httpx` is equally non-Chrome, which is precisely
why the original ships an optional `curl_cffi` transport.

**Mitigations**

- The transport is behind an interface from Phase 3, so swapping it is a configuration change
  rather than a refactor.
- `NOTEBOOKLM_TRANSPORT=utls` with `refraction-networking/utls` for Chrome JA3 impersonation.
  **v1.1**, not v1 — the original's own equivalent is an explicitly labeled PoC, and there is
  no evidence the fingerprint is currently checked.
- The nightly canary would catch a blanket rejection within a day.

**Note:** `utls` covers the TLS ClientHello. Matching Chrome's **HTTP/2** fingerprint
(SETTINGS order, window sizes, priority frames) needs more than `utls` — record that as a
known limitation rather than implying full impersonation.

## R6 — Concurrency correctness in the runtime

**Likelihood:** medium. **Impact:** high — races here corrupt auth state.

Go makes concurrency easy to write and easy to get wrong. The specific hazards, all real in
the original:

- Two layers refreshing on one logical call (fixed by the shared `RefreshBudget`).
- A retry sending a stale envelope built from a pre-refresh snapshot (fixed by the
  unconditional pre-POST rebuild).
- A late goroutine reading a cookie jar from the *next* resource generation after close
  (fixed by epoch fencing).
- N concurrent failures triggering N refreshes and N browsers (fixed by single-flight).
- A follower's cancellation cancelling the shared refresh (fixed by shielded settlement).

**Mitigations**

- All five ported deliberately in Phase 3, each with a named test.
- `-race -count=5` in CI for the concurrency packages; `-count=20` for the five tests above.
- The AST guard asserting no synchronization operation between envelope rebuild and POST.

**The Go-specific trap to watch:** it is tempting to "simplify" epoch fencing away because
`context` cancellation looks like it covers the same ground. It does not — cancellation stops
*new* work but does not prevent an already-running goroutine from reading state that a
subsequent open rebuilt. Do not remove it.

## R7 — Silent parity gaps

**Likelihood:** high without tooling. **Impact:** medium — erodes the drop-in promise.

Nearly 200 commands and flags, 33 MCP tools, 43 REST routes. A missing `--no-truncate`, a
renamed `--run-id` alias, or a differently-spelled `--json` key breaks somebody's script.

**Mitigations**

- `internal/tools/parityaudit` (Phase 12) parses the Python CLI reference and MCP tool table
  and diffs against the Go command tree and tool registry. Fails CI on a gap.
- Committed name lists for MCP tools and REST routes, asserted by tests.
- The `--json` envelope shapes centralized in `internal/app/serialize`, so a key cannot be
  spelled differently in two adapters.

## R8 — Credential leakage

**Likelihood:** low with discipline, and the discipline is cheap. **Impact:** severe.

Full-account Google credentials flow through this code: cookies, CSRF tokens, session ids,
master tokens, OAuth bearers. Leak paths: a log line, an error message, a `%v` on a struct, a
committed cassette, a response preview under `NOTEBOOKLM_DEBUG`, a browser navigation error
embedding a credential-bearing URL, or a `ps aux` visible token flag.

**Mitigations**

- Redacting `String()`/`LogValue()` on every credential-bearing struct (Phase 0).
- All four regex families ported, covering JSON, HTML-escaped JSON, form bodies, and
  diagnostic prose (Phase 0).
- Two-layer cassette scrubbing: a recorder hook **and** a test over committed files
  (Phase 5).
- A log-scanning assertion across the whole suite: no credential pattern in any captured
  output (Phases 2 and 4).
- The MCP token is env-only, with no flag, so it cannot appear in `ps aux`.
- `gosec` in the lint set.
- The master-token mint discards raw responses and credential-bearing wrapped causes before
  raising.

**Go-specific gotcha:** Go's default `%v` on a struct prints every field, including
unexported ones, and `slog` will happily serialize a whole struct. That is *more* exposed than
Python's dataclass `__repr__`, which the original had to explicitly harden anyway. Implement
`LogValue() slog.Value` on every credential type, not just `String()` — `slog` prefers it.

## R9 — Scope creep from over-porting

**Likelihood:** high. **Impact:** medium — delays v1 without adding user value.

The Python original carries ~35 modules of pure deprecation shims, re-export facades, and
compatibility aliases; ~30 one-off audit scripts from its refactor program; a
Python-test-seam framework (`ClientSeams`, `FakeSession`, a monkeypatch policy); and the
loop-affinity contract that exists only because of asyncio. Faithfully porting all of it
would roughly double the work for zero user-visible benefit.

**Mitigations**

- Doc 12's porting map explicitly marks every **dropped** module and why.
- Doc 01's non-goals are the scope boundary; the Android backend is the big one.
- The rule of thumb for any Python module: **if it exists to preserve a Python import path or
  to work around a Python runtime property, it does not get ported.**

**The judgement call to make consciously:** the original's *guardrail tests* look like
scaffolding but are not — they encode hard-won architectural constraints. Port those. Skip the
refactor-audit scripts that produced them.

## R10 — Go's JSON encoder breaking byte-compatibility

**Likelihood:** high on the first attempt; then zero. **Impact:** high while it lasts.

`encoding/json` HTML-escapes `<`, `>`, `&`; `Encoder.Encode` appends a newline; decoding into
`any` turns numbers into `float64`. Each silently produces a payload Google rejects, or an id
that has lost precision — and the failure is a bare gRPC status with no hint about the cause.

**Mitigations**

- One `Marshal`/`Unmarshal` pair in `internal/web/wire/json.go`; `boundarycheck` enforces
  that nothing else imports `encoding/json` (Phases 0 and 1).
- Golden-byte tests including payloads with `<`, `>`, `&`, emoji, and 19-digit ids (Phase 1).
- `escapeAll` has its own table test, because `url.QueryEscape` encodes a space as `+` where
  Python's `quote` encodes `%20`.

This is the risk most likely to be discovered on the first live call and fixed in an hour —
listed because *not knowing about it* costs a day of confusion.

## Risk summary

| ID | Risk | Likelihood | Impact | Owning phase |
|---|---|---|---|---|
| R1 | Wire-shape drift | certain | total | 1, 12 |
| R2 | Cookie jar correctness | high | high | 2 |
| R3 | Browser login without Playwright | medium | medium | 4 |
| R4 | Master token without gpsoauth | medium | high | 4 |
| R5 | TLS fingerprint | low | total | 3, v1.1 |
| R6 | Runtime concurrency | medium | high | 3 |
| R7 | Silent parity gaps | high | medium | 12 |
| R8 | Credential leakage | low | severe | 0, 2, 4, 5 |
| R9 | Scope creep | high | medium | all |
| R10 | JSON byte-compatibility | high once | high | 1 |
