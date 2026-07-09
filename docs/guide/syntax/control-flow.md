# Control flow

gsx control-flow blocks embed standard Go `if`, `for`, and `switch` statements directly into a component body. The outer `{ … }` tells the parser that the content is dynamic — a Go control-flow construct whose branches contribute markup — as opposed to a literal HTML element or an interpolated value.

Control flow is real Go: the branches are ordinary Go branches, the loop variable is an ordinary Go variable, and the generated code is straightforward Go with markup-writing calls inserted where elements appear. There is no new template language to learn.

## If / else

`{ if cond { <markup> } }` conditionally renders its child markup. An `else` branch renders when the condition is false.

<!--@include: ./_generated/control-flow/010-if-else.md-->

Both branches can contain any markup — elements, interpolations, nested components, even further control-flow blocks. The `else` clause is optional; omitting it means no output is produced when the condition is false.

::: v-pre
`{ if cond { … } }` is distinct from `{{ stmt }}` (a GoBlock): the GoBlock runs a Go statement and produces **no** HTML output. The control-flow form here produces markup from whichever branch is taken. See [Raw Go](./raw-go.md) for GoBlocks.
:::

## For / range

`{ for … := range … { <markup> } }` renders its child markup once per element. The loop variable is in scope inside the block.

<!--@include: ./_generated/control-flow/020-loops-over-lists.md-->

The loop is a regular Go `range` loop. Both index and value variables follow normal Go scoping: declare them with `:=` inside the loop header, and they are only available inside the loop body. A blank identifier `_` discards the index when only the value is needed.

Any Go iterable is supported: slices, arrays, maps, strings (rune iteration), channels, and — with Go 1.23 — range-over functions. The loop body can contain any markup.

## Switch

`{ switch expr { case x: <markup> default: <markup> } }` selects one branch of markup. The `case` and `default` labels are not surrounded by braces; the markup they contain is their body.

<!--@include: ./_generated/control-flow/030-switch.md-->

Each `case` label can list multiple comma-separated values, exactly as in Go. A `default` branch handles values that match no case. gsx lowers `{ switch … }` to a native Go `switch` statement; as in Go, a matched case runs only its own branch — cases do not fall through implicitly.

## Init statements

An `if` or `for` statement can include an init statement before the condition. The init statement runs first, and any variables it declares are scoped to the entire control block — both the condition and all branches.

<!--@include: ./_generated/control-flow/040-init-statement.md-->

The form `{ if v, err := f(); err != nil { … } else { … } }` is the idiomatic way to **handle** a `(T, error)` return without auto-unwrapping. When you write `{ f() }` as a bare interpolation and `f` returns `(T, error)`, gsx auto-unwraps: it propagates the error to the caller. An init-statement `if` lets you inspect the error in-place and produce fallback markup instead of propagating.

The same init-statement form works with map lookups (`{ if v, ok := m[k]; ok { … } }`), type assertions, and any other Go expression that returns multiple values.
