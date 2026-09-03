// Package cmd — shared JSON view types for notebook payloads.
//
// The notebook list / create / metadata / summary / use commands
// all serialize the typed SDK Notebook into a JSON row. The view
// here is the single source of truth so every command emits the
// same shape under --json.
package cmd

import "github.com/raihankhan/notebooklm-go/notebooklm"

// notebookView projects a typed notebooklm.Notebook into the
// JSON shape the --json envelope carries. Centralizing the
// projection here keeps the wire shape consistent across every
// command.
type notebookViewJSON struct {
	ID       string               `json:"id"`
	Title    string               `json:"title"`
	Summary  string               `json:"summary,omitempty"`
	Metadata *notebooklm.Metadata `json:"metadata,omitempty"`
}

// notebookView returns the JSON row for a single notebook. The
// caller passes a *Notebook so a nil (e.g. "not found" path) does
// not crash the projection.
func notebookView(n *notebooklm.Notebook) notebookViewJSON {
	if n == nil {
		return notebookViewJSON{}
	}
	return notebookViewJSON{
		ID:       n.ID,
		Title:    n.Title,
		Summary:  n.Summary,
		Metadata: n.Metadata,
	}
}
