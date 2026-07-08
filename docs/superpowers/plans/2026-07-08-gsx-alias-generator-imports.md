# `_gsx`-alias generator-emitted imports — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `gsx generate` never emit non-compiling `.x.go`, by recording each generator-owned import at its emission site and always referencing it through a reserved `_gsx*` alias.

**Architecture:** `internal/codegen/emit.go` currently seeds `context`/`io`/`gsx` into the `imports` map as a constant (`emit.go:65`) and writes the bare identifiers `gsx.`/`context.`/`io.`/`strconv.` into generated code. That single constant answers two independent questions — *are these needed* and *are these names free* — and both are sometimes false. We introduce a second, generator-owned import set (`rtImports`) threaded alongside the existing `imports` map. Four accessors return the reserved alias **and** record the need, so the two facts cannot drift. `writeImports` emits the recorded set as aliased lines, exactly the way it already emits reserved filter aliases (`_gsxf<i>`) beside a user's plain import of the same path.

**Tech Stack:** Go 1.26.1 (pin: `GO_VERSION` in `.github/workflows/ci.yml`). `internal/codegen` (emit + analyze), `internal/corpus` (txtar harness). Runtime (root `gsx` package) is untouched.

**Spec:** `docs/superpowers/specs/2026-07-08-gsx-alias-generator-imports-design.md`

## Global Constraints

- Runtime (root `gsx` package) is **standard-library only**. This change touches only `internal/codegen` and `internal/corpus`; do not add dependencies.
- **No "simple heuristics" in core logic.** Need is recorded at the emission site, never inferred by scanning generated text.
- **Never hand-edit `.x.go` or `*.golden`** — regenerate with `go test ./internal/corpus -run TestCorpus -update`, then verify without `-update`.
- Every syntax/codegen change ships a corpus case (`internal/corpus/testdata/cases/**/*.txtar`).
- `make check` (inner loop) must pass at the end of every task. `make ci` before the branch merges.
- Pin Go to 1.26.1; a different minor re-introduces `gofmt` drift.
- Do the work in a git worktree (repo convention). Every subagent dispatch must `cd` to the worktree and confirm the branch before editing.

## Reserved aliases (fixed, used verbatim throughout)

| path | alias |
|---|---|
| `github.com/gsxhq/gsx` | `_gsxrt` |
| `context` | `_gsxctx` |
| `io` | `_gsxio` |
| `strconv` | `_gsxsc` |

## Three categories of `gsx.` in `emit.go` — do not conflate

`grep -c 'gsx\.' internal/codegen/emit.go` → 96. They split three ways:

1. **Emitted into generated Go** (~71 sites) → become `_gsxrt.`
   e.g. `emit.go:128` `gsx.DefaultClassMerge`, `:522/:524/:580` `gsx.Node` return types, `:526/:588/:666/:4327` `gsx.Func(func(ctx context.Context, _gsxw io.Writer)`, `:557` `Children gsx.Node`, `:560` `Attrs gsx.Attrs`, `:622/:4328` `gsx.W`, `:1293/:1301/:1304/:1322/:3088/:3101/:3139` `gsx.Class`, `:3111/:3149` `gsx.ClassIf`, `:1435` `var %s gsx.Attrs`, `:1498` `gsx.StyleString`, `:2966/:3136/:3182/:3204` `gsx.StyleValue`, `:3841` `gsx.Val`, `:3849` `gsx.Attrs{`.
2. **Diagnostic message prose shown to users** → **must stay `gsx.`**
   `emit.go:806, 2272, 2484, 2618, 2962, 4697, 4765, 4778`. A user reading `need string/number/Stringer or gsx.RawCSS` must see `gsx.RawCSS`, not `_gsxrt.RawCSS`.
3. **Type-string comparisons against the user's own source** → **must stay `gsx.`**
   `analyze.go:318` (`strings.TrimSpace(typ) == "gsx.Node"`), `byo.go:313` (`== "gsx.Attrs"`). Verified safe: both scan hand-written `.go`/`.gsx` only — `byo.go:169` and `byo.go:354` explicitly skip `.x.go`.

**Guardrail:** always-aliasing makes a *missed* category-1 site fail loudly (generated code says `gsx.Class` while only `_gsxrt` is imported → `undefined: gsx` across many goldens). A *wrongly converted* category-2 site fails silently. So when in doubt, check whether the string is inside a `diag.Diagnostic`/`fmt.Errorf` message.

## File Structure

| File | Responsibility | Change |
|---|---|---|
| `internal/codegen/rtimports.go` | **new.** The `rtImports` type, the four path/alias constants, the four accessors. One place that knows the generator's own imports. | Create |
| `internal/codegen/emit.go` | Emission. Drop the constant seed; thread `rt rtImports`; convert 71 sites; `writeImports` emits reserved-alias lines. | Modify |
| `internal/codegen/analyze.go` | Delete `emittedImportIdent` + its `checkReservedParams` branch; add the file-scope `_gsx` prefix check. | Modify `:3561-3589` |
| `internal/corpus/batch.go` | Compile non-renderable cases (the blind spot). | Modify `:210-215` |
| `internal/corpus/testdata/cases/imports/*.txtar` | **new.** The permutation matrix. | Create |
| `docs/guide/syntax/raw-go.md` | Document the one rule. | Modify |
| `docs/ROADMAP.md` | Flip the tracked-debt item to `[x]`. | Modify |

---

## Task 1: Corpus harness compiles non-renderable cases

**Why first:** this is the detector. `internal/corpus/batch.go:215` reads `if !c.renderable() { continue }`, so a `.gsx` with no component (nothing to `-- invoke --`) has its `.x.go` golden-pinned but **never compiled**. Every bug this plan fixes hid there. Without this task, Task 2's new cases would pass while emitting broken code.

**Measured triage surface:** exactly one existing case is gen-pinned and non-renderable (`components/child_prop_attrs_reference`), and it is `diag(error)` — which we skip. So expect **zero** pre-existing breakage. (The spec's risk section over-estimated this; correct it in Task 5.)

**Design:** do **not** shell out to `go build ./...` — most non-renderable case dirs contain no Go files at all and `go build` would error. Instead blank-import the compiled packages into the `main.go` the harness already synthesizes, so the existing single `go run .` compiles them. Zero extra processes.

**Files:**
- Modify: `internal/corpus/batch.go:210-258`
- Create: `internal/corpus/testdata/cases/imports/component_no_invoke.txtar`

**Interfaces:**
- Consumes: `caseDoc.renderable() bool`, `caseDoc.multiPkg bool`, `caseDoc.packageDirs() []string`, `caseImportRoot(c) string` (`internal/corpus/codegen.go:18`), `results[c.name].gen []byte`, `results[c.name].diag []diag.Diagnostic`.
- Produces: nothing new for later tasks; changes only which cases get compiled.

- [ ] **Step 1: Write the failing test — a gen-pinned, non-renderable case**

Create `internal/corpus/testdata/cases/imports/component_no_invoke.txtar`. This is the **positive control**: it has a component (so its generated imports are genuinely used) but no `-- invoke --` section, so it is non-renderable and, before this task, never compiled.

```
# Context: a gen-pinned case with no `-- invoke --` section. Exercises the
# harness path added for non-renderable cases: its .x.go must COMPILE, not just
# match a golden. Before internal/corpus/batch.go blank-imported non-renderable
# case packages, generated output for a case like this was golden-pinned and
# never built — the blind spot that hid six ways to emit non-compiling code.
-- input.gsx --
package demo

component Only() {
	<b>hi</b>
}
-- diagnostics.golden --
-- generated.x.go.golden --
```

- [ ] **Step 2: Generate its golden and confirm the case is NOT compiled today**

```bash
go test ./internal/corpus -run TestCorpus -update 2>&1 | tail -2
```

Confirm the golden filled in, then prove the blind spot exists by temporarily
corrupting the generated output — add a deliberate unused import to the golden:

```bash
# Insert `"os"` into the generated golden's import block by hand, TEMPORARILY.
go test ./internal/corpus -run TestCorpus 2>&1 | tail -3
```

Expected **today**: the corpus **fails on a golden mismatch**, not a compile
error — proving the case is never built. Revert the hand-edit
(`git checkout -- internal/corpus/testdata/cases/imports/component_no_invoke.txtar`)
and regenerate before continuing. (This is a one-off diagnostic; do not commit
the corrupted golden.)

- [ ] **Step 3: Implement — blank-import non-renderable case packages**

In `internal/corpus/batch.go`, replace the `built := 0` declaration and add a
second loop after the existing renderable loop (which ends just before
`if built > 0 {`).

Change the counter block at `:211-212` from:

```go
	// Step 5: build and run all renderable cases with a single `go run`.
	var imports, dispatch bytes.Buffer
	built := 0
```

to:

```go
	// Step 5: build and run all renderable cases with a single `go run`.
	// Non-renderable cases that produced generated output are blank-imported into
	// the same main.go so they COMPILE too. A .gsx with no component has nothing
	// to invoke, so before this its .x.go was golden-pinned but never built — the
	// blind spot that hid `generate` emitting unused imports and redeclared
	// identifiers while exiting 0.
	var imports, dispatch bytes.Buffer
	built := 0
	compiled := 0
```

Then, immediately after the existing renderable `for _, c := range candidates { … }`
loop closes and **before** `if built > 0 {`, insert:

```go
	for _, c := range candidates {
		if c.renderable() {
			continue // already imported (and thus compiled) by the loop above
		}
		cg := results[c.name]
		if cg == nil || len(cg.gen) == 0 || len(cg.diag) > 0 {
			// No generated output, or the case pins expected diagnostics — an
			// error case is not meant to compile.
			continue
		}
		root := caseImportRoot(c)
		pkgs := []string{root}
		if c.multiPkg {
			pkgs = pkgs[:0]
			for _, dir := range c.packageDirs() {
				pkgs = append(pkgs, root+"/"+dir)
			}
		}
		for _, p := range pkgs {
			fmt.Fprintf(&imports, "\t_ %q\n", p)
			compiled++
		}
	}
```

Finally change the gate and the `main.go` body so a compile-only run still
produces a valid program. Replace:

```go
	if built > 0 {
		main := "package main\n\nimport (\n\t\"context\"\n\t\"fmt\"\n\t\"os\"\n" + imports.String() + ")\n\nfunc main() {\n\tctx := context.Background()\n" + dispatch.String() + "}\n"
```

with:

```go
	if built > 0 || compiled > 0 {
		// With no renderable case, context/fmt/os would be unused imports in the
		// harness's own main.go — emit the minimal program instead.
		main := "package main\n\nimport (\n" + imports.String() + ")\n\nfunc main() {}\n"
		if built > 0 {
			main = "package main\n\nimport (\n\t\"context\"\n\t\"fmt\"\n\t\"os\"\n" + imports.String() + ")\n\nfunc main() {\n\tctx := context.Background()\n" + dispatch.String() + "}\n"
		}
```

- [ ] **Step 4: Prove the harness now compiles the case**

Re-apply the temporary corruption from Step 2 (add `"os"` to the case's
generated golden), then:

```bash
go test ./internal/corpus -run TestCorpus 2>&1 | tail -5
```

Expected: a **build failure** mentioning `"os" imported and not used`, not a
golden mismatch. The detector works. Now revert:

```bash
git checkout -- internal/corpus/testdata/cases/imports/component_no_invoke.txtar
```

- [ ] **Step 5: Full suite green**

```bash
go test ./internal/corpus -run TestCorpus -count=1 2>&1 | tail -3
```

Expected: `ok`. Zero pre-existing cases newly fail (measured: the only
gen-pinned non-renderable case is `diag(error)`, which is skipped). If any case
*does* fail, **stop and triage it explicitly** — fix it, or quarantine it with a
recorded reason in the case's header comment. Do not silence.

- [ ] **Step 6: `make check`**

```bash
make check
```

Expected: exit 0.

- [ ] **Step 7: Commit**

```bash
git add internal/corpus/batch.go internal/corpus/testdata/cases/imports/component_no_invoke.txtar internal/corpus/testdata/coverage.golden
git commit -m "test(corpus): compile non-renderable cases

batch.go skipped every case without an -- invoke -- section, so a .gsx with no
component had its generated .x.go golden-pinned but never built. Blank-import
those case packages into the synthesized main.go so the existing single \`go run\`
compiles them. Adds a gen-pinned, non-renderable positive control."
```

---

## Task 2: `rtImports` — record need at the emission site, always alias

**This task is atomic.** D1 (need) and D2 (binding) touch the same ~71 sites through the same accessors; a partial conversion emits `gsx.Class` while importing only `_gsxrt` and nothing builds. Do not split it.

**Scale, measured:** 43 signatures in `emit.go` take `imports map[string]bool`; 86 call sites pass it. `rt rtImports` is added beside it at each. The Go compiler proves completeness of the threading — a missed thread is a compile error, not a silent bug.

**Files:**
- Create: `internal/codegen/rtimports.go`
- Modify: `internal/codegen/emit.go` (`:65-69`, `:108-112`, `:322`, `:388-448`, + 71 emission sites + 43 signatures)
- Create: `internal/corpus/testdata/cases/imports/no_gsx_parts.txtar`, `plain_go_only.txtar`, `f_literal_only.txtar`, `gsx_node_no_component.txtar`, `component_and_user_gsx.txtar`, `element_literal_var.txtar`

**Interfaces:**
- Produces, consumed by Tasks 3 and 4:
  - `type rtImports map[string]bool` — generator-owned import paths, disjoint from `imports`.
  - `const gsxRuntimePath = "github.com/gsxhq/gsx"`
  - `const rtAlias, ctxAlias, ioAlias, scAlias = "_gsxrt", "_gsxctx", "_gsxio", "_gsxsc"`
  - `func (r rtImports) rt() string` / `ctx() string` / `io() string` / `sc() string` — each records the need and returns the alias.
  - `func writeImports(b *bytes.Buffer, imports map[string]bool, rt rtImports, aliased []importSpec, filterAlias map[string]string, usedFilterPkg, userPlainImports map[string]bool, typeArgAliases map[string]string)` — note the new third parameter.
- Consumes: nothing from Task 1.

- [ ] **Step 1: Write the failing corpus cases**

Six cases under `internal/corpus/testdata/cases/imports/`. Each has a
`-- generated.x.go.golden --` section (so `-update` fills it) and **no**
`-- invoke --` unless it renders, so Task 1's harness compiles them.

`no_gsx_parts.txtar`:

```
# Context: a .gsx file with NO gsx parts at all. The generated .x.go must have
# NO import block — before this change, emit.go seeded context/io/gsx as a
# constant and `go build` failed with three "imported and not used" errors while
# `gsx generate` exited 0.
-- input.gsx --
package demo
-- diagnostics.golden --
-- generated.x.go.golden --
```

`plain_go_only.txtar`:

```
# Context: a .gsx carrying only ordinary Go and its own import. Generated output
# imports `fmt` and nothing else — no generator runtime imports, because the
# generator emits no runtime references.
-- input.gsx --
package demo

import "fmt"

func Helper() string { return fmt.Sprintf("x%d", 1) }
-- diagnostics.golden --
-- generated.x.go.golden --
```

`f_literal_only.txtar`:

```
# Context: `f"…"` is a gsx feature that lowers to a plain Go string. It needs no
# runtime import, so the generated file must have no import block. Pins that
# "has gsx syntax" and "needs the gsx package" are different questions.
-- input.gsx --
package demo

var S = f"hello world"
-- diagnostics.golden --
-- generated.x.go.golden --
```

`gsx_node_no_component.txtar`:

```
# Context: user Go references gsx.Node, but there is no component and no element
# literal — so the generator emits no closures. Generated output must import the
# runtime under the user's OWN plain name (they wrote the import) and must NOT
# import context or io. Pins that needGsx without needCtxIO is reachable.
-- input.gsx --
package demo

import "github.com/gsxhq/gsx"

func wrap(n gsx.Node) gsx.Node { return n }

var _ = wrap
-- diagnostics.golden --
-- generated.x.go.golden --
```

`component_and_user_gsx.txtar`:

```
# Context: BOTH a component (generator emits _gsxrt/_gsxctx/_gsxio) and user Go
# naming gsx.Node (user's own plain import). One path, two names — exactly the
# `userPlainImports` pattern emit.go already uses for filter packages. Pins that
# the generator's namespace and the user's never interact.
-- input.gsx --
package demo

import "github.com/gsxhq/gsx"

func wrap(n gsx.Node) gsx.Node { return n }

component Uses() {
	<div>{ wrap(<b>hi</b>) }</div>
}
-- invoke --
Uses()
-- diagnostics.golden --
-- render.golden --
-- generated.x.go.golden --
```

`element_literal_var.txtar`:

```
# Context: an element literal in a var initializer, no component. The generator
# emits a closure, so it needs the runtime, context and io — all under reserved
# aliases. The user's Go names nothing, so no plain import.
-- input.gsx --
package demo

var X = <div><b>hi</b></div>

var _ = X
-- diagnostics.golden --
-- generated.x.go.golden --
```

- [ ] **Step 2: Run to verify they fail**

```bash
go test ./internal/corpus -run TestCorpus -update 2>&1 | tail -3
go test ./internal/corpus -run TestCorpus -count=1 2>&1 | tail -8
```

Expected: after `-update` writes goldens reflecting *current* (buggy) output,
the verify run **fails to build** several cases:
`"context" imported and not used`, `"io" imported and not used`,
`"github.com/gsxhq/gsx" imported and not used`.

Do **not** commit those goldens. This is the red state.

- [ ] **Step 3: Create `internal/codegen/rtimports.go`**

```go
package codegen

// rtImports records the imports the GENERATOR itself emits references to, keyed
// by import path. It is deliberately disjoint from emit's `imports` map (which
// holds the user's Go-chunk imports plus filter/type-arg/class-merger packages):
// both may need the same path under different names. The generator always
// reaches the runtime through a reserved `_gsx` alias; user Go always reaches it
// through the plain package name. Go permits one path under two names, and
// emit.go already relies on that for filter packages (see userPlainImports).
//
// Every accessor records the need AND returns the identifier to print, so a
// reference can never be emitted without its import, nor an import without a
// reference. This is why no site prints these package names literally.
type rtImports map[string]bool

// gsxRuntimePath is the import path of the gsx runtime.
const gsxRuntimePath = "github.com/gsxhq/gsx"

// Reserved aliases. The `_gsx` prefix is not a valid identifier for user code
// (checkReservedParams and checkReservedDecls reject it), so these can never
// collide with anything the user writes — which is what lets a .gsx file bind
// `gsx`, `context`, `io` or `strconv` to whatever it likes.
const (
	rtAlias  = "_gsxrt"
	ctxAlias = "_gsxctx"
	ioAlias  = "_gsxio"
	scAlias  = "_gsxsc"
)

// rt records a need for the gsx runtime and returns its alias.
func (r rtImports) rt() string { r[gsxRuntimePath] = true; return rtAlias }

// ctx records a need for "context" and returns its alias.
func (r rtImports) ctx() string { r["context"] = true; return ctxAlias }

// io records a need for "io" and returns its alias.
func (r rtImports) io() string { r["io"] = true; return ioAlias }

// sc records a need for "strconv" and returns its alias.
func (r rtImports) sc() string { r["strconv"] = true; return scAlias }
```

- [ ] **Step 4: Drop the constant seed and create the set**

In `generateFile` (`internal/codegen/emit.go:65`), replace:

```go
	imports := map[string]bool{
		"context":              true,
		"io":                   true,
		"github.com/gsxhq/gsx": true,
	}
```

with:

```go
	// imports holds the USER's Go-chunk imports plus the filter / type-arg /
	// class-merger packages. It starts empty: nothing is needed until something
	// is emitted. The generator's own imports live in `rt` and are recorded at
	// their emission sites (see rtimports.go) — the discipline `strconv` already
	// followed, now applied to the runtime, context and io as well.
	imports := map[string]bool{}
	rt := rtImports{}
```

In the same function, remove the three `boundNames` seeds at `:108-112`:

```go
	boundNames := map[string]string{
		"context": "context",
		"io":      "io",
		"gsx":     "github.com/gsxhq/gsx",
	}
```

becomes:

```go
	// The generator binds no plain package names any more — every generator
	// reference goes through a reserved `_gsx` alias — so nothing is pre-bound
	// here. User imports and the reserved alias family are added below.
	boundNames := map[string]string{}
```

and update `mergeExpr` at `:128`:

```go
	mergeExpr := "gsx.DefaultClassMerge"
```

becomes:

```go
	mergeExpr := rt.rt() + ".DefaultClassMerge"
```

Note this records the runtime need unconditionally in `generateFile`, which is
wrong for a file with no components. Guard it — `mergeExpr` is only *used* when
a class attribute is emitted:

```go
	// Resolved lazily: naming the default merger would otherwise record a
	// runtime need in every file, including files that emit nothing.
	mergeExpr := ""
	if merger != nil {
		mergeExpr = classMergerAlias + "." + merger.FuncName
	}
```

and at each `mergeExpr` **use** site, substitute the default if empty:

```go
	if mergeExpr == "" {
		mergeExpr = rt.rt() + ".DefaultClassMerge"
	}
```

Locate the use sites with `grep -n mergeExpr internal/codegen/emit.go`. Since
`mergeExpr` is threaded as a `string` parameter, the cleanest form is to keep
threading it but resolve it once, immediately before the first component is
emitted, only if the file has at least one component or element literal. Prefer:
thread `rt` to the `_gsxgw.Class(...)` emission sites and build the merge
expression there.

- [ ] **Step 5: Thread `rt rtImports` through emit.go**

Mechanical. For each of the 43 signatures containing `imports map[string]bool`,
add `rt rtImports` immediately after it, and update the 86 call sites to pass
`rt`. The compiler proves completeness.

```bash
# Find them:
grep -n 'imports map\[string\]bool' internal/codegen/emit.go
grep -n ', imports,' internal/codegen/emit.go
```

Build after each batch:

```bash
go build ./internal/codegen
```

- [ ] **Step 6: Convert the 71 emission sites**

Replace every generated-code reference. Representative examples — apply the same
transformation everywhere:

`emit.go:522-526`:

```go
		if c.Recv != "" {
			fmt.Fprintf(b, "func %s %s%s(%s) %s.Node {\n", c.Recv, c.Name, typeParamsDecl, strings.TrimSpace(c.Params), rt.rt())
		} else {
			fmt.Fprintf(b, "func %s%s(%s) %s.Node {\n", c.Name, typeParamsDecl, strings.TrimSpace(c.Params), rt.rt())
		}
		fmt.Fprintf(b, "\treturn %s.Func(func(ctx %s.Context, _gsxw %s.Writer) error {\n", rt.rt(), rt.ctx(), rt.io())
```

`emit.go:622`:

```go
	fmt.Fprintf(b, "\t\t_gsxgw := %s.W(_gsxw)\n", rt.rt())
```

`emit.go:2798-2805` (strconv — delete the `imports[…]` lines, the accessor does it):

```go
	case catInt:
		return rt.sc() + ".FormatInt(int64(" + expr + "), 10)", true
	case catUint:
		return rt.sc() + ".FormatUint(uint64(" + expr + "), 10)", true
	case catFloat:
		return rt.sc() + ".FormatFloat(float64(" + expr + "), 'g', -1, 64)", true
```

`emit.go:2013`:

```go
	case catBool:
		fmt.Fprintf(b, "\t\t_gsxgw.Text(%s.FormatBool(bool(%s)))\n", rt.sc(), expr)
```

`emit.go:3088`:

```go
			parts = append(parts, fmt.Sprintf("%s.Class(%s)", rt.rt(), tmp))
```

**The `ctx` parameter name stays bare** — it is the documented ambient context
that user interpolations reference. Only its *type* becomes `_gsxctx.Context`.

**Do not touch** the eight diagnostic-prose sites (`:806, 2272, 2484, 2618,
2962, 4697, 4765, 4778`) or `analyze.go:318` / `byo.go:313`.

Verify the conversion is complete and correct:

```bash
# Remaining `gsx.` in emit.go must be EXACTLY the 8 diagnostic-prose sites.
grep -n 'gsx\.' internal/codegen/emit.go | grep -v '_gsx' | grep -v 'rt\.rt()'
```

Expected: only lines 806, 2272, 2484, 2618, 2962, 4697, 4765, 4778 (line numbers
will have shifted; check each is inside a diagnostic message).

- [ ] **Step 7: `writeImports` emits the reserved-alias lines**

Change the signature at `emit.go:388` and the call at `:322`:

```go
	writeImports(&b, imports, rt, aliased, filterAlias, usedFilterPkg, userPlainImports, typeArgAliases)
```

```go
func writeImports(b *bytes.Buffer, imports map[string]bool, rt rtImports, aliased []importSpec, filterAlias map[string]string, usedFilterPkg map[string]bool, userPlainImports map[string]bool, typeArgAliases map[string]string) {
```

Inside, build the generator's alias lines and suppress the whole block when
everything is empty:

```go
	// The generator's own imports, always under their reserved aliases. Kept
	// separate from `imports` so a user's plain import of the SAME path (e.g.
	// they wrote `gsx.Node` in a Go chunk) emits its own line — Go permits one
	// path under two names, and the generator must never depend on, or be
	// satisfied by, whatever the user bound that name to.
	type rtImp struct{ alias, path string }
	var rts []rtImp
	for _, e := range []rtImp{
		{ctxAlias, "context"},
		{ioAlias, "io"},
		{scAlias, "strconv"},
		{rtAlias, gsxRuntimePath},
	} {
		if rt[e.path] {
			rts = append(rts, e)
		}
	}

	if len(imports) == 0 && len(rts) == 0 && len(aliased) == 0 && len(typeArgAliases) == 0 {
		return // nothing to import — emit no import block at all
	}
```

Emit `rts` in the aliased section (after `ext`, alongside `filters`), so std
grouping stays as-is. `go/format` (already applied at `:327`) normalises the
final grouping, so exact placement within the block need not be hand-tuned.

- [ ] **Step 8: Build, then regenerate every golden**

```bash
go build ./... && go vet ./internal/codegen
go test ./internal/corpus -run TestCorpus -update 2>&1 | tail -3
```

- [ ] **Step 9: Review the golden diff — this is the safety net**

The 269 regenerated `generated.x.go.golden` sections must differ **only** by the
four substitutions and by removed import lines. Anything else is a real
regression hiding in mechanical churn.

```bash
git diff internal/corpus/testdata/cases \
  | grep '^[+-]' | grep -v '^[+-][+-]' \
  | grep -vE '_gsxrt\.|_gsxctx\.|_gsxio\.|_gsxsc\.|gsx\.Func|gsx\.W|gsx\.Class|gsx\.ClassIf|gsx\.Node|gsx\.Attrs|gsx\.Val|gsx\.Style|gsx\.DefaultClassMerge|strconv\.|context\.Context|io\.Writer|^[+-]\s*"(context|io|strconv|github.com/gsxhq/gsx)"$|^[+-]\s*_gsx\w+ "|^[+-]import \(|^[+-]\)|^[+-]$'
```

Expected: empty output. Investigate every line it prints.

- [ ] **Step 10: Verify, including the new cases**

```bash
go test ./internal/corpus -run TestCorpus -count=1 2>&1 | tail -3
```

Expected: `ok`. The six new cases now compile: `no_gsx_parts` has **no import
block**, `gsx_node_no_component` imports only `"github.com/gsxhq/gsx"` plainly,
`element_literal_var` imports only the three reserved aliases.

Inspect two by eye to confirm the intent, not just the pass:

```bash
sed -n '/generated.x.go.golden/,$p' internal/corpus/testdata/cases/imports/no_gsx_parts.txtar
sed -n '/generated.x.go.golden/,$p' internal/corpus/testdata/cases/imports/gsx_node_no_component.txtar
```

- [ ] **Step 11: Fix the 5 codegen unit tests asserting literal `gsx.`**

```bash
go test ./internal/codegen 2>&1 | tail -20
```

Update assertions from `gsx.Func` → `_gsxrt.Func` etc. Locate with:

```bash
grep -rln '"gsx\.\|gsx\.Func' internal/codegen/*_test.go
```

- [ ] **Step 12: `make check`**

```bash
make check
```

Expected: exit 0.

- [ ] **Step 13: Commit**

```bash
git add -A
git commit -m "feat(codegen): _gsx-alias generator-emitted imports

emit.go seeded context/io/gsx into the imports map as a constant, and wrote the
bare identifiers gsx./context./io./strconv. into generated code. That one
constant answered two independent questions — are these needed, and are these
names free — and both are sometimes false: a .gsx with no gsx parts emitted
three unused imports, and a .gsx binding \`gsx\` (import gsx \"strings\",
var gsx = 1) emitted \`gsx redeclared in this block\`. Six probed sources where
generate exited 0 and go build failed.

Introduce rtImports: a generator-owned import set, disjoint from the user's, whose
four accessors record the need AND return a reserved _gsx alias, so a reference
can never be emitted without its import nor an import without a reference. This is
the discipline strconv already followed, now applied to all four. writeImports
emits them as reserved-alias lines beside any plain user import of the same path —
the pattern already used for filter packages.

An empty set emits no import block at all.

Spec: docs/superpowers/specs/2026-07-08-gsx-alias-generator-imports-design.md"
```

---

## Task 3: Pin the collisions; delete the reserved-param stopgap

No emitter change. Task 2 already made these work; this task **pins** them and
removes the now-dead stopgap that `analyze.go`'s own comment calls temporary.

**Files:**
- Modify: `internal/codegen/analyze.go:3561-3589`
- Create, under `internal/corpus/testdata/cases/imports/`: `shadow_gsx_import.txtar`, `shadow_gsx_decl.txtar`, `shadow_context.txtar`, `shadow_io.txtar`, `shadow_strconv.txtar`, `param_named_gsx.txtar`, `unused_gsx_import.txtar`, `alias_gsx_import.txtar`, `blank_gsx_import.txtar`, `dot_gsx_import.txtar`

**Interfaces:**
- Consumes: `rtImports` and the reserved aliases from Task 2.
- Produces: `checkReservedParams` no longer rejects `gsx`/`strconv` params.

- [ ] **Step 1: Write the failing tests**

`shadow_gsx_import.txtar`:

```
# Context: the .gsx binds the name `gsx` to a DIFFERENT package. The generator
# references the runtime as _gsxrt, so there is no collision — before the alias
# change this emitted `gsx redeclared in this block` while generate exited 0.
-- input.gsx --
package demo

import gsx "strings"

component Uses() {
	<b>{ gsx.ToUpper("hi") }</b>
}
-- invoke --
Uses()
-- diagnostics.golden --
-- render.golden --
<b>HI</b>
-- generated.x.go.golden --
```

`shadow_gsx_decl.txtar`:

```
# Context: a top-level declaration named `gsx`. boundNames was seeded only from
# the import region, so this slipped past every guard and emitted
# `gsx already declared through import of package gsx`.
-- input.gsx --
package demo

var gsx = 7

component Uses() {
	<b>{ gsx }</b>
}
-- invoke --
Uses()
-- diagnostics.golden --
-- render.golden --
<b>7</b>
-- generated.x.go.golden --
```

`shadow_context.txtar`:

```
# Context: the .gsx binds `context` to something else. The closure preamble names
# the type as _gsxctx.Context, so the user's binding wins.
-- input.gsx --
package demo

var context = "shadowed"

component Uses() {
	<b>{ context }</b>
}
-- invoke --
Uses()
-- diagnostics.golden --
-- render.golden --
<b>shadowed</b>
-- generated.x.go.golden --
```

`shadow_io.txtar`:

```
# Context: the .gsx binds `io`. The closure's writer param is typed _gsxio.Writer.
-- input.gsx --
package demo

var io = 3

component Uses() {
	<b>{ io }</b>
}
-- invoke --
Uses()
-- diagnostics.golden --
-- render.golden --
<b>3</b>
-- generated.x.go.golden --
```

`shadow_strconv.txtar`:

```
# Context: the .gsx binds `strconv` AND interpolates a numeric value, which is
# the only construct that makes the generator reference strconv (as _gsxsc).
-- input.gsx --
package demo

var strconv = "shadowed"

component Uses(n int) {
	<b>{ n }{ strconv }</b>
}
-- invoke --
Uses(UsesProps{N: 42})
-- diagnostics.golden --
-- render.golden --
<b>42shadowed</b>
-- generated.x.go.golden --
```

`param_named_gsx.txtar`:

```
# Context: `gsx` and `strconv` were reserved PARAM names as a stopgap for the
# emitter naming those packages in closure bodies. With generator references
# _gsx-aliased, nothing shadows anything and the reservation is gone.
-- input.gsx --
package demo

component Uses(gsx string, strconv int) {
	<b>{ gsx }{ strconv }</b>
}
-- invoke --
Uses(UsesProps{Gsx: "ok", Strconv: 1})
-- diagnostics.golden --
-- render.golden --
<b>ok1</b>
-- generated.x.go.golden --
```

`unused_gsx_import.txtar` — **this case pins the rule**, and the rule is the
point of the whole change. A component does not make the import used; only the
user's own Go naming `gsx` does:

```
# Context: the file imports the gsx runtime but no user Go references it — a
# component is generated through the reserved _gsxrt alias, which does not use
# the user's import. So it is unused, exactly as an unused `fmt` would be. This
# is the load-bearing half of the rule: "reference it -> import it; don't -> don't."
-- input.gsx --
package demo

import "github.com/gsxhq/gsx"

component Uses() {
	<b>hi</b>
}
-- diagnostics.golden --
3:8: error: "github.com/gsxhq/gsx" imported and not used
```

`alias_gsx_import.txtar`:

```
# Context: the user aliases the runtime to their own name and uses it. Their
# alias and the generator's _gsxrt coexist — one path, two names.
-- input.gsx --
package demo

import g "github.com/gsxhq/gsx"

func wrap(n g.Node) g.Node { return n }

component Uses() {
	<div>{ wrap(<b>hi</b>) }</div>
}
-- invoke --
Uses()
-- diagnostics.golden --
-- render.golden --
<div><b>hi</b></div>
-- generated.x.go.golden --
```

`blank_gsx_import.txtar`:

```
# Context: a blank import binds no name, so it neither satisfies nor conflicts
# with anything. It passes through verbatim alongside the generator's aliases.
-- input.gsx --
package demo

import _ "github.com/gsxhq/gsx"

component Uses() {
	<b>hi</b>
}
-- invoke --
Uses()
-- diagnostics.golden --
-- render.golden --
<b>hi</b>
-- generated.x.go.golden --
```

`dot_gsx_import.txtar`:

```
# Context: a dot import binds the runtime's exported names directly, not `gsx`.
# It must not be mistaken for the generator's import, and the generator still
# reaches the runtime through _gsxrt.
-- input.gsx --
package demo

import . "github.com/gsxhq/gsx"

func wrap(n Node) Node { return n }

component Uses() {
	<div>{ wrap(<b>hi</b>) }</div>
}
-- invoke --
Uses()
-- diagnostics.golden --
-- render.golden --
<div><b>hi</b></div>
-- generated.x.go.golden --
```

- [ ] **Step 2: Run to verify `param_named_gsx` fails**

```bash
go test ./internal/corpus -run 'TestCorpus/imports' -count=1 2>&1 | tail -6
```

Expected: `param_named_gsx` fails with
`codegen: param name "gsx" is reserved (shadows a generated import)`.
The five `shadow_*` cases should already pass (Task 2 fixed them) once their
goldens exist — that is the point of pinning them.

- [ ] **Step 3: Delete the stopgap**

In `internal/codegen/analyze.go`, remove from `checkReservedParams`:

```go
		// Package identifiers the emitter references inside the closure body: a
		// same-named param would shadow them via local-binding and break the
		// generated code. (The runtime import and strconv are the only package
		// idents emitted into bodies today; a more robust fix would _gsx-alias
		// generator-emitted imports — tracked for phase 2.)
		if emittedImportIdent[p.name] {
			return fmt.Errorf("codegen: param name %q is reserved (shadows a generated import)", p.name)
		}
```

and delete the variable entirely (`analyze.go:3587-3589`):

```go
// emittedImportIdent is the set of package identifiers the emitter references in
// a render closure body (see genInterp/emitRender and genComponent).
var emittedImportIdent = map[string]bool{"gsx": true, "strconv": true}
```

`ctx`, `children`, `attrs`, and the `_gsx` prefix check all stay.

- [ ] **Step 4: Run to verify all six pass**

```bash
go test ./internal/corpus -run TestCorpus -update 2>&1 | tail -2
go test ./internal/corpus -run TestCorpus -count=1 2>&1 | tail -3
```

Expected: `ok`.

Confirm nothing else referenced the deleted symbol:

```bash
gopls check -severity=hint internal/codegen/analyze.go 2>&1 | head -5
grep -rn emittedImportIdent internal/ || echo "clean"
```

- [ ] **Step 5: `make check`**

```bash
make check
```

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "feat(codegen): drop the gsx/strconv reserved-param stopgap

With generator references _gsx-aliased, a param named gsx or strconv can no
longer shadow anything the emitter writes. Deletes emittedImportIdent and its
checkReservedParams branch — the stopgap analyze.go's own comment flagged for
phase 2. Pins the six shadowing permutations that previously produced
non-compiling output while generate exited 0."
```

---

## Task 4: Reserve the `_gsx` prefix at file scope

Aliasing moves the entire collision surface onto one prefix. It is enforced for
params (`checkReservedParams`) but not for file-scope declarations, so
`var _gsxrt = 1` would now collide with the generator's import.

**Files:**
- Modify: `internal/codegen/analyze.go` (add `checkReservedDecls`, call it per file)
- Create: `internal/corpus/testdata/cases/imports/reserved_prefix_decl.txtar`

**Interfaces:**
- Consumes: nothing from Tasks 2–3 beyond the aliases existing. Reuses the
  lowered per-file skeleton `*goast.File` that `buildPackageSkeletons`
  (`internal/codegen/unused_imports_syntactic.go:114`) already produces and
  stores as `packageSkeletons.skel`; `goast` is that file's alias for `go/ast`.
- Produces: `func checkReservedDecls(f *goast.File) []reservedDecl` where
  `type reservedDecl struct { Name string; Pos token.Pos }` — every package-scope
  binding whose name begins `_gsx`, with its position. Returning a slice (not an
  error) lets the caller report each one into the `diag.Bag` with a real
  `.gsx` position rather than collapsing to the first.

- [ ] **Step 1: Write the failing test**

`reserved_prefix_decl.txtar`:

```
# Context: the `_gsx` prefix is the generator's reserved identifier space — it is
# what lets a .gsx file bind gsx/context/io/strconv freely. A file-scope
# declaration in that space must be a clean diagnostic, not a silent collision
# with the generator's own import lines.
-- input.gsx --
package demo

var _gsxrt = 1

component Uses() {
	<b>hi</b>
}
-- diagnostics.golden --
3:5: error: declaration name "_gsxrt" uses the reserved _gsx prefix
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./internal/corpus -run 'TestCorpus/imports/reserved_prefix_decl' -count=1 2>&1 | tail -5
```

Expected: FAIL — either no diagnostic at all, or a raw Go `_gsxrt redeclared`
error rather than the clean gsx one.

- [ ] **Step 3: Implement `checkReservedDecls`**

Add to `internal/codegen/analyze.go`, beside `checkReservedParams`. It walks the
lowered skeleton `*goast.File` (already built by `buildPackageSkeletons`), so no
new scanner is written:

```go
// reservedDecl is one package-scope binding that intrudes on the generator's
// reserved identifier space, with the position to report it at.
type reservedDecl struct {
	Name string
	Pos  token.Pos
}

// checkReservedDecls reports file-scope names in the generator's reserved `_gsx`
// space. Every import the generator emits is `_gsx`-aliased (rtimports.go), which
// is exactly what allows a .gsx file to bind `gsx`, `context`, `io` or `strconv`
// to anything it likes. The whole collision surface therefore collapses onto this
// one prefix, and this is where it is defended. Params are checked separately by
// checkReservedParams; this closes package scope.
//
// Receivers are skipped: a method name lives in its receiver's namespace, not the
// package's, so it cannot collide with an import.
func checkReservedDecls(f *goast.File) []reservedDecl {
	var out []reservedDecl
	report := func(id *goast.Ident) {
		if id != nil && strings.HasPrefix(id.Name, "_gsx") {
			out = append(out, reservedDecl{Name: id.Name, Pos: id.Pos()})
		}
	}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *goast.FuncDecl:
			if d.Recv == nil {
				report(d.Name)
			}
		case *goast.GenDecl:
			for _, s := range d.Specs {
				switch s := s.(type) {
				case *goast.ImportSpec:
					report(s.Name) // nil for plain imports; `_`/`.` never match the prefix
				case *goast.ValueSpec:
					for _, n := range s.Names {
						report(n)
					}
				case *goast.TypeSpec:
					report(s.Name)
				}
			}
		}
	}
	return out
}
```

Wire it where the per-file skeleton is available. `checkReservedParams` is
invoked per component at `analyze.go:929`; this is per **file**, so call it once
where `packageSkeletons` yields each file's `skel`. Map each `reservedDecl.Pos`
back to a `.gsx` position through the same fset the skeleton was parsed with, and
record:

```go
	for _, rd := range checkReservedDecls(skel) {
		bag.Add(diag.Diagnostic{
			Severity: diag.Error,
			Message:  fmt.Sprintf("declaration name %q uses the reserved _gsx prefix", rd.Name),
			Source:   "codegen",
			Start:    gsxFset.Position(rd.Pos),
		})
	}
```

so the golden's `3:5:` prefix resolves. Confirm the exact `diag.Diagnostic` field
names against `internal/diag` before writing — `grep -n 'type Diagnostic' -A 12
internal/diag/*.go`.

- [ ] **Step 4: Run to verify it passes**

```bash
go test ./internal/corpus -run 'TestCorpus/imports/reserved_prefix_decl' -count=1 -v 2>&1 | tail -5
```

Expected: PASS.

- [ ] **Step 5: Confirm no existing case declares a `_gsx` name**

```bash
go test ./internal/corpus -run TestCorpus -count=1 2>&1 | tail -3
```

Expected: `ok`.

- [ ] **Step 6: `make check`**

```bash
make check
```

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "feat(codegen): reserve the _gsx prefix at file scope

Aliasing collapses the generator's whole collision surface onto one prefix, so
defend it: a top-level declaration or import named _gsx* is now a clean,
positioned diagnostic instead of a silent collision with a generated import line.
Params were already checked; this closes file scope."
```

---

## Task 5: Documentation

**Files:**
- Modify: `docs/guide/syntax/raw-go.md`
- Modify: `docs/ROADMAP.md` (tracked-debts entry → `[x]`)
- Modify: `docs/superpowers/specs/2026-07-08-gsx-alias-generator-imports-design.md` (two corrections)

No sibling-repo updates: this changes no syntax, so `../tree-sitter-gsx`,
`../vscode-gsx`, and `gsxhq.github.io` CodeMirror are unaffected.

- [ ] **Step 1: Document the rule in `docs/guide/syntax/raw-go.md`**

Add a section. Note the CLAUDE.md constraint: literal `{{ }}` in `docs/guide/**`
prose must be wrapped in a `::: v-pre` block. The text below contains none.

```markdown
## The `gsx` package is an ordinary import

Inside a `.gsx` file, `github.com/gsxhq/gsx` is a Go package like any other.
Reference `gsx.X` in your Go and you import it; don't, and you don't. An unused
import is an error, exactly as with `fmt`.

```gsx
package ui

import "github.com/gsxhq/gsx"

func wrap(n gsx.Node) gsx.Node { return n }
```

Markup does **not** make the import necessary. A component, an element literal,
or an `f"…"` literal needs nothing from you — the generated code reaches the
runtime through reserved `_gsx`-prefixed aliases you never see:

```gsx
package ui

// No import: no Go here names the gsx package.
var X = <div><b>hi</b></div>
```

Because generated code never uses the plain names, your file may bind `gsx`,
`context`, `io`, or `strconv` to whatever it likes:

```gsx
package ui

import gsx "strings"

component Shout(s string) {
	<b>{ gsx.ToUpper(s) }</b>
}
```

The one identifier space reserved for the generator is the `_gsx` prefix.
Declaring a file-scope name that begins `_gsx` is an error.
```

- [ ] **Step 2: Flip the ROADMAP entry**

In `docs/ROADMAP.md`, change the tracked-debts item from `[~]` to `[x]` and
rewrite the body to past tense, keeping the spec reference. Also add the
follow-up that this plan deliberately does not do:

```markdown
- [x] **`_gsx`-alias generator-emitted imports** - SHIPPED (2026-07-08). Every
  generator-emitted import (`gsx`, `context`, `io`, `strconv`) is recorded at its
  emission site and referenced through a reserved `_gsx` alias, so a `.gsx` may
  bind any of those names freely and a file with no gsx parts emits no import
  block. Removes the `gsx`/`strconv` reserved-param stopgap. Establishes the rule
  **"`gsx` is an ordinary Go package in `.gsx` source: reference it → import it;
  don't → don't"**. Also closed a corpus blind spot: `internal/corpus/batch.go`
  never compiled non-renderable cases, which is where all six non-compiling-output
  bugs hid. Spec `2026-07-08-gsx-alias-generator-imports-design.md`.
  **Deferred:** `gsx fmt` / LSP `source.organizeImports` do not *add* a missing
  `gsx` import (goimports mode uses `imports.Process` with `FormatOnly: true`,
  which skips usage-based add/remove); the type-check already knows the identifier
  is undefined, so this is a tractable follow-up against the organize-imports spec.
```

- [ ] **Step 3: Correct two claims in the spec**

The spec's D4 risk section over-estimated the triage surface. Measurement: the
only gen-pinned non-renderable case is `components/child_prop_attrs_reference`,
which is `diag(error)` and skipped. Replace:

```markdown
**Risk:** enabling it may surface pre-existing breakage in other non-renderable
corpus cases. Triage each explicitly — fix, or quarantine with a recorded reason.
Do not silence.
```

with:

```markdown
**Measured surface:** exactly one existing case is gen-pinned and non-renderable
(`components/child_prop_attrs_reference`), and it is `diag(error)`, which this
skips. Expect zero pre-existing breakage; the check exists to compile the *new*
cases. If a case does newly fail, triage it explicitly — fix, or quarantine with a
recorded reason. Do not silence.
```

And Risk 3 asks for something impossible (code and goldens must land together or
tests fail). Replace:

```markdown
3. **Golden regeneration hides a real regression.** Mitigate: regenerate in a
   commit that touches *only* goldens, and review its diff for anything other
   than `gsx.`→`_gsxrt.`, `context.`→`_gsxctx.`, `io.`→`_gsxio.`,
   `strconv.`→`_gsxsc.`, and removed import blocks.
```

with:

```markdown
3. **Golden regeneration hides a real regression.** Code and goldens must land in
   one commit (tests fail otherwise), so the mitigation is a filtered diff review:
   after `-update`, the golden diff must contain *nothing* except
   `gsx.`→`_gsxrt.`, `context.`→`_gsxctx.`, `io.`→`_gsxio.`,
   `strconv.`→`_gsxsc.`, and removed import lines. The plan gives the exact
   `git diff | grep -vE …` invocation. Investigate every surviving line.
```

- [ ] **Step 4: Verify the docs build is unaffected**

The CI `docs` job is not part of `make check`. It only matters for
`docs/guide/**`, which Step 1 touches:

```bash
grep -n '{{' docs/guide/syntax/raw-go.md || echo "no Vue interpolation hazard"
```

Expected: no `{{ }}` outside a `::: v-pre` block.

- [ ] **Step 5: `make ci` — the authoritative, uncached run**

```bash
make ci
```

Expected: exit 0. This is the gate before merging.

- [ ] **Step 6: Commit**

```bash
git add docs/
git commit -m "docs: gsx is an ordinary import; close the _gsx-alias roadmap item"
```

---

## Self-Review

**Spec coverage:**

| Spec section | Task |
|---|---|
| The rule (`gsx` is an ordinary Go package) | Task 5 Step 1 (docs); pinned by Task 2's `gsx_node_no_component` + `component_and_user_gsx`, and by the existing skeleton behavior (an unused user `gsx` import already errors) |
| D1 — need recorded at emission site | Task 2 Steps 3–6 (`rtImports` accessors; constant seed deleted) |
| D2 — always `_gsx`-alias | Task 2 Steps 6–7 |
| D2 — delete `emittedImportIdent` | Task 3 Step 3 |
| D2 — `_gsx` prefix reserved at file scope | Task 4 |
| D3 — user's own plain `gsx` import, one path two names | Task 2 Step 7 (`writeImports`), pinned by `component_and_user_gsx.txtar` |
| D4 — compile non-renderable corpus cases | Task 1 |
| Test matrix rows 1–15 | 1,2,3 → Task 2 (`no_gsx_parts`, `plain_go_only`, `f_literal_only`); 4,5 → Task 2 (`gsx_node_no_component`); 6 → Task 1 (`component_no_invoke`); 7 → Task 3 (`unused_gsx_import`); 8 → Task 3 (`alias_gsx_import`); 9–12 → Task 3 (`shadow_*`); 13 → Task 3 (`shadow_strconv`); 14 → Task 2 (`element_literal_var`); 15 → Task 3 (`param_named_gsx`) |
| Blank / dot imports of the gsx path | Task 3 (`blank_gsx_import`, `dot_gsx_import`) |
| Non-goal: auto-add imports | Task 5 Step 2 (recorded as deferred in ROADMAP) |

**Gaps found during self-review, fixed inline:**

- **Matrix row 7** (component + unused `import "…/gsx"`) had no task. Probing
  confirms it *already* errors today (`"github.com/gsxhq/gsx" imported and not
  used`) because the skeleton uses `_gsxrt` — but it is the case that **pins the
  rule**, and the rule is the point of the change. Added to Task 3 Step 1 as a
  `diag(error)` case.
- **Matrix row 8** (`import g "…/gsx"`, aliased and used) had no task. Added.
- **Blank and dot imports** were listed in the spec as unit tests with no task.
  Added as corpus cases instead — cheaper here, and they pin generated output,
  which a unit test would not.
- **`checkReservedDecls` had the wrong signature** (it took gsx's `ast.File` and
  invented a `topLevelNames` helper). Corrected to `*goast.File`, walking the
  lowered skeleton `buildPackageSkeletons` already produces, and returning
  `[]reservedDecl` so every offending name is reported rather than only the first.

**Type consistency:** `rtImports` (map type, value semantics — no pointer needed
for mutation), accessors `rt()`, `ctx()`, `io()`, `sc()`, constants
`gsxRuntimePath`, `rtAlias`, `ctxAlias`, `ioAlias`, `scAlias`. Used consistently
in Tasks 2, 3, 4. `writeImports`'s new third parameter `rt rtImports` matches the
call site in Task 2 Step 7. `checkReservedDecls(file *ast.File) error` in Task 4
mirrors `checkReservedParams(params []param) error`.

**Placeholder scan:** no TBD/TODO. Two steps intentionally say "locate with
`grep`" rather than listing all 43 signatures and 86 call sites — the greps are
exact and the compiler verifies completeness, which is stronger than an
enumeration that would go stale on the first rebase. Task 2 Step 4's `mergeExpr`
handling is the one place needing judgement; it is called out explicitly rather
than hand-waved.

**Known risk carried forward:** Task 2 Step 4 (`mergeExpr`) is the subtlest edit —
naming `gsx.DefaultClassMerge` eagerly in `generateFile` would record a runtime
need in *every* file and silently defeat `no_gsx_parts`. That case is the
regression test for exactly this mistake.
