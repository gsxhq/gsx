# Control flow

Use Go control flow inside `{ … }` to decide which markup renders. Conditions, variables, and scopes follow normal Go rules.

## If / else

An `if` block renders its markup when the condition is true. Add `else` for the alternative; without it, a false condition renders nothing.

<!--@include: ./_generated/control-flow/010-if-else.md-->

## Range

A `for … range` block renders its markup once for each value. The loop variables are in scope inside the block.

<!--@include: ./_generated/control-flow/020-loops-over-lists.md-->

Any value supported by Go's `range` can be used here.

## Switch

A `switch` block renders the matching `case`, or `default` when no case matches.

<!--@include: ./_generated/control-flow/030-switch.md-->

Cases use normal Go syntax, including comma-separated values.

## Init statements

An `if` can declare values before its condition. Those values remain in scope through every branch, which is useful for checking a result and its error together.

<!--@include: ./_generated/control-flow/040-init-statement.md-->

The same form works for map lookups, type assertions, and other expressions that return multiple values.

## Whitespace in control-flow bodies

Whitespace inside `{ if }`, `{ for }`, and `{ switch }` bodies follows the same
whitespace rule as element bodies: whitespace *between* content is preserved (a
run with a newline collapses away; an inline run collapses to one space).
Whitespace *immediately inside* the control-flow braces is ignored, like the
interior of `{ expr }`. To keep a separator space next to a conditional, put it
in the surrounding markup rather than at the block's inner edge:

::: v-pre
```gsx
<title>{ if !isProd { {env} - } } {page} - One Learning</title>
```
:::
