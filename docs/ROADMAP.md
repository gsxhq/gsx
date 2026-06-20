# gsx Roadmap & Status

Living high-level status. Update as subsystems land. Detailed design lives in
`docs/superpowers/specs/`, plans in `docs/superpowers/plans/`.

Module: `github.com/gsxhq/gsx` · runtime is **standard-library only**; the
generator/CLI may use `golang.org/x/tools`.

## Pipeline at a glance

`.gsx` → **parser** → **AST** → **codegen** (`go/packages` resolution) → `.x.go` → `go build` → renders HTML via the **runtime**.

| Stage | Status |
|---|---|
| Parser + AST | ✅ done (Part 2 grammar + pipeline parsing) |
| Runtime (`gsx`) | ✅ done |
| Codegen | 🟡 interpolation + control flow + attributes (security core) + pipeline `\|>` (first slice) done; methods/composable class+style/spread/extension-seam pending |
| CLI / `gen.Main` | ⬜ not started |
| Pipeline `|>` end-to-end | 🟡 lowering + `std` filters done (interp + attr, harvest-by-contract) — **extension seam (`gen.Main`/user filter pkgs) + per-stage `?` not done** |

## Done

**Parser / grammar** (`parser/`, `ast/`) — elements, fragments, text, interpolation
(`{ expr }`, `?` try), attributes (static / expr / bool / spread / markup),
control flow (`{ if/for/switch }`), `{{ }}` Go blocks, conditional attributes,
composable `class`/`style`, comments, `<!DOCTYPE>`, `<!-- -->`, raw-text
`<script>`/`<style>`, **pipeline `|>` parsing** (`Interp.Stages` / `ExprAttr`
stages). Public go/ast-parity API; fuzz-hardened (no crashers). 11/12 examples
parse (see Debts: example 02).

**Runtime** (`gsx`, module root) — `Node`/`Func`/`Raw`, error-threading `Writer`
with streaming text/attr/URL escapers, class/style compose + pluggable
`ClassMerger`, `Attrs` bag + deterministic `Spread`. Independent-review SHIP.

**Codegen phase 1** (`internal/codegen`) — `GeneratePackage(dir)`: `go/packages`
+ `Overlay` skeleton type resolution (cross-file, cross-component); arity-safe
`_gsxuse` probe; components+params → props + used-param local-binding; full §5
type-aware interpolation (string / []byte / numeric / bool / `gsx.Node` /
`[]gsx.Node` / `fmt.Stringer`; `gsx.Raw` via Node); `(T,error)` auto-unwrap;
child components (no props yet); GoChunk import hoisting; `//line` maps;
identifier hygiene + pointer-`Render` + overlay-collision hardening.
Tested by source golden + ~21 compile-and-render goldens.

## Codegen phase 2 — feature phases (next)

Each is a spec/plan → SDD slice that graduates more of the example corpus to
render goldens. Suggested order:

1. ✅ **Guard pipeline silent-drop** — codegen now errors on non-empty
   `Interp.Stages` (interpolation). *(ExprAttr stages will be guarded when
   attribute codegen lands — attributes aren't emitted yet.)*
2. ✅ **Control flow** — `{ if/for/switch }`, `{{ }}`, fragments → plain Go around
   writes (probe mirrors structure so loop-var/block-local interps resolve).
3. 🟡 **Attributes — security core + composable kinds done.** Static (always-quoted,
   codegen-escaped), bool, and expr attrs with **structural context-aware escaping**
   (URL → scheme-allow-list + entity-escape `gw.URL`; plain → §5 type-aware
   `gw.AttrValue`; JS `on*`/`@*`/`hx-on*` and CSS `style` → **fail-closed compile
   error**). Plus composable **`class`** (`gw.Class`), **element spread** `{...attrs}`
   (`gw.Spread`), and **conditional** `{ if cond { attr } else { attr } }` (a shared
   `walkAttrExprs` keeps the type-probe order invariant with branch-nested exprs).
   Two independent adversarial reviews: SHIP. **Deferred:** `style` composition
   (stays fail-closed pending `|> css`), `[]string` class parts, attr `?`/`|>`,
   non-string-value-in-URL-attr clean error.
4. 🟡 **Pipeline `|>` + filters — first slice done.** Lowering of `Stages` to
   nested qualified Go calls (`{x |> a |> b(n)}` → `_gsxstd.B(n)(_gsxstd.A((x)))`),
   resolved against the shipped `std` package via `go/types` harvest-by-contract;
   the lowered expr is both the type-probe and the emitted render, so the result
   flows through the existing type-aware render / context escaper (interp + attr).
   Independent review: SHIP (1 bug found+fixed — params used only in filter args).
   **Deferred:** the `gen.Main`/`cmd/gsx`/`WithFilters` extension seam + user filter
   packages + collision/precedence + `gsx info`/`vet`; per-stage `?` (failable
   filters); initialism-aware naming; pipeline-as-filter-argument; ambient `mapEach`.
5. ✅ **Child-component props + `{children}`** — attr→field mapping
   (`<Card title={x} featured/>` → `Card(CardProps{Title: x, Featured: true})`,
   shared `childPropsFields` for emit+probe); `{children}` slot (synthesized
   `Children gsx.Node` field + `gsx.Func` closure passed by the parent; slot renders
   in parent scope; nil-safe). Order invariant: component elements recurse children
   (slot), skip attrs (props). Independent review: SHIP. **Deferred:** named slots
   (markup attrs `header={<m/>}`), auto-fallthrough / `{...attrs}` / component
   spread (→ #7), class/cond/pipeline attrs on a component.
6. ⬜ **Method components** — `component (r T) X()` → method.
7. ⬜ **Auto-fallthrough attrs + diagnostics** — single-root fallthrough +
   compile-time ambiguity errors.

## Security — safe by default, escape hatch via pipeline

Threat model (the line every major engine draws): **template authors are
trusted; interpolated data is not.** Output encoding is gsx's job; input
validation is the app's job. Because gsx compiles to Go through `go/types`, the
ambition is to turn html/template's *runtime* safety into *compile-time* safety:
**unsafe contexts become build errors, not runtime surprises.** Research synthesis
(templ / html/template / safehtml / JSX / Jinja2) in the security design doc.

**Escape-hatch direction — pipeline, not function calls.** templ/html-template
spell the opt-out as a typed-string constructor (`templ.Raw(x)`,
`template.HTML(x)`) — easy to apply to tainted data and invisible in review. gsx
instead routes *all* escaping decisions through the `|>` pipeline, which is more
flexible and pluggable:

- Safe is the default: a bare `{ x }` is always context-escaped by codegen.
- The opt-out is a *filter*, not a cast: `{ x |> raw }`, `{ x |> js }`,
  `{ data |> json }`, `{ url |> trustedResource }`. Filters are registered and
  `grep`-able (`|> raw` greps cleanly for audit), and the registry is pluggable
  so projects can add vetted domain-specific safe constructors.
- Filters can carry the *type contract*: a `raw`/`js`/`css` filter's signature
  forces a dedicated safe type (à la safehtml), so the escape hatch is a
  deliberate, type-checked step rather than a string conversion.

**Already shipped (runtime):** context escapers (`Text` / `AttrValue` / `URL`),
URL scheme allow-list (http/https/mailto/tel → `about:invalid#gsx` sentinel),
attribute-name validation against tag breakout (`validAttrName`), documented
`Attrs` security contract.

**Prioritized work (dig into, in order):**

1. ✅ **[Blocking] Context dispatch in codegen** — attribute escaping is now
   auto-dispatched (`AttrValue`/`URL`/reject) from the *parsed attribute name*,
   not author choice. (Element-content interpolation was already §5 type-aware.)
   This is the safe-by-default core. *(A full Text/RCDATA/comment-position state
   machine across all markup positions is broader future work.)*
2. ✅ **[Blocking] Always-quote emitted attribute values** — every static and
   expr attr value is wrapped in codegen-emitted double quotes; kills the Jinja
   `xmlattr` / unquoted-attribute injection class (CVE-2024-22195) outright.
3. ✅ **[High] Compile-time rejection of bare exprs in JS/CSS/`on*=` contexts**
   — `on*`/`@*`/`hx-on*` and `style` expr values are a build error (fail-closed),
   not a runtime `ZgotmplZ`. Safe-type pipeline filters (`|> js`/`|> css`) will
   later open these intentionally.
4. 🟡 **[High] Harden `urlSanitize` + complete URL-attr table** — control-char /
   whitespace scheme evasion (`java\tscript:`, `\x01javascript:`, leading-space)
   maps to the sentinel (verified by adversarial probe); the `urlAttrs` table
   covers `href`/`src`/`action`/`formaction`/`poster`/`cite`/`ping`/`data`/
   `background`/`manifest`/`xlink:href`/`hx-*`. **Remaining:** `meta
   http-equiv=refresh` content (CVE-2026-27142) and `base href` carriers; a
   dedicated fuzz target seeded from the OWASP filter-evasion sheet.
5. ⬜ **[High] Split navigational vs resource URLs** in the type/filter
   vocabulary (`URL` vs `TrustedResourceURL`, à la safehtml; html/template
   conflates them — go#27926).
6. ⬜ **[Medium] One obvious data→`<script>` path** — `{ data |> json }`
   (HTML-safe JSON: escape `< > &` and U+2028/U+2029).
7. ⬜ **[v2] CSP nonce threading** for emitted `<script>`/`<style>` — thread a
   per-request nonce; do not build a CSP engine (header is the server's job).

## Tracked debts / deferrals

- ⬜ **`ctx` not usable in interpolations** — the design calls `ctx` ambient and
  valid in interp exprs (e.g. `{ structpages.ID(ctx, …) }`), but the type-resolution
  **skeleton** component func (`func X(_gsxp XProps) Node`) never binds `ctx`, so
  `{ f(ctx) }` fails resolution with `undefined: ctx` (the real render closure does
  have `ctx`). Skeleton/closure asymmetry. **Blocks method components (#6)** — the
  `11_struct_methods` example uses `ctx` in bodies. Fix: bind/accept `ctx` in the
  skeleton component func. (Found by Phase-3 independent review.)
- ⬜ **Pipeline codegen + filters/`std`/`gen`** — designed
  (`2026-06-19-gsx-pipeline-and-extensions-design.md`), not implemented (phase-2 #4).
- ⬜ **Example 02 `02_text_escaping.gsx`** stays red (`47:35`): a `//` line
  comment in markup-content position. Separate parser gap; decision pending.
- ⬜ **`_gsx`-alias generator-emitted imports** — robust form of the import-shadow
  guard (currently `gsx`/`strconv` are reserved param names as a stopgap).
- ⬜ **Structured diagnostics** (`internal/diag`: GSXnnnn codes, ranges, JSON) —
  designed in the CLI-skeleton spec; not built.
- ⬜ **CLI / `gen.Main`** (`generate`/`fmt`/`vet`/`lsp`/`render`), file discovery,
  `//go:generate`, incremental/watch — designed, not built.
- ⬜ **Codegen niceties** — coalesce adjacent `gw.S` static writes; `//line`
  trailing-state reset; `data:image` URL allowance.

## Design docs (reference)

- `2026-06-18-gsx-templating-design.md` — the language.
- `2026-06-18-gsx-codegen-walkthrough.md` — hand-written generated code / runtime model.
- `2026-06-19-gsx-runtime-design.md` — runtime package.
- `2026-06-19-gsx-codegen-design.md` — codegen architecture + lowering rules.
- `2026-06-19-gsx-pipeline-and-extensions-design.md` — `|>` + filters + `gen.Main`.
- `2026-06-18-gsx-cli-skeleton-design.md` — CLI, exit codes, diagnostics model.
- `2026-06-20-gsx-security-design.md` — threat model, contextual auto-escaping,
  pipeline escape hatch, URL/JS/CSS contexts (to be written).
