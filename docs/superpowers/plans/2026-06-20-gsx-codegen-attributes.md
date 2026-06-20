# gsx Codegen — Attributes (security core) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Emit element attributes with **compile-time context-aware escaping** — static, expr (URL/plain context, §5 type-aware, always double-quoted), and bool — with expr values in JS/CSS/event-handler contexts **rejected fail-closed**.

**Architecture:** Because gsx parses to an AST, every attribute's escaping context is **structural** (known at codegen from the attribute name + element), not inferred by a runtime HTML tokenizer like `html/template`. So gsx's "context state machine" is compile-time, table-driven classification per attribute. Expr-attr types are resolved by extending the existing skeleton probe to attribute expressions (keeping the source-order alignment invariant).

**Tech Stack:** Go 1.26.1. `internal/codegen` (`go/packages`, `go/types`, `go/format`). Runtime target `github.com/gsxhq/gsx` (stdlib-only): `gw.S`/`gw.AttrValue`/`gw.URL`/`gw.BoolAttr`.

## Global Constraints

- Runtime stays stdlib-only; generator may use `golang.org/x/tools`. No runtime reflection / no `any` in generated code.
- **Safe by default:** every emitted attribute value is **double-quoted** and escaped for its context. The author-written *static* value and `<script>`/`<style>` bodies are trusted (verbatim, the parser already keeps raw-text literal). Untrusted is *interpolated* data.
- **Fail-closed JS/CSS/event-handler gate:** an **expr** value in a JS context (`on*`, `@*`, `hx-on*`), a CSS context (`style`), errors clearly at codegen — escaping cannot secure those grammars; a safe type via `|> js`/`|> css` filter comes with the pipeline phase. (Static literals in those attributes are fine.)
- gofmt + go vet clean. Each task ends with a **render golden** (`renderPackage` compiles+runs, `assertHTMLEqual`) plus `go test ./...` green. The security cases get explicit XSS / URL-sanitize / reject tests.
- This is codegen phase-2 "Attributes (security core)" (`docs/ROADMAP.md`). **In scope:** static / expr (context-classified) / bool. **Deferred to a follow-on:** composable `class={…}`/`style={…}`, spread `{...attrs}`, conditional `{ if c { attr } }` (those `genNode`/`emitAttr` paths must error clearly, not silently drop). Also deferred: expr-attr `?` try-marker, attr-expr pipeline `|>` (guard like interpolation).

### Context classification (the table)

- **URL context** (value escaped via `gw.URL`, scheme allow-listed): attribute name (lowercased) in: `href`, `src`, `action`, `formaction`, `poster`, `cite`, `ping`, `data`, `background`, `manifest`, `xlink:href`, `hx-get`, `hx-post`, `hx-put`, `hx-delete`, `hx-patch`. (Note for a later task: `<meta http-equiv=refresh content="…url=…">` and `srcset` are special-cased URL carriers — out of scope here, tracked.)
- **JS context** (expr → reject): name starts with `on` followed by a letter (`onclick`, `onload`, …), or starts with `@` (Alpine), or starts with `hx-on`.
- **CSS context** (expr → reject): name == `style`.
- **Plain context** (value via `gw.AttrValue`, §5 type-aware): everything else.

---

## File Structure (modified)

- `internal/codegen/analyze.go` — `collectExprs` (was `collectInterps`; now collects `*Interp` + `*ExprAttr` in source order), `componentExprs`, `usedParams`, `emitProbes` (probe attr exprs), `harvest` (build `map[ast.Node]types.Type`). The `resolved` map type changes to `map[ast.Node]types.Type`.
- `internal/codegen/emit.go` — `generateFile`/`genComponent`/`genNode`/`genInterp` re-typed to `map[ast.Node]types.Type`; `genNode` Element case emits the open tag with attributes; new `emitAttr` + `attrContext` classifier.
- `internal/codegen/e2e_test.go` — render goldens (static/bool, expr URL/plain/§5-types, XSS, reject cases).

---

### Task 1: Resolve attribute expression types (extend the probe)

Extend type resolution from interpolations to `*ast.ExprAttr` expressions, keeping the source-order alignment invariant (per element: attr-exprs before children).

**Files:**
- Modify: `internal/codegen/analyze.go`, `internal/codegen/emit.go` (signature retype only)
- Test: `internal/codegen/analyze_test.go` (new white-box unit test)

**Interfaces:**
- Produces: `resolved map[ast.Node]types.Type` (keyed by `*ast.Interp` OR `*ast.ExprAttr`); `collectExprs(nodes, *[]ast.Node)`; `componentExprs(c) []ast.Node`.
- Consumes: existing skeleton/`_gsxuse`/`harvest` machinery.

- [ ] **Step 1: Write the failing white-box test**

Create `internal/codegen/analyze_test.go`:

```go
package codegen

import (
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
	gsxparser "github.com/gsxhq/gsx/parser"
)

// TestResolveAttrExprType checks that an attribute expression's type is resolved
// (not just interpolations).
func TestResolveAttrExprType(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gsxa\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(tmp, "genpkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := "package views\n\ncomponent A(id string) {\n\t<div data-id={id}></div>\n}\n"
	writeFile(t, pkgDir, "views.gsx", src)

	file, err := gsxparser.ParseFile(token.NewFileSet(), filepath.Join(pkgDir, "views.gsx"), []byte(src), 0)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveTypesPkg(pkgDir, map[string]*gsxast.File{filepath.Join(pkgDir, "views.gsx"): file})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// find the ExprAttr node and assert its resolved type is string.
	var attr *gsxast.ExprAttr
	gsxast.Inspect(file, func(n gsxast.Node) bool {
		if a, ok := n.(*gsxast.ExprAttr); ok {
			attr = a
		}
		return true
	})
	if attr == nil {
		t.Fatal("no ExprAttr in AST")
	}
	got, ok := resolved[attr]
	if !ok || got == nil {
		t.Fatalf("attr expr type not resolved (resolved has %d entries)", len(resolved))
	}
	if b, ok := got.Underlying().(*types.Basic); !ok || b.Info()&types.IsString == 0 {
		t.Fatalf("attr expr type = %s, want string", got)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/codegen/ -run TestResolveAttrExprType -v`
Expected: FAIL — `resolveTypesPkg` returns `map[*ast.Interp]types.Type` (won't compile against `resolved[attr]` keyed by `*ExprAttr`), or the attr type isn't in the map.

- [ ] **Step 3: Retype `resolved` to `map[ast.Node]types.Type` and collect attr exprs**

In `internal/codegen/analyze.go`:

(a) Change `resolveTypesPkg`'s return type and the `out` map to `map[gsxast.Node]types.Type` (was `map[*gsxast.Interp]types.Type`).

(b) Rename `collectInterps` → `collectExprs` and collect `*ExprAttr` too (per non-component element: its ExprAttr exprs in attr order, THEN children). Replace it with:

```go
// collectExprs gathers the type-needing expression nodes (*Interp and *ExprAttr)
// in depth-first source order — per element, attribute expressions BEFORE
// children — matching emitProbes/genNode traversal so the k-th probe aligns with
// the k-th node.
func collectExprs(nodes []gsxast.Markup, out *[]gsxast.Node) {
	for _, n := range nodes {
		switch t := n.(type) {
		case *gsxast.Interp:
			*out = append(*out, t)
		case *gsxast.Element:
			if isComponentTag(t.Tag) {
				continue // child component: props deferred; no attr exprs / children here
			}
			for _, a := range t.Attrs {
				if ea, ok := a.(*gsxast.ExprAttr); ok {
					*out = append(*out, ea)
				}
			}
			collectExprs(t.Children, out)
		case *gsxast.Fragment:
			collectExprs(t.Children, out)
		case *gsxast.ForMarkup:
			collectExprs(t.Body, out)
		case *gsxast.IfMarkup:
			collectExprs(t.Then, out)
			collectExprs(t.Else, out)
		case *gsxast.SwitchMarkup:
			for _, cc := range t.Cases {
				collectExprs(cc.Body, out)
			}
		}
	}
}
```

(c) Replace `componentInterps` with:

```go
func componentExprs(c *gsxast.Component) []gsxast.Node {
	var out []gsxast.Node
	collectExprs(c.Body, &out)
	return out
}
```

(d) In `usedParams`, change the loop to iterate `componentExprs(c)` and read `.Expr` from either node kind:

```go
	for _, n := range componentExprs(c) {
		var expr string
		switch v := n.(type) {
		case *gsxast.Interp:
			expr = v.Expr
		case *gsxast.ExprAttr:
			expr = v.Expr
		}
		for id := range valueIdents(expr) {
			refs[id] = true
		}
	}
```

(e) In `harvest`, change `interps := componentInterps(c)` → `nodes := componentExprs(c)`, `out map[gsxast.Node]types.Type`, and map `out[nodes[k]] = …`.

(f) Update any other reference to `componentInterps`/`collectInterps` (e.g. in `collectClauseSrc`'s sibling code) to the new names. Run `grep -rn 'collectInterps\|componentInterps' internal/codegen/` and fix all.

- [ ] **Step 4: Emit `_gsxuse` probes for attr exprs**

In `internal/codegen/analyze.go` `emitProbes`, in the `*gsxast.Element` non-component branch, emit attr-expr probes BEFORE recursing children:

```go
		case *gsxast.Element:
			if isComponentTag(t.Tag) {
				fmt.Fprintf(sb, "_ = %s(%sProps{})\n", t.Tag, t.Tag)
			} else {
				for _, a := range t.Attrs {
					if ea, ok := a.(*gsxast.ExprAttr); ok {
						fmt.Fprintf(sb, "_gsxuse(%s)\n", strings.TrimSpace(ea.Expr))
					}
				}
				emitProbes(sb, t.Children)
			}
```

- [ ] **Step 5: Retype the emitter signatures**

In `internal/codegen/emit.go`, change every `resolved map[*ast.Interp]types.Type` to `resolved map[ast.Node]types.Type` (in `generateFile`, `genComponent`, `genNode`, `genInterp`). `genInterp`'s `resolved[n]` lookup (n is `*ast.Interp`) still works (interface key). Add `import` of nothing new (`ast` already imported).

- [ ] **Step 6: Run to verify pass + no regression**

Run: `go test ./internal/codegen/ -run TestResolveAttrExprType -v` (PASS), then the FULL suite `go test ./internal/codegen/` — the retype + traversal change must not break phase-1/control-flow tests (interpolation resolution still aligns; attr exprs are additive and only appear where Elements have ExprAttrs, which existing tests don't have).

- [ ] **Step 7: Run all + commit**

Run: `go test ./... && go vet ./... && gofmt -l internal/codegen/`
Expected: green.

```bash
git add internal/codegen/
git commit -m "codegen: resolve attribute expression types (resolved keyed by ast.Node; collectExprs)"
```

---

### Task 2: Emit the open tag with static + bool attributes

Restructure `genNode`'s Element case to emit `<tag …attrs…>` and add `emitAttr` for `StaticAttr` and `BoolAttr` (always double-quoted; static value escaped at codegen).

**Files:**
- Modify: `internal/codegen/emit.go`
- Test: `internal/codegen/e2e_test.go`

**Interfaces:**
- Consumes: `emitS`, `gw.S`, `gw.BoolAttr`.
- Produces: `emitAttr(b, attr, resolved, imports) error`; `genNode` Element emits open tag + attrs.

- [ ] **Step 1: Write the failing render test**

Add to `internal/codegen/e2e_test.go`:

```go
func TestRenderStaticAndBoolAttrs(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

component Field(on bool) {
	<input type="text" class="form-control" required disabled={on}/>
}
`,
	}
	got := renderPackage(t, files, `p.Field(p.FieldProps{On: true})`)
	assertHTMLEqual(t, got, `<input type="text" class="form-control" required disabled/>`)
	got = renderPackage(t, files, `p.Field(p.FieldProps{On: false})`)
	assertHTMLEqual(t, got, `<input type="text" class="form-control" required/>`)
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/codegen/ -run TestRenderStaticAndBoolAttrs -v`
Expected: FAIL — `element attributes not supported yet (<input>)`.

- [ ] **Step 3: Restructure the Element case + add `emitAttr`**

In `internal/codegen/emit.go`, replace the `*ast.Element` case's attribute guard. Find:

```go
		if len(t.Attrs) > 0 {
			return fmt.Errorf("codegen spike: element attributes not supported yet (<%s>)", t.Tag)
		}
		if t.Void {
			emitS(b, "<"+t.Tag+"/>")
			return nil
		}
		emitS(b, "<"+t.Tag+">")
```

and replace with:

```go
		emitS(b, "<"+t.Tag)
		for _, a := range t.Attrs {
			if err := emitAttr(b, a, resolved, imports); err != nil {
				return err
			}
		}
		if t.Void {
			emitS(b, "/>")
			return nil
		}
		emitS(b, ">")
```

Add `emitAttr` (and `htmlAttrEscape` for compile-time escaping of static values):

```go
// emitAttr emits one element attribute. Static values are escaped at codegen and
// always double-quoted; bool attrs use gw.BoolAttr. Expr attrs are handled in a
// later task; the deferred attr kinds error clearly.
func emitAttr(b *bytes.Buffer, a ast.Attr, resolved map[ast.Node]types.Type, imports map[string]bool) error {
	switch t := a.(type) {
	case *ast.StaticAttr:
		fmt.Fprintf(b, "\t\tgw.S(%s)\n", strconv.Quote(" "+t.Name+`="`+htmlAttrEscape(t.Value)+`"`))
	case *ast.BoolAttr:
		fmt.Fprintf(b, "\t\tgw.BoolAttr(%s, true)\n", strconv.Quote(t.Name))
	case *ast.ExprAttr:
		return emitExprAttr(b, t, resolved, imports) // implemented in Task 3
	case *ast.SpreadAttr, *ast.ClassAttr, *ast.CondAttr:
		return fmt.Errorf("codegen: attribute kind %T not supported yet (deferred)", a)
	default:
		return fmt.Errorf("codegen: unknown attribute %T", a)
	}
	return nil
}

// htmlAttrEscape escapes a static attribute value for a double-quoted context at
// codegen time (matches the runtime gw.AttrValue entity set).
func htmlAttrEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;", "'", "&#39;")
	return r.Replace(s)
}
```

For Task 2, add a temporary stub so the package compiles before Task 3:

```go
func emitExprAttr(b *bytes.Buffer, a *ast.ExprAttr, resolved map[ast.Node]types.Type, imports map[string]bool) error {
	return fmt.Errorf("codegen: expr attribute %q not supported yet", a.Name)
}
```

(Task 3 replaces this stub with the real implementation. Ensure `strconv` and `strings` are imported in emit.go.)

- [ ] **Step 4: Run to verify pass + all + commit**

Run: `go test ./internal/codegen/ -run TestRenderStaticAndBoolAttrs -v` (PASS), then `go test ./... && go vet ./... && gofmt -l internal/codegen/`.

```bash
git add internal/codegen/
git commit -m "codegen: emit open tag with static + bool attributes (always-quoted, codegen-escaped)"
```

---

### Task 3: Expr attributes — context classification + escaping (the security core)

Implement `emitExprAttr`: classify the attribute's context, apply the right escaper, reject JS/CSS contexts fail-closed, and render the value type-aware (§5).

**Files:**
- Modify: `internal/codegen/emit.go`
- Test: `internal/codegen/e2e_test.go`

**Interfaces:**
- Consumes: `classify` (§5), `resolved[a]` (Task 1), `gw.AttrValue`/`gw.URL`/`gw.BoolAttr`.
- Produces: `emitExprAttr` (replacing the stub), `attrContext(name string) context`.

- [ ] **Step 1: Write the failing render tests**

Add to `internal/codegen/e2e_test.go`:

```go
func TestRenderExprAttrs(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

component Link(url string, label string, n int, checked bool) {
	<a href={url} data-label={label} data-n={n} aria-hidden={checked}>{label}</a>
}
`,
	}
	// URL sanitized + attr-escaped; string attr-escaped; int formatted; bool -> boolean attr.
	got := renderPackage(t, files,
		`p.Link(p.LinkProps{URL: "/p?q=a&b", Label: "a\"b", N: 5, Checked: true})`)
	assertHTMLEqual(t, got, `<a href="/p?q=a&b" data-label="a&#34;b" data-n="5" aria-hidden>a"b</a>`)
}

func TestRenderExprAttrURLBlocked(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

component Bad(u string) { <a href={u}>x</a> }
`,
	}
	got := renderPackage(t, files, `p.Bad(p.BadProps{U: "javascript:alert(1)"})`)
	// urlSanitize replaces a dangerous scheme with the sentinel.
	assertHTMLEqual(t, got, `<a href="about:invalid#gsx">x</a>`)
}

func TestRenderExprAttrJSRejected(t *testing.T) {
	for _, name := range []string{"onclick", "style"} {
		files := map[string]string{
			"views.gsx": "package views\n\ncomponent C(x string) {\n\t<div " + name + "={x}>y</div>\n}\n",
		}
		err := generatePackageErr(t, files)
		if err == nil {
			t.Fatalf("%s: expected fail-closed error for expr in JS/CSS context, got nil", name)
		}
		if !strings.Contains(err.Error(), "context") {
			t.Fatalf("%s: expected context-rejection error, got: %v", name, err)
		}
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/codegen/ -run 'TestRenderExprAttr' -v`
Expected: FAIL — the `emitExprAttr` stub errors "not supported yet" for all of them.

- [ ] **Step 3: Implement `emitExprAttr` + `attrContext`**

In `internal/codegen/emit.go`, replace the `emitExprAttr` stub with:

```go
type attrCtx int

const (
	ctxPlain attrCtx = iota
	ctxURL
	ctxJS
	ctxCSS
)

var urlAttrs = map[string]bool{
	"href": true, "src": true, "action": true, "formaction": true, "poster": true,
	"cite": true, "ping": true, "data": true, "background": true, "manifest": true,
	"xlink:href": true, "hx-get": true, "hx-post": true, "hx-put": true,
	"hx-delete": true, "hx-patch": true,
}

// attrContext classifies an attribute name into an escaping context (structural,
// from the name — gsx's compile-time alternative to html/template's tokenizer).
func attrContext(name string) attrCtx {
	n := strings.ToLower(name)
	switch {
	case urlAttrs[n]:
		return ctxURL
	case n == "style":
		return ctxCSS
	case strings.HasPrefix(n, "@") || strings.HasPrefix(n, "hx-on") ||
		(strings.HasPrefix(n, "on") && len(n) > 2 && n[2] >= 'a' && n[2] <= 'z'):
		return ctxJS
	default:
		return ctxPlain
	}
}

// emitExprAttr emits an expr attribute value with context-aware escaping. JS/CSS
// contexts reject (fail-closed) until safe-type pipeline filters exist.
func emitExprAttr(b *bytes.Buffer, a *ast.ExprAttr, resolved map[ast.Node]types.Type, imports map[string]bool) error {
	if len(a.Stages) > 0 {
		return fmt.Errorf("codegen: pipeline `|>` not supported in codegen yet (attribute %q)", a.Name)
	}
	if a.Try {
		return fmt.Errorf("codegen: `?` try-marker in attribute %q not supported yet", a.Name)
	}
	switch attrContext(a.Name) {
	case ctxJS:
		return fmt.Errorf("codegen: expr value in JS/event-handler context (%q) is unsafe; needs a safe type via `|> js` (not available yet) — use a static value", a.Name)
	case ctxCSS:
		return fmt.Errorf("codegen: expr value in CSS context (%q) is unsafe; needs a safe type via `|> css` (not available yet) — use a static value", a.Name)
	}

	t, ok := resolved[a]
	if !ok || t == nil {
		return fmt.Errorf("codegen: could not resolve type of attribute %q value %q", a.Name, a.Expr)
	}
	expr := strings.TrimSpace(a.Expr)

	// A bool-typed value is a boolean attribute regardless of context.
	if classify(t) == catBool {
		fmt.Fprintf(b, "\t\tgw.BoolAttr(%s, bool(%s))\n", strconv.Quote(a.Name), expr)
		return nil
	}

	fmt.Fprintf(b, "\t\tgw.S(%s)\n", strconv.Quote(" "+a.Name+`="`))
	if attrContext(a.Name) == ctxURL {
		// URL context: value must be string-like; sanitize + escape.
		fmt.Fprintf(b, "\t\tgw.URL(%s)\n", urlStringExpr(expr, t))
	} else {
		if err := emitAttrValue(b, expr, t, imports); err != nil {
			return err
		}
	}
	fmt.Fprintf(b, "\t\tgw.S(%s)\n", strconv.Quote(`"`))
	return nil
}

// urlStringExpr renders a URL-context value as a string expression for gw.URL.
func urlStringExpr(expr string, t types.Type) string {
	if classify(t) == catString {
		return "string(" + expr + ")"
	}
	return expr // non-string URL values are unusual; let the Go compiler check gw.URL's arg
}

// emitAttrValue writes a non-URL attribute value via gw.AttrValue, §5 type-aware.
func emitAttrValue(b *bytes.Buffer, expr string, t types.Type, imports map[string]bool) error {
	switch classify(t) {
	case catString:
		fmt.Fprintf(b, "\t\tgw.AttrValue(string(%s))\n", expr)
	case catBytes:
		fmt.Fprintf(b, "\t\tgw.AttrValue(string(%s))\n", expr)
	case catInt:
		imports["strconv"] = true
		fmt.Fprintf(b, "\t\tgw.AttrValue(strconv.FormatInt(int64(%s), 10))\n", expr)
	case catUint:
		imports["strconv"] = true
		fmt.Fprintf(b, "\t\tgw.AttrValue(strconv.FormatUint(uint64(%s), 10))\n", expr)
	case catFloat:
		imports["strconv"] = true
		fmt.Fprintf(b, "\t\tgw.AttrValue(strconv.FormatFloat(float64(%s), 'g', -1, 64))\n", expr)
	case catStringer:
		fmt.Fprintf(b, "\t\tgw.AttrValue((%s).String())\n", expr)
	default:
		return fmt.Errorf("codegen: attribute value type %s not supported (string/number/bool/Stringer only)", t)
	}
	return nil
}
```

(`gw.URL` takes a `string`; the `string(expr)` conversion handles named string types. A bool expr attr → `gw.BoolAttr(name, bool(expr))`. Node/[]Node in an attribute value are not meaningful → fall to the `default` error.)

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/codegen/ -run 'TestRenderExprAttr' -v`
Expected: PASS — URL sanitized+escaped, string escaped, int formatted, bool→boolean attr, JS/CSS rejected.

- [ ] **Step 5: Add an XSS regression + run all + commit**

Add to `internal/codegen/e2e_test.go`:

```go
func TestRenderExprAttrXSS(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

component C(v string) { <div data-x={v}>y</div> }
`,
	}
	got := renderPackage(t, files, `p.C(p.CProps{V: "\"><script>alert(1)</script>"})`)
	// the quote and angle brackets must be entity-escaped — no tag breakout.
	assertHTMLEqual(t, got, `<div data-x="&#34;&gt;&lt;script&gt;alert(1)&lt;/script&gt;">y</div>`)
}
```

Run: `go test ./internal/codegen/ -run TestRenderExprAttrXSS -v` (PASS), then `go test ./... && go vet ./... && gofmt -l internal/codegen/`.

```bash
git add internal/codegen/
git commit -m "codegen: context-aware expr attribute escaping (URL/plain/§5; JS/CSS fail-closed)"
```

---

## Self-Review

**1. Spec coverage** (roadmap "Attributes (security core)" + security section):
- Static attrs, always-quoted, escaped → Task 2. ✓
- Bool attrs → Task 2. ✓
- Expr attrs, context-classified (structural, compile-time) → Task 3. ✓
- URL context → `gw.URL` (scheme allow-list via runtime) + the URL-attr table → Task 3. ✓
- Plain context → `gw.AttrValue`, §5 type-aware → Task 3. ✓
- JS/CSS/event-handler expr → fail-closed reject → Task 3. ✓
- Always-quote emitted values → Tasks 2–3. ✓
- Attr-expr type resolution (the probe extension) → Task 1. ✓
- Deferred (class/style composable, spread, conditional, attr `?`, attr `|>`) error clearly → Task 2 (`emitAttr` default) + Task 3 (Stages/Try guards). ✓

**2. Placeholder scan:** No TBD. Task 2's `emitExprAttr` stub is explicitly replaced in Task 3 (stated). The `urlStringExpr` non-string fallback is a documented decision (let the Go compiler check), not a placeholder.

**3. Type/signature consistency:**
- `resolved map[ast.Node]types.Type` introduced in Task 1, threaded through `genNode`/`genInterp`/`emitAttr`/`emitExprAttr` (Tasks 1–3). `genInterp`'s `resolved[n]` (n `*ast.Interp`) and `emitExprAttr`'s `resolved[a]` (a `*ast.ExprAttr`) both key the unified map. ✓
- `collectExprs`/`componentExprs` (Task 1) return `[]ast.Node`; `harvest`/`usedParams` updated to match. ✓
- `classify`/`catString`/`catInt`/… (existing) reused by `emitAttrValue` and the bool check. ✓
- `attrContext`/`attrCtx`/`urlAttrs` defined and used within Task 3. ✓

---

## Execution Notes for the Controller

- Tasks sequential 1→3. Task 1 (resolution retype + traversal) is the riskiest — it changes the shared `resolved` map type and the traversal, and must preserve the **order invariant** (now: per element, attr-exprs before children, then the same control-flow recursion). Task 1's review must verify `collectExprs`, `emitProbes`, and `harvest` traverse identically including attr-exprs.
- Security is the point of Task 3: its review should adversarially probe escaping (XSS string in a plain attr; `javascript:`/`data:` in a URL attr → sentinel; a quote/`>` breakout attempt; a bool expr → boolean attr; expr in `onclick`/`style`/`@click`/`hx-on:` → clean reject; a named string/int/float attr value).
- Out-of-scope attrs (class/style composable, spread, conditional) must ERROR clearly (Task 2 default) — never silently drop.
- Model guidance: Task 1 → standard (shared-map retype + invariant); Task 2 → cheap (mechanical); Task 3 → standard (security logic + the context table). Final review → most capable, adversarial escaping probe.
- Branch: `feat/codegen-attributes` off `main`; land via review + an independent adversarial security review (the project's pattern) when green.
