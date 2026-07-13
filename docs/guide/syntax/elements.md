# Elements

Elements use familiar HTML syntax: attributes go in the opening tag and children nest between opening and closing tags. Dynamic values use Go expressions in braces.

## Tags and nesting

```gsx
package views

component Card(title string, body string) {
	<article class="card">
		<h2>{title}</h2>
		<p>{body}</p>
	</article>
}
```

Static attributes use quoted strings, while dynamic attributes use expressions such as `disabled={disabled}`. See [Attributes](./attributes.md) for boolean attributes, spreads, and composition.

Whether a tag is an element or a component depends on its name and declarations in the package. See [Basic Syntax — Element or component?](./basic-syntax.md#element-or-component).

## Void elements

HTML void elements have no children. Write them with `/>`; they render without a separate closing tag.

<!--@include: ./_generated/elements/020-void-elements.md-->

## Raw-text elements

The bodies of `<script>` and `<style>` are literal text, so `<` is not treated as a nested tag. Use `@{ expression }` for dynamic values inside them.

<!--@include: ./_generated/elements/030-raw-text-elements.md-->

See [JavaScript](./javascript.md) and [Styling](./styling.md#style-blocks) for interpolation and minification options.

## Full documents

A component can render a complete HTML document. Place `<!DOCTYPE html>` before the root `<html>` element.

<!--@include: ./_generated/elements/040-full-html-document.md-->

## Elements as values

An element can be used wherever Go expects an expression in a `.gsx` file: a variable initializer, return value, function argument, struct field, slice item, map item, or interpolation operand. The result is a `gsx.Node`.

```gsx
package views

var help = <a href="/help">Help</a>
var empty = <><h2>No results</h2><p>Try another search.</p></>

component Page() {
	<main>{help}{empty}</main>
}
```

A fragment (`<>…</>`) works in the same expression positions when the value needs multiple sibling roots. See [Fragments](./fragments.md).
