# vscode-gsx: VS Code extension (highlighting + LSP)

**Date:** 2026-06-26
**Status:** Approved (design)
**Topic:** A VS Code extension for `.gsx` — TextMate syntax highlighting plus a
language client that runs `gsx lsp`, delivering diagnostics, navigation, hover,
references, and formatting. Distributed to the VS Code Marketplace and Open VSX.

## Problem

gsx ships a language server (`gsx lsp`) and a tree-sitter grammar
([tree-sitter-gsx](https://github.com/gsxhq/tree-sitter-gsx)), but VS Code users
have no first-class support: VS Code's stable extension API highlights via
**TextMate grammars** (not tree-sitter), and nothing wires `gsx lsp` into VS
Code. Today a VS Code user gets no `.gsx` highlighting and no diagnostics /
navigation / formatting.

## Goals

1. Syntax highlighting for `.gsx` in VS Code, including the embedded languages
   (Go in `{ }`/`@{ }` holes and pipeline segments, JavaScript in `<script>`/JS
   attributes, CSS in `<style>`), working offline on install.
2. Wire `gsx lsp` as a language client so VS Code gets **diagnostics,
   go-to-definition, hover, find-references, and formatting** (format-on-save).
3. Obtain the `gsx` binary the gopls way: discover it, and offer a one-click
   install when it is missing.
4. A local-first distribution workflow: build an installable `.vsix` for
   testing, with a tag-gated CI release to the Marketplace and Open VSX.

## Non-goals (v1)

- **No formatter code in the extension.** Formatting is delivered entirely by
  the LSP (`textDocument/formatting` → `gsx fmt`). As the width-aware Doc-IR
  rewrite (`2026-06-26-gsx-formatter-doc-ir-design.md`) and the separate
  embedded JS/CSS formatter land in `gsx fmt`, the extension's formatting
  improves automatically with zero changes.
- **No bundled gsx binary** — require it on PATH / install it (see Binary
  manager). Bundling per-platform binaries is a possible future, not v1.
- **No tree-sitter / semantic-token highlighting in v1** — a coarse TextMate
  grammar is the base (see §2 for the full rationale: VS Code has no stable
  third-party tree-sitter highlighting API, and the wasm route degrades UX and
  embedded-language support). A tree-sitter-wasm semantic-tokens layer *on top*
  is a possible future augmentation, never the base.
- **No snippets, no completion** (the LSP offers no completion yet).

## Architecture

A standalone repository **`vscode-gsx`** (sibling to `gsx`, `tree-sitter-gsx`,
`vite-plugin-gsx`). TypeScript, bundled with **esbuild**, packaged with
**`@vscode/vsce`**. Marketplace identity **`gsxhq.gsx`** (publisher `gsxhq`,
created by the maintainer). Targets VS Code engine `^1.85`.

Four focused units.

### 1. Language contribution (static, declarative)

`package.json` `contributes`:

- `languages`: id `gsx`, `extensions: [".gsx"]`, an `aliases` entry (`"gsx"`),
  a `configuration` pointing at `language-configuration.json`, and the icon.
- `grammars`: `scopeName: "source.gsx"`, path to the TextMate grammar, plus
  `embeddedLanguages` mapping the embedded scopes to `source.go` /
  `source.js` / `source.css` so VS Code applies the right tokenization and
  bracket/comment behavior inside holes and raw bodies.

`language-configuration.json`: bracket pairs (`<>`, `{}`, `()`, `[]`),
auto-closing pairs (those plus quotes), surrounding pairs, and comment config.
Comment toggling is best-effort at the gsx layer (the file mixes Go `//`, gsx
braced `{/* */}`, and embedded JS/CSS comments); VS Code's
`embeddedLanguages` mapping handles comment toggling correctly *inside* the
embedded regions.

### 2. TextMate grammar (`syntaxes/gsx.tmLanguage.json`)

> **Why TextMate, not the existing tree-sitter grammar (decision + rationale).**
> The maintenance instinct — drive VS Code from `tree-sitter-gsx` and skip a
> second grammar — does not pan out in VS Code as of 2026:
> - VS Code has **no public/stable API** to register a third-party tree-sitter
>   grammar for highlighting; its native tree-sitter work is core-only, for a
>   few built-in languages. The only DIY route is bundling `web-tree-sitter`
>   wasm + a `DocumentSemanticTokensProvider`, which **augments, not replaces**
>   TextMate.
> - That route *degrades UX*: semantic-tokens-only coloring is gated on the
>   theme's `editor.semanticHighlighting` opt-in (some themes show nothing), can
>   flash unstyled on open, and — critically for gsx — **loses first-class
>   embedded-language support** (`embeddedLanguages` delegating Go/JS/CSS regions
>   to VS Code's real grammars). It is also *more* work, not less (wasm + a
>   provider, and you still want a base TextMate grammar).
> - **Every templating peer that embeds sub-languages uses a TextMate grammar
>   for VS Code** — templ, Astro, Svelte, Vue, Phoenix/HEEx; none uses
>   tree-sitter for VS Code highlighting. templ — the closest analogue (Go host,
>   `{ }` holes, HTML/JS/CSS) — ships a single coarse `source.templ` grammar
>   with a small `embeddedLanguages` map and leans on its LSP for semantics.
>
> So gsx follows the templ model: a **coarse, minimal** TextMate grammar that
> does structural coloring + embedded-language delegation, with the **LSP**
> (`gsx lsp`) providing semantic accuracy. The grammar is deliberately *not* a
> reimplementation of tree-sitter-gsx — keeping it coarse is what keeps the
> maintenance low.

Scope `source.gsx`. The grammar is intentionally **coarse**: it colors gsx
structure and hands embedded regions to the real Go/JS/CSS grammars. It mirrors
the capture vocabulary tree-sitter-gsx uses (`@tag`, `@type` for components,
`@keyword`, `@string`, `@comment`, `@attribute`, `@operator`,
`@punctuation.*`) translated to TextMate scope names (`entity.name.tag`,
`entity.name.type`/`support.class`, `keyword.control`, `string.quoted`,
`comment`, `entity.other.attribute-name`, `keyword.operator`, `punctuation.*`).
Anything requiring real type/semantic awareness is left to the LSP, not encoded
in regex.

Embedded-language regions use `begin/end` rules with `contentName` set to a
`meta.embedded.* <scope>`, paired with the `embeddedLanguages` map in the
language contribution so VS Code applies the real embedded grammar (coloring +
comment-toggle + brackets) inside each region:

- `{ … }` / `@{ … }` holes and `|>` pipeline segments → `source.go`.
- `<script>…</script>` body → `source.js`.
- `<style>…</style>` body → `source.css`.

**Authoring/maintenance:** author the grammar as a readable **YAML source**
(`syntaxes/gsx.tmLanguage.src.yaml`) and **generate** the committed JSON in the
build (the Astro/Svelte pattern), so edits stay legible. Grammar fidelity is
exercised by snapshot tests (below).

**Reuse beyond VS Code:** this same TextMate grammar is the artifact GitHub.com
highlighting consumes via **Linguist** — so it is not a VS-Code-only cost; it
also buys `.gsx` highlighting on GitHub. It is a *separate* grammar from
tree-sitter-gsx (which serves Neovim); maintaining both is the universal norm
for this class of language, and keeping each coarse keeps the burden small.

**Deferred augmentation (not v1):** tree-sitter wasm + a
`DocumentSemanticTokensProvider` *layered on top* of this TextMate base (the way
VS Code does its built-ins) could add context-aware precision later, reusing
`tree-sitter-gsx`'s grammar + highlight queries. It is an enhancement, never the
base, and is out of scope for v1.

### 3. Binary manager (`src/gsxBinary.ts`)

The gopls pattern — require `gsx`, make obtaining it effortless.

- **Resolve order:** `gsx.server.path` setting (if set) → `gsx` on `PATH` →
  `GOBIN` → `GOPATH/bin` (derived via `go env GOBIN GOPATH` when Go is present).
- **Missing / unrunnable:** show a notification with an **Install gsx** action
  that runs `go install github.com/gsxhq/gsx/cmd/gsx@latest` in a VS Code
  terminal (visible, so the user sees progress/errors), then offers to start
  the server. A **Dismiss** leaves the extension dormant (highlighting still
  works — it is independent of the binary).
- **Version surfacing:** capture `gsx version` for the output channel /
  troubleshooting. No hard minimum-version gate in v1 (the LSP is young and
  this extension tracks it); revisit if a capability mismatch appears.

The binary manager has one job — *produce a runnable `gsx` path or a clear
remediation* — and is unit-testable by mocking the filesystem / `which` lookup.

### 4. LSP client (`src/extension.ts`)

Thin `vscode-languageclient/node` integration.

- `activate`: resolve the binary (unit 3). If found, construct a
  `LanguageClient` whose `serverOptions` spawn `{ command: gsxPath, args:
  ["lsp"] }` over stdio; `clientOptions.documentSelector = [{ language: "gsx"
  }]`. Start it; register it for disposal on deactivate.
- Capabilities are **the server's** — diagnostics
  (`publishDiagnostics`), `definition`, `hover`, `references`, and
  `documentFormatting` arrive with no per-feature client code. Format-on-save
  works once the user enables `editor.formatOnSave` (optionally scoped to
  `[gsx]`); the extension does not override the user's global setting.
- An **output channel** ("gsx") carries server logs; `gsx.trace.server`
  (off/messages/verbose) drives LSP trace via the client's built-in tracing.

### Settings & commands

`contributes.configuration`:

| Setting | Type | Default | Effect |
| --- | --- | --- | --- |
| `gsx.server.path` | string | `""` | Absolute path to the `gsx` binary; empty ⇒ auto-discover |
| `gsx.trace.server` | enum | `"off"` | LSP message trace level (off / messages / verbose) |

`contributes.commands`:

| Command | Title | Action |
| --- | --- | --- |
| `gsx.installServer` | gsx: Install/Update Language Server | run `go install …/cmd/gsx@latest` |
| `gsx.restartServer` | gsx: Restart Language Server | stop + restart the `LanguageClient` |

## Data flow

```
.gsx file opened
   │
   ├─ TextMate grammar (source.gsx + embedded go/js/css)  → highlighting (offline, no binary)
   │
   └─ activate → resolve gsx binary
                   │ found        → LanguageClient spawns `gsx lsp` (stdio)
                   │                  → diagnostics, definition, hover, references, formatting
                   │ not found    → notification → [Install gsx] → go install → start
```

Formatting requests (`editor.action.formatDocument` / format-on-save) flow:
VS Code → LSP `textDocument/formatting` → `gsx fmt` engine → edits applied.
Nothing in the extension parses or formats gsx, Go, JS, or CSS.

## Error handling

- **gsx not found:** friendly notification + one-click install; highlighting
  unaffected; no crash.
- **Server crash:** `vscode-languageclient`'s default restart policy applies;
  `gsx.restartServer` is the manual lever; errors surface in the output channel.
- **`go` not installed (so install can't run):** the install action detects a
  missing `go` and points to the gsx install docs instead of failing opaquely.

## Testing

- **Grammar generation:** a build step compiles
  `syntaxes/gsx.tmLanguage.src.yaml` → `gsx.tmLanguage.json` (e.g. `js-yaml`).
  CI asserts the committed JSON matches a fresh generation (`git diff
  --exit-code`), so the source and artifact never drift.
- **Grammar snapshot tests** (`vscode-tmgrammar-test`): `.gsx` fixtures with
  inline scope assertions — a component tag vs a native tag, a `{ }` hole
  tokenized as Go, a `<style>` body as CSS, a `<script>` body as JS, comments.
  Runs in CI without VS Code, against the generated JSON.
- **Binary-manager unit tests:** resolve-order (setting > PATH > GOBIN >
  GOPATH/bin), missing-binary path, missing-`go` path — with the environment
  and lookups mocked.
- **Extension integration smoke test** (`@vscode/test-electron`): opens a
  `.gsx` doc, asserts the language id registers and (against a real or stub
  `gsx`) the client reaches `running`; a formatting round-trip if a real `gsx`
  is on PATH in CI.

## CI / release (local-first)

- **Local dev:** `npm run package` → `gsx-x.y.z.vsix`; install with
  `code --install-extension gsx-*.vsix`; iterate.
- **PR CI** (GitHub Actions): install deps → generate grammar (+ assert no
  drift) → typecheck → lint → grammar snapshot tests → unit tests →
  `vsce package`, uploading the `.vsix` as a build artifact (grab it without a
  local build).
- **Tag release CI:** on a `v*` tag, publish to the **Marketplace**
  (`vsce publish`) and **Open VSX** (`ovsx publish`) using `VSCE_PAT` /
  `OVSX_PAT` repository secrets (maintainer-provided). Publishing happens only
  on a tag — so the `.vsix` is tested locally before anything goes public.

## File structure (`vscode-gsx` repo)

```
package.json                     # manifest: languages, grammars, config, commands, scripts
language-configuration.json      # brackets / autoclose / comments
syntaxes/gsx.tmLanguage.src.yaml # TextMate grammar source (authored)
syntaxes/gsx.tmLanguage.json     # generated from the YAML (committed; source.gsx + embeds)
src/extension.ts                 # activate/deactivate + LanguageClient wiring
src/gsxBinary.ts                 # resolve / install / version the gsx binary
test/grammar/*.gsx               # tmgrammar-test fixtures + assertions
test/suite/*.ts                  # binary-manager unit + integration smoke
.github/workflows/ci.yml         # PR: build/test/package artifact
.github/workflows/release.yml    # tag: publish Marketplace + Open VSX
icons/ , README.md , CHANGELOG.md, LICENSE
```

## Dependency on other gsx work

- **Consumes** `gsx lsp` (existing) for all language intelligence.
- **Consumes** `gsx fmt` for formatting via the LSP; **auto-improves** as the
  Doc-IR formatter rewrite and the embedded JS/CSS formatter (separate efforts)
  land. No coordination needed beyond "the LSP keeps speaking
  `textDocument/formatting`."
- **Parallel** to `tree-sitter-gsx` (Neovim highlighting) — the two grammars are
  independent; this one is TextMate for VS Code.

## Out of scope (future, separate specs)

- Bundling per-platform `gsx` binaries in the `.vsix`.
- Tree-sitter-wasm or LSP-semantic-token highlighting layered on the TextMate
  base (precision augmentation; rationale for deferring in §2).
- Snippets; completion (blocked on the LSP gaining completion).
- A `.gsx`-aware Markdown/embedded experience beyond the four units above.
