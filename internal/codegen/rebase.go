package codegen

import (
	"strconv"
	"strings"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/cssfmt"
	"github.com/gsxhq/gsx/internal/jsfmt"
)

// rebaseEmbedded strips the block's common (markup-depth) leading indentation
// from every embedded JS/CSS body in f, preserving the author's relative
// structure — the same re-basing gsx fmt applies to source, applied here to the
// generated asset so it does not ship indented to its source markup depth.
//
// It runs for a language only when that language is NOT minified (doJS/doCSS):
// MinifyFull's minifier removes all whitespace anyway. Opaque tokens (string /
// template / regex / comment) are left verbatim; holey bodies round-trip their
// @{ } holes through a sentinel so the *ast.Interp pointers are preserved.
func rebaseEmbedded(f *ast.File, doJS, doCSS bool) {
	if !doJS && !doCSS {
		return
	}
	for _, d := range f.Decls {
		if c, ok := d.(*ast.Component); ok {
			rebaseMarkup(c.Body, doJS, doCSS)
		}
	}
}

func rebaseMarkup(nodes []ast.Markup, doJS, doCSS bool) {
	for _, n := range nodes {
		switch v := n.(type) {
		case *ast.Element:
			switch {
			case doJS && strings.EqualFold(v.Tag, "script") && !isDataIsland(v):
				v.Children = rebaseBody(v.Children, ast.EmbeddedJS)
			case doCSS && strings.EqualFold(v.Tag, "style"):
				v.Children = rebaseBody(v.Children, ast.EmbeddedCSS)
			default:
				rebaseMarkup(v.Children, doJS, doCSS)
			}
			rebaseAttrs(v.Attrs, doJS, doCSS)
		case *ast.EmbeddedInterp:
			v.Segments = rebaseBody(v.Segments, v.Lang)
		case *ast.Fragment:
			rebaseMarkup(v.Children, doJS, doCSS)
		case *ast.IfMarkup:
			rebaseMarkup(v.Then, doJS, doCSS)
			rebaseMarkup(v.Else, doJS, doCSS)
		case *ast.ForMarkup:
			rebaseMarkup(v.Body, doJS, doCSS)
		case *ast.SwitchMarkup:
			for i := range v.Cases {
				rebaseMarkup(v.Cases[i].Body, doJS, doCSS)
			}
		}
	}
}

func rebaseAttrs(attrs []ast.Attr, doJS, doCSS bool) {
	for _, a := range attrs {
		switch v := a.(type) {
		case *ast.EmbeddedAttr:
			if (v.Lang == ast.EmbeddedJS && doJS) || (v.Lang == ast.EmbeddedCSS && doCSS) {
				v.Segments = rebaseBody(v.Segments, v.Lang)
			}
		case *ast.MarkupAttr:
			rebaseMarkup(v.Value, doJS, doCSS)
		case *ast.CondAttr:
			rebaseAttrs(v.Then, doJS, doCSS)
			rebaseAttrs(v.Else, doJS, doCSS)
		}
	}
}

// rebaseBody re-bases one embedded body's Segments in place. Lang other than
// JS/CSS (an f`…` text literal) is left unchanged.
func rebaseBody(segs []ast.Markup, lang ast.EmbeddedLang) []ast.Markup {
	if lang != ast.EmbeddedJS && lang != ast.EmbeddedCSS {
		return segs
	}
	holey := false
	for _, s := range segs {
		if _, ok := s.(*ast.Interp); ok {
			holey = true
			break
		}
	}
	if !holey {
		var sb strings.Builder
		for _, s := range segs {
			if t, ok := s.(*ast.Text); ok {
				sb.WriteString(t.Value)
			}
		}
		d, ok := dedent(sb.String(), lang)
		if !ok {
			return segs
		}
		return []ast.Markup{&ast.Text{Value: d}}
	}

	// Holey: replace each @{ } with a collision-free free-identifier sentinel,
	// dedent, then split the result back to Text + the original Interp pointers.
	var scan strings.Builder
	for _, s := range segs {
		if t, ok := s.(*ast.Text); ok {
			scan.WriteString(t.Value)
		}
	}
	prefix := "gsxRebase"
	for strings.Contains(scan.String(), prefix) {
		prefix += "q"
	}
	var sb strings.Builder
	var interps []*ast.Interp
	for _, s := range segs {
		switch t := s.(type) {
		case *ast.Text:
			sb.WriteString(t.Value)
		case *ast.Interp:
			sb.WriteString(prefix)
			sb.WriteString(strconv.Itoa(len(interps)))
			sb.WriteByte('z')
			interps = append(interps, t)
		}
	}
	d, ok := dedent(sb.String(), lang)
	if !ok {
		return segs
	}
	if out, ok := splitRebaseSentinels(d, prefix, interps); ok {
		return out
	}
	return segs
}

// dedent re-bases a self-contained JS/CSS string via the formatter (which strips
// the common leading indentation and preserves relative structure). ok=false on
// a lex error → caller leaves the body unchanged.
func dedent(text string, lang ast.EmbeddedLang) (string, bool) {
	var out []byte
	var err error
	if lang == ast.EmbeddedJS {
		out, err = jsfmt.Format([]byte(text), 0)
	} else {
		out, err = cssfmt.Format([]byte(text), 0)
	}
	if err != nil {
		return "", false
	}
	return string(out), true
}

// splitRebaseSentinels reassembles a dedented sentinel string into Text + the
// original Interp nodes. Each `<prefix><digits>z` run is replaced by
// interps[<digits>]; every index must appear exactly once (else ok=false and the
// caller leaves the body unchanged).
func splitRebaseSentinels(s, prefix string, interps []*ast.Interp) ([]ast.Markup, bool) {
	var out []ast.Markup
	var text strings.Builder
	seen := make([]bool, len(interps))
	flush := func() {
		if text.Len() > 0 {
			out = append(out, &ast.Text{Value: text.String()})
			text.Reset()
		}
	}
	for i := 0; i < len(s); {
		if strings.HasPrefix(s[i:], prefix) {
			j := i + len(prefix)
			k := j
			for k < len(s) && s[k] >= '0' && s[k] <= '9' {
				k++
			}
			if k > j && k < len(s) && s[k] == 'z' {
				idx, _ := strconv.Atoi(s[j:k])
				if idx < 0 || idx >= len(interps) || seen[idx] {
					return nil, false
				}
				seen[idx] = true
				flush()
				out = append(out, interps[idx])
				i = k + 1
				continue
			}
		}
		text.WriteByte(s[i])
		i++
	}
	flush()
	for _, ok := range seen {
		if !ok {
			return nil, false
		}
	}
	return out, true
}

// isDataIsland reports whether el is a <script> whose static `type` marks it a
// data block (not executable JS), e.g. <script type="application/json"> — such a
// body must not be treated as JS.
func isDataIsland(el *ast.Element) bool {
	for _, a := range el.Attrs {
		if sa, ok := a.(*ast.StaticAttr); ok && strings.EqualFold(sa.Name, "type") {
			t := strings.ToLower(strings.TrimSpace(sa.Value))
			return t != "" && t != "text/javascript" && t != "module" &&
				t != "application/javascript" && t != "text/ecmascript" &&
				t != "application/ecmascript"
		}
	}
	return false
}
