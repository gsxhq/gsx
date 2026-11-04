# Normalized prose spacing implementation plan

User-approved contract: discard layout-only whitespace; preserve prose word
separation; attach punctuation; explicit spaces override normalization.
The normalized baseline and ordinary indented fixtures have been restored.

1. Extend shared structural edge analysis with finite branch outcomes and the
   documented directional punctuation policy; normalize lost prose boundaries.
   Cover rule tables, comments, branch alternatives, optional content, Unicode,
   explicit spaces and preserved/raw contexts. Keep normalization idempotent.
2. Check formatter faithfulness and idempotence against normalized semantics,
   update only affected canonical corpus cases through regeneration and review
   each changed case. Keep exact render golden checks.
3. Reconcile diagnostics/editor behavior and docs with automatic normalization;
   avoid CSS inference and annotations. Explain static source-space semantics.
4. Separate spec/quality reviews, independent actual CLI/render probes, lint,
   docs build and final uncached make ci; update existing PR without merging.

## Review evidence

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
