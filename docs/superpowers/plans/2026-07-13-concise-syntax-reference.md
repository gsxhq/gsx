# Concise Syntax Reference Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rewrite the complete public syntax reference so each page states user-visible rules quickly, teaches through focused examples, and omits implementation and history baggage.

**Architecture:** Keep the current page structure, routes, generated runnable-example pipeline, and useful anchors. Rewrite only the hand-authored Markdown around those examples, grouping pages into independently reviewable conceptual slices. Finish by syncing the worktree into the VitePress publishing shell, building it, and reviewing representative pages in the browser.

**Tech Stack:** Markdown, VitePress includes, golden-tested `examples/*.txtar` partials, Go corpus tests, Node/npm, VitePress.

## Global Constraints

- Public sections follow **rule → normal example → result when useful → likely exceptions**.
- Each main concept gets at most one complete runnable include; minor variants use short snippets.
- Rendered output is shown only for behavior such as omission, escaping, ordering, or merge precedence.
- Keep current user-visible behavior, security boundaries, writing constraints, and realistic JSX, templ, or `html/template` patterns.
- Delete codegen, parser, resolver, module-analysis, lowering, migration, former-behavior, and unreleased-history explanations.
- Delete rare cases without a recognizable application pattern; do not relocate deleted baggage into another public page.
- Keep the current page structure and stable URLs unless a page remains difficult to scan after editing.
- Preserve useful anchors with explicit VitePress heading IDs when a heading is renamed.
- `docs/guide/**` is canonical. Never edit the ignored synced copies under `gsxhq.github.io/guide/**` by hand.
- Do not edit `docs/guide/syntax/_generated/**` or `examples/*.txtar` in this phase. Omit redundant includes rather than hand-editing generated artifacts.
- Literal `{{ }}` prose stays inside `::: v-pre` blocks.
- Accuracy comes from current source and corpus behavior, not existing prose.

---

## File Structure

- Modify `docs/guide/syntax/attributes.md`: attribute forms, bags, ordering, interpolation, and short escaping pointers.
- Modify `docs/guide/syntax/escaping.md`: security model, contexts, URLs, CSP nonces, and trust opt-outs.
- Modify `docs/guide/syntax/javascript.md`: JavaScript-valued attributes, framework directives, scripts, and JSON islands.
- Modify `docs/guide/syntax/raw-html.md`: the deliberate raw-HTML opt-out.
- Modify `docs/guide/syntax/composition.md`: calls, generics, children, slots, forwarding, precedence, and methods.
- Modify `docs/guide/syntax/props.md`: generated, bring-your-own, nullary, splat, and attrs-only props models.
- Modify `docs/guide/syntax/interpolation.md`: body interpolation, interpolating literals, typed values, and error unwrapping.
- Modify `docs/guide/syntax/pipelines.md`: filter syntax, arguments, whole-value pipelines, errors, and contexts.
- Modify `docs/guide/syntax/basic-syntax.md`: packages, component declarations, tags, and the lowercase wrapper pattern.
- Modify `docs/guide/syntax/raw-go.md`: Go blocks, elements in expression position, imports, and reserved names.
- Modify `docs/guide/syntax/elements.md`: nesting, void/raw-text/document elements, and element values.
- Modify `docs/guide/syntax/comments.md`: rendered and source-only comment forms.
- Modify `docs/guide/syntax/fragments.md`: multiple roots.
- Modify `docs/guide/syntax/control-flow.md`: `if`, `for`, `switch`, and init statements.
- Modify `docs/guide/syntax/context.md`: ambient context and the preference for typed props.
- Modify `docs/guide/syntax/styling.md`: composable class/style, merge behavior, Tailwind, selection, and style blocks.
- Modify `docs/guide/syntax/forms.md`: a concise form-field recipe and server-side validation pointer.
- Modify `docs/guide/syntax/interop.md`: common templ and `html/template` composition patterns.
- Modify `docs/guide/syntax/std-functions.md`: callable runtime helper reference without generated internals.
- Modify `docs/guide/syntax.md`: concise reference hub and quick-reference tables.
- Verification-only use of `/Users/jackieli/personal/gsxhq/gsxhq.github.io`: sync the ignored guide copy, run the VitePress build, and inspect rendered pages; do not commit website changes in this phase.

## Task 1: Establish the pattern on Attributes

**Files:**
- Modify: `docs/guide/syntax/attributes.md`
- Read: `attrs.go`
- Read: `internal/corpus/testdata/cases/attrs/**/*.txtar`
- Read: `examples/20-attributes.txtar`
- Read: `examples/230-boolean-attrs.txtar`
- Read: `examples/231-conditional-attrs.txtar`
- Read: `examples/232-spread-attrs.txtar`
- Read: `examples/233-ordered-attrs.txtar`
- Read: `examples/234-attr-contexts.txtar`
- Read: `examples/300-attr-interpolation.txtar`
- Read: `examples/301-attr-interpolation-url.txtar`

**Interfaces:**
- Consumes: the editorial contract in `docs/superpowers/specs/2026-07-13-concise-documentation-design.md`.
- Produces: the concrete page pattern used by every later task: short rule, existing runnable include, short variants, and links to owning pages.

- [ ] **Step 1: Capture the current public contract and inbound anchors**

Run:

```bash
rg -n '^#{1,4} |<!--@include:' docs/guide/syntax/attributes.md
rg -n 'attributes\.md#' docs/guide README.md
rg -n 'type Attr|type Attrs|type AttrMap|func \(.*Attrs' attrs.go
```

Expected: the current headings/includes, all source-doc links into the page, and the current public attribute types/methods. Record the existing anchors `spread-x-—-ordered`, `ordered-attrs-literal-k-v`, `targeting-the-synthesized-attrs-bag`, `contextual-escaping`, `interpolating-attribute-literals`, `on-a-component-tag`, `url-attributes-sanitize-the-whole-value`, `data-image-literals`, and `class-and-style-are-merge-targets` for preservation or link updates.

- [ ] **Step 2: Rewrite the page to this exact reader flow**

Use this heading structure and content ownership:

````markdown
# Attributes

Use quoted strings for static values, braces for Go expressions, and typed
literals when an attribute contains interpolated text, JavaScript, or CSS.

```gsx
<input name="email" value={value} required={required}/>
<a href=f`/users/@{id}`>Profile</a>
<button @click=js`save(@{id})`>Save</button>
<div style=css`color:@{color}`>...</div>
```

## Expression attributes
Use `name={expr}` to bind a Go value. Keep the existing 010 include, then show
`data-count={count}` as the numeric variant and state that quoted values do not
scan for `@{}` holes.

## Boolean attributes
A bare boolean attribute is always present. A boolean expression renders the
attribute when true and omits it when false. Keep the existing 020 include.

## Conditional attributes
Use `{ if cond { ... } }` inside an opening tag when a condition contributes
one or more attributes. Keep the existing 030 include and show
`{ if active { class="active" } else { class="idle" } }` as the short variant.

## Spread `{ x… }` — ordered
Spread a `gsx.Attrs` bag with `{ bag... }`; entries render in slice order. Keep
the existing 040 include. Follow it with four bullets: source order is kept;
boolean true is bare and false is omitted; spread values use the same escaping
as written attributes; `AttrMap.ToAttrs()` sorts map keys.

## Ordered-attrs literal <code v-pre>{{ "k": v }}</code>
Use `{{ "k": v }}` as the value of a component's `gsx.Attrs` prop when the
call site must declare the bag in source order. Keep the existing 050 include.
Follow it with five bullets: component attribute only; quoted keys; boolean
handling; last scalar wins; all class/style values compose.

### Targeting the synthesized attrs bag
Show `<Panel id="profile" { defaults... } attrs={{ "role": "region" }}/>`.
State three rules: ordinary attrs and spreads compose in source order;
`attrs={{...}}` is applied last; a call site may contain only one ordered
literal.

## Contextual escaping
State that ordinary values are attribute-escaped while URL attributes also
reject dangerous schemes. Keep the existing 060 include. Show the existing
two-line `js`/`css` example and link to Escaping and JavaScript for the complete
context rules.

## Interpolating attribute literals
Use `f` literals to combine static text with typed `@{expr}` holes. Keep the
existing 070 include. Explain the `f"..."` delimiter, `` \` ``, and `\@{` in
three short bullets.

### On a component tag
Show `<PageHeader title="Tickets" subtitle=f`@{count} tickets`/>` and state that
a matching name receives a string prop while an unmatched name falls through
to the component's attrs bag.

### URL attributes sanitize the whole value
State that static text and holes are assembled before the URL scheme check.
Keep the existing 080 include.

### `data:image` literals
Show `<img src=f`data:image/png;base64,@{b64}`/>` and
`<img src={imageBytes |> dataURL("image/png")}/>`; warn that a `data:` literal
is rejected on strict navigation sinks; link to Escaping.

### `class` and `style` are merge targets
State that interpolated class/style values merge with a forwarded attrs bag.
Keep the existing styling 040 include and link to Styling for precedence.
````

Delete references to generated `Attrs` fields, `gsx.ConcatAttrs`, eager `Merge`, adjacent literal runs, module analysis, dependency discovery, direct bag iteration, old behavior, internal diagnostics names, and generated-code outcomes. Keep the observable rules those details were trying to explain.

- [ ] **Step 3: Check the page for specification prose**

Run:

```bash
rg -n 'generated|codegen|lower|module analysis|eager|old |formerly|internal|implementation|ConcatAttrs|literal run' docs/guide/syntax/attributes.md
awk 'BEGIN{p=0} /^$/{if(p>80) print NR ": " p " words"; p=0; next} {p+=NF} END{if(p>80) print NR ": " p " words"}' docs/guide/syntax/attributes.md
```

Expected: no implementation/history matches except a public generated-props term required to explain a user choice, and no prose paragraph above 80 words. Split or delete any reported paragraph.

- [ ] **Step 4: Verify examples and Markdown**

Run:

```bash
make ci-examples
git diff --check
```

Expected: both commands pass; generated artifacts remain unchanged.

- [ ] **Step 5: Review and commit the slice**

Review the diff against the design spec, then run:

```bash
git add docs/guide/syntax/attributes.md
git commit -m "docs: make attributes reference example-led"
```

Expected: one commit containing only `attributes.md`.

## Task 2: Security, JavaScript, and Raw HTML

**Files:**
- Modify: `docs/guide/syntax/escaping.md`
- Modify: `docs/guide/syntax/javascript.md`
- Modify: `docs/guide/syntax/raw-html.md`
- Read: `escape.go`
- Read: `internal/attrclass/attrclass.go`
- Read: `internal/corpus/testdata/cases/security/**/*.txtar`
- Read: `examples/30-auto-escaping.txtar`
- Read: `examples/270-script-interpolation.txtar`
- Read: `examples/271-rawjs-handler.txtar`
- Read: `examples/273-alpine-search.txtar`
- Read: `examples/274-json-attributes.txtar`
- Read: `examples/290-raw-html.txtar`

**Interfaces:**
- Consumes: the Attributes page's short pointers to the owning security and JavaScript pages.
- Produces: one authoritative explanation for escaping/trust and one for JavaScript contexts, preventing later pages from repeating them.

- [ ] **Step 1: Verify the current security surface**

Run:

```bash
rg -n 'RawURL|RawJS|RawCSS|RawHTML|about:invalid|srcset|imagesrcset|nonce|data:' escape.go internal/attrclass internal/corpus/testdata/cases/security examples/{30,270,271,273,274,290}-*.txtar
rg -n '^#{1,4} |<!--@include:' docs/guide/syntax/{escaping,javascript,raw-html}.md
```

Expected: source/corpus evidence for every retained trust helper, URL rule, JavaScript/CSS context, CSP nonce behavior, and raw-HTML opt-out.

- [ ] **Step 2: Rewrite `escaping.md` around user decisions**

Use these sections:

```markdown
# Escaping
## Escape by default
## Contexts at a glance
## URL attributes
### Resource and navigation URLs
### `srcset` and `imagesrcset`
## JavaScript and CSS contexts
## CSP nonces
## Trusted-value helpers
```

Keep the existing auto-escaping include. Use one compact context table. Explain blocked schemes, image-only `data:` URLs, candidate-list behavior, explicit `js`/`css` literals, automatic context nonces, and the `Raw*` trust boundary. Delete comparisons to `html/template` internals, sanitizer implementation, static-fold routing, writer paths, exact lowering, and security development history.

- [ ] **Step 3: Rewrite `javascript.md` around common ecosystems**

Use these sections:

```markdown
# JavaScript
## JavaScript-valued attributes
## Alpine and htmx directives
## JSON attribute values
## `<script>` interpolation
## JSON data islands
```

Use the 030 handler include as the main attribute example, the 050 Alpine search include as the single advanced ecosystem example, the 055 JSON attribute include, the 020 script include, and the 010 data-island include. Remove the separate 040 Alpine dropdown include because it duplicates the full Alpine example. Replace repeated delimiter and escaping explanations with links to Attributes and Escaping.

- [ ] **Step 4: Reduce `raw-html.md` to the deliberate opt-out**

Keep `# Raw HTML`, `## Rendering trusted HTML`, the existing runnable include, and `## Security`. State that `gsx.Raw` requires already trusted or sanitized HTML, then link to Escaping. Do not add renderer or implementation detail.

- [ ] **Step 5: Verify and commit the security slice**

Run:

```bash
rg -n 'implementation|lower|codegen|writer path|static fold|formerly|old behavior|divergence' docs/guide/syntax/{escaping,javascript,raw-html}.md
make ci-examples
git diff --check
git add docs/guide/syntax/escaping.md docs/guide/syntax/javascript.md docs/guide/syntax/raw-html.md
git commit -m "docs: simplify escaping and JavaScript reference"
```

Expected: the text scan has no implementation/history prose, checks pass, and one focused commit is created.

## Task 3: Components, Composition, and Props

**Files:**
- Modify: `docs/guide/syntax/composition.md`
- Modify: `docs/guide/syntax/props.md`
- Read: `internal/corpus/testdata/cases/components/**/*.txtar`
- Read: `internal/corpus/testdata/cases/attrs/**/*.txtar`
- Read: `examples/70-components.txtar`
- Read: `examples/80-children.txtar`
- Read: `examples/90-named-slots.txtar`
- Read: `examples/100-template-composition.txtar`
- Read: `examples/110-fallthrough-attrs.txtar`
- Read: `examples/120-method-components.txtar`
- Read: `examples/122-generic-components.txtar`
- Read: `examples/124-generic-explicit-args.txtar`
- Read: `examples/250-byo-props.txtar`
- Read: `examples/251-props-heuristic.txtar`
- Read: `examples/252-splat.txtar`
- Read: `examples/305-attrs-forwarding-wrapper.txtar`

**Interfaces:**
- Consumes: Attributes as the owner of bag syntax and Escaping as the owner of trust semantics.
- Produces: concise composition/props pages that later hub and interop pages can link to.

- [ ] **Step 1: Verify each public composition form against corpus examples**

Run:

```bash
rg -n 'generic|TypeArgs|children|named slot|fallthrough|forward|method component|splat|attrs-only' internal/corpus/testdata/cases examples/{70,80,90,100,110,120,122,124,250,251,252,305}-*.txtar
rg -n '^#{1,4} |<!--@include:' docs/guide/syntax/{composition,props}.md
```

Expected: a current fixture or corpus case supports every retained user-facing form.

- [ ] **Step 2: Rewrite `composition.md` to this flow**

```markdown
# Composition
## Calling components
## Generic components
### Explicit type arguments
## Children `{children}`
## Named slots
## Cross-file and cross-package calls
## Explicit attribute forwarding
### Precedence
### Derived bags
## Forwarding through components
## Method components
```

Retain one existing include per main form. Reduce precedence to one source-order example plus three bullets: before spread is a default, after spread is forced, `class`/`style` compose. Keep derived bags as three short examples. Remove generated call shapes, resolver behavior, analyzed/unavailable dependency states, internal warnings, source-run coalescing, and provenance machinery.

- [ ] **Step 3: Rewrite `props.md` to this flow**

```markdown
# Props
## Choose a props model
## Bring your own struct
## Generated props
## Whole-struct splat
## Advanced case: attrs-only component values
```

Use one compact model table covering bring-your-own, generated, nullary, and attrs-only values. Keep the existing 010 example for bring-your-own, 020 for generated-model selection, and 030 for splat. Reduce attrs-only component values to the icon/factory use case familiar from JSX component factories: accepted public function shapes, no children, one short factory example. Delete static-type resolution algorithms, named-of-named slice discussion, generated conversions, diagnostic wording, and generated call expressions.

- [ ] **Step 4: Verify cross-links and commit**

Run:

```bash
rg -n 'generated call|codegen|resolver|module analysis|warning|diagnostic|underlying type|named-of-named|emits `|compiles to' docs/guide/syntax/{composition,props}.md
rg -n '\]\(\./(attributes|escaping|composition|props)\.md#[^)]+\)' docs/guide/syntax/{composition,props}.md
make ci-examples
git diff --check
git add docs/guide/syntax/composition.md docs/guide/syntax/props.md
git commit -m "docs: streamline composition and props reference"
```

Expected: no implementation/history prose, links target retained headings, checks pass, and one focused commit is created.

## Task 4: Interpolation and Pipelines

**Files:**
- Modify: `docs/guide/syntax/interpolation.md`
- Modify: `docs/guide/syntax/pipelines.md`
- Read: `examples/10-interpolation.txtar`
- Read: `examples/150-pipelines.txtar`
- Read: `examples/220-field-access.txtar`
- Read: `examples/221-func-error-unwrap.txtar`
- Read: `examples/222-error-unwrap-childprop.txtar`
- Read: `examples/280-filter-arguments.txtar`
- Read: `examples/281-pipeline-contexts.txtar`
- Read: `examples/303-body-interpolation-literal.txtar`
- Read: `examples/304-whole-literal-pipe.txtar`
- Read: `internal/corpus/testdata/cases/interpolation/**/*.txtar`
- Read: `internal/corpus/testdata/cases/pipelines/**/*.txtar`
- Read: `internal/corpus/testdata/cases/pipeerr/**/*.txtar`

**Interfaces:**
- Consumes: Attributes for attribute literals and Escaping for context behavior.
- Produces: the authoritative expression-transformation pages used by Styling and the syntax hub.

- [ ] **Step 1: Verify expression forms and error behavior**

Run:

```bash
rg -n 'auto-unwrap|T, error|pipeline|whole-literal|body.*literal|child.*prop|numeric' internal/corpus/testdata/cases examples/{10,150,220,221,222,280,281,303,304}-*.txtar
rg -n '^#{1,4} |<!--@include:' docs/guide/syntax/{interpolation,pipelines}.md
```

Expected: current cases for direct interpolation, body literals, fields, `(T, error)`, filter arguments, whole-literal pipelines, and context-specific output.

- [ ] **Step 2: Rewrite `interpolation.md`**

Use this structure:

```markdown
# Interpolation
## Go expressions
## Interpolating body literals
### Delimiters and literal `@{`
### Using a literal as a Go value
## Fields and typed values
## Functions returning `(T, error)`
### Component props
## Choosing braces
```

Keep the 010, 040, 020, 030, and 035 includes. Replace scanner/materialization/lowering prose with observable rules. End with a compact choice table for `{expr}`, `{f` literal `}`, element literals, and raw Go blocks; link to Attributes and Raw Go for full treatment.

- [ ] **Step 3: Rewrite `pipelines.md`**

Use this structure:

```markdown
# Pipelines
## Chain filters
## Pass filter arguments
## Pipe a whole interpolated literal
## Errors
## Context-aware output
```

Keep the 010, 020, 040, and 030 includes. Explain `try` and any-stage `(T, error)` in one Errors section. Keep URL sanitization and `dataURL` as short security variants linking to Escaping. Remove registry/codegen internals and repeated per-context escaping explanations.

- [ ] **Step 4: Verify and commit**

Run:

```bash
rg -n 'scanner|materializ|lower|codegen|generated|internal|implementation|formerly|old behavior' docs/guide/syntax/{interpolation,pipelines}.md
make ci-examples
git diff --check
git add docs/guide/syntax/interpolation.md docs/guide/syntax/pipelines.md
git commit -m "docs: condense interpolation and pipelines"
```

Expected: no implementation/history prose, checks pass, and one focused commit is created.

## Task 5: Language Fundamentals

**Files:**
- Modify: `docs/guide/syntax/basic-syntax.md`
- Modify: `docs/guide/syntax/raw-go.md`
- Modify: `docs/guide/syntax/elements.md`
- Modify: `docs/guide/syntax/comments.md`
- Modify: `docs/guide/syntax/fragments.md`
- Modify: `docs/guide/syntax/control-flow.md`
- Modify: `docs/guide/syntax/context.md`
- Read: `examples/40-if-else.txtar`
- Read: `examples/50-loops.txtar`
- Read: `examples/60-switch.txtar`
- Read: `examples/160-fragments.txtar`
- Read: `examples/190-full-document.txtar`
- Read: `examples/200-component-declaration.txtar`
- Read: `examples/201-lowercase-component-wrapper.txtar`
- Read: `examples/205-go-block.txtar`
- Read: `examples/210-void-elements.txtar`
- Read: `examples/211-raw-text.txtar`
- Read: `examples/215-html-comments.txtar`
- Read: `examples/216-tag-comments.txtar`
- Read: `examples/217-content-comments-preserved.txtar`
- Read: `examples/240-init-statement.txtar`
- Read: `examples/295-reading-context.txtar`

**Interfaces:**
- Consumes: the established concise page pattern and links to the owning expression/props pages.
- Produces: a complete beginner-facing foundation without duplicating advanced reference content.

- [ ] **Step 1: Verify current syntax and headings**

Run:

```bash
rg -n '^#{1,4} |<!--@include:' docs/guide/syntax/{basic-syntax,raw-go,elements,comments,fragments,control-flow,context}.md
go test ./internal/corpus -run 'TestExamples|TestCorpus' -count=1
```

Expected: tests pass and the current generated includes are accounted for.

- [ ] **Step 2: Rewrite the foundation pages with these boundaries**

Use these exact page responsibilities:

```text
basic-syntax.md: package/imports; component declaration; element vs component; lowercase wrapper as the only advanced case.
raw-go.md: GoBlock; GoBlock vs ordered attrs; elements in Go expression position; ordinary gsx import; reserved _gsx prefix as one warning.
elements.md: nesting; void elements; raw-text elements; full documents; elements/fragments as expression values; element-vs-component link.
comments.md: one opening table for the three positions; rendered HTML comments; source-only tag/content comments; Go comments outside markup.
fragments.md: multiple roots, one rule and the existing example.
control-flow.md: if/else; range; switch; init statements, each with its current include and no lowering discussion.
context.md: ambient ctx; typed helper example; two-sentence preference for explicit typed props for application data.
```

Keep every existing runnable include in these pages. Collapse repeated syntax descriptions after examples. Remove AST/parser position explanations beyond what a user needs to choose braces, formatter internals, generated-code discussion, and comparisons to unshipped behavior.

- [ ] **Step 3: Enforce concise paragraphs and verify**

Run:

```bash
for f in docs/guide/syntax/{basic-syntax,raw-go,elements,comments,fragments,control-flow,context}.md; do awk -v f="$f" 'BEGIN{p=0} /^$/{if(p>80) print f ":" NR ": " p " words"; p=0; next} {p+=NF} END{if(p>80) print f ":EOF: " p " words"}' "$f"; done
rg -n 'AST|parser|lower|codegen|generated code|implementation|formerly|old behavior' docs/guide/syntax/{basic-syntax,raw-go,elements,comments,fragments,control-flow,context}.md
make ci-examples
git diff --check
```

Expected: no paragraph exceeds 80 words, no implementation/history prose remains, and checks pass.

- [ ] **Step 4: Commit the foundation slice**

Run:

```bash
git add docs/guide/syntax/basic-syntax.md docs/guide/syntax/raw-go.md docs/guide/syntax/elements.md docs/guide/syntax/comments.md docs/guide/syntax/fragments.md docs/guide/syntax/control-flow.md docs/guide/syntax/context.md
git commit -m "docs: make syntax fundamentals answer-first"
```

Expected: one independently reviewable fundamentals commit.

## Task 6: Styling and Forms

**Files:**
- Modify: `docs/guide/syntax/styling.md`
- Modify: `docs/guide/syntax/forms.md`
- Read: `examples/130-composable-class.txtar`
- Read: `examples/140-style-blocks.txtar`
- Read: `examples/170-forms.txtar`
- Read: `examples/260-class-style-merge.txtar`
- Read: `examples/302-attr-interpolation-class-merge.txtar`
- Read: `internal/corpus/testdata/cases/class/**/*.txtar`
- Read: `internal/corpus/testdata/cases/style/**/*.txtar`

**Interfaces:**
- Consumes: Attributes precedence, Interpolation literals, and Escaping trust rules.
- Produces: one styling owner page and one small real-world form recipe.

- [ ] **Step 1: Verify class/style and form behavior**

Run:

```bash
rg -n 'class_merger|class=|style=|value.*switch|value.*if|attrs\.\.\.' examples/{130,140,170,260,302}-*.txtar internal/corpus/testdata/cases/{class,style}
rg -n '^#{1,4} |<!--@include:' docs/guide/syntax/{styling,forms}.md
```

Expected: current evidence for composable values, merge order, Tailwind-aware merging, value-form selection, style blocks, and form attrs forwarding.

- [ ] **Step 2: Rewrite `styling.md`**

Use this structure:

```markdown
# Styling
## Compose classes
## Compose inline styles
## Merge forwarded class and style {#class-style-merging}
### Tailwind-aware class merging
## Choose one value with `if` or `switch`
## `<style>` blocks
```

Keep the existing 010, 030, and style-block 040 includes. Use the interpolated-class 040 include as a short variant under merge behavior rather than another conceptual section. Keep one minimal `gsx.toml` snippet for `class_merger`. Remove merge implementation, route/fold distinctions, emitter details, and special-case history.

- [ ] **Step 3: Rewrite `forms.md`**

Keep `# Forms`, `## A reusable form field`, the existing form include, and `## Server-side validation is ordinary Go`. Limit the second section to one short handler/component boundary explanation and link to the relevant framework or standard-library validation approach without creating a CRUD tutorial.

- [ ] **Step 4: Verify and commit**

Run:

```bash
rg -n 'fold|emitter|writer|lower|codegen|route|implementation|formerly|old behavior' docs/guide/syntax/{styling,forms}.md
make ci-examples
git diff --check
git add docs/guide/syntax/styling.md docs/guide/syntax/forms.md
git commit -m "docs: simplify styling and forms guidance"
```

Expected: no internal/history prose, checks pass, and one focused commit is created.

## Task 7: Interop, Runtime Helpers, and Reference Hub

**Files:**
- Modify: `docs/guide/syntax/interop.md`
- Modify: `docs/guide/syntax/std-functions.md`
- Modify: `docs/guide/syntax.md`
- Read: `attrs.go`
- Read: `node.go`
- Read: `val.go`
- Read: `rawjs.go`
- Read: `rawcss.go`
- Read: `docs/guide/comparisons.md`

**Interfaces:**
- Consumes: final page titles, anchors, and content ownership from Tasks 1–6.
- Produces: the finished navigation hub and compact public API/interop pointers for the complete syntax reference.

- [ ] **Step 1: Verify public helper APIs and interop claims**

Run:

```bash
rg -n '^type |^func |^var ' attrs.go node.go val.go rawjs.go rawcss.go
rg -n 'templ|html/template|gsx\.Node|templ\.Component|Render' docs/guide/syntax/interop.md docs/guide/comparisons.md
rg -n '^#{1,4} ' docs/guide/syntax/*.md docs/guide/syntax.md
```

Expected: exact callable helper names/signatures, current structural interop claims, and the final page/heading inventory.

- [ ] **Step 2: Rewrite `interop.md` as recipes**

Use this structure:

```markdown
# Interop
## templ
### Render gsx from templ
### Render templ from gsx
### Children stay explicit
## `html/template`
### Render gsx into a template
### Render a template into gsx
## Client-side islands
```

Lead each direction with the smallest working adapter or direct-call snippet. Keep the real structural compatibility and trust-boundary caveats. Remove extended framework discussion, rationale, and hypothetical edge cases.

- [ ] **Step 3: Rewrite `std-functions.md` as a callable reference**

Use compact tables for:

```text
Core: Node and Func.
Trusted values: Raw, RawURL, RawJS, RawCSS.
Node values: Val, Text, Fragment, and the public value categories Val accepts.
Attribute bags: Attr, Attrs, AttrMap.ToAttrs, Class, Style, Get, Has, Without, Take, and Merge.
```

Remove the current “generated; rarely called directly” class/style helper section and all generated-helper names. Link to Attributes, Styling, and Escaping rather than restating semantics.

- [ ] **Step 4: Rewrite `syntax.md` as the concise hub**

Use this structure:

```markdown
# Syntax reference
State that this is the lookup reference for `.gsx` syntax and direct new users
to Learn gsx for a guided path.
## Start here
List, in order: Basic syntax, Elements, Interpolation, Attributes, Control flow,
Composition, and Props.
## More topics
Group links as Styling and scripts; Security and raw output; Go, context,
interop, and runtime helpers.
## Build constraints and `//go:` directives
State that file-level Go build directives apply to generated files; keep one
build-tag component-variant example and link to the Go build-constraint docs.
## Quick reference
Use four compact tables: declarations, body forms, attribute forms, and
component forms. Every row links to its owning page.
```

Remove paragraph-length syntax explanations already owned by topic pages. Preserve the public status link and useful quick-reference value.

- [ ] **Step 5: Verify all 20 public reference pages are linked**

Run:

```bash
for f in docs/guide/syntax/*.md; do slug=$(basename "$f" .md); rg -q "syntax/$slug|\./$slug" docs/guide/syntax.md || echo "missing from hub: $slug"; done
rg -n 'generated helper|codegen|lower|resolver|implementation|formerly|old behavior|migration' docs/guide/syntax/{interop,std-functions}.md docs/guide/syntax.md
git diff --check
```

Expected: no `missing from hub` output and no implementation/history prose.

- [ ] **Step 6: Commit the reference-support slice**

Run:

```bash
git add docs/guide/syntax/interop.md docs/guide/syntax/std-functions.md docs/guide/syntax.md
git commit -m "docs: finish concise syntax reference hub"
```

Expected: one commit completing the hand-authored syntax-reference rewrite.

## Task 8: Full Verification and Adversarial Review

**Files:**
- Review: `docs/guide/syntax.md`
- Review: `docs/guide/syntax/*.md`
- Verification-only sync/build target: `/Users/jackieli/personal/gsxhq/gsxhq.github.io`

**Interfaces:**
- Consumes: all rewritten pages and retained anchors from Tasks 1–7.
- Produces: a verified syntax-reference phase ready for PR/release handling and a concrete issue list for any follow-up edits.

- [ ] **Step 1: Run mechanical editorial checks**

Run:

```bash
for f in docs/guide/syntax.md docs/guide/syntax/*.md; do awk -v f="$f" 'BEGIN{p=0} /^$/{if(p>100) print f ":" NR ": " p " words"; p=0; next} {p+=NF} END{if(p>100) print f ":EOF: " p " words"}' "$f"; done
rg -n 'implementation detail|codegen|lowering|module analysis|resolver|formerly|old behavior|migration|backward compat|eager merge|writer path' docs/guide/syntax.md docs/guide/syntax/*.md
git diff --check
```

Expected: no paragraph above 100 words, no accidental implementation/history baggage, and no whitespace errors. Inspect every match rather than blindly deleting legitimate public terms.

- [ ] **Step 2: Run authoritative repository verification**

Run:

```bash
make ci-examples
make ci
```

Expected: both commands pass with no generated-example drift, test failure, formatting error, or lint failure.

- [ ] **Step 3: Sync the canonical worktree into the website shell**

First verify the website checkout is clean:

```bash
git -C /Users/jackieli/personal/gsxhq/gsxhq.github.io status --short
```

Expected: no output. Then run copy-mode sync from the documentation worktree:

```bash
GSX_DOCS_SRC=/Users/jackieli/personal/gsxhq/gsx/.worktrees/concise-docs npm run sync
```

Run from `/Users/jackieli/personal/gsxhq/gsxhq.github.io`.

Expected: `copied guide: .../.worktrees/concise-docs/docs/guide -> .../gsxhq.github.io/guide`. Do not use `--link`; linked guide trees previously produced invalid local routes.

- [ ] **Step 4: Build the website from the worktree docs**

Run from `/Users/jackieli/personal/gsxhq/gsxhq.github.io`:

```bash
GSX_DOCS_SRC=/Users/jackieli/personal/gsxhq/gsx/.worktrees/concise-docs npm run build
```

Expected: VitePress completes without dead links, Vue interpolation errors, missing includes, or WASM build failures.

- [ ] **Step 5: Review representative pages in the browser**

Start the built site:

```bash
npm run preview -- --host 127.0.0.1 --port 4173
```

Using the in-app browser, inspect these routes at desktop and narrow widths:

```text
http://127.0.0.1:4173/guide/syntax.html
http://127.0.0.1:4173/guide/syntax/attributes.html
http://127.0.0.1:4173/guide/syntax/escaping.html
http://127.0.0.1:4173/guide/syntax/composition.html
http://127.0.0.1:4173/guide/syntax/interpolation.html
http://127.0.0.1:4173/guide/syntax/basic-syntax.html
http://127.0.0.1:4173/guide/syntax/styling.html
```

For each route, verify: the rule appears before background prose; code blocks fit without broken layout; the page outline is scannable; generated examples render; previous/next links work; retained anchor links land on the intended section; mobile layout does not turn short sections into walls of text.

- [ ] **Step 6: Run the required independent adversarial review**

Give a fresh reviewer the design spec, this plan, the complete diff from `bf83ed2`, and the current corpus/source. Require the reviewer to challenge at least these probes:

```text
Attributes: mixed ordinary attrs, spread, conditional attrs, and attrs={{...}} precedence.
Escaping: dangerous URL, image data URL, srcset candidate, js/css literal, and Raw* opt-out.
Composition: before/after-spread precedence, nested forwarding, generic inference, and method calls.
Props: BYO vs generated discriminator, splat, attrs-only factory, and children restriction.
Interpolation/pipelines: per-hole vs whole-literal pipeline and any-stage (T, error).
Styling: class/style merge order and value-form if/switch.
```

Expected: a written list of factual errors, missing typical cases, unnecessary rare cases, duplicated explanations, broken anchors, or an explicit “no findings” with the probes performed. Fix every confirmed finding in the owning task's file and rerun Steps 1–5.

- [ ] **Step 7: Commit review fixes separately**

When confirmed review findings required edits, run:

```bash
git add docs/guide/syntax.md docs/guide/syntax/*.md
git commit -m "docs: address syntax reference review"
```

Expected: a focused review-fix commit. If the reviewer had no findings, create no empty commit.

- [ ] **Step 8: Confirm the final branch state**

Run:

```bash
git status --short
git log --oneline bf83ed2..HEAD
```

Expected: a clean worktree and the planned sequence of focused documentation commits.
