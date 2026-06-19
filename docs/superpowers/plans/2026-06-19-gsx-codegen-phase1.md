# gsx Codegen Phase 1 — Foundation + Full-Type Interpolation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn the validated codegen spike into the real foundation — all type resolution through `go/packages` (no probe shortcut), full `§5` type-aware interpolation, `(T, error)` auto-unwrap, and `//line` source maps.

**Architecture:** `internal/codegen` lowers parsed `.gsx` to `.x.go`. Type resolution loads the whole package via `golang.org/x/tools/go/packages` with a synthesized component **skeleton** injected through `Overlay`; each interpolation is probed as `_gsxuse(expr)` (with `func _gsxuse(...any){}`) so any arity type-checks, and its type is harvested from `TypesInfo`. The emitter chooses precise, type-checked `gsx` runtime calls.

**Tech Stack:** Go 1.26.1. Generator deps: `golang.org/x/tools/go/packages`, `go/types`, `go/format`. Runtime target `github.com/gsxhq/gsx` (stdlib-only).

## Global Constraints

- **Dependency boundary:** the runtime (`github.com/gsxhq/gsx`, module root) stays standard-library only. The generator (`internal/codegen`) may use `golang.org/x/tools`.
- **No runtime reflection, no `any` in generated code** — every emitted call is type-checked by `go build`.
- **One package load per generate** — resolve all of a package's interpolations from a single `go/packages` load.
- **gofmt + go vet clean**; generated output runs through `go/format`. Every task ends green via `go test ./internal/codegen/`.
- **Tests:** source goldens (byte-exact generated `.x.go`) + render goldens (compile & run the generated code, assert HTML **semantically** via the existing `renderPackage`/`assertHTMLEqual` helpers).
- This is **Phase 1** of the codegen design (`2026-06-19-gsx-codegen-design.md`). Out of scope here (later phases): attributes, control flow, method components, child-component props/children, the pipeline `|>`, auto-fallthrough, the CLI. Unsupported constructs must fail with a clear `codegen: … not supported yet` error, never silently.

---

## File Structure (after this plan)

- `internal/codegen/codegen.go` — `GeneratePackage(dir)` orchestration + `.gsx` discovery (the only public entry; the in-memory `Generate`/probe path is removed).
- `internal/codegen/analyze.go` — type resolution: `resolveTypesPkg`, `buildSkeleton`, `emitProbes`, `harvest`, `classify` + the category model, and the analysis helpers (`parseParams`, `usedParams`, `valueIdents`, `collectInterps`, `componentInterps`, `fieldName`).
- `internal/codegen/emit.go` — emission: `generateFile`, `genComponent`, `genNode`, `genInterp`, `genChildComponent`, `emitS`, `writeImports`, `isComponentTag`.
- `internal/codegen/codegen_test.go`, `internal/codegen/e2e_test.go` — tests (existing helpers `renderPackage`, `assertHTMLEqual`, `writeFile`, the `-update` flag).

---

### Task 1: Collapse onto go/packages; arity-safe probe; restructure

Remove the transitional in-memory probe path so all resolution goes through `go/packages`, change the interpolation probe to the arity-safe `_gsxuse(expr)` form, and split the package into `analyze.go` / `emit.go` / `codegen.go`.

**Files:**
- Modify/move: `internal/codegen/codegen.go`, `internal/codegen/pkg.go` → split into `codegen.go`, `analyze.go`, `emit.go`
- Modify: `internal/codegen/codegen_test.go`, `internal/codegen/e2e_test.go`

**Interfaces:**
- Produces: `GeneratePackage(dir string) (map[string][]byte, error)` (unchanged signature); `resolveTypesPkg(dir string, files map[string]*ast.File) (map[*ast.Interp]types.Type, error)`; skeleton helper `func _gsxuse(...any){}` injected into every skeleton; harvest keyed on the `_gsxuse` call.
- Removes: `Generate(*ast.File)`, `resolveTypes` (probe), the `go/importer` import.

- [ ] **Step 1: Move the probe to the arity-safe form (write the failing test)**

Add to `internal/codegen/e2e_test.go`:

```go
// TestRenderTryUnwrapResolves is a forward check that the probe type-checks a
// (T, error) interpolation expression (multi-value), which the old `_ = (expr)`
// probe could not. Full unwrap rendering lands in Task 3; here we only assert
// the package RESOLVES + GENERATES without a type error.
func TestProbeAcceptsMultiValueExpr(t *testing.T) {
	files := map[string]string{
		"helpers.go": `package views

func lookup(k string) (string, error) { return k, nil }
`,
		"views.gsx": `package views

component Label(key string) {
	<span>{lookup(key)}</span>
}
`,
	}
	tmp := t.TempDir()
	repoRoot, _ := filepath.Abs("../..")
	writeFile(t, tmp, "go.mod", "module gsxr\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(tmp, "genpkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for n, c := range files {
		writeFile(t, pkgDir, n, c)
	}
	// Resolution must succeed (the multi-value expr type-checks under _gsxuse);
	// EMISSION may still error "not supported yet" until Task 3 — that is fine.
	_, err := GeneratePackage(pkgDir)
	if err != nil && strings.Contains(err.Error(), "type resolution failed") {
		t.Fatalf("probe failed to type-check a (T,error) expr: %v", err)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/codegen/ -run TestProbeAcceptsMultiValueExpr -v`
Expected: FAIL — `type resolution failed: … multiple-value lookup(key) … in single-value context` (the current `_ = (expr)` probe).

- [ ] **Step 3: Switch the probe to `_gsxuse(...any)` and update harvest**

In `internal/codegen/pkg.go`, in `buildSkeleton`, inject the helper after the gsx import and change the interpolation probe; in `emitProbes` change the interpolation line; in `harvest` key on the `_gsxuse` call.

In `buildSkeleton`, after `sb.WriteString("import _gsxrt \"github.com/gsxhq/gsx\"\n")`, add:

```go
	sb.WriteString("func _gsxuse(...any) {}\n")
```

Replace `emitProbes` with:

```go
// emitProbes writes the type-resolution probes for a component body:
// `_gsxuse(expr)` per interpolation (arity-safe — _gsxuse is variadic, so a
// (T,error) call spreads to two args and still type-checks), and a child call
// `Child(ChildProps{})` per child component.
func emitProbes(sb *strings.Builder, nodes []gsxast.Markup) {
	for _, n := range nodes {
		switch t := n.(type) {
		case *gsxast.Interp:
			fmt.Fprintf(sb, "\t_gsxuse(%s)\n", strings.TrimSpace(t.Expr))
		case *gsxast.Element:
			if isComponentTag(t.Tag) {
				fmt.Fprintf(sb, "\t_ = %s(%sProps{})\n", t.Tag, t.Tag)
			} else {
				emitProbes(sb, t.Children)
			}
		}
	}
}
```

Replace the body of `harvest` with (an interpolation probe is now an `ExprStmt` whose call target is the identifier `_gsxuse`; harvest the single argument's type):

```go
func harvest(f *goast.File, comps []*gsxast.Component, info *types.Info, out map[*gsxast.Interp]types.Type) {
	byName := map[string]*gsxast.Component{}
	for _, c := range comps {
		byName[c.Name] = c
	}
	for _, decl := range f.Decls {
		fd, ok := decl.(*goast.FuncDecl)
		if !ok {
			continue
		}
		c, ok := byName[fd.Name.Name]
		if !ok || fd.Body == nil {
			continue
		}
		interps := componentInterps(c)
		k := 0
		for _, stmt := range fd.Body.List {
			es, ok := stmt.(*goast.ExprStmt)
			if !ok {
				continue
			}
			call, ok := es.X.(*goast.CallExpr)
			if !ok {
				continue
			}
			id, ok := call.Fun.(*goast.Ident)
			if !ok || id.Name != "_gsxuse" || len(call.Args) != 1 {
				continue // child-component probe or other
			}
			if k >= len(interps) {
				break
			}
			out[interps[k]] = info.Types[call.Args[0]].Type
			k++
		}
	}
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/codegen/ -run TestProbeAcceptsMultiValueExpr -v`
Expected: PASS (resolution succeeds; emission may still error on the unsupported `(T,error)` render until Task 3, which this test tolerates).

- [ ] **Step 5: Remove the probe path and the dead resolver**

In `internal/codegen/codegen.go`, delete `Generate` and `resolveTypes` (the `importer.Default()` probe), and remove the now-unused `go/importer` import. Move `GeneratePackage` (currently in `pkg.go`) to be the sole entry. Run `grep -rn 'resolveTypes\b\|func Generate\b\|go/importer' internal/codegen/` and confirm only `generateFile`/`resolveTypesPkg` remain (no `resolveTypes`, no `Generate`, no importer).

- [ ] **Step 6: Migrate the old single-string tests to `renderPackage`**

In `internal/codegen/e2e_test.go`, rewrite `TestRenderEndToEnd` and `TestRenderFieldAccess` to use `renderPackage` (they currently use `renderGSX` → the removed `Generate`). Replace `TestRenderEndToEnd` with:

```go
func TestRenderEndToEnd(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

component Greeting(name string, count int) {
	<p>Hello, {name}! You have {count} messages.</p>
}
`,
	}
	got := renderPackage(t, files, `p.Greeting(p.GreetingProps{Name: "World", Count: 3})`)
	assertHTMLEqual(t, got, "<p>Hello, World! You have 3 messages.</p>")
}
```

Replace `TestRenderFieldAccess` with:

```go
func TestRenderFieldAccess(t *testing.T) {
	files := map[string]string{
		"model.go": `package views

type User struct {
	Name string
	Age  int
}
`,
		"views.gsx": `package views

component Profile(user User) {
	<p>{user.Name} is {user.Age}</p>
}
`,
	}
	got := renderPackage(t, files, `p.Profile(p.ProfileProps{User: p.User{Name: "Alice", Age: 30}})`)
	assertHTMLEqual(t, got, "<p>Alice is 30</p>")
}
```

Then delete `renderGSX` and the now-unused `packageClause` regexp (and the `var _ = p.Footer` line stays in `renderPackage`'s harness — Footer may not be present in every fixture; replace it with a no-op: remove that line and instead reference nothing). Specifically, in `renderPackage`'s harness template, remove the `var _ = p.Footer` line.

- [ ] **Step 7: Replace the source golden test (now package-based)**

`TestGenerateSource` used `Generate` (removed). Replace it in `internal/codegen/codegen_test.go` with a package-based golden that writes the fixture to a temp module, runs `GeneratePackage`, and goldens the generated bytes:

```go
func TestGenerateSource(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gsxg\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(tmp, "genpkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, pkgDir, "views.gsx", `package views

component Greeting(name string, count int) {
	<p>Hello, {name}! You have {count} messages.</p>
}
`)
	gen, err := GeneratePackage(pkgDir)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	var got string
	for _, src := range gen {
		got = string(src)
	}
	const golden = "testdata/greeting.x.go.golden"
	if *update {
		os.MkdirAll(filepath.Dir(golden), 0o755)
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden (run -update): %v", err)
	}
	if got != string(want) {
		t.Fatalf("source mismatch (run -update):\n--- got ---\n%s", got)
	}
}
```

Ensure `codegen_test.go` imports `path/filepath` and `os` (and drops `go/token`/`parser` if now unused).

- [ ] **Step 8: Split files (mechanical move, no behavior change)**

Move the analysis-side functions into `internal/codegen/analyze.go` and the emission-side into `internal/codegen/emit.go`, leaving `GeneratePackage` in `codegen.go`. Use `git mv pkg.go analyze.go` then move `generateFile`/`genComponent`/`genNode`/`genInterp`/`genChildComponent`/`emitS`/`writeImports`/`isComponentTag` from `codegen.go` into a new `emit.go`, and move `classify`/category consts/`parseParams`/`usedParams`/`valueIdents`/`collectInterps`/`componentInterps`/`fieldName` into `analyze.go`. Fix per-file imports (each file imports only what it uses; `ast` vs `gsxast` aliases are the same package and interchangeable). This is pure relocation — the package must compile and all tests stay green.

- [ ] **Step 9: Regenerate golden, run all, vet, fmt**

Run: `go test ./internal/codegen/ -update` then `go test ./... && go vet ./... && gofmt -l internal/codegen/`
Expected: all green; gofmt prints nothing.

- [ ] **Step 10: Commit**

```bash
git add internal/codegen/
git commit -m "codegen: collapse onto go/packages; arity-safe _gsxuse probe; split analyze/emit"
```

---

### Task 2: Full §5 type-aware interpolation

Extend `classify` and `genInterp` to handle every single-value `§5` render category, emitting the precise runtime call.

**Files:**
- Modify: `internal/codegen/analyze.go` (`classify`)
- Modify: `internal/codegen/emit.go` (`genInterp`)
- Test: `internal/codegen/e2e_test.go`

**Interfaces:**
- Consumes: `resolveTypesPkg` result (Task 1).
- Produces: `classify(t types.Type) category` returning one of the categories below; `genInterp` emits per category.

- [ ] **Step 1: Write the failing render test**

Add to `internal/codegen/e2e_test.go`:

```go
func TestRenderInterpTypes(t *testing.T) {
	files := map[string]string{
		"model.go": `package views

import "fmt"

type Money int

func (m Money) String() string { return fmt.Sprintf("$%d", int(m)) }
`,
		"views.gsx": `package views

import "github.com/gsxhq/gsx"

component Demo(s string, n int, f float64, ok bool, node gsx.Node, price Money) {
	<div>{s}|{n}|{f}|{ok}|{node}|{price}</div>
}
`,
	}
	got := renderPackage(t, files,
		`p.Demo(p.DemoProps{S: "hi", N: 7, F: 3.5, Ok: true, Node: gsx.Raw("<b>x</b>"), Price: p.Money(9)})`)
	// gsx.Raw renders verbatim; Money is a fmt.Stringer -> "$9"; bool -> "true".
	assertHTMLEqual(t, got, `<div>hi|7|3.5|true|<b>x</b>|$9</div>`)
}
```

(The harness import alias `p` is the generated package; `gsx` must also be importable in the harness — see Step 4 to extend `renderPackage`'s harness imports.)

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/codegen/ -run TestRenderInterpTypes -v`
Expected: FAIL — `codegen: interpolation … type … only string/int supported yet` (float/bool/Node/Stringer unhandled).

- [ ] **Step 3: Extend `classify`**

In `internal/codegen/analyze.go`, replace the `category` consts and `classify` with:

```go
type category int

const (
	catUnsupported category = iota
	catString
	catBytes
	catInt
	catUint
	catFloat
	catBool
	catNode
	catNodeSlice
	catStringer
)

// classify maps a resolved type to a render category using structural checks
// (method sets), so it needs no handle to the gsx.Node / fmt.Stringer interface
// types.
func classify(t types.Type) category {
	if t == nil {
		return catUnsupported
	}
	if implementsNode(t) {
		return catNode
	}
	if s, ok := t.Underlying().(*types.Slice); ok && implementsNode(s.Elem()) {
		return catNodeSlice
	}
	if implementsStringer(t) {
		return catStringer
	}
	switch u := t.Underlying().(type) {
	case *types.Basic:
		switch {
		case u.Info()&types.IsString != 0:
			return catString
		case u.Info()&types.IsUnsigned != 0:
			return catUint
		case u.Info()&types.IsInteger != 0:
			return catInt
		case u.Info()&types.IsFloat != 0:
			return catFloat
		case u.Info()&types.IsBoolean != 0:
			return catBool
		}
	case *types.Slice:
		if b, ok := u.Elem().Underlying().(*types.Basic); ok && b.Kind() == types.Byte {
			return catBytes
		}
	}
	return catUnsupported
}

// implementsNode reports whether t has a method Render(context.Context, io.Writer) error.
func implementsNode(t types.Type) bool {
	m := lookupMethod(t, "Render")
	if m == nil {
		return false
	}
	sig := m.Type().(*types.Signature)
	if sig.Params().Len() != 2 || sig.Results().Len() != 1 {
		return false
	}
	if sig.Params().At(0).Type().String() != "context.Context" {
		return false
	}
	if sig.Params().At(1).Type().String() != "io.Writer" {
		return false
	}
	return sig.Results().At(0).Type().String() == "error"
}

// implementsStringer reports whether t has a method String() string.
func implementsStringer(t types.Type) bool {
	m := lookupMethod(t, "String")
	if m == nil {
		return false
	}
	sig := m.Type().(*types.Signature)
	return sig.Params().Len() == 0 && sig.Results().Len() == 1 &&
		sig.Results().At(0).Type().String() == "string"
}

func lookupMethod(t types.Type, name string) *types.Func {
	ms := types.NewMethodSet(t)
	if sel := ms.Lookup(nil, name); sel != nil {
		if fn, ok := sel.Obj().(*types.Func); ok {
			return fn
		}
	}
	// also try the pointer method set (value may be addressable at the call site)
	ms = types.NewMethodSet(types.NewPointer(t))
	if sel := ms.Lookup(nil, name); sel != nil {
		if fn, ok := sel.Obj().(*types.Func); ok {
			return fn
		}
	}
	return nil
}
```

- [ ] **Step 4: Extend `genInterp` and the harness imports**

In `internal/codegen/emit.go`, replace the `switch classify(t)` block in `genInterp` with:

```go
	expr := strings.TrimSpace(n.Expr)
	switch classify(t) {
	case catString:
		fmt.Fprintf(b, "\t\tgw.Text(%s)\n", expr)
	case catBytes:
		fmt.Fprintf(b, "\t\tgw.Text(string(%s))\n", expr)
	case catInt:
		imports["strconv"] = true
		fmt.Fprintf(b, "\t\tgw.Text(strconv.FormatInt(int64(%s), 10))\n", expr)
	case catUint:
		imports["strconv"] = true
		fmt.Fprintf(b, "\t\tgw.Text(strconv.FormatUint(uint64(%s), 10))\n", expr)
	case catFloat:
		imports["strconv"] = true
		fmt.Fprintf(b, "\t\tgw.Text(strconv.FormatFloat(float64(%s), 'g', -1, 64))\n", expr)
	case catBool:
		imports["strconv"] = true
		fmt.Fprintf(b, "\t\tgw.Text(strconv.FormatBool(%s))\n", expr)
	case catStringer:
		fmt.Fprintf(b, "\t\tgw.Text((%s).String())\n", expr)
	case catNode:
		fmt.Fprintf(b, "\t\tgw.Node(ctx, %s)\n", expr)
	case catNodeSlice:
		fmt.Fprintf(b, "\t\tfor _, _gsxn := range %s {\n\t\t\tgw.Node(ctx, _gsxn)\n\t\t}\n", expr)
	default:
		return fmt.Errorf("codegen: interpolation %q has type %s; not a renderable type", expr, t)
	}
	return nil
```

In `internal/codegen/e2e_test.go`, extend `renderPackage`'s harness so the gsx package is importable (needed by `TestRenderInterpTypes`): change the harness template's import block to

```go
import (
	"context"
	"os"

	"github.com/gsxhq/gsx"
	p "gsxrender/genpkg"
)

var _ = gsx.Raw
```

(keeping `gsx` referenced via `var _ = gsx.Raw` so the import is always used).

- [ ] **Step 5: Run to verify pass**

Run: `go test ./internal/codegen/ -run TestRenderInterpTypes -v`
Expected: PASS.

- [ ] **Step 6: Run all + commit**

Run: `go test ./... && go vet ./... && gofmt -l internal/codegen/`
Expected: green.

```bash
git add internal/codegen/
git commit -m "codegen: full §5 type-aware interpolation (string/bytes/numeric/bool/Node/[]Node/Stringer)"
```

---

### Task 3: `(T, error)` auto-unwrap

A `(T, error)` interpolation is detected (its harvested type is a 2-tuple ending in `error`) and lowered to a temp + propagate, then `T` is rendered by its category.

**Files:**
- Modify: `internal/codegen/emit.go` (`genInterp`)
- Test: `internal/codegen/e2e_test.go`

**Interfaces:**
- Consumes: `classify` (Task 2); resolution yields a `*types.Tuple` for a 2-value expr (via the `_gsxuse` probe, Task 1).

- [ ] **Step 1: Write the failing render test**

Add to `internal/codegen/e2e_test.go`:

```go
func TestRenderTryUnwrap(t *testing.T) {
	files := map[string]string{
		"helpers.go": `package views

func greet(name string) (string, error) { return "Hi " + name, nil }
`,
		"views.gsx": `package views

component Card(name string) {
	<p>{greet(name)}</p>
}
`,
	}
	got := renderPackage(t, files, `p.Card(p.CardProps{Name: "Al"})`)
	assertHTMLEqual(t, got, "<p>Hi Al</p>")
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/codegen/ -run TestRenderTryUnwrap -v`
Expected: FAIL — the harvested type is a `*types.Tuple`, which `classify` returns `catUnsupported` for, so `genInterp` errors "not a renderable type".

- [ ] **Step 3: Handle the tuple in `genInterp`**

In `internal/codegen/emit.go`, at the top of `genInterp` (after fetching `t := resolved[n]`), add tuple handling before the `classify` switch:

```go
	expr := strings.TrimSpace(n.Expr)
	if tup, ok := t.(*types.Tuple); ok {
		if tup.Len() != 2 || tup.At(1).Type().String() != "error" {
			return fmt.Errorf("codegen: interpolation %q returns %s; only (T, error) is supported", expr, t)
		}
		// v, err := expr; if err != nil { return err }; then render v by its type.
		n.tmp++ // unique temp name per component — see note
		tmp := fmt.Sprintf("_gsxv%d", interpTemp)
		interpTemp++
		fmt.Fprintf(b, "\t\t%s, _gsxerr := %s\n\t\tif _gsxerr != nil {\n\t\t\treturn _gsxerr\n\t\t}\n", tmp, expr)
		return emitRender(b, tmp, tup.At(0).Type(), imports)
	}
	return emitRender(b, expr, t, imports)
```

Refactor the per-category emission (from Task 2's `switch`) into a helper `emitRender(b *bytes.Buffer, expr string, t types.Type, imports map[string]bool) error` that contains the `switch classify(t)` block (operating on the given `expr` string and type `t`). `genInterp` then calls `emitRender(b, expr, t, imports)` for the single-value case and `emitRender(b, tmp, tup.At(0).Type(), imports)` after unwrapping.

Add a package-level temp counter for unique names (reset per `generateFile` call):

```go
// interpTemp gives unique unwrap temp names within a generated file. It is reset
// at the start of generateFile.
var interpTemp int
```

In `generateFile` (emit.go), add `interpTemp = 0` as the first line. (Remove the stray `n.tmp++` line above — it was illustrative; the real counter is `interpTemp`.) The final `genInterp` tuple branch is:

```go
	if tup, ok := t.(*types.Tuple); ok {
		if tup.Len() != 2 || tup.At(1).Type().String() != "error" {
			return fmt.Errorf("codegen: interpolation %q returns %s; only (T, error) is supported", expr, t)
		}
		tmp := fmt.Sprintf("_gsxv%d", interpTemp)
		interpTemp++
		fmt.Fprintf(b, "\t\t%s, _gsxerr := %s\n\t\tif _gsxerr != nil {\n\t\t\treturn _gsxerr\n\t\t}\n", tmp, expr)
		return emitRender(b, tmp, tup.At(0).Type(), imports)
	}
	return emitRender(b, expr, t, imports)
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/codegen/ -run TestRenderTryUnwrap -v`
Expected: PASS.

- [ ] **Step 5: Run all + commit**

Run: `go test ./... && go vet ./... && gofmt -l internal/codegen/`
Expected: green.

```bash
git add internal/codegen/
git commit -m "codegen: (T, error) interpolation auto-unwrap + propagate"
```

---

### Task 4: `//line` source maps

Emit `//line` directives so the Go compiler attributes errors in generated code to the originating `.gsx` position.

**Files:**
- Modify: `internal/codegen/emit.go` (`genNode`/`genInterp` emit a directive before each node), `internal/codegen/codegen.go` (thread the `.gsx` filename + `token.File` for position lookup)
- Test: `internal/codegen/codegen_test.go`

**Interfaces:**
- Consumes: the AST nodes' `Pos()` (gsx `token.Pos`) and the parse `token.FileSet`.

- [ ] **Step 1: Write the failing test**

Add to `internal/codegen/codegen_test.go`:

```go
func TestLineDirectives(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gsxl\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(tmp, "genpkg")
	os.MkdirAll(pkgDir, 0o755)
	writeFile(t, pkgDir, "views.gsx", "package views\n\ncomponent Greeting(name string) {\n\t<p>{name}</p>\n}\n")
	gen, err := GeneratePackage(pkgDir)
	if err != nil {
		t.Fatal(err)
	}
	var got string
	for _, src := range gen {
		got = string(src)
	}
	if !strings.Contains(got, "//line views.gsx:") {
		t.Fatalf("expected //line directives in generated source:\n%s", got)
	}
}
```

Ensure `codegen_test.go` imports `strings`.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/codegen/ -run TestLineDirectives -v`
Expected: FAIL — no `//line` directives yet.

- [ ] **Step 3: Thread positions and emit directives**

The parser records positions against a `token.FileSet`. `GeneratePackage` parses each `.gsx` with `token.NewFileSet()`; capture that fset and pass it through to emission.

In `internal/codegen/codegen.go`, change the parse loop to keep the fset per file and thread it into `generateFile`. Add a `*token.FileSet` parameter:

```go
		f, err := gsxparser.ParseFile(fset, m, src, 0)  // reuse one fset per call
```

Use a single `fset := token.NewFileSet()` for all files in `GeneratePackage`, and pass it to `generateFile(file, resolved, fset)`.

In `internal/codegen/emit.go`, change `generateFile`/`genComponent`/`genNode`/`genInterp` to carry `fset *token.FileSet`, and add a directive emitter. Before emitting an interpolation (and the open of each element), write:

```go
// emitLine writes a //line directive mapping subsequent output to the gsx node's
// source position, so Go compiler errors point at the .gsx file.
func emitLine(b *bytes.Buffer, fset *token.FileSet, pos token.Pos) {
	p := fset.Position(pos)
	fmt.Fprintf(b, "//line %s:%d:%d\n", filepath.Base(p.Filename), p.Line, p.Column)
}
```

Call `emitLine(b, fset, n.Pos())` at the start of `genInterp` (before the unwrap/render emission) and `emitLine(b, fset, t.Pos())` at the start of an `*ast.Element` case in `genNode`. After the function body's writes, a trailing `//line` is unnecessary — `go/format` keeps directives intact. Add `path/filepath` and `go/token` imports to `emit.go`.

NOTE: `//line` directives must start at column 1 (no leading tab). Because `go/format` would otherwise indent them, emit them with a leading newline and no indentation, and verify `format.Source` preserves them (it does — `//line` is special-cased by the formatter). If `format.Source` reflows them incorrectly, fall back to emitting directives only at statement boundaries already at column 0 within the closure body (acceptable: the directive applies to following lines).

- [ ] **Step 4: Run to verify pass + regen golden**

Run: `go test ./internal/codegen/ -run TestLineDirectives -v`
Expected: PASS.
Then: `go test ./internal/codegen/ -update` (the source golden now contains `//line` directives) and read `testdata/greeting.x.go.golden` to confirm the directives look right (e.g. `//line views.gsx:4:5`).

- [ ] **Step 5: Run all + commit**

Run: `go test ./... && go vet ./... && gofmt -l internal/codegen/`
Expected: green.

```bash
git add internal/codegen/
git commit -m "codegen: //line source maps to .gsx positions"
```

---

## Self-Review

**1. Spec coverage** (against `2026-06-19-gsx-codegen-design.md`, Phase-1 scope):
- go/packages + Overlay resolution as the sole path (collapse probe) → Task 1. ✓
- Arity-safe interpolation probe (enables `(T,error)`) → Task 1. ✓
- Full §5 single-value interpolation categories → Task 2. ✓
- `(T, error)` auto-unwrap → Task 3. ✓
- `//line` source maps → Task 4. ✓
- Render-golden acceptance via `renderPackage`/`assertHTMLEqual` → all tasks. ✓
- Out of scope (attributes, control flow, methods, child props, pipeline) → explicitly deferred; unsupported constructs already error clearly. ✓

**2. Placeholder scan:** No TBD/"handle edge cases". Task 4 Step 3's "fall back" note is a concrete contingency with a stated condition, not a placeholder. The illustrative `n.tmp++` line in Task 3 Step 3 is explicitly corrected to the real `interpTemp` counter in the same step.

**3. Type/signature consistency:**
- `classify(t types.Type) category` (Task 2) consumed by `genInterp`/`emitRender` (Tasks 2–3). ✓
- `emitRender(b *bytes.Buffer, expr string, t types.Type, imports map[string]bool) error` introduced in Task 3, used by both single-value and unwrap paths. ✓
- `_gsxuse(...any)` probe (Task 1) + harvest keying on it (Task 1) → tuple types flow to Task 3. ✓
- `generateFile(file, resolved, fset)` gains `fset` in Task 4; `GeneratePackage` threads one `token.FileSet`. ✓
- `interpTemp` reset in `generateFile` (Task 3). ✓

---

## Execution Notes for the Controller

- Tasks are sequential 1→4. Task 1 is the largest (collapse + restructure + test migration) and unblocks the rest; it must stay green before proceeding.
- After each task the FULL suite (`go test ./...`) and the example-corpus coverage stay green — Phase 1 is additive to the parser/runtime, which are untouched.
- Model guidance: Task 1 (structural, multi-file moves + test migration) → standard model; Tasks 2–4 (focused, with judgment on type classification / line directives) → standard model; the final whole-branch review → most capable model, ideally with a render-probe pass over a few hand-written `.gsx` inputs.
- Branch: this builds on `experiment/codegen-spike` (the spike is the seed). Continue there; land Phase 1 via review when all four tasks are green, then the spike+phase-1 graduate from "experiment" to a real `feat/codegen` history at merge time.
