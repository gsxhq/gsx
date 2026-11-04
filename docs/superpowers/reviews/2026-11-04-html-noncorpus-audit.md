# Non-corpus causal audit

Base: origin/main. This ledger covers 87 non-corpus files; separate ledgers cover semantic/formatter archives, examples and generated docs. Runtime root package has no changes.

Review findings: source-column freezing must be removed; stale column-preservation documentation must be reconciled; raw planner test narrative corrected; no new runtime normalization. Final independent review approved at 4da61bf6 after rebuilding the CLI. Actual fmt -w probes cover canonical Go, 11 host/alignment fixtures, idempotence, changed-length f-hole/pipeline AST mutation, and exact rendered CRLF/Unicode bytes (242 bytes). No actionable findings.

| File | Why changed / required action |
|---|---|
| `CHANGELOG.md` | Document breaking literal whitespace, variadic argument separators and retired lint; revise placement wording to final formatter contract. |
| `README.md` | Document literal HTML behavior; no output or formatting transformation. |
| `ast/ast.go` | Remove JSX-only layout flags; retain syntactic trivia/origin metadata used by literal cursors. Removed now-unused original-line geometry APIs; no semantic Text rewriting. |
| `ast/ast_test.go` | Source-layout span/Unicode/CRLF tests; layout metadata only. |
| `ast/walk_exhaustive_test.go` | Remove registry entries for deleted spacing analysis and normalization; maintain active exhaustive walk checks. |
| `docs/ROADMAP.md` | Record shipped semantic contract and exact-output coverage; final status must match final verification. |
| `docs/guide/cli.md` | Document HTML literal output/control trimming, variadic arguments, active lint migration or syntax formatting. Reconcile retained-column claims with revised contract. |
| `docs/guide/config.md` | Document HTML literal output/control trimming, variadic arguments, active lint migration or syntax formatting. Reconcile retained-column claims with revised contract. |
| `docs/guide/syntax/basic-syntax.md` | Document HTML literal output/control trimming, variadic arguments, active lint migration or syntax formatting. Reconcile retained-column claims with revised contract. |
| `docs/guide/syntax/composition.md` | Document HTML literal output/control trimming, variadic arguments, active lint migration or syntax formatting. Reconcile retained-column claims with revised contract. |
| `docs/guide/syntax/control-flow.md` | Document HTML literal output/control trimming, variadic arguments, active lint migration or syntax formatting. Reconcile retained-column claims with revised contract. |
| `docs/superpowers/plans/2026-11-04-html-whitespace-review-corrections.md` | Design/process evidence. Superseded proposal remains explicitly marked; historical completion records are not current behavior specifications. |
| `docs/superpowers/plans/2026-11-04-html-whitespace.md` | Design/process evidence. Superseded proposal remains explicitly marked; historical completion records are not current behavior specifications. |
| `docs/superpowers/specs/2026-11-04-html-whitespace-design.md` | Design/process evidence. Superseded proposal remains explicitly marked; historical completion records are not current behavior specifications. |
| `docs/superpowers/specs/2026-11-04-intentional-spacing-design.md` | Design/process evidence. Superseded proposal remains explicitly marked; historical completion records are not current behavior specifications. |
| `gen/cache_relocation_test.go` | Replace retired spacing lint with active duplicate-attribute warning; cache relocation/range/JSON/policy/startup coverage remains live. |
| `gen/cachestore_test.go` | Replace retired spacing lint with active duplicate-attribute warning; cache relocation/range/JSON/policy/startup coverage remains live. |
| `gen/configfile.go` | Reject retired lint configuration with actionable removal message, for every level. |
| `gen/diag_render_test.go` | Replace retired spacing lint with active duplicate-attribute warning; cache relocation/range/JSON/policy/startup coverage remains live. |
| `gen/fmt.go` | Remove normalization terminology; CLI delegates to same literal-preserving formatter as editor. |
| `gen/info_test.go` | Replace retired spacing lint with active duplicate-attribute warning; cache relocation/range/JSON/policy/startup coverage remains live. |
| `gen/lint_config_test.go` | Replace retired spacing lint with active duplicate-attribute warning; cache relocation/range/JSON/policy/startup coverage remains live. |
| `gen/options.go` | Use active lint in public example; behavior routed through existing config validator. |
| `gen/watchsession_test.go` | Replace retired spacing lint with active duplicate-attribute warning; cache relocation/range/JSON/policy/startup coverage remains live. |
| `internal/codegen/analyze.go` | Stop JSX normalization of deferred embedded markup; necessary to match top-level literal semantics. |
| `internal/codegen/barecomment.go` | Check substantive neighbouring text explicitly because whitespace-only Text now survives. |
| `internal/codegen/barecomment_test.go` | Review pending; do not claim approval. |
| `internal/codegen/childspacing.go` | Delete superseded JSX whitespace normalization/spacing lint and its specific tests; literal-output and retired-config tests cover new contract. |
| `internal/codegen/childspacing_fmt_test.go` | Delete superseded JSX whitespace normalization/spacing lint and its specific tests; literal-output and retired-config tests cover new contract. |
| `internal/codegen/component_call_plan_test.go` | Pin authored AST retention before variadic assembly; stale rendered-variadic comment corrected by root. |
| `internal/codegen/component_positional_plan.go` | Omit only HTML-ASCII-whitespace Text when binding resolved variadic children; scalar content and AST unchanged. |
| `internal/codegen/component_positional_plan_test.go` | Pin variadic argument indexes/counts, explicit space, Unicode, comments, fragment/control grouping and no AST mutation. |
| `internal/codegen/direct_component_semantics_test.go` | Unchanged source now emits whitespace after Go return/child, shifting writes; exact failure boundaries expanded. Root notified ordinary source could be migrated, never trim results. |
| `internal/codegen/minify_thread_test.go` | Literal sibling separators coalesce into existing static writes; expectations retain minification checks and actual bytes. |
| `internal/codegen/module_ephemeral_test.go` | Replace retired lint fixture with active self-reference lint; editor promotion coverage retained. |
| `internal/codegen/module_importer.go` | Stop normalization of pristine parse cache; remove retired spacing diagnostic emit. Cache cloning unchanged. |
| `internal/codegen/spread_fold_diff_test.go` | Unchanged GoBlock/element spacing now emits leading newline/tab prefixes; reference explicitly includes bytes. Root notified optional ordinary source migration; attribute oracle stays independent. |
| `internal/diag/lint.go` | Remove retired spacing lint registry entry; policy tests use active lint without reducing severity/filter coverage. |
| `internal/diag/lint_test.go` | Remove retired spacing lint registry entry; policy tests use active lint without reducing severity/filter coverage. |
| `internal/gsxfmt/gsxfmt.go` | Remove JSX normalization from shared formatting path; preserve raw HTML Text. |
| `internal/gsxfmt/gsxfmt_test.go` | Stop expecting invented element-child newlines; retain deep nested tab-width threshold coverage. |
| `internal/htmltag/htmltag.go` | Extract lexical bare-comment suppression for pre/textarea/script/style from deleted whitespace normalizer; no CSS/whitespace inference. |
| `internal/lsp/codeaction.go` | Remove quickfixes for retired lint; import actions remain. |
| `internal/lsp/codeaction_spacing_test.go` | Delete superseded JSX whitespace normalization/spacing lint and its specific tests; literal-output and retired-config tests cover new contract. |
| `internal/lsp/format.go` | Shared formatter documentation updated; no distinct editor whitespace behavior. |
| `internal/pretty/doc.go` | Remove unused Fill engine; retain literal Text bytes and local opening-tag fit. SourceIndent was removed with source-island machinery. AlignLine bases generated indentation on physical line leading whitespace, ignoring inline prose width. Root0 and three-space-prefix external probes passed. |
| `internal/pretty/doc_test.go` | Remove unused Fill engine; retain literal Text bytes and local opening-tag fit. SourceIndent was removed with source-island machinery. AlignLine bases generated indentation on physical line leading whitespace, ignoring inline prose width. Root0 and three-space-prefix external probes passed. |
| `internal/pretty/print.go` | Remove unused Fill engine; retain literal Text bytes and local opening-tag fit. SourceIndent was removed with source-island machinery. AlignLine bases generated indentation on physical line leading whitespace, ignoring inline prose width. Root0 and three-space-prefix external probes passed. |
| `internal/pretty/print_test.go` | Remove unused Fill engine; retain literal Text bytes and local opening-tag fit. SourceIndent was removed with source-island machinery. AlignLine bases generated indentation on physical line leading whitespace, ignoring inline prose width. Root0 and three-space-prefix external probes passed. |
| `internal/printer/corpus_property_test.go` | Use strict content AST oracle for all parseable inputs; preserve idempotence/reparse checks. |
| `internal/printer/corpus_test.go` | Independent content oracle keeps exact HTML Text, canonicalizes Go AST/JS/CSS token contracts, ignores layout metadata; no emitted-whitespace normalization. |
| `internal/printer/embedded_attr_fmt_test.go` | Remove normalization from test helpers; update exact expectations where old formatter invented/reflowed HTML whitespace. Keep JS/CSS token equivalence, literal Text preservation, parsing and idempotence. Canonical syntax and exact-literal expectations reviewed after correction; independent rebuilt-CLI probes passed. |
| `internal/printer/fuzz_test.go` | Remove normalization from test helpers; update exact expectations where old formatter invented/reflowed HTML whitespace. Keep JS/CSS token equivalence, literal Text preservation, parsing and idempotence. Canonical syntax and exact-literal expectations reviewed after correction; independent rebuilt-CLI probes passed. |
| `internal/printer/goexpr_gofmt_property_test.go` | Remove JSX normalization; keep gofmt reachability assertions over embedded Go regions. |
| `internal/printer/goexpr_test.go` | Remove normalization from test helpers; update exact expectations where old formatter invented/reflowed HTML whitespace. Keep JS/CSS token equivalence, literal Text preservation, parsing and idempotence. Canonical syntax and exact-literal expectations reviewed after correction; independent rebuilt-CLI probes passed. |
| `internal/printer/golayout_test.go` | Corrected generator defect: canonical Go now uses ordinary placeholder/gofmt path, literal HTML Text stays exact, and generated breaks align to physical leading indentation via AlignLine rather than containing prose width or nesting-only depth. Independent rebuilt-CLI probes passed. |
| `internal/printer/goparts.go` | Corrected generator defect: canonical Go now uses ordinary placeholder/gofmt path, literal HTML Text stays exact, and generated breaks align to physical leading indentation via AlignLine rather than containing prose width or nesting-only depth. Independent rebuilt-CLI probes passed. |
| `internal/printer/hostlayout.go` | Corrected generator defect: canonical Go now uses ordinary placeholder/gofmt path, literal HTML Text stays exact, and generated breaks align to physical leading indentation via AlignLine rather than containing prose width or nesting-only depth. Independent rebuilt-CLI probes passed. |
| `internal/printer/hostlayout_test.go` | Corrected generator defect: canonical Go now uses ordinary placeholder/gofmt path, literal HTML Text stays exact, and generated breaks align to physical leading indentation via AlignLine rather than containing prose width or nesting-only depth. Independent rebuilt-CLI probes passed. |
| `internal/printer/inline.go` | Delete JSX tag classifications/prose wrapping/spacing bonds and tests because literal Text may not reflow. |
| `internal/printer/inline_test.go` | Delete JSX tag classifications/prose wrapping/spacing bonds and tests because literal Text may not reflow. |
| `internal/printer/literal_test.go` | Remove normalization from test helpers; update exact expectations where old formatter invented/reflowed HTML whitespace. Keep JS/CSS token equivalence, literal Text preservation, parsing and idempotence. Canonical syntax and exact-literal expectations reviewed after correction; independent rebuilt-CLI probes passed. |
| `internal/printer/printer.go` | Corrected generator defect: canonical Go now uses ordinary placeholder/gofmt path, literal HTML Text stays exact, and generated breaks align to physical leading indentation via AlignLine rather than containing prose width or nesting-only depth. Independent rebuilt-CLI probes passed. |
| `internal/printer/printer_test.go` | Remove normalization from test helpers; update exact expectations where old formatter invented/reflowed HTML whitespace. Keep JS/CSS token equivalence, literal Text preservation, parsing and idempotence. Canonical syntax and exact-literal expectations reviewed after correction; independent rebuilt-CLI probes passed. |
| `internal/printer/script_property_test.go` | Remove normalization from test helpers; update exact expectations where old formatter invented/reflowed HTML whitespace. Keep JS/CSS token equivalence, literal Text preservation, parsing and idempotence. Canonical syntax and exact-literal expectations reviewed after correction; independent rebuilt-CLI probes passed. |
| `internal/printer/script_test.go` | Remove normalization from test helpers; update exact expectations where old formatter invented/reflowed HTML whitespace. Keep JS/CSS token equivalence, literal Text preservation, parsing and idempotence. Canonical syntax and exact-literal expectations reviewed after correction; independent rebuilt-CLI probes passed. |
| `internal/printer/segment.go` | Delete JSX tag classifications/prose wrapping/spacing bonds and tests because literal Text may not reflow. |
| `internal/printer/segment_test.go` | Delete JSX tag classifications/prose wrapping/spacing bonds and tests because literal Text may not reflow. |
| `internal/printer/spacing_bond_test.go` | Delete JSX tag classifications/prose wrapping/spacing bonds and tests because literal Text may not reflow. |
| `internal/printer/style_property_test.go` | Remove normalization from test helpers; update exact expectations where old formatter invented/reflowed HTML whitespace. Keep JS/CSS token equivalence, literal Text preservation, parsing and idempotence. Canonical syntax and exact-literal expectations reviewed after correction; independent rebuilt-CLI probes passed. |
| `internal/printer/style_test.go` | Remove normalization from test helpers; update exact expectations where old formatter invented/reflowed HTML whitespace. Keep JS/CSS token equivalence, literal Text preservation, parsing and idempotence. Canonical syntax and exact-literal expectations reviewed after correction; independent rebuilt-CLI probes passed. |
| `internal/rendergolden/rendergolden.go` | Exact output archive codec: one framing LF, no content trim/dedent. |
| `internal/rendergolden/rendergolden_test.go` | Pin exact archive roundtrips for leading/interior/trailing whitespace, CRLF and empty output. |
| `internal/wsnorm/spacing.go` | Delete superseded JSX whitespace normalization/spacing lint and its specific tests; literal-output and retired-config tests cover new contract. |
| `internal/wsnorm/wsnorm.go` | Delete superseded JSX whitespace normalization/spacing lint and its specific tests; literal-output and retired-config tests cover new contract. |
| `internal/wsnorm/wsnorm_internal_test.go` | Delete superseded JSX whitespace normalization/spacing lint and its specific tests; literal-output and retired-config tests cover new contract. |
| `internal/wsnorm/wsnorm_test.go` | Delete superseded JSX whitespace normalization/spacing lint and its specific tests; literal-output and retired-config tests cover new contract. |
| `parser/attrs.go` | Capture original literal/key spans for formatting without stale semantic source replay. |
| `parser/barecomment_test.go` | Remove stale normalizer reference; lexical slot behavior test unchanged. |
| `parser/blockbody_ws_test.go` | Pin syntactic-edge trimming, exact interior Text, correct spans and comment/layout retention. |
| `parser/component.go` | Parse literal interior with only syntactic outer-edge trimming. |
| `parser/file.go` | Capture original field bounds for grammar cursor; obsolete line geometry removed. Current-AST hole/pipeline mutation probe passed. |
| `parser/markup.go` | Keep literal Text; trim ASCII only at GSX body edges; retain spans/trivia; lexical bare-comment behavior unchanged. |
| `parser/markup_test.go` | Remove assertions of deleted JSX layout flags; literal body/span tests replace semantic coverage. |
| `parser/parser.go` | Remove obsolete JSX-only leading-break scanner. |
| `parser/unicode_identifier_test.go` | Account for newly retained sibling Text; Unicode names still tested. |
| `parser/valueform.go` | Capture explicit arm-brace/literal geometry; no change to value semantics. |

## Final probe evidence

- Rebuilt CLI: `/tmp/gsx-adversarial-probe/gsx-round3-final`.
- `/tmp/gsx-round3-probes`: eight semantic host fixtures plus root-column-zero, three-space indentation and inline-control alignment probes. All actual `fmt -w` and second-pass idempotence checks passed.
- Root column zero produces one-tab attribute indentation and column-zero closer. Three authored spaces produce five-column attributes and three-column closer. Inline prose does not produce giant tag indentation.
- `/tmp/gsx-adversarial-probe/render3`: regenerate/render before and after actual formatting returned identical 242 bytes, preserving CRLF, Unicode, two variadic element arguments, explicit third space argument, fragment/control grouping and scalar whitespace.
- `/tmp/gsx-adversarial-probe/mutate-final.go`: longer f-literal hole/stage names and arguments print current AST values while literal Text remains exact.
- No repository edits or full CI run were performed by this reviewer.
