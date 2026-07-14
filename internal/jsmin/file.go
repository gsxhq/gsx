package jsmin

import (
	"fmt"
	"strconv"
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
			// A data-block <script> (e.g. type="application/json") is not
			// JavaScript; running the JS minifier on its body is wrong. Leave the
			// body unchanged (a HOLELESS static JSON block would otherwise be
			// JS-minified). Its attributes are still walked below.
			if strings.EqualFold(v.Tag, "script") && !isDataIslandScript(v) {
				mc, err := minifyScriptChildren(v.Children, ext)
				if err != nil {
					return err
				}
				v.Children = mc
			} else if err := minifyMarkup(v.Children, ext); err != nil {
				return err
			}
			// Minify js`…` attribute values (x-data, @click, hx-on::…) and recurse
			// into markup-slot / conditional attributes.
			if err := minifyJSAttrs(v.Attrs, ext); err != nil {
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

// minifyJSAttrs minifies js`…` attribute VALUES on an element and recurses into
// attributes that carry nested markup. Unlike a <script> body (a program), a
// js`…` attribute value is a FRAGMENT — an object literal (x-data), a handler
// statement, or a call expression — so it goes through cascadeJS (see below).
func minifyJSAttrs(attrs []ast.Attr, ext func(string) (string, error)) error {
	for _, a := range attrs {
		switch v := a.(type) {
		case *ast.EmbeddedAttr:
			if v.Lang == ast.EmbeddedJS {
				minifyJSEmbedded(v, ext)
			}
		case *ast.MarkupAttr:
			if err := minifyMarkup(v.Value, ext); err != nil {
				return err
			}
		case *ast.CondAttr:
			if err := minifyJSAttrs(v.Then, ext); err != nil {
				return err
			}
			if err := minifyJSAttrs(v.Else, ext); err != nil {
				return err
			}
		}
	}
	return nil
}

// minifyJSEmbedded minifies one js`…` attribute value in place. A HOLELESS body
// is cascade-minified. A holey body (Task 2) uses a sentinel round-trip under the
// full minifier and is left unchanged under the safe level.
func minifyJSEmbedded(v *ast.EmbeddedAttr, ext func(string) (string, error)) {
	for _, s := range v.Segments {
		if _, ok := s.(*ast.Interp); ok {
			minifyJSEmbeddedHoley(v, ext)
			return
		}
	}
	var sb strings.Builder
	for _, s := range v.Segments {
		if t, ok := s.(*ast.Text); ok {
			sb.WriteString(t.Value)
		}
	}
	min := cascadeJS(sb.String(), ext)
	if min == "" {
		return
	}
	v.Segments = []ast.Markup{&ast.Text{Value: min}}
}

// cascadeJS minifies a JS FRAGMENT. tdewolff (ext) parses its input as a program,
// so a bare object literal (`{…}`) errors; wrapping it as `(…)` makes it a valid
// expression that fully minifies (the `(…)` is kept — a parenthesized expression
// is an equivalent value). Order: ext(raw) → ext("("+raw+")") → the safe,
// never-erroring built-in (also the whole safe level, where ext is nil).
func cascadeJS(text string, ext func(string) (string, error)) string {
	if ext != nil {
		if o, err := ext(text); err == nil {
			return o
		}
		if o, err := ext("(" + text + ")"); err == nil {
			return o
		}
	}
	return minifyJS(text)
}

// minifyJSEmbeddedHoley minifies a holey js`…` attribute value under the FULL
// minifier via a sentinel round-trip: each @{ } hole becomes a collision-free
// FREE IDENTIFIER (which tdewolff never mangles), the whole is cascade-minified,
// then the sentinels are split back into the original *ast.Interp holes. Safe
// because attribute holes sit in expression value positions (object property
// values, call args, spreads). Under the safe level (ext == nil) a holey value is
// left unchanged, matching the holey <script> policy.
func minifyJSEmbeddedHoley(v *ast.EmbeddedAttr, ext func(string) (string, error)) {
	if ext == nil {
		return
	}
	// A collision-free identifier prefix absent from every Text segment. The
	// sentinel is `<prefix><index>z` — prefix and digits are identifier chars and
	// the `z` terminates the digit run so `<prefix>1z` and `<prefix>12z` never
	// alias.
	var scan strings.Builder
	for _, s := range v.Segments {
		if t, ok := s.(*ast.Text); ok {
			scan.WriteString(t.Value)
		}
	}
	prefix := "gsxHole"
	for strings.Contains(scan.String(), prefix) {
		prefix += "q"
	}

	var sb strings.Builder
	var interps []*ast.Interp
	for _, s := range v.Segments {
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
	min := cascadeJS(sb.String(), ext)
	if out, ok := splitJSSentinels(min, prefix, interps); ok {
		v.Segments = out
	}
	// On any sentinel mismatch, leave v.Segments unchanged (safe).
}

// splitJSSentinels reassembles a minified sentinel string into Text + Interp
// nodes. Each `<prefix><digits>z` run is replaced by interps[<digits>]; the spans
// between become Text nodes. ok=false if any sentinel index is out of range,
// duplicated, or missing (every hole must survive exactly once).
func splitJSSentinels(s, prefix string, interps []*ast.Interp) ([]ast.Markup, bool) {
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
