# gsx Documentation Groundwork Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stand up the documentation foundation for gsx — a status-honest README, a small set of public guide pages (philosophy well-covered, syntax kept light), a portable authoring skill, and a working VitePress site in a sibling repo that renders the guide pages and deploys to GitHub Pages.

**Architecture:** Two repos. `gsxhq/gsx` (this repo) owns authored Markdown under `docs/guide/`, the README, and `skills/gsx/`. A new sibling repo `gsxhq/website` (at `~/personal/gsxhq/website`) is a VitePress app that pulls `docs/guide/**` via a build-time sync script and deploys to Pages. The two develop together: edit `gsx/docs/guide/*`, the site picks it up on the next sync (symlinked for live reload locally, copied in CI).

**Tech Stack:** Markdown; Node + VitePress 1.x (default theme, local search) for the site; GitHub Actions for Pages deploy.

## Global Constraints

- **Status-honest copy.** Every surface (README, site home, guide pages, skill) must state: language design stable; parser/runtime/codegen-phase-1 done; **CLI WIP, gsx not yet runnable end-to-end**. Never imply `.gsx` files currently build.
- **Philosophy well-covered; syntax light.** `vision.md` and `principles.md` are substantial (the "why" is stable, drawn from the specs). `syntax.md` is deliberately lean and defers to `examples/` as the canonical syntax reference, with a "syntax not yet frozen" note.
- **`examples/` is canonical.** No invented syntax anywhere — every form shown must exist in an `examples/NN_*.gsx` file.
- **Module / org facts:** module is `github.com/gsxhq/gsx`; org repos live under `~/personal/gsxhq/`; runtime is standard-library only (generator/CLI may use `golang.org/x/tools`).
- **No LICENSE yet.** README states license TBD; do not add a LICENSE file (maintainer decision).

---

## File Structure

**Repo A — `gsxhq/gsx` (this repo):**
- Create: `README.md` — repo front door.
- Create: `docs/index.md` — lightweight docs landing for GitHub browsers.
- Create: `docs/guide/vision.md` — "Why gsx" (substantial).
- Create: `docs/guide/principles.md` — design philosophy (substantial).
- Create: `docs/guide/syntax.md` — light syntax overview, defers to examples.
- Create: `skills/gsx/SKILL.md` — portable authoring skill.

**Repo B — `gsxhq/website` (new, at `~/personal/gsxhq/website`):**
- Create: `package.json`, `.gitignore`, `scripts/sync-docs.mjs`, `.vitepress/config.mts`, `index.md` (home/hero), `README.md`, `.github/workflows/deploy.yml`.
- Generated (gitignored): `guide/` (synced from `gsx/docs/guide/`), `node_modules/`, `.vitepress/{cache,dist}/`.

---

## Task 1: Repo A — docs landing page

**Files:**
- Create: `docs/index.md`

- [ ] **Step 1: Write `docs/index.md`**

```markdown
# gsx documentation

gsx is a templating language for Go: **templ-style `component` declarations** with
a **JSX-style markup body**, compiled to plain Go.

```
.gsx → parser → AST → codegen → .x.go → go build → HTML
```

> **Status — alpha.** Language design is stable; the parser, runtime, and codegen
> phase 1 are done. The CLI is a work in progress, so gsx is **not yet runnable
> end-to-end**. See the [roadmap](./ROADMAP.md).

## Start here

- **[Why gsx](./guide/vision.md)** — the problem it solves and the bet behind it.
- **[Principles](./guide/principles.md)** — the design commitments.
- **[Syntax](./guide/syntax.md)** — a quick tour; the [`examples/`](../examples/)
  corpus is the canonical reference.

## Reference

- [Roadmap & status](./ROADMAP.md)
- [Design docs](./superpowers/specs/) — the internal specifications.
```

- [ ] **Step 2: Verify links resolve**

Run: `ls docs/ROADMAP.md docs/guide/vision.md docs/guide/principles.md docs/guide/syntax.md examples docs/superpowers/specs`
Expected: `docs/guide/*` will not exist yet (created in Tasks 2–4) — confirm only `docs/ROADMAP.md`, `examples`, and `docs/superpowers/specs` exist now; the guide links are forward references resolved by later tasks.

- [ ] **Step 3: Commit**

```bash
git add docs/index.md
git commit -m "docs: add docs landing page"
```

---

## Task 2: Repo A — "Why gsx" (vision)

**Files:**
- Create: `docs/guide/vision.md`

**Interfaces:**
- Source material: `docs/superpowers/specs/2026-06-18-gsx-templating-design.md` (Summary; "Relationship to templ, and the lessons borrowed") and `docs/ROADMAP.md` (status). Read these before writing; the prose below is a complete draft, refine for accuracy against the specs.

- [ ] **Step 1: Write `docs/guide/vision.md`**

```markdown
# Why gsx

Generating HTML from Go has always meant a trade-off. `html/template` ships in the
standard library and auto-escapes, but it is stringly-typed: templates parse at
runtime, errors surface late, and refactoring across templates is unsafe.
[templ](https://templ.guide) solved the type-safety problem by compiling templates
to Go — but its component syntax is its own, and an experimental branch that tried
to bring JSX-style inline components to templ ran into a wall.

gsx is a fresh take on that JSX-for-Go idea, designed around the lesson that broke
the first attempt.

## The bet: no symbol resolver

The experimental templ branch tried to map markup attributes onto *positional*
function parameters and to infer whether a lowercase tag was a component. Doing
that across packages drove it to a ~5,000-line symbol resolver on `go/packages`
and overlays — hitting overlay module-boundary bugs and performance cliffs.

gsx designs that whole class of work away:

- the **`component` keyword** identifies templates — no inference about what is a
  template;
- **capitalization** decides component-vs-element: `<div>` is HTML, `<Card>` is a
  component;
- gsx **generates every component's props struct**, so it always owns the field
  names.

What's left is plain Go the compiler type-checks. There is no symbol resolver to go
wrong.

## Lessons borrowed from templ

1. **Symbol resolution is the tar pit** — so gsx designs it away (above).
2. **Find Go boundaries; don't re-parse Go.** templ locates where a Go expression
   ends instead of reimplementing a Go parser. gsx does the same for embedded
   expressions and hands the rest to the real `go/parser`.
3. **Markup-vs-Go detection is subtle.** `{ <div/> }` (markup) versus `{ a < b }`
   (Go) is resolved positionally — the same rule Babel uses for JSX.

## Type-safe by construction

Because components lower to plain Go and props are generated structs, a wrong prop
name or type is a **compile error with a real source location** — gsx emits
`//line` directives back to the `.gsx` — not a runtime surprise. Interpolation is
auto-escaped, and the design treats escaping as context-aware (text, attribute,
URL, script/style). This compile-time safety is gsx's core differentiator.

## Relationship to templ

gsx shares no code with templ, but it is built to interoperate. `gsx.Node` — the
universal renderable — has the **identical method set to `templ.Component`**
(`Render(ctx, w) error`). Because the method sets match, a `gsx.Node` is accepted
anywhere a `templ.Component` is expected (structpages and other templ-ecosystem
tools) **without importing templ**. You can adopt gsx incrementally inside an
existing templ project.

## What gsx is not

gsx is templating only — no router, no HTTP handlers, no framework. It is a way to
write HTML as a first-class, composable Go value. Everything non-template is
ordinary Go.

---

> **Status — alpha.** Language design is stable; parser, runtime, and codegen
> phase 1 are done. The CLI is a work in progress, so gsx is **not yet runnable
> end-to-end**. See the [roadmap](https://github.com/gsxhq/gsx/blob/main/docs/ROADMAP.md).
```

- [ ] **Step 2: Verify claims against the spec**

Run: `grep -n "5,000-line\|symbolresolver\|identical method set\|Babel" docs/superpowers/specs/2026-06-18-gsx-templating-design.md`
Expected: matches confirming the symbol-resolver size, the templ method-set claim, and the Babel rule are faithful to the spec.

- [ ] **Step 3: Commit**

```bash
git add docs/guide/vision.md
git commit -m "docs: add 'Why gsx' vision guide"
```

---

## Task 3: Repo A — Principles

**Files:**
- Create: `docs/guide/principles.md`

**Interfaces:**
- Source material: `docs/superpowers/specs/2026-06-18-gsx-templating-design.md` ("Guiding Principles") and `docs/superpowers/specs/2026-06-19-gsx-runtime-design.md` (stdlib-only, escaping). Read before writing.

- [ ] **Step 1: Write `docs/guide/principles.md`**

```markdown
# Principles

gsx's design follows a few firm commitments. They explain most of the syntax
decisions you'll meet in the [syntax guide](./syntax).

## Stay close to HTML and to Go

Markup looks like HTML/JSX; helpers, variant functions, and everything that isn't a
template are ordinary Go. There is no third language between them — the seam is the
`component` keyword and `{ }` interpolation.

## Syntax tidiness is the top priority

Parser complexity bends to serve clean syntax, never the reverse. Where it buys
tidier markup, gsx does targeted Go-expression boundary parsing rather than forcing
an awkward syntax to keep the parser simple.

## Lean on the Go compiler

Generated code is plain Go. Prop names, types, and call sites are checked by the Go
compiler, not by gsx at runtime. A wrong template is a compile error with a real
source location (gsx emits `//line` directives back to the `.gsx`), not a runtime
surprise.

## Secure by construction

Interpolation is auto-escaped by default, and the design treats escaping as
**context-aware**: text, attribute, URL, and script/style contexts each get the
right treatment. Unescaped output is explicit and visible (`gsx.Raw`). Type-driven
attributes mean a `bool` renders as a bare or omitted attribute rather than the
string `"true"`.

## Standard-library-only runtime

The `gsx` runtime package imports nothing outside the Go standard library. The
generator/CLI may use `golang.org/x/tools`, but what ships in your binary stays
stdlib-only.
```

- [ ] **Step 2: Verify the stdlib-only claim**

Run: `cat go.mod`
Expected: confirms `golang.org/x/tools` is a dependency (generator side); the principles page must not imply the runtime imports it.

- [ ] **Step 3: Commit**

```bash
git add docs/guide/principles.md
git commit -m "docs: add Principles guide"
```

---

## Task 4: Repo A — Syntax (light)

**Files:**
- Create: `docs/guide/syntax.md`

**Interfaces:**
- Source material: `examples/README.md` (the quick-reference and files tables) and the `examples/NN_*.gsx` files. The page is intentionally light — a tour plus the reference table, pointing at examples as canonical.

- [ ] **Step 1: Write `docs/guide/syntax.md`**

```markdown
# Syntax

> **Syntax is roughly fixed, not frozen.** This page is a quick tour. The
> [`examples/`](https://github.com/gsxhq/gsx/tree/main/examples) corpus is the
> canonical, always-current reference — every accepted form is demonstrated there.

A `.gsx` file is ordinary Go (package, imports, types, funcs) plus `component`
declarations. A component has a templ-style header and a JSX-style body — the
markup *is* the result, so there is no return type and no `return`:

```gsx
component Card(title string, featured bool) {
	<section class={ "card", "card-featured": featured }>
		<h2>{title}</h2>
		{ if featured { <span class="badge">Featured</span> } }
		<div class="body">{children}</div>
	</section>
}
```

## Elements vs components

Capitalization decides what a tag means:

- lowercase / hyphenated → HTML element: `<div>`, `<el-dialog>`
- Capitalized / dotted → component: `<Card>`, `<ui.Button>`, `<p.Content>`

Inline component params become a generated props struct, so gsx owns the field
names: `<Card title="Hi" featured/>` → `Card(CardProps{Title: "Hi", Featured: true})`.

## Quick reference

| Form | Meaning |
|------|---------|
| `component X(params) { … }` | component declaration (emission body — no return) |
| `component (p T) Name(params) { … }` | method component (receiver) |
| `<div>`, `<el-dialog>` | HTML element (lowercase / hyphenated) |
| `<Card>`, `<ui.Button>` | component (Capitalized / dotted) |
| `{ expr }` | interpolation in body (auto HTML-escaped) |
| `{ expr? }` | try-marker: unwrap `(T, error)`, propagate the error |
| `name="lit"` | static string attribute |
| `name={ expr }` | dynamic attribute (Go expression) |
| `name` (bare) | boolean attribute = `true` |
| `disabled={ cond }` | type-driven boolean attr (bool → bare/omitted) |
| `{...expr}` | spread attributes (element) / props (component) |
| `{ if … }` / `{ for … }` inside a tag | conditional attributes |
| `{ if/for/switch … { <markup> } }` | control flow contributing children |
| `{{ stmt }}` | Go statement escape hatch (no output) |
| `<>…</>` | fragment |
| `class={ a, "cls": cond }` | composable `class`/`style` (comma list; conditional sugar) |
| `{children}` | explicit children placement |
| `gsx.Raw(s)` | unescaped HTML |

## Markup vs Go (the one subtlety)

Inside `{ }`, gsx decides markup-vs-Go positionally — the Babel rule: `{ <div/> }`
is markup, `{ a < b }` is a Go expression. When in doubt, see
[`examples/06_corner_cases.gsx`](https://github.com/gsxhq/gsx/tree/main/examples/06_corner_cases.gsx).

## Learn by example

| Topic | Example |
|-------|---------|
| Elements, attrs, void, DOCTYPE, SVG, web components | `01_elements.gsx` |
| Interpolation, raw HTML, escaping contexts | `02_text_escaping.gsx` |
| if / for / switch, fragments, `{{ }}` | `03_control_flow.gsx` |
| `component` decls, props, `{children}`, slots | `04_components.gsx` |
| The full attribute system | `05_attributes.gsx` |
| Markup-vs-Go corner cases | `06_corner_cases.gsx` |
| Method components, page composition | `11_struct_methods.gsx` |
| Children & attribute fallthrough | `12_children_attrs.gsx` |

> **Status — alpha.** `.gsx` files are illustrative; the CLI that generates `.x.go`
> is a work in progress. Follow the
> [roadmap](https://github.com/gsxhq/gsx/blob/main/docs/ROADMAP.md).
```

- [ ] **Step 2: Verify every referenced example exists**

Run: `ls examples/01_elements.gsx examples/02_text_escaping.gsx examples/03_control_flow.gsx examples/04_components.gsx examples/05_attributes.gsx examples/06_corner_cases.gsx examples/11_struct_methods.gsx examples/12_children_attrs.gsx`
Expected: all eight files exist (no broken example references).

- [ ] **Step 3: Commit**

```bash
git add docs/guide/syntax.md
git commit -m "docs: add light syntax guide deferring to examples"
```

---

## Task 5: Repo A — README

**Files:**
- Create: `README.md`

- [ ] **Step 1: Write `README.md`**

```markdown
# gsx

A templating language for Go: **templ-style `component` declarations** with a
**JSX-style markup body**, compiled to plain Go.

> **Status — alpha.** Language design is stable; the parser, runtime, and codegen
> phase 1 are done. The CLI is a work in progress, so gsx is **not yet runnable
> end-to-end**. See the [roadmap](docs/ROADMAP.md).

## What is gsx

`.gsx` files hold ordinary Go (imports, types, funcs) plus `component`
declarations. A generator lowers each component to plain Go in a `.x.go` file the
Go compiler type-checks and builds:

```
.gsx → parser → AST → codegen → .x.go → go build → HTML
```

- **Type-safe by construction** — components become plain Go; props are generated
  structs, so gsx owns the field names (no symbol-resolver guesswork).
- **Close to HTML and to Go** — JSX-style markup for templates; ordinary Go for
  everything else. Capitalization decides component-vs-element (`<div>` vs `<Card>`).
- **templ-compatible** — `gsx.Node` has the identical method set to
  `templ.Component`, so gsx output drops into the templ ecosystem without importing
  templ. The runtime is **standard-library only**.

## A taste

```gsx
component Card(title string, featured bool) {
	<section class={ "card", "card-featured": featured }>
		<h2>{title}</h2>
		{ if featured { <span class="badge">Featured</span> } }
		<div class="body">{children}</div>
	</section>
}
```

*(Illustrative — `.gsx` files are not yet buildable; the CLI is WIP.)*

## Learn more

- **Docs** — [Why gsx](docs/guide/vision.md) ·
  [Principles](docs/guide/principles.md) · [Syntax](docs/guide/syntax.md)
- **Examples** — the [`examples/`](examples/) corpus is the canonical syntax
  reference.
- **Roadmap & status** — [docs/ROADMAP.md](docs/ROADMAP.md).
- **Design docs** — [docs/superpowers/specs/](docs/superpowers/specs/).

## Documentation site

The public docs site is built with VitePress in the separate
[`gsxhq/website`](https://github.com/gsxhq/website) repo, which renders the
Markdown in [`docs/guide/`](docs/guide/).

## Contributing

Issues and discussion welcome. Runtime code must stay standard-library only; the
generator/CLI may use `golang.org/x/tools`.

## License

Not yet chosen — license **TBD**. Until a `LICENSE` file is added, all rights are
reserved by the author.
```

- [ ] **Step 2: Verify internal links resolve**

Run: `ls docs/ROADMAP.md docs/guide/vision.md docs/guide/principles.md docs/guide/syntax.md examples docs/superpowers/specs`
Expected: all exist (guide pages created in Tasks 2–4).

- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "docs: add repo README"
```

---

## Task 6: Repo A — authoring skill

**Files:**
- Create: `skills/gsx/SKILL.md`

**Interfaces:**
- Mirrors the structure of an existing skill's `SKILL.md` (frontmatter + body). Content draws from `docs/guide/syntax.md` and `examples/`; do not invent syntax.

- [ ] **Step 1: Write `skills/gsx/SKILL.md`**

```markdown
---
name: gsx
description: Use when writing or editing .gsx files — gsx templating components, JSX-style markup in Go, or when asked about gsx syntax (component declarations, interpolation, attributes, control flow, class/style, children/fallthrough).
---

# Authoring gsx templates

gsx is a Go templating language: templ-style `component` declarations with a
JSX-style markup body, compiled to plain Go (`.gsx` → `.x.go`).

> **Status:** language design is stable; the CLI/codegen is a work in progress, so
> `.gsx` files are not yet buildable end-to-end. Treat `examples/*.gsx` as the
> canonical, current reference and prefer copying patterns from there.

## Core rules

- A `.gsx` file is ordinary Go (package, imports, types, funcs) **plus** `component`
  declarations. Put non-template Go (helpers, types) in the same file as normal Go.
- A component body is **emission** — the markup *is* the result. No return type, no
  `return` keyword. Bare Go statements are not allowed in the body; wrap them in
  `{{ … }}`.
- **Capitalization decides the tag's meaning:** lowercase/hyphenated → HTML element
  (`<div>`, `<el-dialog>`); Capitalized/dotted → component (`<Card>`, `<ui.Button>`).
- Inline component params become a generated `XProps` struct — gsx owns the field
  names: `<Card title="Hi" featured/>` → `Card(CardProps{Title: "Hi", Featured: true})`.

## Forms

- Interpolation: `{ expr }` (auto HTML-escaped). Unescaped: `gsx.Raw(s)`.
- Try-marker: `{ expr? }` unwraps `(T, error)` and propagates the error.
- Attributes: static `name="lit"`, dynamic `name={ expr }`, boolean bare `name`,
  type-driven `disabled={ cond }` (bool → bare/omitted), spread `{...expr}`,
  conditional `{ if … }` / `{ for … }` inside the tag.
- Composable `class`/`style`: `class={ a, "cls": cond, x }` (comma list;
  `"cls": cond` is conditional sugar).
- Control flow contributing children: `{ if/for/switch … { <markup> } }`.
- Go escape hatch (no output): `{{ stmt }}`. Fragments: `<>…</>`.
- Children: `{children}` places passed children. Passing children to a component
  that never places them is a compile error.
- Attribute fallthrough: undeclared call-site attrs auto-apply to a single root
  element (`class`/`style` merge); an ambiguous root is a compile error.

## The one subtlety: markup vs Go

Inside `{ }`, gsx decides markup-vs-Go **positionally** (the Babel rule):
`{ <div/> }` is markup, `{ a < b }` is a Go expression. If a comparison or generic
looks like a tag, reach for parentheses or a `{{ }}` block. See
`examples/06_corner_cases.gsx`.

## When in doubt

Read the matching example: elements `01`, escaping `02`, control flow `03`,
components/children `04`, attributes `05`, corner cases `06`, method components
`11`, fallthrough `12`. Full guide: `docs/guide/syntax.md`.
```

- [ ] **Step 2: Verify frontmatter and example references**

Run: `head -4 skills/gsx/SKILL.md && ls examples/06_corner_cases.gsx examples/04_components.gsx docs/guide/syntax.md`
Expected: frontmatter shows valid `name:` and `description:` keys; referenced files exist.

- [ ] **Step 3: Commit**

```bash
git add skills/gsx/SKILL.md
git commit -m "docs: add portable gsx authoring skill"
```

---

## Task 7: Repo B — scaffold the website repo

**Files (all in `~/personal/gsxhq/website`):**
- Create: `package.json`
- Create: `.gitignore`

- [ ] **Step 1: Create and initialize the repo**

```bash
mkdir -p ~/personal/gsxhq/website
git -C ~/personal/gsxhq/website init -b main
```

- [ ] **Step 2: Write `~/personal/gsxhq/website/package.json`**

```json
{
  "name": "@gsxhq/website",
  "version": "0.0.0",
  "private": true,
  "type": "module",
  "scripts": {
    "sync": "node scripts/sync-docs.mjs",
    "predev": "node scripts/sync-docs.mjs --link",
    "dev": "vitepress dev",
    "prebuild": "node scripts/sync-docs.mjs",
    "build": "vitepress build",
    "preview": "vitepress preview"
  },
  "devDependencies": {
    "vitepress": "^1.6.3"
  }
}
```

- [ ] **Step 3: Write `~/personal/gsxhq/website/.gitignore`**

```gitignore
node_modules/
.vitepress/cache/
.vitepress/dist/
# synced from gsxhq/gsx at build time
/guide/
```

- [ ] **Step 4: Install dependencies (generates the lockfile)**

```bash
cd ~/personal/gsxhq/website && npm install
```
Expected: `vitepress` installs; `package-lock.json` and `node_modules/` are created. `node_modules/` is gitignored.

- [ ] **Step 5: Commit**

```bash
git -C ~/personal/gsxhq/website add package.json package-lock.json .gitignore
git -C ~/personal/gsxhq/website commit -m "chore: scaffold VitePress website repo"
```

---

## Task 8: Repo B — build-time docs sync script

**Files:**
- Create: `~/personal/gsxhq/website/scripts/sync-docs.mjs`

**Interfaces:**
- Produces `./guide/` (the synced VitePress content dir consumed by config in Task 9).
- Source resolution order: `GSX_DOCS_SRC` env (path to a gsx checkout root) → local sibling `../gsx` → shallow `git clone` of `gsxhq/gsx`.
- `--link` flag (used by `predev`): for a *local* source, symlink `./guide` → `<src>/docs/guide` so VitePress live-reloads on source edits. Without `--link` (used by `prebuild`/CI), always copy real files.

- [ ] **Step 1: Write `scripts/sync-docs.mjs`**

```javascript
// Build-time fetch: bring gsx's docs/guide/** into ./guide so VitePress can render
// it. Source order: GSX_DOCS_SRC env > local sibling ../gsx > shallow git clone.
import { existsSync, rmSync, mkdirSync, cpSync, symlinkSync, mkdtempSync } from 'node:fs'
import { join, resolve, dirname } from 'node:path'
import { execFileSync } from 'node:child_process'
import { tmpdir } from 'node:os'

const GSX_REPO = 'https://github.com/gsxhq/gsx.git'
const DEST = resolve('guide')
const link = process.argv.includes('--link')

// Returns { dir, local }: the docs/guide source dir, and whether it is a stable
// local path safe to symlink (vs a throwaway clone that must be copied).
function resolveSource() {
  if (process.env.GSX_DOCS_SRC) {
    const dir = resolve(process.env.GSX_DOCS_SRC, 'docs', 'guide')
    if (!existsSync(dir)) {
      throw new Error(`GSX_DOCS_SRC is set but ${dir} does not exist`)
    }
    return { dir, local: true }
  }
  const sibling = resolve('..', 'gsx', 'docs', 'guide')
  if (existsSync(sibling)) return { dir: sibling, local: true }

  const tmp = mkdtempSync(join(tmpdir(), 'gsx-docs-'))
  execFileSync('git', ['clone', '--depth', '1', GSX_REPO, tmp], { stdio: 'inherit' })
  return { dir: resolve(tmp, 'docs', 'guide'), local: false }
}

const { dir: src, local } = resolveSource()
rmSync(DEST, { recursive: true, force: true })

if (link && local) {
  mkdirSync(dirname(DEST), { recursive: true })
  symlinkSync(src, DEST, 'dir')
  console.log(`linked guide: ${DEST} -> ${src}`)
} else {
  mkdirSync(DEST, { recursive: true })
  cpSync(src, DEST, { recursive: true })
  console.log(`copied guide: ${src} -> ${DEST}`)
}
```

- [ ] **Step 2: Run the sync against the local sibling and verify output**

```bash
cd ~/personal/gsxhq/website && npm run sync
ls guide/
```
Expected: copies from `../gsx/docs/guide`; `guide/` contains `vision.md`, `principles.md`, `syntax.md`. (Requires Tasks 2–4 committed in `gsxhq/gsx`.)

- [ ] **Step 3: Verify the `--link` mode produces a symlink**

```bash
cd ~/personal/gsxhq/website && rm -rf guide && npm run sync -- --link && ls -l guide
```
Expected: `guide` is a symlink pointing at `../gsx/docs/guide`.

- [ ] **Step 4: Commit**

```bash
git -C ~/personal/gsxhq/website add scripts/sync-docs.mjs
git -C ~/personal/gsxhq/website commit -m "feat: build-time docs sync from gsxhq/gsx"
```

---

## Task 9: Repo B — VitePress config + home page, verify build

**Files:**
- Create: `~/personal/gsxhq/website/.vitepress/config.mts`
- Create: `~/personal/gsxhq/website/index.md`
- Create: `~/personal/gsxhq/website/README.md`

**Interfaces:**
- Consumes `./guide/{vision,principles,syntax}.md` produced by Task 8.
- `base: '/website/'` targets project Pages at `gsxhq.github.io/website/`.

- [ ] **Step 1: Write `.vitepress/config.mts`**

```typescript
import { defineConfig } from 'vitepress'

// Project Pages live at https://gsxhq.github.io/website/ — switch base to '/' if a
// custom domain is added later.
export default defineConfig({
  title: 'gsx',
  description:
    'A templating language for Go — templ-style components, JSX-style markup, compiled to plain Go.',
  base: '/website/',
  themeConfig: {
    nav: [
      { text: 'Guide', link: '/guide/vision' },
      { text: 'Examples', link: 'https://github.com/gsxhq/gsx/tree/main/examples' },
      { text: 'Roadmap', link: 'https://github.com/gsxhq/gsx/blob/main/docs/ROADMAP.md' },
    ],
    sidebar: {
      '/guide/': [
        {
          text: 'Guide',
          items: [
            { text: 'Why gsx', link: '/guide/vision' },
            { text: 'Principles', link: '/guide/principles' },
            { text: 'Syntax', link: '/guide/syntax' },
          ],
        },
      ],
    },
    search: { provider: 'local' },
    socialLinks: [{ icon: 'github', link: 'https://github.com/gsxhq/gsx' }],
  },
})
```

- [ ] **Step 2: Write `index.md` (home/hero)**

```markdown
---
layout: home
hero:
  name: gsx
  text: HTML as a first-class Go value
  tagline: templ-style components, JSX-style markup, compiled to plain Go. A standard-library-only runtime.
  actions:
    - theme: brand
      text: Why gsx
      link: /guide/vision
    - theme: alt
      text: Syntax
      link: /guide/syntax
    - theme: alt
      text: GitHub
      link: https://github.com/gsxhq/gsx
features:
  - title: Type-safe by construction
    details: Components lower to plain Go the compiler checks. Props are generated structs — gsx owns the field names, so there is no symbol-resolver guesswork.
  - title: Close to HTML, close to Go
    details: JSX-style markup for templates; ordinary Go for everything else. Capitalization decides component-vs-element.
  - title: templ-compatible
    details: gsx.Node has the identical method set to templ.Component, so gsx output drops into the templ ecosystem without importing templ.
---

> **Status — alpha.** Language design is stable; parser, runtime, and codegen
> phase&nbsp;1 are done. The CLI is a work in progress, so gsx is **not yet
> runnable end-to-end**. Follow the
> [roadmap](https://github.com/gsxhq/gsx/blob/main/docs/ROADMAP.md).
```

- [ ] **Step 3: Write `README.md` for the website repo**

```markdown
# gsxhq/website

The documentation site for [gsx](https://github.com/gsxhq/gsx), built with
[VitePress](https://vitepress.dev) and deployed to GitHub Pages.

## How content works

The guide pages are **authored in the gsx repo** under `docs/guide/` and pulled in
at build time by `scripts/sync-docs.mjs`. Do not edit `guide/` here — it is
generated and gitignored. Edit the Markdown in `gsxhq/gsx` instead.

Source resolution order for the sync: `GSX_DOCS_SRC` env → local sibling `../gsx` →
shallow `git clone`.

## Develop

```bash
npm install
npm run dev      # symlinks ../gsx/docs/guide for live reload, then serves
```

Edits to `../gsx/docs/guide/*.md` hot-reload in the dev server.

## Build

```bash
npm run build    # copies guide content, then builds to .vitepress/dist
npm run preview
```
```

- [ ] **Step 4: Build the site and verify it succeeds**

```bash
cd ~/personal/gsxhq/website && rm -rf guide && npm run build
```
Expected: `prebuild` syncs `guide/` (copy), then VitePress builds to `.vitepress/dist` with **no dead-link errors** and the pages `/`, `/guide/vision`, `/guide/principles`, `/guide/syntax` present.

- [ ] **Step 5: Serve and confirm pages render (visual check)**

```bash
cd ~/personal/gsxhq/website && npm run dev
```
Expected: home hero renders; sidebar shows Why gsx / Principles / Syntax; local search box present. Stop the dev server after confirming.

- [ ] **Step 6: Commit**

```bash
git -C ~/personal/gsxhq/website add .vitepress/config.mts index.md README.md
git -C ~/personal/gsxhq/website commit -m "feat: VitePress config, home page, and dev/build docs"
```

---

## Task 10: Repo B — GitHub Pages deploy workflow

**Files:**
- Create: `~/personal/gsxhq/website/.github/workflows/deploy.yml`

**Interfaces:**
- Checks out `gsxhq/gsx` into `_gsx` and sets `GSX_DOCS_SRC` so the build-time sync copies from it (works for a private gsx repo too, via the default workflow token).
- Uploads `.vitepress/dist` (VitePress project root is the repo root here, not `docs/`).

- [ ] **Step 1: Write `.github/workflows/deploy.yml`**

```yaml
name: Deploy VitePress site to Pages

on:
  push:
    branches: [main]
  workflow_dispatch:

permissions:
  contents: read
  pages: write
  id-token: write

concurrency:
  group: pages
  cancel-in-progress: false

jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - name: Checkout website
        uses: actions/checkout@v5
      - name: Checkout gsx (docs source)
        uses: actions/checkout@v5
        with:
          repository: gsxhq/gsx
          path: _gsx
      - name: Setup Node
        uses: actions/setup-node@v6
        with:
          node-version: 24
          cache: npm
      - name: Setup Pages
        uses: actions/configure-pages@v4
      - name: Install dependencies
        run: npm ci
      - name: Build with VitePress
        run: npm run build
        env:
          GSX_DOCS_SRC: ${{ github.workspace }}/_gsx
      - name: Upload artifact
        uses: actions/upload-pages-artifact@v3
        with:
          path: .vitepress/dist

  deploy:
    environment:
      name: github-pages
      url: ${{ steps.deployment.outputs.page_url }}
    needs: build
    runs-on: ubuntu-latest
    steps:
      - name: Deploy to GitHub Pages
        id: deployment
        uses: actions/deploy-pages@v4
```

- [ ] **Step 2: Validate the workflow build path locally**

```bash
cd ~/personal/gsxhq/website && rm -rf guide && GSX_DOCS_SRC="$HOME/personal/gsxhq/gsx" npm run build && ls .vitepress/dist/index.html
```
Expected: build succeeds using `GSX_DOCS_SRC` (the same mechanism the workflow uses) and `.vitepress/dist/index.html` exists.

- [ ] **Step 3: Commit**

```bash
git -C ~/personal/gsxhq/website add .github/workflows/deploy.yml
git -C ~/personal/gsxhq/website commit -m "ci: deploy VitePress site to GitHub Pages"
```

- [ ] **Step 4: Post-merge manual step (note for the maintainer)**

After pushing `gsxhq/website` to GitHub: in the repo's **Settings → Pages**, set
**Source = GitHub Actions**. The first push to `main` then publishes to
`https://gsxhq.github.io/website/`. (This cannot be done from the CLI without
admin API calls; left as a maintainer action.)

---

## Self-Review

**Spec coverage:**
- README → Task 5. ✅
- `docs/` content (index + vision + principles + syntax) → Tasks 1–4. ✅
- Skill (`skills/gsx/`) → Task 6. ✅
- `gsxhq/website` VitePress site (scaffold, sync, config/home, deploy) → Tasks 7–10. ✅
- Build-time fetch with sibling/clone/env resolution → Task 8. ✅
- Pages deploy + `base` + Source=Actions note → Task 10. ✅
- "Philosophy well-covered, syntax light" asymmetry → Tasks 2–3 substantial, Task 4 lean. ✅
- LICENSE left out (TBD note) → Task 5 Step 1. ✅

**Placeholder scan:** No TBD/TODO in steps; all file contents are written in full; example references checked in verification steps.

**Type/name consistency:** sync script writes `./guide` (Task 8) ↔ config sidebar/nav use `/guide/*` and home links `/guide/vision`,`/guide/syntax` (Task 9) ↔ `.gitignore` ignores `/guide/` (Task 7). `GSX_DOCS_SRC` used identically in Task 8 (resolution), Task 10 Step 1 (workflow env), Task 10 Step 2 (local validation). Scripts `predev`/`prebuild` (Task 7) match the `sync-docs.mjs --link`/copy behavior (Task 8). `.vitepress/dist` upload path (Task 10) matches VitePress project-root build output (Task 9).
```
