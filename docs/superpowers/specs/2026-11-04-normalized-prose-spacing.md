# GSX normalized spacing: bounded static contract

Baseline: origin/main JSX normalization; retain ordinary formatter/layout and
HTML-preserving pre/textarea/raw script/style contexts. Do not implement literal
HTML emission, implicit subtree dedenting, CSS analysis, or runtime word parsing.

## Four rules

1. Whitespace-only Text containing CR/LF is layout and disappears. Inline
   whitespace-only Text without a newline normalizes to one space.
2. Internal inline ASCII whitespace in substantive Text collapses to one space.
   Newline-bearing runs use the same word/punctuation boundary rule as Text
   edges: retain a word separator, suppress a gap beside attaching punctuation
   or an existing rendered space. Entity spelling and explicit data stay intact.
   Splitting a text run with an interpolation must not change this rule.
3. At a substantive Text's leading/trailing edge, an authored inline space stays
   one space. An authored newline becomes one space only when structural edge
   analysis finds a feasible prose boundary requiring separation.
4. Such inferred spaces are not inserted before attaching punctuation or after
   opening punctuation. Explicit inline spaces and expression values override
   inference. In particular {" "}, {"  "}, and NBSP retain their data.

This is GSX source normalization, not literal browser HTML whitespace semantics.

## Structural edge analysis

Reuse wsnorm.Spacing, but replace lossy branch merging with finite outcome sets.
Outcomes: no output/optional, existing space, opaque content, and prose content
classified by punctuation attachment classes (unknown dynamic content is prose
with unknown edge character). Keep exact/static edge character where useful.
Alternatives union outcomes. Empty/optional alternatives also expose following
siblings in the scan direction. No Go conditions, return values, classes or CSS
are evaluated. Known prose elements use HTML child-rendering semantics: the existing text-level
set plus button, label, output and transparent map expose their child text.
Empty elements, embedded content, raw/inert content, fallback-only containers,
value controls and unknown components remain opaque unless a separate rule
explicitly applies. This is not a CSS display or margin inference.
The same source classification must apply in the formatter and compiler; sibling
elements without a substantive Text boundary never acquire a separator.

A newline-bearing Text edge receives a gap if ANY feasible prose outcome needs
it. Space, opaque, or attaching-punctuation outcomes alone never require a gap.
Known punctuation alternatives must remain distinguishable: branches returning
'.' and '!' both attach; merging them into unknown character is incorrect.

In syntactic component/slot bodies, the parser must retain a substantive Text
node's leading source prefix for this decision. It may continue discarding
layout-only separators between markup nodes, as in the existing baseline;
explicit space expressions remain the universal override.

Normalization is postorder: normalize child lists, collapse own Text, retain
raw leading/trailing newline flags for the boundary decision, then insert gaps
left-to-right. Recheck updated neighboring edges so comments separating two Text
nodes do not cause duplicate inferred spaces. Added spaces become ordinary inline
spaces, so a second normalization leaves them unchanged.

## Punctuation policy

Use the existing directional Unicode policy: no inferred space after Ps/Pi or
before Pe/Pf/Terminal_Punctuation. Explicitly specify suffix signs absent from
those Unicode properties: %, per-mille/per-ten-thousand, and ellipsis marks.
Treat ASCII '...' by its first period, U+2026 as an attaching ellipsis. Do not
invent all-Po/all-symbol rules: '$' before an amount and percent after an amount
are directional, and '/' or '-' can occur in prose or joined tokens. Preserve
baseline behavior unless a table row explicitly defines attachment.

## Required examples

| Source boundary | Result |
| --- | --- |
| Hello newline <b>world</b> | Hello <b>world</b> |
| <span>A</span> newline <span>B</span> | <span>A</span><span>B</span> |
| {value} newline % | {value}% |
| {value} inline-space % | {value} % |
| {value}{" "} newline % | {value} % |
| Hello newline {name} | Hello {name} |
| {name} newline world | {name} world |
| {name} newline . / ... / U+2026 | attached punctuation |
| Hello newline if branches '.' or '!' | attached in both branches |
| Hello newline if branches word or explicit-leading-space word | one inferred source gap; explicit branch space remains additional data |
| Hello newline comment newline world | one inferred gap, not two |
| preserve/raw subtree | baseline verbatim contract |

## Deliberate static limitation

Inference belongs to authored source Text, like writing an inline source space.
It is not conditional on what a dynamic expression happens to render. Example:

    Hello
    { if ok { <b>world</b> } }!

The feasible prose branch requires a source gap. It normalizes like
`Hello { if ok { <b>world</b> } }!`, hence false may emit `Hello !`. This is
intentional bounded static semantics, not a guarantee of global runtime
punctuation cleanup. Likewise a mixed word/already-spaced branch may render two
spaces, because explicit branch whitespace is data and cannot be deleted.
Likewise, a branch choosing opening punctuation or a word followed by a source
newline may leave a space after the opening punctuation, just as an explicit
inline space at that outer boundary would. Unknown runtime strings are not
inspected to reclassify their actual punctuation.
Authors requiring branch-sensitive spacing put the explicit separator in the
branch and keep the external boundary adjacent. No runtime HTML parser, writer
buffering, generated loop-first-item state, or Go-value speculation is justified.

## Implementation and validation

- Extend shared wsnorm edge analysis, not independent lint/formatter heuristics.
- Update normalizeMarkup to preserve required Text-edge gaps using that analysis.
- Retire diagnostics for gaps the normalizer now supplies; do not retain warnings
  merely because the original source included a newline.
- Preserve formatter NoSpace/word-gap bonds, allowing canonical indent/reflow;
  normalize(parse(fmt(source))) must equal normalize(parse(source)).
- Pin direct rule table, all branch combinations, optional if/loop/switch scans,
  explicit string spaces, Unicode/NBSP, preserve contexts, deferred embedded Go
  element splits, slots/fragments, and named/custom component boundaries.
- Exact corpus outputs, normalization idempotence, formatter idempotence, and real
  CLI render-before/after probes. Existing module-backed fixture tables should
  be extended rather than opening additional expensive modules per test row.
