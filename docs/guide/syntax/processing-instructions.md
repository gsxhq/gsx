# Processing instructions

A processing instruction is markup gsx never renders as an element: a bare
placeholder, or a region wrapping content meant to be replaced later. Both
forms are useful targets for client-side patching, such as Chrome's
[declarative partial updates](https://developer.chrome.com/blog/declarative-partial-updates),
which patch by name via `<template for="…">`. `<template>` itself is an
ordinary element in gsx and needs no special syntax.

## Marker

`<?marker name=VALUE>` is a void placeholder:

```gsx
component Row(id string) {
	<li>
		<?marker name={id}>
	</li>
}
```

```html
<li><?marker name="row-1"></li>
```

## Region

`<?start name=VALUE>` … `<?end>` wraps temporary content. Regions nest, and
each `<?end>` closes its nearest open `<?start>`:

```gsx
component Feed() {
	<?start name="feed">
		<li>Loading…</li>
	<?end>
}
```

```html
<?start name="feed"><li>Loading…</li><?end>
```

## `name`

`VALUE` is a string literal or `{expr}`, optionally piped through `|>`
stages — the same grammar as any attribute value. `name` is required on
`marker` and `start`; `<?end>` takes no attributes.

An `{expr}` must be a `string`, `[]byte`, or `fmt.Stringer` — or have a
registered [renderer](../patterns/package-renderers.md), which is applied
before the name is written. Any other type is a compile error.

A name can't contain `>` or `"`: neither is escapable in processing-instruction
data, since it is never entity-decoded. A literal containing one is a compile
error; an `{expr}` producing one is a render error.

## Restrictions

`marker`, `start`, and `end` are the only valid targets — any other target,
such as `<?foo>`, is a compile error. Source must terminate with `>`; the
XML-style `<?marker name="a"?>` is a compile error naming the `?>`.

Only `<?end>` closes a region. Any close tag reached inside one — including a
fragment's `</>` — is a compile error naming `<?end>` as the expected
terminator.

Processing instructions are markup only. They are not valid as a Go-expression
value:

```gsx
x := <?marker name="a"> // error: not supported as a Go expression value
```

## Patch by name

Pair a marker or region with `<template for="…">` so client code can replace
it:

```gsx
component Row(id string) {
	<li>
		<?marker name={id}>
	</li>
	<template for={id}>
		<li>Updated</li>
	</template>
}
```
