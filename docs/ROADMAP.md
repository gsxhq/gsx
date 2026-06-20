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
| Codegen | 🟡 phase 1 done (foundation + interpolation) + control flow; attributes/pipeline/methods pending |
| CLI / `gen.Main` | ⬜ not started |
| Pipeline `|>` end-to-end | 🟡 parsed + codegen errors cleanly (no silent drop) — **lowering + filters not done** |

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
3. ⬜ **Attributes** — static / expr (type+context-aware: `AttrValue` vs `URL`) /
   bool / composable `class`+`style` / spread / conditional `{ if … { attr } }`.
4. ⬜ **Pipeline `|>` + filters** — lower `Stages` to nested (generic) filter
   calls; `gen`-registered filter resolution via `go/types` harvest; ship a
   starter `std` filter package. (Ergonomically load-bearing for numerics.)
5. ⬜ **Child-component props + `{children}`** — attr→field mapping, children/slot
   closures.
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

1. ⬜ **[Blocking] Context state machine in codegen** — auto-dispatch
   `Text`/`AttrValue`/`URL` from *parsed HTML position* (not author choice).
   This is the make-or-break: without it, gsx is a string concatenator with
   helpers, not "safe like html/template". Folds into phase-2 #3 (Attributes).
2. ⬜ **[Blocking] Always-quote emitted attribute values** — kills the Jinja
   `xmlattr` / unquoted-attribute injection class (CVE-2024-22195) outright.
3. ⬜ **[High] Compile-time rejection of bare strings in JS/CSS/`on*=` contexts**
   — escaping *cannot* secure these grammars (safehtml refuses to try); require a
   safe type via pipeline filter (`|> js` / `|> css` / `|> json`). gsx's
   leapfrog feature: a build error, not a runtime `ZgotmplZ`.
4. ⬜ **[High] Harden `urlSanitize`** against control-char/whitespace scheme
   evasion (`java\tscript:`, `\x01javascript:`); fuzz target seeded from the
   OWASP filter-evasion sheet. Build the *complete* URL-context attribute table
   (`meta http-equiv=refresh` — CVE-2026-27142, `formaction`, `base href`,
   `ping`, `xlink:href`), not just `href`/`src`.
5. ⬜ **[High] Split navigational vs resource URLs** in the type/filter
   vocabulary (`URL` vs `TrustedResourceURL`, à la safehtml; html/template
   conflates them — go#27926).
6. ⬜ **[Medium] One obvious data→`<script>` path** — `{ data |> json }`
   (HTML-safe JSON: escape `< > &` and U+2028/U+2029).
7. ⬜ **[v2] CSP nonce threading** for emitted `<script>`/`<style>` — thread a
   per-request nonce; do not build a CSP engine (header is the server's job).

## Tracked debts / deferrals

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
