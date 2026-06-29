# Comments

gsx files contain two distinct kinds of comments: **HTML comments** (`<!-- … -->`) that appear in the markup body and are emitted to the rendered output, and **Go line comments** (`// …`) that appear outside the markup body (in Go code, import blocks, or top-level helper functions) and are stripped by the Go compiler.

## HTML comments vs Go comments

An `<!-- … -->` HTML comment inside a component body is a markup node. It is parsed, kept in the AST, and written verbatim to the rendered HTML output. The comment text is literal — no escaping, no interpolation — so characters like `<` and `&` inside an HTML comment are preserved exactly as written.

::: v-pre
A `//` Go comment outside the markup body (for example, above a component declaration or inside a `{{ }}` GoBlock) is ordinary Go source and is stripped by the Go compiler before the generated code runs.
:::

<!--@include: ./_generated/comments/010-html-comments.md-->

In the example above, `<!-- header -->` and `<!-- a < b -->` both appear in the rendered output. The `<` inside the second comment is not HTML-escaped — it is part of the comment text and passes through literally.
