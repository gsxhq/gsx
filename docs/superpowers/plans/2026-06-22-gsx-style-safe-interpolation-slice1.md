# gsx `<style>` / `style=` Safe Interpolation — Slice 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Allow `${ expr }` interpolation inside `<style>` blocks and auto-sanitized `style={ expr }` attributes, with a real CSS value-filter and a `gsx.SafeCSS` opt-out — safe by default, like gsx's URL handling.

**Architecture:** Three independent layers plus glue. (1) Runtime: a `gsx.SafeCSS` string type + a port of `html/template`'s `cssValueFilter`, exposed as two `*Writer` methods `CSS`/`CSSAttr`. (2) Parser: `parseRawTextBody` learns to split a `<style>` body into `Text`+`Interp` on `${`. (3) Codegen: a positional CSS-context emit path (inside `<style>`, or the `style=` attribute) that dispatches on the resolved type — `SafeCSS`→raw, numeric→`strconv`, string→value-filter. (4) Printer prints `${ }` in `<style>`. The skeleton/probe/harvest walks already recurse into element children generically, so type resolution of `<style>` interps needs no changes.

**Tech Stack:** Go (stdlib-only runtime; generator may use `golang.org/x/tools`). Existing gsx packages: root `gsx` (runtime), `parser`, `ast`, `internal/codegen`, `internal/printer`, `internal/wsnorm`, `internal/corpus`.

## Global Constraints

- **Runtime is stdlib-only** (the root `gsx` package + `escape.go`/`writer.go`/`safe.go`). No third-party imports there. The generator (`internal/codegen`) may use `golang.org/x/tools`.
- **Unexported by default** — new helpers/types that need no serialization or cross-package use start lowercase (e.g. `cssValueFilter`, `isSafeCSS`). Exported: `gsx.SafeCSS`, `(*Writer).CSS`, `(*Writer).CSSAttr`.
- **The ORDER INVARIANT** (`collectExprs` ≡ `emitProbes` ≡ `harvest` traversal) must stay intact — this slice adds NO new probe/collect logic; `<style>` interps ride the existing generic element-children recursion.
- **CSS safety is a PORT, never an approximation** — `cssValueFilter` and its helpers are copied from `$(go env GOROOT)/src/html/template/css.go` with only the typed-string machinery removed. Do not hand-roll CSS-safety logic.
- After each task: `go build ./...` and `go test ./...` must pass before committing.

**Slice-1 boundaries (deferred, error clearly):** pipeline stages (`${ x |> f }`) and the `?` try-marker inside `<style>` interps; composed `style={ "a", cond }` (`ClassAttr`) auto-sanitize (only `style={ expr }` `ExprAttr` is unlocked here); `<script>` interpolation; CSS minification (slice 2).

---

### Task 1: Runtime — `gsx.SafeCSS` + `cssValueFilter` + `gw.CSS`/`gw.CSSAttr`

**Files:**
- Create: `safe.go`
- Modify: `escape.go` (append the CSS filter + helpers)
- Modify: `writer.go` (append two methods)
- Test: `escape_test.go` (append `TestCSSValueFilter`), `writer_test.go` (append `TestWriterCSS`)

**Interfaces:**
- Produces: `type SafeCSS string`; `func cssValueFilter(s string) string` (unexported); `func (gw *Writer) CSS(s string)`; `func (gw *Writer) CSSAttr(s string)`.
- Consumes: existing `writeHTML(io.Writer, string) error`, `(*Writer).writeStr`, the `Writer{w, err}` shape.

- [ ] **Step 1: Write the failing filter test** — append to `escape_test.go`:

```go
func TestCSSValueFilter(t *testing.T) {
	tests := []struct{ css, want string }{
		{"", ""},
		{"foo", "foo"},
		{"0", "0"},
		{"0px", "0px"},
		{"-5px", "-5px"},
		{"1.25in", "1.25in"},
		{"+.33em", "+.33em"},
		{"100%", "100%"},
		{".foo", ".foo"},
		{"#bar", "#bar"},
		{"-moz-corner-radius", "-moz-corner-radius"},
		{"#123456", "#123456"},
		{"U+00-FF, U+980-9FF", "U+00-FF, U+980-9FF"},
		{"color: red", "color: red"},
		{"<!--", "ZgotmplZ"},
		{"-->", "ZgotmplZ"},
		{"</style", "ZgotmplZ"},
		{`"`, "ZgotmplZ"},
		{`'`, "ZgotmplZ"},
		{"`", "ZgotmplZ"},
		{"\x00", "ZgotmplZ"},
		{"/* foo */", "ZgotmplZ"},
		{"//", "ZgotmplZ"},
		{"rgb(1,2,3)", "ZgotmplZ"},
		{"expression(alert(1337))", "ZgotmplZ"},
		{"EXPRESSION", "ZgotmplZ"},
		{"-moz-binding", "ZgotmplZ"},
		{`-express\69on(alert(1337))`, "ZgotmplZ"},
		{`-expre\0000073sion`, "-expre\x073sion"},
		{"@import url evil.css", "ZgotmplZ"},
		{"<", "ZgotmplZ"},
		{">", "ZgotmplZ"},
	}
	for _, tt := range tests {
		if got := cssValueFilter(tt.css); got != tt.want {
			t.Errorf("cssValueFilter(%q) = %q, want %q", tt.css, got, tt.want)
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test . -run TestCSSValueFilter`
Expected: FAIL — `undefined: cssValueFilter`.

- [ ] **Step 3: Port the filter into `escape.go`** — append (and add `"bytes"`, `"unicode/utf8"` to the import block):

```go
// cssFailsafe replaces a CSS value that could break out of its context. It
// mirrors html/template's "ZgotmplZ" sentinel — a deliberately inert identifier
// that renders harmlessly.
const cssFailsafe = "ZgotmplZ"

var cssExpressionBytes = []byte("expression")
var cssMozBindingBytes = []byte("mozbinding")

// cssValueFilter returns s when it is a safe CSS value, else cssFailsafe. It is a
// port of the standard library's html/template/css.go cssValueFilter (and its
// helpers decodeCSS/isCSSNmchar/skipCSSSpace/hexDecode/isHex), with the typed-
// string machinery removed: gsx always passes an untrusted plain string here.
// It conservatively rejects any value containing 0x00 " ' ( ) / ; @ [ \ ] ` { }
// < >, a -- run, or (after decoding+lowercasing) "expression"/"mozbinding".
func cssValueFilter(s string) string {
	b, id := decodeCSS([]byte(s)), make([]byte, 0, 64)
	for i, c := range b {
		switch c {
		case 0, '"', '\'', '(', ')', '/', ';', '@', '[', '\\', ']', '`', '{', '}', '<', '>':
			return cssFailsafe
		case '-':
			if i != 0 && b[i-1] == '-' {
				return cssFailsafe
			}
		default:
			if c < utf8.RuneSelf && isCSSNmchar(rune(c)) {
				id = append(id, c)
			}
		}
	}
	id = bytes.ToLower(id)
	if bytes.Contains(id, cssExpressionBytes) || bytes.Contains(id, cssMozBindingBytes) {
		return cssFailsafe
	}
	return string(b)
}

// isCSSNmchar reports whether r is a CSS3 nmchar (ignoring multi-rune escapes).
func isCSSNmchar(r rune) bool {
	return 'a' <= r && r <= 'z' ||
		'A' <= r && r <= 'Z' ||
		'0' <= r && r <= '9' ||
		r == '-' || r == '_' ||
		0x80 <= r && r <= 0xd7ff ||
		0xe000 <= r && r <= 0xfffd ||
		0x10000 <= r && r <= 0x10ffff
}

// decodeCSS decodes CSS3 escape sequences in s.
func decodeCSS(s []byte) []byte {
	i := bytes.IndexByte(s, '\\')
	if i == -1 {
		return s
	}
	b := make([]byte, 0, len(s))
	for len(s) != 0 {
		i := bytes.IndexByte(s, '\\')
		if i == -1 {
			i = len(s)
		}
		b, s = append(b, s[:i]...), s[i:]
		if len(s) < 2 {
			break
		}
		if isHex(s[1]) {
			j := 2
			for j < len(s) && j < 7 && isHex(s[j]) {
				j++
			}
			r := hexDecode(s[1:j])
			if r > utf8.MaxRune {
				r, j = r/16, j-1
			}
			n := utf8.EncodeRune(b[len(b):cap(b)], r)
			b, s = b[:len(b)+n], skipCSSSpace(s[j:])
		} else {
			_, n := utf8.DecodeRune(s[1:])
			b, s = append(b, s[1:1+n]...), s[1+n:]
		}
	}
	return b
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

func hexDecode(s []byte) rune {
	n := '\x00'
	for _, c := range s {
		n <<= 4
		switch {
		case '0' <= c && c <= '9':
			n |= rune(c - '0')
		case 'a' <= c && c <= 'f':
			n |= rune(c-'a') + 10
		case 'A' <= c && c <= 'F':
			n |= rune(c-'A') + 10
		default:
			panic("bad hex digit")
		}
	}
	return n
}

func skipCSSSpace(c []byte) []byte {
	if len(c) == 0 {
		return c
	}
	switch c[0] {
	case '\t', '\n', '\f', ' ':
		return c[1:]
	case '\r':
		if len(c) >= 2 && c[1] == '\n' {
			return c[2:]
		}
		return c[1:]
	}
	return c
}
```

Note: the loop uses `for i, c := range b` where `b` is `[]byte`, so `c` is a `byte` (matches the stdlib). `utf8.MaxRune` replaces the stdlib's `unicode.MaxRune` so no `"unicode"` import is needed.

- [ ] **Step 4: Run the filter test to verify it passes**

Run: `go test . -run TestCSSValueFilter`
Expected: PASS.

- [ ] **Step 5: Write the failing writer/type test** — append to `writer_test.go`:

```go
func TestWriterCSS(t *testing.T) {
	cases := []struct {
		name   string
		render func(*Writer)
		want   string
	}{
		{"block safe", func(w *Writer) { w.CSS("10px") }, "10px"},
		{"block breakout", func(w *Writer) { w.CSS("red;}body{x") }, "ZgotmplZ"},
		{"attr safe", func(w *Writer) { w.CSSAttr("color: red") }, "color: red"},
		{"attr breakout", func(w *Writer) { w.CSSAttr(`a"b`) }, "ZgotmplZ"},
		{"safecss type is a string", func(w *Writer) { w.S(string(SafeCSS("1px solid"))) }, "1px solid"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var b strings.Builder
			w := W(&b)
			c.render(w)
			if err := w.Err(); err != nil {
				t.Fatalf("Err = %v", err)
			}
			if b.String() != c.want {
				t.Errorf("got %q, want %q", b.String(), c.want)
			}
		})
	}
}
```

(If `writer_test.go` lacks `"strings"` in its imports, add it.)

- [ ] **Step 6: Run to verify it fails**

Run: `go test . -run TestWriterCSS`
Expected: FAIL — `undefined: SafeCSS`, `w.CSS undefined`, `w.CSSAttr undefined`.

- [ ] **Step 7: Create `safe.go`**

```go
package gsx

// SafeCSS is a string the template author vouches for as safe CSS. In a CSS
// context — inside a <style> block or a style= attribute — a SafeCSS value is
// emitted verbatim, bypassing the gw.CSS value-filter (the CSS analogue of
// trusting raw HTML via Raw). Use it only for CSS you control, never for
// untrusted data.
type SafeCSS string
```

- [ ] **Step 8: Add the two methods to `writer.go`** — append:

```go
// CSS writes s into a <style> raw-text context, value-filtered so it cannot
// break out of a CSS value. The filter rejects '<', so the result is raw-text
// safe and needs no HTML escaping.
func (gw *Writer) CSS(s string) {
	gw.writeStr(cssValueFilter(s))
}

// CSSAttr writes s into a style="…" attribute: value-filtered, then
// HTML-attribute-escaped so it can never break the quote (CSS survives HTML
// decoding).
func (gw *Writer) CSSAttr(s string) {
	if gw.err != nil {
		return
	}
	gw.err = writeHTML(gw.w, cssValueFilter(s))
}
```

- [ ] **Step 9: Run the writer test, then the full runtime package**

Run: `go test . -run TestWriterCSS && go test .`
Expected: PASS.

- [ ] **Step 10: Commit**

```bash
git add safe.go escape.go writer.go escape_test.go writer_test.go
git commit -m "runtime: gsx.SafeCSS + cssValueFilter port + gw.CSS/CSSAttr"
```

---

### Task 2: Parser — `${ }` interpolation inside `<style>`

**Files:**
- Modify: `parser/markup.go` (`parseRawTextBody`, ~line 665)
- Test: `parser/markup_test.go` (append tests)

**Interfaces:**
- Consumes: existing `(*parser).parseInterp() (*ast.Interp, error)` (cursor at `{`), `p.posAt`, `p.eof`, `p.peek`, `isTagNameByte`, `p.skipSpace`, `ast.SetSpan`, `strings.EqualFold`.
- Produces: a `<style>` element whose `Children` is a `[]ast.Markup` of `*ast.Text` and `*ast.Interp` (other raw-text tags, incl. `<script>`, still yield a single `*ast.Text`).

- [ ] **Step 1: Write the failing parser tests** — append to `parser/markup_test.go`:

```go
func TestStyleInterpolation(t *testing.T) {
	src := `<style>.a{width:${w}px}</style>`
	p := testParser(src)
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := n.(*ast.Element)
	if len(el.Children) != 3 {
		t.Fatalf("got %d children, want 3: %#v", len(el.Children), el.Children)
	}
	if txt, ok := el.Children[0].(*ast.Text); !ok || txt.Value != ".a{width:" {
		t.Fatalf("child0 = %#v, want Text \".a{width:\"", el.Children[0])
	}
	if in, ok := el.Children[1].(*ast.Interp); !ok || in.Expr != "w" {
		t.Fatalf("child1 = %#v, want Interp{Expr:w}", el.Children[1])
	}
	if txt, ok := el.Children[2].(*ast.Text); !ok || txt.Value != "px}" {
		t.Fatalf("child2 = %#v, want Text \"px}\"", el.Children[2])
	}
}

func TestScriptStaysRaw(t *testing.T) {
	// <script> must NOT interpolate: ${x} is literal text.
	src := `<script>var x = ${y};</script>`
	p := testParser(src)
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := n.(*ast.Element)
	if len(el.Children) != 1 {
		t.Fatalf("got %d children, want 1: %#v", len(el.Children), el.Children)
	}
	if txt, ok := el.Children[0].(*ast.Text); !ok || txt.Value != "var x = ${y};" {
		t.Fatalf("child0 = %#v, want literal Text", el.Children[0])
	}
}

func TestStyleBareDollarIsLiteral(t *testing.T) {
	// A '$' not immediately followed by '{' stays raw, as do bare { } #.
	src := `<style>.a{c:$x; #d{} }</style>`
	p := testParser(src)
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := n.(*ast.Element)
	if len(el.Children) != 1 {
		t.Fatalf("got %d children, want 1 (all literal): %#v", len(el.Children), el.Children)
	}
	if txt := el.Children[0].(*ast.Text); txt.Value != ".a{c:$x; #d{} }" {
		t.Fatalf("text = %q", txt.Value)
	}
}

func TestStyleUnterminatedInterp(t *testing.T) {
	src := `<style>.a{w:${w}</style>`
	p := testParser(src)
	if _, err := p.parseElement(); err == nil {
		t.Fatal("expected an error for unterminated interpolation, got nil")
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./parser -run 'TestStyleInterpolation|TestScriptStaysRaw|TestStyleBareDollarIsLiteral|TestStyleUnterminatedInterp'`
Expected: FAIL — `TestStyleInterpolation` gets 1 child (the whole body as one Text), etc.

- [ ] **Step 3: Replace `parseRawTextBody`** in `parser/markup.go` with the interpolation-aware version:

```go
// parseRawTextBody consumes a raw-text element body until the matching
// case-insensitive `</tag>` close tag, which it consumes. For <style> the body
// is split into Text and ${ … } Interp children; for every other raw-text tag
// (e.g. <script>) the body is a single verbatim Text. openPos describes the open
// tag, used for the unterminated error.
func (p *parser) parseRawTextBody(tag string, openPos token.Position) ([]ast.Markup, error) {
	interpolate := strings.EqualFold(tag, "style")
	closeLower := "</" + strings.ToLower(tag)
	var nodes []ast.Markup
	segStart := p.i
	segStartPos := p.posAt(p.i)
	flush := func(end int) {
		if end > segStart {
			txt := &ast.Text{Value: p.src[segStart:end]}
			ast.SetSpan(txt, segStartPos, p.posAt(end))
			nodes = append(nodes, txt)
		}
	}
	for !p.eof() {
		// Close tag?
		if p.peek() == '<' && p.i+1 < len(p.src) && p.src[p.i+1] == '/' &&
			p.i+len(closeLower) <= len(p.src) &&
			strings.EqualFold(p.src[p.i:p.i+len(closeLower)], closeLower) {
			after := p.i + len(closeLower)
			if after >= len(p.src) || !isTagNameByte(p.src[after]) {
				flush(p.i)
				p.i += len(closeLower)
				p.skipSpace()
				if p.peek() != '>' {
					cp := p.file.Position(p.pos())
					return nil, fmt.Errorf("%d:%d: malformed close tag </%s>", cp.Line, cp.Column, tag)
				}
				p.i++ // past '>'
				return nodes, nil
			}
		}
		// Interpolation? (<style> only; trigger is exactly `${`.)
		if interpolate && p.peek() == '$' && p.i+1 < len(p.src) && p.src[p.i+1] == '{' {
			flush(p.i)
			p.i++ // past '$'; cursor now at '{' for parseInterp
			in, err := p.parseInterp()
			if err != nil {
				return nil, err
			}
			nodes = append(nodes, in)
			segStart = p.i
			segStartPos = p.posAt(p.i)
			continue
		}
		p.i++
	}
	return nil, fmt.Errorf("%d:%d: unterminated raw-text element <%s>", openPos.Line, openPos.Column, tag)
}
```

- [ ] **Step 4: Run the new tests, then the full parser package**

Run: `go test ./parser -run 'TestStyle|TestScriptStaysRaw' && go test ./parser`
Expected: PASS (the whole `parser` package stays green — `<script>` behavior is unchanged).

- [ ] **Step 5: Commit**

```bash
git add parser/markup.go parser/markup_test.go
git commit -m "parser: \${ } interpolation inside <style> bodies (script stays raw)"
```

---

### Task 3: Codegen — CSS-context emit dispatch (`<style>` interps + `style=`)

**Files:**
- Modify: `internal/codegen/analyze.go` (add `isSafeCSS`)
- Modify: `internal/codegen/emit.go` (style-children emit, `emitRenderCSS`, `emitCSSAttrValue`, `emitExprAttr` CSS case)
- Test: `internal/corpus/testdata/cases/codegen-shape/style_interp.txtar` (new — exact generated-Go golden)

**Interfaces:**
- Consumes: `gsx.SafeCSS` / `(*Writer).CSS` / `(*Writer).CSSAttr` (Task 1); `<style>` `Interp` children (Task 2); existing `classify`, `emitS`, `emitAttrValue`, `attrContext`, `interpTemp`, `urlStringExpr`, `types.Named`.
- Produces: `func isSafeCSS(types.Type) bool`; CSS-context emission routing `_gsxgw.CSS`/`_gsxgw.CSSAttr`/`_gsxgw.S`/`_gsxgw.AttrValue` by type.

- [ ] **Step 1: Add `isSafeCSS` to `analyze.go`** (near `classify`):

```go
// isSafeCSS reports whether t is the named type github.com/gsxhq/gsx.SafeCSS —
// the author-vouched safe-CSS string, emitted raw in a CSS context.
func isSafeCSS(t types.Type) bool {
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj != nil && obj.Name() == "SafeCSS" &&
		obj.Pkg() != nil && obj.Pkg().Path() == "github.com/gsxhq/gsx"
}
```

- [ ] **Step 2: Add the CSS emit helpers to `emit.go`** (place after `emitRender`):

```go
// genStyleChild emits one child of a <style> element. Text is raw CSS (verbatim);
// an Interp is rendered in CSS context (auto-sanitized). <style> bodies contain
// only Text and ${ } interps (parser guarantee).
func genStyleChild(b *bytes.Buffer, n ast.Markup, resolved map[ast.Node]types.Type, imports map[string]bool, fset *token.FileSet) error {
	switch t := n.(type) {
	case *ast.Text:
		emitS(b, t.Value)
		return nil
	case *ast.Interp:
		return emitCSSInterp(b, t, resolved, imports, fset)
	default:
		return fmt.Errorf("codegen: <style> body may contain only text and ${ } interpolations, got %T", n)
	}
}

// emitCSSInterp renders a <style> interpolation value in CSS block context.
func emitCSSInterp(b *bytes.Buffer, n *ast.Interp, resolved map[ast.Node]types.Type, imports map[string]bool, fset *token.FileSet) error {
	if n.Try {
		return fmt.Errorf("codegen: `?` try-marker not supported in <style> interpolation yet")
	}
	if len(n.Stages) > 0 {
		return fmt.Errorf("codegen: pipeline stages not supported in <style> interpolation yet")
	}
	t, ok := resolved[n]
	if !ok || t == nil {
		return fmt.Errorf("codegen: could not resolve type of <style> interpolation %q", n.Expr)
	}
	expr := strings.TrimSpace(n.Expr)
	if tup, ok := t.(*types.Tuple); ok {
		if tup.Len() != 2 || tup.At(1).Type().String() != "error" {
			return fmt.Errorf("codegen: <style> interpolation %q returns %s; only (T, error) is supported", expr, t)
		}
		tmp := fmt.Sprintf("_gsxv%d", interpTemp)
		interpTemp++
		fmt.Fprintf(b, "\t\t%s, _gsxerr := %s\n\t\tif _gsxerr != nil {\n\t\t\treturn _gsxerr\n\t\t}\n", tmp, expr)
		return emitRenderCSS(b, tmp, tup.At(0).Type(), imports)
	}
	return emitRenderCSS(b, expr, t, imports)
}

// emitRenderCSS writes a value in CSS block context (inside <style>): SafeCSS and
// numbers are emitted raw (safe by construction); strings/Stringers go through
// gw.CSS (the value-filter).
func emitRenderCSS(b *bytes.Buffer, expr string, t types.Type, imports map[string]bool) error {
	if isSafeCSS(t) {
		fmt.Fprintf(b, "\t\t_gsxgw.S(string(%s))\n", expr)
		return nil
	}
	switch classify(t) {
	case catInt:
		imports["strconv"] = true
		fmt.Fprintf(b, "\t\t_gsxgw.S(strconv.FormatInt(int64(%s), 10))\n", expr)
	case catUint:
		imports["strconv"] = true
		fmt.Fprintf(b, "\t\t_gsxgw.S(strconv.FormatUint(uint64(%s), 10))\n", expr)
	case catFloat:
		imports["strconv"] = true
		fmt.Fprintf(b, "\t\t_gsxgw.S(strconv.FormatFloat(float64(%s), 'g', -1, 64))\n", expr)
	case catString, catBytes:
		fmt.Fprintf(b, "\t\t_gsxgw.CSS(string(%s))\n", expr)
	case catStringer:
		fmt.Fprintf(b, "\t\t_gsxgw.CSS((%s).String())\n", expr)
	default:
		return fmt.Errorf("codegen: value of type %s not renderable in CSS context (need string/number/Stringer or gsx.SafeCSS)", t)
	}
	return nil
}

// emitCSSAttrValue writes a style="…" attribute value: SafeCSS and numbers are
// attr-escaped only (safe by construction); strings/Stringers go through
// gw.CSSAttr (value-filter + attr-escape).
func emitCSSAttrValue(b *bytes.Buffer, expr string, t types.Type, imports map[string]bool) error {
	if isSafeCSS(t) {
		fmt.Fprintf(b, "\t\t_gsxgw.AttrValue(string(%s))\n", expr)
		return nil
	}
	switch classify(t) {
	case catInt:
		imports["strconv"] = true
		fmt.Fprintf(b, "\t\t_gsxgw.AttrValue(strconv.FormatInt(int64(%s), 10))\n", expr)
	case catUint:
		imports["strconv"] = true
		fmt.Fprintf(b, "\t\t_gsxgw.AttrValue(strconv.FormatUint(uint64(%s), 10))\n", expr)
	case catFloat:
		imports["strconv"] = true
		fmt.Fprintf(b, "\t\t_gsxgw.AttrValue(strconv.FormatFloat(float64(%s), 'g', -1, 64))\n", expr)
	case catString, catBytes:
		fmt.Fprintf(b, "\t\t_gsxgw.CSSAttr(string(%s))\n", expr)
	case catStringer:
		fmt.Fprintf(b, "\t\t_gsxgw.CSSAttr((%s).String())\n", expr)
	default:
		return fmt.Errorf("codegen: style attribute value type %s not supported (need string/number/Stringer or gsx.SafeCSS)", t)
	}
	return nil
}
```

- [ ] **Step 3: Route `<style>` children through `genStyleChild`** in `emit.go`. In `genNode`'s `*ast.Element` case, replace the children loop (currently `for _, c := range t.Children { genNode(...) }`) with:

```go
		emitS(b, ">")
		if strings.EqualFold(t.Tag, "style") {
			for _, c := range t.Children {
				if err := genStyleChild(b, c, resolved, imports, fset); err != nil {
					return err
				}
			}
		} else {
			for _, c := range t.Children {
				if err := genNode(b, c, resolved, table, structFields, imports, fset, recvVar, recvTypeName); err != nil {
					return err
				}
			}
		}
		emitS(b, "</"+t.Tag+">")
```

Also handle a `<style>` that is a component's single ROOT element: in the root-emit loop (the `for _, c := range el.Children { genNode(...) }` that follows `emitS(b, ">")` in the root-fallthrough path, ~line 328), wrap identically:

```go
		emitS(b, ">")
		if strings.EqualFold(el.Tag, "style") {
			for _, c := range el.Children {
				if err := genStyleChild(b, c, resolved, imports, fset); err != nil {
					return err
				}
			}
		} else {
			for _, c := range el.Children {
				if err := genNode(b, c, resolved, table, structFields, imports, fset, recvVar, recvTypeName); err != nil {
					return err
				}
			}
		}
		emitS(b, "</"+el.Tag+">")
```

- [ ] **Step 4: Auto-sanitize the `style=` attribute** in `emitExprAttr`. Remove the `case ctxCSS:` arm from the top fail-closed `switch attrContext(a.Name)` (keep `case ctxJS:`), so the CSS case no longer rejects:

```go
	switch attrContext(a.Name) {
	case ctxJS:
		return fmt.Errorf("codegen: expr value in JS/event-handler context (%q) is unsafe; needs a safe type via `|> js` (not available yet) — use a static value", a.Name)
	}
```

Then, in the value-emit section of `emitExprAttr` (the `if attrContext(a.Name) == ctxURL { … } else { emitAttrValue(…) }` block after `_gsxgw.S(" name=\"")`), make it a three-way:

```go
	switch attrContext(a.Name) {
	case ctxURL:
		fmt.Fprintf(b, "\t\t_gsxgw.URL(%s)\n", urlStringExpr(expr, t))
	case ctxCSS:
		if err := emitCSSAttrValue(b, expr, t, imports); err != nil {
			return err
		}
	default:
		if err := emitAttrValue(b, expr, t, imports); err != nil {
			return err
		}
	}
```

- [ ] **Step 5: Build, and confirm no test asserts the old rejection**

Run: `go build ./... && grep -rn "CSS context\|not available yet) — use a static value" internal/codegen --include=*_test.go`
Expected: build PASS. If the grep finds a unit test asserting the old `style=` CSS rejection, update its expectation (the `style=` ExprAttr case now auto-sanitizes; the composed-`style` `ClassAttr` path and `onclick=` JS path still reject and should be left). The composed-style reject lives at the `*ast.ClassAttr` `attrContext(t.Name) == ctxCSS` branch — do NOT change it in this slice.

- [ ] **Step 6: Write the codegen-shape golden** — create `internal/corpus/testdata/cases/codegen-shape/style_interp.txtar`:

```
-- input.gsx --
package views

component Theme(w int, accent string, raw gsx.SafeCSS) {
	<style>.card{width:${w}px;color:${accent};border:${raw}}</style>
}
-- invoke --
Theme(ThemeProps{W: 8, Accent: "red", Raw: gsx.SafeCSS("1px solid")})
-- render.golden --
<style>.card{width:8px;color:red;border:1px solid}</style>
```

- [ ] **Step 7: Generate the goldens for the new case and inspect them**

Run: `go test ./internal/corpus -run TestCorpus -update`
Then inspect: `sed -n '/generated.x.go.golden/,/^-- /p' internal/corpus/testdata/cases/codegen-shape/style_interp.txtar`
Expected: the generated body contains `_gsxgw.S("<style>.card{width:")`, `_gsxgw.S(strconv.FormatInt(int64(_gsxp.W), 10))`, `_gsxgw.CSS(string(_gsxp.Accent))`, and `_gsxgw.S(string(_gsxp.Raw))`. If `-update` populated `render.golden` differently from the hand-written value above, reconcile (the structural render compare is whitespace-insensitive, so it should match).

- [ ] **Step 8: Run the corpus + full suite**

Run: `go test ./internal/corpus && go test ./...`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/codegen/analyze.go internal/codegen/emit.go internal/corpus/testdata/cases/codegen-shape/style_interp.txtar
git commit -m "codegen: CSS-context auto-sanitize for <style> interps + style= attribute"
```

---

### Task 4: Printer — print `${ }` in `<style>` + wsnorm guard

**Files:**
- Modify: `internal/printer/printer.go` (`element`, add a style-children path)
- Test: `internal/printer/printer_test.go` (append), `internal/wsnorm/wsnorm_test.go` (append a guard)

**Interfaces:**
- Consumes: `<style>` `Text`+`Interp` children (Task 2); existing `(*printer).interp`, `(*printer).ws`, `isPreserveTag`, `checkFormat`.
- Produces: `<style>` interps print as `${ expr }`; faithfulness + idempotence preserved.

- [ ] **Step 1: Write the failing printer test** — append to `internal/printer/printer_test.go`:

```go
func TestStyleInterpFormat(t *testing.T) {
	src := "package p\n\ncomponent C(w int) {\n\t<style>.a{width:${ w }px}</style>\n}\n"
	want := "package p\n\ncomponent C(w int) {\n\t<style>.a{width:${ w }px}</style>\n}\n"
	checkFormat(t, src, want)
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/printer -run TestStyleInterpFormat`
Expected: FAIL — the interp prints as `{ w }` (no `$`), so output ≠ want (and re-parse would treat `{` as raw CSS, breaking idempotence).

- [ ] **Step 3: Add a CSS-interp printing path** in `internal/printer/printer.go`. In `element`, special-case `<style>` so its children print with `${ }`:

```go
	p.ws(">")
	if strings.EqualFold(e.Tag, "style") {
		p.styleChildren(e.Children)
	} else if isPreserveTag(e.Tag) {
		p.children(e.Children, depth, true)
	} else {
		p.children(e.Children, depth, false)
	}
	p.ws("</")
```

Add the helper (near `interp`):

```go
// styleChildren prints a <style> body: Text verbatim, Interp as ${ … }.
func (p *printer) styleChildren(nodes []ast.Markup) {
	for _, n := range nodes {
		switch v := n.(type) {
		case *ast.Text:
			p.ws(v.Value)
		case *ast.Interp:
			p.ws("$")
			p.interp(v)
		default:
			p.err = fmt.Errorf("printer: unexpected %T in <style> body", n)
		}
	}
}
```

`p.interp` emits `{ expr }`, so prefixing `$` yields `${ expr }`. Ensure `"strings"` is imported in `printer.go` (it is — `isPreserveTag` uses `strings.ToLower`).

- [ ] **Step 4: Run the printer test, then the printer property tests**

Run: `go test ./internal/printer`
Expected: PASS — including the corpus-wide faithfulness/idempotence property tests.

- [ ] **Step 5: Add a wsnorm guard test** — append to `internal/wsnorm/wsnorm_test.go`:

```go
func TestStyleInterpPreserved(t *testing.T) {
	// A <style> body with an interp must pass through wsnorm untouched: Text
	// verbatim, Interp intact.
	src := "package p\n\ncomponent C(w int) {\n\t<style>.a {\n\t\twidth: ${ w }px;\n\t}</style>\n}\n"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "t.gsx", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	Normalize(f)
	style := f.Decls[0].(*ast.Component).Body[0].(*ast.Element)
	var sawInterp bool
	for _, c := range style.Children {
		switch v := c.(type) {
		case *ast.Text:
			if strings.TrimSpace(v.Value) == "" && v.Value != "" {
				// fine: whitespace kept verbatim in preserve context
			}
		case *ast.Interp:
			sawInterp = true
			if v.Expr != "w" {
				t.Fatalf("interp Expr = %q, want w", v.Expr)
			}
		}
	}
	if !sawInterp {
		t.Fatal("interp was lost from <style> body after Normalize")
	}
	// The leading whitespace inside <style> must be preserved (not collapsed).
	if txt, ok := style.Children[0].(*ast.Text); !ok || txt.Value != ".a {\n\t\twidth: " {
		t.Fatalf("child0 = %#v, want verbatim leading CSS text", style.Children[0])
	}
}
```

(Ensure `wsnorm_test.go` imports `"go/token"`, `"strings"`, `"github.com/gsxhq/gsx/ast"`, and `"github.com/gsxhq/gsx/parser"` — add any missing.)

- [ ] **Step 6: Run the wsnorm test, then the full suite**

Run: `go test ./internal/wsnorm && go test ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/printer/printer.go internal/printer/printer_test.go internal/wsnorm/wsnorm_test.go
git commit -m "printer: print \${ } in <style> bodies; wsnorm preserve guard"
```

---

### Task 5: Integration — end-to-end corpus + example

**Files:**
- Create: `internal/corpus/testdata/cases/style/interpolation.txtar`
- Create: `internal/corpus/testdata/cases/style/attribute.txtar`
- Modify: `examples/02_text_escaping.gsx` (add a tiny `<style>` demonstration component)

**Interfaces:**
- Consumes: all prior tasks (parser, runtime, codegen, printer).
- Produces: golden-locked end-to-end render of `<style>` interpolation and `style=` auto-sanitize, including the breakout-neutralization behavior.

- [ ] **Step 1: Write the `<style>` interpolation corpus case** — create `internal/corpus/testdata/cases/style/interpolation.txtar`:

```
-- input.gsx --
package views

component Card(w int, userColor string) {
	<style>
		.card {
			width: ${ w }px;
			color: ${ userColor };
		}
	</style>
}
-- invoke --
Card(CardProps{W: 12, UserColor: "red; } body { display:none"})
-- render.golden --
<style>
		.card {
			width: 12px;
			color: ZgotmplZ;
		}
	</style>
```

(The untrusted `userColor` carries a breakout; `gw.CSS` neutralizes it to `ZgotmplZ`, proving safety. Whitespace inside `<style>` is preserved verbatim — minification is slice 2.)

- [ ] **Step 2: Write the `style=` attribute corpus case** — create `internal/corpus/testdata/cases/style/attribute.txtar`:

```
-- input.gsx --
package views

component Badge(userColor string) {
	<span style={ "color: " + userColor }>hi</span>
}
-- invoke --
Badge(BadgeProps{UserColor: "blue"})
-- render.golden --
<span style="color: blue">hi</span>
```

- [ ] **Step 3: Generate goldens + run the corpus**

Run: `go test ./internal/corpus -run TestCorpus -update && go test ./internal/corpus`
Expected: PASS. Inspect both `.txtar` files to confirm `generated.x.go.golden` shows `_gsxgw.CSS(string(_gsxp.UserColor))` (block) and `_gsxgw.CSSAttr(string("color: " + _gsxp.UserColor))` (attribute).

- [ ] **Step 4: Add a `<style>` demo to `examples/02_text_escaping.gsx`** — append before the final line:

```gsx
// <style> interpolation: dynamic values are CSS-value-filtered automatically;
// gsx.SafeCSS opts out for author-controlled CSS.
component ThemedCard(width int, accent string) {
	<style>
		.themed {
			width: ${ width }px;
			color: ${ accent };
		}
	</style>
}
```

- [ ] **Step 5: Confirm examples still parse + the coverage golden**

Run: `go test ./internal/corpus -run Example -update && go test ./internal/corpus`
Expected: PASS — `02_text_escaping.gsx: ok` unchanged.

- [ ] **Step 6: Full suite + vet**

Run: `go build ./... && go test ./... && go vet ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/corpus/testdata/cases/style/ examples/02_text_escaping.gsx
git commit -m "corpus+examples: end-to-end <style>/style= safe interpolation"
```

---

## Self-Review

**Spec coverage (Components 1–4 of the design):**
- Component 1 (parser `${ }`) → Task 2. ✓
- Component 2 (`SafeCSS` + `cssValueFilter` + `gw.CSS`/`CSSAttr`) → Task 1. ✓
- Component 3 (codegen CSS-context dispatch; `isSafeCSS`; `style=` auto-sanitize; `ctxJS` unchanged) → Task 3. ✓
- Component 4 (printer `${ }`; wsnorm preserve guard) → Task 4. ✓
- End-to-end + examples + breakout-neutralization proof → Task 5. ✓
- Component 5 (CSS/JS minification) → out of this slice, by design. ✓

**Deferred-with-clear-errors (verified present):** pipeline stages + `?` in `<style>` interps (Task 3 Step 2 `emitCSSInterp`); composed-`style` `ClassAttr` untouched (Task 3 Step 5); `<script>` stays raw (Task 2 Step 1 `TestScriptStaysRaw`).

**Type/name consistency:** `cssValueFilter(string) string`, `cssFailsafe = "ZgotmplZ"`, `SafeCSS`, `(*Writer).CSS`, `(*Writer).CSSAttr`, `isSafeCSS`, `genStyleChild`, `emitCSSInterp`, `emitRenderCSS`, `emitCSSAttrValue`, `styleChildren` — each defined once and referenced with the same signature throughout.

**Placeholder scan:** none — every code step shows complete code; the only "port from source" is `cssValueFilter`, which is transcribed in full in Task 1 Step 3 with its exact test vectors in Step 1.
