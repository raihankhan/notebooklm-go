// Package policy holds the IdempotencyRegistry: the gate the HTTP
// transport consults before replaying a batchexecute RPC after a lost
// response. Every active wire.Method must have at least one registered
// (method, variant) entry; a missing entry fails startup by design, because
// replaying an unclassified mutation after a network blip silently
// duplicates the side effect (a duplicate notebook, a duplicate source, an
// extra LLM inference, a re-sent invite email).
//
// The registry is keyed by (wire.Method, variant) so that methods with
// several wire shapes (e.g. MethodAddSource has URL / pasted text / Drive
// import variants) carry per-variant classes; the variant is a free-form
// short label the caller picks (e.g. "url", "paste", "drive"). A method
// without variant distinctions can be registered under the empty string.
//
// The five idempotency classes live in doc 03, "Idempotency taxonomy"
// (https://github.com/raihankhan/notebooklm-go/blob/master/docs/03-protocol-batchexecute.md#idempotency-taxonomy):
//
//   - ClassSafe — never mutates server state; safe to retry forever.
//   - ClassReadOnly — same as ClassSafe by current behavior; split exists
//     so a future policy addition (caching hint, request coalescing) does
//     not require an axis change.
//   - ClassProbeThenCreate — caller owns its own probe loop; the transport
//     must force-disable inner retries so a probe-then-create never
//     silently doubles up. Entries in this class require a non-empty
//     Rationale describing how the caller recovers from the lost response.
//   - ClassIdempotentMutation — replay-safe mutation (set-state / delete
//     by id / rename); retries stay on.
//   - ClassUnsafeMutation — no dedupe key, no probe; the first failure
//     must surface. Inner retries are off.
//
// Per docs/AGENTS.md rule 7 ("Mutations declare their retry safety"), an
// unregistered RPC is a programming error, not a warning. The
// NewRegistry constructor enforces that invariant at startup.
//
// Boundary: per docs/AGENTS.md rule 5, this package may import stdlib and
// the wire package only. It is registered in boundaries.yaml as
// mode=stdlib so boundarycheck rejects any future import of internal/app,
// internal/cli, or any third-party module.
package policy
