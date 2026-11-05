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


Revision review evidence:

- Removed punctuation classifications and retained the existing rendered-gap
  policy. Internal and interpolation-split Unicode/entity gaps agree; explicit
  extra inline spaces beside them stay protected during wrapping.
- Reviewed every changed semantic archive: `childspacing/quiet` now honors its
  authored punctuation separators; `normalized_prose_internal` preserves
  separators before punctuation while retaining existing-gap behavior;
  `normalized_prose_root_slots` applies the same rule to roots/slots;
  `normalized_prose_rules` pins spaced and adjacent percent/emphasis examples.
  The `childspacing/branches` change only clarifies its fixture description.
- Reviewed formatter archives: `explicit-prose-spaces` allows punctuation-space
  wrapping while retaining entity/Unicode gap bonds; `inline-atoms` and
  `spacing_bond` preserve joined punctuation boundaries during reflow.
- Separate contract and adversarial reviews approved the final correction.
  A freshly built CLI matched 579-byte false and 591-byte true hand-expected
  renders before formatting and at widths 1, 20 and 120, with idempotent second
  passes. Probes include punctuation, branches, entities/NBSP, root/slots,
  explicit spaces, comments, sibling layout and preserved contexts.
- Focused wsnorm/printer/gsxfmt tests, formatter corpus properties, exact corpus
  and examples passed. Final lint and Node 24 docs build passed.
- Final authoritative uncached `make ci` passed on Go 1.27.1 (exit 0), after
  the existing-gap correction, including both modules, build/vet/tests,
  formatting, examples and type-bundle parity. Log:
  `/tmp/gsx-authored-spacing-final-ci.log`.

## Source-whitespace revision after Claude review

The user requested fixing line breaks that still silently join text beside
components and images. The approved direction is source normalization without
an element allowlist or branch-output analysis:

1. Capture raw text whitespace uniformly in parser body contexts, retaining
   existing syntactic brace-padding trim; move layout decisions to wsnorm.
2. Normalize text-edge whitespace facing any source sibling. Remove branch and
   element-output classification, decoded-gap exceptions and formatter guards
   that make newline and inline prose separators differ. Keep expression data
   verbatim and layout-only whitespace absent.
3. Update canonical cases for components/images/block/empty/branch boundaries,
   explicit space data and root/slots; review every generated delta separately.
4. Update published guide, CLI, changelog and roadmap around source rules.
5. Obtain independent adversarial render probes and contract review, run focused
   tests, lint, docs build and final uncached CI, then push the existing PR.

Prior validation records above cover prior revisions, not this revision.


Source-whitespace revision validation:

- Removed element/output/branch classifications and decoded-gap inference.
  Source Text regions now normalize through transparent comments/Go blocks in
  one linear scan, preserving the first whitespace location. Parser brace
  padding and formatter source-edge guards use the same transparency rule.
- Review exposed comment-split newline loss, inline/newline coalescing
  differences, brace padding hidden behind comments, first-gap relocation,
  and whitespace-only inline regions losing their space at narrow widths.
  Fixed each in the source-region/edge model with direct regressions.
- Reviewed each changed existing semantic archive: `childspacing/branches`
  retains source spaces beside NBSP; `childspacing/quiet` preserves text-edge
  spaces beside components, blocks, empty elements and void tags;
  `comments/parse_snapshot` records the newly retained raw parser whitespace;
  `nested-literal/eval_order_positions` retains five authored inline root
  separators; `normalized_prose_internal` preserves ASCII separators beside
  entities/Unicode and input tags; `normalized_prose_rules` preserves source
  separators in addition to explicit expression data.
- New `source_sibling_boundaries` pins both branch outcomes, component/image/
  block/empty/unknown output boundaries, root/slot inline spacing and adjacency.
  New `source_comment_runs` pins transparent source runs, outer whitespace,
  brace padding, pure inline whitespace and comment-hidden edge spaces. Its
  363-byte rendered output was checked against hand-expected HTML.
- Reviewed all changed formatter archives: `explicit-prose-spaces` allows
  ordinary internal ASCII spaces beside entity/Unicode data to wrap;
  `spacing_bond` preserves source spacing and first-gap placement while reflowing;
  new `source-sibling-spacing` pins adjacency and the comment-edge regressions.
  All goldens were regenerated, never edited manually. Examples had no drift.
- Separate contract and adversarial reviewers approved `c1cbfdae`. A rebuilt
  CLI matched 887/905-byte hand-expected renders for both branch outcomes before
  formatting and at widths 1, 20 and 120; second passes were idempotent. A strict
  nine-case edge probe and 18 additional Go/fragment/comment checks passed.
- All 1,076 parseable formatter corpus inputs passed faithfulness/idempotence.
  Final `make lint` passed with golangci-lint 2.13.2; Node 24 docs build passed.
- Final authoritative uncached `make ci` passed on Go 1.27.1 (exit 0), including
  both modules, build/vet/tests, formatting, examples and type-bundle parity.
  Log: `/tmp/gsx-source-spacing-final-ci.log`. A final comment-only test-doc
  cleanup was verified with an uncached wsnorm test run.


## Nonblocking re-review follow-up

- Corrected the stale config-guide phrase about inferred separators. It now
  describes collapsing source whitespace beside text. Documented that retired
  lint configuration is invalid even at `"off"` in the guide and changelog.
  Existing config/API rejection tests passed; a rebuilt CLI probe confirmed
  `generate` exits 2 and requests removal for the retired `"off"` entry.
- Tracked text/markup separator wrapping as a formatter follow-up in the
  roadmap. Current bonds preserve exact output; relaxing them must preserve
  comment whitespace placement, adjacency and source edge spaces.
- Independently audited fresh default branches: `vscode-gsx` at
  `27c289968ba8ddaecbee64eddcb284053e4b941f`, `gsxhq.github.io` at
  `820b16b977ad88ec08109f3a57758e77d22bacaf`, and `tree-sitter-gsx` at
  `400f54906ba1442aeb4ef93a9405d74e2e42e51d`. None hard-code the retired lint
  or its quick-fix. VS Code delegates to the language server; CodeMirror uses
  generic compiler diagnostics/formatting and syntax-only highlighting. The
  normal site build rebuilds WASM and syncs the guide from GSX. Grammar corpuses
  vendor source only, without diagnostic/action goldens. No sibling edits or
  grammar changes are required.
