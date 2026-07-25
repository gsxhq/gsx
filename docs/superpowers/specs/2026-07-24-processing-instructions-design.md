# Processing instructions: `<?marker>` and `<?start>…<?end>`

Status: approved (design), 2026-07-24

## Summary

Add processing-instruction markup to gsx so a page can emit the placeholders used
by [declarative partial
updates](https://developer.chrome.com/blog/declarative-partial-updates):

```gsx
<?marker name="results">

<?start name="feed">
	<Spinner/>
<?end>
```

The vocabulary is fixed to three targets — `marker`, `start`, `end` — the only
ones with defined semantics. `name` accepts a string literal or a `{goexpr}`
hole, like any attribute value.

## Background: what the HTML spec actually says

[whatwg/html#12118](https://github.com/whatwg/html/pull/12118) makes `<?target
data?>` tokenize into a real `ProcessingInstruction` node. Three rules from that
tokenizer drive this design:

1. **The target is constrained.** It starts with ASCII alpha or `_`, continues
   with `[A-Za-z0-9_-]`. An invalid first character, or a target matching `xml` /
   `xml-stylesheet` (ASCII case-insensitive), makes the browser **silently
   downgrade the whole thing to a bogus comment**.
2. **The data is opaque.** The tokenizer does no pseudo-attribute parsing and no
   character-reference decoding. `name="…"` is interpreted by the
   declarative-partial-updates layer, not the parser.
3. **A PI ends at the first `>`.** (`?>` also ends it, dropping the `?`.) There
   is **no quote protection** — a `>` inside `name="a>b"` terminates the PI
   early, and everything after it is live HTML.

Rules 2 and 3 together are the security core: because PI data is never
entity-decoded, `>` **cannot be escaped**. It can only be rejected. See
[Escaping](#escaping-the-pi-name-sink).

`marker` / `start` / `end` and the `<template for="…">` that patches them are
defined by the declarative-partial-updates feature layer, which is
Chrome-experimental and not yet in WHATWG main.

### Why not general `<?target data?>`

Considered and rejected for v1. No target outside `marker`/`start`/`end` has
defined semantics, so the general form buys reach we cannot describe or test.
Matching three literals is also less code than porting the target-character
scanner plus the `xml`/`xml-stylesheet` disallow rules.

lit-html is not a reason to generalize: it does not emit PIs. Its markers look
like `lit$$123456789$`, whose `$` is not a valid target character, so they hit
the invalid-first-character fallback and **stay Comment nodes** — which is what
lit's `TreeWalker` requires. #12118's lit discussion is about *not breaking* that
markup, not about emitting it.

`gsx.Raw("<?…>")` remains the escape hatch, and widening to general targets later
is purely additive.

## Syntax

```
<?marker name=VALUE>                 // void
<?start  name=VALUE> children <?end> // region
```

- `VALUE` is `"literal"` or `{goexpr}` (with `|>` pipeline stages) — the existing
  attribute-value grammar, no new value syntax.
- `name` is required on `marker` and `start`. `<?end>` takes no attributes.
- Regions nest and may contain any gsx markup, including control flow and other
  PIs.
- gsx source terminates a PI with `>`, and gsx emits `>` (matching the Chrome
  examples). The XML-style `?>` terminator is **not** accepted in gsx source in
  v1 — it is a diagnostic pointing at `>`. Both terminate identically in the
  browser, so accepting `?>` later is additive.

## AST

Two nodes in `ast/ast.go`, sitting beside `Doctype` and `HTMLComment`:

```go
type Marker struct {          // <?marker name=…>
	span
	Name Attr
}

type MarkerRegion struct {    // <?start name=…> … <?end>
	span
	Name              Attr
	Children          []Markup
	ChildrenMultiline bool
}
```

Both implement `markupNode()`.

`Name` is an `Attr` holding a `*StaticAttr` or `*ExprAttr` (with `Name == "name"`)
rather than a bespoke struct. That reuses existing machinery for free: `Inspect`
walking, the LSP go-to-definition/hover bridges keyed on `ExprAttr.ExprPos`,
pipeline stages, and the printer's attribute-value layout. It does **not** reuse
attribute *escaping* classification — see below.

> Naming note for review: `Marker`/`MarkerRegion` follows the plain descriptive
> style of `Doctype`/`HTMLComment`. Alternative: `ProcessingInstruction`/`PIRegion`.

## Parser

In `parseElement` (`parser/markup.go`), add a `p.peek() == '?'` branch calling
`parsePI`, symmetric to the existing `'!'` → `parseBang`. `parsePI`:

1. Scans the target; rejects anything outside `marker`/`start`/`end`.
2. Parses the required `name=VALUE` via the existing attribute-value path, then
   expects `>`.
3. For `start`, parses children up to `<?end>`.

Two supporting changes:

- **Text-position gate.** `startsTagAt` (`parser/identifier.go`) decides whether a
  `<` begins markup. It must accept `?` so `<?marker …>` is recognized in child
  and markup-attribute positions.
- **Region terminator.** `parseChildren(closeTag string)` terminates only on
  `</tag>`. Generalize its terminator so a region can close on `<?end>`:
  introduce an internal terminator descriptor (either `</tag>` or `<?end>`), with
  `parseChildren` kept as a thin wrapper over it. This keeps one child loop —
  text, interpolation, control flow, and nested elements behave identically
  inside a region.

## Escaping: the PI-name sink

A new sink, because no existing one has these rules and `html/template` has no PI
context to port.

**Rejected characters: `>` and `"`.**

- `>` terminates the PI (tokenizer rule 3) — a value containing it breaks out
  into live HTML. Unescapable, since PI data is not entity-decoded.
- `"` terminates the `name="…"` quoting the feature layer reads — a value
  containing it can forge additional pseudo-attributes.

`?>` needs no separate rule: rejecting `>` already excludes it.

Strict handling, chosen over stripping so a mistargeted update can never happen
silently:

- **Static value** → validated at codegen; an offender is a positioned
  **diagnostic** (compile-time failure).
- **Dynamic `{expr}`** → `Writer.PIName(s)` writes `s`, or sets a render
  **error** if it contains `>` or `"`.

Relaxing later (e.g. to strip-and-warn) is a compatible change; tightening would
not be.

## Codegen

New cases in `genNode` (`internal/codegen/emit.go`):

- Static name → a single `emitS`: `_gsxgw.S("<?marker name=\"results\">")`.
- Dynamic name → `S("<?marker name=\"")` + `PIName(expr)` + `S("\">")`.
- `MarkerRegion` emits its open PI, walks `Children`, then emits `<?end>`.

Escaping is selected by the AST node, not by `attrclass` attribute-name
classification — the `Attr` reuse is syntactic only.

## Formatter

`internal/printer`: `Marker` prints as a standalone segment like `Doctype`;
`MarkerRegion` prints element-style with indented children and a `<?end>` close.
Both join `segment.go`'s block-node list so surrounding whitespace is handled
like other block markup.

## Diagnostics

- unknown PI target (with the valid set listed)
- `?>` used as the terminator (point at the `?`, so the caret sits on the first
  offending byte — pinned by corpus `pi/e_question_terminator`)
- missing `name` on `marker` / `start`
- attributes on `<?end>`
- unterminated `<?start>` (EOF before `<?end>`)
- a `</…>` close tag reached inside a region — including a fragment's `</>` —
  naming `<?end>` as the expected terminator
- stray `<?end>` with no open region
- static `name` containing `>` or `"`

## Testing

Per CLAUDE.md, a syntax change ships corpus coverage in every valid context. PIs
are markup-position-only, so the contexts are child positions:

- **Semantic corpus** (`internal/corpus/testdata/cases/pi/`): static `marker`;
  expr `marker`; expr with a `|>` pipeline; region with children; nested regions;
  PI inside `{if}` / `{for}`; PI in element-literal position; one case per
  diagnostic above. Each pins `generated.x.go.golden` + `render.golden` +
  `diagnostics.golden`.
- **fmt corpus** (`internal/gsxfmt/testdata/cases/`): void and region layout,
  including a multiline region and a nested one.
- **Runtime unit tests** (root package): `PIName` accepts `?`, `<`, `'`, and
  non-ASCII; errors on `>` and `"`.

## Sibling updates

- `../tree-sitter-gsx` — grammar rules for both forms
- `../vscode-gsx` — TextMate grammar
- `../gsxhq.github.io` — CodeMirror syntax; VitePress sidebar entry
- `docs/guide/syntax/` — a page documenting the syntax, the fixed vocabulary, and
  the strict `name` rules

## Position: markup only

Processing instructions are markup, not values. They are valid in child
position and inside a `{ … }` markup attribute, but **not** as a Go-expression
value: `x := <?marker name="a">` is an error, since `splitGoElements` admits
only `*ast.Element` and `*ast.Fragment` there. Users see:

```
gsx: a *ast.Marker is not supported as a Go expression value here
```

Verified during implementation. Admitting PIs as element literals later would be
additive.

## Deferred

- Processing instructions as Go-expression values (element literals).
- General `<?target data?>` for arbitrary targets.
- Multiple/arbitrary pseudo-attributes beyond `name`.
- Any `<template for="…">` sugar — it is an ordinary element and needs no gsx
  feature.
