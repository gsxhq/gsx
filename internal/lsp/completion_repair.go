package lsp

import (
	"go/token"

	gsxast "github.com/gsxhq/gsx/ast"
	gsxparser "github.com/gsxhq/gsx/parser"
)

// repairResult is the buffer completion analyzes, plus what was done to it.
type repairResult struct {
	src    []byte         // patched bytes (== live buffer when patch == "")
	patch  string         // inserted at off (see completionPatches): "", "_", "/>", "\"/>", "\"\"/>", "}/>", "_/>"
	parsed *gsxast.File   // parse of src (nil only when unrepairable)
	fset   *token.FileSet // resolves parsed's positions
}

// completionPatches is the closed, ordered repair set. Each is tried by
// inserting at the cursor and reparsing; the first parse wins. Bytes before
// the cursor are never modified, so every client-visible offset survives.
//
// "_/>" is last: a phantom tag-name closer for a bare `<▮` or a qualified
// `<pkg.▮` with nothing typed after the dot — neither has any typed text a
// simple `/>` self-close can attach to (`</>`  and `<pkg./>` both still fail
// to parse), so a placeholder identifier is required to stand in for the
// not-yet-typed tag/member name. `_` was chosen (over e.g. `x`) because gsx
// tag names carry no Go identifier semantics — an element's Tag is an opaque
// string, never resolved as a Go expression — so there is no risk of it
// colliding with blank-identifier handling elsewhere; it is also visually
// inert if it were ever (it never is) surfaced. It sorts after "/>" so a
// prefixed half-typed tag/attr (`<Ca`, `<div cl`) keeps healing via the
// simpler `/>` patch alone — `<div cl` + "_/>" would parse too (as attribute
// `cl_`), but that muddies the present-attr-name reasoning downstream, so the
// plain `/>` heal (attribute `cl`) must keep winning.
var completionPatches = []string{"", "_", "/>", "\"/>", "\"\"/>", "}/>", "_/>"}

// repairAtCursor parses text; on failure tries a closed, ordered patch list
// inserted at off, first parse wins. Deterministic; never touches bytes before
// off. Each attempt parses into its own FileSet (the caller's for the first,
// which is why callers pass a fresh one; a new one for every subsequent try) so
// a failed parse never grows the FileSet that a later success returns. The
// winning attempt's FileSet travels back in repairResult so classification
// resolves positions against the exact bytes that parsed.
func repairAtCursor(fset *token.FileSet, path string, text string, off int) repairResult {
	if off < 0 {
		off = 0
	}
	if off > len(text) {
		off = len(text)
	}
	for i, patch := range completionPatches {
		src := make([]byte, 0, len(text)+len(patch))
		src = append(src, text[:off]...)
		src = append(src, patch...)
		src = append(src, text[off:]...)
		attemptFset := fset
		if i > 0 {
			attemptFset = token.NewFileSet()
		}
		f, perrs := gsxparser.ParseFileWithClassifier(attemptFset, path, src, 0, nil)
		if len(perrs) == 0 && f != nil {
			return repairResult{src: src, patch: patch, parsed: f, fset: attemptFset}
		}
	}
	return repairResult{src: []byte(text), patch: ""}
}
