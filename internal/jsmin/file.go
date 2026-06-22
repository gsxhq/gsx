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
