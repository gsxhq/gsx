package codegen

import (
	goast "go/ast"
	goparser "go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"
)

// coalesceStaticWrites merges runs of consecutive `_gsxgw.S("literal")` calls in
// the generated source into a single call, reducing both generated-code size and
// the number of runtime writes. A static element/attr emits one S call per
// fragment (e.g. `<div`, `>`, `x`, `</div>` → four calls), so adjacent runs are
// common.
//
// It works on the AST, not the text: it parses the generated source, finds
// maximal runs of consecutive string-literal S calls within each block, and
// splices a single merged call over each run's exact byte span. Editing precise
// AST-derived spans (rather than reprinting via go/printer) means every `//line`
// directive and comment outside the runs is preserved verbatim — and a run is
// broken whenever a comment lies between two members, so a directive is never
// swallowed or displaced. Only `_gsxgw.S(<string literal>)` calls merge; a
// non-literal arg (`string(raw)`, `strconv.FormatInt(…)`) or any other writer
// call breaks the run. The merged literal is rebuilt with strconv.Unquote/Quote
// so escapes are exact. On any parse error the input is returned unchanged (the
// caller's format.Source surfaces the real error).
func coalesceStaticWrites(src []byte) []byte {
	fset := token.NewFileSet()
	f, err := goparser.ParseFile(fset, "", src, goparser.ParseComments)
	if err != nil {
		return src
	}

	// Comment start offsets, sorted, so a run can break at any comment in its span.
	var commentOffsets []int
	for _, cg := range f.Comments {
		commentOffsets = append(commentOffsets, fset.Position(cg.Pos()).Offset)
	}
	commentBetween := func(lo, hi int) bool {
		i := sort.SearchInts(commentOffsets, lo)
		return i < len(commentOffsets) && commentOffsets[i] < hi
	}

	type edit struct {
		start, end int
		repl       string
	}
	var edits []edit

	// collectRuns scans one statement list (a block body, or a switch/select clause
	// body — all just []Stmt) for maximal runs of literal S calls and records a
	// merge edit per run.
	collectRuns := func(list []goast.Stmt) {
		for i := 0; i < len(list); {
			v0, ok := staticSLiteral(list[i])
			if !ok {
				i++
				continue
			}
			parts := []string{v0}
			j := i + 1
			for j < len(list) {
				vj, ok := staticSLiteral(list[j])
				if !ok {
					break
				}
				gapLo := fset.Position(list[j-1].End()).Offset
				gapHi := fset.Position(list[j].Pos()).Offset
				if commentBetween(gapLo, gapHi) {
					break // a //line or comment sits between — keep them separate
				}
				parts = append(parts, vj)
				j++
			}
			if len(parts) >= 2 {
				start := fset.Position(list[i].Pos()).Offset
				end := fset.Position(list[j-1].End()).Offset
				repl := "_gsxgw.S(" + strconv.Quote(strings.Join(parts, "")) + ")"
				edits = append(edits, edit{start, end, repl})
			}
			i = j
		}
	}

	goast.Inspect(f, func(n goast.Node) bool {
		switch t := n.(type) {
		case *goast.BlockStmt:
			collectRuns(t.List)
		case *goast.CaseClause: // switch / type-switch clause body ([]Stmt, not a block)
			collectRuns(t.Body)
		case *goast.CommClause: // select clause body
			collectRuns(t.Body)
		}
		return true
	})

	if len(edits) == 0 {
		return src
	}
	// Apply right-to-left so earlier offsets stay valid.
	sort.Slice(edits, func(a, b int) bool { return edits[a].start > edits[b].start })
	out := src
	for _, e := range edits {
		merged := make([]byte, 0, len(out)-(e.end-e.start)+len(e.repl))
		merged = append(merged, out[:e.start]...)
		merged = append(merged, e.repl...)
		merged = append(merged, out[e.end:]...)
		out = merged
	}
	return out
}

// staticSLiteral returns the unquoted string when stmt is exactly a
// `_gsxgw.S("…")` call whose sole argument is a string literal; ok is false for
// any other statement (a different writer call, or an S call with a non-literal
// argument such as `string(raw)` / `strconv.FormatInt(…)`).
func staticSLiteral(stmt goast.Stmt) (string, bool) {
	es, ok := stmt.(*goast.ExprStmt)
	if !ok {
		return "", false
	}
	call, ok := es.X.(*goast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return "", false
	}
	sel, ok := call.Fun.(*goast.SelectorExpr)
	if !ok || sel.Sel.Name != "S" {
		return "", false
	}
	recv, ok := sel.X.(*goast.Ident)
	if !ok || recv.Name != "_gsxgw" {
		return "", false
	}
	lit, ok := call.Args[0].(*goast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	v, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return v, true
}
