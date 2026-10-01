package codegen

import (
	"slices"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/internal/htmlattr"
)

// attrBranch identifies one branch of one in-tag if/switch group: group is the
// group attribute, branch its branch index (attrGroupBranches order).
type attrBranch struct {
	group  ast.Attr
	branch int
}

// namedAttr is an authored attribute that renders under its own name, with
// the chain of group branches it sits in.
type namedAttr struct {
	attr ast.Attr
	name string
	path []attrBranch
}

// warnDuplicateAttrs reports a duplicate-attribute warning for each authored
// attribute of the leaf element el that names the same attribute
// (htmlattr.SameName) as an earlier one when both can render in the same
// execution. The output is unchanged — the HTML parser keeps the first — so
// this is a warning, never an error.
//
// It warns only where two attributes would reach the output:
//   - class and style never do: every class/style attribute merges into one
//     (composition), whatever its spelling.
//   - an element that folds its attributes into one bag (elementFolds) never
//     does: the leaf Spread keeps the last of each name.
//   - two attributes in different branches of the same if/else or switch
//     group are mutually exclusive, so never render together.
//
// Everything else renders every attribute it reaches: the plain path writes
// them in order, and the forwarding path guards only against the bag, not
// against its own attributes.
func warnDuplicateAttrs(bag *diag.Bag, el *ast.Element) {
	if elementFolds(el.Attrs) {
		return
	}
	var seen []namedAttr
	var walk func(attrs []ast.Attr, path []attrBranch)
	walk = func(attrs []ast.Attr, path []attrBranch) {
		for _, a := range attrs {
			if branches := attrGroupBranches(a); branches != nil {
				for i, branch := range branches {
					walk(branch, append(slices.Clip(path), attrBranch{group: a, branch: i}))
				}
				continue
			}
			name, ok := rootAttrName(a)
			if !ok || htmlattr.SameName(name, "class") || htmlattr.SameName(name, "style") {
				continue
			}
			for _, prev := range seen {
				if htmlattr.SameName(prev.name, name) && !exclusiveBranches(prev.path, path) {
					bag.Report(a.Pos(), a.End(), diag.Warning, "duplicate-attribute", "codegen",
						"attribute %q is already set on this element; the browser keeps the first one", name)
					break
				}
			}
			seen = append(seen, namedAttr{attr: a, name: name, path: path})
		}
	}
	walk(el.Attrs, nil)
}

// exclusiveBranches reports whether two attributes at paths a and b can never
// render in the same execution: they sit in different branches of one group.
func exclusiveBranches(a, b []attrBranch) bool {
	for _, x := range a {
		for _, y := range b {
			if x.group == y.group && x.branch != y.branch {
				return true
			}
		}
	}
	return false
}
