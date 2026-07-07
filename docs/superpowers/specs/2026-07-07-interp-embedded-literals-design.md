# One tag-aware Go-expression scanner: `<tag>`/`<>` and prefixed interpolating string literals (`f`/`js`/`css`) as values everywhere

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
2. **Prefixed interpolating string literals** — `` f`hello @{name}` `` (or
   `f"hello @{name}"`) — an opt-in `f`/`js`/`css` prefix that turns a string
   literal into a gsx interpolating literal (`@{}` holes, JS-like escapes), while
   a bare `"…"`/`` `…` `` stays plain Go:
   ```gsx
   var greeting = f`hello @{name}`          // a string value, anywhere
   f(f`id-@{n}`)                            // as a call argument
   { emphasize(f`@{label}!`) }              // inside an interpolation
   js"const t = `hi ${x}`"                  // "-delimiter escape-hatch for backtick content
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

## Feature 2: prefixed interpolating string literals (`f`, `js`, `css`) as values everywhere

**Prefix, not the delimiter, does the interpolation.** An `f`/`js`/`css` prefix
in front of a string literal turns it into a gsx **interpolating literal** — one
with `@{ expr }` holes and JS-like escapes. A **bare** `"…"` or `` `…` `` (no
prefix) stays an ordinary Go string literal, untouched. This is the key to zero
compatibility cost: gsx never reinterprets Go's own string syntax; it claims only
the `IDENT`-immediately-before-a-string sequence (`f"…"`, `` f`…` ``, `js"…"`, …),
which is never valid Go.

**Two delimiter forms, semantically identical.** Each prefix accepts either
delimiter — `f"…"` and `` f`…` `` behave the same; you pick whichever quote your
content does *not* contain, exactly like choosing `'` vs `"` in JS/Python. The
double-quote form is the escape-hatch for **content that contains backticks** —
which is common precisely for `js`/`css`, since JS template literals *are*
backticks: `js"const t = `hi ${x}`"` writes clean where the backtick-delimited
form would force escaping every inner backtick.

**Always-escaping, JS-like** (unlike Go, where `"…"` escapes and `` `…` `` is
raw): both delimiters of an f-literal process the same escape set —
`\n \t \r \\`, the delimiter (`\"` in `f"…"`, `` \` `` in `` f`…` ``), and `\@{`
for a literal `@{`. So `` f`a\nb` `` yields a newline; it is *not* a Go raw
string.

**Value & escaping.** An f-literal evaluates to a **`string`** — its literal text
segments concatenated with its `@{expr}` holes (each via `embeddedValueExpr`,
already built for the piped `EmbeddedInterp` path). Escaping of the *result* stays
contextual at the use site, like any interpolation: HTML-escaped as text,
attr-escaped in an attribute, unescaped as a plain Go `string` arg.

**Reuse & new work.** `js`/`css` (`EmbeddedAttr`) and the body `EmbeddedInterp`
already implement `@{}` holes + `\`` /`\@{` escapes; this (a) adds `f` as a third
prefix and the `"…"` delimiter variant to all three, (b) lets an f/js/css literal
appear as a first-class value in any Go-expression position (recognized as an
escape-aware span by the unified scanner), and (c) emits its `string` value there.

**One rule, applied everywhere (behavior change to confirm).** With prefixes as
the interpolation trigger, **bare `` `…` `` stops being an interpolating
`EmbeddedInterp` in body/attr position too** — interpolation requires `f`/`js`/`css`
everywhere; a bare backtick is always a literal. This is a change to the current
body/attr bare-backtick behavior (today a bare `` `…@{x}…` `` in a component body
interpolates), chosen for one teachable rule ("bare = literal, `f`/`js`/`css` =
interpolate") over "bare interpolates in body but not in expressions." Existing
`js`/`css` attr usage is unaffected; bare-backtick body usage migrates to `f`.

## Compatibility

**No Go compatibility break.** Because interpolation is opt-in behind an
`f`/`js`/`css` prefix, bare Go string literals — `"…"` and `` `…` `` — keep their
exact Go meaning everywhere, including the `@{`-containing and `` `…\` `` cases
that an earlier "bare backticks interpolate" design would have broken. gsx claims
only `f"…"` / `` f`…` `` / `js"…"` / … (an `IDENT` immediately before a string,
never valid Go), the same claim-what-Go-leaves-free move as `|>`. Element/fragment
literals likewise introduce no break (operand-position `<` was already claimed).

The **one** behavior change is internal to gsx, not to Go: a bare `` `…@{x}…` ``
in **body/attr** position no longer interpolates (it did before) — interpolation
now requires the `f` prefix there too (see Feature 2's "one rule"). `js`/`css`
attr literals are unchanged.

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
