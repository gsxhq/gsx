# Preserve source comments through `gsx fmt`

Date: 2026-07-03
Status: approved

## Problem

gsx recognizes several source-only comment forms but the parser **discards** them
as trivia, so `gsx fmt` deletes them. Concretely, all of these vanish on format
today:

- Tag-interior `// …` and `/* … */` — between attributes in an element open tag
  (`parseAttrs`, `parser/markup.go:494`) and inside `{ if COND { attrs } }`
  conditional-attribute blocks (`parseAttrsUntilBrace`, `parser/attrs.go:451`).
  Consumed by `skipTagComment` (`parser/markup.go:64`), which appends no AST node.
- Content-position `{/* … */}` and `{// … }` — between child nodes. Consumed by
  `skipBracedComment`/`commentOnly` (`parser/markup.go:92,114`); `parseBraceNode`
  returns `(nil, true, nil)`.

Because no AST node is produced, the printer (`internal/printer`) has nothing to
re-emit. This is pinned by tests asserting the drop (`TestTagTrailingLineComment`,
`TestTagOwnLineComment`, `TestTagBlockComment`, `TestBracedContentComment`,
`TestBracedLineComment`).

Users want to annotate attribute lists and markup with comments that **survive**
formatting.

## Position semantics (the invariant that keeps this unambiguous)

Bare `//` means "comment" **only inside a tag** (the structured attribute region).
In text/child content it is literal text and renders verbatim.

| Position | `//` bare | `/* */` bare | `{// }` / `{/* */}` braced | `<!-- -->` |
|---|---|---|---|---|
| **Tag interior** (`<tag …>`, `{ if { … } }` attr blocks) | comment → preserve | comment → preserve | comment → preserve, **canonicalized to bare** | n/a |
| **Text / child content** | **literal text (renders)** | **literal text (renders)** | content comment → preserve (stays braced) | renders |

A comment-only `{ … }` is unambiguous in every position, so it is accepted in the
attribute list as well. There, all four forms canonicalize to the bare spelling on
output (`{/* x */}` → `/* x */`, `{// x }` → `// x`); the braced line form's
trailing `}` (which Go requires on the next line) is thereby dropped. In content
position bare `//` is literal text, so only the braced forms are comments and they
stay braced.

The text-content-`//`-is-literal rule is existing behaviour (`TestContentIsLiteral`,
`parser/markup_test.go:336`) and MUST stay green. Example that must keep rendering
`//hello` as text:

```gsx
<p>Hello, { name }! You have { count } messages.
//hello
</p>
```

## AST

Two new nodes (comments print differently by position, so two types):

```go
// Tag-interior comment in an attribute list. Always printed bare (`// text` or
// `/* text */`); a braced `{/* */}` / `{// }` in attr position parses to this same
// node and is canonicalized to bare on output — no "braced" field is retained.
type CommentAttr struct {
    span
    Text     string // inner text, delimiters (and any wrapping braces) stripped
    Block    bool   // true = /* */, false = //
    Trailing bool   // true = sat on the same source line as the previous attr
}
func (*CommentAttr) attrNode() {}

// Content-position comment: `{/* text */}` or `{// text }` between children. Braced.
type Comment struct {
    span
    Text  string
    Block bool
}
func (*Comment) markupNode() {}
```

Both are source-only: codegen ignores them, nothing reaches rendered HTML
(unchanged from today — only the AST/formatter now remembers them). `<!-- -->`
remains a distinct rendered node (`HTMLComment`).

## Parser

- `skipTagComment` → `parseTagComment` returning a `*CommentAttr`. Both attr loops
  append it instead of `continue`-ing. `Trailing` = no `\n` occurred between the
  end of the previous attribute and the `//`/`/*`.
- Attr loops also detect a **comment-only `{ … }`** (reuse `commentOnly`) before
  attempting to parse the brace as a spread/embedded/cond attribute, and emit a
  `*CommentAttr` (`Block` from the inner comment kind). This makes `{/* */}` /
  `{// }` legal in attribute position; they canonicalize to bare on output.
- `skipBracedComment`/`commentOnly` → in content position, produce a `*Comment`
  child node instead of dropping. `parseBraceNode` returns the node.
- Bare-`//`-in-content stays literal `Text` (comment detection for bare `//` is
  tag-interior only). Unterminated `/*` still errors.

## Printer

**Attribute list** (`element`, `attrDoc`, `internal/printer/printer.go:177,262`):
- `CommentAttr` renders `// text` or `/* text */`.
- own-line comment → `pretty.Line` + text + `pretty.BreakParent`.
- trailing comment → `pretty.Text(" ")` + text + `pretty.BreakParent` (glued to the
  preceding attr; forces the following attr onto a new line).
- A `//` line comment ALWAYS forces the opening-tag group to break (a flat
  `<input // x id={y}>` would comment out `id={y}>`). Same `BreakParent` mechanism
  `CondAttr` uses.
- A `/* */` block comment MAY stay inline when the tag fits (most faithful);
  it rides to its own line only if the group breaks for another reason.

**Content position** (`markup` switch, `internal/printer/printer.go:412`):
- `Comment` renders `{/* text */}` or `{// text }`, block-level like `HTMLComment`.

## Codegen / LSP

- Codegen: no-op case for both nodes in the attr and markup walkers (no emit, no
  render). No `computeKey` change — generated output is byte-identical to today.
- LSP: no-op cases in exhaustive attr/markup switches (comments have no
  go-to-def / hover). No new navigation positions.

## Testing

- **Flip** the 5 drop-asserting parser tests to assert preservation (intentional
  behaviour change): `TestTagTrailingLineComment`, `TestTagOwnLineComment`,
  `TestTagBlockComment`, `TestBracedContentComment`, `TestBracedLineComment`.
- **Printer unit tests**: own-line, trailing, block-inline, consecutive comments,
  comment inside `{ if { … } }` block, braced comment in attr position
  canonicalizing to bare (`{/* x */}` → `/* x */`, `{// x }` → `// x`),
  content-position; plus idempotency `fmt(fmt(x)) == fmt(x)`.
- **Corpus cases** (per-context, per CLAUDE.md), pinning that generated `.x.go`
  and `render.golden` are unaffected (comments never render):
  - tag-interior comment on an element,
  - comment inside a `{ if COND { attrs } }` block,
  - content-position `{/* */}` / `{// }` between children.
- `TestContentIsLiteral` stays green.
- `make check` green.

## Sibling grammar / highlighting (separate plan tasks)

Highlight tag-interior `//` `/* */` and content `{/* */}` `{// }` as comments in:

- **tree-sitter-gsx** — grammar rule + `queries/highlights.scm` `@comment`; sync
  `test/examples/`; `npx tree-sitter test`.
- **vscode-gsx** — TextMate grammar patterns + grammar test; version bump only if
  released (tag-gated).
- **gsxhq.github.io** — VitePress/Shiki highlight and **playground CodeMirror**
  mode.

Each verified on a sample; done directly on `main` of each repo (small change).

## Docs

Rewrite `docs/guide/syntax/comments.md`:
- fix the incorrect "can never appear inside element markup at all" line,
- add the position table above as the mental model,
- document tag-interior comments and that all source comments now survive `fmt`,
- state the text-content-`//`-is-literal rule,
- add corpus-backed runnable examples via `examples/*.txtar` + `make examples`.
- Mirror to `gsxhq.github.io` per the two-repo docs flow.

## Out of scope

- Comments inside `class={ … }` / `style={ … }` value lists (different parse path;
  YAGNI until asked). Comments inside an attribute *value* Go expression
  (`id={ x /* keep */ }`) already survive via `go/format` and are untouched.
- Blank-line preservation between attributes.
