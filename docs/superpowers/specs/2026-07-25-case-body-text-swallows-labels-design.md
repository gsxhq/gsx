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
write it as an interpolation `{"default: 5"}` or wrap it in an element.

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
