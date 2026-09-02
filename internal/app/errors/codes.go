// Package errors is the transport-neutral error vocabulary used by the
// CLI/MCP/REST adapters and the public SDK.
//
// This file is a STUB that lands in T-P5-7 to let the CLI skeleton compile
// before T-P5-3 (the typed-error vocabulary) is on master. The full version
// of this package is queued in a parallel worktree; if T-P5-3 lands first,
// delete this stub and import the canonical copy.
//
// Until T-P5-3 lands, every CLI command that wants a stable code calls
// Classify(err) and the function below falls back to UNEXPECTED_ERROR for
// every non-nil err — meaning --json envelopes stay well-formed but the
// code is the least informative one. The exit-code mapping in
// docs/07-cli-spec.md still applies: UNEXPECTED_ERROR → 2.
package errors

import "fmt"

// Code is the stable error-code vocabulary. The 11 values below are
// the contract every CLI command and MCP tool returns; automation
// branches on these, never on prose. The set mirrors the CLI/MCP
// spec — docs/07-cli-spec.md "Exit codes" + docs/08-mcp-spec.md
// "Code table" — verbatim.
//
// Add a new code only when no existing one fits and the new failure
// mode is reproducible across adapters. Renaming or splitting an
// existing code is a breaking change for every scripted caller.
type Code string

const (
	// CodeRateLimit indicates a server-side rate-limit response. Carries
	// a RetryAfter hint (seconds).
	CodeRateLimit Code = "RATE_LIMITED"

	// CodeAuth indicates authentication or authorization failure. Caller
	// should re-authenticate and retry.
	CodeAuth Code = "AUTH_ERROR"

	// CodeValidation indicates invalid user input — a bad flag, a
	// malformed id, an unsupported choice value.
	CodeValidation Code = "VALIDATION_ERROR"

	// CodeConfig indicates a misconfigured environment — bad env var,
	// missing credential, allowlist-violating base URL.
	CodeConfig Code = "CONFIG_ERROR"

	// CodeNetwork indicates a pre-RPC transport failure (DNS, TCP,
	// TLS handshake, timeout). Distinct from CodeRPC.
	CodeNetwork Code = "NETWORK_ERROR"

	// CodeNotebookLimit indicates the per-account notebook quota is
	// exhausted. Distinct from CodeRateLimit (which is per-RPC).
	CodeNotebookLimit Code = "NOTEBOOK_LIMIT"

	// CodeNotFound indicates a missing resource (notebook, source,
	// artifact, note, label, collection). The envelope carries the id.
	CodeNotFound Code = "NOT_FOUND"

	// CodeArtifactTimeout indicates an artifact generation did not
	// reach a terminal state in time. Distinct from the generic
	// CodeNetwork timeout.
	CodeArtifactTimeout Code = "ARTIFACT_TIMEOUT"

	// CodeNotebookLM is the catch-all for library errors that fit no
	// narrower category (e.g. an unexpected RPC failure with a typed
	// payload the CLI does not recognize).
	CodeNotebookLM Code = "NOTEBOOKLM_ERROR"

	// CodeCancelled indicates SIGINT (128 + 2). Always exits 130.
	//
	//nolint:misspell // "CANCELLED" is the canonical spelling per docs/07-cli-spec.md.
	CodeCancelled Code = "CANCELLED"

	// CodeUnexpected is the catch-all for non-library exceptions
	// (nil dereferences, panics, programmer error). Always exits 2.
	CodeUnexpected Code = "UNEXPECTED_ERROR"
)

// ExitCode returns the process exit code the docs/07-cli-spec.md
// "Exit codes" table maps this Code to. The function is the single
// source of truth for the code → exit mapping; both the CLI root
// error handler and the MCP server consult it.
//
// The mapping is fixed; any change is a breaking change for scripted
// callers (CI gates, Make recipes, shell aliases) that key on the
// numeric code.
func (c Code) ExitCode() int {
	switch c {
	case CodeRateLimit, CodeAuth, CodeValidation, CodeConfig,
		CodeNetwork, CodeNotebookLimit, CodeNotFound,
		CodeArtifactTimeout, CodeNotebookLM:
		// Per docs/07-cli-spec.md: "1 | user / application error".
		return 1
	case CodeCancelled:
		// 128 + SIGINT(2).
		return 130
	case CodeUnexpected:
		return 2
	default:
		// An unknown code never reaches a user (Classify is the only
		// constructor) but the safe fallback is 2 — system/unexpected.
		return 2
	}
}

// Classify maps an arbitrary error to a (Code, exit-code) pair. The
// STUB implementation below returns (CodeUnexpected, 2) for every
// non-nil error — T-P5-3 replaces it with the type-sensitive
// classification the Python original uses (see
// notebooklm-py/src/notebooklm/_app/errors.py::classify for the
// canonical mapping).
//
// The CLI error handler (internal/cli/errors.go) calls this function
// for every non-nil error returned from a RunE hook; the stub's
// always-unexpected output keeps --json envelopes well-formed but
// loses the typed-code fidelity T-P5-3 will restore.
func Classify(err error) (Code, int) {
	if err == nil {
		// Defensive: callers should not invoke Classify on a nil
		// error but the contract is "no panic" if they do.
		return "", 0
	}
	// TODO(T-P5-3): replace with the type-sensitive ladder.
	_ = fmt.Errorf
	return CodeUnexpected, CodeUnexpected.ExitCode()
}
