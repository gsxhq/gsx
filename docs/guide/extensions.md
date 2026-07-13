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
| Props field-matcher function | Project binary with `gen.Main` |

`gen.Main` still loads `gsx.toml`. Programmatic options take precedence over an
environment override and the config file when they set the same behavior.

## Create `cmd/gsx/main.go`

This complete example gives a project explicit attribute-to-prop mappings:

```go
package main

import (
	"slices"

	"github.com/gsxhq/gsx/gen"
)

var propFields = map[string]string{
	"variant":      "Variant",
	"full-width":   "FullWidth",
	"data-test-id": "TestID",
}

func main() {
	gen.Main(gen.WithFieldMatcher(matchField))
}

func matchField(attr string, fields []string) (string, bool) {
	field, ok := propFields[attr]
	return field, ok && slices.Contains(fields, field)
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

## Custom field matching

`gen.WithFieldMatcher` replaces the default attribute-to-field matcher for
bring-your-own props:

```go
gen.WithFieldMatcher(func(attr string, fields []string) (field string, ok bool) {
	// Return a name from fields, or false to place the attribute in Props.Attrs.
	return matchProjectField(attr, fields)
})
```

The callback receives the raw attribute name and the target struct's exported
field names. A successful match must return one of those field names. Returning
`false` sends the attribute to the attrs bag. The target props struct must
declare an `Attrs gsx.Attrs` field to receive it; otherwise generation fails.

## Run the project binary

Invoke the project command explicitly so its options are used:

```bash
go run ./cmd/gsx generate ./...
```

Use the same prefix for other affected commands, for example
`go run ./cmd/gsx fmt -w .`.

To inspect resolved declarative settings, run `go run ./cmd/gsx info` for the
readable view or add `--json` for the JSON view. Function hooks are not
enumerated. A custom field matcher is reported only as present (`custom` in the
readable view or `hasFieldMatcher` in JSON).
