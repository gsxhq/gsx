# gsx Examples Framework — Design

**Date:** 2026-06-24
**Status:** Approved (brainstorm) → ready for implementation plan

## Goal

Rebuild the docs **Examples** section as a curated, feature-by-feature gallery
where every example is **compiled and checked in CI** and **drift-free**: one
source file per example feeds the docs page, the playground presets, and the
render test. This permanently kills the class of bug we just hit (the
auto-escaping example differing between the frontend presets, the backend
cache-seed, and the corpus golden).

## Problem

Today an "example" is duplicated across three places that drift independently:

1. `GsxPlayground.vue` — a hardcoded `presets` array (`{name, source, invoke}`).
2. `playground/server/presets.go` — the backend response-cache seed list.
3. `internal/corpus/testdata/cases/playground/*.txtar` — the CI render goldens.

The auto-escaping example was fixed in (1) and (3) but left stale in (2). The
docs site also has a **broken "Examples" nav link** (it pointed at the removed
GitHub `examples/` folder), so there is no docs examples page at all.

## Decisions (from brainstorm)

- **Presentation:** a dedicated docs page; each example is prose + a
  syntax-highlighted gsx code block + an **"Open in Playground"** deep-link.
  The playground stays the one interactive surface (no embedded live editors,
  no inline rendered output).
- **Source of truth:** **single source → generated.** Examples are defined once
  as data; a generator emits the docs page and the playground presets; the
  corpus-style render harness checks them in CI.
- **Fixture location:** `examples/*.txtar` at the **gsx repo root** (reclaiming
  the removed folder), checked by a **dedicated test**.
- **Doc metadata lives inside the txtar** (a `-- doc --` section), so one file
  is the complete single source for an example: prose, code, invoke, golden.
- **Grouping:** the gallery is sectioned; the `doc` block carries a `category`
  so the generator emits section headings.
- **Multi-file examples:** an example may contain more than one `.gsx` file.
  txtar is already Go Playground's multi-file format (`-- file --` separators),
  and the corpus render harness already compiles multi-file cases. Multi-file
  examples stay in **one package (`views`)** so they remain playground-
  renderable; cross-package composition stays corpus-only. There is still
  exactly **one invoke** (one render entry point) per example. The "template
  composition" example (a shared component-library file + a page-method file)
  is the showcase for this.

## Architecture

```
examples/NN-slug.txtar          ← SINGLE SOURCE (per example)
  -- doc --        name / summary / category / order
  -- <file>.gsx -- one or more package-views source files
  -- invoke --     the single render expression
  -- render.golden -- expected HTML (CI-verified, -update regenerates)
        │
        ├── internal/corpus  (dedicated test)  → compile + `go run` → assert render.golden   [CI gate]
        │
        └── internal/examplegen  (generator, run via `go generate` / make)
                ├── docs/guide/examples.md            (docs page: prose + code + #try= link)
                ├── docs/examples.json                (frontend presets, synced to site)
                └── playground/server/examples.json   (backend cache-seed presets)
```

Site side (gsxhq.github.io):

```
sync-docs.mjs  copies docs/guide/** → guide/   (existing)
            +  copies docs/examples.json → .vitepress/theme/presets.generated.json   (NEW)
GsxPlayground.vue  import presets from './presets.generated.json'   (replaces hardcoded array)
.vitepress/config  "Examples" nav/sidebar → /guide/examples   (fix broken link)
```

## Components

### 1. Fixture format — `examples/NN-slug.txtar`

A txtar archive with a `-- doc --` block, one or more `.gsx` source files, an
`-- invoke --`, and a `-- render.golden --`. Single-file example:

```
-- doc --
name: Control flow
summary: if / else and loops that contribute markup, using the { … } brace forms.
category: Control flow
order: 40
-- input.gsx --
package views

component Inbox(name string, count int) {
	<section>
		{ if count > 0 { <p>Hi {name}, you have {count} messages.</p> } else { <p>Inbox zero.</p> } }
	</section>
}
-- invoke --
Inbox(InboxProps{Name: "World", Count: 2})
-- render.golden --
<section><p>Hi World, you have 2 messages.</p></section>
```

**Multi-file example** (the template-composition showcase — a shared component
library file + a page-method file, all `package views`, one invoke):

```
-- doc --
name: Template composition
summary: A shared component library composed by a page method — multiple files, one render entry.
category: Components & composition
order: 100
-- components.gsx --
package views

component Button(label string) { <button class="btn">{label}</button> }
component Card(title string) { <section class="card"><h2>{title}</h2>{children}</section> }
-- page.gsx --
package views

type HomePage struct{ Title string }

component (p HomePage) Render() {
	<main>
		<Card title={p.Title}><Button label="Save" /></Card>
	</main>
}
-- invoke --
HomePage{Title: "Dashboard"}.Render()
-- render.golden --
<main><section class="card"><h2>Dashboard</h2><button class="btn">Save</button></section></main>
```

- Each `.gsx` file includes its `package views` line (matches the playground
  editor `source`; the render server normalizes any package line to
  `package views`).
- All source files in an example share **one package** (`views`); the file
  names are flat (no `/`) so they land together in the playground's views dir.
- `category` groups the example under a section heading on the docs page and
  (optionally) in the dropdown; `order` sorts globally. The `NN-` filename
  prefix mirrors `order` for human sorting.
- `name` / `summary` / `category` are single-line values.

### 2. Loader extension — `internal/corpus/loader.go`

`loadCase`'s section switch currently routes any unrecognized section into
`c.files` (it would treat `doc` as a stray source file). Add a `doc` case:

- New `caseDoc` field `doc []byte` (raw `-- doc --` body).
- `case f.Name == "doc": c.doc = f.Data` (so it is NOT written to disk as a
  file during codegen).
- A small parser `parseDocMeta(c.doc) → docMeta{name, summary, category, order}`
  (simple `key: value` line scan; unknown keys ignored; missing `order` → 0).

Multi-file cases already work in the loader/harness (`c.files` keys multiple
`.gsx` files; `batchCodegen` compiles them together).

This change is inert for existing `testdata/cases` (none have a `doc` section).

### 3. Render test — `internal/corpus/examples_test.go`

A dedicated test (`package corpus`, so it can reuse `loadCase` + the proven
batch render harness in `batch.go`):

- Walk `../../examples/*.txtar`, `loadCase` each.
- Reuse `batchCodegen` + the single-`go run` batch render path that
  `TestCorpus` uses, asserting each case's `render.golden`.
- Honor the existing `-update` flag to regenerate `render.golden` in place.
- Fail if `examples/` is empty (guards against a broken glob).

This is the "compiled & checked" guarantee: every example is generated, type-
checked by the Go compiler, executed, and compared to its golden — by the same
pipeline that ships.

### 4. Generator — `internal/examplegen` + `cmd/gsx-examples`

A small command (wired to `//go:generate` and/or a `make examples` target) that:

1. Reads `examples/*.txtar`, parses `doc` + the `.gsx` source file(s) + `invoke`,
   sorts by `(order, filename)`.
2. Computes, per example, the **playground source string**:
   - Single file → the file's bytes verbatim.
   - Multiple files → the files joined in **Go-Playground txtar format**
     (`-- name.gsx --\n<bytes>\n-- name2.gsx --\n<bytes>`), sorted by file name.
     This is the exact form the render server splits and the editor displays.
3. Emits **`docs/guide/examples.md`**:
   - VitePress front-matter + a short intro paragraph.
   - Examples grouped under `## {category}` headings (categories in first-seen
     order by `order`).
   - Per example: `### {name}`, the `{summary}`, then **one ` ```gsx ` fenced
     block per source file** (each preceded by a small `**file.gsx**` caption
     when there is more than one file), and a link
     `[▶ Open in Playground](/playground#try={payload})`.
   - `payload = base64.StdEncoding(JSON)` where
     `JSON = {"s":<playground source string>,"i":<invoke>}`. This byte-for-byte
     matches the Vue decoder
     (`JSON.parse(b64decode(decodeURIComponent(hash)))` reading `o.s` / `o.i`,
     where `b64decode` is `atob` over UTF-8 bytes). Go `json.Marshal` of a
     struct with tags `s`,`i` produces the same `{"s":…,"i":…}` shape; standard
     base64 (`+`,`/`,`=`) is safe in a URL hash fragment.
4. Emits **`docs/examples.json`** and **`playground/server/examples.json`** with
   identical content:
   `[{ "name", "category", "source": <playground source string>, "invoke" }]`
   in sorted order.

The generator is deterministic. Generated artifacts are committed; a
`make examples` + `git diff --exit-code` check (optional CI guard) catches a
fixture edited without regenerating.

### 5. Backend cache-seed — `playground/server/presets.go`

Replace the hardcoded seed list with one that reads the generated
`playground/server/examples.json` via `//go:embed`. The server iterates the
embedded presets to warm the response cache (same `{source, invoke}` it serves),
so the backend seed can never drift from the docs/frontend again.

### 6. Frontend presets — `GsxPlayground.vue` + `sync-docs.mjs`

- `sync-docs.mjs`: after the guide sync, copy
  `<gsxsrc>/docs/examples.json` → `.vitepress/theme/presets.generated.json`.
  (Resolves `<gsxsrc>` the same way it already resolves the guide source:
  `GSX_DOCS_SRC` env → sibling `../gsx` → shallow clone.)
- `GsxPlayground.vue`: replace the hardcoded `const presets = [...]` with
  `import presets from './presets.generated.json'`. A checked-in
  `presets.generated.json` (committed once, regenerated by sync at build time)
  keeps the import resolvable for local dev.

### 7. Multi-file render — `playground/server/render.go` + editor

The render server currently writes the single `in.GSX` to one `comp.gsx`. Change
it to treat `in.GSX` as a **Go-Playground txtar source**:

- If `in.GSX` contains `-- name.gsx --` separators, parse it with the existing
  `internal/txtar` package and write each file into the views dir (file names
  are flat, single package; reject names containing `/` or `..` to keep writes
  inside the workspace). Each file's package line is normalized to
  `package views` as today.
- If there are no separators, behavior is unchanged (one `comp.gsx`).
- The render shim + single invoke are unchanged.

The cache key already hashes `(gsx, invoke)`, so multi-file sources cache
correctly with no key change.

**Editor (`GsxPlayground.vue`):** the single CodeMirror editor holds the
playground source string as-is, and the user can **edit multi-file sources
directly in the box** — exactly the Go Playground model: one text area whose
`-- file --` separator lines delimit the files (Go Playground has no tabs; the
separators *are* its multi-file UI). The server splits the source on those
separators at render time, so adding/removing a file is just editing text. This
multi-file editing is **in scope**. The only deferred enhancement is a richer
*tabbed* multi-file editor (which Go Playground itself doesn't have) — see
Scope. A small nicety included here: the separator lines get a subtle visual
treatment (a CodeMirror line decoration) so they read as dividers rather than
code.

### 8. Nav fix — `.vitepress/config.*`

Point the "Examples" nav item (and add a sidebar entry) at `/guide/examples`.

## The example set (initial)

Nineteen examples in six sections. Each borrows the canonical walkthrough of a
popular templating library and adds the gsx flavor. The existing five playground
corpus cases are migrated in (with `doc` sections); the rest are new. Slugs are
prefixed with `order` (`NN-slug.txtar`).

**§ Basics**

| order | slug | borrows from | gsx flavor |
|-------|------|--------------|-----------|
| 10 | interpolation | JSX/Vue/Jinja first example | compile-checked props struct |
| 20 | attributes | Vue `:attr` / JSX | `attr={cond}` lazy conditional + boolean attrs |
| 30 | auto-escaping | Jinja autoescape / Handlebars `{{{}}}` / JSX `dangerouslySetInnerHTML` | escaped by construction + `gsx.Raw`/`RawURL` |

**§ Control flow**

| order | slug | borrows from | gsx flavor |
|-------|------|--------------|-----------|
| 40 | if-else | Vue `v-if` / Jinja `if` | brace `{ if … else … }` form |
| 50 | loops | Vue `v-for` / Jinja `for` / Handlebars `each` | real Go `range`, loop-var binding |
| 60 | switch | templ `switch` / Go `html/template` | brace `{ switch … }` |

**§ Components & composition**

| order | slug | borrows from | gsx flavor |
|-------|------|--------------|-----------|
| 70 | components | everyone | typed props struct |
| 80 | children | JSX `children` / Vue default slot / templ `@children` | `{children}` of `gsx.Node` |
| 90 | named-slots | Vue named slots | typed named slots |
| 100 | template-composition *(multi-file)* | Jinja inheritance / templ layout / EJS layout | shared component lib file + page-method file, one invoke |
| 110 | fallthrough-attrs | JSX `{...props}` / Vue fallthrough attrs | caller-wins merge |
| 120 | method-components | templ method components | components as methods on a type |

**§ Styling**

| order | slug | borrows from | gsx flavor |
|-------|------|--------------|-----------|
| 130 | composable-class | Vue `:class` object / `clsx` | class merge + conditional classes |
| 140 | style-blocks | Vue `:style` / SFC scoped style | sanitized style, `RawCSS` opt-out |

**§ Transforming values**

| order | slug | borrows from | gsx flavor |
|-------|------|--------------|-----------|
| 150 | pipelines | Jinja filters `{{ x\|upper }}` / Handlebars helpers | typed filters from the `gsx info` registry |

**§ Interactive & whole-page**

| order | slug | borrows from | gsx flavor |
|-------|------|--------------|-----------|
| 160 | fragments | JSX/Vue fragments | multiple roots, no wrapper |
| 170 | forms | practical composite | labels, inputs, conditional state |
| 180 | js-and-islands | Alpine `x-data` / htmx | safe JS-attr handling + JSON data islands |
| 190 | full-document | doctype + page layout | `<!DOCTYPE html>` + full page |

Cross-package composition is intentionally excluded (the single-package
playground cannot render it). The old
`internal/corpus/testdata/cases/playground/*.txtar` cases are **removed**
(superseded by `examples/`); the `examples/` render test provides equivalent
(stronger) coverage.

## Data flow

1. Author/edit `examples/NN-slug.txtar` (the only file touched per example).
2. `go test ./internal/corpus/ -run Examples [-update]` verifies/regenerates the
   golden.
3. `go generate ./...` (or `make examples`) regenerates `examples.md` +
   the two `examples.json` artifacts.
4. Commit. The site build's `sync-docs.mjs` pulls `examples.md` into the guide
   and `examples.json` into the theme; the playground dropdown + the docs page
   are now identical, and the backend seed embeds the same data.

## Error handling

- **Generator:** a fixture missing `doc`, any `.gsx` source file, or `invoke` is
  a hard error (non-zero exit, names the file) — fail loud, never emit a partial
  page. A multi-file fixture with a source file outside `package views` (or a
  name containing `/`) is also an error.
- **Render test:** a compile error or golden mismatch fails the test with the
  case name and diff (existing batch-harness behavior).
- **Sync:** if `docs/examples.json` is absent at sync time, `sync-docs.mjs`
  errors (mirrors its existing "source missing" behavior) rather than silently
  shipping stale presets.

## Testing

- **`internal/corpus` examples render test** — every example compiles, runs, and
  matches `render.golden` (the CI gate).
- **`internal/examplegen` unit tests** — golden tests for the emitted
  `examples.md` and `examples.json` from a small fixture set (covering both a
  single-file and a multi-file example, and category grouping); a focused test
  that the generated `#try=` payload round-trips through the same decode logic
  the Vue uses (decode base64 → JSON → `{s,i}` equals the fixture's
  source/invoke), including the multi-file txtar-joined source.
- **Multi-file render test** — a `playground/server` test that posts a
  txtar-format multi-file source and asserts the composed HTML; and that a
  source with no separators still renders (unchanged path).
- **`parseDocMeta` unit test** — key/value parsing, missing-order default,
  unknown-key tolerance.
- **Manual:** build the site with the synced presets; confirm the Examples page
  renders, each "Open in Playground" link preloads the right source+invoke, and
  the dropdown matches.

## Scope / YAGNI

In scope: the fixture format (incl. multi-file + `category`) + loader hook, the
render test, the generator (sectioned docs page + both presets JSONs), the
backend embed-seed, the multi-file render-server split, the frontend import +
multi-file source display, the sync step, the nav fix, and the nineteen-example
set.

Multi-file editing in the playground box **is in scope** (Go-Playground-style
`-- file --` separators in one editor, fully editable, with a subtle divider
decoration).

Out of scope (future): a CI guard that fails on un-regenerated artifacts (nice
to have, can add later); inline rendered output on the docs page; embedded live
playgrounds; a richer **tabbed** multi-file editor (a per-file-tab UI on top of
the in-scope separator editing — Go Playground itself doesn't have this);
cross-package examples; per-example "edit history" or versioning.
