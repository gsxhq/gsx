# Renderers registry — type-directed value rendering

**Date:** 2026-07-11
**Status:** Draft (approved direction; pending user spec review)

## Problem

A hole `{x}` renders only if its static type classifies as renderable (string,
numeric kinds, bool, `[]byte`, `fmt.Stringer`, `gsx.Node`). Wrapper types from
third-party packages — `pgtype.Text`, `pgtype.Timestamp`, `sql.NullString`,
`decimal.Decimal`, `uuid.UUID` — are none of these. `pgtype.Text.String` is a
*field*, not a method; the only stdlib interface pgtype wrappers implement is
`driver.Valuer`.

The one-learning templ→gsx migration pays for this at every render site:

```gsx
<SourceTypeBadge sourceType={item.SourceType.String}/>   // .String reach
{ props.Filter.DateFrom |> date }                        // pipe for every hole
```

The project cannot add methods to types it does not own, so `fmt.Stringer` is
not a way out. We want `{item.SourceType}` to just work — with the project, not
gsx, deciding what "work" means (NULL handling, date formats).

## Decision

A **`[renderers]` table in `gsx.toml`**: a compile-time registry mapping a
fully-qualified named type to a fully-qualified rendering func.

```toml
[renderers]
"github.com/jackc/pgx/v5/pgtype.Text" = "github.com/tespkg/one-learning/ds/filters.PgText"
"github.com/jackc/pgx/v5/pgtype.Timestamp" = "github.com/tespkg/one-learning/ds/filters.PgDateTime"
```

```go
// Ordinary user Go — NULL semantics are the project's decision.
func PgText(t pgtype.Text) string { return t.String }
func PgDateTime(ts pgtype.Timestamp) (string, error) { ... }
```

At the **render boundary**, when the value's type is identical to a registered
type, codegen wraps the expression in the renderer call and classifies the
renderer's *result* type through the existing rules. `{item.SourceType}` emits
`PgText(item.SourceType)` (via the aliased-import machinery) — semantically an
implicit final pipe stage.

### Why not the alternatives

- **Runtime `driver.Valuer` support** (`catValuer` + dispatch on
  `driver.Value`): automatic for all pgtype/`sql.Null*` types, but `Value()`
  means "value for the database", not "value for display" — `pgtype.Timestamp`
  unwraps to `time.Time` whose `String()` is not a UI format, `Timestamptz`
  infinity modifiers error at render time, and any type implementing Valuer
  for storage reasons silently becomes renderable with no per-type opt-out.
  `html/template` deliberately does not unwrap Valuer.
- **Runtime registry** (`gsx.RegisterRenderer[T](...)`): requires
  `map[reflect.Type]func(any)` dispatch, global mutable state, and codegen
  could not even classify a hole as renderable without knowing registry
  contents. Not type-safe; rejected.
- **gsx-defined render interface**: users cannot add methods to third-party
  types, so it only helps types that could already implement `fmt.Stringer`.
- **App-side sqlc type overrides**: would churn every non-UI `.String`/`.Valid`
  field access across the application to fix a templating concern.

The config registry is fully type-safe because resolution happens at codegen
time: the emitted `.x.go` contains a direct, statically-typed call, and
`go build` (plus generation-time validation below) enforces the signature.

## Semantics

**Boundary.** A renderer applies wherever gsx renders a *value* into output:
interpolation holes in text, attribute-value, style, and script positions, and
interpolated string literals (f\`...\`, js\`...\`, css\`...\`). It applies to
the **result of a pipe chain** the same as to a bare hole (`{x |> f}` consults
the registry on `f`'s result type). It does **not** apply to component
arguments, child-prop values, or any other plain Go position — passing a
`pgtype.Text` to a component param of type `pgtype.Text` is ordinary Go and
stays untouched.

**Registration always wins.** If a type is registered, its renderer is used
even when the type would classify renderable on its own (e.g. it also has a
`String()` method). One rule, no fallback tiers: the project explicitly chose
the display form. Unregistered types classify exactly as today.

**Matching is go/types identity** on the concrete named type. Pointer types
are distinct (register `*T` separately if wanted). Type-parameter holes do not
consult the registry in v1 — they classify via the existing type-param rules
(`catAnyMixed` etc.); a hole whose type is a type param instantiated with a
registered type is out of scope for now and documented as such.

**Apply once, never chain.** The renderer's result type must classify
natively renderable. A renderer whose result type is itself a registered type
(including its own parameter type) is a **generation-time diagnostic**, not a
second application. This keeps the rule statable in one sentence and avoids
cycle analysis.

**Security is untouched by construction.** The renderer's output enters the
same per-context classification and sanitization a pipe filter's output does
today: URL holes remain whole-value sanitized, script/style holes keep their
context escaping. No new escaping surface is introduced.

**Runtime `any` values are out of scope.** Values that reach the runtime as
`any` (attr bags / `gsx.Attrs`, runtime type-param dispatch through
`anyRenderString`) follow runtime rules and never see renderers — the registry
is static. Documented as a limitation.

## Signatures and errors

A renderer func must be:

```go
func(T) R
func(T) (R, error)
```

where `T` is the registered type and `R` classifies renderable. The `error`
form rides the existing pipe-filter error path (errors at any stage, PRs
#29/#30 machinery) — same capture, same propagation, both harvest paths.

Variadic, extra parameters, methods, and generic funcs are rejected in v1
(diagnostic). Renderers are deliberately simpler than filters: no template-side
arguments exist to bind.

## Generation-time validation and diagnostics

All validated during generation with positioned diagnostics (not raw Go
compile errors), following the filter-resolution precedent:

1. Renderer func not found / unexported / wrong shape (not `func(T) R` or
   `func(T) (R, error)`).
2. Renderer parameter type not identical to the registered type.
3. Renderer result type not renderable.
4. Renderer result type is itself registered (chain attempt), including
   self-chain (`func(T) T`).
5. Registered type string that does not parse as `<pkgPath>.<TypeName>`
   (config-load error, mirroring `[filters]` strict decoding).

## Config plumbing

- New field on `tomlConfig`: `Renderers map[string]string \`toml:"renderers"\``,
  strict-decoded like `[filters]`.
- Three-layer precedence as everywhere: option (`gen.WithRenderers`) > env >
  config.
- **Folds into `computeKey`** — renderers change generated output (unlike
  `[formatter]`/`[dev]`).
- Resolution (type-checking renderer packages) **rides the same
  `packages.Load` as `ResolveFilters`** — renderer packages join the filter
  packages in the one existing load; no additional Load is introduced
  (packages.Load is expensive; this is a hard requirement).
- Emission uses the `_gsx*` aliased-import machinery, need-tracked like filter
  imports.

## Testing

Corpus (`internal/corpus/testdata/cases/`), per the syntax-change rule — one
case per context even though this is config-driven, because classification
differs per context:

- text hole, attribute hole, URL-attribute hole (sanitization pinned), style
  hole, script hole, interpolated-literal hole — each rendering a registered
  type.
- pipe-chain result of a registered type (`{x |> f}` where `f` returns the
  registered type).
- `(R, error)` renderer with render-error capture pinned.
- registration-wins: registered type that also implements `fmt.Stringer`.
- negative: component argument of a registered type (renderer NOT applied).
- diagnostics: unrenderable result, chain attempt, self-chain, bad signature,
  unknown func.

Unit tests in `codegen` for matching (identity, pointer distinctness,
type-param exclusion) and in `gen` for config decoding + computeKey folding.

## Docs and siblings

- `docs/guide/config.md`: new `[renderers]` section with the pgtype recipe as
  the worked example.
- **No syntax change** — tree-sitter-gsx, vscode-gsx, and the CodeMirror
  grammar are unaffected. Playground unaffected (registry is per-project
  config).
- `docs/ROADMAP.md` updated.

## Follow-ups (not in scope)

- LSP hover on a hole showing the applied renderer (`rendered via
  filters.PgText`).
- A documented pgtype converter recipe or a `preset = "pgx"` à la the htmx
  preset.
- Type-param holes consulting the registry when the type set is a single
  registered named type.
