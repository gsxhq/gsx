# Pipelines

Use `|>` to pass a value through named filters from left to right. The final
value is written with the same context-aware rules as any other expression.

## Chain filters

Add one or more stages after an expression.

<!--@include: ./_generated/pipelines/010-pipelines-filters.md-->

`name |> trim |> upper` trims `name`, then passes that result to `upper`.
Built-in filters include `upper`, `lower`, `trim`, `truncate`, `join`,
`default`, `printf`, `urlquery`, and `dataURL`. Custom names come from
[configured filters](../config.md#filters-named-pipeline-filters).

`default` preserves the value's type and returns the first non-zero choice:

```gsx
{count |> default(1)}
{label |> default(shortLabel, "Untitled")}
```

## Pass filter arguments

Put additional arguments in parentheses. The piped value is always the first
argument, followed by the values you write.

<!--@include: ./_generated/pipelines/020-filter-arguments.md-->

Stages with and without arguments can be mixed in the same chain.

## Pipe a whole interpolated literal {#whole-literal-pipelines}

Put `|>` after an `f` literal to filter the complete interpolated string.

<!--@include: ./_generated/pipelines/040-whole-literal-pipelines.md-->

Compare the placement:

```gsx
{f`item-@{ id |> upper }`} // filters only id
{f`item-@{id}` |> upper}   // filters "item-" and id together
```

The whole-literal form also works in a braced attribute value:
`` title={f`item-@{id}` |> upper} ``. Braces are required there; in the direct
`` title=f`item-@{id}` `` form, pipelines stay inside `@{}` holes. See
[Interpolating attribute literals](./attributes.md#interpolating-attribute-literals).

## Errors

Any pipeline stage may return `(T, error)`. A nil error passes the `T` value to
the next stage; a non-nil error returns from `Render` immediately, so later
stages do not run.

Do not add a try marker: `value |> parse?` is an error because propagation is
automatic. To handle a failure locally, use an explicit Go `if` statement
instead of that stage; see [Control flow](./control-flow.md#init-statements).

## Context-aware output

Pipelines transform values; they do not change the safety rules of the place
where the result is written.

<!--@include: ./_generated/pipelines/030-pipelines-in-attribute-context.md-->

Here `trim` runs first, then the URL sink rejects the dangerous scheme.
`urlquery` is useful for encoding one query component, while `dataURL` builds a
base64 `data:` URL. Neither grants trust: the final URL is still checked for
its sink. See [Escaping](./escaping.md#url-attributes) for the URL rules and
trusted-value helpers.
