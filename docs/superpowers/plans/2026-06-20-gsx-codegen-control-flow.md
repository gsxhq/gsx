# gsx Codegen — Control Flow Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Lower `{ for }`, `{ if/else }`, `{ switch }`, `{{ }}`, and `<>…</>` fragments to Go in the generated `.x.go`, including correct type resolution of interpolations that reference loop variables and block-locals.

**Architecture:** Control flow lowers to ordinary Go statements wrapping the child writes (`for c { gw.S(…) }`). The one wrinkle: an interpolation inside a control body (e.g. `{ for _, it := range items { <li>{it.Name}</li> } }`) references `it`, which only exists inside the loop. So the type-resolution **probe must mirror the control structure** — emit the real `for`/`if`/`switch` (+ `{{ }}` code) around the nested `_gsxuse(expr)` probes — and `harvest`/`collectInterps`/`emitProbes` must recurse through control nodes in identical source order so the k-th probe still aligns with the k-th interpolation.

**Tech Stack:** Go 1.26.1. `internal/codegen` (`go/packages`, `go/types`, `go/ast`, `go/format`). Runtime target `github.com/gsxhq/gsx` (stdlib-only).

## Global Constraints

- Runtime stays stdlib-only; generator may use `golang.org/x/tools`. No runtime reflection / no `any` in generated code.
- Generated output is `go/format`-ed, so **emitted indentation is cosmetic** — correctness comes from braces/structure; `format.Source` reindents.
- Control-flow clauses/conditions/tags and `{{ }}` bodies are emitted **verbatim** as Go (they reference params-as-locals, ambient `ctx`, and loop vars introduced by the clause). gsx switch cases are independent (Go's no-fallthrough default matches gsx semantics).
- gofmt + go vet clean. Each task ends with a **render golden** (`renderPackage` compiles + runs the generated code and asserts HTML semantically) plus `go test ./...` green.
- This is codegen phase-2 "control flow" (`docs/ROADMAP.md`). Out of scope here: attributes, child-component props, pipeline, methods. Unsupported constructs still error clearly.
- **Order invariant (load-bearing):** `collectInterps`, `emitProbes`, and `genNode` MUST traverse a component body in the SAME depth-first source order (children in order; `if` Then before Else; switch cases in order), so the k-th `_gsxuse` in the probe maps to the k-th interpolation. Task 1 builds `collectInterps`/`emitProbes` for ALL control constructs at once (lockstep); `genNode` emission grows per task.

---

## File Structure (modified)

- `internal/codegen/analyze.go` — `collectInterps` (recurse control nodes), `emitProbes` (mirror control structure + `{{ }}` code), `harvest` (recurse via `ast.Inspect`).
- `internal/codegen/emit.go` — `genNode` gains `*ast.Fragment`, `*ast.ForMarkup`, `*ast.IfMarkup`, `*ast.SwitchMarkup`, `*ast.GoBlock` cases.
- `internal/codegen/e2e_test.go` — render goldens per construct.

---

### Task 1: Resolution infrastructure + `{ for }` + fragments

**Files:**
- Modify: `internal/codegen/analyze.go` (`collectInterps`, `emitProbes`, `harvest`)
- Modify: `internal/codegen/emit.go` (`genNode`)
- Test: `internal/codegen/e2e_test.go`

**Interfaces:**
- Consumes: existing `genNode`, `emitS`, `genInterp`, `isComponentTag`; AST `ForMarkup{Clause string; Body []Markup}`, `IfMarkup{Cond string; Then,Else []Markup}`, `SwitchMarkup{Tag string; Cases []*CaseClause}`, `CaseClause{List string; Default bool; Body []Markup}`, `GoBlock{Code string}`, `Fragment{Children []Markup}`.
- Produces: `collectInterps`/`emitProbes` recurse all control constructs; `harvest` recurses; `genNode` emits `Fragment` and `ForMarkup`.

- [ ] **Step 1: Write the failing render test**

Add to `internal/codegen/e2e_test.go`:

```go
func TestRenderForLoop(t *testing.T) {
	files := map[string]string{
		"model.go": `package views

type Item struct {
	Name  string
	Count int
}
`,
		"views.gsx": `package views

component List(items []Item) {
	<ul>{ for _, it := range items { <li>{it.Name}: {it.Count}</li> } }</ul>
}
`,
	}
	got := renderPackage(t, files,
		`p.List(p.ListProps{Items: []p.Item{{Name: "a", Count: 1}, {Name: "b", Count: 2}}})`)
	assertHTMLEqual(t, got, "<ul><li>a: 1</li><li>b: 2</li></ul>")
}

func TestRenderFragment(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

component Pair(a string, b string) {
	<><span>{a}</span><span>{b}</span></>
}
`,
	}
	got := renderPackage(t, files, `p.Pair(p.PairProps{A: "x", B: "y"})`)
	assertHTMLEqual(t, got, "<span>x</span><span>y</span>")
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/codegen/ -run 'TestRenderForLoop|TestRenderFragment' -v`
Expected: FAIL — `TestRenderForLoop` fails at resolution (`{it.Name}` references `it`, undefined at the probe's top level) or emission (`unsupported markup node *ast.ForMarkup`); `TestRenderFragment` fails with `unsupported markup node *ast.Fragment`.

- [ ] **Step 3: Make `collectInterps` recurse control constructs**

In `internal/codegen/analyze.go`, replace `collectInterps` with:

```go
// collectInterps gathers interpolation nodes in depth-first source order — the
// SAME order emitProbes emits probes and genNode emits writes, so the k-th probe
// aligns with the k-th interpolation.
func collectInterps(nodes []gsxast.Markup, out *[]*gsxast.Interp) {
	for _, n := range nodes {
		switch t := n.(type) {
		case *gsxast.Interp:
			*out = append(*out, t)
		case *gsxast.Element:
			if !isComponentTag(t.Tag) {
				collectInterps(t.Children, out)
			}
		case *gsxast.Fragment:
			collectInterps(t.Children, out)
		case *gsxast.ForMarkup:
			collectInterps(t.Body, out)
		case *gsxast.IfMarkup:
			collectInterps(t.Then, out)
			collectInterps(t.Else, out)
		case *gsxast.SwitchMarkup:
			for _, cc := range t.Cases {
				collectInterps(cc.Body, out)
			}
		// *gsxast.GoBlock: no interpolations (its body is opaque Go).
		}
	}
}
```

- [ ] **Step 4: Make `emitProbes` mirror control structure**

In `internal/codegen/analyze.go`, replace `emitProbes` with:

```go
// emitProbes writes type-resolution probes for a component body. It MIRRORS the
// control structure (real for/if/switch + {{ }} code) so interpolations that
// reference loop vars / block-locals type-check in scope. Each interpolation is
// `_gsxuse(expr)`; child components are `_ = Child(ChildProps{})`.
func emitProbes(sb *strings.Builder, nodes []gsxast.Markup) {
	for _, n := range nodes {
		switch t := n.(type) {
		case *gsxast.Interp:
			fmt.Fprintf(sb, "_gsxuse(%s)\n", strings.TrimSpace(t.Expr))
		case *gsxast.Element:
			if isComponentTag(t.Tag) {
				fmt.Fprintf(sb, "_ = %s(%sProps{})\n", t.Tag, t.Tag)
			} else {
				emitProbes(sb, t.Children)
			}
		case *gsxast.Fragment:
			emitProbes(sb, t.Children)
		case *gsxast.ForMarkup:
			fmt.Fprintf(sb, "for %s {\n", t.Clause)
			emitProbes(sb, t.Body)
			sb.WriteString("}\n")
		case *gsxast.IfMarkup:
			fmt.Fprintf(sb, "if %s {\n", t.Cond)
			emitProbes(sb, t.Then)
			sb.WriteString("}")
			if t.Else != nil {
				sb.WriteString(" else {\n")
				emitProbes(sb, t.Else)
				sb.WriteString("}")
			}
			sb.WriteString("\n")
		case *gsxast.SwitchMarkup:
			fmt.Fprintf(sb, "switch %s {\n", t.Tag)
			for _, cc := range t.Cases {
				if cc.Default {
					sb.WriteString("default:\n")
				} else {
					fmt.Fprintf(sb, "case %s:\n", cc.List)
				}
				emitProbes(sb, cc.Body)
			}
			sb.WriteString("}\n")
		case *gsxast.GoBlock:
			sb.WriteString(t.Code)
			sb.WriteString("\n")
		}
	}
}
```

(Indentation in the probe is irrelevant — it's parsed, not gofmt-compared.)

- [ ] **Step 5: Make `harvest` recurse**

In `internal/codegen/analyze.go`, replace the inner statement loop of `harvest` (the `for _, stmt := range fd.Body.List { … }`) with an `ast.Inspect` walk that collects `_gsxuse` call-argument types in source order:

```go
		interps := componentInterps(c)
		k := 0
		goast.Inspect(fd.Body, func(node goast.Node) bool {
			call, ok := node.(*goast.CallExpr)
			if !ok {
				return true
			}
			id, ok := call.Fun.(*goast.Ident)
			if !ok || id.Name != "_gsxuse" || len(call.Args) != 1 {
				return true
			}
			if k < len(interps) {
				out[interps[k]] = info.Types[call.Args[0]].Type
				k++
			}
			return true
		})
```

(The rest of `harvest` — the `byName` map and the `FuncDecl`/component matching — is unchanged. `goast` is the existing `go/ast` import alias.)

- [ ] **Step 6: Emit `Fragment` and `ForMarkup` in `genNode`**

In `internal/codegen/emit.go`, add these cases to `genNode`'s type switch (before the `default`):

```go
	case *ast.Fragment:
		for _, c := range t.Children {
			if err := genNode(b, c, resolved, imports, fset); err != nil {
				return err
			}
		}
	case *ast.ForMarkup:
		emitLine(b, fset, t.Pos())
		fmt.Fprintf(b, "for %s {\n", t.Clause)
		for _, c := range t.Body {
			if err := genNode(b, c, resolved, imports, fset); err != nil {
				return err
			}
		}
		b.WriteString("}\n")
```

(Match `genNode`'s actual signature — it threads `resolved`, `imports`, `fset`; copy the exact parameter names from the existing `*ast.Element` case.)

- [ ] **Step 7: Run to verify pass**

Run: `go test ./internal/codegen/ -run 'TestRenderForLoop|TestRenderFragment' -v`
Expected: PASS. Also run the FULL existing suite — the harvest/collectInterps/emitProbes changes must not break any phase-1 test: `go test ./internal/codegen/`.

- [ ] **Step 8: Run all + commit**

Run: `go test ./... && go vet ./... && gofmt -l internal/codegen/`
Expected: green.

```bash
git add internal/codegen/
git commit -m "codegen: control-flow resolution infra (probe mirrors structure) + { for } + fragments"
```

---

### Task 2: `{ if / else if / else }`

**Files:**
- Modify: `internal/codegen/emit.go` (`genNode`)
- Test: `internal/codegen/e2e_test.go`

**Interfaces:**
- Consumes: the resolution infra from Task 1 (`IfMarkup` already handled in `emitProbes`/`collectInterps`).
- Produces: `genNode` emits `*ast.IfMarkup`.

- [ ] **Step 1: Write the failing render test**

Add to `internal/codegen/e2e_test.go`:

```go
func TestRenderIf(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

component Status(n int) {
	<p>{ if n > 0 { <span>pos</span> } else if n < 0 { <span>neg</span> } else { <span>zero</span> } }</p>
}
`,
	}
	for _, tc := range []struct {
		n    int
		want string
	}{{1, "<p><span>pos</span></p>"}, {-1, "<p><span>neg</span></p>"}, {0, "<p><span>zero</span></p>"}} {
		got := renderPackage(t, files, fmt.Sprintf(`p.Status(p.StatusProps{N: %d})`, tc.n))
		assertHTMLEqual(t, got, tc.want)
	}
}
```

(`fmt` is already imported in `e2e_test.go`.)

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/codegen/ -run TestRenderIf -v`
Expected: FAIL — `unsupported markup node *ast.IfMarkup`.

- [ ] **Step 3: Emit `IfMarkup` in `genNode`**

In `internal/codegen/emit.go`, add to `genNode`'s switch:

```go
	case *ast.IfMarkup:
		emitLine(b, fset, t.Pos())
		fmt.Fprintf(b, "if %s {\n", t.Cond)
		for _, c := range t.Then {
			if err := genNode(b, c, resolved, imports, fset); err != nil {
				return err
			}
		}
		b.WriteString("}")
		if t.Else != nil {
			b.WriteString(" else {\n")
			for _, c := range t.Else {
				if err := genNode(b, c, resolved, imports, fset); err != nil {
					return err
				}
			}
			b.WriteString("}")
		}
		b.WriteString("\n")
```

(An `else if` arrives as `Else = []Markup{*IfMarkup}`, so this emits `else { if … }` — valid Go that `format.Source` keeps; semantics are identical.)

- [ ] **Step 4: Run to verify pass + all + commit**

Run: `go test ./internal/codegen/ -run TestRenderIf -v` (PASS), then `go test ./... && go vet ./... && gofmt -l internal/codegen/`.

```bash
git add internal/codegen/
git commit -m "codegen: { if / else if / else } emission"
```

---

### Task 3: `{ switch / case / default }`

**Files:**
- Modify: `internal/codegen/emit.go` (`genNode`)
- Test: `internal/codegen/e2e_test.go`

**Interfaces:**
- Consumes: Task 1 infra (`SwitchMarkup`/`CaseClause` handled in `emitProbes`/`collectInterps`).
- Produces: `genNode` emits `*ast.SwitchMarkup`.

- [ ] **Step 1: Write the failing render test**

Add to `internal/codegen/e2e_test.go`:

```go
func TestRenderSwitch(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

component Badge(kind string) {
	<span>{ switch kind {
	case "warn":
		<b>warning</b>
	case "err":
		<b>error</b>
	default:
		<b>info</b>
	} }</span>
}
`,
	}
	for _, tc := range []struct{ kind, want string }{
		{"warn", "<span><b>warning</b></span>"},
		{"err", "<span><b>error</b></span>"},
		{"other", "<span><b>info</b></span>"},
	} {
		got := renderPackage(t, files, fmt.Sprintf(`p.Badge(p.BadgeProps{Kind: %q})`, tc.kind))
		assertHTMLEqual(t, got, tc.want)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/codegen/ -run TestRenderSwitch -v`
Expected: FAIL — `unsupported markup node *ast.SwitchMarkup`.

- [ ] **Step 3: Emit `SwitchMarkup` in `genNode`**

In `internal/codegen/emit.go`, add to `genNode`'s switch:

```go
	case *ast.SwitchMarkup:
		emitLine(b, fset, t.Pos())
		fmt.Fprintf(b, "switch %s {\n", t.Tag)
		for _, cc := range t.Cases {
			if cc.Default {
				b.WriteString("default:\n")
			} else {
				fmt.Fprintf(b, "case %s:\n", cc.List)
			}
			for _, c := range cc.Body {
				if err := genNode(b, c, resolved, imports, fset); err != nil {
					return err
				}
			}
		}
		b.WriteString("}\n")
```

(`t.Tag` may be empty for a tagless `switch {`; `switch  {` formats fine. Go switch cases don't fall through, matching gsx.)

- [ ] **Step 4: Run to verify pass + all + commit**

Run: `go test ./internal/codegen/ -run TestRenderSwitch -v` (PASS), then `go test ./... && go vet ./... && gofmt -l internal/codegen/`.

```bash
git add internal/codegen/
git commit -m "codegen: { switch / case / default } emission"
```

---

### Task 4: `{{ }}` Go-statement blocks

**Files:**
- Modify: `internal/codegen/emit.go` (`genNode`)
- Test: `internal/codegen/e2e_test.go`

**Interfaces:**
- Consumes: Task 1 infra (`GoBlock` code already emitted into probes for scope).
- Produces: `genNode` emits `*ast.GoBlock`.

- [ ] **Step 1: Write the failing render test**

Add to `internal/codegen/e2e_test.go`:

```go
func TestRenderGoBlock(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

component Chip(first string, last string) {
	<div>{{ full := first + " " + last }}<span>{full}</span></div>
}
`,
	}
	got := renderPackage(t, files, `p.Chip(p.ChipProps{First: "Ada", Last: "Lovelace"})`)
	assertHTMLEqual(t, got, "<div><span>Ada Lovelace</span></div>")
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/codegen/ -run TestRenderGoBlock -v`
Expected: FAIL — `unsupported markup node *ast.GoBlock` (the `{{ }}` block is dropped, so `full` is undefined → resolution or emission error).

- [ ] **Step 3: Emit `GoBlock` in `genNode`**

In `internal/codegen/emit.go`, add to `genNode`'s switch:

```go
	case *ast.GoBlock:
		emitLine(b, fset, t.Pos())
		b.WriteString(t.Code)
		b.WriteString("\n")
```

(`t.Code` is the verbatim Go statements between `{{` and `}}`; emitted in source order, so locals it declares are in scope for later writes.)

- [ ] **Step 4: Run to verify pass + all + commit**

Run: `go test ./internal/codegen/ -run TestRenderGoBlock -v` (PASS), then `go test ./... && go vet ./... && gofmt -l internal/codegen/`.

```bash
git add internal/codegen/
git commit -m "codegen: {{ }} Go-statement block emission"
```

---

## Self-Review

**1. Spec coverage** (codegen design + roadmap "control flow"):
- `{ for }` → Task 1. ✓  `<>…</>` fragments → Task 1. ✓
- `{ if/else if/else }` → Task 2. ✓
- `{ switch/case/default }` (incl. tagless) → Task 3. ✓
- `{{ }}` → Task 4. ✓
- Interpolations inside control bodies (loop vars, block-locals, `{{ }}` locals) resolve → Task 1's probe-mirroring + recursion. ✓
- Order invariant preserved (collectInterps/emitProbes/harvest all recurse identically) → Task 1. ✓

**2. Placeholder scan:** No TBD/"handle edge cases". Step instructions reference exact functions and show complete code.

**3. Type/signature consistency:**
- `collectInterps`/`emitProbes`/`harvest` all built for the full construct set in Task 1; `genNode` cases added incrementally (Tasks 1–4) — but `emitProbes`/`collectInterps` already handle if/switch/goblock from Task 1, so Tasks 2–4 only add `genNode` emission (no resolution gap). ✓
- `genNode`'s recursive calls use the same `(b, node, resolved, imports, fset)` signature throughout (the implementer must copy the exact parameter list from the existing `*ast.Element` case — flagged in Task 1 Step 6). ✓
- `ast.Fragment/ForMarkup/IfMarkup/SwitchMarkup/CaseClause/GoBlock` field names match `ast/ast.go` (Clause/Body, Cond/Then/Else, Tag/Cases, List/Default/Body, Code, Children). ✓

---

## Execution Notes for the Controller

- Tasks are sequential 1→4. Task 1 is the largest (it builds the resolution infra that 2–4 rely on) and must stay green. Tasks 2–4 are small (one `genNode` case + a render golden each).
- The load-bearing risk is the **order invariant** — if `collectInterps`, `emitProbes`, and the recursive `harvest` ever diverge in traversal order, a wrong type maps to a wrong interpolation (silent-wrong). Task 1's review should specifically verify all three traverse identically (children in order; `if` Then-before-Else; switch cases in order; fragments/for bodies recursed; GoBlock contributes no interp).
- Model guidance: Task 1 → standard model (the infra + order invariant need care); Tasks 2–4 → cheap model (mechanical `genNode` cases). Final whole-feature review → most capable model, with an adversarial render-probe pass (nested control flow: `for` containing `if`, `switch` inside a `for`, interps using loop vars two levels deep, a `{{ }}` local used inside a later `for`).
- Branch: build on `main` via a `feat/codegen-control-flow` branch; land via review when all four tasks are green.
