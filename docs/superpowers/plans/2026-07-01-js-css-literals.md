# Explicit JS/CSS Literals Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add explicit `js`...`` and `css`...`` attribute literals with compact `@{expr}` interpolation, remove JS/CSS attr classification config, and keep URL attrs as the only attr-context knob.

**Architecture:** Introduce an embedded-language attribute node that carries `Name`, language kind (`js` or `css`), and raw text/interpolation segments. Parse it both directly after `=` and inside `{}`; reuse the existing JSX classifier for JS holes and the existing CSS interpolation emitter for CSS holes. Remove quoted JS-attribute interpolation and user-facing JS/CSS attr classification so framework directives are safe through explicit value-site syntax, not config.

**Tech Stack:** Go parser/codegen, `internal/jsx` JS context classifier, corpus txtar golden tests, `gsx fmt`/printer docs, Go module `gen` options/config.

---

## File Structure

- `ast/ast.go`: replace or generalize `JSAttr` with an embedded attr node, preserving `attrNode`.
- `parser/attrs.go`: parse direct `name=js`...`` / `name=css`...`` and braced `name={js`...`}` / `name={css`...`}`; stop splitting quoted JS attrs.
- `parser/markup_test.go`, `parser/attrclass_test.go`: parser-level coverage for embedded literals and removal of custom JS attr splitting.
- `internal/jsx/jsx.go`: classify `LangJS` embedded attr holes instead of old quoted `JSAttr` nodes.
- `internal/codegen/analyze.go`: probe and collect embedded attr interpolation expressions for type resolution and liveness.
- `internal/codegen/emit.go`: emit JS/CSS embedded attrs through existing JS/CSS attr/block escapers; remove JS/CSS context behavior from plain expr attrs.
- `internal/codegen/*attrclass*_test.go`: remove or rewrite custom JS/CSS classifier tests.
- `internal/corpus/testdata/cases/jsattr/`, `internal/corpus/testdata/cases/style/`, `internal/corpus/testdata/cases/parser/`: add/update canonical syntax cases.
- `gen/options.go`, `gen/main.go`, `gen/configfile.go`, `gen/cachekey.go`, `gen/manifest.go`, `gen/info.go`, `gen/*test.go`: remove `WithJSAttrs`, `WithCSSAttrs`, general JS/CSS classifier config, TOML `jsAttrs`/`cssAttrs`, and manifest/cache references; keep `WithURLAttrs`.
- `docs/guide/config.md`, `docs/guide/extensions.md`, `docs/guide/syntax/javascript.md`, `docs/guide/syntax/escaping.md`, `docs/guide/syntax/attributes.md`, `docs/guide/editor.md`, `docs/ROADMAP.md`: update user-facing docs.
- `../tree-sitter-gsx`, `../vscode-gsx`: follow-up sibling updates for new syntax highlighting/injection.

## Task 1: Parser and AST for Embedded Attribute Literals

**Files:**
- Modify: `ast/ast.go`
- Modify: `parser/attrs.go`
- Modify: `parser/markup_test.go`
- Modify: `parser/attrclass_test.go`

- [ ] **Step 1: Add failing parser tests for direct and braced embedded attrs**

Add tests to `parser/markup_test.go`:

```go
func TestEmbeddedJSAttrDirect(t *testing.T) {
	attrs := parseSingleElemAttrs(t, "<button @click=js`save(@{id})`></button>")
	if len(attrs) != 1 {
		t.Fatalf("got %d attrs, want 1: %#v", len(attrs), attrs)
	}
	a, ok := attrs[0].(*ast.EmbeddedAttr)
	if !ok {
		t.Fatalf("attr0 = %T, want *ast.EmbeddedAttr", attrs[0])
	}
	if a.Name != "@click" || a.Lang != ast.EmbeddedJS {
		t.Fatalf("attr = %#v, want @click JS", a)
	}
	if len(a.Segments) != 3 {
		t.Fatalf("segments = %d, want 3: %#v", len(a.Segments), a.Segments)
	}
	if txt, ok := a.Segments[0].(*ast.Text); !ok || txt.Value != "save(" {
		t.Fatalf("seg0 = %#v, want Text(save()", a.Segments[0])
	}
	if in, ok := a.Segments[1].(*ast.Interp); !ok || in.Expr != "id" {
		t.Fatalf("seg1 = %#v, want Interp{id}", a.Segments[1])
	}
	if txt, ok := a.Segments[2].(*ast.Text); !ok || txt.Value != ")" {
		t.Fatalf("seg2 = %#v, want Text())", a.Segments[2])
	}
}

func TestEmbeddedCSSAttrDirect(t *testing.T) {
	attrs := parseSingleElemAttrs(t, "<div style=css`width:@{pct}%`></div>")
	a, ok := attrs[0].(*ast.EmbeddedAttr)
	if !ok {
		t.Fatalf("attr0 = %T, want *ast.EmbeddedAttr", attrs[0])
	}
	if a.Name != "style" || a.Lang != ast.EmbeddedCSS {
		t.Fatalf("attr = %#v, want style CSS", a)
	}
}

func TestEmbeddedJSAttrBraced(t *testing.T) {
	attrs := parseSingleElemAttrs(t, "<button @click={js`save(@{id})`}></button>")
	a, ok := attrs[0].(*ast.EmbeddedAttr)
	if !ok {
		t.Fatalf("attr0 = %T, want *ast.EmbeddedAttr", attrs[0])
	}
	if a.Name != "@click" || a.Lang != ast.EmbeddedJS {
		t.Fatalf("attr = %#v, want @click JS", a)
	}
}

func TestQuotedJSAttrHoleStaysStatic(t *testing.T) {
	attrs := parseSingleElemAttrs(t, `<div x-data="{ id: @{id} }"></div>`)
	a, ok := attrs[0].(*ast.StaticAttr)
	if !ok || a.Name != "x-data" || a.Value != "{ id: @{id} }" {
		t.Fatalf("attr0 = %#v, want StaticAttr with literal @{id}", attrs[0])
	}
}
```

- [ ] **Step 2: Run parser tests and verify they fail**

Run:

```bash
go test ./parser -run 'TestEmbedded|TestQuotedJSAttrHoleStaysStatic' -count=1
```

Expected: compile failure for `ast.EmbeddedAttr` / `ast.EmbeddedJS` / `ast.EmbeddedCSS` not existing, or parser assertions failing.

- [ ] **Step 3: Add AST node**

In `ast/ast.go`, replace the old `JSAttr` declaration with:

```go
type EmbeddedLang uint8

const (
	EmbeddedJS EmbeddedLang = iota + 1
	EmbeddedCSS
)

// EmbeddedAttr is an explicit embedded-language attribute value:
// name=js`... @{expr} ...`, name={js`...`}, name=css`...`, or name={css`...`}.
// Segments contain *Text and *Interp only. JS interps receive JSCtx during
// internal/jsx resolution; CSS interps use the CSS emitter directly.
type EmbeddedAttr struct {
	span
	Name     string
	Lang     EmbeddedLang
	Segments []Markup
}

func (*EmbeddedAttr) attrNode() {}
```

Remove `type JSAttr` and its `attrNode` method. Update comments that referred to quoted JS attrs.

- [ ] **Step 4: Implement embedded literal parsing**

In `parser/attrs.go`:

1. Remove the `attrclass` import if it becomes unused.
2. In `parseSingleAttr`, before the existing `"`/`{` dispatch, detect direct `js`/`css` literals:

```go
case p.at("js`") || p.at("css`"):
	return p.parseEmbeddedAttrValue(name, attrStartPos)
```

3. In the `{` branch, before `class`/`style` composed handling, detect braced embedded literals:

```go
if p.i+3 < len(p.src) && p.src[p.i] == '{' && (strings.HasPrefix(p.src[p.i+1:], "js`") || strings.HasPrefix(p.src[p.i+1:], "css`")) {
	return p.parseBracedEmbeddedAttrValue(name, attrStartPos)
}
```

4. Stop classifying quoted strings by attr name. Replace:

```go
if p.classifier.Context(name) == attrclass.CtxJS {
	return p.parseJSAttrValue(name, attrStartPos, quotePos)
}
```

with normal static string parsing.

5. Add these helpers. `parseEmbeddedAttrLiteral` returns the concrete `*ast.EmbeddedAttr`, so braced parsing can set the final span after consuming the closing brace:

```go
func (p *parser) parseBracedEmbeddedAttrValue(name string, attrStartPos token.Pos) (ast.Attr, error) {
	open := p.i
	p.i++ // past {
	n, err := p.parseEmbeddedAttrLiteral(name, attrStartPos)
	if err != nil {
		return nil, err
	}
	p.skipSpace()
	if p.eof() || p.peek() != '}' {
		return nil, p.errorf(p.posAt(open), "unterminated embedded attribute value for %q; expected closing `}`", name)
	}
	p.i++ // past }
	ast.SetSpan(n, attrStartPos, p.posAt(p.i))
	return n, nil
}

func (p *parser) parseEmbeddedAttrValue(name string, attrStartPos token.Pos) (ast.Attr, error) {
	n, err := p.parseEmbeddedAttrLiteral(name, attrStartPos)
	if err != nil {
		return nil, err
	}
	ast.SetSpan(n, attrStartPos, p.posAt(p.i))
	return n, nil
}

func (p *parser) parseEmbeddedAttrLiteral(name string, attrStartPos token.Pos) (*ast.EmbeddedAttr, error) {
	var lang ast.EmbeddedLang
	switch {
	case p.at("js`"):
		lang = ast.EmbeddedJS
		p.i += len("js`")
	case p.at("css`"):
		lang = ast.EmbeddedCSS
		p.i += len("css`")
	default:
		return nil, p.errorf(p.pos(), "expected embedded attribute literal js`...` or css`...` for %q", name)
	}
	segments, err := p.parseEmbeddedSegments(name)
	if err != nil {
		return nil, err
	}
	n := &ast.EmbeddedAttr{Name: name, Lang: lang, Segments: segments}
	ast.SetSpan(n, attrStartPos, p.posAt(p.i))
	return n, nil
}
```

6. Implement `parseEmbeddedSegments` by adapting `parseJSAttrValue`'s scan loop:

```go
func (p *parser) parseEmbeddedSegments(name string) ([]ast.Markup, error) {
	var segments []ast.Markup
	segStart := p.i
	segStartPos := p.posAt(p.i)
	flush := func(end int) {
		if end > segStart {
			txt := &ast.Text{Value: p.src[segStart:end]}
			ast.SetSpan(txt, segStartPos, p.posAt(end))
			segments = append(segments, txt)
		}
	}
	for !p.eof() {
		if p.src[p.i] == '\\' && p.i+1 < len(p.src) && p.src[p.i+1] == '`' {
			p.i += 2
			continue
		}
		if p.src[p.i] == '`' {
			flush(p.i)
			p.i++
			return segments, nil
		}
		if p.src[p.i] == '@' && p.i+1 < len(p.src) && p.src[p.i+1] == '{' {
			flush(p.i)
			p.i++
			in, err := p.parseInterp()
			if err != nil {
				return nil, err
			}
			segments = append(segments, in)
			segStart = p.i
			segStartPos = p.posAt(p.i)
			continue
		}
		p.i++
	}
	return nil, p.errorf(p.pos(), "unterminated embedded attribute literal for %q", name)
}
```

- [ ] **Step 5: Update/remove parser attrclass tests**

In `parser/attrclass_test.go`, replace `TestCustomJSRuleSplitsHoles` with a test showing custom JS rules no longer affect quoted attrs:

```go
func TestCustomJSRuleDoesNotSplitQuotedAttr(t *testing.T) {
	cls := attrclass.New(attrclass.Rules{JS: []attrclass.Rule{{Prefix: "wire:"}}}, nil)
	got := parseAttrType(t, `<div wire:click="@{action}"></div>`, cls)
	if a, ok := got.(*ast.StaticAttr); !ok || a.Value != "@{action}" {
		t.Fatalf("custom JS rule should not split quoted attr, got %#v", got)
	}
}
```

Keep URL/CSS parser tests only if they still exercise parser behavior after Task 4. Delete tests that assert quoted JS attrs parse as `*ast.JSAttr`.

- [ ] **Step 6: Run parser tests**

Run:

```bash
go test ./parser -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit parser/AST slice**

```bash
git add ast/ast.go parser/attrs.go parser/markup_test.go parser/attrclass_test.go
git commit -m "feat(parser): parse explicit JS CSS attr literals"
```

## Task 2: Analyze and Codegen Embedded JS/CSS Attributes

**Files:**
- Modify: `internal/jsx/jsx.go`
- Modify: `internal/jsx/jsx_test.go`
- Modify: `internal/codegen/analyze.go`
- Modify: `internal/codegen/emit.go`
- Modify: `internal/codegen/attrclass_wire_test.go`
- Modify: `internal/codegen/attrclass_css_wire_test.go`
- Test: `internal/corpus/testdata/cases/jsattr/*.txtar`
- Test: `internal/corpus/testdata/cases/style/*.txtar`

- [ ] **Step 1: Add corpus cases for explicit JS literals**

Create or replace cases under `internal/corpus/testdata/cases/jsattr/`:

`explicit_attr_literal.txtar`:

```txtar
# explicit JS attr literal direct RHS
-- input.gsx --
package p
component Button(id string) {
	<button @click=js`save(@{id})`>Save</button>
}
-- input.go --
package p
import "github.com/gsxhq/gsx"
func Render() gsx.Node { return Button(ButtonProps{ID: "abc"}) }
-- render.golden --
<button @click="save(&#34;abc&#34;)">Save</button>
```

`explicit_attr_braced.txtar`:

```txtar
# explicit JS attr literal inside braced dynamic value
-- input.gsx --
package p
component Button(id string) {
	<button @click={js`save(@{id})`}>Save</button>
}
-- input.go --
package p
import "github.com/gsxhq/gsx"
func Render() gsx.Node { return Button(ButtonProps{ID: "abc"}) }
-- render.golden --
<button @click="save(&#34;abc&#34;)">Save</button>
```

`quoted_attr_literal_no_interp.txtar`:

```txtar
# quoted JS-looking attrs are literal strings; @{...} is not special
-- input.gsx --
package p
component Panel() {
	<div x-data="{ id: @{id} }">x</div>
}
-- input.go --
package p
import "github.com/gsxhq/gsx"
func Render() gsx.Node { return Panel(PanelProps{}) }
-- render.golden --
<div x-data="{ id: @{id} }">x</div>
```

- [ ] **Step 2: Add corpus cases for explicit CSS literals**

Create `internal/corpus/testdata/cases/style/explicit_css_attr_literal.txtar`:

```txtar
# explicit CSS attr literal filters interpolated values
-- input.gsx --
package p
component Box(w int, color string) {
	<div data-style=css`width:@{w}px;color:@{color}`>x</div>
}
-- input.go --
package p
import "github.com/gsxhq/gsx"
func Render() gsx.Node { return Box(BoxProps{W: 12, Color: "teal"}) }
-- render.golden --
<div data-style="width:12px;color:teal">x</div>
```

Create `internal/corpus/testdata/cases/style/explicit_css_attr_hostile.txtar`:

```txtar
# explicit CSS attr literal rejects unsafe interpolated CSS
-- input.gsx --
package p
component Box(color string) {
	<div style=css`color:@{color}`>x</div>
}
-- input.go --
package p
import "github.com/gsxhq/gsx"
func Render() gsx.Node { return Box(BoxProps{Color: "red;background:url(javascript:alert(1))"}) }
-- render.golden --
<div style="color:ZgotmplZ">x</div>
```

- [ ] **Step 3: Run corpus tests and verify they fail**

Run:

```bash
go test ./internal/corpus -run TestCorpus -count=1
```

Expected: FAIL because generated goldens are missing and codegen does not emit `EmbeddedAttr` yet.

- [ ] **Step 4: Resolve embedded JS attr contexts**

In `internal/jsx/jsx.go`:

1. Change the attr walk in `resolveMarkup`:

```go
if ea, ok2 := a.(*ast.EmbeddedAttr); ok2 && ea.Lang == ast.EmbeddedJS {
	if !resolveJSAttr(ea.Name, ea.Segments, bag) {
		ok = false
	}
}
```

2. Keep `ResolveJSAttr` test helper, but update comments from "JS-context attribute" to "explicit JS attribute literal".

3. Keep direct `ResolveJSAttr` unit tests because that helper still classifies segment slices, and add one integration-style test that builds `ast.EmbeddedAttr{Name: "@click", Lang: ast.EmbeddedJS, Segments: ...}` and verifies `resolveMarkup` fills each interpolation `JSCtx`.

- [ ] **Step 5: Probe and collect embedded attr interpolations**

In `internal/codegen/analyze.go`, find all `walkMarkupAttrs` / `attrsRefAttrs` logic that handles `*ast.JSAttr` and change it to `*ast.EmbeddedAttr`.

For expression probes and collection, embedded JS and CSS both need `@{}` holes type-resolved:

```go
case *gsxast.EmbeddedAttr:
	walkMarkupAttrs([]gsxast.Attr{at}, func(value []gsxast.Markup) {
		// existing emitProbes / collectExprs path handles value
	})
```

Find the direct type switch in `walkMarkupAttrs` and update it to pass `at.Segments` for `*EmbeddedAttr`.

In `attrsRefAttrs`, update:

```go
case *ast.EmbeddedAttr:
	for _, seg := range at.Segments {
		if in, ok := seg.(*ast.Interp); ok && refsAttrs(in.Expr) {
			return true
		}
	}
```

- [ ] **Step 6: Emit embedded attr literals**

In `internal/codegen/emit.go`:

1. In `emitAttr`, replace `case *ast.JSAttr` with:

```go
case *ast.EmbeddedAttr:
	switch t.Lang {
	case ast.EmbeddedJS:
		return emitEmbeddedJSAttr(b, t, resolved, table, imports, interpTemp, bag)
	case ast.EmbeddedCSS:
		return emitEmbeddedCSSAttr(b, t, resolved, table, imports, interpTemp, bag)
	default:
		bag.Errorf(a.Pos(), a.End(), "unsupported-attr", "unknown embedded attribute language")
		return false
	}
```

2. Rename `emitJSAttr` to `emitEmbeddedJSAttr` and accept `*ast.EmbeddedAttr`. Keep its internals and call `emitJSAttrInterp`.

3. Add CSS attr emitter. CSS interpolation must use a dedicated attribute-safe path: filter the value with the same CSS value filter used by style contexts, then HTML-attribute escape the filtered bytes before writing them into the attribute. Do not write raw CSS-filtered bytes directly into an HTML attribute.

```go
func emitEmbeddedCSSAttr(b *bytes.Buffer, a *ast.EmbeddedAttr, resolved map[ast.Node]types.Type, table filterTable, imports map[string]bool, interpTemp *int, bag *diag.Bag) bool {
	fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(" "+a.Name+`="`))
	for _, seg := range a.Segments {
		switch s := seg.(type) {
		case *ast.Text:
			fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(htmlAttrEscape(s.Value)))
		case *ast.Interp:
			if !emitCSSAttrInterp(b, s, resolved, table, imports, interpTemp, bag) {
				return false
			}
		default:
			bag.Errorf(seg.Pos(), seg.End(), "unsupported-attr", "CSS attribute %q value may contain only text and @{} interpolations, got %T", a.Name, seg)
			return false
		}
	}
	fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(`"`))
	return true
}
```

4. Add a new helper named `emitCSSAttrInterp`. It should mirror `emitCSSInterp`'s tuple and pipeline handling, then delegate to a new `emitRenderCSSAttr` helper.

```go
func emitCSSAttrInterp(b *bytes.Buffer, n *ast.Interp, resolved map[ast.Node]types.Type, table filterTable, imports map[string]bool, interpTemp *int, bag *diag.Bag) bool {
	expr := strings.TrimSpace(n.Expr)
	if len(n.Stages) > 0 {
		lowered, usedPkgs, err := lowerPipe(n.Expr, n.Stages, table)
		if err != nil {
			bag.Errorf(n.Pos(), n.End(), "unresolved-pipeline", "%s", strings.TrimPrefix(err.Error(), "codegen: "))
			return false
		}
		for _, p := range usedPkgs {
			imports[p] = true
		}
		expr = lowered
	}
	t, ok := resolved[n]
	if !ok || t == nil {
		bag.Errorf(n.Pos(), n.End(), "unresolved-interp", "could not resolve type of CSS attribute interpolation %q", n.Expr)
		return false
	}
	if _, isTuple := t.(*types.Tuple); isTuple {
		elemT, ok := tupleUnwrapType(t)
		if !ok {
			bag.Errorf(n.Pos(), n.End(), "invalid-tuple", "CSS attribute interpolation %q returns %s; only (T, error) is supported", expr, t)
			return false
		}
		tmp := hoistTuple(b, expr, interpTemp)
		return emitRenderCSSAttr(b, tmp, elemT, imports, n, bag)
	}
	return emitRenderCSSAttr(b, expr, t, imports, n, bag)
}
```

5. Implement `emitRenderCSSAttr` by composing existing public runtime methods: use `gsx.StyleValue` to apply the CSS value filter, then `_gsxgw.AttrValue(...)` to HTML-attribute escape the filtered result. RawCSS and numeric values should still pass through `StyleValue` for consistency; `StyleValue` already preserves `gsx.RawCSS` and safe numeric strings.

```go
func emitRenderCSSAttr(b *bytes.Buffer, expr string, t types.Type, imports map[string]bool, n ast.Node, bag *diag.Bag) bool {
	imports["github.com/gsxhq/gsx"] = true
	switch classify(t) {
	case catInt:
		imports["strconv"] = true
		fmt.Fprintf(b, "\t\t_gsxgw.AttrValue(gsx.StyleValue(strconv.FormatInt(int64(%s), 10)))\n", expr)
	case catUint:
		imports["strconv"] = true
		fmt.Fprintf(b, "\t\t_gsxgw.AttrValue(gsx.StyleValue(strconv.FormatUint(uint64(%s), 10)))\n", expr)
	case catFloat:
		imports["strconv"] = true
		fmt.Fprintf(b, "\t\t_gsxgw.AttrValue(gsx.StyleValue(strconv.FormatFloat(float64(%s), 'g', -1, 64)))\n", expr)
	case catString, catBytes, catStringer:
		if isRawCSS(t) {
			fmt.Fprintf(b, "\t\t_gsxgw.AttrValue(gsx.StyleValue(%s))\n", expr)
			return true
		}
		if classify(t) == catStringer {
			fmt.Fprintf(b, "\t\t_gsxgw.AttrValue(gsx.StyleValue((%s).String()))\n", expr)
		} else {
			fmt.Fprintf(b, "\t\t_gsxgw.AttrValue(gsx.StyleValue(string(%s)))\n", expr)
		}
	default:
		bag.Errorf(n.Pos(), n.End(), "unrenderable-css", "value of type %s not renderable in CSS attribute context (need string/number/Stringer or gsx.RawCSS)", t)
		return false
	}
	return true
}
```

- [ ] **Step 7: Remove JS/CSS behavior from plain expr attrs**

In `emitExprAttr`, keep URL handling, but remove JS and CSS attr context branches:

- `x-data={state}` should use ordinary attr rendering, not JS JSON encoding.
- `data-style={userStyle}` should use ordinary attr rendering. `data-style=css`...`` should use CSS value filtering.
- `href={url}` should still use URL sanitization.

The bool attr rule should no longer special-case `CtxJS`; bools in plain expr attrs follow normal bool attr semantics. JS booleans must be written inside `js`...`` as `@{open}`.

- [ ] **Step 8: Update/remove custom JS/CSS classifier codegen tests**

Delete or rewrite:

- `internal/codegen/attrclass_wire_test.go`
- `internal/codegen/attrclass_css_wire_test.go`

Replacement coverage should assert:

```go
func TestWireClickNeedsExplicitJSLiteral(t *testing.T) {
	// wire:click={expr} emits plain attr value; wire:click=js`...` emits JS literal.
}
```

and:

```go
func TestDataStyleNeedsExplicitCSSLiteral(t *testing.T) {
	// data-style=css`color:@{userStyle}` filters CSS.
}
```

- [ ] **Step 9: Regenerate corpus goldens**

Run:

```bash
go test ./internal/corpus -run TestCorpus -update
```

Expected: new `generated.x.go.golden`, `render.golden`, and `coverage.golden` changes for embedded literal cases.

- [ ] **Step 10: Verify codegen/parser suites**

Run:

```bash
go test ./parser ./internal/jsx ./internal/codegen ./internal/corpus -count=1
```

Expected: PASS.

- [ ] **Step 11: Commit codegen slice**

```bash
git add ast parser internal/jsx internal/codegen internal/corpus/testdata/cases
git commit -m "feat: emit explicit JS CSS attr literals"
```

## Task 3: Remove JS/CSS Attr Config and Public Options

**Files:**
- Modify: `gen/options.go`
- Modify: `gen/main.go`
- Modify: `gen/configfile.go`
- Modify: `gen/cachekey.go`
- Modify: `gen/manifest.go`
- Modify: `gen/info.go`
- Modify: `gen/*test.go`
- Modify: `internal/codegen/module.go` if `Options.Classifier` can be narrowed or removed

- [ ] **Step 1: Add failing config/options tests for removal**

Update `gen/configfile_test.go`:

```go
func TestLoadConfigRejectsJSAttrs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gsx.toml")
	mkfile(t, path, "[[jsAttrs]]\nname = \"wire:click\"\n")
	_, err := loadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "unknown key") || !strings.Contains(err.Error(), "jsAttrs") {
		t.Fatalf("loadConfig err = %v, want unknown jsAttrs", err)
	}
}

func TestLoadConfigRejectsCSSAttrs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gsx.toml")
	mkfile(t, path, "[[cssAttrs]]\nname = \"data-style\"\n")
	_, err := loadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "unknown key") || !strings.Contains(err.Error(), "cssAttrs") {
		t.Fatalf("loadConfig err = %v, want unknown cssAttrs", err)
	}
}
```

Update `gen/options_test.go` so only `WithURLAttrs` remains:

```go
func TestWithURLAttrsOnly(t *testing.T) {
	var cfg config
	WithURLAttrs(attrclass.Rule{Name: "data-href"})( &cfg )
	cls := cfg.classifier()
	if cls.Context("data-href") != attrclass.CtxURL {
		t.Fatal("data-href should be URL")
	}
	if cls.Context("wire:click") == attrclass.CtxJS {
		t.Fatal("wire:click must not be JS-configurable")
	}
}
```

Fix spacing in the code above during implementation: `)( &cfg )` should be `)(&cfg)`.

- [ ] **Step 2: Run gen tests and verify they fail**

Run:

```bash
go test ./gen -run 'TestLoadConfigRejectsJSAttrs|TestLoadConfigRejectsCSSAttrs|TestWithURLAttrsOnly' -count=1
```

Expected: tests fail until config schema/options are removed.

- [ ] **Step 3: Remove JS/CSS rule fields from config**

In `gen/main.go`:

- remove `jsRules []attrclass.Rule`
- remove `cssRules []attrclass.Rule`
- remove `attrPred` and `predLabel`; do not add a URL predicate API in this slice
- update `classifier()` to build:

```go
func (cfg *config) classifier() *attrclass.Classifier {
	return attrclass.New(attrclass.Rules{URL: cfg.urlRules}, nil)
}
```

In `gen/configfile.go`:

- remove `JSAttrs` and `CSSAttrs` from `tomlConfig`
- remove `appendTomlRules` calls for `jsAttrs` and `cssAttrs`
- update comments to say only `urlAttrs` remains.

In `mergeConfig`, concatenate only URL rules.

- [ ] **Step 4: Remove public JS/CSS options**

In `gen/options.go`:

- delete `WithJSAttrs`
- delete `WithCSSAttrs`
- delete `WithAttrClassifier`; keep only declarative URL rules through `WithURLAttrs`
- keep `WithURLAttrs`.

Remove public aliases for `Context`, `CtxJS`, and `CtxCSS` when they are only used by deleted APIs. Keep internal `attrclass.Context` and `CtxURL` for generator internals.

- [ ] **Step 5: Remove cache/manifest/info JS/CSS classifier reporting**

Search:

```bash
rg -n 'jsRules|cssRules|predLabel|attrPred|WithJSAttrs|WithCSSAttrs|WithAttrClassifier|jsAttrs|cssAttrs|Classifier' gen internal/codegen
```

For each hit:

- remove JS/CSS rule folding from cache keys;
- keep URL rule folding because URL attrs affect generated output;
- remove JS/CSS classifier fields from manifests and `gsx info --json`;
- update tests to expect URL-only attr context metadata, or no classifier metadata if URL attrs already fold directly into the cache key.

- [ ] **Step 6: Run gen tests**

Run:

```bash
go test ./gen -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit config/API removal**

```bash
git add gen internal/codegen
git commit -m "refactor(gen): remove JS CSS attr classification config"
```

## Task 4: Docs, Examples, and Roadmap

**Files:**
- Modify: `docs/guide/syntax/javascript.md`
- Modify: `docs/guide/syntax/escaping.md`
- Modify: `docs/guide/syntax/attributes.md`
- Modify: `docs/guide/config.md`
- Modify: `docs/guide/extensions.md`
- Modify: `docs/guide/editor.md`
- Modify: `docs/ROADMAP.md`
- Modify/Create: docs generated snippets under `docs/guide/syntax/_generated/` if the docs generator owns them
- Modify: `docs/examples.json` if examples generation changes it

- [ ] **Step 1: Update JavaScript syntax docs**

In `docs/guide/syntax/javascript.md`, replace JS attr sections with:

```md
## Attribute-local JavaScript

Use `js`...`` when an attribute value is JavaScript source:

```gsx
<button @click=js`open = !open`>Toggle</button>
<div x-data=js`{ open: false, initial: @{initial} }`>...</div>
```

Inside `js`...``, `@{expr}` inserts a Go value and escapes it for its JavaScript position. Quoted attributes remain literal strings:

```gsx
<div x-data="{ open: false }">...</div>
```
```

Use four-backtick outer Markdown fences around examples containing `js`...`` or `css`...`` so the docs render correctly.

- [ ] **Step 2: Update escaping docs**

In `docs/guide/syntax/escaping.md`, update the table:

- remove JS-context attribute name enumeration;
- add `js`...`` row for attribute-local JavaScript;
- add `css`...`` row for attribute-local CSS;
- keep URL attrs by name;
- keep `<script>` and `<style>` rows.

State explicitly: `attr={expr}` is ordinary attr escaping unless the attr is URL-context by name.

- [ ] **Step 3: Update config/extensions docs**

In `docs/guide/config.md`:

- remove `[[jsAttrs]]` and `[[cssAttrs]]`;
- keep `[[urlAttrs]]`;
- remove references to `gen.WithJSAttrs`, `gen.WithCSSAttrs`, `gen.WithAttrClassifier`;
- update examples to use `js`...`` / `css`...``.

In `docs/guide/extensions.md`:

- remove the custom attr classifier section;
- keep filters, formatters, minifiers, and URL attr extension;
- update example plugin snippets from `gen.WithJSAttrs(...)` to either no attr config or `gen.WithURLAttrs(...)`.

- [ ] **Step 4: Update editor docs and roadmap**

In `docs/guide/editor.md`, update embedded-language injection text to mention:

- `<script>` and `js`...`` use JavaScript injection;
- `<style>` and `css`...`` use CSS injection;
- `@{expr}` holes are Go.

In `docs/ROADMAP.md`, replace the completed custom JS/URL/CSS classification text with:

```md
explicit JS/CSS attr literals (`js`...`` / `css`...``) + URL attr classification
```

Use escaped backticks or prose that renders correctly.

- [ ] **Step 5: Regenerate docs/examples**

Run:

```bash
go run ./cmd/gsx-examples
```

Expected: `docs/examples.json` and generated snippets update consistently.

- [ ] **Step 6: Run docs-related checks**

Run:

```bash
go test ./internal/examplegen ./internal/gsxfmt ./internal/printer -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit docs**

```bash
git add docs
git commit -m "docs: document explicit JS CSS literals"
```

## Task 5: Formatter and Printer Support

**Files:**
- Modify: `internal/printer/*`
- Modify: `internal/gsxfmt/*`
- Modify: `internal/reindent/*`
- Modify: `parser/golden_test.go` or printer tests

- [ ] **Step 1: Add printer round-trip tests**

Add tests in `internal/printer/printer_test.go` or the existing attr printer test file:

```go
func TestPrintEmbeddedAttrDirect(t *testing.T) {
	src := "package p\ncomponent C(id string) {\n\t<button @click=js`save(@{id})`>Save</button>\n}\n"
	assertPrintStable(t, src)
}

func TestPrintEmbeddedAttrMultiline(t *testing.T) {
	src := "package p\ncomponent C(open bool) {\n\t<div x-data=js`\n\t\t{ open: @{open} }\n\t`>x</div>\n}\n"
	assertPrintStable(t, src)
}
```

Use the repo's actual printer test helper names; if `assertPrintStable` does not exist, copy the pattern used by adjacent tests.

- [ ] **Step 2: Run printer tests and verify they fail**

Run:

```bash
go test ./internal/printer ./internal/gsxfmt -run Embedded -count=1
```

Expected: FAIL until printer handles `EmbeddedAttr`.

- [ ] **Step 3: Implement printer support**

Find the attr printing switch and add:

```go
case *ast.EmbeddedAttr:
	p.write(a.Name)
	p.write("=")
	if a.Lang == ast.EmbeddedJS {
		p.write("js`")
	} else {
		p.write("css`")
	}
	p.printRawHoleSegments(a.Segments)
	p.write("`")
```

Use the existing raw-text / `<script>` / `<style>` segment rendering helper if present. Preserve compact `@{expr}` formatting for interpolation holes.

- [ ] **Step 4: Preserve embedded literal bodies in the formatter**

For this slice, print embedded attribute literal bodies exactly as parsed and keep `@{expr}` holes compact. Do not route `js`...`` / `css`...`` bodies through the `<script>` / `<style>` reindent machinery until there is a real embedded-literal formatter design that understands backtick delimiters and attribute layout.

Add a formatter test proving that multiline embedded literals round-trip without content changes:

```go
func TestFormatEmbeddedAttrPreservesBody(t *testing.T) {
	src := "package p\ncomponent C(open bool) {\n\t<div x-data=js`\n\t\t{ open: @{open} }\n\t`>x</div>\n}\n"
	assertFormat(t, src, src)
}
```

Use the formatter assertion helper already used in `internal/gsxfmt` tests. Pass `src` as both input and expected output.

- [ ] **Step 5: Run formatter/printer tests**

Run:

```bash
go test ./internal/printer ./internal/gsxfmt ./internal/reindent -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit formatter support**

```bash
git add internal/printer internal/gsxfmt internal/reindent parser
git commit -m "feat(fmt): support JS CSS attr literals"
```

## Task 6: Full Regression and Cleanup

**Files:**
- Modify any stale references found by search.

- [ ] **Step 1: Search for stale JS/CSS attr classifier references**

Run:

```bash
rg -n 'WithJSAttrs|WithCSSAttrs|WithAttrClassifier|jsAttrs|cssAttrs|JS-context attribute|JS attribute|CSS-context attribute|attr classifier|attr-classifier' .
```

Expected: hits only in historical design/plan docs or deliberately updated comments. Update all current docs/comments/tests. Do not edit old historical specs/plans unless they are linked as current guidance.

- [ ] **Step 2: Search for old spaced interpolation examples in current docs**

Run:

```bash
rg -n '@\{ [^}]+ \}' docs internal/corpus parser internal
```

Expected: no current examples for new syntax use spaced `@{ expr }`. Historical docs may remain if they describe existing `<script>` syntax; prefer compact `@{expr}` when touching current docs.

- [ ] **Step 3: Run focused tests**

Run:

```bash
go test ./parser ./internal/jsx ./internal/codegen ./internal/corpus ./gen ./internal/printer ./internal/gsxfmt -count=1
```

Expected: PASS.

- [ ] **Step 4: Run gopls checks on touched Go files**

Run one command with all touched Go files, or batches if the list is long:

```bash
gopls check -severity=hint parser/attrs.go ast/ast.go internal/codegen/emit.go internal/codegen/analyze.go internal/jsx/jsx.go gen/options.go gen/configfile.go gen/main.go
```

Expected: no errors; hints should be reviewed for unused helpers/imports.

- [ ] **Step 5: Run full baseline**

Run:

```bash
make check
```

Expected: PASS.

- [ ] **Step 6: Commit cleanup**

When Step 1-5 required additional edits:

```bash
git add .
git commit -m "chore: clean up JS CSS literal rollout"
```

When Step 1-5 did not require edits, record that in the task review and skip this commit.

## Task 7: Sibling Editor Grammar Follow-Up

**Files:**
- Modify sibling repo: `../tree-sitter-gsx`
- Modify sibling repo: `../vscode-gsx`

- [ ] **Step 1: Update tree-sitter grammar**

In `../tree-sitter-gsx`, add grammar support for:

```gsx
@click=js`save(@{id})`
style=css`width:@{pct}%`
@click={js`save(@{id})`}
```

The scanner should treat `js`...`` and `css`...`` like raw embedded language text with `@{}` Go holes, not as Go template literals.

- [ ] **Step 2: Update injections/highlights**

In `../tree-sitter-gsx`, update injection queries so:

- `js`...`` content injects JavaScript;
- `css`...`` content injects CSS;
- `@{expr}` holes inject Go;
- the surrounding attr remains gsx/HTML.

- [ ] **Step 3: Update VS Code extension grammar/tests**

In `../vscode-gsx`, update TextMate/semantic grammar support for the same syntax and add fixture coverage.

- [ ] **Step 4: Run sibling repo tests**

Use the sibling repos' documented test commands. If unknown, inspect their `package.json`, `Makefile`, or README and run the smallest relevant grammar test suite.

- [ ] **Step 5: Commit sibling repo changes separately**

Commit in each sibling repo with messages like:

```bash
git commit -m "feat: highlight explicit JS CSS attr literals"
```

Do not mix sibling repo commits into the gsx repo branch.

## Self-Review Checklist

- [ ] Spec coverage: parser direct/braced syntax, compact `@{expr}`, JS/CSS escaping, static quoted attrs, config/API removal, docs, formatter, URL-only attr config, and sibling editor work all have tasks.
- [ ] Placeholder scan: no "TBD", "TODO", "similar to", or vague "handle edge cases" steps remain.
- [ ] Type consistency: `ast.EmbeddedAttr`, `ast.EmbeddedJS`, `ast.EmbeddedCSS`, `EmbeddedLang`, `emitEmbeddedJSAttr`, and `emitEmbeddedCSSAttr` names are used consistently.
- [ ] Verification: focused tests, `gopls check`, and `make check` are included before completion.
