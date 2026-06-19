# gsx Codegen — Design

**Date:** 2026-06-19
**Status:** design, pending user review → implementation plan.
**Validated by:** the `experiment/codegen-spike` branch (4 commits) — every load-bearing decision below is proven end-to-end (parse → resolve → emit `.x.go` → compile → render exact HTML).
**Builds on:** `2026-06-18-gsx-codegen-walkthrough.md` (runtime model + hand-written lowering), the `gsx` runtime package (already merged), and `2026-06-19-gsx-pipeline-and-extensions-design.md` (the `gen`/`std`/`internal/codegen` layout, `//line` maps, no-runtime-reflection).

## Overview & pipeline

Codegen lowers parsed `.gsx` files to `.x.go` Go source targeting the `gsx`
runtime. The pipeline, per package:

1. **Parse** every `.gsx` in the package → gsx AST (existing parser).
2. **Resolve types** with `go/types`, loaded via `golang.org/x/tools/go/packages`
   over the **whole package** — the hand-written `.go` files plus synthesized
   *skeletons* of the gsx components, injected with `packages.Config.Overlay`.
   One unified type-check resolves cross-file types, cross-component calls, and
   imports. (§"Symbol combination".)
3. **Emit** `.x.go` per `.gsx` using the resolved types to choose precise,
   type-checked writer calls. No runtime reflection, no `any`.
4. **`go build`** compiles the generated code (the real, final type-check).

Steps 2 and 4 both type-check Go — inherent to type-aware codegen; every such
tool pays it. The cost is **one package load per `generate`**, amortized over all
the package's components, with Go's build cache speeding repeats.

### Dependency boundary (firm)

- The **runtime** (`github.com/gsxhq/gsx`, module root) stays **standard-library
  only** — it is what users import to render.
- The **generator** (`internal/codegen` and the `gen`/CLI layer) may use
  `golang.org/x/tools/go/packages` and friends — it is a build-time tool.

## Symbol combination — the skeleton overlay (validated)

A `.gsx` component *becomes* Go symbols (`Card`, `CardProps`) living in the **same
package** as the hand-written `.go` files, and components reference each other
(`<Card/>` → `Card(CardProps{…})`). So all symbols must be type-checked together.

For each `.gsx` file we synthesize a **skeleton** `.go` file containing: the
file's `GoChunks` (verbatim user Go — imports, types, helpers), and for each
component its **real props struct** and **func signature** with a **probe body**:
used params bound to same-named locals, each interpolation written as `_ = (expr)`
(a parenthesized RHS marks an interpolation probe), and each child component as
`_ = Child(ChildProps{})`. We inject each skeleton at its `<file>.x.go` path via
`packages.Config.Overlay` (which also *replaces* any stale on-disk `.x.go`), load
the package, and harvest each interpolation's type from `TypesInfo`.

The skeleton is a faithful stand-in for the final `.x.go`'s **signatures**, so
the real generated code (full bodies) type-checks identically. This is the
production form of the spike's probe.

## Lowering rules

### Components → funcs

- `component X(params) { body }` → `type XProps struct {…}` + `func X(p XProps)
  gsx.Node`. Props field types come **syntactically** from the param list
  (`title string` → `Title string`); param→field name uses first-letter-upper
  (the §3 rule; pluggable mapper deferred).
- **Method components** `component (r T) X() { body }` → `func (r T) X() gsx.Node`
  (receiver carries the page data; no props struct unless params are present).
- The body is a `gsx.Func` closure: `return gsx.Func(func(ctx, w) error { gw :=
  gsx.W(w); …; return gw.Err() })`. Ambient `ctx` is the closure param.
- **Used params are bound to same-named locals** (`title := p.Title`) so every
  interpolation/attribute expression emits **verbatim** — no AST rewriting.
  "Used" = referenced in value position by any expression (token-scan, excludes
  selector fields).

### Interpolation `{ expr }` → type-aware writer call

The resolved `types.Type` selects the call (the §5 table). Escaping context
(text vs attribute vs URL) is **structural** — known from the node's position in
the AST, independent of the value's type.

| Resolved type | Text context | Notes |
|---|---|---|
| `string`, `[]byte` | `gw.Text(s)` / `gw.Text(string(b))` | HTML-escaped |
| integer / float | `gw.Text(strconv.…)` | formatted then escaped |
| `bool` | `gw.Text("true"/"false")` text; boolean-attr in attribute position | context-dependent |
| `gsx.Node` (anything with `Render(ctx,w) error`) | `gw.Node(ctx, n)` | rendered inline, nil-safe |
| `[]gsx.Node` | loop `gw.Node` | each in order |
| `fmt.Stringer` | `gw.Text(x.String())` | |
| `gsx.Raw` | `gw.Node(ctx, x)` | trusted, unescaped (Raw is a Node) |
| `(T, error)` (2-value) | unwrap+propagate (see Errors) | T then rendered by its row |
| anything else | **compile-time diagnostic** | clear `.gsx`-positioned error |

### Attributes

- **Static** `class="x"` → `gw.S(\` class="x"\`)`.
- **Expr** `id={x}` → `gw.S(\` id="\`)` + type/context-aware value (`gw.AttrValue`,
  or `gw.URL` for URL-context attrs — `href`/`src`/`action`/`formaction`/`poster`/
  `cite`/`hx-get`/`hx-post`/…, a known list) + `gw.S(\`"\`)`.
- **Bool** bare `disabled` → `gw.S(\` disabled\`)`; `disabled={cond}` →
  `gw.BoolAttr("disabled", cond)`.
- **Composable** `class={ … }` / `style={ … }` → `gw.S(\` class="\`)` +
  `gw.Class(parts…)` / `gw.Style(parts…)` + `gw.S(\`"\`)`, the parts built from
  the comma/`"x":cond` grammar.
- **Spread** `{...attrs}` → `gw.Spread(ctx, attrs)`.
- **Conditional** `{ if cond { attr } }` → `if cond { <emit attr> }`.

### Child components

- `<Card title={x} featured>kids</Card>` → `gw.Node(ctx, Card(CardProps{Title:
  x, Featured: true, Children: <kids closure>}))`. Attr→field mapping as above;
  `{children}` slot and inline-markup slot args become `gsx.Func` closures.
- **Auto-fallthrough** of undeclared attrs to the single root and the implicit
  `attrs` bag (templating-design §4) — included, but its compile-time
  ambiguity checks make it a later task.
- Casing decides element vs component (`<div>` HTML, `<Card>`/`<ui.Button>`
  component) — no symbol resolver needed.

### Control flow → plain Go around writes

`{ if/for/switch }` lower to ordinary Go statements wrapping the child writes
(walkthrough §3): `for _, it := range items { gw.S(…); … }`. Bodies recurse the
same emitter. `{{ stmt }}` blocks emit the Go statements verbatim between writes.

### Errors (the `?` decision)

`go/types` gives us arity, so a `(T, error)` interpolation/attribute is detected
and lowered to a temp + propagate: `v, err := expr; if err != nil { return err }`
then render `v`. **Leaning: implicit auto-unwrap** (drop the `?` marker — error
propagation is the overwhelming default; the escape hatch is explicit Go in a
`{{ }}` block). The marker's surface (removed / kept / type-enforced) is a
**deferred cleanup**; the *capability* (detect + unwrap) is built now.

## Diagnostics & position mapping

- Generated `.x.go` carries `//line file.gsx:L:C` directives so the Go compiler's
  errors on generated code point at the `.gsx` source.
- `go/types` errors during resolution are mapped from the skeleton's positions
  back to the originating `.gsx` interpolation/attribute and reported as
  `.gsx`-positioned diagnostics (ties into the deferred `internal/diag` model).

## Testing strategy

- **Source goldens** — generated `.x.go` compared byte-exact (lowering shape).
- **Render goldens** — the generated code is compiled and run against the real
  runtime in a temp module, and its HTML asserted **semantically** (via
  `golang.org/x/net/html`, whitespace-insensitive). Helpers already exist
  (`renderGSX`, `renderPackage`, `assertHTMLEqual`).
- The **example corpus** (`examples/*.gsx`) is the acceptance target: each
  example that the codegen handles graduates to a render golden.

## Scope

**v1 (this design's build), sequenced into incremental plan phases** — each an
independently testable slice that graduates more of the example corpus to render
goldens, starting from the spike as the seed: components + method components;
params→props + local-binding; full-type interpolation (the §5 table); control
flow (`if`/`for`/`switch`, `{{ }}`); attributes (static/expr/bool/composable
class+style/spread/conditional); child components with props + `{children}`;
context-aware escaping; error auto-unwrap; `//line` maps; collapse the spike's
transitional probe path onto `go/packages`. The implementation plan decides the
exact phase boundaries (likely: core emit + interpolation → control flow →
attributes → child components → fallthrough/diagnostics).

**Deferred (own specs/plans later):**
- Auto-fallthrough attribute placement + its compile-time ambiguity errors (may
  land late in v1 or as v1.1).
- The `gen.Main` CLI / `generate` command / file-watching / incremental builds.
- Pipeline `|>` + filter resolution + `std` (its own design already written).
- The structured `internal/diag` model (codes/ranges/JSON).
- Pluggable attr→field and filter-name mappers.

## Open questions / deferred

- **`?` marker surface** — remove (implicit) vs keep vs type-enforce; capability
  built now, surface decided in cleanup.
- **`ctx` in the skeleton probe** — interpolations that reference ambient `ctx`
  (e.g. `route.URL(ctx)`) need `ctx` in the probe scope; add a probe `ctx` local.
- **Coalescing adjacent `gw.S` static writes** — a peephole optimization
  (correctness-neutral); deferred.
- **GoChunk import merging** — when a `GoChunk` and the generated preamble both
  import a package; dedupe imports in the emitter.
- **`[]byte` / `data:image` URL nuance** and other runtime-edge categories.
