# Fragment Expression Literals Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let `<>…</>` fragments appear in Go-expression position inside `.gsx` files, evaluating to a `gsx.Node` (empty `<></>` = the render-nothing nop), closing the element-literals v1 boundary that errored them there.

**Architecture:** A direct mirror of element literals (PR #42). Fragments already parse and codegen in **markup position**; this admits `*ast.Fragment` as a `GoPart` so it rides inside `ast.GoWithElements`, turns the deliberate parser error (`goexpr.go`) into a real lowering, and reuses the element-value machinery (`emitNodeFuncBody` + the scope-capturing IIFE probe). A fragment lowers to an inline `gsx.Func(func(ctx, w){ …render children… })`; the empty case is a no-op closure.

**Tech Stack:** Go 1.26.1, `go/types` skeleton probe, `go/scanner` operand/operator tracker, txtar corpus.

## Global Constraints

- **Runtime stays stdlib-only.** No new dependencies in the root `gsx` package. The lowering target `gsx.Fragment`/`gsx.Func` already exist.
- **emit ≡ probe invariant.** Every shape `emit.go` produces, `analyze.go` must produce a type-check skeleton for. A fragment value emitted as `gsx.Func(...)` MUST have a matching IIFE probe, or generation breaks.
- **Empty-fragment lowering is UNIFORM.** `<></>` lowers to a no-op `gsx.Func(func(ctx context.Context, _gsxw io.Writer) error { return nil })`, NOT a special-cased `gsx.Fragment()`. One lowering path for all fragments. (Locked design decision.)
- **JSX rules:** explicit wrapping required — bare adjacent `<A/><B/>` in expression position stays unsupported; multiple siblings must be wrapped `<><A/><B/></>`. Fragments carry NO attributes (grammar already enforces).
- **Scope capture:** interps inside an expression-position fragment (`{ x }`, `{ for … }`) MUST resolve against the enclosing func's params / locals / receiver, exactly like element literals. This is the emit≡probe scope requirement the element-literals adversarial review caught — corpus MUST cover func-local and method-receiver scope, not just package-level vars.
- **Every syntax/codegen change ships a corpus case** (`internal/corpus/testdata/cases/**/*.txtar`), goldens regenerated with `go test ./internal/corpus -run TestCorpus -update` then verified without `-update`. Don't hand-edit `.x.go`/golden files.

---

## File Structure

- `ast/ast.go` — add `*Fragment` to the `GoPart` interface (one method); update Inspect doc comment.
- `parser/goexpr.go` — `splitGoElements`: admit a parsed `*ast.Fragment` as a part instead of erroring.
- `internal/printer/printer.go` — `goWithElements`: render a `*ast.Fragment` part via existing `p.fragment`.
- `internal/wsnorm/wsnorm.go` — GoWithElements branch: normalize a fragment part's children.
- `internal/codegen/emit.go` — extract shared `emitNodeValue`; add `emitFragmentValue`; dispatch `*ast.Fragment` in the GoWithElements loop.
- `internal/codegen/analyze.go` — generalize the embedded-element probe list from `[]*Element` to `[][]Markup`; probe + harvest a fragment's children.
- `internal/codegen/module_importer.go` — follow the `gwElements` type change (map value type + call site).
- `internal/corpus/testdata/cases/fragment-literals/*.txtar` — new corpus cases.
- `docs/guide/syntax/*.md`, `docs/ROADMAP.md` — docs.

---

### Task 1: AST — `*ast.Fragment` joins `GoPart`

**Files:**
- Modify: `ast/ast.go` (near line 229, after `func (*Fragment) markupNode() {}`)
- Test: `ast/ast_test.go`

**Interfaces:**
- Consumes: existing `GoPart` interface (`ast.go:129`, method `goPartNode()`), existing `*Fragment` type, existing `GoWithElements{Parts []GoPart}`, existing `Inspect`.
- Produces: `*ast.Fragment` now satisfies `ast.GoPart`, so it is a legal `GoWithElements` part. Consumed by Tasks 2–4.

- [ ] **Step 1: Write the failing test**

Add to `ast/ast_test.go`:

```go
func TestFragmentIsGoPart(t *testing.T) {
	// A *Fragment must be usable as a GoWithElements part (compile-time proof
	// via the slice literal) and Inspect must recurse into its children.
	frag := &ast.Fragment{Children: []ast.Markup{&ast.Text{Value: "hi"}}}
	we := &ast.GoWithElements{Parts: []ast.GoPart{
		ast.GoText{Src: "var x = "},
		frag,
	}}

	var sawText bool
	ast.Inspect(we, func(n ast.Node) bool {
		if txt, ok := n.(*ast.Text); ok && txt.Value == "hi" {
			sawText = true
		}
		return true
	})
	if !sawText {
		t.Fatal("Inspect did not reach the fragment's child text through GoWithElements")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ast -run TestFragmentIsGoPart`
Expected: FAIL to compile — `*ast.Fragment` does not implement `ast.GoPart` (missing `goPartNode`).

- [ ] **Step 3: Add the interface method + fix the doc comment**

In `ast/ast.go`, immediately after `func (*Fragment) markupNode() {}`:

```go
func (*Fragment) goPartNode() {}
```

In the `Inspect` doc comment (the `// - *GoWithElements: each Part …` line), update to reflect that fragments recurse too:

```go
//   - *GoWithElements: each Part (GoText leaves; *Element and *Fragment recurse)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./ast -run TestFragmentIsGoPart`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add ast/ast.go ast/ast_test.go
git commit -m "feat(ast): admit *Fragment as a GoPart"
```

---

### Task 2: Parser — lower fragment marks to `*ast.Fragment` parts

**Files:**
- Modify: `parser/goexpr.go` (the `el, ok := markup.(*ast.Element)` block inside `splitGoElements`, ~lines 275–291)
- Test: `parser/goexpr_test.go`

**Interfaces:**
- Consumes: `scanGoElementMarks` (already flags `<>` marks), `sub.parseElement()` (already returns `*ast.Fragment` for a `<>…</>` span), `goTextPart`, Task 1's `GoPart` membership.
- Produces: `splitGoElements` returns a `*ast.GoWithElements` whose Parts may include `*ast.Fragment`; no diagnostic for a well-formed fragment. Consumed by Tasks 3–4.

- [ ] **Step 1: Write the failing tests**

Add to `parser/goexpr_test.go` (follow the file's existing parse-helper conventions; if it parses a whole file, wrap each snippet as a top-level `var`):

```go
func TestFragmentInExpressionPosition(t *testing.T) {
	// A non-empty fragment as a var initializer: one GoWithElements decl whose
	// parts include an *ast.Fragment with two element children. No diagnostics.
	f, errs := parseFileForTest(t, "package v\n\nvar list = <><a/><b/></>\n")
	if len(errs) != 0 {
		t.Fatalf("unexpected diagnostics: %v", errs)
	}
	we := findGoWithElements(t, f)
	frag := firstFragmentPart(t, we)
	if got := len(frag.Children); got != 2 {
		t.Fatalf("fragment children = %d, want 2", got)
	}
}

func TestEmptyFragmentInExpressionPosition(t *testing.T) {
	// <></> is legal and yields a zero-child fragment (the nop). No diagnostics.
	f, errs := parseFileForTest(t, "package v\n\nvar nop = <></>\n")
	if len(errs) != 0 {
		t.Fatalf("unexpected diagnostics: %v", errs)
	}
	frag := firstFragmentPart(t, findGoWithElements(t, f))
	if len(frag.Children) != 0 {
		t.Fatalf("empty fragment has %d children, want 0", len(frag.Children))
	}
}
```

Add the two helpers `findGoWithElements` and `firstFragmentPart` (type-assert over `we.Parts` for `*ast.Fragment`) and `parseFileForTest` if not already present in the test file — reuse whatever the existing element-literal parser tests use; do not invent a second parse harness.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./parser -run 'TestFragmentInExpressionPosition|TestEmptyFragmentInExpressionPosition'`
Expected: FAIL — currently `goexpr.go` records the diagnostic *"a fragment (<>...</>) literal is not supported as a Go expression value here"* and emits the fragment as GoText, so `firstFragmentPart` finds no `*ast.Fragment`.

- [ ] **Step 3: Admit the fragment as a part**

In `parser/goexpr.go`, replace the block:

```go
		el, ok := markup.(*ast.Element)
		if !ok {
			// scanGoElementMarks also flags a fragment-open (`<>`) as a
			// mark, but GoPart only admits *Element (and GoText) — a bare
			// fragment isn't yet a supported Go-expression value. Surface a
			// clear error rather than mistyping the part, but preserve the
			// fragment's consumed bytes as a verbatim GoText so the
			// round-trip invariant (concatenating each part's source
			// reproduces src) still holds.
			p.errorf(base+token.Pos(m.Off), "gsx: %s is not supported as a Go expression value here", markupKind(markup))
			parts = append(parts, goTextPart(src, m.Off, sub.i, base))
			cursor = sub.i
			continue
		}
		parts = append(parts, el)
		cursor = sub.i
```

with:

```go
		switch node := markup.(type) {
		case *ast.Element:
			parts = append(parts, node)
		case *ast.Fragment:
			// A <>…</> fragment is a first-class Go-expression value: a
			// gsx.Node holding its children with no wrapper element. Admitted
			// as a GoPart alongside *Element; lowered by emit.go to an inline
			// gsx.Func closure over the children (empty <></> renders nothing).
			parts = append(parts, node)
		default:
			// Any other markup (none reach here today — byteBeginsTag's
			// remaining candidates are never flagged as marks) is not a
			// supported Go-expression value. Preserve its bytes as verbatim
			// GoText so the round-trip invariant holds.
			p.errorf(base+token.Pos(m.Off), "gsx: %s is not supported as a Go expression value here", markupKind(markup))
			parts = append(parts, goTextPart(src, m.Off, sub.i, base))
			cursor = sub.i
			continue
		}
		cursor = sub.i
```

Leave `markupKind` in place (still used by the default arm; harmless).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./parser -run 'TestFragmentInExpressionPosition|TestEmptyFragmentInExpressionPosition'`
Expected: PASS.

- [ ] **Step 5: Run the full parser suite (no regression)**

Run: `go test ./parser`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add parser/goexpr.go parser/goexpr_test.go
git commit -m "feat(parser): admit <>…</> fragments in Go-expression position"
```

---

### Task 3: Printer + wsnorm — handle `*ast.Fragment` in `GoWithElements`

**Files:**
- Modify: `internal/printer/printer.go` (`goWithElements`, ~line 177)
- Modify: `internal/wsnorm/wsnorm.go` (GoWithElements branch, ~line 44)
- Test: `internal/printer/printer_test.go`, `internal/wsnorm/wsnorm_test.go`

**Interfaces:**
- Consumes: existing `p.fragment(*ast.Fragment)` (`printer.go:544`), existing `normalizeMarkup`, Task 2's parser output.
- Produces: `gsx fmt` round-trips an expression-position fragment; wsnorm normalizes its children. Both consumed by the corpus (Tasks 4–5), which pins fmt + render goldens.

- [ ] **Step 1: Write the failing printer test**

Add to `internal/printer/printer_test.go` (mirror the existing GoWithElements/element-value test style in that file — parse source, print, compare):

```go
func TestPrintFragmentExpressionValue(t *testing.T) {
	src := "package v\n\nvar list = <><a/><b/></>\n"
	got := formatForTest(t, src) // reuse the file's existing parse→print helper
	want := "package v\n\nvar list = <><a/><b/></>\n"
	if got != want {
		t.Fatalf("fmt round-trip:\n got: %q\nwant: %q", got, want)
	}
}
```

(If the file's helper is named differently, use it; do not add a new formatting harness.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/printer -run TestPrintFragmentExpressionValue`
Expected: FAIL — `goWithElements`'s `default` arm returns `p.fail("printer: unknown Go-expression part type *ast.Fragment")`.

- [ ] **Step 3: Add the printer fragment case**

In `internal/printer/printer.go`, `goWithElements`, add a case beside `case *ast.Element:`:

```go
		case *ast.Fragment:
			docs = append(docs, p.fragment(pt))
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/printer -run TestPrintFragmentExpressionValue`
Expected: PASS.

- [ ] **Step 5: Write the failing wsnorm test**

Add to `internal/wsnorm/wsnorm_test.go` (mirror existing GoWithElements normalization tests): parse `var x = <>  <a>{ v }</a>  </>`, run `Normalize`, assert the fragment's inter-element whitespace text nodes are collapsed/dropped exactly as a component-body fragment would be (compare against the same markup normalized in a component body).

- [ ] **Step 6: Run test to verify it fails**

Run: `go test ./internal/wsnorm -run TestNormalizeFragmentExpressionValue`
Expected: FAIL — the GoWithElements branch only matches `*ast.Element`, so the fragment's children are never normalized.

- [ ] **Step 7: Add the wsnorm fragment handling**

In `internal/wsnorm/wsnorm.go`, the `*ast.GoWithElements` branch, generalize the part loop to also normalize fragments (a fragment has no tag, so preserve=false):

```go
		case *ast.GoWithElements:
			for _, part := range v.Parts {
				switch p := part.(type) {
				case *ast.Element:
					// Mirrors normalizeMarkup's own *ast.Element case: a
					// Go-embedded element starts a fresh (preserve=false)
					// context, same as a top-level component body element.
					p.Children = normalizeMarkup(p.Children, isPreserveTag(p.Tag))
					normalizeAttrs(p.Attrs)
				case *ast.Fragment:
					// A fragment has no wrapper tag; its children normalize in a
					// fresh (preserve=false) context, same as a body fragment.
					p.Children = normalizeMarkup(p.Children, false)
				}
			}
```

- [ ] **Step 8: Run test to verify it passes**

Run: `go test ./internal/wsnorm -run TestNormalizeFragmentExpressionValue`
Expected: PASS.

- [ ] **Step 9: Run both suites (no regression)**

Run: `go test ./internal/printer ./internal/wsnorm`
Expected: PASS.

- [ ] **Step 10: Commit**

```bash
git add internal/printer/printer.go internal/printer/printer_test.go internal/wsnorm/wsnorm.go internal/wsnorm/wsnorm_test.go
git commit -m "feat(fmt): print + wsnorm fragments in Go-expression position"
```

---

### Task 4: Codegen — emit + probe fragment values (first corpus case)

This is the emit≡probe gate: the corpus case renders end-to-end only if BOTH the emitter and the type-check skeleton handle a fragment part.

**Files:**
- Modify: `internal/codegen/emit.go` (`emitElementValue` ~line 635; GoWithElements loop ~line 232)
- Modify: `internal/codegen/analyze.go` (GoWithElements probe loop ~line 495; `harvestEmbeddedElements` ~line 1915; `buildSkeleton` return type ~line 336; return statement ~line 620)
- Modify: `internal/codegen/module_importer.go` (`gwElementsByXGo` map ~line 751; assignment ~line 825; `harvestEmbeddedElements` call ~line 1068)
- Create: `internal/corpus/testdata/cases/fragment-literals/var.txtar`, `internal/corpus/testdata/cases/fragment-literals/empty-nop.txtar`

**Interfaces:**
- Consumes: `emitNodeFuncBody(b, nodes []ast.Markup, …)` (`emit.go:603`), `emitProbes(…, []gsxast.Markup, …)`, `harvestBody(body, []gsxast.Markup, …)`, `Fragment.Children []ast.Markup`.
- Produces: a `*ast.Fragment` GoWithElements part lowers to `gsx.Func(func(ctx, _gsxw){ …children… })` and type-checks. No new public interface.

- [ ] **Step 1: Extract the shared node-value emitter**

In `internal/codegen/emit.go`, refactor `emitElementValue` so the closure-wrapping body is a shared helper keyed on a markup list. Rename the current body into `emitNodeValue(b *bytes.Buffer, nodes []ast.Markup, …same trailing params…) bool`:

```go
// emitNodeValue wraps a markup list as a self-contained gsx.Node value — the
// inline `gsx.Func(func(ctx, w){ … })` closure element and fragment literals
// share. Reuses emitNodeFuncBody (the SAME lowering a component body gets).
func emitNodeValue(b *bytes.Buffer, nodes []ast.Markup, currentPkg *types.Package, resolved map[ast.Node]types.Type, table filterTable, structFields, nodeProps, attrsProps map[string]map[string]bool, byo *byoData, imports map[string]bool, importAliases map[string]string, boundNames map[string]string, typeArgAliases map[string]string, interpTemp *int, fset *token.FileSet, cls *attrclass.Classifier, fm FieldMatcher, bag *diag.Bag, mergeExpr string) bool {
	b.WriteString("gsx.Func(func(ctx context.Context, _gsxw io.Writer) error {\n")
	if !emitNodeFuncBody(b, nodes, currentPkg, resolved, table, structFields, nodeProps, attrsProps, byo, imports, importAliases, boundNames, typeArgAliases, interpTemp, fset, "", "", cls, fm, bag, mergeExpr) {
		return false
	}
	b.WriteString("\t})")
	return true
}

func emitElementValue(b *bytes.Buffer, el *ast.Element, /* …same params… */) bool {
	return emitNodeValue(b, []ast.Markup{el}, /* …forward… */)
}

func emitFragmentValue(b *bytes.Buffer, fr *ast.Fragment, /* …same params… */) bool {
	// Empty <></> → an empty markup list → emitNodeFuncBody writes nothing →
	// the closure is the uniform no-op nop (renders nothing).
	return emitNodeValue(b, fr.Children, /* …forward… */)
}
```

Keep `emitElementValue`'s existing doc comment about carrying no `emitLine` of its own; move it to `emitNodeValue`.

- [ ] **Step 2: Dispatch the fragment part in the GoWithElements loop**

In `emit.go`'s `*ast.GoWithElements` case, beside `case *ast.Element:`:

```go
				case *ast.Fragment:
					if !emitFragmentValue(&wbuf, p, currentPkg, resolved, table, structFields, nodeProps, attrsProps, byo, imports, importAliases, boundNames, typeArgAliases, &interpTemp, fset, cls, fm, bag, mergeExpr) {
						partsOK = false
					}
```

- [ ] **Step 3: Generalize the probe list to markup lists (analyze.go)**

The embedded-element probe currently keys one `*Element` per IIFE. A fragment probes its whole children list. Change the collection from `[]*Element` to `[][]Markup` (each entry = the markup a single IIFE probes/harvests).

In `buildSkeleton` (`analyze.go`), replace `var gwElements []*gsxast.Element` and its loop body:

```go
	var gwMarkups [][]gsxast.Markup
	for _, d := range file.Decls {
		we, ok := d.(*gsxast.GoWithElements)
		if !ok {
			continue
		}
		for _, part := range we.Parts {
			switch p := part.(type) {
			case gsxast.GoText:
				emitSkeletonBlockLine(&compBuf, fset, p.Pos())
				compBuf.WriteString(p.Src)
			case *gsxast.Element:
				markup := []gsxast.Markup{p}
				compBuf.WriteString("func() _gsxrt.Node {\n")
				fmt.Fprintf(&compBuf, "_gsxelem(%d)\n", len(gwMarkups))
				compBuf.WriteString("var ctx _gsxctx.Context\n_ = ctx\n")
				if err := emitProbes(&compBuf, markup, table, propFields, nodeProps, attrsProps, combinedSigs, byo, fm, "", "", usedFilters, fset, ctrlOff, registry, bag); err != nil {
					return "", nil, nil, nil, nil, nil, err
				}
				compBuf.WriteString("return nil\n}()")
				gwMarkups = append(gwMarkups, markup)
			case *gsxast.Fragment:
				// A fragment probes its children list (empty <></> → no probes,
				// still a valid _gsxrt.Node-returning IIFE — the nop).
				compBuf.WriteString("func() _gsxrt.Node {\n")
				fmt.Fprintf(&compBuf, "_gsxelem(%d)\n", len(gwMarkups))
				compBuf.WriteString("var ctx _gsxctx.Context\n_ = ctx\n")
				if err := emitProbes(&compBuf, p.Children, table, propFields, nodeProps, attrsProps, combinedSigs, byo, fm, "", "", usedFilters, fset, ctrlOff, registry, bag); err != nil {
					return "", nil, nil, nil, nil, nil, err
				}
				compBuf.WriteString("return nil\n}()")
				gwMarkups = append(gwMarkups, p.Children)
			default:
				return "", nil, nil, nil, nil, nil, fmt.Errorf("codegen: unsupported Go-expression part %T", part)
			}
		}
		compBuf.WriteString("\n")
	}
```

Update `buildSkeleton`'s signature 7th return type from `[]*gsxast.Element` to `[][]gsxast.Markup`, and the final `return sb.String(), comps, imports, ctrlOff, registry, gwMarkups, nil`.

- [ ] **Step 4: Harvest a markup list per IIFE**

Change `harvestEmbeddedElements`'s param and inner call:

```go
func harvestEmbeddedElements(f *goast.File, markups [][]gsxast.Markup, info *types.Info, out map[gsxast.Node]types.Type, exprOut map[gsxast.Node]goast.Expr, registry *inferRegistry) {
	if len(markups) == 0 {
		return
	}
	goast.Inspect(f, func(node goast.Node) bool {
		// …unchanged marker-matching down to idx…
		if err != nil || idx < 0 || idx >= len(markups) {
			return true
		}
		harvestBody(fl.Body, markups[idx], info, out, exprOut, registry)
		return true
	})
}
```

Update the doc comment to say "each embedded value's markup list" rather than "single embedded element".

- [ ] **Step 5: Follow the type change in module_importer.go**

- `gwElementsByXGo := map[string][]*gsxast.Element{}` → `map[string][][]gsxast.Markup{}` (rename to `gwMarkupsByXGo` for clarity).
- The `buildSkeleton(...)` call's `gwElements` receiver → `gwMarkups`; the assignment `gwElementsByXGo[absXpath] = gwElements` → `gwMarkupsByXGo[absXpath] = gwMarkups`.
- The `harvestEmbeddedElements(gf, gwElementsByXGo[fname], …)` call → `gwMarkupsByXGo[fname]`.

- [ ] **Step 6: Write the first corpus case — `var.txtar`**

Create `internal/corpus/testdata/cases/fragment-literals/var.txtar`:

```
-- input.gsx --
package views

var pair = <><span>one</span><span>two</span></>

component Host() {
	{ pair }
}
-- invoke --
Host(HostProps{})
-- render.golden --
<span>one</span><span>two</span>
```

(Leave `generated.x.go.golden` and `render.golden` to be produced by `-update`; the `render.golden` above is the expected result to confirm after generating.)

- [ ] **Step 7: Write the nop case — `empty-nop.txtar`**

Create `internal/corpus/testdata/cases/fragment-literals/empty-nop.txtar`:

```
-- input.gsx --
package views

var nothing = <></>

component Host() {
	<div>{ nothing }</div>
}
-- invoke --
Host(HostProps{})
-- render.golden --
<div></div>
```

- [ ] **Step 8: Generate goldens and verify**

Run:
```bash
go test ./internal/corpus -run TestCorpus -update
go test ./internal/corpus -run TestCorpus
```
Expected: first regenerates `generated.x.go.golden`, `render.golden`, and `coverage.golden`; second PASSES with no diff. Inspect `fragment-literals/var/generated.x.go.golden` — the `pair` initializer must be `gsx.Func(func(ctx context.Context, _gsxw io.Writer) error { …two spans… })`; `empty-nop`'s `nothing` must be a `gsx.Func` whose body writes nothing before `return`.

- [ ] **Step 9: Full codegen + package build**

Run: `go build ./... && go test ./internal/codegen`
Expected: PASS.

- [ ] **Step 10: Commit**

```bash
git add internal/codegen/emit.go internal/codegen/analyze.go internal/codegen/module_importer.go internal/corpus/testdata/cases/fragment-literals/ internal/corpus/testdata/coverage.golden
git commit -m "feat(codegen): lower <>…</> fragments to gsx.Func node values"
```

---

### Task 5: Corpus — full context + scope coverage

Covers the remaining Go-expression contexts AND the scope sub-contexts the element-literals adversarial review proved necessary (func-local, method-receiver). One case = one context.

**Files:**
- Create under `internal/corpus/testdata/cases/fragment-literals/`:
  - `return.txtar` — `return <>…</>` from a function returning `gsx.Node`.
  - `call-arg.txtar` — fragment as a call argument (e.g. a `RenderComponent`-style helper defined in the fixture).
  - `struct-field.txtar` — fragment as a struct-literal field of type `gsx.Node`.
  - `loop-list.txtar` — `<>{ for _, s := range items { <li>{ s }</li> } }</>` — the "return a list" driver.
  - `interp-func-local.txtar` — fragment child interps a **function parameter/local**, proving scope capture (emit≡probe).
  - `interp-method-receiver.txtar` — fragment child interps a **method receiver field**, the second scope sub-context.

**Interfaces:**
- Consumes: Tasks 2–4 (parser + codegen). No new interface.

- [ ] **Step 1: Write `return.txtar`**

```
-- input.gsx --
package views

func Panel() gsx.Node {
	return <><h1>Title</h1><p>Body</p></>
}

component Host() {
	{ Panel() }
}
-- invoke --
Host(HostProps{})
-- render.golden --
<h1>Title</h1><p>Body</p>
```

- [ ] **Step 2: Write `loop-list.txtar` (the list driver)**

```
-- input.gsx --
package views

func Items(xs []string) gsx.Node {
	return <>{ for _, s := range xs { <li>{ s }</li> } }</>
}

component Host() {
	<ul>{ Items([]string{"a", "b"}) }</ul>
}
-- invoke --
Host(HostProps{})
-- render.golden --
<ul><li>a</li><li>b</li></ul>
```

- [ ] **Step 3: Write `interp-func-local.txtar` (scope-capture regression)**

```
-- input.gsx --
package views

func Greeting(name string) gsx.Node {
	return <><span>Hello </span><span>{ name }</span></>
}

component Host() {
	{ Greeting("world") }
}
-- invoke --
Host(HostProps{})
-- render.golden --
<span>Hello </span><span>world</span>
```

This case MUST generate without an `undefined: name` type-check error — that is the emit≡probe scope requirement. If generation fails here, the IIFE probe is not capturing the enclosing func's params (see Task 4 Step 3).

- [ ] **Step 4: Write `interp-method-receiver.txtar`**

```
-- input.gsx --
package views

type Card struct {
	Title string
}

func (c Card) View() gsx.Node {
	return <><h2>{ c.Title }</h2></>
}

component Host() {
	{ Card{Title: "Hi"}.View() }
}
-- invoke --
Host(HostProps{})
-- render.golden --
<h2>Hi</h2>
```

- [ ] **Step 5: Write `call-arg.txtar` and `struct-field.txtar`**

`call-arg.txtar` — define a fixture helper `func wrap(n gsx.Node) gsx.Node { return n }` and use `{ wrap(<><b/></>) }`. `struct-field.txtar` — define `type slot struct{ Body gsx.Node }` and initialize `slot{Body: <><i/></>}`, render `{ s.Body }`. Both follow the render-golden pattern above.

- [ ] **Step 6: Generate and verify all cases**

Run:
```bash
go test ./internal/corpus -run TestCorpus -update
go test ./internal/corpus -run TestCorpus
```
Expected: all six new cases render as pinned above; `coverage.golden` updated; second run clean. Confirm `interp-func-local` and `interp-method-receiver` actually produced generated output (a codegen failure would show as an empty/absent `generated.x.go.golden` and a diagnostics entry).

- [ ] **Step 7: Commit**

```bash
git add internal/corpus/testdata/cases/fragment-literals/ internal/corpus/testdata/coverage.golden
git commit -m "test(corpus): fragment literals across contexts + scope sub-contexts"
```

---

### Task 6: Docs + sibling grammars

**Files:**
- Modify: `docs/guide/syntax/elements.md` (or wherever the "Elements as values" section landed — grep for it) and/or `docs/guide/syntax/raw-go.md`.
- Modify: `docs/ROADMAP.md`.
- Modify: `/Users/jackieli/.claude/projects/-Users-jackieli-personal-gsxhq-gsx/memory/gsx-element-literals-shipped.md` (the "Known limitation: Fragments `<>` in expr position are errored (v1)" line — mark it closed, link the new work).
- Verify (and update if gated): `../tree-sitter-gsx`, `../vscode-gsx`, `../gsxhq.github.io` CodeMirror + VitePress syntax.

**Interfaces:** none (docs).

- [ ] **Step 1: Extend the guide**

In the "Elements as values" section, add a "Fragments as values" subsection: `<>…</>` yields a `gsx.Node` with no wrapper element; `<></>` renders nothing (the nop / `templ.NopComponent` equivalent); it works in every expression position elements do; multiple siblings must be wrapped (bare `<A/><B/>` is not allowed); fragments take no attributes. Include a verified example copied from a corpus case's `input.gsx` (e.g. `loop-list`), and note `gsx.Fragment(...)` as the Go-side runtime equivalent. Wrap any literal `{{ }}` in `::: v-pre` if editing `docs/guide/**`.

- [ ] **Step 2: Update ROADMAP + memory**

`docs/ROADMAP.md`: mark expression-position fragments done; remove/annotate the element-literals "fragments deferred" limitation. In the memory file, edit the Known-limitation line to record fragments-in-expr shipped.

- [ ] **Step 3: Verify example correctness**

Run: `go run ./cmd/gsx fmt -l docs` (or the repo's docs-example check) and confirm any new fenced `.gsx` examples match a corresponding corpus case verbatim. Do NOT ship an example that isn't backed by a passing corpus case.

- [ ] **Step 4: Sibling grammar check**

For each of `../tree-sitter-gsx`, `../vscode-gsx`, `../gsxhq.github.io` (CodeMirror + VitePress): confirm `<></>` in expression position highlights correctly. If the grammar gates fragments to markup/body context only, extend it; if fragments already highlight context-free, note "no change needed" in the commit message. (These are separate repos — commit there separately per their own conventions.)

- [ ] **Step 5: Commit**

```bash
git add docs/
git commit -m "docs: fragments as values in Go-expression position"
```

---

## Self-Review

- **Spec coverage:** every spec section maps to a task — AST GoPart (T1), parser error→lowering (T2), printer/wsnorm (T3), emit+analyze lowering & empty-nop uniform closure (T4), per-context + scope corpus (T5), docs + sibling grammars (T6). The "explicit wrapping required" and "no attrs" rules are enforced by the existing grammar/scanner and asserted in T2/T5. ✓
- **Placeholder scan:** no TBD/TODO; every code step shows real code or exact edits; golden files are generated (not hand-written) per project rule, with the expected `render.golden` pinned for confirmation. ✓
- **Type consistency:** `emitNodeValue`/`emitFragmentValue` signatures mirror `emitElementValue`; the `[]*Element` → `[][]Markup` change is threaded through `buildSkeleton` return, `harvestEmbeddedElements` param, and `module_importer.go` map + call site (all three named). `Fragment.Children` is `[]ast.Markup`, matching `emitNodeFuncBody`/`emitProbes`/`harvestBody` param types. ✓
- **emit≡probe:** T4 changes emit and analyze together and gates on a rendering corpus case; T5's `interp-func-local`/`interp-method-receiver` are the scope-capture regression tests the element-literals review proved necessary. ✓
