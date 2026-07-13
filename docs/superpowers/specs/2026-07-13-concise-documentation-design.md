# Concise User Documentation Design

- **Date:** 2026-07-13
- **Status:** Approved in brainstorming
- **Scope:** `docs/guide/**`, followed by website-only documentation surfaces in
  `../gsxhq.github.io`

## Goal

Rewrite the public gsx documentation as a concise, example-led guide and
reference. A reader should be able to find a rule, see its normal use, and move
on without reading specification-style prose.

The target style is a strong Stack Overflow answer:

1. state the rule;
2. show the normal use;
3. show the result when it clarifies the rule;
4. list only the exceptions a typical user is likely to encounter.

## Problem

The guide is accurate and well covered, but several pages read like design
specifications. Long paragraphs combine user behavior, implementation details,
historical comparisons, diagnostics, and rare edge cases. The cited
`attributes.md` section is representative: a reader looking for the precedence
rule must extract it from details about generated fields, internal assembly,
old eager-merge behavior, module analysis, and direct bag iteration.

This is unreleased software. Public docs do not need migration history or
compatibility explanations for behavior that users never encountered in a
release.

## Editorial contract

### Lead with the answer

Each section starts with one or two sentences stating the user-visible rule.
The primary example follows immediately. Avoid motivation before the answer
unless the reader needs it to choose the right syntax.

Use this default shape:

```markdown
## Conditional attributes

Use `{ if ... }` inside an opening tag to add attributes conditionally.

<!--@include: ./_generated/attributes/030-conditional-attributes.md-->

An `else` branch works too:
`{ if active { class="active" } else { class="idle" } }`.
```

Headings should name a user task or concept. Do not add a heading merely to
hold one extra paragraph.

### Examples carry the explanation

Each main concept gets one complete runnable example sourced from the existing
`examples/*.txtar` pipeline. The example remains golden-tested and includes an
Open in Playground link.

Variants use small inline or fenced snippets. Do not repeat `package`, imports,
component declarations, rendered HTML, and Playground links for every minor
variation. Show rendered output only when it demonstrates semantics that are
not obvious from the source, such as omission, escaping, ordering, or merge
precedence.

### Document current user behavior

Keep:

- syntax and observable rendering behavior;
- the normal choice between two public forms;
- security behavior and explicit trust boundaries;
- errors or constraints that change how a user writes gsx;
- advanced cases representing recognizable JSX, templ, or `html/template`
  patterns.

Remove:

- codegen and generated-code mechanics;
- internal helper names when the user never calls them;
- parser, resolver, module-analysis, and lowering explanations;
- development history, former behavior, and migration baggage;
- rare cases without a realistic application pattern;
- repeated explanations already owned by another page.

Link to the owning page instead of restating its full contract. Public API
methods may use a compact table when the methods themselves are the subject.

### Use advanced sections sparingly

An **Advanced cases** section is justified only when all of these are true:

1. the case occurs in real application code;
2. it maps to a familiar pattern from JSX, templ, or `html/template`, or avoids
   a likely production mistake;
3. it cannot be explained as a short variant beside the main example.

Advanced sections follow the same rule-first, example-led format. They are not
a place to preserve completeness for its own sake.

### Prefer deletion over relocation

Implementation and history details are deleted, not moved into another public
page. Internal engineering specs under `docs/superpowers/**` remain the place
for design rationale and implementation contracts.

## Information architecture

Keep the current public page structure and stable URLs by default. Rewrite
within existing pages using clearer sections and shorter paragraphs. Split a
page only when it serves distinct reader tasks that remain difficult to scan
after editing; do not create many small pages solely to reduce word counts.

Preserve useful anchors where practical. An obsolete implementation-focused
anchor may be removed after links in both repositories are checked and updated.

`docs/guide/**` remains the canonical content source. The website syncs that
tree. Do not independently edit synced copies under
`../gsxhq.github.io/guide/**`.

## Rollout

### Phase 1: syntax reference

Rewrite every hand-authored page in `docs/guide/syntax/`, starting with the
dense pages that establish the pattern:

1. `attributes.md`
2. `escaping.md`
3. `composition.md`
4. `props.md`
5. `interpolation.md`
6. `pipelines.md`
7. the remaining syntax pages and `docs/guide/syntax.md`

Generated `_generated/**` example partials remain generated artifacts. Change
their source fixtures or generator only when the selected example itself needs
to become more focused.

### Phase 2: the rest of the guide

Apply the same contract to every page in `docs/guide/**`, prioritizing the
dense CLI, configuration, editor, extension, and dev-loop pages. Tutorial,
vision, status, comparison, and pattern pages may keep different structures,
but all must remain answer-first and example-led where code behavior is being
explained.

### Phase 3: website-only surfaces

Inspect `../gsxhq.github.io` after canonical content is rewritten. Update only
content and presentation owned there, including the home page, VitePress
navigation/sidebar, theme callouts, and any website-only copy. Run the normal
copy-mode sync before reviewing or building the site.

## Accuracy and review

Concise must not mean approximate. For every rewritten section:

- verify syntax and observable behavior against the corpus, runtime, parser, or
  codegen as appropriate;
- keep runnable examples sourced from `examples/*.txtar` and their render
  goldens;
- run `make ci-examples` when examples or generated includes change;
- check local Markdown links and literal `{{ }}` VitePress handling;
- sync and run `npm run build` in `../gsxhq.github.io` for each publishable
  phase;
- review the rendered pages at desktop and narrow widths;
- use an adversarial factual review before declaring a phase complete.

Word count is a diagnostic, not a target. Success is measured by whether each
section answers one reader question quickly while retaining the behavior users
actually need.

## Non-goals

- Changing gsx syntax, runtime behavior, or public APIs.
- Replacing the runnable-example pipeline with hand-tested Markdown snippets.
- Creating a second exhaustive reference to hold deleted details.
- Redesigning the VitePress theme before content proves a presentation change
  is necessary.
- Rewriting internal specs, plans, generated references, or the engineering
  roadmap into Stack Overflow style.

## Completion criteria

The effort is complete when:

- every public guide page has been reviewed under this editorial contract;
- the syntax reference consistently leads with rules and examples;
- implementation and history baggage has been removed;
- retained advanced cases have a recognizable user need;
- runnable examples and links pass their drift checks;
- the synced website builds and the rendered reading experience is verified;
- any website-only copy and navigation follow the same concise structure.
