# Principles

## Stay close to HTML and Go

Use HTML-shaped markup for templates and ordinary Go for helpers, types, and
application logic. The [Syntax reference](./syntax.md) covers the boundary.

## Prefer readable syntax

Common component code should scan like the HTML it produces. Explicit forms are
available when composition or security needs more control; start with the
shortest form that says what the page does. Browse the [Syntax
reference](./syntax.md) by task.

## Let Go check the program

Components and parameters become Go declarations and calls, so `go build` checks
names and types. See [Composition](./syntax/composition.md).

## Escape by default

Dynamic values are escaped for where they appear. Trusted-value opt-outs are
explicit and narrow; use them only after validating the value. See
[Escaping](./syntax/escaping.md).

## Keep the runtime small

The `gsx` runtime uses only the Go standard library. Generation, formatting, and
editor support stay in the [toolchain](./cli.md) instead of adding application
dependencies.
