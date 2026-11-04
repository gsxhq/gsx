# HTML whitespace semantics

**Status:** Implemented and validated on `codex/intentional-spacing`.
**Date:** 2026-11-04

## Decision and scope

GSX adopts HTML content whitespace semantics instead of JSX normalization.
HTML markup should behave as its source would in HTML; CSS and the browser
decide how literal whitespace displays. Do not infer CSS from tags, class names,
component implementations, or stylesheets. No spacing annotations, whitespace
mode switches, or legacy JSX compatibility mode are introduced.

This is a breaking language and formatter change. It supersedes the intentional
CSS-spacing comment proposal. The child-element-spacing diagnostic is retired;
the branch-analysis and ellipsis patches are unnecessary under this model.
EOS, one-learning, and gsxui remain unchanged during GSX implementation. Their
migrations are separately reviewable work, not implicitly authorized here.

## Literal content and syntactic whitespace

Inside HTML elements, fragments, and marker regions, retain literal text,
including spaces, tabs, and newlines. This applies uniformly in component
output, embedded Go element literals, control-flow bodies, and markup slots.
Do not collapse internal text whitespace, discard whitespace-only children,
or trim text at element boundaries. Attribute values retain their existing
contracts. HTML parsing rules, including newline handling in pre/textarea,
remain the browser's responsibility; do not add a second HTML parser to runtime.

Go-like delimiters remain syntax. Whitespace immediately inside component-body,
control-flow-body, switch-case-body, and markup-slot delimiters is not output.
Trim only the outer leading/trailing ASCII whitespace runs of these bodies;
preserve all interior separators. This extends the existing control-body
boundary convention consistently to component bodies and markup slots.
Whitespace inside a string interpolation is explicit data and is never trimmed.
Whitespace inside an HTML element is literal even when that element is the only
node in such a syntactic body.

Examples:

```gsx
component Greeting(name string) {
    <p>Hello
        <strong>{ name }</strong>!</p>
}
```

The whitespace after Hello is emitted, allowing normal HTML layout to show a
word gap. The punctuation remains attached. Indentation around the component's
root element is syntax and does not pad every component invocation.

```gsx
<p>{ if ok { Yes } else { No } }!</p>
<span>{ count }%</span>
<span>Hello</span><strong>world</strong>
<div class="whitespace-pre-wrap">{ value }</div>
```

These render the selected word without brace padding, an attached percent sign,
touching inline elements, and exactly the supplied value inside the div.
To emit spaces at a trimmed body edge, use a string value such as `{ " " }`.

Whitespace outside a conditional remains unconditional output. If no branch
renders, authored whitespace surrounding that conditional still exists. Loop
iterations concatenate each trimmed body; authors supply separators explicitly.
Do not speculate about Go conditions or insert punctuation-aware spacing.

## Components and child values

Scalar gsx.Node children preserve authored content whitespace. The existing
children contract accepts only gsx.Node or ...gsx.Node; do not introduce
primitive typed children.

A variadic children parameter is a GSX argument list, not an HTML content body.
Whitespace-only literal Text nodes separate its arguments and do not become
arguments themselves. This means a multiline call containing two element nodes
passes exactly two values. Only HTML ASCII whitespace (space, tab, LF, CR, FF)
is ignored here; substantive text, NBSP, and vertical tabs remain values with
their exact content. Explicit whitespace expressions such as `{ " " }` remain
arguments. Fragments and control-flow nodes remain single authored arguments;
their internal content is not filtered. Source comments remain non-values.

This exception belongs to resolved variadic argument binding only. It must not
alter scalar children, slots, HTML elements, or stored literal AST text. Pin
rendered list cardinality, not merely concatenation, in the corpus. This corrects
the initial implementation's overly broad treatment of indentation as values.

## Source comments

Source comments emit nothing; surrounding literal whitespace remains output.
HTML comments keep their existing emitted-comment behavior. Neither form is a
spacing directive. Bare-comment validation must distinguish whitespace-only
neighbours from substantive text instead of assuming normalization removed
indentation. The formatter cannot invent rendered newlines to relocate a bare
comment; preserve its existing boundary text or leave the content layout intact.

## Formatter contract

Formatting preserves emitted literal content whitespace and adjacency, not
merely an approximation of visual output under normal CSS. It may format Go
syntax and tag/attribute syntax where those changes are not rendered content.
It must not reflow prose, reindent literal text, or add/remove separators between
children merely because a tag usually displays as block or flex. A touching
boundary stays touching. Existing literal spaces/newlines remain literal.

Syntactic body-edge whitespace may be formatted because it is not emitted.
Element content may retain unconventional layout when necessary for fidelity.
Preserving emitted HTML bytes does not require preserving absolute source
columns or the spelling of surrounding Go syntax. Format Go expressions,
component signatures, type arguments and other syntax normally, including
syntax on the same line as a literal. Never insert indentation into literal
Text as a side effect of formatting a containing Go field or pipeline argument.
Where literal lines constrain indentation, retain their content and arrange
syntactic boundaries coherently rather than freezing unrelated expressions.
Generated indentation follows the physical line's leading indentation, not
implicit component nesting or the width of preceding inline prose.
An opening tag's width decision measures its syntax, not following unbreakable
literal content.

The semantic AST remains authoritative. Source metadata may retain syntax
trivia needed for faithful printing, but must not be a blanket escape from
formatting Go. Ordinary corpus inputs and documentation examples must be
readable, correctly indented source. Deliberately malformed whitespace belongs
in explicit preservation regressions; do not silently turn a formatter repair
test into a preservation test by replacing its golden or description.
For ordinary examples and corpus fixtures, author HTML subtrees flush left
inside component braces, with indentation relative to the HTML root. The user
explicitly selected this convention so exact emitted HTML is conventionally
indented; component braces do not add an implicit HTML indentation layer.
Dedicated whitespace regressions may deliberately use other layouts.
Rendered goldens remain exact output. Correct manually authored source when
its literal indentation is unintended; do not normalize generated HTML or
pretty-print documentation output to conceal those bytes.

Do not use generated string interpolations or magic comments to disguise
formatter-introduced whitespace changes. Existing raw-text JS/CSS formatter
contracts remain separate and require their existing semantic tests.

## Evidence and migration

EOS: 665 tracked non-testdata GSX files, 115 explicit space interpolations in
60 files, and a dedicated joined-text checker. Risks include attached punctuation
and CommentBody's segmented content under pre-wrap.

One-learning: 141 GSX files. Notification-rule prose currently loses the space
before a link. Searches found 65 whitespace-preserving declarations in 22 files;
multiline field and email wrappers can acquire visible indentation.

gsxui: 1,252 tracked GSX files, largely generated/copied variants, and 87 explicit
space interpolations in 25 files. Joined controls use flex. Documentation has
attached punctuation on separate lines; reusable wrappers need literal-content
review even where local CSS is forgiving. Migrate canonical/generator sources
and regenerate derived files rather than editing generated variants.

These are audit/search counts, not counts of confirmed rendering defects.

Before application changes, produce a read-only comparison of old/new emitted
text boundaries for representative inputs. Report inserted/removed text and
preserve exact whitespace in the comparison; do not normalize it away. Separate
source changes needed to retain existing output from deliberate fixes to prose.
No automatic global deletion of explicit space interpolations.

## Verification and rollout

Create corpus cases for literal whitespace in text, adjacent inline elements,
interpolations, comments, fragments, marker regions, components, slots, embedded
Go literals, and every control-flow kind. Include empty paths and repeated loop
iterations. Pin exact output for whitespace-preserving content without teaching
the compiler CSS classes. Add component binding cases and retired lint-config
tests. Existing references to the retired lint should produce an actionable
configuration error, not silently disable an unknown setting.

Replace JSX-normalized formatter faithfulness with an emitted-content-preserving
oracle. Test idempotence at existing widths, touching boundaries, multiline tags,
pre-wrap-compatible wrappers, and bare comments. Regenerate goldens only after
reviewing expected semantic differences. Keep the standard-library-only runtime
and streaming architecture; do not add package loads or runtime CSS analysis.

For application evaluation, add exact text and browser checks for prose, attached
punctuation, flex controls, stored multiline content, and empty wrappers. Existing
one-learning DOM normalization trims text-node boundaries; gsxui's text helper
inserts spaces between nodes. Neither is a sufficient whitespace oracle.

Update the language documentation, roadmap, changelog, corpus manifest, and
sibling editor/parser documentation where affected. Explain the breaking change
and the formatter's narrower layout freedom. Pin PATH Go to CI's 1.27.1, run
narrow tests during development, make lint, then one final make ci. Use the
repository's subagent execution/review process and an independent adversarial
reviewer with throwaway probes. Do not merge automatically.
