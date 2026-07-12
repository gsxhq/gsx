# Attrs-Only Variadic Forwarding Binding Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make direct GSX markup returned from `func(attrs ...gsx.Attr) gsx.Node` forward `{ attrs... }` through a generated `gsx.Attrs` binding so the existing `Has`, `Class`, `Style`, and `Spread` lowering compiles and renders correctly.

**Architecture:** Preserve the author's variadic function signature and the shared element-forwarding emitter. At the raw Go element-value closure boundary, when the markup references the exact `attrs` identifier, shadow the captured slice with `attrs := _gsxrt.Attrs(attrs)` before shared node emission; declared components keep their existing synthesized `gsx.Attrs` binding.

**Tech Stack:** Go 1.26.1, GSX code generator, canonical txtar corpus.

## Global Constraints

- The root `gsx` runtime remains standard-library only and dependency-free.
- Keep explicit attribute forwarding semantics unchanged: pre-spread scalar attributes are caller-overridable, post-spread scalar attributes are forced, and `class`/`style` merge.
- Do not rewrite the author's `func(...gsx.Attr) gsx.Node` signature.
- The regression must be pinned in `internal/corpus/testdata/cases/**/*.txtar` with generated and rendered goldens.
- Do not hand-edit generated `.x.go` files or generated golden contents; regenerate them with the corpus update command.
- This is a codegen-only correction with no syntax change, so sibling grammar/editor repositories are out of scope.

---

### Task 1: Normalize captured variadic attrs in element-value closures

**Files:**
- Create: `internal/corpus/testdata/cases/attrsonly/direct_factory_variadic_forwarding.txtar`
- Modify: `internal/codegen/emit.go`
- Generated: `internal/corpus/testdata/coverage.golden`

**Interfaces:**
- Consumes: `usesAttrs(nodes []ast.Markup) bool`, `rtImports.rt() string`, and `emitNodeFuncBody(...) bool`.
- Produces: element-value closure scaffolding that emits `attrs := _gsxrt.Attrs(attrs)` exactly when its markup references the exact captured identifier `attrs`.

- [ ] **Step 1: Add the failing canonical corpus case**

Create `internal/corpus/testdata/cases/attrsonly/direct_factory_variadic_forwarding.txtar` with a factory returning direct SVG markup:

```txtar
# A factory-produced attrs-only component may return direct markup from a
# variadic func. The generated render closure must normalize the captured
# []gsx.Attr parameter to gsx.Attrs before forwarding uses Has/Class/Style.
-- input.gsx --
package views

import "github.com/gsxhq/gsx"

func named(name string) func(...gsx.Attr) gsx.Node {
	return func(attrs ...gsx.Attr) gsx.Node {
		return <svg stroke-width="2" class="size-5" { attrs... }>{ name }</svg>
	}
}

var Search = named("search")

component Page() {
	<Search stroke-width="3" class="size-4"/>
}
-- invoke --
Page()
-- diagnostics.golden --
-- generated.x.go.golden --
-- render.golden --
<svg stroke-width="3" class="size-5 size-4">search</svg>
```

- [ ] **Step 2: Regenerate and verify the regression fails for the expected reason**

Run:

```bash
GOCACHE=/tmp/gsx-attrsonly-gocache go test ./internal/corpus -run 'TestCorpus/attrsonly/direct_factory_variadic_forwarding' -update -count=1
```

Expected: FAIL while compiling the generated case because `attrs` has type `[]gsx.Attr` and has no `Has`, `Class`, `Style`, or related methods. Retain the generated golden written by `-update`; it demonstrates the broken lowering.

- [ ] **Step 3: Add the minimal element-value binding**

In `emitNodeValue`, after opening the generated `gsx.Func` closure and before calling `emitNodeFuncBody`, add:

```go
if usesAttrs(nodes) {
	fmt.Fprintf(b, "\t\tattrs := %s.Attrs(attrs)\n", rt.rt())
}
```

Document that the inner render closure deliberately shadows the captured raw-Go binding. This converts variadic `[]gsx.Attr`, unnamed slice, named slice, or already-`gsx.Attrs` values to the method-bearing bag without changing the author's outer signature; `usesAttrs` prevents emitting an undefined or unused binding for unrelated element literals.

- [ ] **Step 4: Regenerate the goldens and verify the focused corpus case passes**

Run:

```bash
GOCACHE=/tmp/gsx-attrsonly-gocache go test ./internal/corpus -run 'TestCorpus/attrsonly/direct_factory_variadic_forwarding' -update -count=1
GOCACHE=/tmp/gsx-attrsonly-gocache go test ./internal/corpus -run 'TestCorpus/attrsonly/direct_factory_variadic_forwarding' -count=1
```

Expected: PASS. Inspect the regenerated `generated.x.go.golden` and confirm it contains `attrs := _gsxrt.Attrs(attrs)` before the generated `attrs.Has`, `attrs.Class`, `attrs.Style`, and `Spread` calls. Confirm the render golden contains one caller-overridden `stroke-width` and one merged `class` attribute.

- [ ] **Step 5: Run codegen checks and the canonical corpus**

Run:

```bash
GOCACHE=/tmp/gsx-attrsonly-gocache go test ./internal/codegen ./internal/corpus -count=1
gopls check -severity=hint internal/codegen/emit.go
```

Expected: all tests pass and `gopls check` reports no new diagnostics.

- [ ] **Step 6: Commit the focused fix**

```bash
git add docs/superpowers/plans/2026-07-12-attrsonly-variadic-forwarding-binding.md internal/codegen/emit.go internal/corpus/testdata/cases/attrsonly/direct_factory_variadic_forwarding.txtar internal/corpus/testdata/coverage.golden
git commit -m "fix(codegen): bind variadic forwarded attrs as Attrs"
```
