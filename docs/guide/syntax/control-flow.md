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
