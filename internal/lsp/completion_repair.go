package lsp

import (
	"go/token"
	"unicode"
	"unicode/utf8"

	gsxast "github.com/gsxhq/gsx/ast"
	gsxparser "github.com/gsxhq/gsx/parser"
)

// repairResult is the buffer completion analyzes, plus what was done to it.
type repairResult struct {
	src    []byte         // patched bytes (== live buffer when patch == "")
	patch  string         // inserted at off (see completionPatches): "", "_", "}", "_}", "/>", "\"/>", "\"\"/>", "}/>", "_/>"
	parsed *gsxast.File   // parse of src (nil only when unrepairable)
	fset   *token.FileSet // resolves parsed's positions
}

// completionPatches is the closed, ordered repair set. Each is tried by
// inserting at the cursor and reparsing; the first parse wins. Bytes before
// the cursor are never modified, so every client-visible offset survives.
//
// "}" closes an UNCLOSED body interpolation with no autopaired brace — a
// trailing-member cursor (`{ strconv.▮`, `{ user.▮`) or a bare/prefixed ident
// (`{ us▮`) with nothing after it. Both shapes parse as gsx once the brace
// closes (a trailing-dot selector is itself a valid, if incomplete, Go
// expression to the gsx grammar; completion.go's separate trailing-dot
// phantom then heals the resulting skeleton's broken selector, same as the
// already-closed case). It sits right after "_" — before any of the
// tag/attr-value closers — because it is purely a body-interp heal: inside an
// open tag (an ExprAttr value, `class={x▮`) a lone `}` still leaves the tag
// itself unclosed and fails to parse (verified), so it never preempts the
// "}/>" case below regardless of order; ordering it early just keeps the
// common body-interp path short.
//
// "_}" closes an UNCLOSED empty pipe stage (`{ x |> ▮`, no autopaired brace):
// a bare `}` right after `|> ` is rejected as an empty pipeline stage, so a
// placeholder identifier is required, exactly as the tag-closer reasoning
// below. It must come after "}" (a plain `}` never falsely wins here — the
// empty-stage error keeps it from parsing at all) and it also independently
// heals the same trailing-member/bare-ident shapes "}" already heals, so its
// position relative to "}" only matters for the pipe case; putting it right
// after "}" keeps both single-purpose-brace patches adjacent.
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
var completionPatches = []string{"", "_", "}", "_}", "/>", "\"/>", "\"\"/>", "}/>", "_/>"}

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

// navRepair repairs a mid-edit buffer for the go-to-definition / hover fallback.
// Completion's cursor is the edit point, so it repairs AT the cursor; a nav
// cursor, by contrast, may sit inside — or just after — the identifier or tag
// name it is resolving, and a patch inserted at the cursor would split that token
// (`<icon.Be▮ll` + `/>` → `<icon.Be/>ll`, the wrong tag). navRepair instead
// finds the identifier/tag token under the cursor and inserts the repair patch at
// its END, keeping the token whole, and returns a query offset strictly inside
// the token for the resolver cascade (the shared half-open [start,start+len)
// span check never matches a cursor exactly at a token's end). insertOff is also
// the pivot for repaired→live coordinate mapping. ok=false when the buffer is
// unrepairable even after moving the insertion to the token boundary.
func navRepair(path, text string, off int) (r repairResult, insertOff, queryOff int, ok bool) {
	if off < 0 {
		off = 0
	}
	if off > len(text) {
		off = len(text)
	}
	tokStart, _ := completionTokenSpan(text, off, true)
	tokEnd := off
	for tokEnd < len(text) {
		rn, size := utf8.DecodeRuneInString(text[tokEnd:])
		if rn == '_' || rn == '-' || unicode.IsLetter(rn) || unicode.IsDigit(rn) {
			tokEnd += size
			continue
		}
		break
	}
	insertOff, queryOff = off, off
	if tokEnd > tokStart { // the cursor is on, or immediately after, a token
		insertOff = tokEnd
		if queryOff >= tokEnd {
			queryOff = tokEnd - 1 // step inside so the span check can match
		}
	}
	r = repairAtCursor(token.NewFileSet(), path, text, insertOff)
	if r.parsed == nil {
		return repairResult{}, 0, 0, false
	}
	return r, insertOff, queryOff, true
}
