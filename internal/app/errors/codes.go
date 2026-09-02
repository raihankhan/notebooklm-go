// Package errors maps typed library errors to the stable machine codes and
// process exit codes the CLI, MCP, and REST adapters emit. It is the Go port
// of the classifier at notebooklm-py/src/notebooklm/_app/errors.py::classify
// and the per-branch code/exit projection in
// notebooklm-py/src/notebooklm/cli/error_handler.py::handle_errors.
//
// The mapping in this file is normative — it must match exactly the table at
// docs/07-cli-spec.md §"Exit codes" and §"The --json error envelope". The
// constants here are the JSON `code` field on the wire; automation branches
// on those strings, never on prose. The exit code is the process exit code
// the adapter returns to the shell; SIGINT remains 130 and parse-time usage
// errors remain exit 2 per the spec.
//
// Boundary: per docs/AGENTS.md rule 5, this package is part of internal/app
// (mode=internal in boundaries.yaml). It may import stdlib + the sibling
// internal/* packages it depends on. Every adapter (CLI / MCP / REST)
// imports this package; none of them rolls its own classifier.
package errors

// Code is the stable machine-readable error code emitted in the `--json`
// error envelope and the MCP tool-error payload. The set is closed: any
// library error that does not map to a more specific code lands on
// NOTEBOOKLM_ERROR; any non-library error lands on UNEXPECTED_ERROR.
//
// Codes are stable strings. Renaming a constant is a wire-incompatible
// change; automation parses them. Add new codes only when no existing code
// names the failure category.
//
//nolint:misspell // "CANCELLED" is the canonical spelling per docs/07-cli-spec.md exit-code table.
type Code string

// Canonical error codes. The string values are the JSON `code` field on
// the wire (see docs/07-cli-spec.md §"The --json error envelope"). The
// list is exhaustive — there are exactly eleven.
const (
	// CodeRateLimited signals a transient rate-limit response. Carries a
	// Retry-After hint on the envelope (`retry_after` in seconds).
	CodeRateLimited Code = "RATE_LIMITED"

	// CodeAuthError signals an authentication or authorization failure.
	// Re-authenticating may help (see recoverable: true on the underlying
	// transport.AuthError).
	CodeAuthError Code = "AUTH_ERROR"

	// CodeValidationError signals invalid user input or CLI arguments.
	// The same code is also emitted for parse-time usage errors that
	// reach the post-Cobra layer; parse-time usage errors raised before
	// the command runs use exit code 2 (see docs/07-cli-spec.md §"Exit
	// codes").
	CodeValidationError Code = "VALIDATION_ERROR"

	// CodeConfigError signals missing or invalid configuration
	// (auth profile, storage location, env).
	CodeConfigError Code = "CONFIG_ERROR"

	// CodeNetworkError signals a pre-RPC connection / DNS / read failure.
	// Distinct from the generic RPC-library error so adapters can advise
	// a retry.
	CodeNetworkError Code = "NETWORK_ERROR"

	// CodeNotebookLimit signals the user's account is at or near the
	// per-account notebook quota. The underlying NotebookLimitError
	// carries the current count and (when known) the server-reported
	// limit.
	CodeNotebookLimit Code = "NOTEBOOK_LIMIT"

	// CodeNotFound signals a missing-resource lookup (notebook, source,
	// artifact, note, mind map, label, collection). Includes domain
	// variants — a single umbrella code covers them all so automation
	// only branches on one key.
	CodeNotFound Code = "NOT_FOUND"

	// CodeArtifactTimeout signals an artifact generation that did not
	// reach a terminal state inside the wait budget. The underlying
	// ArtifactTimeoutError carries the notebook_id, task_id, and
	// last_status; the envelope exposes them under
	// docs/07-cli-spec.md's id / notebook_id fields.
	CodeArtifactTimeout Code = "ARTIFACT_TIMEOUT"

	// CodeNotebookLMError is the catch-all for any library error
	// (notebooklm.NotebookLMError hierarchy) that does not map to a
	// more specific code above. Exit code 1 — visible to the user as a
	// failure, not a crash.
	CodeNotebookLMError Code = "NOTEBOOKLM_ERROR"

	// CodeCancelled signals a SIGINT (Ctrl-C) cancellation. The exit
	// code is 130 (128 + signal 2), distinct from every other code so
	// a shell script can detect "user gave up" vs "library failure".
	//
	//nolint:misspell // "CANCELLED" is the canonical spelling per docs/07-cli-spec.md exit-code table; it is also what the canonical --json error envelope document shows.
	CodeCancelled Code = "CANCELLED"

	// CodeUnexpectedError signals a non-library exception (a bug, or a
	// stdlib panic escape). Exit code 2 per docs/07-cli-spec.md —
	// system / unexpected error.
	CodeUnexpectedError Code = "UNEXPECTED_ERROR"
)

// AllCodes is the exhaustive list of every canonical Code constant. Boundary
// tests iterate it to assert that every (code, exit) pair in
// docs/07-cli-spec.md §"Exit codes" is wired through Classify. The list
// order mirrors the table in docs/07-cli-spec.md for human readability.
var AllCodes = []Code{
	CodeRateLimited,
	CodeAuthError,
	CodeValidationError,
	CodeConfigError,
	CodeNetworkError,
	CodeNotebookLimit,
	CodeNotFound,
	CodeArtifactTimeout,
	CodeNotebookLMError,
	CodeCancelled,
	CodeUnexpectedError,
}

// exitFor is the table that maps each Code to the process exit code from
// docs/07-cli-spec.md §"Exit codes". Parse-time usage errors are an
// adapter-level concern (the CLI's FlagErrorFunc pre-decides the exit
// code before the command body runs), so they are not represented here;
// the table covers the runtime error surface.
//
// Keep in sync with docs/07-cli-spec.md — every entry pins one row.
var exitFor = map[Code]int{
	CodeRateLimited:     1,
	CodeAuthError:       1,
	CodeValidationError: 1,
	CodeConfigError:     1,
	CodeNetworkError:    1,
	CodeNotebookLimit:   1,
	CodeNotFound:        1,
	CodeArtifactTimeout: 1,
	CodeNotebookLMError: 1,
	CodeCancelled:       130,
	CodeUnexpectedError: 2,
}

// ExitFor returns the process exit code for the given Code per
// docs/07-cli-spec.md §"Exit codes". Returns 2 (the "unexpected" exit)
// for any unrecognized code string, so a future caller that introduces a
// new code constant without updating the map still degrades to a
// visible-to-the-user failure rather than a silent zero exit.
func ExitFor(c Code) int {
	if code, ok := exitFor[c]; ok {
		return code
	}
	return 2
}
