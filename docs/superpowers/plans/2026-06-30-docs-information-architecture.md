# GSX Documentation Information Architecture Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rework the GSX documentation so it has a clear learning path, accurate status, stronger reference structure, and playground jump links without embedding live examples in content pages.

**Architecture:** Keep `gsx/docs/guide/**` as the authoritative documentation source, and keep `../gsxhq.github.io` as the VitePress publishing shell that syncs those source docs at build time. Documentation improvements land in small slices: source-of-truth cleanup, learning path, reference/navigation split, comparison/status pages, and site verification.

**Tech Stack:** Markdown, generated example includes from `internal/examplegen`, Go tests, VitePress in `../gsxhq.github.io`, Node/npm for site build.

---

## File Structure

- Modify `README.md`: align alpha/status language and public docs pointers with current shipped features.
- Modify `docs/index.md`: make it a concise docs landing page for repository readers and align it with the public site.
- Modify `docs/ROADMAP.md`: keep detailed project state, but make public-facing status easier to summarize from it.
- Modify `docs/guide/getting-started.md`: add a short learning path after the first successful run.
- Create `docs/guide/learn.md`: connected tutorial path for first component, props, children, attrs, class/style, and dev loop.
- Modify `docs/guide/syntax.md`: make this page explicitly a syntax reference hub, not the primary tutorial.
- Modify `docs/guide/syntax/render-once.md`: move gap-note framing out of the syntax learning path.
- Create `docs/guide/status.md`: public alpha/status page with shipped, partial, and deferred features.
- Create `docs/guide/comparisons.md`: explain GSX vs templ, html/template, JSX, and Jinja-style templates.
- Modify `docs/guide/performance.md`: add reproducibility metadata and exact benchmark command.
- Modify `docs/guide/editor.md`: align LSP feature status with current roadmap.
- Modify `docs/guide/cli.md`: align CLI status wording with current roadmap.
- Modify `docs/guide/syntax/attributes.md` and `docs/guide/syntax/std-functions.md`: verify current `gsx.Attrs` / `gsx.OrderedAttrs` text against runtime code and corpus, then update only if runtime semantics have changed.
- Modify `../gsxhq.github.io/.vitepress/config.mts`: restructure public nav/sidebar into Start, Learn, Reference, Tooling, Interop, Status.
- Modify `../gsxhq.github.io/index.md`: point home actions at the new learning path and status page.
- Leave `scripts/sync-docs.mjs` unchanged unless execution discovers contributors are editing generated `../gsxhq.github.io/guide/**` files directly; the current script already documents the sync direction.

## Out Of Scope

- Do not embed live interactive examples inside documentation pages.
- Do keep generated `Open in Playground` links emitted by the existing example pipeline.
- Do not move internal `docs/superpowers/**` specs/plans into the public VitePress site.
- Do not redesign the VitePress theme or playground component.

## Task 1: Baseline Verification

**Files:**
- Read: `attrs.go`
- Read: `orderedattrs.go`
- Read: `docs/ROADMAP.md`
- Read: `docs/guide/editor.md`
- Read: `docs/guide/cli.md`
- Read: `docs/guide/syntax/attributes.md`
- Read: `docs/guide/syntax/std-functions.md`
- Read: `../gsxhq.github.io/.vitepress/config.mts`

- [ ] **Step 1: Confirm clean worktrees**

Run:

```bash
git -C /Users/jackieli/personal/gsxhq/gsx status --short
git -C /Users/jackieli/personal/gsxhq/gsxhq.github.io status --short
```

Expected: no output, or only known user changes that are unrelated to docs IA. If there are unrelated user changes, leave them untouched.

- [ ] **Step 2: Confirm attribute runtime shape**

Run:

```bash
rg -n "type Attrs|type OrderedAttrs|type AttrMap|type Attr struct" attrs.go orderedattrs.go
```

Expected with the current source tree:

```text
orderedattrs.go:8:type Attr struct {
orderedattrs.go:17:type OrderedAttrs []Attr
attrs.go:20:type Attrs map[string]any
```

Use this result to decide whether `docs/guide/syntax/attributes.md` and `docs/guide/syntax/std-functions.md` need content changes. The docs must match runtime source and corpus, not an ignored synced copy in `../gsxhq.github.io/guide`.

- [ ] **Step 3: Confirm docs site sync direction**

Run:

```bash
sed -n '1,90p' /Users/jackieli/personal/gsxhq/gsxhq.github.io/scripts/sync-docs.mjs
```

Expected: the script copies or links `../gsx/docs/guide` into `../gsxhq.github.io/guide`. Treat `gsx/docs/guide/**` as authoritative.

- [ ] **Step 4: Confirm generated playground links already exist**

Run:

```bash
rg -n "Open in Playground" docs/guide/syntax/_generated | head
```

Expected: multiple generated snippets already include `[▶ Open in Playground](/playground#try=...)`. The implementation should preserve these jump links and avoid adding embedded live examples.

- [ ] **Step 5: Commit baseline if no changes were made**

No commit is needed for this task if it only reads files.

## Task 2: Public Status Source Of Truth

**Files:**
- Create: `docs/guide/status.md`
- Modify: `README.md`
- Modify: `docs/index.md`
- Modify: `docs/guide/cli.md`
- Modify: `docs/guide/editor.md`
- Modify: `docs/guide/syntax.md`

- [ ] **Step 1: Add `docs/guide/status.md`**

Create a public-facing status page with this structure:

```markdown
# Status

gsx is alpha software. It is usable end to end, but the language and APIs may still change before a stable release.

## Shipped

- `gsx init`, `gsx dev`, `gsx generate`, `gsx fmt`, `gsx info`, `gsx clean`, `gsx lsp`, `gsx version`, and `gsx help`.
- Component declarations, method components, generated props, bring-your-own props, `{children}`, named slots, and explicit attribute forwarding.
- Interpolation, control flow, attributes, contextual escaping, pipelines, `(T, error)` auto-unwrap, fragments, raw Go blocks, raw-text elements, composable `class`, element-level composable `style`, class/style merge, ordered attrs, and value-form `if`/`switch` in class/style lists.
- Vite-backed development loop with warm generation, server rebuild, browser reload, and browser error overlay.
- Language server diagnostics, go-to-definition, hover, references, formatting, and editor integration paths.

## Partial

- LSP completion and cross-package reference coverage are deferred.
- CLI `vet`, `render`, `explain`, and stable numeric diagnostic codes are deferred.
- Component-invocation `style={...}` composition and `[]string` class parts are deferred.

## Known Gaps

- There is no built-in render-once primitive equivalent to `templ.Once`.
- CSP nonce threading for emitted `<script>` and `<style>` is not implemented.

## Detailed Roadmap

The detailed engineering roadmap lives in [Roadmap & Status](../ROADMAP.md).
```

- [ ] **Step 2: Update `README.md` alpha paragraph**

Replace the stale paragraph at the top of `README.md` with:

```markdown
> **Status — alpha.** gsx is runnable end-to-end. `gsx init` scaffolds a Go and
> Vite application, `gsx dev` runs the warm generate/build/reload loop, and
> `gsx lsp` provides diagnostics, navigation, hover, references, and formatting.
> The language and APIs are usable but may still change before a stable release.
> See the [status](docs/guide/status.md) and [roadmap](docs/ROADMAP.md).
```

- [ ] **Step 3: Update `docs/index.md` alpha paragraph**

Use the same wording as `README.md`, with links relative to `docs/index.md`:

```markdown
> **Status — alpha.** gsx is runnable end-to-end. `gsx init` scaffolds a Go and
> Vite application, `gsx dev` runs the warm generate/build/reload loop, and
> `gsx lsp` provides diagnostics, navigation, hover, references, and formatting.
> The language and APIs are usable but may still change before a stable release.
> See the [status](./guide/status.md) and [roadmap](./ROADMAP.md).
```

- [ ] **Step 4: Update CLI status section**

In `docs/guide/cli.md`, make the final status block point at the new public status page:

```markdown
> **Alpha.** The shipped command set is listed above. Planned commands and
> deferred diagnostics work are tracked in [Status](./status.md) and the
> [Roadmap](https://github.com/gsxhq/gsx/blob/main/docs/ROADMAP.md).
```

- [ ] **Step 5: Update editor deferred-feature wording**

In `docs/guide/editor.md`, replace stale LSP limitations with:

```markdown
**Deferred:** completion and broader cross-package reference coverage. See
[Status](./status.md) and the
[roadmap](https://github.com/gsxhq/gsx/blob/main/docs/ROADMAP.md) for current scope.
```

- [ ] **Step 6: Link syntax page to status**

In `docs/guide/syntax.md`, replace the closing alpha note with:

```markdown
> **Status — alpha.** Syntax is stable enough for real projects, but not frozen.
> Follow [Status](./status.md) and the
> [roadmap](https://github.com/gsxhq/gsx/blob/main/docs/ROADMAP.md) before relying
> on deferred features.
```

- [ ] **Step 7: Verify links in source docs**

Run:

```bash
rg -n "status.md|guide/status|Roadmap|roadmap" README.md docs/index.md docs/guide/cli.md docs/guide/editor.md docs/guide/syntax.md docs/guide/status.md
```

Expected: each updated file links either to `docs/guide/status.md`, `./status.md`, or `./guide/status.md` with a valid relative path.

- [ ] **Step 8: Commit**

Run:

```bash
git add README.md docs/index.md docs/guide/status.md docs/guide/cli.md docs/guide/editor.md docs/guide/syntax.md
git commit -m "docs: add public status source of truth"
```

## Task 3: Learning Path Without Live Embedded Examples

**Files:**
- Create: `docs/guide/learn.md`
- Modify: `docs/guide/getting-started.md`
- Modify: `docs/guide/syntax.md`

- [ ] **Step 1: Create `docs/guide/learn.md`**

Create the page with this structure and concise examples:

````markdown
# Learn gsx

This path starts after `gsx init` and shows the core model in the order most projects use it: components, data, composition, attributes, styling, and the dev loop.

## 1. A component is Go plus markup

```gsx
package views

component Hello(name string) {
	<p>Hello, { name }</p>
}
```

`component` declares a Go function-like value. The body is the HTML result; there is no return type and no `return`.

## 2. Props are typed

```gsx
component Card(title string, featured bool) {
	<section class={ "card", "card-featured": featured }>
		<h2>{ title }</h2>
		{ children }
	</section>
}
```

The generator creates a props struct for call sites. Boolean props can use the bare attribute shorthand.

## 3. Components compose with children

```gsx
component Page() {
	<Card title="Dashboard" featured>
		<p>Welcome back.</p>
	</Card>
}
```

Nested content is available through `{ children }`, exactly where the component places it.

## 4. Attributes are explicit

```gsx
component Button(variant string) {
	<button class={ "btn", variant } { attrs... }>
		{ children }
	</button>
}
```

Undeclared call-site attributes are accepted only when the component explicitly spreads `{ attrs... }`.

## 5. Style and script stay close to HTML

```gsx
package views

import "fmt"

component Meter(value int, color string) {
	<div
		class={ "meter", "meter-full": value >= 100 }
		style={ fmt.Sprintf("width: %d%%", value), "color: " + color }
	/>
}
```

`class` and `style` support composable lists. Dynamic values are escaped or sanitized for their context.

## 6. The development loop is one command

```sh
npm run dev
```

The starter runs `go tool gsx dev`, which keeps generation warm, rebuilds the Go server, and reloads the browser through Vite.

## Next

- Keep the [syntax reference](./syntax) open while writing `.gsx`.
- Use the [playground](/playground) to try small components.
- Configure the dev loop, filters, minification, and class merging in [`gsx.toml`](./config).
- Check [Status](./status) before relying on alpha or deferred features.
````

- [ ] **Step 2: Update `docs/guide/getting-started.md` next steps**

Replace the final list with:

```markdown
## Where to go next

- Follow [Learn gsx](./learn) for the first component, props, children, attrs, and styling.
- Keep the [syntax reference](./syntax) open while writing `.gsx`.
- Use the [playground](/playground) for quick experiments.
- See the [`gsx dev` CLI reference](./cli#gsx-dev) for custom build, run, log,
  and front-door commands.
- Configure filters, asset processing, and the dev loop in
  [`gsx.toml`](./config).
```

- [ ] **Step 3: Update `docs/guide/syntax.md` intro**

Change the opening description so it does not present syntax as the first learning path:

```markdown
> **Syntax is roughly fixed, not frozen.** This page is the compact reference and
> topic hub. If you are new to gsx, start with [Learn gsx](./learn), then use this
> page while writing templates.
```

- [ ] **Step 4: Verify examples parse through existing docs/example tests**

Run:

```bash
go test ./internal/examplegen ./gen
```

Expected: pass. If `docs/guide/learn.md` contains examples that are not covered by tests, keep them short and copied from already-tested syntax forms.

- [ ] **Step 5: Commit**

Run:

```bash
git add docs/guide/learn.md docs/guide/getting-started.md docs/guide/syntax.md
git commit -m "docs: add guided learning path"
```

## Task 4: Reference Structure And Gap Placement

**Files:**
- Modify: `docs/guide/syntax.md`
- Modify: `docs/guide/syntax/render-once.md`
- Modify: `docs/guide/status.md`
- Modify: `docs/guide/performance.md`

- [ ] **Step 1: Rename syntax page heading text**

In `docs/guide/syntax.md`, change:

```markdown
# Syntax
```

to:

```markdown
# Syntax reference
```

Keep the file path unchanged to avoid breaking existing links.

- [ ] **Step 2: Keep render-once as status/reference material**

In `docs/guide/syntax/render-once.md`, change:

```markdown
# Render-once (gap note)
```

to:

```markdown
# Render-once
```

Then change the opening paragraph to:

```markdown
This page documents the current render-once status so applications can make an explicit layout choice today.
```

- [ ] **Step 3: Add render-once to status known gaps**

Ensure `docs/guide/status.md` links to the render-once page:

```markdown
- There is no built-in render-once primitive equivalent to `templ.Once`; see [Render-once](./syntax/render-once).
```

- [ ] **Step 4: Add reproducibility metadata to performance page**

In `docs/guide/performance.md`, after the opening paragraph and before `## Numbers`, add:

````markdown
## Reproduce

The benchmark source lives at [github.com/gsxhq/gsx-bench](https://github.com/gsxhq/gsx-bench).

```sh
git clone https://github.com/gsxhq/gsx-bench
cd gsx-bench
go test -bench . -benchmem
```

The numbers below are a snapshot from Apple M3 Ultra with Go 1.26.1. Treat them as directional; use the command above on your hardware for local decisions.
````

Then remove duplicate command wording from the final notes if it repeats the same command.

- [ ] **Step 5: Verify Markdown headings**

Run:

```bash
rg -n "^(#|##) " docs/guide/syntax.md docs/guide/syntax/render-once.md docs/guide/performance.md docs/guide/status.md
```

Expected: headings show `Syntax reference`, `Render-once`, `Reproduce`, and `Known Gaps`.

- [ ] **Step 6: Commit**

Run:

```bash
git add docs/guide/syntax.md docs/guide/syntax/render-once.md docs/guide/status.md docs/guide/performance.md
git commit -m "docs: clarify reference and gap pages"
```

## Task 5: Comparisons Page

**Files:**
- Create: `docs/guide/comparisons.md`
- Modify: `docs/guide/vision.md`
- Modify: `docs/guide/syntax/interop.md`

- [ ] **Step 1: Create `docs/guide/comparisons.md`**

Create:

```markdown
# Comparisons

gsx sits between Go template engines and JSX-like markup systems: templates stay close to HTML, data stays typed Go, and output compiles to ordinary Go.

## gsx and templ

Both gsx and templ compile components to Go values with `Render(ctx, w) error`. gsx differs in surface syntax: component declarations are templ-style, while the body is JSX-style markup. This makes HTML-like structure easier to scan while keeping structural compatibility with `templ.Component`.

Use gsx when you want JSX-like authoring inside Go projects. Use templ directly when you prefer templ's existing syntax, ecosystem, or render-once primitive.

## gsx and html/template

`html/template` is stable, standard-library, and excellent for string-template workflows. gsx adds typed components, generated props, compiler-checked composition, contextual escaping by construction, and a formatter/LSP path.

Use `html/template` when you need runtime-loaded templates or the standard package alone. Use gsx when templates are part of the compiled application.

## gsx and JSX

JSX makes markup part of JavaScript. gsx borrows the readable tag structure, but expressions are Go, components compile to Go, and there is no virtual DOM.

Use client-side JSX for interactive browser applications. Use gsx for server-rendered Go components, optionally with JavaScript islands.

## gsx and Jinja-style templates

Jinja-style templates provide a compact template language with filters, blocks, and inheritance. gsx instead leans on Go for data, functions, imports, and control flow, with pipelines for common formatting.

Use Jinja-style templates when a dynamic template language is the product requirement. Use gsx when compile-time checking and Go-native component composition matter more.

## Interop

See [Interop](./syntax/interop) for examples that compose gsx with templ, `html/template`, and client-side islands.
```

- [ ] **Step 2: Link from `vision.md`**

Add this sentence near the relationship discussion:

```markdown
For a practical side-by-side with templ, `html/template`, JSX, and Jinja-style templates, see [Comparisons](./comparisons).
```

- [ ] **Step 3: Link from interop page**

Add this sentence near the top of `docs/guide/syntax/interop.md`:

```markdown
For a higher-level choice guide, see [Comparisons](../comparisons).
```

- [ ] **Step 4: Verify comparison links**

Run:

```bash
rg -n "Comparisons|comparisons" docs/guide/vision.md docs/guide/syntax/interop.md docs/guide/comparisons.md
```

Expected: all three files contain links or headings.

- [ ] **Step 5: Commit**

Run:

```bash
git add docs/guide/comparisons.md docs/guide/vision.md docs/guide/syntax/interop.md
git commit -m "docs: add framework comparisons"
```

## Task 6: Attribute Docs Accuracy Check

**Files:**
- Verify: `attrs.go`
- Verify: `orderedattrs.go`
- Verify: `internal/corpus/testdata/cases/orderedattrs/**`
- Modify when runtime and docs disagree: `docs/guide/syntax/attributes.md`
- Modify when runtime and docs disagree: `docs/guide/syntax/std-functions.md`
- Modify when runtime and docs disagree: `docs/guide/syntax.md`

- [ ] **Step 1: Compare docs to runtime and corpus**

Run:

```bash
rg -n "type Attrs|type OrderedAttrs|type Attr struct" attrs.go orderedattrs.go
rg -n "OrderedAttrs|AttrMap|Attrs" internal/corpus/testdata/cases/orderedattrs examples docs/examples.json | head -80
```

Expected in the current runtime: `gsx.Attrs` is map-backed and sorted; `gsx.OrderedAttrs` is slice-backed and preserves order.

- [ ] **Step 2: Fix text only when it contradicts source**

If current runtime reports `type Attrs map[string]any`, leave the existing `Attrs` / `OrderedAttrs` model intact and make no commit for this task.

If current runtime reports `type Attrs []Attr`, update:

```text
docs/guide/syntax/attributes.md
docs/guide/syntax/std-functions.md
docs/guide/syntax.md
```

so the quick reference and runtime helper table describe the slice-backed model.

- [ ] **Step 3: Regenerate generated example docs if examples changed**

Run only if `examples/*.txtar` or `docs/examples.json` are edited:

```bash
go run ./cmd/gsx-examples
```

Expected: generated Markdown under `docs/guide/syntax/_generated/**` is updated consistently.

- [ ] **Step 4: Run focused tests**

Run:

```bash
go test ./internal/examplegen ./gen
```

Expected: pass.

- [ ] **Step 5: Commit if files changed**

Run:

```bash
git status --short
git add docs/guide/syntax/attributes.md docs/guide/syntax/std-functions.md docs/guide/syntax.md docs/guide/syntax/_generated docs/examples.json
git commit -m "docs: align attribute reference with runtime"
```

If `git status --short` shows no docs changes from this task, skip the commit.

## Task 7: VitePress Navigation And Homepage

**Files:**
- Modify: `../gsxhq.github.io/.vitepress/config.mts`
- Modify: `../gsxhq.github.io/index.md`

- [ ] **Step 1: Update VitePress nav**

In `../gsxhq.github.io/.vitepress/config.mts`, replace:

```ts
nav: [
  { text: 'Guide', link: '/guide/getting-started' },
  { text: 'Playground', link: '/playground' },
],
```

with:

```ts
nav: [
  { text: 'Start', link: '/guide/getting-started' },
  { text: 'Learn', link: '/guide/learn' },
  { text: 'Reference', link: '/guide/syntax' },
  { text: 'Playground', link: '/playground' },
],
```

- [ ] **Step 2: Restructure sidebar groups**

Replace the two existing sidebar groups with these groups:

```ts
{
  text: 'Start',
  items: [
    { text: 'Getting started', link: '/guide/getting-started' },
    { text: 'Learn gsx', link: '/guide/learn' },
    { text: 'Why gsx', link: '/guide/vision' },
    { text: 'Principles', link: '/guide/principles' },
    { text: 'Status', link: '/guide/status' },
  ],
},
{
  text: 'Reference',
  items: [
    { text: 'Syntax reference', link: '/guide/syntax' },
    { text: 'Basic syntax', link: '/guide/syntax/basic-syntax' },
    { text: 'Raw Go', link: '/guide/syntax/raw-go' },
    { text: 'Elements', link: '/guide/syntax/elements' },
    { text: 'Comments', link: '/guide/syntax/comments' },
    { text: 'Fragments', link: '/guide/syntax/fragments' },
    { text: 'Interpolation & expressions', link: '/guide/syntax/interpolation' },
    { text: 'Attributes', link: '/guide/syntax/attributes' },
    { text: 'Control flow', link: '/guide/syntax/control-flow' },
    { text: 'Components & composition', link: '/guide/syntax/composition' },
    { text: 'Props model', link: '/guide/syntax/props' },
    { text: 'Styling', link: '/guide/syntax/styling' },
    { text: 'JavaScript & scripts', link: '/guide/syntax/javascript' },
    { text: 'Pipelines & filters', link: '/guide/syntax/pipelines' },
    { text: 'Rendering raw HTML', link: '/guide/syntax/raw-html' },
    { text: 'Security & escaping', link: '/guide/syntax/escaping' },
    { text: 'Context', link: '/guide/syntax/context' },
    { text: 'Runtime helpers', link: '/guide/syntax/std-functions' },
    { text: 'Forms', link: '/guide/syntax/forms' },
  ],
},
{
  text: 'Tooling',
  items: [
    { text: 'CLI', link: '/guide/cli' },
    { text: 'Configuration', link: '/guide/config' },
    { text: 'Extensions', link: '/guide/extensions' },
    { text: 'Editor support', link: '/guide/editor' },
    { text: 'Performance', link: '/guide/performance' },
  ],
},
{
  text: 'Interop and Gaps',
  items: [
    { text: 'Comparisons', link: '/guide/comparisons' },
    { text: 'Interop', link: '/guide/syntax/interop' },
    { text: 'Render once', link: '/guide/syntax/render-once' },
  ],
},
```

- [ ] **Step 3: Update homepage actions**

In `../gsxhq.github.io/index.md`, change actions to:

```yaml
actions:
  - theme: brand
    text: Get started
    link: /guide/getting-started
  - theme: alt
    text: Learn gsx
    link: /guide/learn
  - theme: alt
    text: Playground
    link: /playground
```

Add a status link in the body:

```markdown
See [Status](/guide/status) for what ships today and what remains deferred.
```

- [ ] **Step 4: Build site**

Run:

```bash
cd /Users/jackieli/personal/gsxhq/gsxhq.github.io
npm run build
```

Expected: VitePress build completes successfully and pages under `/guide/learn`, `/guide/status`, and `/guide/comparisons` render.

- [ ] **Step 5: Commit in docs site repo**

Run:

```bash
cd /Users/jackieli/personal/gsxhq/gsxhq.github.io
git add .vitepress/config.mts index.md
git commit -m "docs: reorganize public site navigation"
```

## Task 8: Full Verification

**Files:**
- Read all changed docs files.
- Build in `../gsxhq.github.io`.

- [ ] **Step 1: Run Go tests covering docs examples**

Run:

```bash
cd /Users/jackieli/personal/gsxhq/gsx
go test ./internal/examplegen ./gen
```

Expected: pass.

- [ ] **Step 2: Run broader docs-relevant tests**

Run:

```bash
cd /Users/jackieli/personal/gsxhq/gsx
go test ./...
```

Expected: pass. If this is too slow or fails outside docs scope, record the failing package and exact error before deciding whether to fix it in this branch.

- [ ] **Step 3: Build VitePress site from synced source docs**

Run:

```bash
cd /Users/jackieli/personal/gsxhq/gsxhq.github.io
npm run build
```

Expected: build complete. The prebuild sync must copy `docs/guide` from the `gsx` repo.

- [ ] **Step 4: Inspect generated routes**

Run:

```bash
test -f /Users/jackieli/personal/gsxhq/gsxhq.github.io/.vitepress/dist/guide/learn.html
test -f /Users/jackieli/personal/gsxhq/gsxhq.github.io/.vitepress/dist/guide/status.html
test -f /Users/jackieli/personal/gsxhq/gsxhq.github.io/.vitepress/dist/guide/comparisons.html
```

Expected: all commands exit `0`.

- [ ] **Step 5: Check final worktree state**

Run:

```bash
git -C /Users/jackieli/personal/gsxhq/gsx status --short
git -C /Users/jackieli/personal/gsxhq/gsxhq.github.io status --short
```

Expected: only intentional changes remain, or no output if all task commits were made.

- [ ] **Step 6: Final summary**

Prepare a concise summary with:

```markdown
Changed:
- Source docs: status, learning path, reference structure, comparisons, performance reproducibility.
- Public site: navigation/homepage IA.

Verified:
- `go test ./internal/examplegen ./gen`
- `go test ./...`
- `npm run build` in `../gsxhq.github.io`
```

If any command was not run or failed, include the exact reason and output summary.

## Self-Review

- Spec coverage: The plan covers all accepted recommendations except embedded live examples, which are explicitly out of scope. Playground jump links are preserved through the generated example pipeline.
- Placeholder scan: The plan avoids unspecified implementation steps; file paths, commands, and expected outcomes are included.
- Type consistency: The attribute-doc task explicitly verifies runtime type shape before editing, avoiding stale copied-doc assumptions.
