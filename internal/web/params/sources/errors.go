// Package sources — errors.go: the small typed error this
// package's URL / YouTube builders return on bad input.
//
// The shape mirrors `params.paramError` (the pre-T-S3-004a
// version in `internal/web/params/notebooks.go`); the duplicated
// type keeps the sources package self-contained so a future
// ticket can grow it without touching the parent `params`
// package. Adapters route every `*paramError` to the same
// `apperrors.Validation` envelope the public SDK exposes, so
// the duplicate type does not introduce a new error class —
// callers see one ValidationError stream on the SDK surface.
package sources

// paramError is a small typed error so callers can route "bad
// URL argument" / "bad YouTube argument" into the same
// ValidationError class the public SDK exposes, without
// dragging a stdlib errors import into every build site.
//
// Mirrors `params.paramError` (in `notebooks.go`) — kept as a
// separate type so the sources package can grow its own error
// vocabulary (e.g. a future ticket that adds AddDrive-specific
// rules) without bleeding into the parent package. The two
// types carry the same fields and the same Error() format, so
// adapters that switch on `*paramError` via type-assertion need
// to register one assertion per package — T-S3-004c/d will
// wire this in the boundary-sweep PR.
type paramError struct {
	Field  string
	Reason string
}

func (e *paramError) Error() string {
	return "params.sources: " + e.Field + " " + e.Reason
}
