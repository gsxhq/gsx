# One tag-aware Go-expression scanner: `<tag>`/`<>` and interpolating backtick literals as values everywhere

**Status:** design · **Date:** 2026-07-07

## Idea

Two constructs should be usable as ordinary Go-expression **values** in every Go
position inside a `.gsx` file — including *inside* a `{ }` interpolation, where
neither works today:

1. **Element / fragment literals** — `<div/>`, `<>…</>` (already values at
   top-level Go since #42/#44, but not inside interpolations):
   ```gsx
   <div>{ wrap(<>…</>) }</div>              // fragment as a call argument, inline
   { pick(cond, <A/>, <B/>) }              // element as a conditional operand
   ```
2. **Interpolating backtick literals** — `` `hello @{name}` `` — the existing
   `@{ }`-in-backtick construct (`EmbeddedInterp`), lifted out of its gated
   positions into a first-class value:
   ```gsx
   var greeting = `hello @{name}`          // a string value, anywhere
   f(`id-@{n}`)                            // as a call argument
   { emphasize(`@{label}!`) }             // inside an interpolation
   ```

Both are blocked by the *same* root cause and unlocked by the *same* fix: gsx
scans interpolation interiors with a separate, tag-blind, backtick-fragile byte
scanner. Replacing that with the top-level `go/scanner`-based scanner — the one
that already understands tags and operand position — makes the parser **more
consistent** (one scanner, no divergence) and unlocks both value forms at once.

**No type-structure change.** Purely parser (unify onto one scanner) + a codegen
splice at the sites that currently emit a raw Go fragment verbatim.

## Why it fails today: two divergent Go-scanners

- **Top-level Go regions** are scanned by `scanGoElementMarks` (`parser/goexpr.go`)
  — a `go/scanner`-based walk with **operand/operator tracking** that recognizes
  a `<` at operand position as a tag start and skips the tag span. This is why
  `var x = wrap(<>…</>)` works.
- **Every interpolation-family boundary** funnels through **one** tag-blind
  byte scanner, `goDepth1End` (`parser/boundary.go`), via `goExprEnd` /
  `goStagesEnd`. Its callers: interpolation `{ expr }` (`markup.go:15`), GoBlock
  `{{ stmt }}` (`markup.go:173,177`), attribute values (`attrs.go:163,182,636`),
  value-form `{ if … }` / `{ switch … }` (`valueform.go:92`), and pipe-stage
  tails. Siblings `splitPipe` (on `|>`), `composedDelims` (ordered-attrs `,`/`:`),
  and `parenEnd` complete the family. These count `{}()[]` depth and skip
  strings/comments and `js`…`/`css`…` literals, but have **no notion of a tag
  span** — so embedded-tag prose (an apostrophe, a `</`, a nested `{ }`) misdirects
  them, and `genInterp` emits the expression verbatim anyway.

The limitation is the divergence, and the boundary logic is a **single
chokepoint**. Make that chokepoint tag-aware and the fix is uniform across every
position above.

## Decision: one `go/scanner`-based expression scanner

Promote `scanGoElementMarks` into a fuller **expression scanner** and route all
interpolation-family delimiting through it. In one operand/operator-aware walk it
reports: the depth-0 closing `}`, depth-0 `|>` positions, tag/fragment spans,
backtick-literal spans, and depth-0 `,`/`:` — while skipping strings/runes/
comments (`go/scanner`), tag spans, and backtick literals. The byte scanners
retire or become thin callers over this one pass.

Two enabling facts, each the same *shape* of argument — a construct gsx can claim
because Go assigns it no conflicting meaning:

### `|>` is unambiguous in `go/scanner` (empirically confirmed)

`go/scanner` tokenizes `|>` as `token.OR` immediately followed by `token.GTR`
at the next offset, zero scan errors. Adjacent `OR`+`GTR` is **not producible by
valid Go** (`a|>c` is a syntax error; a bitwise-or always has an operand before
any `>`). So "`OR`@p, `GTR`@p+1" is a false-positive-free pipe marker. `a |>= b`
tokenizes `OR` then `GEQ` — correctly *not* a pipe. This also hardens pipe
splitting: a `|>` inside a string is one token, never miscounted.

### Backtick raw strings carry no Go semantics — so `@{ }` is gsx's to claim

Go's `` `…` `` raw string does **no** escaping and **no** interpolation — it is
the literal bytes between the backticks. So gsx can define `@{ expr }` inside a
backtick literal as an interpolation hole with zero ambiguity against Go. gsx
already does exactly this for `js`…@{}…`/`css`…@{}…` and body/attr bare
backticks (see next section); this generalizes it.

### The scanner must recognize backtick literals as first-class spans

`go/scanner` **mis**-tokenizes gsx's escaped backticks: `` js`a\`b` `` scans as
`` STRING(`a\`) IDENT(b) STRING(`) `` because `go/scanner` ends a raw string at
the escaped backtick (it doesn't know gsx's `\`` convention). So the unified
scanner MUST detect a backtick-literal span (bare, `js`, or `css`) and consume
it with gsx's escape-aware `embeddedLiteralEnd`, re-initialising `go/scanner`
past it — the identical "recognize span, skip, resume" move it uses for tag
spans. This recognition is required for correctness *and* is what makes the
backtick literal emittable as a value (below). Proving the skip is byte-correct
is part of the risk-gate spike.

## Feature 1: `<tag>`/`<>` literals as values in any Go position

Once the chokepoint skips tag spans, a `<tag>`/`<>` literal delimits correctly
inside interpolations, GoBlocks, attr values, value-form arms, and pipe stages.
Codegen at each of those emit sites runs the existing `splitGoElements` over the
fragment and lowers embedded tags to their inline `gsx.Func(...)` value — reusing
the element/fragment-literal machinery, with the scope-capturing IIFE probe
(emit ≡ probe) mirrored on the analyze side. Nonsense positions (a node in a
`<script>` JS context) are gated by the type-checker as ordinary type errors.

**Because the boundary finder is shared, partial scope is awkward:** the moment
the chokepoint is tag-aware, `{{ x := <div/> }}` also *parses*; if its emit site
doesn't lower, it emits broken Go. So the coherent choice is to lower at every
`gsx.Node`-producing emit site (`genInterp`, `genGoBlock`, `ExprAttr`,
value-form arms). The type system gates the rest.

## Feature 2: interpolating backtick literals as values everywhere

The `@{ }`-in-backtick construct already exists — it is only position-gated:

- **Body/child:** `` {`…@{expr}…`} `` → `ast.EmbeddedInterp` (`ast.go`), HTML-text
  context.
- **Attr value:** `` name=`…@{expr}…` `` / `js`…`/`css`…` → `ast.EmbeddedAttr`
  (`attrs.go:283`, handling `js` `/`css` `/bare `` ` ``).
- **Raw-text element interior:** `<script …>@{ cfg }</script>`.
- Escapes already defined: `\`` → literal backtick, `\@{` → literal `@{`.

The generalization: **a bare interpolating backtick literal becomes a first-class
Go-expression value in any position.** It evaluates to a **`string`** — its
literal text segments concatenated with its `@{expr}` holes, each formatted by
gsx's standard value formatting (the assembly `embeddedValueExpr` already builds
for the piped `EmbeddedInterp` path). Escaping stays **contextual at the use
site**, unchanged from every other interpolation: rendered as HTML text it is
HTML-escaped, as an attribute value it is attribute-escaped, passed as a plain
Go `string` argument it is not escaped — the literal itself is just a string
builder. Reuses `EmbeddedInterp.Segments` + `embeddedValueExpr` wholesale; the
new work is (a) parsing it in any expression position via the unified scanner's
backtick-span recognition and (b) emitting its value form there.

Open point for the plan/spike: confirm whether generalizing changes how the
**static text** of a body-position backtick literal is escaped (a Go raw string's
text is literal bytes; as a string rendered in HTML text it should be HTML-escaped
like any string — verify this matches, or is a documented, intended refinement of,
current `EmbeddedInterp` body behavior).

## Compatibility

Two behavior changes, both accepted — both instances of gsx claiming the escape
space Go leaves free inside backticks:

1. **A plain Go raw string containing `@{` now interpolates** inside a `.gsx` Go
   region, because bare backticks become interpolating literals. `\@{` keeps a
   literal `@{`.
2. **A plain Go raw string ending in a backslash before its close — `` `…\` `` —
   now parse-errors** (later, a gsx literal), because gsx applies its `\``
   escape convention uniformly to every backtick, so the `\`` reads as an escaped
   backtick rather than a Go-literal backslash. Narrow (only raw strings ending
   in `\`, e.g. `` `\` ``/`` `C:\` ``); workaround `"\\"` or restructure.

Both are source-visible and documented, the same claim-what-Go-leaves-free move
as `|>`. Go raw strings without `@{` and not ending in `\` are unaffected.
(Element/fragment literals introduce no compatibility change — operand-position
`<` was already claimed.)

## Codegen: split + lower at emit time; AST reused

- **Tags:** `ast.Interp.Expr` (and GoBlock code, attr exprs, value-form arms)
  stay strings holding verbatim source; the emit site and its probe run
  `splitGoElements` and lower embedded tags. Minimal AST change; printer
  round-trips verbatim; LSP nav inside embedded tags degrades gracefully.
- **Backtick literals:** reuse `ast.EmbeddedInterp` / `Segments`; permit it as an
  expression value and emit `embeddedValueExpr`'s concatenation (plus optional
  `|> stages`) at the new positions.

## Contexts fall out for free

Every `{ }` / `{{ }}` / `name={…}` / value-form / pipe stage flows through the
same chokepoint, so both features apply uniformly. JS/CSS contexts are gated by
types.

## Risk-gate spike (Task 1)

This touches the **most common construct in the language** — every interpolation
is delimited by these scanners. The bar: **for any fragment with no embedded tag
and no interpolating backtick, delimiting must be byte-identical to today.**
Safety comes from reusing the operand-aware detector the top level already trusts
(`a < b`, `x <= y`, `<-ch` stay untouched) and the existing escape-aware backtick
skip. The spike:

- Implements the unified scanner's boundary + `|>` + tag-span + backtick-span +
  `,`/`:` reporting, behind an `IndexByte(src, '<')` / `IndexByte(src, '` + "`" + `')`
  fast-path so tag-and-backtick-free fragments keep the current fast byte path
  (and same speed) — `goDepth1End` is the hottest parse-time path.
- Runs a **differential harness** over the entire corpus: computed interpolation
  boundaries, pipe splits, and ordered-attrs delimiters must equal the current
  byte scanners exactly for every existing `{ }`.
- Proves the escaped-backtick skip (`` `a\`b` ``) is byte-correct.

Only after the spike is green does the lowering work proceed.

## Scope / effort

- **Parser (the bulk, all the risk):** the unified scanner + reroute
  `goExprEnd`/`goStagesEnd`/`splitPipe`/`composedDelims`/`parenEnd`; fast-path
  guard; backtick-span recognition.
- **Codegen:** split+lower tags at `genInterp`/`genGoBlock`/`ExprAttr`/value-form
  + their probes (reuse element/fragment machinery); allow/emit `EmbeddedInterp`
  as an expression value (reuse `embeddedValueExpr`).
- **Corpus:** cases per position for each feature (interp/GoBlock/attr/value-form/
  pipe × tag-literal and backtick-literal), the `@{`-in-raw-string compatibility
  case, a JS/CSS type-error case, and the scope-capture regressions.
- **Docs:** extend the "as values" guide for both forms; document the one
  compatibility change; drop the "not supported inside interpolation" limitation.

## Prerequisites & relationship

Builds on element literals (#42) and fragment literals (#44), both merged. This
is the unification that makes `<tag>`/`<>` and interpolating backtick literals
uniform Go-expression values everywhere, and collapses the two-scanner divergence
into one.

## Out of scope

- **General Go-expression parsing** — gsx still scans for tags/backticks at
  operand positions; it does not build a Go-expression tree.
- **LSP nav/hover inside embedded tags** — graceful degradation now; a follow-up
  wires the nav bridge into the interp position.
- **Non-`gsx.Node` / non-string embedding** — only tag/fragment literals and
  interpolating backtick literals are recognized; the rest of a fragment stays
  opaque Go.
