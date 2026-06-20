# gsx Documentation Groundwork — Design

**Date:** 2026-06-20
**Status:** Approved (design)
**Module:** `github.com/gsxhq/gsx`
**Org layout:** `~/personal/gsxhq/` holds the org's repos (`gsx`, plus the new
`website` repo this iteration creates at `~/personal/gsxhq/website`).

## Summary

Lay the documentation groundwork for gsx in **this iteration**, scoped honestly to
the project's real state: parser + runtime + codegen phase 1 are done, but there is
**no CLI yet — gsx is not runnable end-to-end**. We therefore ship the *foundation*,
not a full user manual: a README, public Markdown docs covering **vision →
principles → syntax**, a portable authoring **skill**, and a bootstrapped
**VitePress site** so the docs and the site develop together. Reference/how-to docs
(CLI, filters, API) and "getting started / install" remain deferred until gsx is
runnable.

All four originally-requested deliverables are addressed — **all built this
iteration**, across two repos:

1. **README** — repo root of `gsxhq/gsx`.
2. **docs/ folder** — public Markdown content (vision/principles/syntax) in
   `gsxhq/gsx`.
3. **GitHub Pages site** — a new sibling repo `gsxhq/website` (VitePress) that
   consumes this repo's Markdown via a build-time fetch and deploys to Pages.
4. **Skill** — top-level `skills/gsx/` in `gsxhq/gsx`, portable.

## Decisions (from brainstorming)

- **Purpose / timing:** groundwork iteration. Start with vision/principles, then
  syntax; the rest (how-to, CLI, API reference) comes later. Everything is honest
  about WIP status.
- **Site tooling:** VitePress, bootstrapped this iteration in a separate repo
  **`gsxhq/website`** (sibling at `~/personal/gsxhq/website`).
- **Content home:** authored Markdown lives in **this repo's `docs/`** (reviewed
  alongside code = single source of truth). The `gsxhq/website` repo consumes it via
  a **build-time fetch**: a sync script copies `gsx/docs/guide/**` into the site's
  content dir before `vitepress dev`/`build`. The script prefers the local sibling
  `../gsx` when present (fast local dev); otherwise it `git clone`s the repo (CI).
- **Skill scope:** teach an agent to **author `.gsx` correctly** (language only),
  mirroring the existing `structpages` skill. WIP-honest.
- **Skill home:** top-level `skills/gsx/SKILL.md` — tool-agnostic so it copies into
  `.claude/skills`, Codex, Gemini, etc.
- **Internal docs:** `docs/superpowers/` specs and `docs/ROADMAP.md` stay in place
  and are never synced to the site (the sync copies only `docs/guide/**`).

## Components

### 1. `README.md` (repo root) — the front door

Concise and status-honest. Sections:

- **Title + one-liner + status line.** e.g. "alpha — language design stable;
  parser/runtime/codegen-phase-1 done; CLI WIP, **not yet runnable end-to-end**."
- **What is gsx.** templ-style `component` declarations + JSX-style markup body →
  codegen to `.x.go`; `gsx.Node` has the identical method set to `templ.Component`
  (templ-ecosystem compatible without importing templ); **stdlib-only runtime**.
- **A taste.** One small `.gsx` snippet (drawn from `examples/`), framed as
  illustrative (not yet buildable).
- **Status & roadmap.** Link to `docs/ROADMAP.md`.
- **Learn more.** Links to `docs/` guide (vision/principles/syntax) and `examples/`.
- **Design docs.** Pointer to `docs/superpowers/specs/`.
- **Contributing / license.** **Open item: no `LICENSE` file exists yet** — the
  README will note license as TBD until the maintainer chooses one. Adding the
  license is out of scope for this docs iteration (maintainer decision).

### 2. `docs/` public content (plain Markdown, VitePress-ready)

Authored for an *external reader* — derived from the internal specs and examples
but rewritten without internal history/baggage. Layout:

```
docs/
  index.md            # overview + status + the pipeline ("how it fits together")
  guide/
    vision.md         # why gsx exists; lessons borrowed from templ; relationship to templ
    principles.md     # guiding principles / design philosophy
    syntax.md         # the language reference for humans (see below)
  ROADMAP.md          # existing — stays
  superpowers/        # existing internal specs — stays (future site excludes)
```

- **`index.md`** — what gsx is, current status, and the
  `.gsx → parser → AST → codegen → .x.go → go build → HTML` pipeline.
- **`guide/vision.md`** — the problem gsx solves, the three templ lessons
  (symbol-resolution tar pit; find Go boundaries don't re-parse; markup-vs-Go
  detection), and how gsx relates to / interoperates with templ.
- **`guide/principles.md`** — stay close to HTML and Go; syntax tidiness as top
  priority; lean on the Go compiler; stdlib-only runtime.
- **`guide/syntax.md`** — the human-facing language guide: element vs component
  casing rules; `{ expr }` interpolation and `{ expr? }` try-marker; attributes
  (static / `={ expr }` / bare bool / type-driven bool / spread / conditional);
  control flow (`{ if/for/switch }`, `{{ }}` Go blocks); composable `class`/`style`;
  attribute fallthrough; the **markup-vs-Go ambiguity** (the Babel positional rule);
  fragments, raw text, `gsx.Raw`. Cross-links each section to the canonical
  `examples/NN_*.gsx` file.

`examples/*.gsx` remains the canonical syntax corpus; `syntax.md` references it
rather than duplicating it.

*Sync-boundary note:* public guide pages sit under `docs/guide/`. The website's
sync script copies **only `docs/guide/**`** into the site — the internal
`superpowers/` specs, `ROADMAP.md`, and `docs/index.md` are never pulled. The site
provides its own home/hero page (see §4); `docs/index.md` stays a lightweight
overview for people browsing the repo on GitHub.

### 3. `skills/gsx/` — portable authoring skill

`skills/gsx/SKILL.md`:

- **Frontmatter:** `name: gsx` (authoring), `description:` triggers on "writing
  `.gsx` templating files / gsx components / gsx markup".
- **Body:** a condensed authoring guide — component vs element casing; `component`
  and method-component declarations; `{ expr }` / `{ expr? }`; attributes
  (static/expr/bool/spread/conditional); control flow and `{{ }}`; composable
  `class`/`style`; attribute fallthrough; and the **markup-vs-Go gotchas** an agent
  most often trips on. Ends with pointers to `docs/guide/syntax.md` and
  `examples/`, and a WIP note (codegen/CLI in progress; `.gsx` is illustrative,
  not yet buildable).
- **Portability:** plain Markdown + frontmatter, no Claude-only paths in the body,
  so it copies/symlinks into any agent's skill location.

### 4. `gsxhq/website` — VitePress site (new sibling repo)

A new repo at `~/personal/gsxhq/website`, bootstrapped by hand (no interactive
`npm create`) so it's reproducible:

```
website/
  package.json              # vitepress devDep; scripts: predev/sync, dev, prebuild, build, preview
  .gitignore                # node_modules/, .vitepress/cache/, .vitepress/dist/, the synced content dir
  scripts/sync-docs.mjs     # build-time fetch: copy gsx/docs/guide/** → content/guide/**
  README.md                 # how to dev/build the site; notes content is synced from gsxhq/gsx
  .vitepress/
    config.{js,ts}          # title, description, base, nav, sidebar, theme, social/repo links
  index.md                  # site home/hero (lives in website repo — presentation, not content)
  content/                  # synced (gitignored): guide/vision.md, guide/principles.md, guide/syntax.md
  .github/workflows/deploy.yml  # build on push to main → deploy to GitHub Pages
```

Key design points:

- **Build-time fetch (`scripts/sync-docs.mjs`).** Resolves the source: if `../gsx`
  exists (local sibling), copy its `docs/guide/**`; else `git clone --depth 1`
  `https://github.com/gsxhq/gsx` into a temp dir and copy from there. Copies into
  `content/guide/` (gitignored). Idempotent; run by `predev`/`prebuild`. A
  `dev:watch` variant re-syncs on changes to the local sibling so docs + site can
  be edited together.
- **VitePress layout.** `srcDir` is the repo root; the home page (`index.md`) and
  nav/sidebar config live in the website repo; content pages come from `content/`.
  Sidebar references `content/guide/{vision,principles,syntax}.md`.
- **Pages deploy.** `.github/workflows/deploy.yml`: checkout website → Node setup →
  run sync (clones gsx) → `npm ci` → `npm run build` → upload `.vitepress/dist` →
  `actions/deploy-pages`. `base` defaults to `/website/` (project Pages at
  `gsxhq.github.io/website/`); documented as switch-to-`/` when a custom domain is
  added.
- **Status-honest.** Home page and nav carry the same WIP framing as the README
  (language design stable; not yet runnable end-to-end).

The two repos develop together: edit `gsx/docs/guide/*`, the site picks it up on
the next sync (or live via `dev:watch`).

## Out of scope (deferred, tracked)

- **Reference docs** — CLI commands, `|>` filters / `std` package, runtime API.
- **Getting started / install** — requires the CLI to exist.
- **Skill expansion** — integration patterns (structpages interop, where `.x.go`
  goes) once the CLI stabilizes.
- **LICENSE file** — maintainer decision, not a docs task.

## Testing / verification

Docs are prose, so verification is lightweight:

- Markdown links resolve (relative links to `examples/`, `docs/ROADMAP.md`, specs).
- Every syntax form in `syntax.md` and the skill maps to an existing
  `examples/NN_*.gsx` file (no invented syntax).
- README status claims match `docs/ROADMAP.md` (no overclaiming of what works).
- Skill frontmatter is valid; body contains no Claude-only tool/path assumptions.
- **Site builds.** In `gsxhq/website`: `npm run build` succeeds end-to-end —
  the sync script pulls `gsx/docs/guide/**` and VitePress builds with no dead links
  or missing pages. `npm run dev` serves locally for visual review of the rendered
  vision/principles/syntax pages.
```
