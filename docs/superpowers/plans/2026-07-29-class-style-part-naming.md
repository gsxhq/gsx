# Class and Style Part Naming Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make generated style composition use `Style`/`StyleIf`, keep its runtime part representation private, and give the shared compiler AST neutral composed-attribute names.

**Architecture:** The root runtime exposes semantic constructor functions over one unexported `conditionalPart`. The parser and compiler continue sharing one structural AST, renamed to `ComposedAttr`/`ComposedPart`; codegen chooses class or style constructors from the containing attribute.

**Tech Stack:** Go 1.26.1, gopls rename, GSX txtar corpus.

## Global Constraints

- Authored `.gsx` syntax and rendered HTML must not change.
- Runtime `conditionalPart` remains unexported; no compatibility alias for `ClassPart`.
- Generated class code uses `Class`/`ClassIf`; generated style code uses `Style`/`StyleIf`.
- Generated and render goldens are regenerated, never hand-edited.
- Implement directly on `main` as explicitly requested.

---

### Task 1: Private runtime part API

**Files:**
- Modify: `class.go`
- Modify: `class_test.go`
- Create: `class_external_test.go`

**Interfaces:**
- Produces: `Class(string) conditionalPart`, `ClassIf(string, bool) conditionalPart`, `Style(string) conditionalPart`, and `StyleIf(string, bool) conditionalPart`.
- Produces: class/style writer and string helpers accepting `...conditionalPart`.

- [ ] **Step 1: Add the external-package regression**

Add a `package gsx_test` test that calls:

```go
gsx.W(&style).Style(
	gsx.Style("color:red"),
	gsx.StyleIf("display:none", false),
)
gsx.W(&class).Class(
	gsx.DefaultClassMerge,
	gsx.Class("button"),
	gsx.ClassIf("active", true),
)
```

Assert the outputs are `color:red` and `button active`. This proves another
package can consume values of the private return type without naming it.

- [ ] **Step 2: Verify the new style constructors do not exist**

Run: `go test . -run TestGeneratedPartConstructorsAreExternallyUsable -count=1`

Expected: FAIL because `gsx.Style` and `gsx.StyleIf` are undefined.

- [ ] **Step 3: Replace exported `ClassPart` with `conditionalPart`**

Change every runtime helper in `class.go` to use the private type. Keep
`Class`/`ClassIf`, add symmetric `Style`/`StyleIf`, and update internal tests to
use `conditionalPart` where they name the variadic type.

- [ ] **Step 4: Run runtime tests**

Run: `go test . -count=1`

Expected: PASS.

### Task 2: Neutral compiler AST terminology

**Files:**
- Modify: `ast/*.go`
- Modify: `parser/*.go`
- Modify: `internal/codegen/*.go`
- Modify: `internal/lsp/*.go`
- Modify: `internal/printer/*.go`
- Modify: `internal/jsx/jsx.go`
- Regenerate: AST sections in canonical txtar cases

**Interfaces:**
- Produces: `ast.ComposedAttr`, containing `Parts []ComposedPart`.
- Produces: `(*ast.ComposedPart).ExprEnd() token.Pos`.

- [ ] **Step 1: Rename the AST types with gopls**

Use `gopls rename -w` at the declarations in `ast/ast.go`:

```text
ClassPart -> ComposedPart
ClassAttr -> ComposedAttr
```

Rename directly related private helpers and comments from `classPart` to
`composedPart`, including clone, print, navigation, analysis, and lowering
helpers. Do not rewrite historical design or plan documents.

- [ ] **Step 2: Update AST printer labels and focused tests**

Change printed node labels to `ComposedAttr` and `ComposedPart`, then update
parser/AST expectations through their owning tests or corpus regeneration.

- [ ] **Step 3: Run compiler-facing tests**

Run:

```bash
go test ./ast ./parser ./internal/codegen ./internal/lsp ./internal/printer ./internal/jsx -count=1
```

Expected: PASS.

### Task 3: Semantic style constructors in generated code

**Files:**
- Modify: `internal/codegen/emit.go`
- Modify: focused codegen tests as required
- Regenerate: `internal/corpus/testdata/cases/**/*.txtar`
- Regenerate: `examples/*.txtar`

**Interfaces:**
- Consumes: the runtime constructors from Task 1 and `ast.ComposedAttr` from Task 2.
- Produces: `_gsxrt.Style(...)` and `_gsxrt.StyleIf(...)` for every composed style part.

- [ ] **Step 1: Pin the expected generated style vocabulary**

Update the focused style corpus expectation only by running the corpus updater
after codegen changes; inspect `style/composed_attribute.txtar` and
`verbatim/class_style_targets.txtar` to confirm style uses `Style`/`StyleIf`
while class still uses `Class`/`ClassIf`.

- [ ] **Step 2: Select constructors by composed attribute kind**

In the shared composed-part lowering, emit:

```go
Class(...) / ClassIf(...) // class={...}
Style(...) / StyleIf(...) // style={...}
```

Apply this to plain expressions, CSS literals, value-form control flow, root
`StyleString`, component props, and Attrs folding.

- [ ] **Step 3: Regenerate canonical outputs**

Run:

```bash
go test ./internal/corpus -run TestCorpus -update
go test ./internal/gsxfmt -run TestFmtCorpus -update
```

Inspect generated diffs. Render goldens and formatter goldens must remain
byte-identical; only AST labels and generated Go vocabulary may change.

- [ ] **Step 4: Verify generated outputs**

Run:

```bash
go test ./internal/corpus -run TestCorpus -count=1
go test ./internal/gsxfmt -run TestFmtCorpus -count=1
```

Expected: PASS.

### Task 4: Repository verification

**Files:**
- Review: all modified source, tests, and generated corpus outputs
- Review: `docs/ROADMAP.md`

**Interfaces:**
- Consumes: Tasks 1-3.
- Produces: a verified repository state with no syntax or render change.

- [ ] **Step 1: Check renamed symbols and generated vocabulary**

Run searches proving no current Go source retains `ClassAttr`, `ClassPart`, or
style calls wrapping `Class`/`ClassIf`. Historical docs are excluded.

- [ ] **Step 2: Run static checks**

Run `gofmt` on modified Go files and:

```bash
gopls check -severity=hint <each-modified-go-file>
make lint
```

Expected: PASS.

- [ ] **Step 3: Run the authoritative gate**

Run: `make ci`

Expected: PASS.

- [ ] **Step 4: Review scope and commit**

Confirm `docs/ROADMAP.md` needs no change because syntax and capability are
unchanged. Review `git diff --check`, `git status --short`, and commit the
implementation with a focused message.
