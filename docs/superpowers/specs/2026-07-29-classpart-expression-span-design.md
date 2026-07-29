# ClassPart expression-only source span

Date: 2026-07-29. Status: approved design, pre-implementation.

## Problem

`ast.ClassPart` exposes two different source regions without making their
relationship explicit:

- `Pos()` through `End()` is the whole comma-delimited class/style
  contribution. It can include leading and trailing trivia, pipeline stages,
  and a `: cond` guard.
- `ExprPos` is the first byte of the contribution's expression, specifically
  the pipeline seed.

There is no corresponding expression endpoint. A source-rewriting tool can
therefore combine `ExprPos` with the whole-node `End()` and silently replace
pipeline stages or a condition along with the expression. The rewritten file
can remain valid GSX while changing rendering semantics.

The parser already maintains a stronger invariant: `Expr` is the exact,
trimmed source byte sequence beginning at `ExprPos`. The LSP relies on
`ExprPos + len(Expr)` to navigate that region.

## Decision

Keep the existing whole-node `Pos()` and `End()` semantics and add an
expression-only endpoint:

```go
func (p *ClassPart) ExprEnd() token.Pos
```

For a non-nil part with a valid `ExprPos`, `ExprEnd` returns:

```go
p.ExprPos + token.Pos(len(p.Expr))
```

`token.Pos` and `len(string)` are both byte-based here, so this is exact for
UTF-8 source as well as ASCII. If `p` is nil or `ExprPos` is invalid,
`ExprEnd` returns `token.NoPos`.

This gives source tools an explicit half-open expression range:

```go
[part.ExprPos, part.ExprEnd())
```

That range covers only the expression seed. It excludes whitespace before a
pipeline operator, all pipeline stages, whitespace before a guard, and the
entire `: cond` guard.

## AST documentation

Expand `ClassPart`'s documentation to state:

- `Pos()` and `End()` bound the complete contribution between comma
  delimiters, including its surrounding trivia and any stages or guard.
- `ExprPos` and `ExprEnd()` bound only `Expr`, the pipeline seed.
- CSS-literal and value-form parts have no expression span and return
  `token.NoPos` from `ExprEnd()`.

The whole-node span remains useful for diagnostics, inspection, and replacing
an entire contribution. Changing it to an expression span would violate that
role and leave CSS-literal and value-form parts without coherent node
boundaries.

## Rewrite policy

Do not add an `IsPlain` or equivalent helper. Whether a transformation can
preserve or accept a condition, pipeline, CSS literal, or value form depends on
that transformation's semantics. GSX provides exact structure and positions;
the rewriting tool decides which variants it supports.

In particular, a tool that replaces only the seed can preserve a guard or
pipeline structurally by using `ExprPos` and `ExprEnd()`. A tool whose
replacement is incompatible with those constructs should continue to reject
them explicitly.

## Testing

Add focused AST and parser position tests:

1. `ExprEnd()` returns `token.NoPos` for a nil receiver and an invalid
   `ExprPos`.
2. A valid synthetic part returns `ExprPos + len(Expr)`, including for
   multibyte UTF-8 source.
3. Parsed plain, guarded, pipelined, guarded-and-pipelined, and multiline
   contributions satisfy:

   ```go
   source[ExprPos:ExprEnd()] == part.Expr
   ```

4. The guarded and pipelined cases also pin that `End()` remains beyond
   `ExprEnd()`, proving the whole-node and expression-only spans are distinct.
5. CSS-literal and value-form parts return `token.NoPos`.

This is an AST API addition only. It changes no syntax, parser acceptance,
formatting, code generation, or rendering, so it needs no semantic or
formatter corpus update and no sibling grammar/editor changes.

Verification runs the focused AST/parser tests, `gopls check -severity=hint`
on changed Go files, `make check`, `make lint`, and the authoritative
`make ci`.

## Alternatives rejected

### Store an `ExprEnd token.Pos` field

The parser would have to populate and every AST copier/canonicalizer would have
to preserve or clear redundant state. It is no more accurate than the existing
byte-faithful `ExprPos` plus `Expr` invariant and can drift if a synthetic AST
is assembled incorrectly.

### Change `ClassPart.Pos()` or `End()`

Making the node span expression-only would remove the range of the actual
class/style contribution, break existing consumers, and provide poor semantics
for CSS-literal and value-form parts. It would also not teach callers the
important distinction between replacing a whole node and replacing one
subexpression.

### Redesign expression spans across the public AST

Several AST nodes carry source fragments, but issue #174 demonstrates a
specific silent-corruption path for `ClassPart`. A cross-AST range redesign is
larger public surface with no current requirement and can be considered
separately if more source-rewriting consumers emerge.
