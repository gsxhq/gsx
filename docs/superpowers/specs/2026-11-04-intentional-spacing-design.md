# Precise child spacing diagnostics

## Goal

Remove false positives from explicit branch separators and attached ellipses,
and let authors explicitly identify CSS-supplied separation. Leave EOS unchanged.
Runtime output and whitespace normalization remain unchanged.

## Branch analysis

Retain the possible rendered edges of alternative branches until checking a
specific text boundary. Warn if any possible content edge needs a separator.
A space edge is safe; a punctuation edge is evaluated independently using the
existing typography rule. Do not merge a safe comma edge with a space edge into
unknown content. Empty branches and zero-iteration loops expose the following
sibling, which must also be checked. No Go condition evaluation is introduced.

Use a bounded, deduplicated summary of relevant edge facts rather than enumerate
execution paths. Preserve the distinction between unknown content and known
punctuation. Keep the formatter and lint on the same spacing implementation.

## Ellipsis

Treat U+2026 as attached terminal punctuation when it is the right-hand edge.
`Rerun { parent }` followed by an ellipsis on another source line stays quiet.
Do not exempt arbitrary Unicode punctuation or change output whitespace.

## Intentional CSS spacing

Use the existing braced block-comment syntax:

```gsx
<label class="choices">
    Conduit
    {/* gsx:spacing css */}
    { if unavailable { <span>No conduit models</span> } }
</label>
```

The exact comment text `gsx:spacing css` declares that CSS supplies separation
at this boundary. It renders nothing and adds no HTML attribute. It is not a
general lint-disable directive and does not suppress other diagnostics.

The marker is an opaque boundary for shared spacing analysis: checking an edge
does not look through it. It applies only inside its immediate children list;
it does not propagate into a following element or control-flow body. Ordinary
comments continue to be transparent. Leading/trailing markers have no effect.
The formatter preserves the marker and may retain a line break there, while
continuing to check and protect all unmarked boundaries. Normalization and HTML
generation treat the marker like an ordinary non-rendering comment.

Only a standalone braced block comment with this exact text is recognized.
Reserve comments beginning `gsx:spacing` and diagnose unsupported values or
forms, so a misspelling cannot silently defeat the annotation. Do not inspect
class names, computed styles, or component implementations.

An element-wide setting was rejected because it hides unrelated missing spaces.
A rendered attribute was rejected because compiler intent should not enter HTML.
The marker names the layout intent rather than a diagnostic severity.

## Verification and integration

Add canonical corpus cases covering safe and unsafe branch mixtures, nested
branches, empty paths, zero-iteration loops, punctuation alternatives and
ellipses. Pin unchanged rendered HTML and generated code by regeneration.
Cover markers before and after text/interpolations, adjacent to control flow,
inside fragments and child props, and ensure suppression does not leak into
nested lists. Include malformed directive cases and ordinary-comment controls.

Add formatter corpus cases for marker preservation and idempotence. Verify
formatting neither creates warnings nor changes rendered output. Document the
annotation and diagnostic scope, update the roadmap/changelog, and check sibling
syntax tooling for compatibility with the existing comment form.

Use Go 1.27.1 exactly; the current PATH reports 1.27.3 and must be corrected for
verification. Run narrow tests during development, then make lint and one final
make ci. Follow subagent execution with task reviews and an independent reviewer
who builds throwaway probes. Do not merge as part of this task.
