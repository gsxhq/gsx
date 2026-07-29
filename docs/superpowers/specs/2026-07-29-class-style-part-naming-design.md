# Class and Style Part Naming Design

## Problem

GSX models the parts of both `class={ ... }` and `style={ ... }` with
`ast.ClassAttr` and `ast.ClassPart`. The runtime similarly represents both with
`gsx.ClassPart`, so generated style code reads:

```go
_gsxgw.Style(
	_gsxrt.Class("color:red"),
	_gsxrt.ClassIf("display:none", hidden),
)
```

The implementation is correct, but its names imply that style composition is a
kind of class composition. That makes generated code misleading and leaves no
type boundary for future class- or style-specific behavior.

## Design

### Compiler AST

Rename the shared syntax representation to neutral structural names:

- `ast.ClassAttr` becomes `ast.ComposedAttr`.
- `ast.ClassPart` becomes `ast.ComposedPart`.

`ComposedAttr.Name` remains `"class"` or `"style"`. The parser syntax and AST
shape do not otherwise change. A composed part may still contain a Go
expression, condition, pipeline, CSS literal, or value-form control flow as
allowed by its containing attribute.

### Runtime

Use distinct semantic types:

```go
type conditionalPart struct {
	value string
	on    bool
}

type ClassPart conditionalPart
type StylePart conditionalPart
```

The two exported types deliberately have the same representation today but are
different named Go types. This gives compile-time separation with no additional
memory cost and allows either type to evolve independently later.

The runtime constructors and consumers are:

```go
func Class(value string) ClassPart
func ClassIf(value string, on bool) ClassPart

func Style(value string) StylePart
func StyleIf(value string, on bool) StylePart

func (gw *Writer) Class(merge func([]string) string, parts ...ClassPart)
func (gw *Writer) Style(parts ...StylePart)
```

Class-only helpers continue to accept `ClassPart`. `StyleString` and other
style-only helpers change to accept `StylePart`.

### Generated Code

Code generation selects the semantic constructor from the containing
`ComposedAttr.Name`:

```go
_gsxgw.Class(
	_gsxcm.Merge,
	_gsxrt.Class("flex"),
	_gsxrt.ClassIf("hidden", hidden),
)

_gsxgw.Style(
	_gsxrt.Style("color:red"),
	_gsxrt.StyleIf("display:none", hidden),
)
```

CSS filtering and escaping remain unchanged. Static CSS literal parts and
dynamic style values still pass through their existing trusted or filtered
paths before being wrapped as `StylePart`.

## Compatibility

This is a generated-code/runtime ABI rename. GSX is pre-alpha, so the old
style-through-`ClassPart` API is removed without compatibility aliases. Authored
`.gsx` syntax and rendered output do not change. Committed generated `.x.go`
files must be regenerated in GSX and affected sibling projects.

## Verification

- Runtime tests pin class/style type-specific constructors and consumers.
- Parser, formatter, LSP, analysis, and codegen tests use the renamed AST types.
- The canonical corpus regenerates all affected generated goldens while keeping
  render goldens unchanged.
- `make ci` and `make lint` pass.
- Sibling projects that commit generated output are regenerated and tested
  against the exact GSX commit.

## Documentation and Roadmap

No public syntax documentation or roadmap status changes are needed because
authored syntax and capabilities are unchanged. Any documentation or examples
that show generated Go are updated to the new runtime names.
