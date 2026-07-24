# Fix: bare text in a switch arm swallows the following `case`/`default` label

Date: 2026-07-25. Status: approved design.

## Defect

A `{ switch … }` arm whose body ends in **bare text** swallows every following
arm label as literal text: the later arms vanish from the AST and the label
renders as page content. Render-visible, silent, pre-existing.

```gsx
{ switch k {
case "a":
	<b>x</b> tail
default:
	<i>y</i>
} }
```

parses as ONE case whose body is `<b>x</b>`, `" tail\n\tdefault:\n\t\t"`,
`<i>y</i>` — the `default:` label is text, the default arm is gone.

**Root cause.** `parseCaseBody` (`parser/markup.go`) tests for the `case`/
`default` terminators only at the top of its loop — a node boundary. When it
falls through to `parseTextCtx(true)` (`parser/markup.go`), that scanner stops
only at `<`, `{`, `}`; it has no notion of arm labels, so one text run consumes
everything up to the switch's closing brace.

Observed boundary (probed):

| arm body shape | today |
|---|---|
| ends with element, then label | correct |
| label mid-line after an element | correct |
| ends with text, then label | **defect** |
| text-only arm, then label | **defect** |
| prose `the default: value is 5` | stays text (correct — must stay so) |

## Decision (user-approved)

**Line-start rule.** Inside a case body, a text run ends at a `case`/`default`
keyword that is

1. the first non-whitespace on its line, and
2. a **valid label**: `default` followed (after spaces) by `:`; `case` followed
   by a case list terminated by a `:` found with the same string/rune-aware
   colon scan `parseCaseClause` already uses (so `case "a:b":` works).

Everything else stays literal text — mid-line prose containing `default:` is
unaffected. Node-boundary label detection (the existing top-of-loop check,
including mid-line labels after an element) is unchanged.

The rule is closed under our own formatter, which always emits labels at line
start, so formatted output re-parses to the same AST (idempotence preserved).

**Escape hatch** (documented): to render line-leading literal `default:` text,
write it as an interpolation `{"default: 5"}` or wrap it in an element. The
same applies to a line-leading `case <expr>:` — `case 1: nope` at line start
becomes an arm, and unlike a stray `default:` it compiles clean, so it changes
the render silently.

### Amendment 2026-07-25b (from adversarial review)

Two corrections, both found by probing rather than reading:

1. **Colon must be on the keyword's own line.** The speculative validity scan
   is bounded to the label's physical line. Unbounded `scanToCaseColon`
   lookahead tokenized to EOF per candidate — an ~800× quadratic parse blowup
   on `case`-leading prose (4000 lines: 2.3ms → 1.84s), and it captured colons
   across arms and even across a sibling switch. A markup arm label is always
   single-line (the formatter emits it that way), so bounding it costs nothing
   real. The committed node-boundary path (`parseCaseClause`) keeps its
   existing unbounded scan — it is not speculative.

2. **The formatter must not create labels.** Closure under the formatter does
   NOT follow from "the formatter emits labels at line start": the word-gap
   fill can wrap PROSE so that a mid-line `default:`/`case 1:` becomes the
   first word on a line, which this rule then reads as a label. Proven
   end-to-end: a valid file's render changed after `gsx fmt`, with no
   diagnostic at any stage. Therefore, inside a case body, **a break candidate
   immediately before a `case`/`default` word is a bond** (never breakable) —
   in the fill's joints and at segment boundaries alike. The formatter then
   cannot move such a word to line start, and the only labels are the
   author's.

## Non-goals

No change to node-boundary label detection, to `{ if }`/`{ for }` bodies (their
text already stops at `}`), to the formatter's output, or to any syntax. No new
diagnostics — the ambiguity resolves silently by position, as decided.

## Testing

- **Parser unit tests** (`parser/markup_test.go`): label after text; label after
  text mid-line (stays text); text-only arm followed by a label; prose with
  mid-line `default:` (stays text); `case "a:b":` label after text (colon scan);
  label at line start with leading tabs/spaces.
- **Semantic corpus** (`internal/corpus/testdata/cases/**`, per repo doctrine —
  parse + codegen + render pinned): an arm ending in text followed by a real
  `default:` arm, rendering only the selected arm's content and NOT the label
  text; plus a case pinning that prose containing `default:` still renders as
  text.
- **fmt corpus** (`internal/gsxfmt/testdata/cases`): the same shape formats and
  round-trips (idempotence), proving formatter output re-parses under the rule.
- Gates: `make ci`, `make lint`.

## Sibling repos

Grammar-level: `../tree-sitter-gsx` may have the same text-vs-label ambiguity in
its switch-body rule — check and file/fix separately if so (highlighting only,
no runtime impact). No docs-site syntax change is implied.
