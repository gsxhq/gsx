# Normalized prose spacing implementation plan

User-approved contract: discard layout-only whitespace; preserve prose word
separation; preserve authored attachment; explicit spaces override normalization.
The normalized baseline and ordinary indented fixtures have been restored.

1. Extend shared structural edge analysis for branch alternatives and normalize
   lost prose boundaries. Punctuation uses the same separator rule as words;
   source adjacency expresses attachment.
   Cover rule tables, comments, branch alternatives, optional content, Unicode,
   explicit spaces and preserved/raw contexts. Keep normalization idempotent.
2. Check formatter faithfulness and idempotence against normalized semantics,
   update only affected canonical corpus cases through regeneration and review
   each changed case. Keep exact render golden checks.
3. Reconcile diagnostics/editor behavior and docs with automatic normalization;
   avoid CSS inference and annotations. Explain static source-space semantics.
4. Separate spec/quality reviews, independent actual CLI/render probes, lint,
   docs build and final uncached make ci; update existing PR without merging.

## Baseline review evidence

Implementation restores the normalized baseline; literal-whitespace emission,
source-column freezing and fixture indentation migrations were backed out.
The shared normalizer and formatter preserve inferred prose gaps and explicit
spaces using one boundary policy. Parser root/slot prefixes and slot closing
padding have direct regressions. All changed goldens were reviewed individually.

Separate spec and quality reviews approved a90233e3. Independent rebuilt-CLI
probes at width 20 matched 1,008 hand-expected rendered bytes and passed
idempotence; five additional explicit-space/punctuation/entity cases passed.
Exact corpus/examples, 1,074 formatter property inputs, targeted package tests,
vet, lint and Node 24 docs build passed. Final uncached `make ci` passed
on Go 1.27.1 (exit 0), including both modules, formatting, examples and
type-bundle parity.

## Authored-attachment revision

The user approved removing punctuation-based whitespace suppression. Prose
newlines now preserve a separator before percent signs, terminal punctuation,
ellipses and after opening punctuation, just as same-line spaces do. Joined
source boundaries remain joined, and layout-only whitespace still disappears.
Remove the obsolete punctuation classifications, update the shared normalizer
and formatter, regenerate affected corpus outputs, and review each changed
case. Run targeted regressions and obtain independent render probes, lint,
docs build and final uncached CI before completing the revision. The baseline
validation above predates this revision and does not validate its changes.
