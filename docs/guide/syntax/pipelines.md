# Pipelines

Pipelines transform a value through a chain of named filters using the `|>` operator. Each stage takes the value from the previous stage as its first argument, applies the named function, and passes the result on. The final value is then rendered with the same context-aware escaping that applies to any other interpolation.

## Filters & chaining

The pipeline syntax is `{ value |> filter }` for a single stage, or `{ value |> f1 |> f2 }` for a chain. Each filter is a registered Go function applied in left-to-right order. A stage can also take additional arguments: `{ value |> truncate(10) }` or `{ count |> format("%d items") }`.

gsx ships a built-in filter library (the `std` package) that is always available without any configuration:

| filter | description |
|---|---|
| `upper` | maps all Unicode letters to upper case |
| `lower` | maps all Unicode letters to lower case |
| `trim` | removes leading and trailing whitespace |
| `truncate(n)` | cuts to at most `n` runes |
| `join(sep)` | joins a `[]string` with `sep` |
| `default(fallback)` | returns `fallback` when the value is the empty string |
| `format(spec, rest…)` | like `fmt.Sprintf` with the piped value as the first verb |

To register your own named filter, add it to the `[filters]` table in `gsx.toml` — see [Configuration → `[filters]`](../config#filters-named-pipeline-filters). To register every exported function from a package at once, list the package path in `filterPackages`. In both cases the function must have the seed-first shape: the piped value is the first parameter (after an optional `context.Context`), and extra stage arguments follow.

<!--@include: ./_generated/pipelines/010-pipelines-filters.md-->

`name |> trim` strips the surrounding whitespace; `|> upper` then maps every letter to upper case. The two stages are lowered to nested calls — `_gsxstd.Upper(_gsxstd.Trim(name))` — and the result is HTML-escaped as it is written to output.

## Filter arguments

A filter stage can take extra arguments by appending them in parentheses after the filter name: `{ value |> truncate(10) }` or `{ count |> format("%d comments") }`. The piped value is always the first argument; the parenthesised values are appended after it. Stages with and without arguments can be freely mixed in a chain.

<!--@include: ./_generated/pipelines/020-filter-arguments.md-->

`s |> trim |> truncate(5)` strips surrounding whitespace first, then cuts to five runes — lowered to `_gsxstd.Truncate(_gsxstd.Trim(s), 5)`. `count |> format("%d comments")` passes `count` as the first `Sprintf` verb and the string literal as the format spec.

## `(T, error)` auto-unwrap

A filter that returns `(T, error)` — or any bare function call `{ f(x) }` with that return shape — is automatically unwrapped. There is no special syntax needed: the generated code assigns the result, checks the error, and if it is non-nil, returns it from `Render`. The caller receives the error and can handle it (log, serve a 500, etc.). See [Interpolation → `(T, error)` unwrap](./interpolation) for a worked example.

To handle an error inline, use a raw-Go init statement: `{ if v, err := f(); err != nil { … } else { … } }`.

The `?` try-marker syntax (e.g., `|> upper?`) is not supported — gsx reports an error — auto-unwrap makes it unnecessary.

## Filters that can fail at any stage

The `(T, error)` unwrap above is not limited to the final stage of a pipeline. Any stage — first, middle, or last — can be a filter matching the contract documented on `std`: `func([ctx context.Context,] subject T, args...) (R, error)`.

```gsx
<p>{ csv |> parse |> join(" ") }</p>
```

If `parse` has that shape, its stage lowers to a hoisted temporary with an error check, and the next stage consumes the unwrapped value — equivalent to:

```go
v, err := parse(csv)
if err != nil {
    return err
}
// join(v, " ") continues the chain — its result is what gets rendered
```

When a stage's error is non-nil, the chain **halts right there**: later stages are never invoked, and the error returns from the component's render — the same semantics as the single-expression `(T, error)` unwrap (see [Interpolation → `(T, error)` unwrap](./interpolation)). This holds in every context a pipeline can appear: text, attributes, composable `class`/`style` parts, spread values, child-component props, and conditional-attribute branches — including a composable `class` part nested inside a component's conditional-attribute branch.

To handle the error instead of propagating it, skip the pipeline for that stage and fall back to the same explicit form: `{ if v, err := parse(csv); err != nil { … } else { … } }`. The `?` try-marker stays rejected at every stage, not just the last.

## Pipelines per context

A pipeline can appear anywhere a `{ expr }` interpolation is valid — text content, plain attributes, URL attributes, and so on. Importantly, pipelines do **not** bypass context-aware escaping: the value produced by the final stage is still sanitized for the context it sits in.

In particular, a URL-context attribute (`href`, `src`, `action`, and the htmx method attributes) always scheme-sanitizes its value. A dangerous scheme like `javascript:` is replaced with `about:invalid#gsx` even when the value was first passed through a pipeline. Trimming whitespace does not make a dangerous URL safe.

<!--@include: ./_generated/pipelines/030-pipelines-in-attribute-context.md-->

`u |> trim` removes the surrounding whitespace, but `href` is a URL-context attribute — the scheme check runs on the trimmed value and rejects `javascript:`, writing `about:invalid#gsx` instead. This also means a safe, clean URL is correctly passed through: `"  /p?q=a&b  " |> trim` produces `/p?q=a&amp;b` (the `&` is attribute-escaped, the path itself is fine).
