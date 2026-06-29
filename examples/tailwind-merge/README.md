# tailwind-merge example

Demonstrates how to configure a Tailwind-aware class-merge strategy via the
`class_merger` knob in `gsx.toml` and the **wrapper idiom**.

## What it shows

Tailwind's conflict-resolution semantics (e.g. `p-4` + `p-8` → `p-8`, not
`p-4 p-8`) differ from gsx's built-in last-wins token dedup.
[tailwind-merge-go](https://github.com/Oudwins/tailwind-merge-go) handles
this correctly, but its API does not match the required `func([]string) string`
shape.

`twcfg/twcfg.go` contains a one-line wrapper that adapts the library to the
expected signature, then `gsx.toml` points `class_merger` at it:

```toml
class_merger = "github.com/gsxhq/gsx/examples/tailwind-merge/twcfg.Merge"
```

Codegen then emits `_gsxcm.Merge` at every class-merge site in the generated
`.x.go` files instead of the default `gsx.DefaultClassMerge`.

## Running

```sh
# From the examples/tailwind-merge directory:
go test ./...

# Regenerate views/card.x.go after editing views/card.gsx:
go run github.com/gsxhq/gsx/cmd/gsx generate ./views
```
