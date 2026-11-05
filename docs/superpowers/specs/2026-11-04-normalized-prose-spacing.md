# GSX normalized spacing: source whitespace

**Status:** Implemented. Independent contract/adversarial reviews, exact render
probes, lint, docs build and final uncached CI passed.

GSX keeps compact output without guessing typography, CSS layout, component
output or branch outcomes. Whitespace normalization operates on authored text
and source boundaries. It does not implement literal HTML emission.

## Three rules

1. A whitespace-only text run containing CR/LF is layout and disappears.
   This covers indentation between tags or expressions and at list edges.
   An inline whitespace-only run normalizes to one space.
2. Every other ASCII whitespace run collapses to one space. Internal text runs
   and text edges facing any source sibling follow the same rule. A component,
   image, block element, interpolation or conditional does not change it.
   Newline indentation at the outer edges of a children list is discarded.
   Punctuation is ordinary text: adjacency expresses attachment.
3. Expression values are data and remain untouched. This includes explicit
   `{" "}`, `{"  "}`, Unicode spaces and whitespace inside string values.
   Character references retain their source spelling. An ASCII source space
   beside a nonbreaking space is still its own authored separator.

The syntax inside component, slot and control-flow braces keeps its existing
brace-padding trim. Parsing retains text whitespace uniformly; the normalization
pass decides which source whitespace is layout. Text in pre/textarea subtrees,
raw script/style bodies, static attributes and expression data keeps its
existing verbatim behavior.

## Boundary behavior

Text-edge whitespace faces a sibling based on source structure, regardless of
what that sibling eventually emits. No HTML element allowlist, branch outcome
sets, decoded runtime edge categories or CSS heuristics are needed. No separator
is invented at an adjacent source boundary. Braced/HTML comments and nonrendering Go code are transparent to source
whitespace: their surrounding Text is normalized as one source stream, without
changing comment/code content or moving non-whitespace text. Bare line comments
retain their requirement to occupy a source line. Syntactic brace padding is
trimmed through transparent comments/code too.

| Source | Output |
| --- | --- |
| Hello newline <b>world</b> | Hello <b>world</b> |
| Hello newline <Link>world</Link> | Hello followed by a space and Link output |
| Read newline <img/> newline more | Read <img> more |
| <span>A</span> newline <span>B</span> | <span>A</span><span>B</span> |
| {value} newline % | {value} % |
| {value}% | {value}% |
| Hello newline ! | Hello ! |
| « newline Bonjour newline » | « Bonjour » |
| Hello newline comment newline world | Hello world |
| a&nbsp; newline b | a&nbsp; b |
| {value}{"  "} newline % | value followed by three spaces and % |

A source separator remains even if an expression or conditional emits nothing,
just as an authored inline source space does. Authors put a separator inside a
branch when it should appear only with that branch. No Go conditions or values
are evaluated to adjust whitespace.

## Formatter

The formatter uses the same source boundary rule. It can wrap every internal
ASCII space because a newline preserves that separator, including beside
punctuation or nonbreaking spaces. It must not introduce whitespace where text
was directly adjacent to a sibling, whatever the sibling's tag or expression.
Layout-only inter-tag/expressions gaps remain safe formatting boundaries.

Keep canonical indentation and ordinary layout. Formatting must preserve exact
normalized text and structure and be idempotent. No source-column freezing,
implicit subtree dedenting or spacing annotations.

## Validation

- Pin component, void element, unknown tag, block, interpolation and branch
  boundaries without inspecting their output.
- Cover text on either side, root/slots/control-flow, comments/Go code,
  whitespace-only siblings, punctuation, Unicode/entities and explicit data.
- Regenerate canonical semantic and formatter archives; inspect every changed
  output against authored source. Do not hand-edit generated files.
- Run focused normalization/parser/formatter tests and corpus property checks,
  independent actual CLI render/fmt probes, lint, docs build and final uncached
  `make ci` on Go 1.27.1. Retain exact render golden comparisons.

The obsolete `child-element-spacing` diagnostic remains retired because source
prose separators are now preserved without output classification. No grammar
syntax is added. EOS, one-learning and gsxui remain unchanged.
