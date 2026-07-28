# Preserve Bare Fallthrough Attributes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> superpowers:subagent-driven-development (recommended) or
> superpowers:executing-plans to implement this plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking.

**Goal:** Preserve the presence semantics of a syntactically bare attribute
when GSX lowers it into an attribute bag and forwards it across one or more
component boundaries.

**Architecture:** A bare attribute is authored presence syntax, independently
of whether its name appears in HTML's boolean-attribute table. Component and
folded-element bag lowering therefore stores `gsx.Toggle(true)`, preserving
that provenance until the leaf writer. Explicit boolean expressions remain
name-driven: custom/data/ARIA names stringify to `"true"`/`"false"` unless the
author explicitly uses `gsx.Toggle`.

**Tech Stack:** Go 1.26.1, GSX parser/AST/codegen, canonical txtar corpus,
`gopls`, `make check`, `make ci`.

## Global Constraints

- Work only in
  `/Users/jackieli/personal/gsxhq/gsx/.worktrees/preserve-bare-fallthrough`
  on `codex/preserve-bare-fallthrough`.
- Do not change parsing, formatting, the AST, or explicit `name={boolExpr}`
  semantics.
- A bare attribute emitted directly, forwarded through any number of
  components, placed in a conditional attribute branch, or folded with
  element spreads must remain a bare presence attribute at the leaf.
- The fix belongs at the source-provenance loss in bag lowering, not in
  `Attrs.Merge`, `Spread`, the boolean-name table, or a gsxui workaround.
- Use `gsx.Toggle(true)` as the bag value because it is the existing runtime
  representation of authored presence semantics.
- Add canonical corpus coverage for bare versus explicit-bool versus explicit
  Toggle behavior.
- Do not hand-edit generated corpus goldens; regenerate them with the owning
  corpus update command after recording RED.
- Run `gopls check -severity=hint` on every authored Go file changed.
- `make ci` is the authoritative final gate.

---

### Task 1: Preserve authored-bare provenance in attribute bags

**Files:**

- Modify: `internal/codegen/emit.go`
- Modify: `boolattr.go`
- Modify: `internal/corpus/testdata/cases/props/kebab_fallthrough.txtar`
- Create:
  `internal/corpus/testdata/cases/attrs/bare_presence_fallthrough.txtar`
- Modify: generated corpus goldens and `coverage.golden` through the corpus
  updater

**Interfaces:**

- Produces: `composeBag` lowering `*ast.BoolAttr` as
  `{Key: name, Value: <runtime>.Toggle(true)}`.
- Preserves: direct leaf `BoolAttr(name, true)` emission.
- Preserves: explicit custom/data/ARIA `name={boolExpr}` value semantics.

- [ ] **Step 1: Write the failing corpus expectations.**

  Restore `props/kebab_fallthrough.txtar` to expect:

  ```html
  <div full-width aria-label="Close" data-id="7"></div>
  ```

  Add `attrs/bare_presence_fallthrough.txtar` with:

  - a leaf component that spreads `attrs`;
  - a wrapper component that forwards `attrs` to the leaf;
  - a bare custom/data marker crossing both boundaries;
  - an explicit `data-valued={true}` that remains `data-valued="true"`;
  - an explicit `data-toggle={gsx.Toggle(true)}` that is bare;
  - a conditional bare marker;
  - a plain element whose multiple spreads force bag folding around a bare
    marker.

  Pin literal rendered HTML for all cases.

- [ ] **Step 2: Run the focused corpus and record RED.**

  Run:

  ```bash
  go test ./internal/corpus -run TestCorpus/props/kebab_fallthrough -count=1
  go test ./internal/corpus -run TestCorpus/attrs/bare_presence_fallthrough -count=1
  ```

  Expected: forwarded/folded bare custom attributes render `="true"` because
  `composeBag` currently stores an ordinary `bool`.

- [ ] **Step 3: Implement the source fix.**

  In the `*ast.BoolAttr` arm of `composeBag`, use the provided runtime package
  alias:

  ```go
  entries = append(entries, fmt.Sprintf(
      "{Key: %s, Value: %s.Toggle(true)}",
      strconv.Quote(t.Name),
      rtPkg,
  ))
  ```

  Update `boolattr.go` documentation to distinguish syntactically bare
  attributes from explicit boolean expressions and explain that `Toggle`
  carries authored presence semantics through a bag.

- [ ] **Step 4: Regenerate and verify focused behavior.**

  Run:

  ```bash
  go test ./internal/corpus -run TestCorpus -update
  go test ./internal/corpus -run \
    'TestCorpus/(props/kebab_fallthrough|attrs/bare_presence_fallthrough)' \
    -count=1
  go test ./... -run \
    'Test(Spread|Toggle|AttrAnyToggle|Corpus)' -count=1
  gopls check -severity=hint internal/codegen/emit.go boolattr.go
  ```

- [ ] **Step 5: Verify the downstream gsxui reproducer.**

  Temporarily point or replace gsxui's GSX dependency at this worktree and
  regenerate a minimal composed component:

  ```gsx
  component Leaf(attrs gsx.Attrs) {
    <div { attrs... }/>
  }

  component Wrapper(attrs gsx.Attrs) {
    <Leaf { attrs... } data-gsxui-slot-wrapper/>
  }
  ```

  Require `<div data-gsxui-slot-wrapper></div>`, never
  `data-gsxui-slot-wrapper="true"`.

- [ ] **Step 6: Run full gates and commit.**

  Run:

  ```bash
  make check
  make ci
  git diff --check
  ```

  Commit:

  ```bash
  git add internal/codegen/emit.go boolattr.go internal/corpus/testdata
  git commit -m "fix: preserve bare attributes through components"
  ```

## Self-review

- Root cause: `composeBag` erased `ast.BoolAttr` provenance by storing ordinary
  `bool(true)`.
- Fix location: the lowering boundary where provenance is lost.
- Explicit boolean-value behavior remains unchanged.
- Component, conditional, nested-forwarding, and folded-element paths are
  covered by rendered corpus output.
