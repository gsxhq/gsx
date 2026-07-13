# Runtime helpers

These are the `github.com/gsxhq/gsx` types and functions commonly called from
application code. See [Attributes](./attributes.md), [Styling](./styling.md),
and [Escaping](./escaping.md) for their syntax and safety rules.

## Core

| Name | Declaration | Use |
|---|---|---|
| `Node` | `type Node interface { Render(ctx context.Context, w io.Writer) error }` | Renderable value returned by every component. |
| `Func` | `type Func func(ctx context.Context, w io.Writer) error` | Adapt a render function to `Node`; its `Render` method calls the function. |

## Trusted values

| Name | Declaration | Use |
|---|---|---|
| `Raw` | `func Raw(html string) Node` | Render trusted HTML verbatim. |
| `RawURL` | `type RawURL string` | Trust a URL scheme while retaining attribute escaping. |
| `RawJS` | `type RawJS string` | Trust JavaScript and bypass JavaScript encoding. |
| `RawCSS` | `type RawCSS string` | Trust CSS and bypass CSS value filtering. |

Each entry crosses a trust boundary. Pass only content you control or have
validated for that context; see [Escaping](./escaping.md#trusted-value-helpers).

## Node values

| Name | Signature | Use |
|---|---|---|
| `Val` | `func Val(v any) Node` | Box a supported Go value as a `Node`. |
| `Text` | `func Text(s string) Node` | Create an HTML-escaped text node. |
| `Fragment` | `func Fragment(nodes ...Node) Node` | Render several nodes in order without a wrapper element. |

`Val` accepts `nil`, `Node`, `[]Node`, `string`, `[]byte`, `fmt.Stringer`,
`bool`, `int`, `int8`, `int16`, `int32`, `int64`, `uint`, `uint8`, `uint16`,
`uint32`, `uint64`, `float32`, and `float64`. Other values return an error from
`Render`. A named scalar without a supported interface must be converted to its
built-in type before boxing it.

## Attribute bags

### Bag types

| Name | Declaration or signature | Use |
|---|---|---|
| `Attr` | `type Attr struct { Key string; Value any }` | One ordered attribute pair. |
| `Attrs` | `type Attrs []Attr` | Ordered attribute bag. |
| `AttrMap.ToAttrs` | `func (m AttrMap) ToAttrs() Attrs` | Convert a map, sorted by key. |

### `Attrs` methods

#### Read values

| Name | Signature | Use |
|---|---|---|
| `Class` | `func (a Attrs) Class() string` | Join `class` values. |
| `Style` | `func (a Attrs) Style() string` | Join `style` values. |
| `Get` | `func (a Attrs) Get(key string) (any, bool)` | Get the last value. |
| `Has` | `func (a Attrs) Has(key string) bool` | Test for a key. |

#### Transform bags

| Name | Signature | Use |
|---|---|---|
| `Without` | `func (a Attrs) Without(keys ...string) Attrs` | Copy without keys. |
| `Take` | `func (a Attrs) Take(key string) (any, Attrs)` | Get and remove a key. |
| `Merge` | `func (a Attrs) Merge(other Attrs) Attrs` | Merge bags; class and style compose. |

See [Attributes](./attributes.md#spread-x-—-ordered) for spread order and
[Styling](./styling.md#class-style-merging) for class and style precedence.
