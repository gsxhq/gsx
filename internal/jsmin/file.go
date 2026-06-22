package jsmin

import (
	"fmt"
	"strings"

	"github.com/gsxhq/gsx/ast"
)

// MinifyFile minifies the static JS of every <script> element in f, in place.
// ext, if non-nil, minifies the script's JS (the pluggable extension point); a
// nil ext uses the built-in safe minifier. Only HOLELESS <script> blocks (all
// *ast.Text children) are minified: a script carrying any @{ } hole (an
// *ast.Interp child) is left UNCHANGED, because segment-minifying the Text runs
// around a hole could collapse whitespace across the hole boundary and change
// ASI semantics. Correctness over minification for holey scripts in this slice.
func MinifyFile(f *ast.File, ext func(string) (string, error)) error {
	for _, d := range f.Decls {
		if comp, ok := d.(*ast.Component); ok {
			if err := minifyMarkup(comp.Body, ext); err != nil {
				return err
			}
		}
	}
	return nil
}

func minifyMarkup(nodes []ast.Markup, ext func(string) (string, error)) error {
	for _, n := range nodes {
		switch v := n.(type) {
		case *ast.Element:
			if strings.EqualFold(v.Tag, "script") {
				// A data-block <script> (e.g. type="application/json") is not
				// JavaScript; running the JS minifier on its body is wrong. Leave
				// it unchanged. (Holey data islands are also covered by the
				// holey-skip in minifyScriptChildren, but a HOLELESS static JSON
				// block would otherwise be JS-minified — this skip prevents that.)
				if isDataIslandScript(v) {
					continue
				}
				mc, err := minifyScriptChildren(v.Children, ext)
				if err != nil {
					return err
				}
				v.Children = mc
				continue
			}
			if err := minifyMarkup(v.Children, ext); err != nil {
				return err
			}
		case *ast.Fragment:
			if err := minifyMarkup(v.Children, ext); err != nil {
				return err
			}
		case *ast.IfMarkup:
			if err := minifyMarkup(v.Then, ext); err != nil {
				return err
			}
			if err := minifyMarkup(v.Else, ext); err != nil {
				return err
			}
		case *ast.ForMarkup:
			if err := minifyMarkup(v.Body, ext); err != nil {
				return err
			}
		case *ast.SwitchMarkup:
			for i := range v.Cases {
				if err := minifyMarkup(v.Cases[i].Body, ext); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// jsExecutableTypes mirrors internal/jsx's set: <script type> values that run as
// JavaScript. Any other (non-empty) type marks a data block.
var jsExecutableTypes = map[string]bool{
	"text/javascript": true, "module": true, "application/javascript": true,
	"text/ecmascript": true, "application/ecmascript": true,
}

// isDataIslandScript reports whether el is a <script> whose static `type` marks
// it a data block (not executable JS). It is a ~6-line duplicate of the jsx
// predicate (internal/jsx/jsx.go); the copy is intentional so jsmin need not
// depend on jsx. Keep the two in sync.
func isDataIslandScript(el *ast.Element) bool {
	for _, a := range el.Attrs {
		if sa, ok := a.(*ast.StaticAttr); ok && strings.EqualFold(sa.Name, "type") {
			t := strings.ToLower(strings.TrimSpace(sa.Value))
			return t != "" && !jsExecutableTypes[t]
		}
	}
	return false
}

func minifyScriptChildren(children []ast.Markup, ext func(string) (string, error)) ([]ast.Markup, error) {
	// A holey <script> (any @{ } interpolation) is left unchanged: minifying the
	// Text runs around the holes is unsafe (ASI / hole-boundary whitespace).
	for _, c := range children {
		if _, ok := c.(*ast.Interp); ok {
			return children, nil
		}
	}
	var sb strings.Builder
	for _, c := range children {
		if t, ok := c.(*ast.Text); ok {
			sb.WriteString(t.Value)
		}
	}
	src := sb.String()
	var min string
	if ext != nil {
		m, err := ext(src)
		if err != nil {
			return nil, fmt.Errorf("jsmin: external JS minifier: %w", err)
		}
		min = m
	} else {
		min = minifyJS(src)
	}
	if min == "" {
		return nil, nil
	}
	return []ast.Markup{&ast.Text{Value: min}}, nil
}
