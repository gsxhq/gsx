# Design: `tree-sitter-gsx` — editor/GitHub highlighting for gsx

**Date:** 2026-06-23
**Status:** Approved (design)
**Repo:** new, separate — `github.com/gsxhq/tree-sitter-gsx` (the `tree-sitter-<lang>`
convention). The design doc lives here in the gsx repo; the implementation lives in
the new repo.

## Goal

A tree-sitter grammar that makes syntax highlighting **just work** across the four
languages a `.gsx` file mixes — **Go, HTML, JavaScript, CSS** — with gsx's only
syntax additions handled: the **`@{ }`** interpolation hole (inside JS/CSS contexts),
the **`{ }`** Go-expression hole (in markup), and the **`|>`** pipeline. The base
languages are delegated to their existing tree-sitter grammars via **injection**; gsx
parses only its own structure.

**v1 consumers:** tree-sitter-native editors (Neovim, Helix, Zed) and **GitHub**
(Linguist runs tree-sitter). One grammar + query files reaches all of them. VS Code
(TextMate / LSP semantic tokens) is out of scope for v1.

## Architecture (chosen: purpose-built grammar + injections)

A `grammar.js` plus an external `src/scanner.c`. The grammar natively parses **only
gsx structure**; every base-language span is exposed as a node whose byte range is
**injected** into `go` / `javascript` / `css`. Rejected alternatives: forking
templ/astro (gsx differs enough that adaptation ≈ fresh work) and a minimal grammar
that injects tree-sitter-html for markup (html can't model component tags, `{ }`
holes, `{ if }`, or fragments — markup highlighting would break).

This grammar is an **independent re-implementation** of gsx's boundary rules — it
cannot call the Go `parser/`. The gsx `examples/` corpus is the shared oracle that
keeps the two in agreement (see Testing).

## Repo layout

```
tree-sitter-gsx/
  grammar.js                       # the gsx grammar
  src/scanner.c                    # external scanner (boundary scanning)
  src/parser.c                     # generated; checked in (Linguist + zero-build install)
  src/tree_sitter/parser.h         # generated header
  src/grammar.json, node-types.json# generated
  queries/highlights.scm
  queries/injections.scm
  test/corpus/*.txt                # tree-sitter test cases (input → expected S-expr)
  test/examples/                   # the 12 gsx examples, copied as a parse oracle
  tree-sitter.json                 # grammar metadata (name, scope, file-types, injection-regex)
  package.json                     # minimal — lets `tree-sitter generate/test` run
  README.md
  .github/workflows/ci.yml         # generate + test on push
```

`package.json`/`tree-sitter.json` are the **dev toolchain** (to run the tree-sitter
CLI), not distribution packaging. npm/crate bindings and publishing are deferred.

## What the grammar parses natively

The file is a sequence of **Go-declaration ranges** and **`component` declarations**:

- `component (recv)? Name(params) { … }` — the `component` keyword, optional receiver,
  name, parameter list (a Go range), and the markup body.
- Everything outside a `component` (package clause, imports, `func`/`type`/`var`/`const`)
  is a Go-declaration range (injected as Go; the grammar does not parse Go itself).

Inside a component body, the **markup tree**:

- Elements `<div>…</div>`, void/self-closing `<br/>`, hyphenated `<el-dialog>`.
- Component tags `<Card>`, dotted `<ui.Button>` / `<p.Content>` (capitalized or dotted).
- Fragments `<>…</>`; `<!DOCTYPE>`; HTML comments `<!-- -->`; `{/* … */}` content comments.
- Attributes: static `name="v"`, expr `name={ expr }`, bare boolean `name`, spread
  `{...expr}`, conditional `{ if cond { attr } }`.
- Control flow `{ if/for/switch … { … } }` and `{{ stmt }}` Go blocks.
- Interpolation holes `{ expr }` (and `{ expr? }` try-marker).
- Raw-text regions `<script>…</script>` and `<style>…</style>` (their bodies are NOT
  parsed as markup — they are raw, scanned for `@{ }` holes and injected).
- Pipelines: within any hole or expr-attr value, `expr |> filter |> filter(arg)` — the
  grammar splits at top-level `|>` into segments, each a Go range, with `|>` as a token.

## External scanner (`scanner.c`)

The boundary scanner — the part tree-sitter's CFG cannot express — mirroring the Go
parser's logic so both agree:

1. **Hole end (`goExprEnd`).** Given `{`/`@{`, find the matching `}` while respecting
   Go string/rune/raw-string literals and nested `{}`.
2. **Top-level `|>`.** Within a hole/attr value, locate `|>` operators at brace/paren
   depth zero (not inside a nested call or string), to split pipeline segments.
3. **Raw text.** Consume `<script>` / `<style>` bodies up to the matching close tag,
   emitting raw-text and `@{ }` hole tokens (so a `}` inside JS/CSS is not treated as a
   hole end unless it closes an `@{`).
4. **Markup-vs-Go (the Babel rule).** Decide whether a `{ … }` in a body/expression
   position is **markup** (`{ <div/> }`) or a **Go expression** (`{ a < b }`),
   positionally — the same rule the parser uses. Drives whether the hole's content is
   injected as Go or recursed as markup.

## Injections (`queries/injections.scm`)

| Range | Injected language | Notes |
|---|---|---|
| Go-declaration ranges, `params`, `{ expr }`, `={ expr }`, `{{ stmt }}`, `{ if/for cond }`, each `\|>` segment, `@{ goExpr }` expr | `go` | each pipeline segment injected separately so Go never sees `\|>` |
| `<script>` text runs; JS-context attrs (`on*`, `@*`, `x-*`, `:*`, `hx-on*`) | `javascript` | `#set! injection.combined` — `@{ }` holes carved out, runs stitched into one JS doc |
| `<style>` text runs; `style=` attr value | `css` | `#set! injection.combined` — `@{ }` holes carved out |

Combined injection is what gives correct JS/CSS highlighting across `@{ }` holes (the
same technique astro/svelte/vue use). The hole nodes sit on top with their own Go
injection + a distinct highlight.

## Highlighting (`queries/highlights.scm`)

gsx's own nodes map to standard capture names; base-language tokens come from the
injected grammars:

- `component`, `if`, `for`, `switch`, `else` → `@keyword`
- component name → `@function` (decl); element tag → `@tag`; component tag → `@type`
- attribute name → `@attribute`; `@tag.attribute`
- `|>` → `@operator`; `?` try-marker → `@operator`
- `{` `}` `@{` `{{` `}}` `<>` `</>` → `@punctuation.special`; `<` `>` `/` → `@punctuation.bracket`
- strings (static attr values) → `@string`; comments → `@comment`

## Testing

- **`test/corpus/*.txt`** — tree-sitter's native test format: input snippet → expected
  S-expression, one file per construct (elements, components, holes, control-flow,
  script-with-holes, style-with-holes, pipeline, attributes, fragments, the Babel
  corner cases from `examples/06`).
- **Parse oracle** — every file in `test/examples/` (the 12 gsx examples) parses with
  **zero ERROR/MISSING nodes**. This is the shared contract with the Go parser; the gsx
  `examples/` are the source of truth. A note in the README explains they must be
  re-synced when gsx syntax changes.
- **Highlight assertions** — a few `tree-sitter highlight` tests over snippets that
  exercise all four languages plus `@{ }` and `|>`, to catch injection regressions.
- **CI** — `tree-sitter generate && tree-sitter test` on every push.

## Out of scope (deferred)

- `queries/indents.scm`, `folds.scm`, `locals.scm`.
- npm / crate bindings and publishing; `binding.gyp`.
- Submission to nvim-treesitter and GitHub Linguist (their gated inclusion processes).
- VS Code highlighting (TextMate grammar or LSP semantic tokens).
- The `gsx lsp` language server (separate effort; will reuse the Go core, not this grammar).

## Risks / open notes

- **Duplication of boundary logic.** The scanner re-implements `goExprEnd` / Babel-rule
  / raw-text scanning independently of the Go parser. Mitigated by the examples oracle;
  accepted as inherent (tree-sitter is language-agnostic C/JS, no Go reuse).
- **Combined-injection fidelity.** A JS/CSS construct that *spans* a hole highlights
  approximately (the run concatenation is positionally correct but semantically
  fragmented). This matches the state of the art for template grammars and is
  acceptable for highlighting.
- **Build toolchain.** Implementation needs the `tree-sitter` CLI + a C compiler + the
  injected `go`/`javascript`/`css` grammars available for highlight tests.
