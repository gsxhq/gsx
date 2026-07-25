# Comments

Comment syntax depends on where the comment appears.

::: v-pre

| Position | Source-only | Rendered |
|---|---|---|
| Inside a tag | `// …`, `/* … */`, `{/* … */}`, `{// … }` | — |
| Between child nodes | `// …` (line start), `{/* … */}`, `{// … }` | `<!-- … -->` |
| Outside markup | `// …`, `/* … */` | — |

:::

A line that starts with `//` between child nodes is a source-only comment. It
may not touch text content — next to a text line it is a compile error; use
`{// …}` to comment or `{"// …"}` to render the slashes. Mid-line `//` is
always literal text, and inside `pre`/`textarea` every `//` renders verbatim.

## HTML comments

An HTML comment is rendered verbatim, including `<` and `&` in its text.

<!--@include: ./_generated/comments/010-html-comments.md-->

## Comments inside a tag

Use Go-style line or block comments to annotate an element's attributes. Braced comment forms are also accepted there. These comments remain in the `.gsx` source but do not render.

<!--@include: ./_generated/comments/020-attribute-comments.md-->

## Comments between children

Use a braced comment between child nodes when the note should stay in source without appearing in the HTML.

<!--@include: ./_generated/comments/030-content-comments.md-->

Both `{/* … */}` and `{// … }` are source-only in child content.

<!--@include: ./_generated/comments/035-bare-line-comments.md-->

## Go comments outside markup

Outside markup, `//` and `/* */` are ordinary Go comments. This includes package and import comments, helper functions, and comments inside a [GoBlock](./raw-go.md#goblock).
