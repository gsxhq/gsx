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
| Pipeline `\|>` end-to-end | [x] seed-first forward-application lowering + `std` filters + user filter packages (`gen.WithFilters` + `gen.WithFilter` aliases, multi-pkg last-wins) + `ctx` injection + `(T,error)` implicit auto-unwrap **at any stage** (halts the chain on error). Works in interp / attr / class / style / spread / child-prop values / `{{ }}` pairs / cond-attr branches (all pipeline-legal contexts). Initialism-aware naming pending. |
| CLI (`gsx`) / `gen.Main` | [~] `generate` (incl. `--watch`/`--format=ndjson`) · `fmt` · `info` · `init` · `lsp` · `clean --cache` · `version` · `help` ship, with `--json` + structured diagnostics. `vet`/`render`/`explain`/numeric codes pending. `WithClassMerger` + `class_merger` TOML knob shipped. |
| Language server (`gsx lsp`) | [~] diagnostics (debounced) + go-to-definition (incl. inside pipelines) + hover (incl. pipelines) + find-references + formatting ship; completion and external/non-project references deferred; references cover project components discovered during module analysis. |
| Developer experience (Vite + `init`) | [x] `gsx init` scaffold + `@gsxhq/vite-plugin-gsx` (npm v0.4.5) + `github.com/gsxhq/vite` (v0.2.0). |

## Done

**Parser / grammar** (`parser/`, `ast/`) - elements, fragments, text, interpolation
(`{ expr }`), attributes (static / expr / bool / spread / markup), control flow
(`{ if/for/switch }`), `{{ }}` Go blocks, conditional attributes, composable
`class`/`style`, comments, `<!DOCTYPE>`, `<!-- -->` HTML comments, raw-text
`<script>`/`<style>`, **pipeline `|>` parsing** (`Interp.Stages` / `ExprAttr`
stages), **`@{ }` interpolation inside embedded JavaScript/CSS**. Public
go/ast-parity API;
fuzz-hardened (no crashers). Parser errors carry structured `token.Pos` and
recover at the `component` boundary (one diagnostic per broken component).

**Runtime** (`gsx`, module root) - `Node`/`Func`/`Raw`, error-threading `Writer`
with streaming text/attr/URL/JS/CSS escapers, class/style compose + gen-configured
class merger (`class_merger` / `gen.WithClassMerger`), ordered `Attrs` bag
(`[]gsx.Attr`) + deterministic `Spread` in slice order. `gsx.AttrMap.ToAttrs`
keeps map-shaped construction explicit and sorts keys before converting to
`Attrs`. `gsx.Val(any)` / `gsx.Text(string)` / `gsx.Fragment(nodes…)`
value-Node boxes. `gsx.Raw` / `gsx.RawJS` / `gsx.RawCSS` / `gsx.RawURL` typed
escape hatches. Independent-review SHIP.

**Codegen phase 1** (`internal/codegen`) - `GeneratePackage(dir)`: `go/packages`
+ `Overlay` skeleton type resolution (cross-file, cross-component); arity-safe
`_gsxuse` probe; components+params → props + used-param local-binding; full §5
type-aware interpolation (string / []byte / numeric / bool / `gsx.Node` /
`[]gsx.Node` / `fmt.Stringer`; `gsx.Raw` via Node); **`(T,error)` auto-unwrap
(implicit, no `?` marker)**; child components; GoChunk import hoisting;
`//line` maps; identifier hygiene + pointer-`Render` + overlay-collision hardening.

**Node-prop promotion** - a non-Node value (or fragment) passed where a
`gsx.Node` prop is expected is boxed automatically via `gsx.Val` / `gsx.Text` /
`gsx.Fragment`, so authors write `<Card title={x}/>` with `x` of any renderable
type. Emit ≡ probe.

## Codegen phase 2 - feature phases

Each is a spec/plan → SDD slice that graduates more of the example corpus to
render goldens.

1. [x] **Guard pipeline silent-drop** - codegen errors on a non-empty
   `Interp.Stages` that fails to lower.
2. [x] **Control flow** - `{ if/for/switch }`, `{{ }}`, fragments → plain Go
   around writes (probe mirrors structure so loop-var/block-local interps resolve).
3. [x] **Attributes - security core + composable kinds.** Static (always-quoted,
   codegen-escaped), bool, and expr attrs with **structural context-aware escaping**
   (URL → scheme-allow-list + entity-escape `gw.URL`; plain → §5 type-aware
   `gw.AttrValue`; CSS `style`/`<style>` → auto value-filter `gw.CSS`/`gw.Style`
   with a `gsx.RawCSS` opt-out; explicit attribute-local JavaScript/CSS literals
   (`` js`...` `` / `` css`...` ``) with escaped `@{ }` holes and escaped literal
   delimiters - see Security). Plus
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
   - **Removed (not deferred):** the per-stage `?` try-marker - `(T,error)`
     auto-unwrap is implicit everywhere, so `?` is now a parse error.
   - **Deferred:** initialism-aware filter naming; pipeline-as-filter-argument and
     ambient `mapEach` (both unbuilt language features, out of scope).
5. [x] **Child-component props + `{children}`** - attr→field mapping
   (`<Card title={x} featured/>` → `Card(CardProps{Title: x, Featured: true})`);
   `{children}` slot (synthesized `Children gsx.Node` field + `gsx.Func` closure;
   nil-safe).
   - [x] **Named slots** - `<Panel header={ <h1/> }/>` (markup attr) → a
     `gsx.Func` closure assigned to the declared `gsx.Node` prop, placed via `{header}`.
6. [x] **Method components** - `component (p T) Name(params) { … }` → method
   `func (p T) Name(...)`; invocation `<p.Content/>` (left ident == enclosing
   receiver var) → method call; other dotted tags stay package calls. Also fixed
   `ctx`-in-interpolation. Generic function components and generic method-component
   syntax lower to Go-shaped generic declarations; method-owned type parameters
   require a Go toolchain with generic method support. Tag type-argument
   inference is caller-side (per-site `_gsxinfer` probes checked by go/types):
   partial props, sibling files, imports, and method components all infer like
   an ordinary Go call; failures degrade to positioned diagnostics, never
   non-compiling output. **Deferred:** `<v.Method/>` for a non-receiver local;
   generic receivers `(p T[X])`; friendly hint for imported nullary-generic
   inference failures (raw-but-honest passthrough today).
7. [x] **Attribute fallthrough** - undeclared invocation attrs split (declared
   props matched against an AST-derived prop-name map vs everything else → an
   `Attrs gsx.Attrs` bag). **Explicit forwarding only** (`2026-06-30`; auto
   single-root removed): a body referencing `attrs` receives the bag and places
   it with `{ attrs... }` - pre-spread attrs caller-overridable, post-spread
   forced, `class`/`style` positional-exempt (always merge caller-last). Covers
   composable `class={…}`, `{...spread}`, conditional `{ if }`, and pipelined
   values on a `<Card …>` invocation, plus whole-struct splat `{ data... }`.
   `2026-07-02` hardening (spec `2026-07-02-attrs-forwarding-hardening-design`):
   derived-bag spreads (`{ attrs.Without("id")... }`, `{ attrs.Merge(x)... }`)
   get the full forwarding machinery via a hoisted once-evaluated temp;
   cond-attrs join caller-wins (pre-spread branch leaves guarded, post-spread
   branch selection recorded once + dynamic spread drop set); one forwarding
   spread per element (compose with `.Merge`, second spread is an error);
   `class`/`style`/spreads inside cond-attr branches on forwarding elements
   rejected with pointers to the composable forms. **Cross-package/imported
   components** (same module) get the same treatment - `2026-07-02`: per-file,
   import-alias-scoped prop discovery matches declared fields exactly like
   same-package calls, including the synthesized `Attrs gsx.Attrs` bag targeted
   via an ordered-attrs literal (`attrs={{ … }}`, canonical lowercase spelling,
   composing with other bag contributors via `.Merge` - merges last, duplicate
   literal is a clean error). A dependency gsx cannot analyze (other modules,
   plain Go packages, or a dep with a parse/type error) falls back to
   assumed-prop identifier matching with a visible `imported-props-unavailable`
   warning, replacing the prior best-effort/silent behavior.
   - **Deferred:** composable `style={…}` on a *component* invocation (works on an
     element, or set a static `style="…"`); a pretty ambiguity diagnostic (today the
     raw Go unknown-field error).
8. [x] **Bare nullary func components** - any same-package tag whose backing func
   is nullary-by-construction is invokable as a bare `<F/>`, like a self-contained
   void element, with no `FProps` ceremony: a hand-written `func F() gsx.Node` (not a
   `.gsx` component - the escape hatch for no-render writer-control nodes; a `gsx.Func`
   gets the underlying `io.Writer`, so it can flush), **and** a `.gsx` no-props
   component (`component F() { … }`, emitted as a bare `func F() gsx.Node`). Both flow
   through one path: codegen probes the tag's real signature (`_gsxcompsig`, harvested
   into `resolved[el]`) and branches on arity - 0 params → `gw.Node(ctx, F())`; ≥1 →
   the `FProps{…}` convention. Passing attributes or children to a zero-arg component
   is a clean diagnostic (was a raw `undefined: FProps`). **Deferred:** non-`gsx.Node`
   renderable returns; cross-package nullary funcs.
9. [x] **Ordered attributes** (`{{ }}` lowering to `gsx.Attrs`) - `2026-06-29`.
   A `{{ "key": goExpr, … }}` literal in attribute-value position binds to a
   declared `gsx.Attrs` component prop; the bag is spread onto an element with
   `{ prop... }` via `Writer.Spread`, which emits pairs in **slice order**.
   Keys must be quoted string literals (enables kebab/colon names); values
   are arbitrary Go expressions (`|>` pipelines not supported inside the literal);
   `bool` values toggle bare/omitted. Duplicate keys and trailing commas are allowed;
   an empty `{{ }}` renders nothing. Using `{{ }}` directly on a plain-element
   attribute is a clean diagnostic. The bag does not participate in class/style
   merging. Escaping and unsafe-name validation mirror `Spread` exactly.
10. [x] **Uniform `(T, error)` auto-unwrap** - `2026-06-29`. The implicit
    two-value unwrap (first value used; second `error` → returned from `Render` on
    non-nil) now applies in **every expression position**: child-component prop values
    (`<Card title={lookup(t)}/>`) and `{{ }}` ordered-attrs pair values
    (`{{ "data-signals": signals(s) }}`), in addition to the already-covered text
    interpolation, element attribute values, `<style>`/`<script>` bodies,
    JS-attribute holes, children/slots, and pipeline stages. Any non-`(T,error)`
    multi-value shape is a clean gsx diagnostic (`only (T, error) is supported`).
    Multiple hoisted values in one call evaluate in source order. A shared
    `hoistTuple` helper replaces five copy-pasted hoist patterns.
11. [x] **Value-form `if`/`switch` in `class`/`style`** - `2026-06-30`. A
    **value-producing** form of `if` and `switch` usable inside `class={…}` /
    `style={…}` contribution lists, providing **exclusive selection** in place of
    the additive-map negation default. Switch values are unbraced
    (`case Green: "cls"`), matching markup-switch case bodies;
    multi-value cases (`case A, B:`) and `else if` chains are supported; a tagless
    `switch { case cond: … }` follows Go. Lowers to an alloc-free hoisted temp
    (`var _gsxvN string` assigned by a generated Go `switch`/`if`), not an IIFE.
    `if` without `else` is exactly equivalent to the additive guard form - no
    match, no `default`/`else` → empty contribution (nothing added). `(T,error)`
    auto-unwrap applies to both plain parts (`class={ cls(v) }`) and individual
    arms (`case A: cls(v)`), extending the shared `hoistTuple` machinery from
    item 10. A guard on a value-form part (`switch x {…}: cond`) is a compile-time
    diagnostic. Corpus coverage: `class/value_switch`, `class/value_if_*`,
    `class/value_switch_tuple`, `class/value_arm_pipeline`, `style/value_switch`,
    `class/part_tuple`, and rejection cases.
12. [ ] **Ordered style property bags (deferred)** - consider
    `style={{ "color": color, "font-size": size }}` only if real-world GSX
    projects repeatedly construct many dynamic declarations and declaration
    string composition becomes a material usability problem. The feature would
    add a second inline-style model plus parser, formatter, codegen, and
    documentation surface, so current usage does not justify it. If adopted,
    prefer quoted native CSS property names; do not add JSX camelCase conversion
    or automatic numeric units.
13. [x] **Interpolating attribute-value literals** - `2026-07-05`. A plain
    backtick literal in attribute-value position, `` name=`…@{ expr }…` `` (no
    language prefix - family-consistent with `` js`...` ``/`` css`...` ``),
    interleaves static text with typed, auto-escaped `@{ }` holes in an
    ordinary attribute, closing the interpolation gap that previously forced
    `fmt.Sprintf`/string concatenation in Go for that case. Per-hole pipelines
    work (`` @{ v |> upper } ``); holes are escaped by Go type (string →
    attribute-escaped, int/uint/float → `strconv`-formatted, `fmt.Stringer` →
    `.String()`). `` \` `` escapes a literal backtick and `\@{` escapes a
    literal `@{`, mirroring `` js`/css` `` literal escaping (shared
    `writeEmbeddedLiteralText`/`unescapeEmbedded` machinery). **URL attributes**
    (`href`/`src`/`action`/…) assemble the whole literal - static text and every
    hole - into one Go string and sanitize it exactly once via the same
    allow-list `_gsxgw.URL` path `href={ expr }` uses, closing the
    split-scheme bypass class a per-hole classifier would have (a dangerous
    scheme divided across a hole boundary, e.g. `` href=`@{a}@{b}` `` with
    `a="javascript"`, `b=":alert(1)"`, is blocked to `about:invalid#gsx` the
    same as a single-expression `javascript:` scheme); `gsx.RawURL` is not
    usable inside a hole - write the value as `` href={ gsx.RawURL(x) } ``
    instead. **`class`/`style`** literals are first-class merge targets: a
    forwarded `{ attrs... }` bag's class/style merges caller-last into the
    interpolated value through the same `gsx.Class`/`StyleMerged` machinery as
    a composable or static `class`/`style`, producing one merged attribute, not
    two. `""` stays purely static (no `@{ }` scanning) and `{ expr }` stays a
    single expression - the literal is additive, not a replacement for either.
    The formatter round-trips the literal (idempotent, `\@{` re-escaped on
    print) and the LSP navigation matrix covers its holes. Corpus:
    `textattr/*` (11 cases - plain/pipeline holes, class/style merge and
    no-spread, six URL cases including the split-scheme bypass). Docs:
    `syntax/attributes.md` §Interpolating attribute literals,
    `syntax/escaping.md`, `syntax/interpolation.md`, `syntax/javascript.md`
    cross-reference; playground examples `examples/300-302-attr-interpolation*`.
    **Deferred follow-ups:** numeric-hole zero-alloc (`emitAttrValue`/
    `holeStringExpr` route `int`/`uint`/`float` through `strconv.AppendInt`-style
    `IntInto`/`UintInto`/`FloatInto` instead of an intermediate `string`, same
    idea as the existing `{ n }` fast path - would touch golden output, kept
    as a separate change); a `` css`...` ``/`` js`...` `` literal used as a
    `class`/`style` **merge target** does not yet get the same dup-merge
    treatment as a plain `EmbeddedText` literal (the finder that detects a
    merge-target literal does not currently bind to the js/css embedded-attr
    node - same class of gap, not yet closed); sibling grammars
    (`../tree-sitter-gsx`, `../vscode-gsx`, `gsxhq.github.io` CodeMirror) do not
    yet parse/highlight the bare backtick-with-`@{}` attribute form. A **hole-free**
    embedded literal now forwards to a component's `Attrs` bag as raw text
    (JSX-style directive forwarding, e.g. `` x-model=js`pdcaCategory` ``; corpus
    `components/embedded_attr_prop.txtar` + `_cond_prop.txtar`); an embedded
    literal carrying an `@{ }` **hole** as a component prop is still a clear error
    (`components/embedded_attr_rejected.txtar`) - forwarding a hole into a bag
    needs JS/CSS-context-correct per-hole escaping (exported string-returning
    escapers + a value assembler), a separate designed feature.

14. [x] **Body interpolation + whole-literal pipe** - `2026-07-05`. Two
    additions that carry the interpolating backtick literal into body/child
    position and add a value-assembling pipeline form. **(a) Body backtick
    literal:** a lone backtick literal inside body braces, `` {`…@{ expr }…`} ``,
    interpolates static text and typed `@{ }` holes per-segment (mirror of the
    attribute-value literal), lowering to the exact zero-alloc writes a
    hand-written mix of static text + `{ expr }` holes produces - NO materialized
    concat string, string holes HTML-text-escaped, numeric holes via the
    `IntInto`/`UintInto`/`FloatInto` fast path. The form applies **only** when the
    backtick is the lone child of the braces; `` {`a` + x} `` (or any larger Go
    expression) reverts the backtick to an ordinary Go raw string and the brace to
    a single `{ expr }` interpolation, so existing raw-string-in-braces code is
    untouched. `` \` `` / `\@{` escape as in the attribute literal; the formatter
    round-trips the form. **(b) Whole-literal pipe:** a backtick literal followed
    by `|>`, `` `…` |> f ``, assembles the interpolated string and pipes the whole
    value as a unit (vs. a per-hole `` `@{ v |> f }` ``). Available in body braces
    (`` {`…` |> f} ``) and the **braced** attribute form (`` attr={`…` |> f} ``);
    the pipe assembles via the same `embeddedValueExpr` path and lowers through the
    same `lowerPipe` the probe uses, so emit ≡ probe. **URL attributes
    sanitize AFTER the pipe:** the `_gsxgw.URL` scheme check runs on the pipe's
    output, so a filter that yields `javascript:` is still blocked to
    `about:invalid#gsx` (guarded by the `FuzzURLWholeLiteralPipeSchemeSafety` fuzz
    target). Corpus: `bodyinterp/*` (plain, whole_pipe, sub_expression,
    escaped_hole) + `textattr/whole_pipe_braced` + `textattr/whole_pipe_url_safe`.
    Docs: `syntax/interpolation.md` §Body backtick literals, `syntax/pipelines.md`
    §Whole-literal pipelines, `syntax/attributes.md` cross-reference; playground
    examples `examples/303-304`. LSP navigation covers the body-literal holes.
    **Deferred:** the whole-literal pipe on the **bare direct-attribute** literal
    (`` attr=`…` |> f `` without braces) - the direct form takes no trailing `|>`;
    wrap it in braces to pipe. Sibling grammars (`../tree-sitter-gsx`,
    `../vscode-gsx`, `gsxhq.github.io` CodeMirror) do not yet highlight the body
    backtick-with-`@{}` form.

15. [x] **Element literals** - `2026-07-07`. A `<tag>…</tag>` expression now
    works anywhere a Go expression is expected inside a `.gsx` file - a `var`
    initializer, a `return`, a call argument (the `RenderComponent(<Foo/>)`
    shape), a struct-literal field, a slice/map element - not just inside a
    `component` body. The parser resolves the classic JSX ambiguity (`<` at an
    operand-start boundary begins a tag; `<` in infix position stays `<`/`<-`/`<<`)
    via expression-start-position detection over the Go chunks; codegen reuses
    the existing component-body element-emission machinery
    (`gsx.Func(func(ctx, w) error {...})`) as an inline expression rather than a
    function body, and the `analyze.go` skeleton probe type-checks embedded
    elements (props, interpolated expressions) the same way it type-checks a
    component body. A `<tag>` in expression position is always an **Element** -
    a baked `gsx.Node`, the *result* of applying the tag, not the component
    itself - so render-site attrs never inject into it; this is the existing
    `<Card>` vs `Card` distinction, now visible outside component bodies too.
    Primary value: removes throwaway single-use `component` declarations -
    markup that exists only to be handed to a function or stored in a field can
    be written where the value is needed (nav-item icons, structpages
    `RenderComponent`/`RenderTarget` sites, playground snippets). Corpus:
    `element-literals/*` (var, call-arg, component-tag, return, struct-field,
    outer-scope interpolation capture, plus an apostrophe/prose-scanning
    regression and a formatter round-trip case). Docs: `syntax/elements.md`
    §Elements as values, `syntax/raw-go.md` cross-reference. **Deferred:**
    component values (`type Component = func(...gsx.Attr) gsx.Node` collapse) -
    parked; a baked element literal already covers the driving nav-icon use
    case since its class is constant there, and component values only earn
    their keep when render-site attrs must vary per call site (rare). Sibling
    grammars (`../tree-sitter-gsx`, `../vscode-gsx`, `gsxhq.github.io`
    CodeMirror) do not yet recognize `<tag>` in Go expression position - follow-up,
    out of scope for this repo. Spec `2026-07-06-element-literals-design.md`.

16. [x] **Fragments as values** - `2026-07-07`. Closes the "fragments deferred
    in expression position" boundary item 15 left open: `<>…</>` now works in
    every Go expression position an element literal does (`var`, `return`, a
    call argument, a struct field, a slice/map element), lowering to a
    `gsx.Node` with no wrapper element, through the same
    `gsx.Func(func(ctx, w) error {...})` closure path as an element literal -
    codegen's `emitNodeValue`/`emitFragmentValue` and `analyze.go`'s
    scope-capturing IIFE mirror the element-literal machinery, just keyed on
    the fragment's child list instead of a single tag. An empty fragment,
    `<></>`, is a **uniform** no-op closure (not a special-cased
    `gsx.Fragment()` call) - the render-nothing nop, the `templ.NopComponent`
    equivalent; the Go-side runtime form is `gsx.Fragment(nodes...)`. The
    driving use case is returning a *list* of sibling elements from a plain
    Go function (a fragment's children can be a `{ for … }` loop emitting
    many top-level siblings, which a single-tag element literal cannot).
    Multiple bare siblings still require explicit `<>…</>` wrapping;
    fragments take no attributes. Corpus: `fragment-literals/*` (var,
    call-arg, struct-field, return, empty-nop, loop-list, plus
    func-local/method-receiver scope-capture regressions mirroring the
    element-literals lesson). Docs: `syntax/elements.md` §Fragments as
    values, `syntax/raw-go.md` cross-reference. **Known limitation (shared
    with element literals):** a fragment/element literal nested directly
    inside a component's `{ … }` interpolation (e.g.
    `<div>{ wrap(<>…</>) }</div>`) is not supported - an interpolation's
    expression text is parsed as plain Go, with no embedded-element/fragment
    scan; use a top-level `var`/`return`/field position instead. Sibling
    grammars (`../tree-sitter-gsx`, `../vscode-gsx`, `gsxhq.github.io`
    CodeMirror) do not yet recognize `<>…</>` in Go expression position
    (fragments already highlight in markup/body context) - follow-up, out of
    scope for this repo.

## Language server (`gsx lsp`)

In-process LSP over JSON-RPC on stdio (`internal/lsp`, wired at `gen/main.go`
`case "lsp"`). The analysis bridge runs the codegen pipeline (parse → type-check
→ harvest) **without writing `.x.go` to disk**.

- [x] **Diagnostics** (`textDocument/publishDiagnostics`) - positioned parse +
  type errors (Start/End, severity, code, help) from the shared `internal/diag`
  bag; re-analyses on every change; semantic multi-error + component-boundary recovery.
- [x] **Go-to-definition** (`textDocument/definition`) - four cases: `.gsx`
  Go-expr → `.go` def (D1/D3); `<Card/>` tag → `component` decl in `.gsx` (D2);
  `.go` component ref → `.gsx` declaration (D1.go). Uses the skeleton `go/types`
  analysis + cross-index + NavIndex.
- [x] **Hover** (`textDocument/hover`) - gopls-style type/signature for an
  identifier or expression; component-tag hover shows the component signature
  (answered from the AST even when type-checking fails mid-edit).
- [x] **Find-references** (`textDocument/references`) - `.go` call sites + `.gsx`
  tag sites for project components discovered during module analysis; external/non-project packages are skipped.
- [x] **Formatting** (`textDocument/formatting`) - canonical form with
  unused-import removal (reuses `gen.Format` / `gsxfmt.FormatRemovingImports`).
- [x] **Pipeline-aware definition + hover** (`internal/lsp/pipe.go`) - go-to-def
  and hover resolve a piped expression's seed, filter, and filter args
  (`pipedTarget` walks `Interp.Stages`/`ExprAttr` stages and maps the cursor offset
  to the right span); the `|>` operator itself returns null. Covers interp / attr /
  class / spread pipes.
- [x] **Debounced diagnostics** (`internal/lsp/server.go`) - a per-directory
  timer (250 ms) coalesces edit bursts; analysis runs off the read loop and
  version-tags its publishes. `didOpen` publishes promptly (no debounce).
- [x] **Full expression-position coverage matrix** (2026-07-03) - go-to-def AND
  hover work for identifiers in EVERY Go-fragment position, via two bridges:
  the ExprMap byte-identical expr bridge (interps, expr attrs, spreads,
  ordered-attrs pair values, class plain parts, value-form arms) and the
  CtrlMap statement-clause bridge (for/if/`{{ }}` clauses, markup + value-form
  switch tags and case lists, in-tag conditional-attribute conds
  `{ if cond { … } }`, class guard conds `"on": cond`, value-form if conds).
  Parser records byte-faithful positions for each span (`parser/navpos_test.go`
  pins the invariant); `TestDefinitionMatrix`/`TestHoverObjectMatrix` pin the
  full matrix. Known limitations: the EXPR of a *guarded* class part
  (`expr: cond` - its cond navigates, the expr doesn't: no type harvest for
  guarded parts), pipeline-stage spans on spreads (parser records no stage
  positions there), paren-unwrapped spread pipelines (`{ (x |> f)... }` seed),
  and ctrl spans inside COMPONENT-tag attributes (`<Kid { if c { … } }/>` -
  the liveness walk that records ctrl offsets only runs for plain elements).
- **Deferred:** completion and external/non-project references; references cover
  project components discovered during module analysis. Dotted/cross-package
  component tags (`<ui.Button/>`) are deferred.

Specs: `2026-06-23-gsx-lsp-design.md`, `2026-06-24-gsx-lsp-slice2a-goto-definition-design.md`,
`2026-06-24-gsx-lsp-go-to-gsx-design.md`, `2026-06-24-gsx-lsp-hover-design.md`.

## Developer experience - Vite + `init`

A complete, ready-to-run dev loop across three coordinated, independently-versioned
pieces. Save → warm generate → build-then-swap Go server → browser reloads.

- [x] **`gsx init` scaffold** (`gen/init.go`, `gen/templates/init/simple/`) -
  scaffolds a `net/http.ServeMux` Go server (graceful shutdown for development
  swaps), a `.gsx` component, a Vite config (front-door proxy +
  `@gsxhq/vite-plugin-gsx` + `devFallback`), embedded `public/*.svg`, and `.env`
  ports. Its `npm run dev` script invokes `go tool gsx dev`. Interactive
  (TTY prompts → runs `go mod tidy` / `npm install`) or non-interactive (`--yes`).
  Flags accepted in any position. Dev serves CSS via Vite JS with a **FOUC loading
  gate** so the first paint isn't unstyled.
- [x] **`gsx dev`** - owns the warm generator, build-then-swap Go server,
  Vite child process, browser diagnostics/reload, `.env` restarts, and clean
  process-tree teardown. Build artifacts and optional default logs live in a
  per-project OS cache directory, leaving the working tree clean.
- [x] **`gsx generate --watch`** (warm daemon, `gen/watch.go`) - a long-lived
  process that keeps the type-resolution environment warm (`gen.CachedResolver`)
  and regenerates in-process on each change, streaming NDJSON diagnostics. Measured:
  a warm regenerate is **~1–2 ms** vs **~140 ms** for a cold one-shot `gsx generate`
  (~70–100×). Rebuilds the resolver
  on `.go`/go.mod changes; pure `.gsx` edits take the fast path. Slice 2 (fine-grained
  per-package invalidation) is deferred - the measured warm time made it unnecessary.
- [x] **`@gsxhq/vite-plugin-gsx`** (npm **v0.4.5**, `~/personal/gsxhq/vite-plugin-gsx`) -
  receives generation/build events from `gsx dev`, surfaces diagnostics in the
  Vite error overlay (auto-clears on recovery), and full-reloads after the server
  becomes ready; `devFallback()` serves a self-recovering interstitial while the
  backend is down/restarting. Its standalone opt-in watch mode still supervises
  `gsx generate --watch`.
- [x] **`github.com/gsxhq/vite`** (Go, **v0.2.0**, `~/personal/gsxhq/vite`,
  stdlib-only) - manifest resolution (dev URL vs embedded prod manifest, transitive
  CSS dedup), `Entry(name) Bundle`, `StaticHandler()`, `NotifyReload(devURL)`, and
  context helpers (`NewContext`/`FromContext`/`Middleware`) for request-scoped
  instance threading.

## Security - safe by default

Threat model (the line every major engine draws): **template authors are
trusted; interpolated data is not.** Output encoding is gsx's job; input
validation is the app's job. Because gsx compiles to Go through `go/types`, the
ambition is to turn html/template's *runtime* safety into *compile-time* safety.
Research synthesis (templ / html/template / safehtml / JSX / Jinja2) in the
security design doc.

**Shipped reality - encoding is automatic by context:**

- **HTML / attr / URL** - auto-escaped by structural context (`gw.Text` /
  `gw.AttrValue` / `gw.URL`); URL scheme allow-list (http/https/mailto/tel →
  `about:invalid#gsx` sentinel); always-quoted attribute values.
- **JS / JSON** - `@{ x }` in `<script>` bodies, `@{ x }` holes inside explicit
  attribute-local `` js`...` `` literals, plus the
  `<script type="application/json">@{ data }</script>` data island, **JSON-encode
  via `gw.JSVal` / JS attribute-literal escaping** (HTML-safe: `< > &`,
  U+2028/U+2029; numeric token-fusion padding). `gsx.RawJS` opts out inside
  holes. Quoted attributes are literal strings; `attr={expr}` is ordinary
  attribute escaping unless the attr is URL-context by name.
- **CSS** - `<style>` bodies + composable `style={...}` values + `@{ x }` holes
  inside explicit attribute-local `` css`...` `` literals, including
  `` css`...` `` contributions inside `style={...}`, route untrusted values through
  `gw.CSS` / `gw.Style` / `FilterCSS` (faithful port of html/template's
  `cssValueFilter`); numbers are raw; `gsx.RawCSS` opts out. Static `<style>` CSS
  is minified at codegen time (`internal/cssmin`, hole-aware).

**JSON and CSS are automatic, never `|> json`/`|> css` filters.** The opt-outs
that ship are **typed constructors** (`gsx.Raw`, `gsx.RawJS`, `gsx.RawCSS`,
`gsx.RawURL`) - there are no `|> raw`/`|> js`/`|> css` filters. (`std` ships only
`default/format/join/lower/trim/truncate/upper`.) A future pipeline-based escape-hatch
vocabulary remains a design aspiration, not the current API.

**Prioritized work:**

1. [x] **Context dispatch in codegen** - ordinary attributes dispatch to
   `AttrValue` or `URL` from the parsed attribute name plus URL classifier;
   JavaScript/CSS attribute contexts are explicit with `` js`...` `` /
   `` css`...` `` literals, not inferred from event/style-like names. (A full
   Text/RCDATA/comment-position state machine across all markup positions is
   broader future work.)
2. [x] **Always-quote emitted attribute values** - kills the Jinja `xmlattr` /
   unquoted-attribute injection class (CVE-2024-22195).
3. [x] **CSS auto-sanitizes; JS contexts safely JSON-encode** - `<style>`/`style={…}`
   and `` css`...` `` holes route untrusted values through `FilterCSS`
   (adversarial-reviewed + fuzzed, 44.7M inputs, no breakout-byte leak);
   `<script>` and `` js`...` `` holes JSON-encode (Slices C1–C3). CSS
   minification on by default.
4. [x] **Harden `urlSanitize` + complete URL-attr table** - control-char /
   whitespace scheme evasion maps to the sentinel (adversarial-probed); the
   `urlAttrs` table covers `href`/`src`/`action`/`formaction`/`poster`/`cite`/`ping`/
   `data`/`background`/`manifest`/`xlink:href`/`hx-*`; a statically-declared
   `<meta http-equiv="refresh" content={...}>` (static, constant-literal, or
   conditional-branch `http-equiv`) sanitizes its embedded redirect URL
   (WHATWG-grammar parser, differential-fuzzed via
   `FuzzRefreshContentSanitize` against an independent spec port, OWASP
   filter-evasion seeds); `<base href={...}>` is explicitly covered by the
   normal `href` URL path. **Residual (accepted):** a runtime-dynamic
   `http-equiv={expr}` keeps plain attribute escaping (pinned in corpus
   `security/meta_refresh_dynamic_http_equiv`), and `{...attrs}` bags follow
   the documented Spread contract (attribute-escaped, never URL-sanitized).
5. [ ] **Split navigational vs resource URLs** in the type/filter vocabulary
   (`URL` vs `TrustedResourceURL`, à la safehtml; html/template conflates them -
   go#27926).
6. [x] **One obvious data→`<script>` path** - `<script type="application/json">@{ data }</script>`
   islands + `<script>` / `` js`...` `` holes auto JSON-encode via `JSVal`;
   `gsx.RawJS` opts out. No `|> json` filter. See
   `2026-06-23-gsx-js-interpolation-design.md` and `datajson/`.
7. [x] **CSP nonce threading** for emitted `<script>`/`<style>` -
   `gsx.WithNonce(ctx, nonce)` stores the per-request nonce on the render
   context; generated code adds `nonce="…"` to every emitted `<script>`/
   `<style>` open tag (an author-written `nonce` attribute or a spread bag
   carrying a `"nonce"` key wins). No nonce generation, middleware, or CSP
   header engine - the header stays the server's job. See
   `2026-07-02-csp-nonce-injection-design.md`.

## Tracked debts / deferrals

- [x] **`` json`...` `` tagged literal** - decided (2026-07-02): declined in
  favor of blessing `` js`...` `` for JSON-valued attributes (htmx `hx-vals`
  et al.): holes already JSON-encode via the `html/template` port, so `js` output
  in value position *is* valid JSON, pinned by `jsattr/hxvals_json` and
  documented in `syntax/javascript.md`. Revisit only if compile-time JSON
  well-formedness validation (trailing commas, single quotes) or multi-hole
  JSON data islands become a real pain point; `{{ }}` interpolation was ruled
  out (collides with composite-literal `{{…}}` and the quoted-attrs-are-literal
  invariant).
- [x] **Pipeline codegen + filters/`std`/`gen`** - SHIPPED (seed-first
  forward-application, `ctx` injection, `(T,error)` auto-unwrap, `gen.WithFilters` +
  `gen.WithFilter` aliases, multi-pkg last-wins). Spec
  `2026-06-25-pipeline-forward-application-design.md`.
- [x] **`(R, error)` filters at any pipeline stage** - SHIPPED (2026-07-03). A
  filter returning `(R, error)` works at any stage, not just the final one: an
  error-returning non-final stage hoists to `_gsxvN, _gsxerr := stage(...); if
  _gsxerr != nil { return _gsxerr }`, the failing stage halts the chain (later
  filters never run), and the error returns from the component's render -
  identical to the existing `(T, error)` auto-unwrap. Works in every
  pipeline-legal context, including component cond-attr branches, which lower
  each side to a `func() (gsx.Attrs, error)` thunk (`gsx.AttrsCond`) with
  hoists inside the thunk body, preserving laziness - see the follow-up spec
  below for the uniform thunk lowering that replaced the original
  statement-form design. Spec `2026-07-03-pipe-error-any-stage-design.md`.
- [ ] **Pipeline extensions** - initialism-aware filter naming;
  pipeline-as-filter-argument; ambient `mapEach` (deferred / out of scope).
- [x] **Class parts inside component cond-attr branches now support
  `(R, error)`** - CLOSED (2026-07-03). `AttrsCond`'s thunks return
  `(Attrs, error)` - one uniform lowering, the statement form deleted - and
  branch positions (ExprAttr values, class parts, value-form CF arms) join
  the probe type-harvest, so a plain tuple-returning call, a mid-stage error
  pipe, and a final-stage error pipe inside a *component* conditional-attribute
  branch (`<Card { if hot { class={ … } } }/>`) all lower exactly like every
  other pipeline-legal position - no more raw `too many arguments in call to
  _gsxrt.Class` leak and no more generic "failing stage is not supported in
  this position" rejection. Spec `2026-07-03-attrscond-error-design.md`.
- [x] **LSP reads `gsx.toml` in-process** - `gsx lsp` resolves config the same
  way `generate`/`info` do (`mergeConfig(gsx.toml, opts)`) but in-process and
  best-effort (no subprocess, the LSP spawns nothing → no orphan children), so
  `gd`/hover/diagnostics on declarative project filters (`[filters] url = …`,
  `filterPackages`, URL attr rules) work in the editor with no Neovim change. A
  malformed `gsx.toml` falls back to the std baseline; opts are layered over the
  file (opts win). Spec/plan `2026-06-25-gsx-lsp-reads-config-design.md` /
  `2026-06-26-gsx-lsp-reads-config.md`.
- [ ] **`[gsx] command` + generate/info/lsp delegation** - a `gsx.toml`
  `[gsx] command = ["./bin/gsx"]` declaring the project's gsx, so the stock binary
  can `syscall.Exec` into it (single process, full fidelity incl. code-only
  options) for any command. Deferred: reintroduces process-ownership questions
  (the `go run` orphan hazard, build-failure fallback) the in-process LSP design
  avoids, and is unnecessary for declarative filters. Spec
  `2026-06-25-gsx-lsp-reads-config-design.md` §7.
- [x] **Example 02 `//`-in-markup grammar** - decided: element content is
  literal text, so a bare `//` in content renders verbatim; the braced
  `{/* … */}` form is the content-comment. Printer simplified; faithfulness +
  idempotence re-proven.
- [ ] **`_gsx`-alias generator-emitted imports** - robust form of the
  import-shadow guard (currently `gsx`/`strconv` are reserved param names as a stopgap).
- [x] **Structured diagnostics - Slice 1 (semantic layer)** - `internal/diag`
  (resolved `token.Position` Start/End, severity, code, message, help, source; `Bag`
  collector; rich/compact/JSON renderers). All `go/types` errors surfaced; codegen
  recovers at the component boundary; per-package write is all-or-nothing. Codegen +
  jsx diagnostics carry `.gsx` positions. `gsx generate` selects rich (TTY) / compact
  / `--json`; exit 1 on any error. Spec/plan `2026-06-23-diagnostics-foundation*`.
- [x] **Structured diagnostics - Slice 2 (parser layer)** - parser errors carry
  `token.Pos` and render `file:line:col: error[syntax]: …`; component-boundary
  recovery (one diagnostic per broken component, forward-progress guarantee);
  `ParseFileWithClassifier` returns `(*ast.File, []Error)`. **Deferred:**
  intra-component recovery; type-errors-alongside-parser-errors. Spec/plan
  `2026-06-24-parser-error-recovery*`.
- [~] **CLI / `gen.Main`** - SHIPPED: `gsx generate` / `fmt` / `info` / `init` /
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
- [ ] **Codegen niceties**
  - [x] coalesce adjacent `gw.S` static writes;
  - [ ] `//line` trailing-state reset;
  - [x] `data:image` resource-URL allowance - URL sinks split into two tiers
    (`internal/attrclass.URLSink`): image sinks (`<img src>`, `<source src>`,
    `<input src>`, `<video poster>`, `background`) allow `data:image/*`
    (raster + `svg+xml`, base64-marked) via the runtime `URLImage` sanitizer;
    strict sinks (`href`, `<script src>`, `<iframe src>`, `<object data>`, …)
    keep blocking all `data:`. Authoring: Form A static-prefix literal
    (`` src=`data:image/png;base64,@{bytes}` `` - `[]byte` auto-encodes,
    `string` passes through; `data:` on a strict sink is a compile error) +
    the `dataURL(mime)` std filter (assembly only, re-validated by the sink);
    `gsx.RawURL` is the vouching escape hatch. **Deferred:** `srcset`
    per-candidate parsing and CSS `background: url(data:…)`.
- [x] **`//go:` directive / build-constraint pass-through** - program-significant
  comment lines before the `package` clause (`//go:build`, `//go:generate`,
  `//go:debug`, legacy `// +build`) copy verbatim into the generated `.x.go`,
  between the generated-code marker and the package clause; prose stays
  `.gsx`-only and `//line` is excluded. Generation stays build-context-independent
  (every `.gsx` generates regardless of host `GOOS`); constraints take effect at
  `go build`. See `docs/guide/syntax.md` §Build constraints.
- [x] **Tag-aware `.gsx` analysis** - two `.gsx` files gated by disjoint
  `//go:build` tags may declare the same component when their signatures match:
  the cross-file `redeclared` type errors are suppressed so `Generate` emits all
  files (go build filters by tag and arbitrates real same-config duplicates),
  while a same-name/*different*-signature component collision is a clean
  `duplicate-component` error that blocks emission. gsx never parses build
  constraints. LSP go-to-definition / find-references are multi-valued over the
  variants. Non-component cross-file helper duplicates are tolerated (deferred to
  go build); within-file redeclarations stay hard errors. Spec
  `2026-07-06-tag-variant-component-analysis-design.md`.
- [ ] **Tooling performance measurement on a realistic large corpus** - the
  existing baseline (`gen/perf_test.go`, `GSX_PERF=1`; note
  `2026-06-24-go-to-gsx-perf.md`) uses a *synthetic* 50-package fixture: ~383 ms/package
  `Analyze` (dominated by `go/packages.Load`), ~24.7 MiB/package retained. Plan:
  measure a realistic corpus (blog example, then a larger real project) to gauge
  `Analyze`/codegen latency, retained memory, GC pressure. Likely mitigations:
  LSP retained-package LRU cap; slim the `.gsx`-side full-`Info`.
  **`gsx fmt` unused-import removal - done:** detection was rearchitected off
  full type-checking onto a syntactic skeleton scan (`internal/codegen/unused_imports_syntactic.go`,
  `Module.UnusedImports` - parses each file's lowered skeleton, classifies
  imports by referenced qualifier name, resolving only ambiguous default-import
  candidates via a cheap `go/packages` `NeedName`-only load; no importer, no
  dependency type-checking). Measured on a real 91-file/8-package project
  (`one-learning-gsx`): `fmt -l` over the whole tree dropped from ~16s to ~3s.
  Behavior change vs the old type-check-gated path: unused imports are now
  removed even when the package has an unrelated type error, matching
  gofmt/goimports parity. (The LSP's `textDocument/formatting` handler, noted
  above, is a separate path and still sources its unused-import list from the
  full type-checked analysis it already performs for diagnostics - untouched by
  this change.) **BYO external-struct field enumeration is now syntactic too:**
  `loadExternalStructFields` (`internal/codegen/byo.go`) dropped its
  per-directory `go/packages` type-load and reuses the same syntactic
  `fieldsFromGsxStruct` scan as the `.gsx`-side path, so it no longer invokes
  the Go toolchain per BYO directory. On the same `one-learning-gsx` project
  this was the last remaining bottleneck the note above called out; `fmt -l`
  over the whole tree now completes in well under 1s (down from ~3s), and
  `generate`'s cold path speeds up correspondingly. Output is byte-identical
  (corpus goldens + a real-world `one-learning-gsx generate` diff check).

## Documentation backlog

- [x] **Examples framework - SHIPPED.** `examples/*.txtar` fixtures (a `-- doc --`
  metadata block + `package views` `.gsx` files + `-- invoke --` + `-- render.golden --`)
  are the single source feeding render tests, per-topic syntax includes under `docs/guide/syntax/_generated/**`, and playground presets. A generator
  (`internal/examplegen` + `cmd/gsx-examples`, `make examples`) emits the generated
  snippets + byte-identical preset JSONs. The public site no longer has a separate
  Examples page; examples live beside the syntax they document and jump to the
  playground.
- [x] **Examples → Playground links - SHIPPED.** Each example emits an "Open in
  Playground" `#try=` deep-link (std-base64 of `{s:source,i:invoke}`); multi-file
  examples ride the Go-Playground txtar format (`-- file --` separators).
- [x] **Per-topic Syntax and usage pages - SHIPPED.** The guide now has 20 per-topic pages under `docs/guide/syntax/`, each with runnable examples sourced directly from golden-tested `examples/*.txtar` fixtures. `docs/guide/syntax.md` serves as a lightweight overview hub linking to all topic pages.
- [x] **Getting Started guide - SHIPPED.** Narrative onboarding using `gsx init`
  (scaffold → `npm run dev` / `go tool gsx dev` → first live-reload edit → error
  recovery → production build), including alternative package-manager setup.

## Design docs (reference)

- `2026-06-18-gsx-templating-design.md` - the language.
- `2026-06-18-gsx-codegen-walkthrough.md` - hand-written generated code / runtime model.
- `2026-06-19-gsx-runtime-design.md` - runtime package.
- `2026-06-19-gsx-codegen-design.md` - codegen architecture + lowering rules.
- `2026-06-19-gsx-pipeline-and-extensions-design.md` - `|>` + filters + `gen.Main`.
- `2026-06-25-pipeline-forward-application-design.md` - seed-first `|>` lowering + `ctx` injection.
- `2026-06-18-gsx-cli-skeleton-design.md` - CLI, exit codes, diagnostics model.
- `2026-06-20-gsx-security-design.md` - threat model, contextual auto-escaping, URL/JS/CSS contexts.
- `2026-06-23-gsx-js-interpolation-design.md` - `@{ }` JS-value contexts + data islands.
- `2026-06-23-diagnostics-foundation-design.md` - `internal/diag` model, renderers, recovery slices.
- `2026-06-24-parser-error-recovery-design.md` - positioned parser errors + component-boundary recovery.
- `2026-06-23-gsx-lsp-design.md` + `2026-06-24-gsx-lsp-slice2a-goto-definition-design.md` + `2026-06-24-gsx-lsp-go-to-gsx-design.md` + `2026-06-24-gsx-lsp-hover-design.md` - LSP.
- `2026-06-24-gsx-examples-framework-design.md` - single-source examples gallery.
