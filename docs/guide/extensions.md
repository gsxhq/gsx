# Extending gsx

Use `gsx.toml` for declarative settings. Build a project-owned gsx binary only
when configuration needs to call Go code directly.

## When you need a custom binary

| Need | Put it in |
|---|---|
| Filters and filter packages | [`gsx.toml`](./config.md#pipeline-filters) |
| URL rules and presets | [`gsx.toml`](./config.md#url-attributes) |
| Type renderers | [`gsx.toml`](./config.md#renderers-type-directed-value-rendering) |
| Minify levels | [`gsx.toml`](./config.md#minify-asset-minification-level) |
| Class merger | [`gsx.toml`](./config.md#class_merger-tailwind-aware-class-merge-strategy) |
| CSS or JavaScript formatter function | Project binary with `gen.Main` |
| CSS or JavaScript minifier function | Project binary with `gen.Main` |

`gen.Main` still loads `gsx.toml`. Programmatic options take precedence over an
environment override and the config file when they set the same behavior.

## Create `cmd/gsx/main.go`

Start with a normal project-owned command, then add the formatter or minifier
options described below:

```go
package main

import "github.com/gsxhq/gsx/gen"

func main() {
	gen.Main()
}
```

The custom command has the same subcommands and flags as the standard command.

## Custom CSS and JavaScript formatters {#custom-cssjs-formatter}

Use a custom formatter when `gsx fmt` should delegate embedded code to a tool
such as Prettier or Biome:

```go
gen.Main(
	gen.WithCSSFormatter(formatCSS),
	gen.WithJSFormatter(formatJavaScript),
)
```

Both callbacks have this signature:

```go
func(src []byte) ([]byte, error)
```

They receive a self-contained `<style>` or executable `<script>` body and
return formatted bytes. If a callback returns an error or panics, gsx keeps that
body unchanged and continues formatting the file.

With no programmatic formatter, gsx uses token-aware CSS and JavaScript
formatters. Once either custom formatter is installed, the two callbacks become
an explicit pair: a `nil` callback leaves that language's body unchanged instead
of selecting its built-in formatter. Supply both callbacks to keep formatting
both languages.

The built-in formatters keep strings and comments intact, preserve meaningful
line breaks and intra-line spacing, and normalize structural indentation.

## Custom minifiers and minify level {#minify-level}

Custom minifiers replace the built-in full minifier:

```go
gen.Main(
	gen.WithCSSMinifier(minifyCSS),
	gen.WithJSMinifier(minifyJavaScript),
	gen.WithMinifyLevel(gen.MinifyFull, gen.MinifyFull), // CSS, JavaScript
)
```

The signatures are:

```go
func minifyCSS(css string) (string, error)
func minifyJavaScript(js string) (string, error)
```

The minify level gates each callback. `gen.MinifyNone` skips minification;
`gen.MinifyFull` uses the custom callback when one is present, otherwise the
built-in full minifier. You can set the levels in `[minify]` instead of calling
`WithMinifyLevel`; the option overrides both `GSX_MINIFY` and `gsx.toml`.

A custom CSS minifier receives only fully static `<style>` blocks. CSS with
`@{...}` holes always uses gsx's built-in hole-aware path. JavaScript minifiers
receive complete, holeless executable `<script>` bodies. A callback error stops
generation and reports which minifier failed.

An executable `<script>` containing any `@{...}` hole remains wholly
unminified. Neither the built-in nor a custom JavaScript minifier changes the
text around its holes.

## Run the project binary

Invoke the project command explicitly so its options are used:

```bash
go run ./cmd/gsx generate ./...
```

Use the same prefix for other affected commands, for example
`go run ./cmd/gsx fmt -w .`.

To inspect resolved declarative settings, run `go run ./cmd/gsx info` for the
readable view or add `--json` for the JSON view. Function hooks are not
enumerated.
