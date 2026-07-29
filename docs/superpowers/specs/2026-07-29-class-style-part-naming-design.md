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

Use one unexported representation:

```go
type conditionalPart struct {
	value string
	on    bool
}
```

Class/style parts are code-generation intermediates, not authored-GSX values.
Users provide values and conditions inside `class={ ... }` and `style={ ... }`;
the compiler creates parts only in generated Go. At component boundaries,
composed values are reduced to strings by `ClassJoin` or `StyleString`.

The runtime constructors and consumers remain exported because generated code
is compiled in the user's package, but their shared type stays private:

```go
func Class(value string) conditionalPart
func ClassIf(value string, on bool) conditionalPart

func Style(value string) conditionalPart
func StyleIf(value string, on bool) conditionalPart

func (gw *Writer) Class(merge func([]string) string, parts ...conditionalPart)
func (gw *Writer) Style(parts ...conditionalPart)
```

Go permits callers to pass the result of an exported function even when its
concrete return type is unexported, so generated calls remain valid. Users
cannot declare `[]conditionalPart`, which is intentional because reusable,
first-class class/style parts are not currently a GSX feature.

The semantic constructor names make generated code clear. The shared private
type does not prevent `Style(Class(...))`, but generated code is the only
supported producer and the corpus pins which constructor is selected for each
attribute kind.

If GSX later introduces first-class authored parts, that feature may add
distinct exported `ClassPart` and `StylePart` types without changing the
generated `Class(...)`, `ClassIf(...)`, `Style(...)`, and `StyleIf(...)` call
shape.

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
paths before being wrapped with `Style` or `StyleIf`.

## Compatibility

This is a generated-code/runtime ABI rename. GSX is pre-alpha, so the exported
`ClassPart` type is removed without a compatibility alias. There is no observed
repository or sibling-project use that declares `ClassPart` values or slices,
and authored GSX has no syntax for passing parts as first-class values.

Authored `.gsx` syntax and rendered output do not change. Committed generated
`.x.go` files must be regenerated in GSX and affected sibling projects.

## Verification

- Runtime tests pin the shared private representation and the class/style
  constructors and consumers.
- An external-package compile test pins that generated-style calls can pass
  values whose concrete type is unexported.
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
