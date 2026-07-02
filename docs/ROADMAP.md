# gsx Roadmap & Status

Living high-level status. Update as subsystems land. Detailed design lives in
`docs/superpowers/specs/`, plans in `docs/superpowers/plans/`.

Module: `github.com/gsxhq/gsx` · runtime is **standard-library only**; the
generator/CLI may use `golang.org/x/tools`.

**Status key:** [x] done · [~] partial / in progress · [ ] not started.

## Pipeline at a glance

`.gsx` → **parser** → **AST** → **codegen** (`go/packages` resolution) → `.x.go` → `go build` → renders HTML via the **runtime**.

| Stage | Status |
|---|---|
| Parser + AST | [x] Part 2 grammar + pipeline parsing + positioned, recoverable errors |
| Runtime (`gsx`) | [x] done |
| Codegen | [~] interpolation + control flow + full attributes (security core, composable class **and element-level style**, spread, conditional, explicit JS/CSS attr literals `` js`...` `` / `` css`...` `` + URL attr classification) + pipeline `\|>` + child props/`{children}` + method components + named slots + attribute fallthrough (auto class-merge/spread + manual `{...attrs}`) + node-prop promotion (`gsx.Val`/`Text`/`Fragment`) + ordered attrs (`{{ }}` lowering to `gsx.Attrs`) + uniform `(T,error)` auto-unwrap (all expression positions) + value-form `if`/`switch` in `class`/`style` (exclusive selection) done; composable `style` **on a component invocation** + `[]string` class parts pending |
| Whitespace model | [x] JSX-style: `internal/wsnorm.Normalize` (parser lossless) wired into codegen + powers `gsx fmt`. render-faithful + idempotent over the whole corpus. |
| Pipeline `\|>` end-to-end | [x] seed-first forward-application lowering + `std` filters + user filter packages (`gen.WithFilters` + `gen.WithFilter` aliases, multi-pkg last-wins) + `ctx` injection + `(T,error)` implicit auto-unwrap. Works in interp / attr / class / style / spread / child-prop values / `{{ }}` pairs (all expression positions). Initialism-aware naming pending. |
| CLI (`gsx`) / `gen.Main` | [~] `generate` (incl. `--watch`/`--format=ndjson`) · `fmt` · `info` · `init` · `lsp` · `clean --cache` · `version` · `help` ship, with `--json` + structured diagnostics. `vet`/`render`/`explain`/numeric codes pending. `WithClassMerger` + `class_merger` TOML knob shipped. |
| Language server (`gsx lsp`) | [~] diagnostics (debounced) + go-to-definition (incl. inside pipelines) + hover (incl. pipelines) + find-references + formatting ship; completion and external/non-project references deferred; references cover project components discovered during module analysis. |
| Developer experience (Vite + `init`) | [x] `gsx init` scaffold + `@gsxhq/vite-plugin-gsx` (npm v0.4.5) + `github.com/gsxhq/vite` (v0.2.0). |

## Done

**Parser / grammar** (`parser/`, `ast/`) — elements, fragments, text, interpolation
(`{ expr }`), attributes (static / expr / bool / spread / markup), control flow
(`{ if/for/switch }`), `{{ }}` Go blocks, conditional attributes, composable
`class`/`style`, comments, `<!DOCTYPE>`, `<!-- -->` HTML comments, raw-text
`<script>`/`<style>`, **pipeline `|>` parsing** (`Interp.Stages` / `ExprAttr`
stages), **`@{ }` interpolation inside embedded JavaScript/CSS**. Public
go/ast-parity API;
fuzz-hardened (no crashers). Parser errors carry structured `token.Pos` and
recover at the `component` boundary (one diagnostic per broken component).

**Runtime** (`gsx`, module root) — `Node`/`Func`/`Raw`, error-threading `Writer`
with streaming text/attr/URL/JS/CSS escapers, class/style compose + gen-configured
class merger (`class_merger` / `gen.WithClassMerger`), ordered `Attrs` bag
(`[]gsx.Attr`) + deterministic `Spread` in slice order. `gsx.AttrMap.ToAttrs`
keeps map-shaped construction explicit and sorts keys before converting to
`Attrs`. `gsx.Val(any)` / `gsx.Text(string)` / `gsx.Fragment(nodes…)`
value-Node boxes. `gsx.Raw` / `gsx.RawJS` / `gsx.RawCSS` / `gsx.RawURL` typed
escape hatches. Independent-review SHIP.

**Codegen phase 1** (`internal/codegen`) — `GeneratePackage(dir)`: `go/packages`
+ `Overlay` skeleton type resolution (cross-file, cross-component); arity-safe
`_gsxuse` probe; components+params → props + used-param local-binding; full §5
type-aware interpolation (string / []byte / numeric / bool / `gsx.Node` /
`[]gsx.Node` / `fmt.Stringer`; `gsx.Raw` via Node); **`(T,error)` auto-unwrap
(implicit, no `?` marker)**; child components; GoChunk import hoisting;
`//line` maps; identifier hygiene + pointer-`Render` + overlay-collision hardening.

**Node-prop promotion** — a non-Node value (or fragment) passed where a
`gsx.Node` prop is expected is boxed automatically via `gsx.Val` / `gsx.Text` /
`gsx.Fragment`, so authors write `<Card title={x}/>` with `x` of any renderable
type. Emit ≡ probe.

## Codegen phase 2 — feature phases

Each is a spec/plan → SDD slice that graduates more of the example corpus to
render goldens.

1. [x] **Guard pipeline silent-drop** — codegen errors on a non-empty
   `Interp.Stages` that fails to lower.
2. [x] **Control flow** — `{ if/for/switch }`, `{{ }}`, fragments → plain Go
   around writes (probe mirrors structure so loop-var/block-local interps resolve).
3. [x] **Attributes — security core + composable kinds.** Static (always-quoted,
   codegen-escaped), bool, and expr attrs with **structural context-aware escaping**
   (URL → scheme-allow-list + entity-escape `gw.URL`; plain → §5 type-aware
   `gw.AttrValue`; CSS `style`/`<style>` → auto value-filter `gw.CSS`/`gw.Style`
   with a `gsx.RawCSS` opt-out; explicit attribute-local JavaScript/CSS literals
   (`` js`...` `` / `` css`...` ``) with escaped `@{ }` holes and escaped literal
   delimiters — see Security). Plus
   composable **`class`** (`gw.Class`), composable **`style`** on elements
   (`gw.Style`/`gsx.StyleString`), **element spread** `{...attrs}` (`gw.Spread`),
   and **conditional** `{ if cond { attr } else { attr } }`. Pipelines `|>` work
   in every interpolation/attr/class/style/spread context. **Deferred:** `[]string`
   class parts; non-string-value-in-URL-attr clean compile error.
4. [x] **Pipeline `|>` + filters.** Seed-first forward-application: `subject |> name(args…)`
   → `Name([ctx,] (subject)[, args…])`, resolved against the shipped `std` package
   (and user packages) via `go/types` harvest-by-contract; the lowered expr is both
   the type-probe and the emitted render, so the result flows through the existing
   type-aware render / context escaper. `ctx` is injected when a filter's first
   param is `context.Context`; `(T,error)` rides the implicit auto-unwrap.
   `std` ships `default/format/join/lower/trim/truncate/upper`.
   - **Removed (not deferred):** the per-stage `?` try-marker — `(T,error)`
     auto-unwrap is implicit everywhere, so `?` is now a parse error.
   - **Deferred:** initialism-aware filter naming; pipeline-as-filter-argument and
     ambient `mapEach` (both unbuilt language features, out of scope).
5. [x] **Child-component props + `{children}`** — attr→field mapping
   (`<Card title={x} featured/>` → `Card(CardProps{Title: x, Featured: true})`);
   `{children}` slot (synthesized `Children gsx.Node` field + `gsx.Func` closure;
   nil-safe).
   - [x] **Named slots** — `<Panel header={ <h1/> }/>` (markup attr) → a
     `gsx.Func` closure assigned to the declared `gsx.Node` prop, placed via `{header}`.
6. [x] **Method components** — `component (p T) Name(params) { … }` → method
   `func (p T) Name(...)`; invocation `<p.Content/>` (left ident == enclosing
   receiver var) → method call; other dotted tags stay package calls. Also fixed
   `ctx`-in-interpolation. Generic function components and generic method-component
   syntax lower to Go-shaped generic declarations; method-owned type parameters
   require a Go toolchain with generic method support. **Deferred:** `<v.Method/>`
   for a non-receiver local; generic receivers `(p T[X])`.
7. [x] **Attribute fallthrough** — undeclared invocation attrs split (declared
   props matched against an AST-derived prop-name map vs everything else → an
   `Attrs gsx.Attrs` bag). **Auto** single-root: the bag's `class` merges into the
   root's class and the rest spreads at the root, root-wins. **Manual** `{...attrs}`:
   a body referencing `attrs` takes over placement. Covers composable `class={…}`,
   `{...spread}`, conditional `{ if }`, and pipelined values on a `<Card …>`
   invocation, plus whole-struct splat `{ data... }`.
   - **Deferred:** composable `style={…}` on a *component* invocation (works on an
     element, or set a static `style="…"`); cross-package undeclared-identifier split
     (best-effort); a pretty ambiguity diagnostic (today the raw Go unknown-field error).
8. [x] **Bare nullary func components** — any same-package tag whose backing func
   is nullary-by-construction is invokable as a bare `<F/>`, like a self-contained
   void element, with no `FProps` ceremony: a hand-written `func F() gsx.Node` (not a
   `.gsx` component — the escape hatch for no-render writer-control nodes; a `gsx.Func`
   gets the underlying `io.Writer`, so it can flush), **and** a `.gsx` no-props
   component (`component F() { … }`, emitted as a bare `func F() gsx.Node`). Both flow
   through one path: codegen probes the tag's real signature (`_gsxcompsig`, harvested
   into `resolved[el]`) and branches on arity — 0 params → `gw.Node(ctx, F())`; ≥1 →
   the `FProps{…}` convention. Passing attributes or children to a zero-arg component
   is a clean diagnostic (was a raw `undefined: FProps`). **Deferred:** non-`gsx.Node`
   renderable returns; cross-package nullary funcs.
9. [x] **Ordered attributes** (`{{ }}` lowering to `gsx.Attrs`) — `2026-06-29`.
   A `{{ "key": goExpr, … }}` literal in attribute-value position binds to a
   declared `gsx.Attrs` component prop; the bag is spread onto an element with
   `{ prop... }` via `Writer.Spread`, which emits pairs in **slice order**.
   Keys must be quoted string literals (enables kebab/colon names); values
   are arbitrary Go expressions (`|>` pipelines not supported inside the literal);
   `bool` values toggle bare/omitted. Duplicate keys and trailing commas are allowed;
   an empty `{{ }}` renders nothing. Using `{{ }}` directly on a plain-element
   attribute is a clean diagnostic. The bag does not participate in class/style
   merging. Escaping and unsafe-name validation mirror `Spread` exactly.
10. [x] **Uniform `(T, error)` auto-unwrap** — `2026-06-29`. The implicit
    two-value unwrap (first value used; second `error` → returned from `Render` on
    non-nil) now applies in **every expression position**: child-component prop values
    (`<Card title={lookup(t)}/>`) and `{{ }}` ordered-attrs pair values
    (`{{ "data-signals": signals(s) }}`), in addition to the already-covered text
    interpolation, element attribute values, `<style>`/`<script>` bodies,
    JS-attribute holes, children/slots, and pipeline stages. Any non-`(T,error)`
    multi-value shape is a clean gsx diagnostic (`only (T, error) is supported`).
    Multiple hoisted values in one call evaluate in source order. A shared
    `hoistTuple` helper replaces five copy-pasted hoist patterns.
11. [x] **Value-form `if`/`switch` in `class`/`style`** — `2026-06-30`. A
    **value-producing** form of `if` and `switch` usable inside `class={…}` /
    `style={…}` contribution lists, providing **exclusive selection** in place of
    the additive-map negation default. Switch values are unbraced
    (`case Green: "cls"`), matching markup-switch case bodies;
    multi-value cases (`case A, B:`) and `else if` chains are supported; a tagless
    `switch { case cond: … }` follows Go. Lowers to an alloc-free hoisted temp
    (`var _gsxvN string` assigned by a generated Go `switch`/`if`), not an IIFE.
    `if` without `else` is exactly equivalent to the additive guard form — no
    match, no `default`/`else` → empty contribution (nothing added). `(T,error)`
    auto-unwrap applies to both plain parts (`class={ cls(v) }`) and individual
    arms (`case A: cls(v)`), extending the shared `hoistTuple` machinery from
    item 10. A guard on a value-form part (`switch x {…}: cond`) is a compile-time
    diagnostic. Corpus coverage: `class/value_switch`, `class/value_if_*`,
    `class/value_switch_tuple`, `class/value_arm_pipeline`, `style/value_switch`,
    `class/part_tuple`, and rejection cases.
12. [ ] **Ordered style property bags (deferred)** — consider
    `style={{ "color": color, "font-size": size }}` only if real-world GSX
    projects repeatedly construct many dynamic declarations and declaration
    string composition becomes a material usability problem. The feature would
    add a second inline-style model plus parser, formatter, codegen, and
    documentation surface, so current usage does not justify it. If adopted,
    prefer quoted native CSS property names; do not add JSX camelCase conversion
    or automatic numeric units.

## Language server (`gsx lsp`)

In-process LSP over JSON-RPC on stdio (`internal/lsp`, wired at `gen/main.go`
`case "lsp"`). The analysis bridge runs the codegen pipeline (parse → type-check
→ harvest) **without writing `.x.go` to disk**.

- [x] **Diagnostics** (`textDocument/publishDiagnostics`) — positioned parse +
  type errors (Start/End, severity, code, help) from the shared `internal/diag`
  bag; re-analyses on every change; semantic multi-error + component-boundary recovery.
- [x] **Go-to-definition** (`textDocument/definition`) — four cases: `.gsx`
  Go-expr → `.go` def (D1/D3); `<Card/>` tag → `component` decl in `.gsx` (D2);
  `.go` component ref → `.gsx` declaration (D1.go). Uses the skeleton `go/types`
  analysis + cross-index + NavIndex.
- [x] **Hover** (`textDocument/hover`) — gopls-style type/signature for an
  identifier or expression; component-tag hover shows the component signature
  (answered from the AST even when type-checking fails mid-edit).
- [x] **Find-references** (`textDocument/references`) — `.go` call sites + `.gsx`
  tag sites for project components discovered during module analysis; external/non-project packages are skipped.
- [x] **Formatting** (`textDocument/formatting`) — canonical form with
  unused-import removal (reuses `gen.Format` / `gsxfmt.FormatRemovingImports`).
- [x] **Pipeline-aware definition + hover** (`internal/lsp/pipe.go`) — go-to-def
  and hover resolve a piped expression's seed, filter, and filter args
  (`pipedTarget` walks `Interp.Stages`/`ExprAttr` stages and maps the cursor offset
  to the right span); the `|>` operator itself returns null. Covers interp / attr /
  class / spread pipes.
- [x] **Debounced diagnostics** (`internal/lsp/server.go`) — a per-directory
  timer (250 ms) coalesces edit bursts; analysis runs off the read loop and
  version-tags its publishes. `didOpen` publishes promptly (no debounce).
- **Deferred:** completion and external/non-project references; references cover
  project components discovered during module analysis. Dotted/cross-package
  component tags (`<ui.Button/>`) are deferred.

Specs: `2026-06-23-gsx-lsp-design.md`, `2026-06-24-gsx-lsp-slice2a-goto-definition-design.md`,
`2026-06-24-gsx-lsp-go-to-gsx-design.md`, `2026-06-24-gsx-lsp-hover-design.md`.

## Developer experience — Vite + `init`

A complete, ready-to-run dev loop across three coordinated, independently-versioned
pieces. Save → warm generate → build-then-swap Go server → browser reloads.

- [x] **`gsx init` scaffold** (`gen/init.go`, `gen/templates/init/simple/`) —
  scaffolds a `net/http.ServeMux` Go server (graceful shutdown for development
  swaps), a `.gsx` component, a Vite config (front-door proxy +
  `@gsxhq/vite-plugin-gsx` + `devFallback`), embedded `public/*.svg`, and `.env`
  ports. Its `npm run dev` script invokes `go tool gsx dev`. Interactive
  (TTY prompts → runs `go mod tidy` / `npm install`) or non-interactive (`--yes`).
  Flags accepted in any position. Dev serves CSS via Vite JS with a **FOUC loading
  gate** so the first paint isn't unstyled.
- [x] **`gsx dev`** — owns the warm generator, build-then-swap Go server,
  Vite child process, browser diagnostics/reload, `.env` restarts, and clean
  process-tree teardown. Build artifacts and optional default logs live in a
  per-project OS cache directory, leaving the working tree clean.
- [x] **`gsx generate --watch`** (warm daemon, `gen/watch.go`) — a long-lived
  process that keeps the type-resolution environment warm (`gen.CachedResolver`)
  and regenerates in-process on each change, streaming NDJSON diagnostics. Measured:
  a warm regenerate is **~1–2 ms** vs **~140 ms** for a cold one-shot `gsx generate`
  (~70–100×). Rebuilds the resolver
  on `.go`/go.mod changes; pure `.gsx` edits take the fast path. Slice 2 (fine-grained
  per-package invalidation) is deferred — the measured warm time made it unnecessary.
- [x] **`@gsxhq/vite-plugin-gsx`** (npm **v0.4.5**, `~/personal/gsxhq/vite-plugin-gsx`) —
  receives generation/build events from `gsx dev`, surfaces diagnostics in the
  Vite error overlay (auto-clears on recovery), and full-reloads after the server
  becomes ready; `devFallback()` serves a self-recovering interstitial while the
  backend is down/restarting. Its standalone opt-in watch mode still supervises
  `gsx generate --watch`.
- [x] **`github.com/gsxhq/vite`** (Go, **v0.2.0**, `~/personal/gsxhq/vite`,
  stdlib-only) — manifest resolution (dev URL vs embedded prod manifest, transitive
  CSS dedup), `Entry(name) Bundle`, `StaticHandler()`, `NotifyReload(devURL)`, and
  context helpers (`NewContext`/`FromContext`/`Middleware`) for request-scoped
  instance threading.

## Security — safe by default

Threat model (the line every major engine draws): **template authors are
trusted; interpolated data is not.** Output encoding is gsx's job; input
validation is the app's job. Because gsx compiles to Go through `go/types`, the
ambition is to turn html/template's *runtime* safety into *compile-time* safety.
Research synthesis (templ / html/template / safehtml / JSX / Jinja2) in the
security design doc.

**Shipped reality — encoding is automatic by context:**

- **HTML / attr / URL** — auto-escaped by structural context (`gw.Text` /
  `gw.AttrValue` / `gw.URL`); URL scheme allow-list (http/https/mailto/tel →
  `about:invalid#gsx` sentinel); always-quoted attribute values.
- **JS / JSON** — `@{ x }` in `<script>` bodies, `@{ x }` holes inside explicit
  attribute-local `` js`...` `` literals, plus the
  `<script type="application/json">@{ data }</script>` data island, **JSON-encode
  via `gw.JSVal` / JS attribute-literal escaping** (HTML-safe: `< > &`,
  U+2028/U+2029; numeric token-fusion padding). `gsx.RawJS` opts out inside
  holes. Quoted attributes are literal strings; `attr={expr}` is ordinary
  attribute escaping unless the attr is URL-context by name.
- **CSS** — `<style>` bodies + composable `style={...}` values + `@{ x }` holes
  inside explicit attribute-local `` css`...` `` literals, including
  `` css`...` `` contributions inside `style={...}`, route untrusted values through
  `gw.CSS` / `gw.Style` / `FilterCSS` (faithful port of html/template's
  `cssValueFilter`); numbers are raw; `gsx.RawCSS` opts out. Static `<style>` CSS
  is minified at codegen time (`internal/cssmin`, hole-aware).

**JSON and CSS are automatic, never `|> json`/`|> css` filters.** The opt-outs
that ship are **typed constructors** (`gsx.Raw`, `gsx.RawJS`, `gsx.RawCSS`,
`gsx.RawURL`) — there are no `|> raw`/`|> js`/`|> css` filters. (`std` ships only
`default/format/join/lower/trim/truncate/upper`.) A future pipeline-based escape-hatch
vocabulary remains a design aspiration, not the current API.

**Prioritized work:**

1. [x] **Context dispatch in codegen** — ordinary attributes dispatch to
   `AttrValue` or `URL` from the parsed attribute name plus URL classifier;
   JavaScript/CSS attribute contexts are explicit with `` js`...` `` /
   `` css`...` `` literals, not inferred from event/style-like names. (A full
   Text/RCDATA/comment-position state machine across all markup positions is
   broader future work.)
2. [x] **Always-quote emitted attribute values** — kills the Jinja `xmlattr` /
   unquoted-attribute injection class (CVE-2024-22195).
3. [x] **CSS auto-sanitizes; JS contexts safely JSON-encode** — `<style>`/`style={…}`
   and `` css`...` `` holes route untrusted values through `FilterCSS`
   (adversarial-reviewed + fuzzed, 44.7M inputs, no breakout-byte leak);
   `<script>` and `` js`...` `` holes JSON-encode (Slices C1–C3). CSS
   minification on by default.
4. [~] **Harden `urlSanitize` + complete URL-attr table** — control-char /
   whitespace scheme evasion maps to the sentinel (adversarial-probed); the
   `urlAttrs` table covers `href`/`src`/`action`/`formaction`/`poster`/`cite`/`ping`/
   `data`/`background`/`manifest`/`xlink:href`/`hx-*`. **Remaining:** `meta
   http-equiv=refresh` content (CVE-2026-27142) and `base href` carriers; a
   dedicated fuzz target seeded from the OWASP filter-evasion sheet.
5. [ ] **Split navigational vs resource URLs** in the type/filter vocabulary
   (`URL` vs `TrustedResourceURL`, à la safehtml; html/template conflates them —
   go#27926).
6. [x] **One obvious data→`<script>` path** — `<script type="application/json">@{ data }</script>`
   islands + `<script>` / `` js`...` `` holes auto JSON-encode via `JSVal`;
   `gsx.RawJS` opts out. No `|> json` filter. See
   `2026-06-23-gsx-js-interpolation-design.md` and `datajson/`.
7. [ ] **CSP nonce threading** for emitted `<script>`/`<style>` — thread a
   per-request nonce; do not build a CSP engine (header is the server's job).

## Tracked debts / deferrals

- [x] **Pipeline codegen + filters/`std`/`gen`** — SHIPPED (seed-first
  forward-application, `ctx` injection, `(T,error)` auto-unwrap, `gen.WithFilters` +
  `gen.WithFilter` aliases, multi-pkg last-wins). Spec
  `2026-06-25-pipeline-forward-application-design.md`.
- [ ] **Pipeline extensions** — initialism-aware filter naming;
  pipeline-as-filter-argument; ambient `mapEach` (deferred / out of scope).
- [x] **LSP reads `gsx.toml` in-process** — `gsx lsp` resolves config the same
  way `generate`/`info` do (`mergeConfig(gsx.toml, opts)`) but in-process and
  best-effort (no subprocess, the LSP spawns nothing → no orphan children), so
  `gd`/hover/diagnostics on declarative project filters (`[filters] url = …`,
  `filterPackages`, URL attr rules) work in the editor with no Neovim change. A
  malformed `gsx.toml` falls back to the std baseline; opts are layered over the
  file (opts win). Spec/plan `2026-06-25-gsx-lsp-reads-config-design.md` /
  `2026-06-26-gsx-lsp-reads-config.md`.
- [ ] **`[gsx] command` + generate/info/lsp delegation** — a `gsx.toml`
  `[gsx] command = ["./bin/gsx"]` declaring the project's gsx, so the stock binary
  can `syscall.Exec` into it (single process, full fidelity incl. code-only
  options) for any command. Deferred: reintroduces process-ownership questions
  (the `go run` orphan hazard, build-failure fallback) the in-process LSP design
  avoids, and is unnecessary for declarative filters. Spec
  `2026-06-25-gsx-lsp-reads-config-design.md` §7.
- [x] **Example 02 `//`-in-markup grammar** — decided: element content is
  literal text, so a bare `//` in content renders verbatim; the braced
  `{/* … */}` form is the content-comment. Printer simplified; faithfulness +
  idempotence re-proven.
- [ ] **`_gsx`-alias generator-emitted imports** — robust form of the
  import-shadow guard (currently `gsx`/`strconv` are reserved param names as a stopgap).
- [x] **Structured diagnostics — Slice 1 (semantic layer)** — `internal/diag`
  (resolved `token.Position` Start/End, severity, code, message, help, source; `Bag`
  collector; rich/compact/JSON renderers). All `go/types` errors surfaced; codegen
  recovers at the component boundary; per-package write is all-or-nothing. Codegen +
  jsx diagnostics carry `.gsx` positions. `gsx generate` selects rich (TTY) / compact
  / `--json`; exit 1 on any error. Spec/plan `2026-06-23-diagnostics-foundation*`.
- [x] **Structured diagnostics — Slice 2 (parser layer)** — parser errors carry
  `token.Pos` and render `file:line:col: error[syntax]: …`; component-boundary
  recovery (one diagnostic per broken component, forward-progress guarantee);
  `ParseFileWithClassifier` returns `(*ast.File, []Error)`. **Deferred:**
  intra-component recovery; type-errors-alongside-parser-errors. Spec/plan
  `2026-06-24-parser-error-recovery*`.
- [~] **CLI / `gen.Main`** — SHIPPED: `gsx generate` / `fmt` / `info` / `init` /
  `lsp` / `clean --cache` / `version` / `help`; public `gen` package + `gen.Main`
  dispatch (`-C`/`-q`/`-v`, exit 0/1/2); `cmd/gsx` stock binary; `//go:generate gsx
  generate`. Extension seam: `WithFilters`/`WithFilter`, `WithCSSMinifier`/`WithJSMinifier`,
  `WithURLAttrs`, `WithFieldMatcher`.
  `gsx info --json` config manifest. `generate`/`init` accept flags in any position
  (`fmt`/`info` require flags first). `WithClassMerger` + `class_merger` TOML knob
  **SHIPPED** (configurable merger seam; Tailwind wrapper idiom; `--watch` validates at
  startup; cache-keyed; corpus + example coverage). **Pending:** GSXnnnn numeric
  codes (codes are string-based today, e.g. `invalid-syntax`); `vet`/`render`/`explain`;
  finer-grained incremental invalidation beyond the current warm watcher.
- [ ] **Codegen niceties** — [x] coalesce adjacent `gw.S` static writes;
  [ ] `//line` trailing-state reset; [ ] `data:image` URL allowance.
- [ ] **Tooling performance measurement on a realistic large corpus** — the
  existing baseline (`gen/perf_test.go`, `GSX_PERF=1`; note
  `2026-06-24-go-to-gsx-perf.md`) uses a *synthetic* 50-package fixture: ~383 ms/package
  `Analyze` (dominated by `go/packages.Load`), ~24.7 MiB/package retained. Plan:
  measure a realistic corpus (blog example, then a larger real project) to gauge
  `Analyze`/codegen latency, retained memory, GC pressure. Also measure `gsx fmt`
  default (module-loading, for unused-import removal) vs `-no-imports` at scale.
  Likely mitigations: LSP retained-package LRU cap; slim the `.gsx`-side full-`Info`.

## Documentation backlog

- [x] **Examples framework — SHIPPED.** `examples/*.txtar` fixtures (a `-- doc --`
  metadata block + `package views` `.gsx` files + `-- invoke --` + `-- render.golden --`)
  are the single source feeding render tests, per-topic syntax includes under
  `docs/guide/syntax/_generated/**`, and playground presets. A generator
  (`internal/examplegen` + `cmd/gsx-examples`, `make examples`) emits the generated
  snippets + byte-identical preset JSONs. The public site no longer has a separate
  Examples page; examples live beside the syntax they document and jump to the
  playground.
- [x] **Examples → Playground links — SHIPPED.** Each example emits an "Open in
  Playground" `#try=` deep-link (std-base64 of `{s:source,i:invoke}`); multi-file
  examples ride the Go-Playground txtar format (`-- file --` separators).
- [x] **Per-topic Syntax and usage pages — SHIPPED.** The guide now has 20 per-topic pages under `docs/guide/syntax/`, each with runnable examples sourced directly from golden-tested `examples/*.txtar` fixtures. `docs/guide/syntax.md` serves as a lightweight overview hub linking to all topic pages.
- [x] **Getting Started guide — SHIPPED.** Narrative onboarding using `gsx init`
  (scaffold → `npm run dev` / `go tool gsx dev` → first live-reload edit → error
  recovery → production build), including alternative package-manager setup.

## Design docs (reference)

- `2026-06-18-gsx-templating-design.md` — the language.
- `2026-06-18-gsx-codegen-walkthrough.md` — hand-written generated code / runtime model.
- `2026-06-19-gsx-runtime-design.md` — runtime package.
- `2026-06-19-gsx-codegen-design.md` — codegen architecture + lowering rules.
- `2026-06-19-gsx-pipeline-and-extensions-design.md` — `|>` + filters + `gen.Main`.
- `2026-06-25-pipeline-forward-application-design.md` — seed-first `|>` lowering + `ctx` injection.
- `2026-06-18-gsx-cli-skeleton-design.md` — CLI, exit codes, diagnostics model.
- `2026-06-20-gsx-security-design.md` — threat model, contextual auto-escaping, URL/JS/CSS contexts.
- `2026-06-23-gsx-js-interpolation-design.md` — `@{ }` JS-value contexts + data islands.
- `2026-06-23-diagnostics-foundation-design.md` — `internal/diag` model, renderers, recovery slices.
- `2026-06-24-parser-error-recovery-design.md` — positioned parser errors + component-boundary recovery.
- `2026-06-23-gsx-lsp-design.md` + `2026-06-24-gsx-lsp-slice2a-goto-definition-design.md` + `2026-06-24-gsx-lsp-go-to-gsx-design.md` + `2026-06-24-gsx-lsp-hover-design.md` — LSP.
- `2026-06-24-gsx-examples-framework-design.md` — single-source examples gallery.
