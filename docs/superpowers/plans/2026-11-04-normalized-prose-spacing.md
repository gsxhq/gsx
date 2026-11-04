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
