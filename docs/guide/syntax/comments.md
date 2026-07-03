# Comments

gsx has several comment forms. Which delimiters mean "comment" depends on
**where** they appear — inside a tag, in text content, or in surrounding Go
code. All comment forms are preserved by `gsx fmt`.

## The three positions

::: v-pre

| Position | `//` and `/* */` (bare) | `{// }` and `{/* */}` (braced) | `<!-- -->` |
|---|---|---|---|
| **Inside a tag** `<… >` (attribute list, and `{ if … { } }` attr blocks) | source-only comment | source-only comment (printed bare) | n/a |
| **Text / child content** | **literal text — rendered** | source-only comment | rendered verbatim |
| **Outside markup** (Go code, `{{ }}`) | Go comment (stripped by the Go compiler) | n/a | n/a |

:::

"Source-only" means the comment is kept in your `.gsx` source and survives
formatting, but never reaches the rendered HTML. The one comment that *is*
rendered is the HTML comment `<!-- -->`.

The key rule: a bare `//` means "comment" **only inside a tag**. Between child
nodes it is ordinary text and renders literally — so `<p>a // b</p>` outputs
`a // b`. This is what keeps the syntax unambiguous: a tag's attribute list is a
structured region, whereas child content is free text.

## HTML comments `<!-- -->` (rendered)

An `<!-- … -->` HTML comment inside a component body is a markup node. It is
parsed, kept in the AST, and written verbatim to the rendered HTML. The comment
text is literal — no escaping, no interpolation — so characters like `<` and `&`
inside an HTML comment are preserved exactly as written.

<!--@include: ./_generated/comments/010-html-comments.md-->

In the example above, `<!-- header -->` and `<!-- a < b -->` both appear in the
rendered output. The `<` inside the second comment is not HTML-escaped — it is
part of the comment text and passes through literally.

## Attribute comments (source-only)

Inside a tag you can annotate the attribute list with `//` and `/* */` comments.
They are recognised by the gsx parser, kept through `gsx fmt`, and dropped from
the output — nothing reaches the browser.

<!--@include: ./_generated/comments/020-attribute-comments.md-->

A `//` line comment sits on its own line (or trailing an attribute); the
formatter keeps a trailing comment on the attribute's line and gives an own-line
comment its own line. A `/* */` block comment may stay inline when the tag fits.
The braced forms `{/* … */}` and `{// … }` are also legal in the attribute list —
a comment-only `{ }` is unambiguous — and the formatter canonicalises them to the
bare spelling. The same comments are allowed inside a `{ if COND { … } }`
conditional-attribute block.

::: tip Line comments break the tag (and its children)
A `//` line comment can't share a flat line with the closing `>`, so it forces
the opening tag to wrap — and because a tag and its body break together, the
element's children reflow onto their own lines too (a `{ if … }` conditional
attribute does the same). Use a `/* … */` block comment to annotate a tag while
keeping everything inline.
:::

## Content comments `{/* … */}` (source-only)

Between child nodes, a `{/* … */}` block is a **content comment** — the parser
recognises it as comment-only and drops it from the rendered output. Unlike
`<!-- -->`, nothing reaches the browser; unlike a bare `//` in text, it *is*
treated as a comment because it is wrapped in braces.

<!--@include: ./_generated/comments/030-content-comments.md-->

The line form `{// … }` works identically. Both are preserved by `gsx fmt` and
stripped from the generated Go code and the final HTML.

## Go comments outside markup

A `//` or `/* */` comment outside the markup body — above a component
declaration, in an import block, inside a `{{ }}` GoBlock, or in a top-level
helper function — is ordinary Go source. It is stripped by the Go compiler before
the generated code runs, exactly as in any `.go` file. The package doc comment
(the block before `package`) is likewise preserved in your source.
