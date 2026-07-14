package cssmin

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gsxhq/gsx/ast"
)

// MinifyFile minifies the static CSS of every <style> element in f, in place.
// ext, if non-nil, minifies the full CSS of a HOLELESS <style> block (the
// pluggable extension point); a block containing @{ } interpolation always uses
// the built-in hole-aware minifier, because an external string->string minifier
// cannot reason across holes. A nil ext uses the built-in for every block.
func MinifyFile(f *ast.File, ext func(string) (string, error)) error {
	for _, d := range f.Decls {
		comp, ok := d.(*ast.Component)
		if !ok {
			continue
		}
		if err := minifyMarkup(comp.Body, ext); err != nil {
			return err
		}
	}
	return nil
}

// minifyMarkup recurses, replacing each <style> element's children in place.
func minifyMarkup(nodes []ast.Markup, ext func(string) (string, error)) error {
	for _, n := range nodes {
		switch v := n.(type) {
		case *ast.Element:
			if strings.EqualFold(v.Tag, "style") {
				mc, err := minifyStyleChildren(v.Children, ext)
				if err != nil {
					return err
				}
				v.Children = mc
				if err := minifyAttrs(v.Attrs, ext); err != nil {
					return err
				}
				continue
			}
			if err := minifyMarkup(v.Children, ext); err != nil {
				return err
			}
			if err := minifyAttrs(v.Attrs, ext); err != nil {
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

// minifyStyleChildren returns the minified replacement for a <style> body. A
// holeless body is minified as one CSS string (via ext or the built-in). A body
// with interps is minified via an opaque sentinel pass that preserves the
// *ast.Interp pointers and their adjacent whitespace.
func minifyStyleChildren(children []ast.Markup, ext func(string) (string, error)) ([]ast.Markup, error) {
	hasInterp := false
	for _, c := range children {
		if _, ok := c.(*ast.Interp); ok {
			hasInterp = true
			break
		}
	}

	if !hasInterp {
		var sb strings.Builder
		for _, c := range children {
			if t, ok := c.(*ast.Text); ok {
				sb.WriteString(t.Value)
			}
		}
		css := sb.String()
		var min string
		if ext != nil {
			m, err := ext(css)
			if err != nil {
				return nil, fmt.Errorf("cssmin: external CSS minifier: %w", err)
			}
			min = m
		} else {
			min = minifyCSS(css)
		}
		if min == "" {
			return nil, nil
		}
		return []ast.Markup{&ast.Text{Value: min}}, nil
	}

	// Holey: replace each interp with a NUL-delimited index sentinel, minify, split
	// back. A NUL byte in the source CSS is pathological — bail to verbatim (no
	// minification) rather than risk a bad split.
	var sb strings.Builder
	var interps []*ast.Interp
	for _, c := range children {
		switch t := c.(type) {
		case *ast.Text:
			if strings.IndexByte(t.Value, 0) >= 0 {
				return children, nil
			}
			sb.WriteString(t.Value)
		case *ast.Interp:
			sb.WriteByte(0)
			sb.WriteString(strconv.Itoa(len(interps)))
			sb.WriteByte(0)
			interps = append(interps, t)
		}
	}
	return splitSentinels(minifyCSS(sb.String()), interps), nil
}

// minifyAttrs walks attribute markup slots (a MarkupAttr value is a fresh markup
// context that may itself contain <style>), so <style> in name={ … } is minified too.
func minifyAttrs(attrs []ast.Attr, ext func(string) (string, error)) error {
	for _, a := range attrs {
		switch v := a.(type) {
		case *ast.EmbeddedAttr:
			// A css`…` attribute value (style=css`…`) is a declaration list;
			// minify it with the same holeless/holey machinery as a <style> body.
			if v.Lang == ast.EmbeddedCSS {
				mc, err := minifyStyleChildren(v.Segments, ext)
				if err != nil {
					return err
				}
				if mc != nil {
					v.Segments = mc
				}
			}
		case *ast.MarkupAttr:
			if err := minifyMarkup(v.Value, ext); err != nil {
				return err
			}
		case *ast.CondAttr:
			if err := minifyAttrs(v.Then, ext); err != nil {
				return err
			}
			if err := minifyAttrs(v.Else, ext); err != nil {
				return err
			}
		}
	}
	return nil
}

// splitSentinels reassembles a minified sentinel string into Text + Interp nodes.
// Each \x00<digits>\x00 run is replaced by interps[<digits>]; the spans between
// become Text nodes.
func splitSentinels(s string, interps []*ast.Interp) []ast.Markup {
	var out []ast.Markup
	var text strings.Builder
	i, n := 0, len(s)
	for i < n {
		if s[i] == 0 {
			j := i + 1
			for j < n && s[j] >= '0' && s[j] <= '9' {
				j++
			}
			if j < n && s[j] == 0 && j > i+1 {
				idx, _ := strconv.Atoi(s[i+1 : j])
				if text.Len() > 0 {
					out = append(out, &ast.Text{Value: text.String()})
					text.Reset()
				}
				if idx >= 0 && idx < len(interps) {
					out = append(out, interps[idx])
				}
				i = j + 1
				continue
			}
		}
		text.WriteByte(s[i])
		i++
	}
	if text.Len() > 0 {
		out = append(out, &ast.Text{Value: text.String()})
	}
	return out
}
