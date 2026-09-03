// Package cligroups holds the canonical cobra.Group ID strings
// every CLI command references. The package is its own boundary
// (mode=internal, stdlib + sibling internal/* only) so:
//
//   - internal/cli declares the *Group instances (with Title
//     fields) on the root command for help rendering.
//   - internal/cli/cmd references the IDs from leaf constructors
//     without creating an import cycle (cli → cmd → cli).
//
// Splitting the IDs into their own package keeps the dependency
// graph acyclic while still letting both sides share one source
// of truth.
package cligroups

// Group IDs. The string values are the wire contract: they appear
// verbatim in `notebooklm --help` and in the sectioned_group
// tests. Renaming any value is a breaking change.
const (
	Session  = "session"
	Notebook = "notebook"
	Source   = "source"
	Chat     = "chat"
	Artifact = "artifact"
	Research = "research"
	Share    = "share"
	Note     = "note"
	Profile  = "profile"
	Auth     = "auth"
	Language = "language"
	Misc     = "misc"
)
