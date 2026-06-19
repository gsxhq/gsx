# gsx Runtime Package — Design

**Date:** 2026-06-19
**Status:** design, pending user review → implementation plan.

The `gsx` runtime is the dependency-free package that *generated* code calls. This
phase builds and tests it **standalone** (by hand-writing `Node`s and `Writer`
calls) — no parser, no codegen, no type resolution. It is the foundation the
codegen phase targets. The runtime model was validated in
`2026-06-18-gsx-codegen-walkthrough.md`; this spec pins the concrete surface,
escaping rules, class/style merge, and `Attrs` behavior.

## Goals / non-goals

- **Goal:** a small, allocation-light, standard-library-only package that emits
  correct, context-escaped HTML by streaming straight to an `io.Writer`.
- **Goal:** `gsx.Node` is structurally identical to `templ.Component`
  (`Render(ctx, w) error`) for templ-ecosystem interop **without** importing templ.
- **Non-goal (this phase):** code generation, type-aware interpolation dispatch
  (codegen decides which writer helper to call), the `?` lowering, the parser/AST.
- **Non-goal:** a Tailwind merger implementation — only the *seam* (`ClassMerger`
  hook) and the default dedupe+join merger.

## Package layout

Single package `gsx` at the module root (`github.com/gsxhq/gsx`), split into
focused files:

- `node.go` — `Node`, `Func`, `Raw`.
- `writer.go` — `Writer`, `W`, and the write helpers (`S`/`Text`/`AttrValue`/
  `URL`/`BoolAttr`/`Class`/`Style`/`Spread`/`Node`/`Err`).
- `escape.go` — streaming context escapers (text / attribute / URL).
- `class.go` — `ClassPart`, `Class`/`ClassIf` constructors, the merge pipeline,
  and the `ClassMerger` hook + default merger.
- `attrs.go` — `Attrs` type, its methods, and spread rendering.
- `*_test.go` — per-file unit tests; plus a `golden`-style test that hand-builds
  a small `Node` tree and asserts exact HTML output.

## Core types

```go
// Node is gsx's own interface — method set identical to templ.Component, so a
// gsx.Node satisfies templ.Component structurally (no templ import).
type Node interface {
	Render(ctx context.Context, w io.Writer) error
}

// Func adapts a render function to a Node (cf. templ.ComponentFunc).
type Func func(ctx context.Context, w io.Writer) error
func (f Func) Render(ctx context.Context, w io.Writer) error { return f(ctx, w) }

// Raw wraps trusted, pre-escaped HTML — the opt-out from auto-escaping.
func Raw(html string) Node
```

## Writer — streaming with error threading

`Writer` wraps the destination and records the **first** write error; once set,
every subsequent helper is a no-op (so generated code needs no per-write error
check). The final error is read once via `Err()`.

```go
type Writer struct { w io.Writer; err error }
func W(w io.Writer) *Writer
func (gw *Writer) Err() error

func (gw *Writer) S(s string)                          // trusted static markup, verbatim
func (gw *Writer) Text(s string)                       // HTML-escape text content
func (gw *Writer) AttrValue(s string)                  // escape a double-quoted attr value
func (gw *Writer) URL(s string)                        // sanitize + escape a URL attr value
func (gw *Writer) BoolAttr(name string, on bool)       // writes ` name` when on, else nothing
func (gw *Writer) Class(parts ...ClassPart)            // compose+merge+escape, write the class VALUE
func (gw *Writer) Style(parts ...ClassPart)            // compose (';'-join)+escape, write the style VALUE
func (gw *Writer) Spread(ctx context.Context, a Attrs) // render an attr bag, deterministically
func (gw *Writer) Node(ctx context.Context, n Node)    // render a child; nil n is a no-op
```

Notes:
- `Class`/`Style` write only the **attribute value** (the generator emits
  `class="` … `"` around the call), so the escaping is attribute-context.
- `Spread` takes `ctx` for forward-compatibility (e.g. future Node-valued
  attrs); the v1 implementation writes scalar attributes and does not use it
  beyond passing through. (Kept to match the walkthrough's generated calls.)
- A `nil` `*Writer` is never produced; `W` always returns a usable writer.
- Helpers operate on the underlying `io.Writer`; if a write fails, `gw.err` is
  set and retained.

## Escaping (escape.go)

Hand-rolled **streaming** escapers (write safe runs directly, emit entities for
specials) — matching `html/template` semantics, avoiding its template-parse cost
and giving allocation-light streaming. Three contexts:

- **Text** (`Text`): escape `&`→`&amp;`, `<`→`&lt;`, `>`→`&gt;`, `"`→`&#34;`,
  `'`→`&#39;`. (Superset-safe; matches `html.EscapeString`'s set.)
- **Attribute value** (`AttrValue`, double-quoted context): escape `&`, `<`,
  `>`, `"`, `'` to entities. (Same set; the double-quote and `&` are the
  load-bearing ones, the rest are defense-in-depth.)
- **URL** (`URL`): first **sanitize the scheme** — if the (case-insensitive,
  whitespace/control-stripped) leading scheme is a dangerous one (`javascript:`,
  `vbscript:`, `data:` other than safe image types), replace the whole value with
  the sentinel `about:invalid#gsx` (mirrors `html/template`'s `#ZgotmplZ`
  behavior). Relative URLs, fragments, query strings, and `http`/`https`/`mailto`/
  `tel` pass. Then **escape the result** as an attribute value. (Percent-encoding
  of already-present sequences is left intact; we do not re-encode — matching
  templ's `URL` treatment of app-controlled URLs.)

Each escaper has a table-driven correctness test against known vectors,
including the classic XSS payloads (`<script>`, `" onmouseover=`,
`javascript:alert(1)`).

## Class / style composition (class.go)

```go
type ClassPart struct { classes string; on bool } // unexported fields
func Class(s string) ClassPart            { ... } // unconditional
func ClassIf(s string, on bool) ClassPart { ... } // included only when on

// ClassMerger is the installable merge strategy. Default: dedupe (first
// occurrence wins, order preserved) + single-space join. Apps replace it once at
// init to install e.g. tailwind-merge-go.
var ClassMerger func(tokens []string) string = defaultClassMerge
```

`gw.Class(parts...)` pipeline: keep `on` parts → split each on ASCII whitespace
→ flatten in order → drop empties → `ClassMerger(tokens)` → `AttrValue`-escape →
write. `gw.Style(parts...)`: keep `on` parts → trim → drop empties → join with
`"; "` → `AttrValue`-escape → write (no merger; style declarations are not
deduped). The shared `ClassPart` carries the conditional string for both, since
the source sugar (`"str": cond`) is identical for class and style.

`ClassMerger` is a package-level `var` (global, one merger per process), per the
"install a merger" model; swapping it is an app-init concern, not per-render.

## Attrs (attrs.go)

```go
type Attrs map[string]any

func (a Attrs) Has(key string) bool
func (a Attrs) Get(key string) (any, bool)
func (a Attrs) Class() string                 // merged class string from the bag's "class" entry
func (a Attrs) Without(keys ...string) Attrs   // a copy minus keys
func (a Attrs) Take(key string) (any, Attrs)   // (value, rest-without-key) in one step
func (a Attrs) Merge(other Attrs) Attrs         // combine (other wins; class/style concatenated)
```

- `Without`/`Take`/`Merge` return **new** maps (do not mutate the receiver).
- `Merge` concatenates `class` and `style` values rather than overwriting (so a
  caller's classes add to existing ones); all other keys: `other` wins.
- `Class()` reads the `"class"` entry (string / `[]string` / a class value) and
  returns the merged class string (via the same merge pipeline).

`gw.Spread(ctx, a)` renders the bag **deterministically**: iterate keys in
sorted order; for each value: `bool` → boolean-attr semantics (` key` when true,
omitted when false); `string` → ` key="…"` with `AttrValue` escaping; anything
else → `fmt.Sprint`-formatted then `AttrValue`-escaped. (`class`/`style` keys are
written as ordinary escaped string attributes here; composition with declared
class/style happens in generated code, not in `Spread`.) Sorted keys make
generated HTML stable and golden-testable.

## Error handling

- All write errors thread through `Writer.err`; `Render` implementations return
  `gw.Err()` at the end. The first error wins; later helpers no-op.
- The runtime never panics on normal input; `Raw`/`Node(nil)`/empty `Attrs` are
  all safe.

## Testing

- Unit tests per file: escaper vectors (incl. XSS), class merge (dedupe, order,
  conditional, whitespace flattening, custom `ClassMerger`), style join, `Attrs`
  methods (immutability, `Merge` class concat), `Spread` determinism + bool/
  string/other value types, `Writer` error-threading short-circuit, `Node` nil
  safety, `Raw` passthrough.
- One integration golden: hand-build a `Func` tree mirroring the walkthrough's
  `Card`/`Box` output and assert the exact HTML string — proves the helpers
  compose into correct markup before any codegen exists. This is the seed of the
  eventual `render.golden` acceptance gate.

## Open / deferred

- Typed numeric helpers (`Int`/`Float`) are **deferred**: codegen formats values
  and calls `Text` for v1; add typed helpers only if codegen ergonomics demand.
- A real Tailwind merger is out of scope (only the hook + default).
- `Spread`'s `ctx` parameter is presently pass-through (forward-compat).
