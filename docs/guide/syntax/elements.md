# Elements

HTML elements in gsx follow the same rules as HTML: tag names are lowercase (or hyphenated for custom elements), attributes appear in the opening tag, and children nest inside. The only additions are dynamic attribute values (`{expr}`) and the component capitalization rule explained in [Basic Syntax](./basic-syntax#element-vs-component).

## Tags and nesting

Any valid HTML element tag is written directly — `<div>`, `<section>`, `<p>`, `<ul>`, `<li>`, and so on. Elements nest the same way they do in HTML: children appear between the opening and closing tag.

```gsx
package views

component Card(title string, body string) {
	<article>
		<h2>{title}</h2>
		<p>{body}</p>
	</article>
}
```

Attributes accept either a static string literal (`type="text"`) or a dynamic Go expression (`disabled={disabled}`). Boolean attributes — ones whose mere presence means `true` — can be written bare (`required`) or driven by a Go expression whose value must be `bool` (`disabled={on}`). A bare boolean attribute is always emitted; a `{bool}` attribute is emitted only when the expression is `true`.

## Void / self-closing elements

Void elements (`<br>`, `<hr>`, `<img>`, `<input>`, `<link>`, `<meta>`, and the others defined by the HTML spec) have no children and no closing tag. In gsx they are written with a trailing `/>`, and they render without a closing tag.

<!--@include: ./_generated/elements/020-void-elements.md-->

A `disabled={disabled}` attribute on the `<input/>` renders as `disabled` when the value is `true`, and is omitted entirely when `false`. Bare boolean attributes like `required` are always rendered.

## Raw-text elements

`<script>` and `<style>` are **raw-text elements**: the parser reads their bodies as literal text, never as markup. There are no nested child elements and `<` is not treated as a tag start — it passes through to the output exactly as written, never HTML-escaped. The one exception is `@{ expr }` interpolation, which is supported inside both elements for dynamic values.

<!--@include: ./_generated/elements/030-raw-text-elements.md-->

By default, raw-text bodies are emitted without minification. Configure
`css_minifier` or `js_minifier` to minify `<style>` or `<script>` output.

Both `<script>` and `<style>` support `@{ expr }` interpolation for dynamic values — see [Style blocks](./styling) and [JavaScript](./javascript) for details.

## Documents and DOCTYPE

A component can render a full HTML page — `<!DOCTYPE html>`, the `<html>` element, and everything inside. There is no special document mode; gsx treats `<!DOCTYPE html>` as a node in the markup and emits it verbatim.

<!--@include: ./_generated/elements/040-full-html-document.md-->
