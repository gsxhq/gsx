# Attrs-Only Variadic Forwarding Binding Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make direct GSX markup returned from `func(attrs ...gsx.Attr) gsx.Node` forward `{ attrs... }` through a generated `gsx.Attrs` binding so the existing `Has`, `Class`, `Style`, and `Spread` lowering compiles and renders correctly.

**Architecture:** Preserve the author's variadic function signature, ordinary Go closure capture, and the shared element-forwarding emitter. At the actual element-spread boundary, inspect the analyzer-harvested static type for that `*ast.SpreadAttr`; when an accepted spread subject lacks the `gsx.Attrs` method set, evaluate it once into a generated temp through `gsx.Attrs(expr)`. Already-method-bearing bags retain their existing fast path, and unrelated identifiers named `attrs` are untouched.

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
- Create: `internal/corpus/testdata/cases/attrsonly/direct_factory_attrs_identifier.txtar`
- Create: `internal/corpus/testdata/cases/attrsonly/direct_factory_variadic_forwarding.txtar`
- Modify: `internal/codegen/emit.go`
- Generated: `internal/corpus/testdata/coverage.golden`

**Interfaces:**
- Consumes: the analyzer's `resolved[*ast.SpreadAttr]` type, `lookupMethod`, `spreadAttrExpr`, and `rtImports.rt()`.
- Produces: forwarding-leaf scaffolding that emits a generated `_gsxvN := _gsxrt.Attrs(expr)` temp only for an accepted spread subject whose static type lacks the `Has`/`Class`/`Style` method set.

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

- [ ] **Step 3: Pin unrelated `attrs` closure capture**

Add `direct_factory_attrs_identifier.txtar` with:

```go
func label(attrs string) gsx.Node {
	return <span>{attrs}</span>
}
```

Run it against the initial closure-wide fix and confirm RED: converting the unrelated string identifier to `gsx.Attrs` fails compilation.

- [ ] **Step 4: Add semantic spread-boundary normalization**

In `emitManualSpreadElement`, use `resolved[spread]` to distinguish a methodless accepted spread subject from an already-method-bearing `gsx.Attrs` value. Hoist and convert only the former:

```go
if spreadType := resolved[spread]; spreadType != nil && !hasAttrsMethodSet(spreadType) {
	bagExpr = fmt.Sprintf("%s.Attrs(%s)", rt.rt(), bagExpr)
	needsHoist = true
}
```

The generated temp preserves evaluation-once behavior for bare and derived spreads. Do not bind or shadow `attrs` at the closure level; ordinary interpolation and markup-local declarations must keep normal Go meaning and scope. Missing type information belongs to synthetic folded spreads that already produce `gsx.Attrs`, so they retain the established path.

- [ ] **Step 5: Regenerate the goldens and verify both focused corpus cases pass**

Run:

```bash
GOCACHE=/tmp/gsx-attrsonly-gocache go test ./internal/corpus -run 'TestCorpus/attrsonly/direct_factory_(attrs_identifier|variadic_forwarding)' -update -count=1
GOCACHE=/tmp/gsx-attrsonly-gocache go test ./internal/corpus -run 'TestCorpus/attrsonly/direct_factory_(attrs_identifier|variadic_forwarding)' -count=1
```

Expected: PASS. Inspect the positive generated golden and confirm `_gsxvN := _gsxrt.Attrs(attrs)` precedes generated `_gsxvN.Has`, `_gsxvN.Class`, `_gsxvN.Style`, and `Spread` calls. Confirm the negative golden interpolates `string(attrs)` without conversion. Confirm the positive render golden contains one caller-overridden `stroke-width` and one merged `class` attribute.

- [ ] **Step 6: Run codegen checks and the canonical corpus**

Run:

```bash
GOCACHE=/tmp/gsx-attrsonly-gocache go test ./internal/codegen ./internal/corpus -count=1
gopls check -severity=hint internal/codegen/emit.go
```

Expected: all tests pass and `gopls check` reports no new diagnostics.

- [ ] **Step 7: Commit the focused fix**

```bash
git add docs/superpowers/plans/2026-07-12-attrsonly-variadic-forwarding-binding.md internal/codegen/emit.go internal/corpus/testdata/cases/attrsonly/direct_factory_attrs_identifier.txtar internal/corpus/testdata/cases/attrsonly/direct_factory_variadic_forwarding.txtar internal/corpus/testdata/coverage.golden
git commit -m "fix(codegen): normalize attrs at spread boundary"
```
