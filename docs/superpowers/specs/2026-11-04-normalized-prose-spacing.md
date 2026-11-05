# GSX normalized spacing: bounded static contract

**Status:** Implemented baseline; authored-attachment revision awaiting final validation.

Baseline: origin/main JSX normalization; retain ordinary formatter/layout and
HTML-preserving pre/textarea/raw script/style contexts. Do not implement literal
HTML emission, implicit subtree dedenting, CSS analysis, or runtime word parsing.

## Four rules

1. Whitespace-only Text containing CR/LF is layout and disappears. Inline
   whitespace-only Text without a newline normalizes to one space.
2. Internal ASCII whitespace in substantive Text collapses to one space,
   including newline-bearing runs. Punctuation is ordinary prose content and
   does not suppress an authored separator. Entity spelling and explicit data
   stay intact. Splitting a text run with an interpolation must not change
   this spacing rule.
3. At a substantive Text's leading/trailing edge, an authored inline space stays
   one space. An authored newline becomes one space only when structural edge
   analysis finds a feasible prose boundary requiring separation.
4. Preserve authored attachment: adjacent content remains joined; authored
   prose whitespace remains a separator even beside punctuation, percent signs,
   ellipses or opening brackets. Explicit inline spaces and expression values
   override inference. In particular {" "}, {"  "}, and NBSP retain their data.

This is GSX source normalization, not literal browser HTML whitespace semantics.

## Structural edge analysis

Reuse wsnorm.Spacing with structural edge categories: no output/optional,
existing space, opaque content, and prose content. Punctuation and unknown
interpolation text are ordinary prose content. Branch alternatives retain the
possibility of prose at the boundary. Empty/optional alternatives also expose
following siblings in the scan direction. No Go conditions, return values,
classes or CSS are evaluated. Known prose elements use HTML child-rendering
semantics: the existing text-level set plus button, label, output and transparent
map expose their child text.
Empty elements, embedded content, raw/inert content, fallback-only containers,
value controls and unknown components remain opaque unless a separate rule
explicitly applies. This is not a CSS display or margin inference.
The same source classification must apply in the formatter and compiler; sibling
elements without a substantive Text boundary never acquire a separator.

A newline-bearing Text edge receives a gap if ANY feasible prose outcome needs
it. Existing-space or opaque outcomes alone never require a gap. Branches
returning '.' and '!' require the same authored separator as branches returning
words; no punctuation attachment classes are needed.

In syntactic component/slot bodies, the parser must retain a substantive Text
node's leading source prefix for this decision. It may continue discarding
layout-only separators between markup nodes, as in the existing baseline;
explicit space expressions remain the universal override.

Normalization is postorder: normalize child lists, collapse own Text, retain
raw leading/trailing newline flags for the boundary decision, then insert gaps
left-to-right. Recheck updated neighboring edges so comments separating two Text
nodes do not cause duplicate inferred spaces. Added spaces become ordinary inline
spaces, so a second normalization leaves them unchanged.

## Authored attachment

No punctuation or Unicode category suppresses a prose separator. The source
expresses attachment by adjacency: `{value}%` stays joined, while `{value} %`
and `{value}` followed by a prose newline and `%` retain a separator. This
matches ordinary HTML prose spacing without emitting layout-only indentation.
The normalizer never guesses whether a symbol should be a typographic prefix
or suffix. The formatter may wrap an authored prose space before punctuation
as long as normalization preserves that separator.

## Required examples

| Source boundary | Result |
| --- | --- |
| Hello newline <b>world</b> | Hello <b>world</b> |
| <span>A</span> newline <span>B</span> | <span>A</span><span>B</span> |
| {value} newline % | {value} % |
| {value}% | {value}% |
| {value} inline-space % | {value} % |
| {value}{" "} newline % | {value} % |
| Hello newline {name} | Hello {name} |
| {name} newline world | {name} world |
| {name} newline . / ... / U+2026 | separator before punctuation |
| Hello newline if branches '.' or '!' | separator in both branches |
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
intentional bounded static semantics. Likewise a mixed prose/already-spaced
branch may render two spaces, because explicit branch whitespace is data and
cannot be deleted. Unknown runtime strings are not inspected to adjust spacing.
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
