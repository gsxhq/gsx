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

## Architecture

```
examples/NN-slug.txtar          ← SINGLE SOURCE (per example)
  -- doc --        name / summary / order
  -- input.gsx --  package views + component(s)
  -- invoke --     render expression
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

A txtar archive with four sections. Example:

```
-- doc --
name: Control flow
summary: if / else and loops that contribute markup, using the { … } brace forms.
order: 20
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

- `input.gsx` includes the `package views` line (matches the playground editor
  `source`; the render server already normalizes any package line to
  `package views`).
- `order` is an integer; it controls both the docs page order and the preset
  dropdown order. The `NN-` filename prefix mirrors it for human sorting.
- `name` / `summary` are single-line values.

### 2. Loader extension — `internal/corpus/loader.go`

`loadCase`'s section switch currently routes any unrecognized section into
`c.files` (it would treat `doc` as a stray source file). Add a `doc` case:

- New `caseDoc` field `doc []byte` (raw `-- doc --` body).
- `case f.Name == "doc": c.doc = f.Data` (so it is NOT written to disk as a
  file during codegen).
- A small parser `parseDocMeta(c.doc) → docMeta{name, summary, order}` (simple
  `key: value` line scan; unknown keys ignored; missing `order` → 0).

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

1. Reads `examples/*.txtar`, parses `doc` + `input.gsx` + `invoke`, sorts by
   `(order, filename)`.
2. Emits **`docs/guide/examples.md`**:
   - VitePress front-matter + a short intro paragraph.
   - Per example: `## {name}`, the `{summary}`, a ` ```gsx ` fenced block with
     `input.gsx`, and a link
     `[▶ Open in Playground](/playground#try={payload})`.
   - `payload = base64.StdEncoding(JSON)` where `JSON = {"s":<input.gsx>,"i":<invoke>}`.
     This byte-for-byte matches the Vue decoder
     (`JSON.parse(b64decode(decodeURIComponent(hash)))` reading `o.s` / `o.i`,
     where `b64decode` is `atob` over UTF-8 bytes). Go `json.Marshal` of a
     struct with tags `s`,`i` produces the same `{"s":…,"i":…}` shape; standard
     base64 (`+`,`/`,`=`) is safe in a URL hash fragment.
3. Emits **`docs/examples.json`** and **`playground/server/examples.json`** with
   identical content: `[{ "name", "source": <input.gsx>, "invoke": <invoke> }]`
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

### 7. Nav fix — `.vitepress/config.*`

Point the "Examples" nav item (and add a sidebar entry) at `/guide/examples`.

## The example set (initial)

Seven examples, each showcasing a distinct feature / ergonomic. The existing
five playground corpus cases are migrated into `examples/` (with `doc`
sections); two are added:

| order | slug | shows |
|-------|------|-------|
| 10 | interpolation | `{expr}` interpolation, props struct |
| 20 | control-flow | `{ if … else … }`, loops contributing markup |
| 30 | composable-class | composable `class` / `style` attributes |
| 40 | attributes | spread, boolean, and conditional attributes |
| 50 | components-and-slots | composition, `{children}`, named slots |
| 60 | pipelines | `{ x \|> upper }` / `truncate(n)` filter pipelines |
| 70 | auto-escaping | HTML-escaping by construction (the `<img onerror>` XSS demo) |

The old `internal/corpus/testdata/cases/playground/*.txtar` cases are **removed**
(superseded by `examples/`) to avoid a second copy. The `examples/` render test
provides equivalent (stronger) coverage.

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

- **Generator:** a fixture missing `doc`/`input.gsx`/`invoke` is a hard error
  (non-zero exit, names the file) — fail loud, never emit a partial page.
- **Render test:** a compile error or golden mismatch fails the test with the
  case name and diff (existing batch-harness behavior).
- **Sync:** if `docs/examples.json` is absent at sync time, `sync-docs.mjs`
  errors (mirrors its existing "source missing" behavior) rather than silently
  shipping stale presets.

## Testing

- **`internal/corpus` examples render test** — every example compiles, runs, and
  matches `render.golden` (the CI gate).
- **`internal/examplegen` unit tests** — golden tests for the emitted
  `examples.md` and `examples.json` from a small fixture set; a focused test
  that the generated `#try=` payload round-trips through the same decode logic
  the Vue uses (decode base64 → JSON → `{s,i}` equals the fixture's
  source/invoke).
- **`parseDocMeta` unit test** — key/value parsing, missing-order default,
  unknown-key tolerance.
- **Manual:** build the site with the synced presets; confirm the Examples page
  renders, each "Open in Playground" link preloads the right source+invoke, and
  the dropdown matches.

## Scope / YAGNI

In scope: the fixture format + loader hook, the render test, the generator
(docs page + both presets JSONs), the backend embed-seed, the frontend import,
the sync step, the nav fix, and the seven-example set.

Out of scope (future): a CI guard that fails on un-regenerated artifacts (nice
to have, can add later); inline rendered output on the docs page; embedded live
playgrounds; per-example "edit history" or versioning.
