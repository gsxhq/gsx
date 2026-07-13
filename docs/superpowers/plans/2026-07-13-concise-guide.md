# Concise Guide Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rewrite every non-syntax page in `docs/guide/**` so readers get the normal command, configuration, workflow, or decision first, with examples carrying most of the explanation.

**Architecture:** Keep the existing guide routes and source-of-truth boundary: hand-authored Markdown remains in `docs/guide/**`, while `../gsxhq.github.io/guide/**` is only a synced verification copy. Group pages by reader task so each commit has one factual owner and one review gate. Verify operational pages against current CLI/config/LSP source and tests; treat vision, comparison, status, and benchmark pages as concise product guidance rather than syntax specifications.

**Tech Stack:** Markdown, Go CLI/config source and tests, VitePress, npm, the existing website copy-sync pipeline.

## Global Constraints

- Follow `docs/superpowers/specs/2026-07-13-concise-documentation-design.md`.
- Lead with the answer; use **rule or decision -> normal example -> result when useful -> likely exceptions**.
- Prefer one complete example per reader task and short snippets for variants.
- Keep current observable behavior, security boundaries, writing constraints, and recognizable Go/JSX/templ/`html/template` workflows.
- Delete implementation, resolver, cache-key, transport, former-behavior, migration, and unreleased-history prose that does not change what a user does.
- Link to the owning syntax page rather than repeating syntax contracts.
- Preserve public routes and useful inbound anchors; use explicit VitePress heading IDs when concise headings would change them.
- `docs/guide/**` is canonical. Never hand-edit the ignored `../gsxhq.github.io/guide/**` copy.
- Literal `{{ }}` prose in `docs/guide/**` stays inside a `::: v-pre` block.
- Do not change runtime, generator, CLI, LSP, syntax, or website-owned behavior
  in this phase. The user-facing scaffold README template may change so newly
  generated projects agree with the guide.

---

## File Structure

- Modify `docs/guide/getting-started.md`: shortest route from install to first live edit and production build.
- Modify `docs/guide/learn.md`: six small concepts that teach the normal authoring model.
- Modify `docs/guide/dev-loop.md`: what `gsx dev` does, failure behavior, and customization links.
- Modify `gen/templates/init/simple/README.md.tmpl`: keep newly scaffolded
  projects consistent with the corrected development and production commands.
- Modify `docs/guide/cli.md`: task-oriented command reference with exact current flags and examples.
- Modify `docs/guide/config.md`: discoverable `gsx.toml` reference organized by common decisions.
- Modify `docs/guide/editor.md`: choose VS Code, Neovim, or a generic LSP client and configure only what is needed.
- Modify `docs/guide/extensions.md`: when a project-owned `gen.Main` binary is necessary and how to wire supported function-valued options.
- Modify `docs/guide/vision.md`: concise product rationale.
- Modify `docs/guide/principles.md`: short public commitments without parser/resolver rationale.
- Modify `docs/guide/comparisons.md`: practical selection and interop guidance.
- Modify `docs/guide/status.md`: current shipped/partial surface without speculative backlog prose.
- Modify `docs/guide/performance.md`: reproducible benchmark snapshot and caveats.
- Modify `docs/guide/patterns.md`: recipe index only.
- Modify `docs/guide/patterns/render-once.md`: one complete copyable per-request recipe and its required setup.
- Verification-only use of `/Users/jackieli/personal/gsxhq/gsxhq.github.io`: copy-sync, build, and rendered inspection.

## Task 1: Onboarding and the Development Loop

**Files:**
- Modify: `docs/guide/getting-started.md`
- Modify: `docs/guide/learn.md`
- Modify: `docs/guide/dev-loop.md`
- Modify: `gen/templates/init/simple/README.md.tmpl`
- Read: `gen/init.go`
- Read: `gen/dev.go`
- Read: `gen/devconfig.go`
- Read: `gen/watch.go`
- Read: `gen/init_test.go`
- Read: `gen/dev_test.go`
- Read: `gen/templates/init/simple/dot-gitignore`
- Read: `gen/templates/init/simple/package.json`
- Read: `/Users/jackieli/personal/gsxhq/vite-plugin-gsx/src/index.ts`

**Interfaces:**
- Consumes: the concise syntax reference completed in Phase 1.
- Produces: the primary newcomer path linked by the website home page and navigation.

- [ ] **Step 1: Verify the starter and dev-loop commands**

Run:

```bash
rg -n 'go 1\.|node|packageManager|npm run dev|go tool gsx|npm run build|--yes' gen/init.go gen/init_test.go gen/templates/init/simple
rg -n 'NewFlagSet\("dev"|StringVar|BoolVar|\.env|go\.mod|go\.sum|last working|VITE_DEV_URL|resolveDevConfig' gen/dev.go gen/devconfig.go gen/watch.go gen/dev_test.go
rg -n 'apply:.*serve|generate|build' /Users/jackieli/personal/gsxhq/vite-plugin-gsx/src/index.ts
go run ./cmd/gsx init -h
go run ./cmd/gsx dev -h
```

Expected: current install/scaffold flags, default dev commands, watched files, logging behavior, and first-build behavior are supported by source or tests before they appear in prose.

- [ ] **Step 2: Rewrite `getting-started.md` as the shortest successful path**

Use this reader flow:

```markdown
# Getting started
## Prerequisites
## Create a project
## Start the development server
## Make the first change
## Build for production
## Next steps
```

Keep one command sequence for install/scaffold, one for `npm run dev`, one
visible edit instruction, and one clean-checkout production sequence:
`npm run build`, `go tool gsx generate`, `go build -o app`, then `./app`.
State that generated `*.x.go` files are ignored by the scaffold and should not
be hand-edited or committed. Keep the binary-name collision note beside
installation. Move alternate package-manager configuration to one sentence plus
a link to `[dev]` configuration. Replace the detailed regenerate/build/swap
sequence with one result-focused sentence and a link to `dev-loop.md`.

- [ ] **Step 3: Keep `learn.md` as six small examples**

Keep these six concepts and their normal examples:

```markdown
# Learn gsx
## 1. A component is Go plus markup
## 2. Props are typed
## 3. Components compose with children
## 4. Attributes are explicit
## 5. Style and script stay close to HTML
## 6. Save and reload
## Next steps
```

Each section starts with one rule sentence and immediately shows or points to its example. Link escaping, composition, attributes, and styling to their owning syntax pages instead of describing generated code or the full dev pipeline.

- [ ] **Step 4: Rewrite `dev-loop.md` around observable behavior**

Use this flow:

```markdown
# Dev loop
## Run it
## What happens on save
## When a build fails
### The first build
## Customize the commands
```

Keep the Mermaid sequence only if it makes the save sequence faster to understand
than a five-item list; do not keep both. State the normal `.gsx` sequence as
`watch -> generate -> build -> swap -> reload`, and that the last working server
remains active after later failures. State dependency behavior separately:
`.go`, `go.mod`, and `go.sum` changes refresh affected generation before rebuild;
`.env` changes restart the existing backend with fresh environment values and do
not regenerate or rebuild. Delete the stale first-build/backend-down/log-file
subsection. State only that before the first successful build there is no last
working server; fix the startup error and save again. Link command flags and
`[dev]` configuration rather than documenting warm resolver, cache-directory,
HTTP endpoint, HMR transport, or process-supervision implementation.

- [ ] **Step 5: Check the onboarding slice**

Run:

```bash
rg -n -i 'codegen\.Module|go/packages|resolver|cache dir|POST /__|HMR WebSocket|140 ms|1–2 ms|implementation|formerly|old behavior|migration' docs/guide/{getting-started,learn,dev-loop}.md gen/templates/init/simple/README.md.tmpl
for f in docs/guide/{getting-started,learn,dev-loop}.md; do awk -v f="$f" 'BEGIN{p=0} /^$/{if(p>100) print f ":" NR ": " p " words"; p=0; next} {p+=NF} END{if(p>100) print f ":EOF: " p " words"}' "$f"; done
git diff --check
```

Expected: no implementation/history baggage, no paragraph over 100 words, and no whitespace errors.

- [ ] **Step 6: Review and commit the onboarding slice**

Run the task review against the design spec, then:

```bash
git add docs/guide/getting-started.md docs/guide/learn.md docs/guide/dev-loop.md gen/templates/init/simple/README.md.tmpl
git commit -m "docs: simplify onboarding and dev loop"
```

Expected: one focused commit containing only the three onboarding pages.

## Task 2: CLI Reference

**Files:**
- Modify: `docs/guide/cli.md`
- Read: `gen/main.go`
- Read: `gen/init.go`
- Read: `gen/dev.go`
- Read: `gen/fmt.go`
- Read: `gen/info.go`
- Read: `gen/watch.go`
- Read: `gen/main_test.go`
- Read: `gen/init_test.go`
- Read: `gen/dev_test.go`
- Read: `gen/fmt_test.go`
- Read: `gen/info_test.go`

**Interfaces:**
- Consumes: the onboarding pages' short links to command details.
- Produces: the authoritative user-facing command and environment reference.

- [ ] **Step 1: Capture exact current help and inbound anchors**

Run:

```bash
go run ./cmd/gsx help
for cmd in init dev generate fmt info clean lsp; do go run ./cmd/gsx "$cmd" -h; done
rg -n 'cli\.md#|\./cli\.md#' README.md docs/guide
rg -n '^#{1,4} ' docs/guide/cli.md
```

Expected: a complete current command/flag inventory plus all guide links that depend on CLI heading IDs. Some commands may print help to stderr while still exiting successfully; record the rendered flag rather than relying on old prose.

- [ ] **Step 2: Rewrite the command overview and global flags**

Use an opening table with one reader task per command, then:

```markdown
# CLI
## Choose a command
## Global flags
```

Document `-C` as the only true global flag. State beside `generate` that `-q`
and `-v` may appear before or after that command but affect generation only.
Remove stock-binary/custom-binary implementation framing from the introduction;
link to Extensions only where a custom binary changes invocation.

- [ ] **Step 3: Rewrite project and development commands**

Use:

```markdown
## `gsx init`
### Interactive setup
### Non-interactive setup
## `gsx dev`
### Common customizations
```

For each command, show the normal invocation first, then a compact flag table
generated from current help, then no more than two realistic examples. For
`init`, retain target directory, `--module`, `--force`, `--yes`/`-y`, and the
single `simple` template. For `dev`, retain the optional directory, `--web`,
`--no-web`, `--build`, `--run`, `--log`, `--log-file`, and `--no-log`. Keep the
first-build/logging caveat only where it changes troubleshooting. Link `[dev]`
configuration for persistent command overrides.

- [ ] **Step 4: Rewrite generation and formatting commands**

Use:

```markdown
## `gsx generate`
### Diagnostics and exit status
### Watch mode
## `gsx fmt`
### Check formatting in CI
```

Keep the normal generate/fmt commands, path selection, `--watch`,
`--format=ndjson`, `--json`, `--no-cache`, write/check behavior, and exit
statuses only when they drive scripts. For formatting, retain `-w`, `-l`, `-d`,
`-imports=goimports|gofmt`, and `-no-imports`. Collapse flag permutations into
one table and a few command examples. State that CSS/JS bodies are formatted;
do not preserve the stale “in development” note. Remove warm resolver timings,
package-load internals, cache-key rationale, and exhaustive control-flow
narration.

- [ ] **Step 5: Rewrite inspection and utility commands**

Use:

```markdown
## `gsx info`
## `gsx clean`
## `gsx lsp`
## `gsx version`
## Environment variables
## Stability
```

Show `info` and `info --json`, `clean --cache`, and the fact that editors launch
`lsp`; link editor setup rather than explaining JSON-RPC. Do not embed a sample
`info` filter table that will drift. State that `version` includes available
build/VCS metadata. Put current environment variables in one compact table with
user-visible effect and precedence. Link Status instead of listing deferred CLI
features.

- [ ] **Step 6: Verify the CLI page against help**

Run:

```bash
rg -n -i 'go/packages|resolver|cache key|JSON-RPC|implementation|formerly|old behavior|migration|hardcoded standard codegen' docs/guide/cli.md
awk 'BEGIN{p=0} /^$/{if(p>100) print NR ": " p " words"; p=0; next} {p+=NF} END{if(p>100) print "EOF: " p " words"}' docs/guide/cli.md
git diff --check
go test ./gen -run 'TestRun|TestInit|TestDev|TestFmt|TestInfo' -count=1
```

Expected: no internal/history prose, no paragraph over 100 words, clean Markdown, and focused CLI tests pass.

- [ ] **Step 7: Review and commit the CLI slice**

```bash
git add docs/guide/cli.md
git commit -m "docs: make CLI reference task oriented"
```

Expected: one commit containing only `cli.md`.

## Task 3: Configuration Reference

**Files:**
- Modify: `docs/guide/config.md`
- Read: `gen/configfile.go`
- Read: `gen/devconfig.go`
- Read: `gen/envconfig.go`
- Read: `gen/configfile_test.go`
- Read: `gen/devconfig_test.go`
- Read: `gen/info.go`
- Read: `internal/attrclass/attrclass.go`

**Interfaces:**
- Consumes: the CLI page's command and environment ownership.
- Produces: the authoritative declarative configuration reference used by Styling, Pipelines, Editor, and Extensions.

- [ ] **Step 1: Capture the schema, defaults, precedence, and inbound anchors**

Run:

```bash
rg -n 'type toml|toml:"|discoverConfig|resolveConfig|applyEnv|precedence|default' gen/configfile.go gen/devconfig.go gen/envconfig.go
rg -n 'config\.md#|\./config\.md#|\.\./config\.md#' README.md docs/guide
rg -n '^#{1,4} ' docs/guide/config.md
go test ./gen -run 'Test.*Config|TestInfo' -count=1
```

Expected: every retained key and default is backed by the current schema/tests, and all dependent anchors are known before headings change.

- [ ] **Step 2: Put the common configuration and discovery rules first**

Use:

```markdown
# Configuration
## Start with `gsx.toml`
## Discovery and precedence
## Development commands `[dev]` {#dev-development-loop}
```

Open with one small, valid file containing a representative `[dev]` table and
one named filter. State the general generated-output precedence as
`programmatic option > environment override > gsx.toml > default`, then link
the formatter section for its separate CLI/config/EditorConfig rules. Keep the
TOML top-level-key-before-table trap as a short warning beside the first
multi-section example: strict decoding reports a misplaced top-level key as an
unknown nested key; it is not silently accepted.

- [ ] **Step 3: Rewrite filters and renderers as recipes plus contracts**

Use:

```markdown
## Pipeline filters
### Named filters `[filters]` {#filters-named-pipeline-filters}
### Filter packages `filter_packages`
## Type renderers `[renderers]` {#renderers-type-directed-value-rendering}
```

Show one normal named-filter example, one package-registration variant, and one renderer example. Keep callable signatures, name/type key spelling, error-return behavior, and precedence because they change user code. Link Pipelines and Interpolation for syntax. Delete lowering, generated alias, `go/types` implementation, packages-load, and cache-key explanations.

- [ ] **Step 4: Rewrite URL, formatter, minify, and class-merger configuration**

Use:

```markdown
## URL attributes
### Exact and prefix rules `[[url_attrs]]`
### Presets `url_presets` {#url_presets-named-opt-in-rulesets}
## Formatter `[formatter]` {#formatter--gsx-fmt--editor-formatting}
### `.editorconfig`
## Asset minification `[minify]` {#minify-asset-minification-level}
## Class merging `class_merger` {#class_merger-tailwind-aware-class-merge-strategy}
```

Each section starts with when to use it, then one valid TOML example, then a
compact option/signature table. Include `srcset` and `imagesrcset` in the
built-in URL surface. Keep URL safety consequences; formatter width/tab
precedence `[formatter] > .editorconfig > built-in`; imports precedence
`CLI > [formatter].imports > goimports`; `none`/`full`; and the
`func([]string) string` class-merger contract. Do not claim a programmatic
width/tab option exists. Link Extensions for function-valued custom
formatters/minifiers. Remove parser choice, internal slice handling, cache
participation, and algorithm history.

- [ ] **Step 5: End with one complete file and verification commands**

Use:

```markdown
## Complete example
## What stays in Go
## Inspect the resolved configuration
```

Keep one compact, complete, typical `gsx.toml` with top-level keys before
tables; it need not repeat every optional key. Keep only function-valued
options under “What stays in Go,” linked to Extensions. Show `gsx info` and
`gsx info --json`, but describe each as an inspection view rather than claiming
they contain identical or exhaustive fields. Do not explain manifest/cache
internals.

- [ ] **Step 6: Verify the configuration page**

Run:

```bash
rg -n -i 'lowering|go/types|packages\.Load|cache key|internal|implementation|formerly|old behavior|migration|generated alias' docs/guide/config.md
awk 'BEGIN{p=0} /^$/{if(p>100) print NR ": " p " words"; p=0; next} {p+=NF} END{if(p>100) print "EOF: " p " words"}' docs/guide/config.md
git diff --check
go test ./gen -run 'Test.*Config|TestInfo' -count=1
```

Expected: concise public contracts only, no oversized prose paragraph, valid Markdown, and focused config tests pass.

- [ ] **Step 7: Review and commit the configuration slice**

```bash
git add docs/guide/config.md
git commit -m "docs: streamline configuration reference"
```

Expected: one commit containing only `config.md`.

## Task 4: Editor Support and Programmatic Extensions

**Files:**
- Modify: `docs/guide/editor.md`
- Modify: `docs/guide/extensions.md`
- Read: `internal/lsp/server.go`
- Read: `internal/lsp/*_test.go`
- Read: `gen/options.go`
- Read: `gen/main.go`
- Read: `gen/info.go`
- Read: `gen/manifest.go`
- Read: `internal/rawfmt/rawfmt.go`
- Read: `internal/printer/printer.go`
- Read: `/Users/jackieli/personal/gsxhq/vscode-gsx/package.json`
- Read: `/Users/jackieli/personal/gsxhq/vscode-gsx/src/extension.ts`
- Read: `/Users/jackieli/personal/gsxhq/vscode-gsx/src/gsxBinary.ts`
- Read: `/Users/jackieli/personal/gsxhq/tree-sitter-gsx/grammar.js`
- Read: `/Users/jackieli/personal/gsxhq/tree-sitter-gsx/queries/highlights.scm`
- Read: `/Users/jackieli/personal/gsxhq/tree-sitter-gsx/queries/injections.scm`

**Interfaces:**
- Consumes: CLI and Configuration ownership for invocation and declarative options.
- Produces: concise setup recipes for supported editors and the code-only configuration escape hatch.

- [ ] **Step 1: Verify current user-facing capabilities and options**

Run:

```bash
rg -n 'Completion|Hover|Definition|References|Formatting|CodeAction|DocumentSymbol|WorkspaceSymbol|organizeImports|quickfix' internal/lsp
rg -n '^func With|type Rule|type MinifyLevel' gen
rg -n 'gsx.server.path|Install/Update|Restart|Ghostscript|TextMate' /Users/jackieli/personal/gsxhq/vscode-gsx/package.json /Users/jackieli/personal/gsxhq/vscode-gsx/src/{extension,gsxBinary}.ts
rg -n 'injection|javascript|css|go' /Users/jackieli/personal/gsxhq/tree-sitter-gsx/grammar.js /Users/jackieli/personal/gsxhq/tree-sitter-gsx/queries/{highlights,injections}.scm
rg -n '^#{1,4} ' docs/guide/{editor,extensions}.md
rg -n '(editor|extensions)\.md#' README.md docs/guide
```

Expected: a source-backed capability list, all callable programmatic options, and inbound anchors.

- [ ] **Step 2: Rewrite `editor.md` around choosing an editor path**

Use:

```markdown
# Editor support
## VS Code
## Neovim
### Language server
### Syntax highlighting
## Other editors
## Language features
## Organize imports {#organize-imports-on-save}
## Troubleshooting the `gsx` binary
```

Lead with the bundled VS Code extension as the shortest path. Give Neovim one
LSP snippet and link the tree-sitter repository for installation rather than
embedding a self-updating plugin recipe. Describe Go as native to the unified
GSX grammar; only JavaScript and CSS regions are injected. Give generic editors
the command, language ID, selector, and root markers. Replace the
LSP-method/transport table with a user-feature table that includes document and
workspace symbols. Keep only practical organize-imports behavior: automatic
unambiguous imports, quick-fix choice when ambiguous, and `go get` for packages
outside the module. Delete module-cache timing, internal visibility examples,
analysis pipeline, JSON-RPC, and deferred-feature narration; link Status for
completion and external-reference limits.

- [ ] **Step 3: Rewrite `extensions.md` around the decision to build a custom binary**

Use:

```markdown
# Extending gsx
## When you need a custom binary
## Create `cmd/gsx/main.go`
## Custom CSS and JavaScript formatters {#custom-cssjs-formatter}
## Custom minifiers and minify level {#minify-level}
## Custom field matching
## Run the project binary
```

Start with a table: declarative filters/URL rules/renderers/minify/class merge
belong in `gsx.toml`; function-valued formatter/minifier/field-matcher options
require `gen.Main`. Show one complete project-owned `cmd/gsx/main.go`, then short
variants for each option. Run it explicitly as `go run ./cmd/gsx generate ...`.
State callable signatures, formatter error/panic fallback, option-over-config
precedence, and the custom CSS-minifier exception: interpolated `<style>` blocks
stay on the built-in hole-aware path. Describe the default CSS/JS formatters as
real formatters, not indentation-only. Remove the long programmatic URL-rule
recipe, resolved-manifest/cache claims, placeholder-token implementation,
built-in formatter internals, formatter-cache claims, and registration history.
Link `gsx info` for inspection without claiming human and JSON views are
identical.

- [ ] **Step 4: Verify editor and extension accuracy**

Run:

```bash
rg -n -i 'JSON-RPC|go/packages|module cache|1\.4 seconds|analysis pipeline|cache key|placeholder token|manifest schema|implementation|formerly|old behavior|migration' docs/guide/{editor,extensions}.md
for f in docs/guide/{editor,extensions}.md; do awk -v f="$f" 'BEGIN{p=0} /^$/{if(p>100) print f ":" NR ": " p " words"; p=0; next} {p+=NF} END{if(p>100) print f ":EOF: " p " words"}' "$f"; done
git diff --check
go test ./internal/lsp ./gen -count=1
```

Expected: reader-facing setup only, no long paragraphs, clean Markdown, and current LSP/generator tests pass.

- [ ] **Step 5: Review and commit the tooling slice**

```bash
git add docs/guide/editor.md docs/guide/extensions.md
git commit -m "docs: simplify editor and extension guides"
```

Expected: one focused commit containing the two tooling pages.

## Task 5: Product Rationale, Comparisons, Status, and Performance

**Files:**
- Modify: `docs/guide/vision.md`
- Modify: `docs/guide/principles.md`
- Modify: `docs/guide/comparisons.md`
- Modify: `docs/guide/status.md`
- Modify: `docs/guide/performance.md`
- Read: `README.md`
- Read: `docs/ROADMAP.md`
- Read: runtime public types in `node.go`, `rawjs.go`, `rawcss.go`
- Read: `/Users/jackieli/personal/gsxhq/gsx-bench/README.md`
- Read: the current commit and benchmark sources under `/Users/jackieli/personal/gsxhq/gsx-bench`

**Interfaces:**
- Consumes: the verified syntax and operations pages for factual links.
- Produces: short product guidance that helps a reader decide whether and how to use gsx.

- [ ] **Step 1: Check current claims against repo truth**

Run:

```bash
rg -n 'alpha|Shipped|Partial|Known|completion|references|style|class|diagnostic' docs/ROADMAP.md docs/guide/status.md
rg -n '^type Node|Render\(context\.Context|type Raw|RawURL|RawCSS' node.go rawjs.go rawcss.go
git -C /Users/jackieli/personal/gsxhq/gsx-bench rev-parse HEAD
rg -n 'Go 1\.|Apple M3|templ|benchmem|Benchmark' /Users/jackieli/personal/gsxhq/gsx-bench/README.md /Users/jackieli/personal/gsxhq/gsx-bench --glob '*.go'
rg -n 'gsx-bench|Go 1\.|Apple M3|benchmem' docs/guide/performance.md README.md
rg -n '(vision|principles|comparisons|status|performance)\.md#' README.md docs/guide
```

Expected: status, interop, trust-helper, and benchmark claims have a current source; drifting claims are corrected rather than preserved for completeness.

- [ ] **Step 2: Rewrite `vision.md` and `principles.md` without design-spec prose**

Use this `vision.md` flow:

```markdown
# Why gsx
## HTML-shaped Go components
## Checked by Go
## A build step with a fast dev loop
## Works with the Go ecosystem
## What gsx does not provide
```

Keep one representative component and short reasons to choose gsx. Qualify
generated props with bring-your-own props, and link Escaping instead of listing
an incomplete set of trusted-value helpers. Delete bounded-resolver/parser
architecture and historical tradeoff explanations. Link Comparisons for tool
choice and Interop for integration.

Use this `principles.md` flow:

```markdown
# Principles
## Stay close to HTML and Go
## Prefer readable syntax
## Let Go check the program
## Escape by default
## Keep the runtime small
```

Keep each principle to one short paragraph and links to the owning guide page. Delete generator mechanics and detailed escaping enumeration already covered by the syntax reference.

- [ ] **Step 3: Rewrite `comparisons.md` as a choice table plus short sections**

Open with a compact table comparing gsx, templ, `html/template`, and client-side
JSX by best fit, component typing, compile/load model, and primary rendering
model. Follow with one short section per alternative containing “choose this
when” guidance. Keep structural templ interoperability and link the runnable
Interop page. Remove templ feature-request history and deep compiler-model
justification.

- [ ] **Step 4: Rewrite `status.md` and `performance.md` as snapshots**

For Status, use:

```markdown
# Status
## Ready to use
## Current limits
## Roadmap
```

Summarize shipped areas by user task rather than exhaustive syntax enumeration. List only current limits that change adoption decisions; do not preserve speculative deferred commands or empty “Known gaps” sections when the roadmap is the owner.

For Performance, state the streaming model in one sentence, put reproduction
before claims, keep the dated machine/Go-version snapshot tables, and identify
the benchmark repository commit or snapshot date so the numbers are
reproducible. End with one caveat that readers should benchmark their own
workload. Do not explain writer-path or allocation implementation.

- [ ] **Step 5: Check product pages for duplication and unsupported claims**

Run:

```bash
rg -n -i 'go/packages|go/types|resolver|parser complexity|codegen from|generated code is direct|writer|long-standing requests|formerly|old behavior|migration|next candidate' docs/guide/{vision,principles,comparisons,status,performance}.md
for f in docs/guide/{vision,principles,comparisons,status,performance}.md; do awk -v f="$f" 'BEGIN{p=0} /^$/{if(p>100) print f ":" NR ": " p " words"; p=0; next} {p+=NF} END{if(p>100) print f ":EOF: " p " words"}' "$f"; done
git diff --check
```

Expected: no internal/history narrative, no oversized paragraph, and no whitespace errors.

- [ ] **Step 6: Review and commit the product slice**

```bash
git add docs/guide/vision.md docs/guide/principles.md docs/guide/comparisons.md docs/guide/status.md docs/guide/performance.md
git commit -m "docs: sharpen product and status guidance"
```

Expected: one focused commit containing the five product pages.

## Task 6: Patterns

**Files:**
- Modify: `docs/guide/patterns.md`
- Modify: `docs/guide/patterns/render-once.md`
- Read: `node.go`
- Read: current `docs/guide/patterns/render-once.md` recipe as the behavioral baseline

**Interfaces:**
- Consumes: the Context and Composition syntax pages.
- Produces: a concise recipe index and one copyable advanced pattern retained because it maps to a common component-ecosystem need.

- [ ] **Step 1: Verify the recipe's public dependencies and inbound links**

Run:

```bash
rg -n '^type Node|Render\(|context\.Context|WithValue' node.go docs/guide/patterns/render-once.md
rg -n 'patterns(?:/render-once)?\.md#|patterns/render-once' README.md docs/guide
rg -n '^#{1,4} ' docs/guide/patterns.md docs/guide/patterns/render-once.md
```

Expected: the recipe depends only on public Go/gsx behavior, and useful anchors are known.

- [ ] **Step 2: Reduce `patterns.md` to the available recipe index**

Keep a one-sentence explanation that patterns are userland conventions, then the Render once link and its use cases. Delete the speculative Planned section and future integration candidates.

- [ ] **Step 3: Rewrite `render-once.md` as one complete recipe**

Use:

```markdown
# Render once
## Copy the helper
## Install the request scope
## Add the component
## Use it
## What happens without middleware
```

Keep the complete `OnceHandle`, per-request state, middleware, `<Once>`
component, and usage example because the reader must copy a coherent
implementation. The `onceState` owns a `sync.Mutex`; `firstRender` locks across
the seen lookup and insert so concurrent renders cannot race. Keep the non-zero
handle field warning and per-request scope rule. Delete the templ port/history
callout, generated-renderer comparison, duplicated rationale, and separate
scope-note prose that can be stated beside setup. State the failure mode once:
without middleware, content renders every time rather than panicking.

- [ ] **Step 4: Verify snippets and commit the patterns slice**

Create a throwaway module under `/tmp/gsx-render-once-probe` containing the
combined documented helper and tests for same-request dedup, fresh rendering in
a new request, distinct handles, missing-middleware behavior, and concurrent
first-render calls. Run `go test -race ./...` in that module. Then run:

```bash
rg -n -i 'ported from|faithful port|generated code|implementation|formerly|old behavior|migration|planned|next candidates' docs/guide/patterns.md docs/guide/patterns/render-once.md
for f in docs/guide/patterns.md docs/guide/patterns/render-once.md; do awk -v f="$f" 'BEGIN{p=0} /^$/{if(p>100) print f ":" NR ": " p " words"; p=0; next} {p+=NF} END{if(p>100) print f ":EOF: " p " words"}' "$f"; done
git diff --check
git add docs/guide/patterns.md docs/guide/patterns/render-once.md
git commit -m "docs: condense render-once pattern"
```

Expected: public recipe only, no long prose, clean Markdown, and one focused commit.

## Task 7: Full Guide Verification and Adversarial Review

**Files:**
- Review: `docs/guide/*.md`
- Review: `docs/guide/patterns/*.md`
- Review: the completed Phase 1 syntax reference for consistent links and terminology
- Verification-only sync/build target: `/Users/jackieli/personal/gsxhq/gsxhq.github.io`

**Interfaces:**
- Consumes: Tasks 1–6 and the concise syntax reference.
- Produces: a fully reviewed canonical guide ready for the separate website-owned-copy phase.

- [ ] **Step 1: Run guide-wide editorial and link scans**

Run:

```bash
for f in docs/guide/*.md docs/guide/patterns/*.md; do awk -v f="$f" 'BEGIN{p=0} /^$/{if(p>100) print f ":" NR ": " p " words"; p=0; next} {p+=NF} END{if(p>100) print f ":EOF: " p " words"}' "$f"; done
rg -n -i 'implementation detail|codegen\.Module|go/packages|go/types|resolver|cache key|JSON-RPC|formerly|old behavior|migration|backward compat|writer path|next candidates' docs/guide/*.md docs/guide/patterns/*.md
rg -n '\]\([^)]*\.md#[^)]+\)' docs/guide/*.md docs/guide/patterns/*.md
git diff --check
```

Expected: no oversized prose, no accidental internal/history narrative, all anchor-bearing links are inspectable, and no whitespace errors.

- [ ] **Step 2: Run authoritative repository checks**

Run:

```bash
make ci
make lint
```

Expected: generated examples, tests, formatting, and lint gates pass with no drift.

- [ ] **Step 3: Copy-sync and build the website shell**

First require a clean website checkout:

```bash
git -C /Users/jackieli/personal/gsxhq/gsxhq.github.io status --short
```

Expected: no output. Then, from `/Users/jackieli/personal/gsxhq/gsxhq.github.io`:

```bash
GSX_DOCS_SRC=/Users/jackieli/personal/gsxhq/gsx/.worktrees/concise-docs npm run sync
GSX_DOCS_SRC=/Users/jackieli/personal/gsxhq/gsx/.worktrees/concise-docs npm run build
```

Expected: copy-mode sync names the worktree source, and VitePress completes without dead links, Vue interpolation errors, missing pages, or WASM failures.

- [ ] **Step 4: Review representative pages in the in-app browser**

Start the built preview from `/Users/jackieli/personal/gsxhq/gsxhq.github.io`:

```bash
npm run preview -- --host 127.0.0.1 --port 4173
```

Inspect these routes at desktop and 390x844:

```text
/guide/getting-started.html
/guide/learn.html
/guide/dev-loop.html
/guide/cli.html
/guide/config.html
/guide/editor.html
/guide/extensions.html
/guide/vision.html
/guide/principles.html
/guide/comparisons.html
/guide/status.html
/guide/performance.html
/guide/patterns.html
/guide/patterns/render-once.html
```

For each route verify: the answer or decision appears before background, examples fit, heading outlines remain scannable, tables do not overflow, previous/next navigation renders, and retained inbound anchors resolve. Click at least one pager link, one cross-guide anchor, and one external editor/benchmark link rather than checking presence only.

- [ ] **Step 5: Run an independent adversarial factual review**

Give a fresh reviewer the design spec, this plan, the full Phase 2 diff, current source/tests, and permission to build throwaway probes under `/tmp`. Require concrete checks for:

```text
Onboarding: scaffold command, default dev command, first-change path, production build.
CLI: every documented flag and exit-status claim against live help/tests.
Configuration: discovery boundary, option > env > file > default, TOML key placement,
filters/renderers, URL rules/presets, formatter/editorconfig, minify, class merger.
Editor: VS Code binary resolution, generic LSP command/root markers, actual capabilities,
organize-import ambiguity and packages outside go.mod.
Extensions: declarative-vs-code boundary, callable option signatures, fallback behavior.
Status/performance: current roadmap alignment and reproducible benchmark metadata.
Pattern: combined render-once helper compiles and demonstrates per-request dedup.
```

Expected: a written probe/evidence table and categorized findings, or explicit no-findings after every probe. Fix confirmed findings in their owning page, rerun affected checks plus Steps 1–4, and commit one focused review-fix commit only when needed.

- [ ] **Step 6: Confirm final state**

Run:

```bash
git status --short
git log --oneline 17085b4..HEAD
git -C /Users/jackieli/personal/gsxhq/gsxhq.github.io status --short
```

Expected: both checkouts are clean and the branch contains the planned Phase 2 documentation commits.
