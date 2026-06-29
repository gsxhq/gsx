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

## `(T, error)` auto-unwrap

A filter that returns `(T, error)` — or any function call with that return shape used directly in a `{ expr }` interpolation — is automatically unwrapped by the code generator. There is no special syntax needed: the generated code assigns the result, checks the error, and if it is non-nil, returns it from `Render`. The caller of `Render` receives the error and can handle it (log, serve a 500, etc.).

The `?` try-marker syntax (e.g., `|> upper?`) is not supported and will produce a compile error — auto-unwrap makes it unnecessary.

<!--@include: ./_generated/pipelines/020-pipeline-t-error-unwrap.md-->

`greet(name)` returns `(string, error)`. The code generator rewrites this to:

```go
_v, _err := greet(name)
if _err != nil {
    return _err
}
// write _v as text
```

The same auto-unwrap applies when a registered pipeline filter returns `(T, error)`: no extra syntax is needed at the call site.

## Pipelines per context

A pipeline can appear anywhere a `{ expr }` interpolation is valid — text content, plain attributes, URL attributes, and so on. Importantly, pipelines do **not** bypass context-aware escaping: the value produced by the final stage is still sanitized for the context it sits in.

In particular, a URL-context attribute (`href`, `src`, `action`, and the htmx method attributes) always scheme-sanitizes its value. A dangerous scheme like `javascript:` is replaced with `about:invalid#gsx` even when the value was first passed through a pipeline. Trimming whitespace does not make a dangerous URL safe.

<!--@include: ./_generated/pipelines/030-pipelines-in-attribute-context.md-->

`u |> trim` removes the surrounding whitespace, but `href` is a URL-context attribute — the scheme check runs on the trimmed value and rejects `javascript:`, writing `about:invalid#gsx` instead. This also means a safe, clean URL is correctly passed through: `"  /p?q=a&b  " |> trim` produces `/p?q=a&amp;b` (the `&` is attribute-escaped, the path itself is fine).
