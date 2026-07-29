# README and Status Current-State Design

## Goal

Present gsx as an independent templating language and toolchain without
defining it through templ. Describe its current alpha status accurately: gsx is
used in production, while language and API compatibility may still change before
1.0.

## Scope

Update only:

- `README.md`
- `docs/guide/status.md`

Interop, comparison, performance, roadmap, and syntax documentation remain out of
scope.

## Evidence

The current repository ships:

- authored Go component signatures with exact named markup binding;
- HTML-shaped markup, control flow, slots, pipelines, attributes, and element
  values;
- contextual HTML, URL, CSS, and JavaScript escaping with explicit trust types;
- generation, formatting, project inspection, scaffolding, and the development
  loop;
- diagnostics, navigation, symbols, formatting, code actions, completion,
  completion resolution, and auto-import support through the language server;
- a standard-library-only runtime and an optional Vite front door;
- component class composition through exact parameters and fallthrough
  `gsx.Attrs`, pinned by the canonical corpus.

The current status page's component-style limitation is obsolete. Its raw
`[]string` class-part limitation is still true, but a small syntax-specific gap
is not a durable public status boundary and will be removed with the rest of the
drifting implementation-gap list. Detailed changing work remains in the roadmap.

## README

Keep the README concise and retain its current shape:

1. Open with gsx's own identity: a JSX-like templating language for Go that
   compiles to plain Go and streams HTML.
2. State that gsx is alpha and already used in production. Explain that alpha
   refers to compatibility before 1.0.
3. Explain the `.gsx` to `.x.go` build pipeline.
4. Describe the core product in its own terms:
   - Go-checked components;
   - HTML-shaped markup with ordinary Go;
   - contextual escaping and a standard-library-only runtime;
   - a Go-native toolchain with optional frontend tooling.
5. Keep the existing runnable taste and navigation sections.
6. Remove every reference to the templ project from the README.

## Status Guide

Organize the page around durable current-state claims:

1. **Maturity** — alpha, used in production, compatibility not guaranteed before
   1.0.
2. **Language and rendering** — typed components, composition, control flow,
   attributes, contextual escaping, and explicit trust boundaries.
3. **Toolchain** — generate, format, inspect, scaffold, and dev loop, with Vite
   optional.
4. **Editor support** — diagnostics, navigation, symbols, formatting, code
   actions, completion, and import-aware completion.
5. **Current boundaries** — generated sources remain part of the build; reference
   search is project-scoped; consumers should expect migration work before 1.0.
6. Link to the roadmap for detailed and changing implementation work.

Remove the machine-specific completion latency and the two class/style bullets.

## Verification

- Confirm `README.md` contains no standalone, case-insensitive `templ` reference
  (`templating` remains valid product vocabulary).
- Check every shipped-surface claim against the CLI/editor guides, code, and
  canonical corpus.
- Run Markdown formatting/link-oriented repository checks through `make check`.
- Run `gopls check -severity=hint` only if Go files change; no Go edits are
  planned.
