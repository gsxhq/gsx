# Syntax Docs — "Syntax and usage" Basics (runnable, example-merged) — Design

- **Date:** 2026-06-29
- **Status:** Draft (awaiting review)
- **Worktree/branch:** `worktree-syntax-docs-basics`
- **Topic:** Restructure the gsx guide so syntax is taught as templ-style per-topic
  pages with subsections, where **examples are merged into the syntax prose** and
  **every example is runnable** (sourced from golden-tested `examples/*.txtar`).
  This spec covers **full "Syntax and usage" coverage** — every templ topic gets a
  gsx page or substantial section, adapted to gsx's mechanics (including the topics
  where gsx deliberately differs, e.g. Context, Raw Go). Each subsection gets its
  own focused, runnable example.

## Problem / motivation

templ's docs have a deep "Syntax and usage" section: one page per topic
(Elements, Attributes, Expressions, Statements, If/else, Switch, For, …), each
with subsections and worked examples. gsx's guide is comparatively thin and
**split awkwardly**:

- `docs/guide/syntax.md` — reference-style prose (tables, rules, terse fragments),
  no runnable output.
- `docs/guide/examples.md` — a flat, **generated** gallery (`cmd/gsx-examples` →
  `internal/examplegen` from `examples/*.txtar`), grouped by category, showing
  source `.gsx` + an "Open in Playground" link but **not** the rendered output.

The two overlap heavily by topic but differ in form, and examples live apart from
the syntax that introduces them. We want, per the user's direction:

1. **Per-topic syntax pages** with subsections (templ-style completeness).
2. **Examples merged into syntax** — examples are how each topic is taught, not a
   separate section.
3. **Every example runnable** — no rot-prone hand-pasted snippets; examples come
   from the canonical, golden-tested fixtures and show their real rendered output.

## Chosen approach (decided in brainstorming)

**Corpus-sourced, doc-grade cases.** Docs examples are `examples/*.txtar`
fixtures (the existing doc-grade tier, expanded). They are already compiled +
rendered + golden-checked end-to-end by `TestExamples`
(`internal/corpus/examples_test.go`) through the real pipeline. The doc generator
extracts the input + the rendered output + the playground link; pages interleave
those generated example blocks with hand-authored prose.

This keeps a **single source of truth** (the `.txtar`), makes examples **runnable
and CI-gated** (the `make ci-examples` drift check already fails on stale
generated artifacts), and lets us write **rich prose** by hand.

### Why not the alternatives

- *Doc-test harness over markdown* (examples authored in `.md`, a test renders
  each fence): a second runnable path parallel to the corpus; rejected to avoid
  divergence.
- *Playground-only embeds*: not CI-gated, depends on the hosted backend; kept as a
  per-example "Open in Playground" link, not the source of truth.

## Scope — full coverage

Every page below ships this pass. Topics where gsx diverges from templ still get a
real section that explains gsx's mechanism or stance — "ours may be different, but
we want the coverage." Each subsection gets one focused, runnable example.

Pages live under `docs/guide/syntax/`.

**A. Foundational syntax**

| Page | Subsections | Notes |
|---|---|---|
| `basic-syntax.md` | package & imports · `component` (header + body, no `return`) · element vs component (capitalization) | — |
| `raw-go.md` | `{ … }` Go statement block · `{{ stmt }}` GoBlock (position-disambiguated from `{{ }}` ordered-attrs) | gsx's "Raw Go" |
| `elements.md` | tags & nesting · void / self-closing · raw-text (`<pre>`, `<textarea>`) | — |
| `comments.md` | HTML `<!-- -->` (rendered) · Go `//` `/* */` (stripped) | — |
| `fragments.md` | `<>…</>` multi-root | — |
| `interpolation.md` | `{ expr }` · field access · func + `(T, error)` auto-unwrap · numeric/typed values | — |
| `attributes.md` | expression · boolean · conditional `{ if … { attr } }` · spread `{ x… }` (sorted) · **ordered `{{ "k": v }}`** · contextual escaping (URL/JS/CSS/JSON) | — |
| `control-flow.md` | `if` / `else` · `for … range` · `switch` / `default` · init statements | — |

**B. Components & composition**

| Page | Subsections |
|---|---|
| `composition.md` | calling components · `{children}` · named slots · cross-package use |
| `props.md` | BYO struct vs generated `<Name>Props` vs nullary · the heuristic · splat `{ x… }` |

**C. Assets & values**

| Page | Subsections |
|---|---|
| `styling.md` | (one page: CSS classes **and** style) composable `class` · `style` composition · **class/style merge** (caller-wins; per-property style dedup; Tailwind-aware `class_merger`) · `<style>` blocks `@{ }` |
| `javascript.md` | `@click` / `on*` · `gsx.RawJS` · `<script>` interpolation · JSON data islands |
| `pipelines.md` | `{ x \|> f \|> g:arg }` · `try` stage · per-context (text/attr/class/style) |

**D. Cross-cutting & reference**

| Page | Subsections / content |
|---|---|
| `raw-html.md` | `gsx.Raw` opt-out (≈ `templ.Raw`); security caveat |
| `escaping.md` | context-aware escaping table · all opt-out helpers · XSS-by-construction |
| `context.md` | components receive `ctx context.Context`; gsx favors explicit props; `ctx` in interpolation is a compile error — explain the stance (this is where gsx *differs*) |
| `std-functions.md` | runtime helper reference: `gsx.Raw` / `RawURL` / `RawJS` / `RawCSS`, `Attrs`, `OrderedAttrs`, `Node`, `Func`, `Join`, `Raw*` — what each is for |

**E. Notes & differences** (real sections; some short)

| Page | Content |
|---|---|
| `interop.md` | `gsx.Node ≡ templ.Component`; using gsx alongside `html/template` and React islands (brief, links out) |
| `render-once.md` | honest "not yet supported" — templ has `templ.Once`; gsx does not (yet). Document the gap + workaround |
| `forms.md` | short cookbook example (a reusable field forwarding `{ attrs… }`), not a CRUD tutorial |

**Ordered attributes (`{{ }}`)** is treated as **already merged** (per
`docs/superpowers/specs/2026-06-29-ordered-attrs-design.md`). It documents on the
Attributes page as a spread variant: `name={{ "k": v, … }}` produces an
order-preserving `gsx.OrderedAttrs` (for `data-*` directive ordering / Datastar),
contrasted with `{ bag… }` which renders sorted. Quoted-key rule, bool form
(`"data-show": true`), and the no-class/style-merge limitation are noted. The
body-position `{{ stmt }}` GoBlock is disambiguated by position (documented on the
Basic syntax page's "Go code blocks" subsection).

## Mechanism — generator + pages

### Doc metadata gains a `page` route (incremental migration)

`examples/*.txtar` `-- doc --` blocks gain two optional keys:

```
page: attributes      # which syntax page this example belongs to (slug)
pageOrder: 30         # position within that page (falls back to existing `order`)
```

`internal/examplegen` routing:

- **`page` set** → the example is emitted as a **generated partial** under
  `docs/guide/syntax/_generated/<page>/<order>-<slug>.md` and is **excluded** from
  the flat `examples.md` gallery.
- **`page` unset** → unchanged: the example flows into `examples.md` exactly as
  today.

This makes the migration **incremental**: this pass routes only the basics
examples; `examples.md` shrinks to the not-yet-migrated categories and is removed
in a later pass once empty. No "big bang" rewrite, and the existing
`category`/`order` fields and playground presets are untouched.

### Generated example partial = input + output + playground

Today `examplegen.RenderMarkdown` emits, per example: `### Name`, summary, one
` ```gsx ` block per source file, and the Playground link. It **does not** read
`render.golden`. The partial renderer extends this to also emit the **rendered
output**, giving the templ-style input→output pairing:

````
```gsx
component Link(url string, label string, external bool, featured bool) {
	<a href={url} data-count={3} aria-current={external} { if featured { class="featured" } }>{label}</a>
}
```

Renders:

```html
<a href="/p?q=a&amp;b" data-count="3" aria-current class="featured">Docs</a>
```

[▶ Open in Playground](/playground#try=…)
````

- The `html` block content is the `render.golden` section — already CI-verified by
  `TestExamples`, so the shown output cannot drift from what the compiler produces.
- The partial omits the `### Name` heading and summary by default (prose lives in
  the page); a `summary` may still be surfaced as a one-line caption. (Exact
  heading/caption policy settled during planning — see open questions.)

### Pages are hand-authored prose + includes

Each `docs/guide/syntax/<page>.md` is **hand-written** (headings, explanatory
prose, tables — full editorial control) and pulls example partials inline using
VitePress's markdown include:

```md
## Conditional attributes

An `{ if … { attr } }` block inside a tag contributes attributes only when the
condition holds:

<!--@include: ./_generated/attributes/30-conditional.md-->
```

- Prose: hand-authored, reviewable, templ-grade.
- Examples: generated, runnable, drift-gated.
- Partials are committed (like `examples.md` today) so the site builds without a
  codegen step, and the drift check guarantees they match the fixtures.

### Drift gating

`make ci-examples` (`Makefile:50`) currently regenerates and `git diff`s
`docs/guide/examples.md`, `docs/examples.json`, `playground/server/examples.json`.
It extends to also diff the generated partials tree
`docs/guide/syntax/_generated/`. A stale partial (e.g. a fixture changed but
partials not regenerated) fails CI exactly as a stale `examples.md` does today.

### Sidebar / nav (sibling repo)

The VitePress sidebar config lives in the **sibling `gsxhq/website` repo**, not in
this repo (this repo owns `docs/guide/**` markdown + the generator). Wiring the new
`syntax/` pages into the sidebar nav (a "Syntax and usage" group) is a
**website-repo change**, noted here and done as a follow-up there. Within this
repo we add the pages and the generator support; we do not block on the nav.

### Relationship to existing `syntax.md`

`syntax.md` stays as the **overview/reference hub** for this pass. Its
basics-relevant prose (element-vs-component rule, the `{ expr }` / attribute /
control-flow quick-reference rows, the escaping table) is the seed for the new
pages; the new pages own the authoritative, example-backed treatment, and
`syntax.md` links out to them. Its non-basics content (props model, spread/splat,
markup-vs-Go) remains in `syntax.md` until those topics get their own pages in a
later pass. No content is deleted that isn't reproduced.

## New `examples/*.txtar` to author

Each new fixture is **adapted from an existing, tested `cases/` corpus entry** (so
the syntax is already proven), given `-- doc --` metadata with a `page` route, an
`-- invoke --`, and a `render.golden` generated via
`go test ./internal/corpus -run TestExamples -update`. Candidate set (final list
settled in the plan):

- **basic-syntax:** a minimal `component` (greeting-style) + a Go code block
  (`{ … }`) example (from `control_flow/goblock`).
- **elements:** void/self-closing (`elements/static_and_bool_attrs_*`), HTML
  comment (`elements/html_comment`), raw-text element (`parser/16_raw_text`-style).
- **interpolation:** field access (`interpolation/field_access`), function call +
  `(T, error)` auto-unwrap (`attrs/attr_error_autounwrap` or an interp variant).
- **attributes:** spread `{ x… }` (`attrs/spread_trailing`), ordered-attrs
  `{{ … }}` (from the ordered-attrs corpus case once merged), one contextual-escape
  example (URL/JS) drawn from `security/`/`jsattr/`.
- **control-flow:** `if` with an init statement (`control_flow/if_init_error_handling`).

Existing reused fixtures (`10`, `20`, `30`, `40`, `50`, `60`, `160`) gain a `page`
route and, where helpful, are split or extended to cover the subsections above.

## Testing

- **`TestExamples`** (`internal/corpus/examples_test.go`) already compiles +
  renders every `examples/*.txtar` and pins `render.golden`. New fixtures are
  covered automatically; regenerate goldens with `-update`, verify without.
- **`internal/examplegen` unit tests** (`examplegen_test.go`) extend to cover: the
  `page` routing (routed vs gallery), the new output (`html`) block emission, and
  partial file paths/naming.
- **Drift:** `make ci-examples` extended to diff `docs/guide/syntax/_generated/`;
  `make check` / `make ci` stays green.
- **No hand-edited generated files** — partials and `examples.md` are generated;
  prose pages are hand-authored and contain only `<!--@include-->` directives for
  examples.

## Non-goals (this pass)

- Removing `examples.md` entirely — every routed example moves into a syntax page,
  but the generator keeps emitting `examples.md` for any still-unrouted example
  (expected to be empty/removed once all topics route, possibly within this pass).
- Any runtime/codegen change. `gsx.OrderedAttrs` / `{{ }}` is delivered by its own
  (in-flight) spec; here it is only documented. **Render-once** is documented as a
  known gap, not implemented.
- Deep CRUD / React / html-template tutorials — `interop.md`, `forms.md`,
  `render-once.md` are concise sections that link out, not full tutorials.
- A new config knob — nothing touches `gsx.toml` / `computeKey`.

## Resolved (during planning)

- **Partial format:** pure `gsx` + `html` + Playground-link triple. The page owns
  all headings and prose; partials carry no heading.
- **Granularity:** one focused fixture per subsection (decided). Existing
  multi-feature fixtures (e.g. `20-attributes`) are split into per-subsection
  fixtures.
- **VitePress include path:** native `<!--@include: ./_generated/<page>/<file>.md-->`,
  relative to the page. Confirmed against `gsxhq.github.io`: `sync-docs.mjs` does a
  recursive `cpSync` of `docs/guide/**`, so the `syntax/` + `_generated/` subtrees
  copy through intact.
- **`_generated/` routing:** to stop partials building as orphan pages, add a
  `srcExclude` glob (`'guide/**/_generated/**'`) to the website `config.mts`
  (bundled with the sidebar change). Build does not *fail* without it (links are
  valid), so repo-ordering is not a hard dependency — but ship both together.
- **Ordered-attrs timing:** the `{{ }}` fixture depends on the ordered-attrs branch
  merging. The Attributes-page ordered-attrs example is sequenced last; if that
  branch is not yet merged at execution time, the example is authored against the
  hand-written `gsx.OrderedAttrs{…}` form until the sugar lands, then updated.

## Open questions

- None blocking. (Per-page prose tone/length is an authoring detail, settled per
  page in the plan.)
