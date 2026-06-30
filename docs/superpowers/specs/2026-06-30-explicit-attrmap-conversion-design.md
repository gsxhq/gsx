# Explicit `AttrMap` conversion

## Goal

Templates have one attribute-bag type: the ordered `gsx.Attrs` slice. Maps are not
attribute bags and codegen does not implicitly convert them.

## Runtime API

`gsx.Attrs` remains the ordered attribute representation:

```go
type Attrs []Attr
```

`gsx.AttrMap` becomes a defined convenience type:

```go
type AttrMap map[string]any

func (m AttrMap) ToAttrs() Attrs
```

`ToAttrs` returns attributes sorted by key so conversion from an unordered map is
deterministic. A nil or empty `AttrMap` returns nil.

Remove `gsx.AttrsFromMap`. There is no conversion API for a bare `map[string]any`;
callers that need map-backed construction must use `gsx.AttrMap` and explicitly call
`ToAttrs`.

## Template and codegen semantics

Component attribute-bag parameters, fallthrough bags, attribute literals, conditional
attribute bags, and element spreads use only `gsx.Attrs`.

Codegen must not:

- recognize `map[string]any` or another map type as an attribute bag;
- admit maps in its synthetic attribute-bag type constraint;
- insert an implicit map-to-`Attrs` conversion;
- emit calls to `gsx.AttrsFromMap`.

An explicit expression such as `gsx.AttrMap{"id": id}.ToAttrs()` is valid wherever
`gsx.Attrs` is valid because the expression has type `gsx.Attrs`. A bare `AttrMap` or
`map[string]any` in the same position fails normal type checking.

`AttrMap` may still be used as an ordinary, explicitly declared non-bag Go value. It
receives no template-specific treatment.

## Diagnostics

Diagnostics describe the sole required bag shape, `gsx.Attrs`. They do not suggest
that `map[string]any`, named map types, or `gsx.AttrMap` are accepted without an
explicit `.ToAttrs()` conversion.

## Tests and documentation

Runtime tests cover sorted conversion and nil conversion through `AttrMap.ToAttrs`.
Codegen corpus coverage must demonstrate:

- `gsx.Attrs` works for component bag binding and element spread;
- `gsx.AttrMap{...}.ToAttrs()` works at those boundaries;
- bare `gsx.AttrMap`, bare `map[string]any`, named map types, and tuple-returned maps
  are not implicitly converted;
- generated Go contains no `AttrsFromMap` calls.

Guide documentation presents `Attrs` as the only template bag type and `AttrMap` as an
explicit Go construction helper.

## Compatibility

This intentionally removes `AttrsFromMap` and implicit support for
`map[string]any`. Existing callers migrate from:

```go
gsx.AttrsFromMap(values)
```

to:

```go
gsx.AttrMap(values).ToAttrs()
```

when `values` is a `map[string]any`, or construct `gsx.Attrs` directly when order is
significant.
