package printer

import (
	"strings"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/pretty"
)

// inlineTags is prettier's HTML inline-elements list minus gsx's preserve
// tags (textarea; script/style were never in it). Only these can be atoms in
// text flow. Components (including lowercase tags that codegen later resolves
// to components — the formatter runs without analysis, so IsComponent is
// never stamped here) may be misclassified inline; that is layout-only and
// always render-safe, since an atom's flat rendering is byte-faithful.
var inlineTags = map[string]bool{
	"a": true, "abbr": true, "acronym": true, "b": true, "bdo": true,
	"big": true, "br": true, "button": true, "cite": true, "code": true,
	"dfn": true, "em": true, "font": true, "i": true, "img": true,
	"input": true, "kbd": true, "label": true, "map": true, "object": true,
	"output": true, "q": true, "samp": true, "select": true, "small": true,
	"span": true, "strong": true, "sub": true, "sup": true, "time": true,
	"tt": true, "u": true, "var": true, "video": true, "audio": true,
}

func isInlineTag(tag string) bool { return inlineTags[strings.ToLower(tag)] }

// atomDoc renders e as an inline atom: one flat line, no groups, so width
// pressure can never break it open. ok=false when e is not an atom — wrong
// tag, author multiline layout (which outranks atom status), a non-inline
// child, or when the assembled doc has no one-line form. The final gate is
// pretty.Flat: forced breaks (line-comment attrs, CondAttr, multi-line
// embedded values) and literal-newline text all disqualify; groups from
// dynamic attr values (e.g. `href={ … }`) are unwrapped to their flat form so
// width can never re-break them. Inside preserve subtrees text is verbatim
// (may hold significant newlines), so nothing is an atom there.
func (p *printer) atomDoc(e *ast.Element) (pretty.Doc, bool) {
	if p.preserve || !isInlineTag(e.Tag) || e.TypeArgs != "" ||
		e.AttrsMultiline || e.ChildrenMultiline {
		return pretty.Doc{}, false
	}
	parts := []pretty.Doc{pretty.Text("<"), pretty.Text(e.Tag)}
	for _, a := range e.Attrs {
		parts = append(parts, pretty.Text(" "), p.attrDoc(a))
	}
	if e.Void && len(e.Children) == 0 {
		parts = append(parts, pretty.Text("/>"))
	} else {
		parts = append(parts, pretty.Text(">"))
		for _, n := range e.Children {
			switch v := n.(type) {
			case *ast.Text:
				parts = append(parts, pretty.Text(v.Value))
			case *ast.Interp:
				parts = append(parts, p.interp(v))
			case *ast.EmbeddedInterp:
				parts = append(parts, p.embeddedInterp(v))
			case *ast.Element:
				child, ok := p.atomDoc(v)
				if !ok {
					return pretty.Doc{}, false
				}
				parts = append(parts, child)
			default:
				return pretty.Doc{}, false
			}
		}
		parts = append(parts, pretty.Text("</"), pretty.Text(e.Tag), pretty.Text(">"))
	}
	return pretty.Flat(pretty.Concat(parts...))
}
