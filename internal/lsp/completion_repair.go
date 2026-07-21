package lsp

import (
	"go/token"

	gsxast "github.com/gsxhq/gsx/ast"
	gsxparser "github.com/gsxhq/gsx/parser"
)

// repairResult is the buffer completion analyzes, plus what was done to it.
type repairResult struct {
	src    []byte         // patched bytes (== live buffer when patch == "")
	patch  string         // inserted at off: "", "_", "/>", "\"/>", "}\"/>"...
	parsed *gsxast.File   // parse of src (nil only when unrepairable)
	fset   *token.FileSet // resolves parsed's positions
}

// completionPatches is the closed, ordered repair set. Each is tried by
// inserting at the cursor and reparsing; the first parse wins. Bytes before
// the cursor are never modified, so every client-visible offset survives.
var completionPatches = []string{"", "_", "/>", "\"/>", "\"\"/>", "}/>"}

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
