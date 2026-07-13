# Component `f`-Literal Values Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every component-position `f`-literal materialize as a Go string, assigning it to a matching declared prop or to the component's explicit `Attrs` bag, without changing optimized leaf-tag emission.

**Architecture:** Parser and AST stay unchanged. Codegen adds a component-boundary string materializer built on the existing typed-hole machinery, and extends the existing ordered child-prop evaluation pass so statement-producing holes cannot reorder sibling prop or bag expressions. The leaf-element `emitEmbeddedTextAttr` path remains separate and keeps direct segment writes plus `_gsxnum`/`IntInto` numeric emission.

**Tech Stack:** Go 1.26.1, `go/types`, GSX AST/codegen, canonical txtar corpus, `gopls`, `make check`/`make ci`.

## Global Constraints

- An `f`-literal always produces a built-in Go `string` at a component boundary; field matching only chooses declared prop versus explicit `Attrs` bag.
- A matching `gsx.Node` prop receives `gsx.Text(materializedString)`.
- `js` and `css` component attrs retain current behavior: hole-free values fall through to `Attrs`; hole-bearing values remain rejected.
- Leaf non-URL/no-whole-pipeline `f` attrs retain direct writes and numeric `_gsxnum` plus `IntInto`/`UintInto`/`FloatInto`; do not route them through component string materialization.
- URL, HTML, JS, and CSS escaping remains the responsibility of the eventual sink; component materialization returns an unescaped string.
- Preserve authored evaluation order and existing `(T, error)` short-circuit behavior across declared props, formatted holes, ordered-attrs pairs, and fallthrough contributors.
- No runtime dependency additions and no exported runtime API.
- Do not hand-edit generated `.x.go` or corpus golden sections; regenerate them from `.gsx`/txtar sources.
- Update `docs/ROADMAP.md` and verify the motivating `/Users/jackieli/work/one-learning-gsx/ui/feedback_pages.gsx` consumer.

---

### Task 1: Core component-boundary `f` string lowering

**Files:**
- Create: `internal/corpus/testdata/cases/components/component_f_literal_values.txtar`
- Modify: `internal/codegen/emit.go` (`propFieldEntry`, `childPropsLiteral`, and a component-only materialization helper beside `embeddedTextValueExpr`)
- Modify (generated): `internal/corpus/testdata/coverage.golden`

**Interfaces:**
- Consumes: `embeddedValueExpr`, `holeStringExpr`, `embeddedProbeSeed`, `matchField`, `validateMatchedField`, `nodeFields`, and existing child-prop generic inference fields.
- Produces: `componentEmbeddedTextValueExpr(...) (string, bool)` (name may be shortened only if its component-only scope remains explicit), plus `propFieldEntry` metadata that identifies a matched `EmbeddedText` field for later ordered evaluation.

- [ ] **Step 1: Add the failing canonical case without generated goldens**

Create this source-only case so the existing rejection is the RED signal:

```txtar
# An f-literal is a string at a component boundary. Attribute-name matching only
# decides whether that string enters a declared prop or the explicit Attrs bag.
# The final div is a leaf regression: its numeric hole must stay a direct IntInto
# write rather than materializing the component-style concat.
-- input.gsx --
package views

import "github.com/gsxhq/gsx"

component PageHeader(title string, subtitle string) {
	<header><h1>{title}</h1><p>{subtitle}</p></header>
}

component Field(label string) {
	<label>{label}<input { attrs... }/></label>
}

component Slot(body gsx.Node) {
	<section>{body}</section>
}

component Page(count int) {
	<PageHeader title="Tickets" subtitle=f`@{count} tickets`/>
	<Field label="Ticket" data-ticket=f`ticket-@{count}`/>
	<Slot body=f`@{count} open`/>
	<div data-count=f`@{count} direct`></div>
}
-- invoke --
Page(PageProps{Count: 3})
```

- [ ] **Step 2: Run the focused corpus test and verify RED**

Run:

```bash
GOCACHE=/tmp/gsx-component-fliteral-gocache go test ./internal/corpus -run 'TestCorpus/components/component_f_literal_values$' -count=1
```

Expected: FAIL because `subtitle=f\`@{count} tickets\`` is rejected by the current `unsupported-component-attr` path. Record the exact failure in the task report.

- [ ] **Step 3: Add component-only string materialization**

Add a helper beside `embeddedTextValueExpr` with this contract:

```go
func componentEmbeddedTextValueExpr(
	b *bytes.Buffer,
	a *ast.EmbeddedAttr,
	resolved map[ast.Node]types.Type,
	table funcTables,
	imports map[string]bool,
	rt rtImports,
	interpTemp *int,
	bag *diag.Bag,
	errReturn string,
) (string, bool)
```

Required body behavior:

```go
// 1. Assemble a.Segments with embeddedTextValueExpr/embeddedValueExpr.
// 2. With no a.Stages, return that built-in string expression unchanged.
// 3. With a.Stages, lower the assembled string through lowerPipe using the
//    errReturn-aware wrapper, unwrap only (T, error), apply a registered
//    renderer when present, and stringify the supported result with the same
//    string/bytes/numeric/Stringer categories as stringifyExpr.
// 4. Record filter/renderer imports and report positioned diagnostics through bag.
// 5. Never escape the returned string.
```

Do not modify or call this helper from `emitEmbeddedTextAttr`.

- [ ] **Step 4: Route `EmbeddedText` through normal component field matching**

In `childPropsLiteral`'s `*ast.EmbeddedAttr` arm, branch on `t.Lang` before the old static/reject logic:

```go
if t.Lang == ast.EmbeddedText {
	fn, isProp := matchField(declared, t.Name, fm)
	if isProp {
		if verr := validateMatchedField(fn, t.Name, propsType, declared); verr != nil {
			return nil, "", nil, &attrError{pos: t.Pos(), end: t.End(), code: "bad-field-match", msg: verr.Error()}
		}
	}
	// Probe mode uses a built-in string expression while holes/stages keep their
	// existing separate probes. Emit mode uses componentEmbeddedTextValueExpr.
	// A matched node field wraps with rtPkg.Text(value); a normal field assigns
	// value and sets inferField/inferArg; an unmatched name appends Value: value
	// to the explicit Attrs bag.
	continue
}
```

Keep the old static fallthrough and hole-bearing rejection text only for `EmbeddedJS`/`EmbeddedCSS`. A hole-free `f` literal must also match a declared field.

- [ ] **Step 5: Regenerate and inspect the corpus goldens**

Run:

```bash
GOCACHE=/tmp/gsx-component-fliteral-gocache go test ./internal/corpus -run 'TestCorpus/components/component_f_literal_values$' -update
```

Inspect the generated section and require all of these shapes:

```go
Subtitle: _gsxsc.FormatInt(int64(count), 10) + " tickets"
Attrs: _gsxrt.Attrs{{Key: "data-ticket", Value: "ticket-" + _gsxsc.FormatInt(int64(count), 10)}}
Body: _gsxrt.Text(_gsxsc.FormatInt(int64(count), 10) + " open")
_gsxgw.IntInto(_gsxnum[:], int64(count))
```

The exact reserved strconv alias may differ; the semantic shapes must not. The leaf line must not become a `FormatInt` concat.

- [ ] **Step 6: Verify GREEN and run focused static checks**

Run:

```bash
GOCACHE=/tmp/gsx-component-fliteral-gocache go test ./internal/corpus -run 'TestCorpus/components/component_f_literal_values$' -count=1
gofmt -w internal/codegen/emit.go
GOCACHE=/tmp/gsx-component-fliteral-gocache gopls check -severity=hint internal/codegen/emit.go
GOCACHE=/tmp/gsx-component-fliteral-gocache go test ./internal/codegen ./internal/corpus -count=1
```

Expected: focused corpus and both packages PASS; `gopls check` reports no new errors or unused helpers.

- [ ] **Step 7: Commit the core lowering**

```bash
git add internal/codegen/emit.go internal/corpus/testdata/cases/components/component_f_literal_values.txtar internal/corpus/testdata/coverage.golden
git commit -m "feat: materialize component f-literal values"
```

---

### Task 2: Ordered evaluation, pipelines, errors, and generic inference

**Files:**
- Create: `internal/corpus/testdata/cases/components/component_f_literal_order.txtar`
- Create: `internal/corpus/testdata/cases/components/component_f_literal_pipeline.txtar`
- Modify: `internal/codegen/emit.go` (`propFieldEntry`, child-prop ordered evaluation/rebuild, skipped-tag sink and attrs-only wrapper as required by the shared metadata)
- Modify (generated): `internal/corpus/testdata/coverage.golden`

**Interfaces:**
- Consumes: Task 1's `componentEmbeddedTextValueExpr` and component `EmbeddedText` routing.
- Produces: one source-ordered child-component value plan used by real emission; analyze/probe continues consuming final `propFieldEntry.str` plus `inferField`/`inferArg`.

- [ ] **Step 1: Add RED cases for order and string-result semantics**

`component_f_literal_order.txtar` must define side-effecting helpers and assert the authored order across an earlier declared prop, an `(int, error)` formatted hole, an ordered-attrs pair, an unmatched bag expression, and a later declared prop. Its rendered output must expose the call log and prove that an erroring hole prevents every later call.

Use these helper signatures and call labels verbatim:

```go
var calls []string

func mark(label, value string) string {
	calls = append(calls, label)
	return value
}

func markInt(label string, value int, fail bool) (int, error) {
	calls = append(calls, label)
	if fail { return 0, fmt.Errorf("%s failed", label) }
	return value, nil
}
```

The successful call log must be `first,hole,pair,bag,last`; the failing invocation must return `hole failed` and must leave the log at `first,hole`.

`component_f_literal_pipeline.txtar` must pin:

```gsx
component Generic[T any](value T) { <p>{value}</p> }
<Generic value=f`item-@{n}`/>
<Header subtitle={f`item-@{n}` |> upper}/>
```

Include a per-hole pipeline, a whole-literal pipeline returning `(string, error)`, and a non-error tuple case that expects the existing `invalid-tuple` diagnostic. The inferred generic instantiation must be `Generic[string]`.

- [ ] **Step 2: Run both focused tests and verify RED**

```bash
GOCACHE=/tmp/gsx-component-fliteral-gocache go test ./internal/corpus -run 'TestCorpus/components/component_f_literal_(order|pipeline)$' -count=1
```

Expected: at least the order case FAILS because statement-producing hole lowering currently escapes the existing ExprAttr/ordered-pair evaluation pass; pipeline/generic gaps may also fail. Record each relevant failure.

- [ ] **Step 3: Generalize the existing ordered child-value pass**

Replace the `ea`/`oa`-only “hoist-all-when-any” assumption with source-indexed value metadata. The concrete metadata must carry these facts (names may vary, fields may be split between `propFieldEntry` and a returned plan, but no fact may be omitted):

```go
type componentValueEntry struct {
	sourceIndex int
	node        ast.Node
	rawExpr     string
	fieldName   string
	isNodeField bool
	embedded    *ast.EmbeddedAttr
	bagIndex    int
	pairIndex   int
}
```

Required algorithm:

```go
// childPropsLiteral records authored attribute order without emitting
// statement-producing embedded lowering into the parent buffer.
//
// genChildComponent determines whether any value needs statements: tuple
// unwrap, erroring pipeline/renderer, AttrString, or embedded materialization.
// If so, walk every side-effecting componentValueEntry by sourceIndex, hoist
// calls in authored order, lower embedded holes in their own segment order,
// then rebuild declared fields, ordered-attrs literals, and the final Attrs
// composition from the resulting expressions/temps.
//
// A failure returns immediately before any later entry is evaluated.
```

Do not add a string-pattern heuristic. Use AST node kinds, `isCallExpr`, resolved tuple types, renderer metadata, and pipeline metadata already available to codegen.

Update `genSkippedTagSink` so it consumes every new planned expression inside its never-executed sink without running side effects. Update `attrsOnlyBagExpr` only where the shared metadata requires it; component-value attrs-only calls still have no declared fields.

- [ ] **Step 4: Preserve probe/emit equivalence and inference**

For probe mode:

```go
// No whole stages: inferArg is a built-in string seed ("" is sufficient).
// Whole stages: embeddedProbeSeed + probeExpr establishes the stage result,
// then the component-boundary probe expression must still have built-in string
// type because componentEmbeddedTextValueExpr stringifies that result.
// walkMarkupAttrs and walkEmbeddedAttrStages remain the only harvest walks.
```

Do not add a new AST walk or parser representation. Confirm explicit and inferred generic tag paths both receive `inferField`/`inferArg` for matched `f` values.

- [ ] **Step 5: Regenerate goldens and verify focused behavior**

```bash
GOCACHE=/tmp/gsx-component-fliteral-gocache go test ./internal/corpus -run 'TestCorpus/components/component_f_literal_(order|pipeline)$' -update
GOCACHE=/tmp/gsx-component-fliteral-gocache go test ./internal/corpus -run 'TestCorpus/components/component_f_literal_(values|order|pipeline)$' -count=1
```

Expected: all three cases PASS; generated order is visibly `first`, `hole`, `pair`, `bag`, `last`; the error path contains no later calls.

- [ ] **Step 6: Run codegen/package verification and static checks**

```bash
gofmt -w internal/codegen/emit.go
GOCACHE=/tmp/gsx-component-fliteral-gocache gopls check -severity=hint internal/codegen/emit.go
GOCACHE=/tmp/gsx-component-fliteral-gocache go test ./internal/codegen ./internal/corpus ./gen -count=1
```

Expected: PASS with no new `gopls` diagnostics.

- [ ] **Step 7: Commit the ordered lowering**

```bash
git add internal/codegen/emit.go internal/corpus/testdata/cases/components/component_f_literal_order.txtar internal/corpus/testdata/cases/components/component_f_literal_pipeline.txtar internal/corpus/testdata/coverage.golden
git commit -m "fix: preserve component f-literal evaluation order"
```

---

### Task 3: Documentation and one-learning dogfood proof

**Files:**
- Modify: `docs/ROADMAP.md`
- Modify: `internal/corpus/testdata/cases/components/embedded_attr_prop.txtar` (comments/coverage only if its blanket embedded-literal wording is stale)
- Modify in dogfood workspace: `/Users/jackieli/work/one-learning-gsx/ui/feedback_pages.gsx`
- Regenerate in dogfood workspace: `/Users/jackieli/work/one-learning-gsx/ui/feedback_pages.x.go`

**Interfaces:**
- Consumes: Tasks 1-2's completed generator behavior.
- Produces: current roadmap semantics and an effective one-learning generated call with `PageHeaderProps.Subtitle` populated from the direct tag.

- [ ] **Step 1: Update the canonical roadmap and stale corpus prose**

In `docs/ROADMAP.md` item 13, replace the blanket statement that a hole-bearing embedded literal is rejected on components with this semantic boundary:

```markdown
An `f`-prefixed literal on a component materializes as a string value: a name
matching a declared prop assigns that string to the prop, while an unmatched
name enters the component's explicit `Attrs` bag. Leaf non-URL attributes keep
their direct per-segment writes (including numeric scratch-buffer emission).
Hole-bearing `js`/`css` component attributes remain unsupported because their
contextual escaping belongs to an element sink.
```

Narrow `embedded_attr_prop.txtar` comments from “embedded literals always fall through” to `js`/`css` specifically if that wording is still present. Do not change its generated behavior.

- [ ] **Step 2: Verify docs/corpus changes**

```bash
GOCACHE=/tmp/gsx-component-fliteral-gocache go test ./internal/corpus -run 'TestCorpus/components/(embedded_attr_prop|component_f_literal_values)$' -count=1
git diff --check
```

Expected: PASS and no whitespace errors.

- [ ] **Step 3: Build the feature generator and regenerate the dogfood package**

From the GSX feature worktree:

```bash
GOCACHE=/tmp/gsx-component-fliteral-gocache go build -o /tmp/gsx-component-fliteral ./cmd/gsx
/tmp/gsx-component-fliteral -C /Users/jackieli/work/one-learning-gsx generate ./ui
```

Before editing, re-run `git status --short` in `/Users/jackieli/work/one-learning-gsx` and preserve every unrelated user change. In `ui/feedback_pages.gsx`, delete only this temporary fallback line:

```gsx
{ PageHeader(PageHeaderProps{Title: "My Feedback Tickets", Subtitle: fmt.Sprintf("%d tickets submitted", props.Count)}) }
```

If that deletion makes `fmt` unused in the file, remove only the now-unused `fmt` import. Run the generator again after the source edit.

- [ ] **Step 4: Verify the effective generated consumer**

Require `ui/feedback_pages.x.go` to contain the semantic equivalent of:

```go
_gsxgw.Node(ctx, PageHeader(PageHeaderProps{
	Subtitle: strconv.FormatInt(int64(props.Count), 10) + " tickets",
}))
```

Then run:

```bash
GOCACHE=/tmp/one-learning-component-fliteral-gocache go test ./ui -run 'TestFeedback|TestFilter' -count=1
```

If no matching tests exist, run `go test ./ui -count=1`. Expected: PASS. Do not commit the user's existing one-learning working-tree changes from the GSX branch.

- [ ] **Step 5: Run authoritative GSX verification**

From the GSX feature worktree:

```bash
GOCACHE=/tmp/gsx-component-fliteral-gocache make ci
```

Run outside the restrictive sandbox if localhost-sensitive or module-cache operations require it. Expected: all build, vet, test, drift, format, and lint gates PASS.

- [ ] **Step 6: Commit core documentation**

```bash
git add docs/ROADMAP.md internal/corpus/testdata/cases/components/embedded_attr_prop.txtar
git commit -m "docs: document component f-literal values"
```

Do not stage or commit files from `/Users/jackieli/work/one-learning-gsx` in the GSX repository.
