# Pipeline as Registered Forward-Application — Design

**Status:** approved design (brainstormed 2026-06-25), ready for an implementation plan.

**Goal:** Make `|>` a registered forward-application operator so that ubiquitous,
context-taking utilities — especially the structpages family (`URLFor`, `ID`,
`IDTarget`) — read as short, subject-first pipelines with the import, the
ambient `ctx`, and the `(T, error)` all handled by gsx:

```gsx
hx-post={ urlFor(ctx, toggle{}, "id", todo.ID) }   // before
hx-post={ toggle{} |> url("id", todo.ID) }          // after
```

**Why this matters:** structpages' call sites are awkward in templates — every
one threads `ctx`, takes the subject in an inner position, and returns an error
the template must swallow. The pipeline already hides errors (implicit
`(T, error)` unwrap); extending it to inject `ctx` and place the subject as the
first real argument removes the remaining ceremony. This is the smallest change
that resolves a recurring itch ("structpages' calls are too awkward") without a
structpages-specific gsx feature.

---

## 1. Background: today's filter contract

`|>` resolves its right-hand side against a **curated filter table** harvested
from registered filter packages (`internal/codegen/filters.go`): `std` plus any
package passed to `gen.WithFilters`, with last-wins precedence. A filter's
pipeline name is `lowerFirst(exportedFuncName)` (e.g. `Upper` → `upper`). There
are exactly two harvested shapes:

- **bare** `func(T) R` — `x |> f` ⇒ `f(x)`
- **curried** `func(Args…) func(T) R` — `x |> f(a)` ⇒ `f(a)(x)` (args first, the
  subject applied last)

Both are *strict single-parameter*: the subject is always the sole argument of
the final application. structpages' functions fit neither:

```go
URLFor  (ctx context.Context, page any, args ...any) (string, error)
ID      (ctx context.Context, v any)                 (string, error)
IDTarget(ctx context.Context, v any)                 (string, error)
```

They are `func(ctx, subject, args…) (R, error)` — ctx first, subject second,
direct (not curried), error-returning.

## 2. The model: a filter is a registered name → function

A *filter* is a registered mapping `name → package.Func`. The registry is what
earns the operator: it gives gsx (a) a short unqualified name, (b) the **import
to inject**, and (c) the **signature**. This is deliberately *not* UFCS (pipe
into any in-scope callable): if a template author would have to write the
qualified call and manage the import themselves, the pipe buys nothing — they
may as well write the call. The curated registry is precisely the
`name → package` map that makes `|>` worth having.

## 3. One calling rule (seed-first)

Replace the two strict shapes with a single rule. For a stage `subject |> name(args…)`
resolving to registered function `Func`:

```
Func( [ctx,] subject, args… )
```

- **ctx injection:** if `Func`'s first parameter type is `context.Context`, gsx
  passes the ambient render `ctx` as that argument. Otherwise no ctx is passed.
- **subject position:** the piped value is inserted as the first non-ctx
  parameter.
- **stage args:** the explicit `name(args…)` arguments follow, positionally;
  a trailing variadic parameter (`args ...any`) absorbs the remainder.
- **error:** a `(R, error)` return rides gsx's existing implicit `(T, error)`
  unwrap (the error propagates out of the enclosing `Render`). No new mechanism.
- **bare** (`x |> f`) is just this rule with zero stage args.

Worked examples:

```gsx
{ name |> upper }            ⇒ std.Upper(name)
{ s |> truncate(80) }        ⇒ std.Truncate(s, 80)
{ toggle{} |> url("id", x) } ⇒ structpages.URLFor(ctx, toggle{}, "id", x)   // ctx + error implicit
{ p.Results |> target }      ⇒ structpages.IDTarget(ctx, p.Results)
{ CommentsList |> id }       ⇒ structpages.ID(ctx, CommentsList)
```

This is the change the user described as "flips our decision of strict single
parameter function": the subject is now positional, not the curried tail, and
the call may take ctx + trailing args.

## 4. Registration API

Two registrations, both already reflection-based, both compiled into the
project's generator binary:

- **`gen.WithFilters(markers ...any)`** — existing whole-package harvest. A
  marker is any value whose `reflect.TypeOf(m).PkgPath()` names the filter
  package (e.g. `std.Pkg`). Names are `lowerFirst(exported)`. (Unchanged, except
  harvested funcs now follow the seed-first rule — see §6.)

- **`gen.WithFilter(name string, fn any)`** — NEW. Registers a single function
  under an explicit short name. gsx reflects `fn`:
  - `runtime.FuncForPC(reflect.ValueOf(fn).Pointer()).Name()` → the qualified
    `package/path.FuncName`, used to emit the import + qualified call;
  - `reflect.TypeOf(fn)` → the signature, used to detect a leading
    `context.Context`, the subject parameter, variadic, and `(R, error)`.

  ```go
  gen.Main(
    gen.WithFilters(std.Pkg),                       // whole-package harvest
    gen.WithFilter("url",    structpages.URLFor),   // explicit aliases
    gen.WithFilter("id",     structpages.ID),
    gen.WithFilter("target", structpages.IDTarget),
  )
  ```

`WithFilter` lets libraries keep idiomatic Go names (`URLFor`, `ID`, `IDTarget`)
while the template vocabulary (`url`, `id`, `target`) is owned by the project's
generator. It works with any function, requiring no gsx-specific code from the
library.

**Config reaches every subcommand for free.** `gen.Main(opts…)` is the entire
CLI — `cmd/gsx/main.go` is literally `func main() { gen.Main() }`, and `Main`
dispatches `generate` / `fmt` / `lsp` / `info` from the *same* options. A
project with custom filters builds its own binary; every subcommand, including
`lsp`, sees the aliases because they are compiled into that one binary. There is
no config file and no possibility of drift between generate and the language
server.

**Editor consequence (documented, not a code change):** the editor must launch
the *project's* generator binary's `lsp` subcommand, not stock
`~/go/bin/gsx lsp`, or the language server won't know the project's aliases.
This is wired by the `gsx init` scaffold (see [[gsx-init-dev-loop-scaffold]]).

## 5. Resolution & precedence

- The RHS of `|>` is resolved against the **filter table only**, not local
  scope. A local variable named `url` does not shadow the `url` filter; the pipe
  stage name is always a registered filter name. (This is unchanged from today —
  filters are a namespace distinct from interpolation identifiers.)
- `WithFilter` aliases and `WithFilters`-harvested names share one table with
  the existing **last-wins** rule (a later registration shadows an earlier
  same-named one). Explicit `WithFilter` aliases are appended after package
  harvests in `gen.Main` option order, so an alias can intentionally override a
  harvested name.
- A stage naming an unregistered filter is a codegen error (unchanged), now
  worded to mention both registration paths.

## 6. emit ≡ probe

The filter's signature is fully known at generation time (from go/types for
harvested packages, from reflection for `WithFilter` aliases), so both the
type-resolution skeleton (probe, `_gsxrt`) and the emitted code (`gsx`) build
the **identical** call — `pkg.Func(ctx, subject, args…)` plus the same injected
import. No second type-check and no new resolution path; the central
emit ≡ probe invariant holds by construction.

## 7. std migration (breaking, accepted)

The three curried std filters convert to seed-first:

| before (curried)                          | after (seed-first)                  |
|-------------------------------------------|-------------------------------------|
| `func Truncate(n int) func(string) string`| `func Truncate(s string, n int) string` |
| `func Join(sep string) func([]string) string` | `func Join(s []string, sep string) string` |
| `func Default(fallback string) func(string) string` | `func Default(s, fallback string) string` |

Template usage is unchanged in surface (`{ s |> truncate(80) }`); only the Go
signatures and the lowering change. The bare filters (`Upper`, `Lower`, `Trim`)
already match the new rule (zero-args) and need no change.

This is a breaking change for any **user-authored** curried filter packages too:
a `func(Args…) func(T) R` filter no longer lowers as `f(a)(x)`. We are early in
development and accept the break; the migration is mechanical (uncurry the
signature, subject becomes the first parameter). The codegen should detect a
filter whose registered function returns a function whose sole parameter matches
the subject (the old curried shape) and emit a **clear diagnostic** pointing at
the new seed-first contract, rather than silently miscompiling.

## 8. Out of scope

- **Non-subject helpers** (e.g. `structpages.Ref(name string)`, which produces a
  page reference used *as* the subject of `url`) have nothing to pipe into. They
  stay normal calls written in the subject position with an ordinary import in
  the `.gsx` package block: `{ structpages.Ref("home") |> url }`. The common
  case is a page struct (`dashboardPage{} |> url`), so this is rare. A future
  "import-injected template function" registration (`gen.WithFunc`) could make
  `ref` unqualified, but it is deliberately deferred.
- **The structpages `WithFilter` wiring and the `url`/`id`/`target` vocabulary**
  live in structpages projects (their `gen.Main`), not in gsx. gsx ships only
  the mechanism (`WithFilter`, the seed-first rule).
- **The `AdminShellWith(body gsx.Node)` example fix** (use the explicit
  `Children` prop instead of a bespoke `body` param) is structpages example
  polish, tracked separately from this language change.

## 9. Edges, errors, risks

- **ctx availability:** pipelines occur only inside component bodies
  (interpolation/attribute markup), where the render `ctx` is always in scope, so
  ctx injection always has a value to pass. (No top-level/ctx-less pipeline
  position exists.)
- **subject type checking:** gsx type-resolves the subject expression and checks
  assignability to `Func`'s subject parameter. For `any` parameters (URLFor's
  `page any`) anything is accepted; for typed parameters (`Truncate`'s `string`)
  a mismatch is a positioned codegen error.
- **arity/variadic:** stage args fill the parameters after the subject; too many
  / too few (accounting for variadic) is a positioned codegen error citing the
  resolved signature.
- **ctx as real data:** a filter that genuinely wants a non-ambient
  `context.Context` as its first data argument cannot exist under this rule (the
  first `context.Context` param is always the injected ctx). This is an accepted
  constraint — template filters do not take user-supplied contexts.
- **reflection accuracy:** `runtime.FuncForPC` returns the declaring function's
  fully-qualified name for a plain top-level function value; method values and
  closures are not valid `WithFilter` targets and must be rejected with a clear
  error at registration time.

## 10. Testing (per [[gsx-syntax-change-test-coverage]])

Every behavior ships txtar corpus cases plus unit coverage:

- **corpus (codegen + render):** seed-first bare (`upper`), seed-first with args
  (`truncate`), ctx-injected + error-unwrapped alias (`url` with variadic args,
  `id`, `target`), in each interpolation context (text, attribute, `<script>`,
  JS-attribute) to confirm uniform lowering; a `WithFilter`-aliased pipeline
  end-to-end (generate + run).
- **emit ≡ probe:** golden assertions that the probe skeleton emits the same
  `pkg.Func(ctx, subj, args)` call as emit.
- **errors (corpus diagnostics):** unregistered filter; subject type mismatch;
  wrong arg count; a still-curried filter function (the migration diagnostic);
  a `WithFilter` target that is a method value/closure.
- **unit:** the reflection resolver (qualified-name + signature extraction,
  ctx/variadic/error detection), and the std seed-first signatures.

## 11. Future (not in this spec)

- `gen.WithFunc(name, fn)` — import-injected unqualified template functions for
  non-subject helpers (`ref`).
- Method-value / generic-function filter targets, if a real need appears.
