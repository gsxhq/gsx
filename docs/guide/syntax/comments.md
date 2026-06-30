# Comments

gsx files contain two distinct kinds of comments: **HTML comments** (`<!-- … -->`) that appear in the markup body and are emitted to the rendered output, and **Go line comments** (`// …`) that appear outside the markup body (in Go code, import blocks, or top-level helper functions) and are stripped by the Go compiler.

## HTML comments vs Go comments

An `<!-- … -->` HTML comment inside a component body is a markup node. It is parsed, kept in the AST, and written verbatim to the rendered HTML output. The comment text is literal — no escaping, no interpolation — so characters like `<` and `&` inside an HTML comment are preserved exactly as written.

::: v-pre
A `//` Go comment outside the markup body (for example, above a component declaration or inside a `{{ }}` GoBlock) is ordinary Go source and is stripped by the Go compiler before the generated code runs.
:::

<!--@include: ./_generated/comments/010-html-comments.md-->

In the example above, `<!-- header -->` and `<!-- a < b -->` both appear in the rendered output. The `<` inside the second comment is not HTML-escaped — it is part of the comment text and passes through literally.

## Content comments `{/* … */}`

A `{/* … */}` block inside markup is a **content comment** — the parser recognises it as comment-only and drops it entirely from the rendered output. Unlike `<!-- -->`, nothing reaches the browser.

::: v-pre
```gsx
component Note() {
	<p>Visible{/* hidden note */} text</p>
}
```
:::

Renders:

```html
<p>Visible text</p>
```

The line form `{// … }` (a Go line comment inside braces) works identically — both are stripped at parse time, so neither appears in the generated Go code or the final HTML.

Content comments are a markup-layer construct. They are distinct from `// …` Go comments that appear **outside** the markup body (above a component declaration, inside a `{{ }}` GoBlock, etc.) — those are stripped by the Go compiler and can never appear inside element markup at all.
