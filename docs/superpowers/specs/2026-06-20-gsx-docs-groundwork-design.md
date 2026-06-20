# gsx Documentation Groundwork — Design

**Date:** 2026-06-20
**Status:** Approved (design)
**Module:** `github.com/gsxhq/gsx`
**Org layout:** `~/personal/gsxhq/` holds the org's repos (currently just `gsx`);
a future docs-site repo lands at `~/personal/gsxhq/<site>`.

## Summary

Lay the documentation groundwork for gsx in **this iteration**, scoped honestly to
the project's real state: parser + runtime + codegen phase 1 are done, but there is
**no CLI yet — gsx is not runnable end-to-end**. We therefore ship the *foundation*,
not a full user manual: a README, public Markdown docs covering **vision →
principles → syntax**, and a portable authoring **skill**. The public site
(VitePress) and reference/how-to docs are deferred.

All four originally-requested deliverables are addressed:

1. **README** — built now (repo root).
2. **docs/ folder** — public Markdown content built now (vision/principles/syntax).
3. **GitHub Pages site** — *deferred* to a separate `gsxhq/<site>` VitePress repo
   that consumes this repo's Markdown. Not built this iteration.
4. **Skill** — built now, portable, at top-level `skills/gsx/`.

## Decisions (from brainstorming)

- **Purpose / timing:** groundwork iteration. Start with vision/principles, then
  syntax; the rest (how-to, CLI, API reference) comes later. Everything is honest
  about WIP status.
- **Site tooling:** VitePress — but deferred to a separate repo, not built now.
- **Content home:** authored Markdown lives in **this repo's `docs/`** (reviewed
  alongside code = single source of truth). The future `gsxhq/<site>` VitePress repo
  consumes it (git submodule or build-time fetch).
- **Skill scope:** teach an agent to **author `.gsx` correctly** (language only),
  mirroring the existing `structpages` skill. WIP-honest.
- **Skill home:** top-level `skills/gsx/SKILL.md` — tool-agnostic so it copies into
  `.claude/skills`, Codex, Gemini, etc.
- **Internal docs:** `docs/superpowers/` specs and `docs/ROADMAP.md` stay in place;
  the future site will `srcExclude` them.

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

*Submodule-boundary note:* public pages sit directly under `docs/` (siblings of the
internal `superpowers/`). The future VitePress repo pulls `docs/` and `srcExclude`s
`superpowers/**` + `ROADMAP.md`. (If a cleaner submodule boundary is wanted later,
public pages can be re-nested under `docs/content/` — deferred, not done now.)

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

## Out of scope (deferred, tracked)

- **VitePress site / GitHub Pages** — separate `gsxhq/<site>` repo: `.vitepress/`
  config, theme, nav, Pages deploy Action, submodule/fetch of this repo's `docs/`.
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
```
