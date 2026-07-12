# Fold-Path Contextual Holes Design (#92, #93)

## Goal

Make an embedded attribute containing `@{ }` holes render identically whether
the element takes the inline path or PR #91's multi-spread fold path. This
includes JavaScript and CSS embedded literals (#92) and mixed non-tilde generic
type parameters such as `T string | int` (#93).

## Current gap

The inline emitter writes each hole directly to `Writer` through its contextual
sink. A folded element must instead produce an `Attrs` value first, then the
shared `Spread` leaf performs HTML attribute escaping. `composeBag` therefore
needs the contextual escaping result as a Go string expression, without writing
and without applying HTML escaping yet.

Today only embedded text holes can produce that intermediate string. JS/CSS
holes are rejected, and `holeStringExpr` rejects `catAnyMixed` because the only
existing mixed-type implementation is the writing method `Writer.AttrAny`.

## Chosen architecture

Add small pure runtime helpers used by generated code:

```go
func EscapeJSVal(v any) string
func EscapeJSStr(s string) string
func EscapeJSTmpl(s string) string
func EscapeJSRegexp(s string) string
func AttrString(v any) (string, error)
```

The four JS helpers expose exactly the first, JavaScript-context stage already
used by `JSValAttr`, `JSStrAttr`, `JSTmplAttr`, and `JSRegexpAttr`; they do not
HTML-escape. `AttrString` exposes `anyRenderString`'s conversion with an error
instead of mutating a writer. Existing writer methods delegate to these helpers
so there is one implementation per rule, not a compatibility copy.

CSS holes use the existing `FilterCSS(string) string`, which already returns
the CSS-context-filtered value without HTML escaping. No new CSS helper or
runtime type is needed.

`composeBag` lowers a hole-bearing embedded literal into a string concatenation:

- static segments remain raw source text;
- JS holes become the appropriate `EscapeJS*` call selected by the parsed
  `JSCtx`;
- CSS holes become `FilterCSS(...)`, except `RawCSS`, which preserves the same
  explicit bypass as the inline emitter;
- text holes continue through `holeStringExpr`;
- `catAnyMixed` text holes call `AttrString`, hoist `(string, error)`, and return
  the render error before evaluating later contributors.

The assembled string becomes the bag entry's `Value`. The existing `Spread`
leaf then performs HTML attribute escaping once. Codegen must never use an
`*Attr` helper while assembling the bag, because that would HTML-escape once in
the helper and again at the leaf.

## Evaluation and errors

PR #91's `materializePrior` boundary remains authoritative. Before any hole
lowering that can hoist a pipeline, tuple, renderer, or `AttrString` error,
earlier contributors are materialized. Later contributors do not run after an
error. Untaken conditional branches remain lazy.

`AttrString` returns an error for a dynamic type outside the same total set
accepted by `AttrAny`; it never silently formats an unsupported value. For
`catAnyMixed`, the compiler has already proven every possible concrete type is
accepted, but generated code still propagates the error rather than assuming
that invariant at runtime.

## Security invariants

1. JS/CSS contextual escaping happens before HTML attribute escaping.
2. HTML attribute escaping happens exactly once, at `Spread`.
3. `RawJS` and `RawCSS` bypass only their language-specific filter and still
   receive HTML attribute escaping at the leaf.
4. Static literal fragments remain byte-identical to the inline path.
5. Renderer and `(T, error)` handling occurs before contextual escaping.
6. No new `packages.Load` call or runtime dependency is introduced.

## Testing

### Runtime helper parity

Table tests compare each new pure JS helper with the existing writer method
before its HTML stage, including quotes, angle brackets, ampersands, Unicode
separators, `RawJS`, booleans, numbers, nil, and hostile breakout payloads.
`AttrString` table tests mirror every `AttrAny`-accepted category and its error.

### Canonical corpus

Add fold-path cases and matching inline controls for:

- JS value, string, template-literal, and regexp hole contexts;
- `RawJS` in a JS-value hole;
- CSS normal and `RawCSS` holes;
- renderer and `(T, error)` holes;
- `Field[T string | int]` instantiated once with `string` and once with `int`;
- a failing conversion/renderer after an earlier marker spread and before a
  later marker spread, pinning source evaluation and short-circuiting.

Every folded case uses at least two spreads so `composeBag` is exercised. Each
inline/fold pair must render byte-identical output. Hostile payload cases must
assert the complete safe output, not merely absence of one substring.

### Issue #94 fuzz coverage

Issue #94 is a separate mechanical commit. Change `foldValAlphabet` and decoded
`Attr.Value` data from `string` to `any`, add a leading/trailing-whitespace class
token and boolean values, and seed both. Update the independent reference to
retain scalar `any` values while type-asserting strings only for class/style
joining. This expands coverage without changing production behavior.

## Documentation and scope

Update `docs/ROADMAP.md` to close #92, #93, and #94. No syntax, parser, formatter,
tree-sitter, VS Code, or website grammar changes are required. The runtime stays
standard-library-only.
