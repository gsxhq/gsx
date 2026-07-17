package jsmin

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/jsfmt"
	"github.com/gsxhq/gsx/internal/pretty"
)

// Minifiers carries the pluggable external minifier funcs jsmin threads
// through a file: JS minifies a script/attribute JS body, JSON minifies a
// JSON-shaped body (data-island <script> text, and a JSON-shaped js`…`
// attribute value — both holeless and holey, via cascadeJS and
// minifyJSSegmentsHoley respectively). A nil field uses the built-in safe
// pass for that kind.
type Minifiers struct {
	JS   func(string) (string, error) // nil = safe level (built-in)
	JSON func(string) (string, error) // nil = safe level (built-in)
}

// MinifyFile minifies the static JS of every <script> element in f, in place.
// m.JS, if non-nil, minifies the script's JS (the pluggable extension point); a
// nil m.JS uses the built-in safe minifier. Only HOLELESS <script> blocks (all
// *ast.Text children) are minified: a script carrying any @{ } hole (an
// *ast.Interp child) is left UNCHANGED, because segment-minifying the Text runs
// around a hole could collapse whitespace across the hole boundary and change
// ASI semantics. Correctness over minification for holey scripts in this slice.
func MinifyFile(f *ast.File, m Minifiers) error {
	for _, d := range f.Decls {
		switch v := d.(type) {
		case *ast.Component:
			if err := minifyMarkup(v.Body, m); err != nil {
				return err
			}
		case *ast.GoWithElements:
			// A top-level var initializer such as `var h = js`…`` carries its
			// literal in the GoWithElements Parts split.
			if err := minifyGoParts(v.Parts, m); err != nil {
				return err
			}
		}
	}
	return nil
}

func minifyMarkup(nodes []ast.Markup, m Minifiers) error {
	for _, n := range nodes {
		switch v := n.(type) {
		case *ast.Element:
			// A data-block <script> (e.g. type="application/json") is not
			// JavaScript; running the JS minifier on its body is wrong. Leave the
			// body unchanged (a HOLELESS static JSON block would otherwise be
			// JS-minified). Its attributes are still walked below.
			if strings.EqualFold(v.Tag, "script") && !isDataIslandScript(v) {
				mc, err := minifyScriptChildren(v.Children, m)
				if err != nil {
					return err
				}
				v.Children = mc
			} else if err := minifyMarkup(v.Children, m); err != nil {
				return err
			}
			// Minify js`…` attribute values (x-data, @click, hx-on::…) and recurse
			// into markup-slot / conditional attributes.
			if err := minifyJSAttrs(v.Attrs, m); err != nil {
				return err
			}
		case *ast.Fragment:
			if err := minifyMarkup(v.Children, m); err != nil {
				return err
			}
		case *ast.IfMarkup:
			if err := minifyMarkup(v.Then, m); err != nil {
				return err
			}
			if err := minifyMarkup(v.Else, m); err != nil {
				return err
			}
		case *ast.ForMarkup:
			if err := minifyMarkup(v.Body, m); err != nil {
				return err
			}
		case *ast.SwitchMarkup:
			for i := range v.Cases {
				if err := minifyMarkup(v.Cases[i].Body, m); err != nil {
					return err
				}
			}
		case *ast.EmbeddedInterp:
			// A body-position { js`…` } literal.
			if v.Lang == ast.EmbeddedJS {
				v.Segments = minifyJSSegments(v.Segments, m)
			}
		case *ast.GoBlock:
			// js` literals in a {{ }} block, carried in the analyze-populated split.
			if err := minifyGoParts(v.Embedded, m); err != nil {
				return err
			}
		case *ast.Interp:
			// js` literals embedded in a { expr } hole.
			if err := minifyGoParts(v.Embedded, m); err != nil {
				return err
			}
		}
	}
	return nil
}

// minifyGoParts minifies the js` literals in a GoBlock's or Interp's Embedded
// split (populated by analyze). GoText parts hold no body; element/fragment parts
// recurse through minifyMarkup.
func minifyGoParts(parts []ast.GoPart, m Minifiers) error {
	for _, p := range parts {
		switch v := p.(type) {
		case *ast.EmbeddedInterp:
			if v.Lang == ast.EmbeddedJS {
				v.Segments = minifyJSSegments(v.Segments, m)
			}
		case *ast.Element:
			if err := minifyMarkup([]ast.Markup{v}, m); err != nil {
				return err
			}
		case *ast.Fragment:
			if err := minifyMarkup([]ast.Markup{v}, m); err != nil {
				return err
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

func minifyScriptChildren(children []ast.Markup, m Minifiers) ([]ast.Markup, error) {
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
	if m.JS != nil {
		mo, err := m.JS(src)
		if err != nil {
			return nil, fmt.Errorf("jsmin: external JS minifier: %w", err)
		}
		min = mo
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
func minifyJSAttrs(attrs []ast.Attr, m Minifiers) error {
	for _, a := range attrs {
		switch v := a.(type) {
		case *ast.EmbeddedAttr:
			if v.Lang == ast.EmbeddedJS {
				v.Segments = minifyJSSegments(v.Segments, m)
			}
		case *ast.MarkupAttr:
			if err := minifyMarkup(v.Value, m); err != nil {
				return err
			}
		case *ast.CondAttr:
			if err := minifyJSAttrs(v.Then, m); err != nil {
				return err
			}
			if err := minifyJSAttrs(v.Else, m); err != nil {
				return err
			}
		}
	}
	return nil
}

// minifyJSSegments minifies one js`…` literal body (an attribute value or a
// Go-expression EmbeddedInterp — both are just Segments), returning the minified
// segments. A HOLELESS body is cascade-minified. A holey body uses a sentinel
// round-trip: cascade-minified under the full minifier, REINDENTED under the safe
// level (see minifyJSSegmentsHoley).
func minifyJSSegments(segments []ast.Markup, m Minifiers) []ast.Markup {
	for _, s := range segments {
		if _, ok := s.(*ast.Interp); ok {
			return minifyJSSegmentsHoley(segments, m)
		}
	}
	var sb strings.Builder
	for _, s := range segments {
		if t, ok := s.(*ast.Text); ok {
			sb.WriteString(t.Value)
		}
	}
	min := cascadeJS(sb.String(), m)
	if min == "" {
		return segments
	}
	return []ast.Markup{&ast.Text{Value: min}}
}

// looksJSON reports whether text is a JSON object/array literal (a real parse,
// not a shape guess). Callers pass a hole-free string (holeless value, or a
// holey value with holes replaced by JSON-valid integer sentinels).
func looksJSON(text string) bool {
	t := strings.TrimLeft(text, " \t\r\n")
	if len(t) == 0 || (t[0] != '{' && t[0] != '[') {
		return false
	}
	return json.Valid([]byte(text))
}

// cascadeJS minifies a JS FRAGMENT. tdewolff (m.JS) parses its input as a
// program, so an object literal must be minified as an EXPRESSION via a `(…)`
// wrap (kept in output — a parenthesized expression is an equivalent value).
//
// A `{`/`[`-leading value that is ALSO valid JSON (htmx hx-vals/hx-headers/
// hx-vars, parsed by htmx with JSON.parse) is routed to the JSON minifier
// first: JS minification would unquote keys and wrap objects in `(…)`, both of
// which JSON.parse rejects. The JSON minifier is whitespace-only and never
// rewrites values, so it can't break validity; on error it falls through to
// the JS cascade below (same as any other minifier miss).
//
// Order matters for the JS fallback. A value that starts with `{` is an object
// literal (x-data, Alpine `:class`/`:style` object bindings) OR a `{ … }`
// statement block. Parsing it raw is WRONG for a single-property object:
// `{ open: false }` is a valid program (a labeled block — `open:` label +
// `false`), so m.JS(raw) would strip the braces to `open:!1` and break it. So
// for `{`-leading values we wrap FIRST (object expression) and fall back to
// raw (a real statement block, where the wrap fails). Non-`{` values (handler
// statements, call expressions) parse raw first. Either way, the safe
// never-erroring built-in is the final fallback (and the whole safe level,
// where m.JS is nil).
func cascadeJS(text string, m Minifiers) string {
	if m.JSON != nil && looksJSON(text) {
		if o, err := m.JSON(text); err == nil {
			return o
		}
	}
	if m.JS != nil {
		first, second := text, "("+text+")"
		if strings.HasPrefix(strings.TrimLeft(text, " \t\r\n"), "{") {
			first, second = second, first
		}
		if o, err := m.JS(first); err == nil {
			return o
		}
		if o, err := m.JS(second); err == nil {
			return o
		}
	}
	return minifyJS(text)
}

// minifyJSSegmentsHoley transforms a holey js`…` value via a sentinel round-trip:
// each @{ } hole becomes a collision-free sentinel token, the whole is transformed
// as one syntactically-complete string, then the sentinels are split back into the
// original *ast.Interp holes.
//
// Before anything else (FULL level only), it tries a JSON-shaped classification: a
// `{`/`[`-leading value is retried with each hole as a bare INTEGER sentinel (a
// valid JSON number token). If that string is valid JSON, it is minified by the
// JSON minifier and split back by exact numeric match — this keeps quoted keys and
// avoids the `(…)` expression wrap the JS cascade requires, both of which htmx's
// JSON.parse (hx-vals/hx-headers/hx-vars) would reject. Any classification miss or
// split mismatch falls through to the identifier-sentinel JS path unchanged.
//
// Otherwise, each hole becomes a collision-free FREE IDENTIFIER (which the JS lexer
// never mangles), and the transform depends on the level:
//
//   - FULL (m.JS != nil): cascade-minify the sentinel string. Safe because attribute
//     holes sit in expression value positions (object property values, call args,
//     spreads), so the hole-as-identifier keeps a valid parse.
//   - SAFE (m.JS == nil): do NOT minify (minifying around holes is the deferred gap;
//     see the package doc). Instead REINDENT, via the same jsfmt.Format the emit-side
//     rebase (MinifyNone) uses — this is artifact removal, not minification. The
//     source carries markup-level tabs from gsx formatting; because the surrounding
//     HTML is whitespace-minified, leaving those tabs in would make the js appear
//     absurdly deep and unreadable. Reindent re-bases the body to its own brace depth
//     (keeping a readable structure) and touches leading whitespace only — never
//     collapsing intra-line or hole-boundary whitespace — so it is safe on a hole.
func minifyJSSegmentsHoley(segments []ast.Markup, m Minifiers) []ast.Markup {
	// A collision-free identifier prefix absent from every Text segment. The
	// sentinel is `<prefix><index>z` — prefix and digits are identifier chars and
	// the `z` terminates the digit run so `<prefix>1z` and `<prefix>12z` never
	// alias.
	var scan strings.Builder
	for _, s := range segments {
		if t, ok := s.(*ast.Text); ok {
			scan.WriteString(t.Value)
		}
	}

	// JSON branch (full level only): a `{`/`[`-leading holey value whose holes sit
	// in JSON value positions (object property values, array elements) is tried as
	// JSON first, via INTEGER sentinels instead of identifiers — a bare integer is
	// a valid JSON number token, so the sentinel string can be json.Valid-checked
	// and, if valid, minified by the tdewolff JSON minifier and split back by
	// numeric match. Sentinels are `base+1..base+n` — never the bare `base` itself
	// — because tdewolff's number minifier rewrites an exact round value like
	// 900000000 to `9e8` (verified); base+1 and up (non-round) survive verbatim
	// across many growth cycles (also verified). splitJSNumberSentinels parses
	// full JSON number TOKENS (not bare digit runs), so even if some future/edge
	// case reshapes a sentinel into exponent form, it is matched by numeric value
	// rather than mis-matching a truncated digit prefix. Falls through to the
	// identifier-sentinel JS cascade below on any classification miss or split
	// mismatch (same "never corrupt output" contract as the existing JS path).
	if m.JSON != nil {
		n := countInterps(segments)
		base := int64(900000000)
		for containsAnySentinel(scan.String(), base, n) {
			base *= 10
		}
		numStr, numInterps := buildNumberSentinelString(segments, base)
		if looksJSON(numStr) {
			if out, err := m.JSON(numStr); err == nil {
				if split, ok := splitJSNumberSentinels(out, base, numInterps); ok {
					return split
				}
			}
		}
	}

	prefix := "gsxHole"
	for strings.Contains(scan.String(), prefix) {
		prefix += "q"
	}

	var sb strings.Builder
	var interps []*ast.Interp
	for _, s := range segments {
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

	var transformed string
	if m.JS == nil {
		out, err := jsfmt.Format([]byte(sb.String()), 0, pretty.DefaultTabWidth)
		if err != nil {
			return segments // lex error → leave unchanged (safe)
		}
		transformed = string(out)
	} else {
		transformed = cascadeJS(sb.String(), m)
	}
	if out, ok := splitJSSentinels(transformed, prefix, interps); ok {
		return out
	}
	// On any sentinel mismatch, leave the segments unchanged (safe).
	return segments
}

// countInterps counts the *ast.Interp holes in segments.
func countInterps(segments []ast.Markup) int {
	n := 0
	for _, s := range segments {
		if _, ok := s.(*ast.Interp); ok {
			n++
		}
	}
	return n
}

// containsAnySentinel reports whether any of the n candidate integer sentinels
// base+1..base+n appears as a substring of text — a collision the caller must
// grow base away from (mirroring the identifier-prefix collision loop above).
// (Sentinel numbering starts at base+1, not base — see buildNumberSentinelString.)
func containsAnySentinel(text string, base int64, n int) bool {
	for i := 1; i <= n; i++ {
		if strings.Contains(text, strconv.FormatInt(base+int64(i), 10)) {
			return true
		}
	}
	return false
}

// buildNumberSentinelString rebuilds segments as one string with each
// *ast.Interp hole replaced by the bare integer literal base+1+<its index> (a
// valid JSON number token), returning the *ast.Interp pointers in the same
// index order (the i-th returned interp was substituted as base+1+i).
//
// Numbering starts at base+1, never the bare base itself: tdewolff's JSON
// number minifier rewrites an exact round value like 900000000 to the shorter
// `9e8`, which would defeat a literal round-trip; base+1 and up carry a
// non-zero low digit and survive verbatim (verified empirically, including
// across repeated *10 growth of base).
func buildNumberSentinelString(segments []ast.Markup, base int64) (string, []*ast.Interp) {
	var sb strings.Builder
	var interps []*ast.Interp
	for _, s := range segments {
		switch t := s.(type) {
		case *ast.Text:
			sb.WriteString(t.Value)
		case *ast.Interp:
			sb.WriteString(strconv.FormatInt(base+1+int64(len(interps)), 10))
			interps = append(interps, t)
		}
	}
	return sb.String(), interps
}

// scanJSONNumberToken scans one JSON number token (RFC 8259 grammar: an
// optional `-`, an integer part, an optional `.digits` fraction, an optional
// `[eE][+-]?digits` exponent) starting at s[i] and returns its exclusive end
// index. The caller has already established s[i] begins a number (a digit, or
// `-` followed by a digit).
func scanJSONNumberToken(s string, i int) int {
	j := i
	if j < len(s) && s[j] == '-' {
		j++
	}
	for j < len(s) && s[j] >= '0' && s[j] <= '9' {
		j++
	}
	if j < len(s) && s[j] == '.' {
		k := j + 1
		for k < len(s) && s[k] >= '0' && s[k] <= '9' {
			k++
		}
		if k > j+1 {
			j = k
		}
	}
	if j < len(s) && (s[j] == 'e' || s[j] == 'E') {
		k := j + 1
		if k < len(s) && (s[k] == '+' || s[k] == '-') {
			k++
		}
		start := k
		for k < len(s) && s[k] >= '0' && s[k] <= '9' {
			k++
		}
		if k > start {
			j = k
		}
	}
	return j
}

// splitJSNumberSentinels reassembles a JSON-minified integer-sentinel string
// into Text + Interp nodes. It scans s for JSON number TOKENS (not bare digit
// runs — a minifier is free to reshape a number into exponent form, e.g.
// `9e8`, so matching must be by parsed numeric VALUE, never by a truncated
// digit prefix, or a token like `900000001e3` could be misread as sentinel
// base+1). A token whose value is an integer in [base+1, base+len(interps)]
// is a sentinel and is replaced by the corresponding interp (index
// value-base-1); every other number token (a static JSON number from the
// source) is left as literal text, byte-for-byte. ok=false if any hole ends
// up missing or duplicated (every hole must survive exactly once) — the
// caller falls back safely.
func splitJSNumberSentinels(s string, base int64, interps []*ast.Interp) ([]ast.Markup, bool) {
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
		c := s[i]
		isNumStart := (c >= '0' && c <= '9') || (c == '-' && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9')
		if !isNumStart {
			text.WriteByte(c)
			i++
			continue
		}
		j := scanJSONNumberToken(s, i)
		tok := s[i:j]
		i = j
		// Values in this scheme's range (~1e9-1e14) are well within float64's
		// exact-integer precision (2^53), so ParseFloat round-trips exactly.
		if val, err := strconv.ParseFloat(tok, 64); err == nil {
			if iv := int64(val); float64(iv) == val {
				if idx := iv - base - 1; idx >= 0 && idx < int64(len(interps)) {
					if seen[idx] {
						return nil, false // duplicate sentinel: abort, fall back safely
					}
					seen[idx] = true
					flush()
					out = append(out, interps[idx])
					continue
				}
			}
		}
		text.WriteString(tok)
	}
	flush()
	for _, ok := range seen {
		if !ok {
			return nil, false
		}
	}
	return out, true
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
