# Testing Architecture Review — Independent Assessment

**Date:** 2026-06-22
**Reviewer role:** Independent (adversarial) review of the gsx test suite
**Verdict:** The foundation is genuinely good — better than most Go codegen projects. The
corpus spine is the right idea, well-executed. The biggest risks are concentrated in two
places: (1) the security-critical escaper has *no differential or property test against its
own oracle* (`html/template`), and (2) coverage *measurement* badly understates reality
because the corpus exercises codegen in a subprocess that `go test -cover` cannot see.

All claims below were verified by reading source and running `go test ./... -count=1`
(all green) and `go test ./... -cover` on 2026-06-22.

---

## 1. Current-state map

Five test layers, tested at their boundaries (matches the design intent in
`docs/superpowers/specs/2026-06-20-corpus-testing-infrastructure-design.md`):

| Layer | Where | Mechanism | What it covers |
|---|---|---|---|
| Parser unit | `parser/*_test.go` (~90 funcs) | table-driven + `TestGoldenCore` (one inline AST golden) + 3 fuzzers + soundness/position | parser productions, brace/apostrophe soundness, absolute positions, pipeline split |
| Pipeline corpus | `internal/corpus/` | txtar fixtures + single-batch codegen + one `go run` | **the spine**: input → diagnostics → generated Go (curated) → rendered HTML |
| Codegen unit | `internal/codegen/*_test.go` | unit + `go run` e2e for filters | type resolution, filter resolution/shadowing, batch equivalence, `//line` directives, RawURL routing |
| CLI / gen | `gen/*_test.go` | `runCapture` integration | generate/fmt/info/clean exit codes, **fmt idempotency**, **incremental cache** correctness + sentinel-guard safety |
| Runtime + helpers | root `*_test.go`, `internal/{printer,wsnorm,cssmin,txtar}`, `ast`, `std` | unit + fuzz + printer property test | HTML/URL/attr/CSS escaping, class/attrs/spread, printer faithfulness/idempotence |

**The corpus (the centerpiece).** `internal/corpus/corpus_test.go:18` `TestCorpus` loads all
`testdata/cases/**/*.txtar` (161 files; `coverage.golden` reports 159 cases — 108 render, 31
error, 11 gen-pinned). Each case is a txtar archive with presence-based facets
(`loader.go:24`): `input.gsx` (+ sibling `.go`, multi-package trees), `invoke`,
`diagnostics.golden` (always checked), `render.golden`, optional `generated.x.go.golden`,
optional `ast.golden`. It classifies cases (`corpus_test.go:45`), runs **one**
`batchCodegen` (`batch.go:49`) that writes every candidate into a single shared temp module,
calls `GeneratePackages` once, then builds + runs **all** renderable cases in **one**
`go run` with NUL-delimited per-case markers (`batch.go:206`). Render comparison is
*structural* (`htmlcompare.go:12`, parse both sides with `golang.org/x/net/html`, compare
trees whitespace-insensitively) so cosmetic formatting never churns goldens.
`coverage.golden` (`coverage.go:10`) is a generated index — one line per case + facet tags +
a TOTAL — regenerated under `-update`; its diff makes every added/removed case visible in
review.

**Golden / `-update` mechanics.** A single `-update` flag (`corpus_test.go:16`) regenerates
`render.golden`, `generated.x.go.golden`, `diagnostics.golden`, `ast.golden`, and
`coverage.golden` in place. `checkOrUpdateFacet` (`corpus_test.go:142`) only rewrites a
facet that already exists (except diagnostics), so `-update` won't silently spawn empty
sections. A genuine safety rule is enforced: a renderable case with no diagnostics **must**
have a `render.golden` or the test fails (`corpus_test.go:111`) — a success case can never
silently skip render verification.

---

## 2. Strengths (keep and extend)

1. **The corpus spine is the right architecture, and it's fast.** One batch module + one
   `go run` for all ~108 renderable cases replaced "91 throwaway `go run`s" (design doc §1).
   `go test ./internal/corpus` runs in ~1.7s. Cross-package cases are first-class in the
   fast path (`batch.go` import-path rewrite), exercising go/packages overlay resolution the
   old single-package tests never touched. **This is comparable to Go's own
   `txtar`/`testscript` model and is better than templ's per-test goquery approach for
   bulk coverage.**
2. **Structural HTML comparison** (`htmlcompare.go`) is exactly right for a templater —
   asserts *meaning* (tree + sorted attrs + collapsed text) not *bytes*, so the whitespace
   model and formatter can evolve without golden churn. This is the same instinct as templ's
   goquery-based assertions ([templ testing docs](https://templ.guide/core-concepts/testing/)).
3. **The file tree *is* the coverage matrix.** `testdata/cases/<area>/<scenario>.txtar`
   across 15 areas; a missing scenario is a visible gap. `coverage.golden` makes the matrix
   reviewable. This is the TypeScript-conformance / Babel-fixture instinct, adapted well.
4. **Property tests already exist where they matter most for refactoring safety.**
   `internal/printer/corpus_property_test.go` runs three contracts over *every* corpus input:
   faithfulness (`Normalize(parse(fmt(S))) == Normalize(parse(S))`), idempotence
   (`fmt(fmt(S)) == fmt(S)`), and re-parse safety. `gsx fmt -w` idempotency is *also*
   unit-tested (`gen/fmt_test.go` `TestFmtWriteIdempotent`). This is the gofmt-idempotency
   discipline, done right.
5. **Real fuzzers with security invariants.** `FuzzCSSValueFilter` (root `fuzz_test.go`)
   asserts the output never contains any CSS breakout byte and is idempotent — a true
   security invariant, not a smoke test. Parser fuzzers (`parser/fuzz_test.go`,
   `pipe_test.go` `FuzzSplitPipe`/`FuzzParsePipe`) assert no-panic + position validity +
   lossless split. Seed corpora target known regressions (apostrophe desync, raw text).
6. **The incremental cache is tested for *correctness*, not just existence.**
   `gen/cache_test.go` (cold/warm/edit isolation), `gen/cachekey_test.go` (build-context
   sensitivity, dependency-closure invalidation), and `main_test.go`'s
   `TestCleanCacheSentinelGuard` (refuses to delete a dir without the `CACHEDIR.TAG`
   sentinel — prevents `GSXCACHE=$HOME` foot-guns). This is unusually mature.
7. **Soundness regressions are pinned as named cases** (`parser/soundness_test.go` C1/I2/B3),
   so the hard-won brace/apostrophe bugs can't silently regress.

---

## 3. Gaps vs best-in-class (prioritized, honest)

### G1 (highest leverage). No differential / oracle test for the escaper — the single highest-risk area.
The escaper in `escape.go` is explicitly a **hand-port of the standard library** —
`cssValueFilter` is "a port of `html/template/css.go`'s cssValueFilter" (`escape.go:63`), the
URL sanitizer mirrors `html/template`'s `about:invalid#zClosurez`/`ZgotmplZ` sentinels
(`escape.go:28,55`). **Yet no test imports `html/template`** (verified: `grep -rl
html/template --include='*_test.go'` returns nothing). The oracle exists in the std lib and
is right there, but the port is checked only against ~8 hand-written input/output pairs
(`escape_test.go`) plus one fuzz invariant on CSS *breakout bytes*. The CSS fuzzer asserts
"no breakout byte leaks," which is necessary but **not sufficient** — it would not catch the
port *over-escaping* (false `ZgotmplZ`) or diverging from std on a tricky decode
(`decodeCSS`, `hexDecode` at `escape.go:103,141`). For a security-sensitive context-aware
escaper, "we copied the stdlib" must be backed by "and we continuously prove we still match
it." ([Go html/template is the reference for contextual auto-escaping](https://security.googleblog.com/2009/03/reducing-xss-by-way-of-automatic.html).)

### G2 (highest leverage, tied). Coverage *measurement* is broken for codegen — the most important package looks under-tested but isn't.
`go test -cover` reports `internal/codegen` at **59.9%**, but this is an artifact: the
corpus exercises codegen by spawning a **subprocess** (`go run`), and subprocess execution
does not feed back into the parent's coverage profile. Verified directly — `emit.go`'s
`emitClassAttr`, `emitStyleAttr`, `emitCSSInterp`, `emitRootComposedClass`, `genStyleChild`,
`htmlAttrEscape` all show **0.0%** under `go test -cover ./internal/codegen`, yet there are
`style/` and `fallthrough/` corpus cases that clearly drive them. The real coverage is far
higher, but **nobody can measure it**, so there is no honest coverage signal and no way to
spot a genuinely-dead branch vs. a corpus-only branch. There is no coverage *target* wired
into CI, no `-coverpkg` aggregation across the corpus boundary.

### G3. Diagnostic testing is whole-string golden, not position-annotated.
`diagnostics.golden` captures the *message text* (e.g. `cases/security_js_rejected_onclick/
input.gsx: codegen: expr value in JS/event-handler context ("onclick") is unsafe...`). That's
better than substring matching, but it does **not** pin the *position* (line:col) of the
diagnostic — and gsx invests heavily in accurate positions (`parser/position_test.go`).
rustc UI tests pin both message *and* the exact span via `//~ ERROR` annotations on the
offending line, which is far more robust to error-rendering refactors and far more readable
in review. gsx error fixtures currently can't tell you *where* the error points without
re-deriving it. Parser-error tests use coarse `strings.Contains` (`markup_test.go:182`,
`component_test.go:87`) — the *weakest* error assertions in the suite.

### G4. No differential test of generated-Go *behavior* against an oracle.
Render goldens prove gsx produces expected HTML, but nothing cross-checks that gsx's
escaping *decisions in a real document* match `html/template` rendering the same logical
document. This is distinct from G1 (which is unit-level escaper parity). A document-level
differential — feed a payload corpus through both gsx and an equivalent `html/template`,
compare — is the strongest possible XSS guard and is how you gain confidence the *codegen
context classifier* (`emit.go:799` `ctxJS` etc., the part that *decides* which escaper to
call) is correct, not just the escapers themselves.

### G5. Security corpus is thin (5 cases) and not systematic.
`security/` has exactly 5 cases: `xss`, `js_rejected_onclick`, `js_rejected_style`,
`url_blocked`, `url_raw_vouched`. No payload matrix (the OWASP/ha.ckers XSS cheat-sheet
vectors), no coverage of: attribute-name injection beyond the spread path, `srcset`/`style`
URL contexts, comment-context breakouts, the `data:` allow/deny edge cases, mixed
interpolation in `<script>`/`<style>` raw text, unicode/encoding bypasses. For the highest-
risk subsystem this is the **least** populated area of the corpus.

### G6. Fuzzing breadth is narrow. Parser + CSS-filter + printer-style only.
No fuzzer drives **codegen** (parse → generate → does it produce *compilable* Go?), no
fuzzer drives the **full pipeline to render** (the property "valid input never renders
unescaped attacker bytes"). The escaper fuzzer covers CSS only — not the URL sanitizer
(whitespace/scheme bypasses are exactly the kind of thing fuzzing finds) and not HTML/attr
escaping.

### G7. No round-trip property for the parser itself.
The printer has a faithfulness property, but there's no `parse → ast.Print → parse` AST-
stability round-trip as a *property over the corpus* (only one inline `TestGoldenCore` AST
golden and per-case opt-in `ast.golden`). go/printer and gofmt both treat round-trip
stability as a first-class property.

### G8. No cross-Go-version / cross-platform guard, no perf regression guard.
codegen depends on `go/types` and `go/packages` (version-sensitive) and the cache key folds
in build context (`cachekey_test.go` tests darwin-vs-linux *key* sensitivity but not actual
cross-platform *output*). No benchmark guards the "one batch build" perf win from regressing
back toward per-case `go run`. CI matrix unknown (no workflow file surfaced in tree).

### G9. Generated-Go goldens are rubber-stamp-prone (acknowledged in design doc §3).
Only 11 `gen`-pinned cases, deliberately kept small because they churn and `-update` makes
blind acceptance easy. The risk is real but the mitigation (keep the set small + structural
render is the real check) is reasonable. Lower priority — flagged for honesty.

### G10. Contributor on-ramp is undocumented.
There is no `CONTRIBUTING` or "how to add a corpus case" doc (only `docs/guide/{syntax,
principles,vision}.md`). The txtar format is discoverable but the facet rules, the
`invoke`/`render.golden` safety rule, and the `-update` workflow live only in the design doc
and the test code.

---

## 4. Recommended foundation roadmap

Aligned with the **existing** corpus/txtar spine — extend it, don't replace it.

### P0 — cheap, high-leverage, do now

- **R1 — Differential escaper test vs `html/template` (closes G1).**
  *What:* a Go unit test (`escape_diff_test.go`) that, for a shared input corpus, asserts
  `urlSanitize`/`cssValueFilter`/`writeHTML` agree with the equivalent `html/template`
  primitive (e.g. render `{{.}}` in URL/CSS/HTML context via `html/template` and compare).
  Drive it with the *same* fuzz seed used by `FuzzCSSValueFilter`, then add
  `FuzzEscaperMatchesStdlib`. *Why:* the port's own comments name std as the oracle; make the
  oracle executable and continuous. *Shape:* ~80 lines + a fuzzer. *Inspired by:* Go's own
  `html/template/escape_test.go` self-consistency tests; differential testing norm.

- **R2 — Honest codegen coverage via `-coverpkg` aggregation (closes G2).**
  *What:* either (a) make the corpus run codegen *in-process* for a coverage build (it
  already calls `GeneratePackages` in-process at `batch.go:86`; only the *render* needs a
  subprocess), and/or (b) add a CI step `go test -coverpkg=./... ./...` and publish the
  merged profile. The codegen-shape goldens + render already drive the emit paths; the fix is
  *measurement*, not new tests. *Why:* 59.9% is a lie that hides real dead code; you can't
  manage what you can't measure. *Shape:* a few lines of test wiring + a CI flag.
  *Inspired by:* `cmd/compile`'s coverage discipline.

- **R3 — Position-annotated diagnostics (closes G3).**
  *What:* extend `diagnostics.golden` (or add a parallel facet) to include `line:col` for
  each diagnostic, OR adopt rustc-style inline `//~ ERROR <substr>` annotations in
  `input.gsx` that the harness extracts and checks against actual diagnostic spans. *Why:*
  gsx's whole value-add includes accurate positions; pin them. *Shape:* the harness already
  has positions in hand from parser/codegen; emit `path:line:col: message` into the golden
  (it partially does — `security` cases include the path prefix). *Inspired by:* rustc UI
  tests / `compiletest`.

  > **Correction (2026-06-22):** "the harness already has positions in hand from
  > parser/codegen" is true only for *parser* diagnostics — parser error cases already pin
  > `line:col` in `diagnostics.golden` (e.g. `3:24: mismatched close tag </span>`). *Codegen*
  > diagnostics carry **no** `.gsx` position today: every `fmt.Errorf("codegen: …")` site in
  > `internal/codegen/` (55 sites across `analyze.go`, `emit.go`, `batch.go`, `filters.go`)
  > emits a bare `codegen: …` string with no `line:col`. Threading them requires plumbing
  > `fset.Position(node.Pos())` into each error site — a real codegen change across multiple
  > files, split into its own increment. "Cheap" holds for parser; codegen is a meaningful
  > engineering task. See `codegen-diagnostic-position-audit.md` for the full site inventory
  > and per-site AST-node recommendations.

- **R10 — "Adding a corpus case" doc (closes G10).**
  *What:* a short `internal/corpus/README.md` (this is the one place a doc *is* warranted —
  it lives next to the thing it documents): the facet table, the render-safety rule, the
  `-update` workflow, the area→feature convention. *Why:* the spine is the contribution
  surface; make it self-serve. *Shape:* ~1 page. *Inspired by:* TS conformance README,
  Babel fixture docs.

### P1 — moderate, high-value

- **R5 — Grow the security corpus into a payload matrix (closes G5).**
  *What:* a `security/` expansion driven by a checked-in XSS payload list (OWASP cheat-sheet
  vectors) crossed with each emitted context (text / attr-quoted / attr-unquoted / URL /
  CSS / raw `<script>` / raw `<style>` / comment). Each row is a render case asserting the
  payload is neutralized. *Why:* highest-risk subsystem, currently 5 cases. *Shape:* a small
  generator that emits txtar cases from a payload×context table, then `-update`. *Inspired
  by:* XSS corpus testing norms; closure-templates' context matrix.

- **R4 — Document-level differential render vs `html/template` (closes G4).**
  *What:* for a subset of corpus cases expressible in both, render via gsx and via an
  equivalent `html/template` and compare structurally. Proves the *context classifier*
  (`emit.go:799+`) routes to the right escaper, not just that escapers are correct. *Why:*
  the classifier is the part R1 doesn't cover. *Shape:* harder (requires hand-authored
  template twins) — keep it to a curated dozen high-risk cases, not the whole corpus.
  *Inspired by:* differential testing; Go's contextual-autoescaping design.

- **R6 — Fuzz the full pipeline + URL sanitizer (closes G6).**
  *What:* `FuzzPipelineNeverLeaks` — generate valid-ish `.gsx`, codegen, render with attacker-
  controlled prop values, assert no raw `<`/`"`/`javascript:` survives in the wrong context;
  and `FuzzURLSanitize` mirroring the CSS fuzzer. Also `FuzzCodegenCompiles` (parse → gen →
  `go/parser` the output is syntactically valid). *Why:* fuzzing finds the bypasses humans
  miss; codegen producing uncompilable Go is a real failure class. *Shape:* medium; reuse the
  batch harness. *Inspired by:* std fuzzing + SWC's fuzz-the-codegen practice.

### P2 — bigger infra, do when the above is in place

- **R7 — Corpus-wide parser round-trip property (closes G7).** `parse → ast.Print → parse`
  AST-equality over every corpus input, as a property test alongside the existing printer
  property. Inspired by go/printer round-trip tests.
- **R8 — CI matrix + perf guard (closes G8).** GitHub Actions matrix over 2–3 Go versions ×
  {linux, darwin}; a `BenchmarkCorpusBatch` with a soft threshold so the single-batch win
  can't silently regress. Inspired by Go's own builders.
- **R2b — Coverage target.** Once R2 makes the number honest, set a floor (e.g. fail CI
  below a ratchet) for `parser` + `internal/codegen` + the escaper.

---

## 5. Anti-recommendations (don't do these)

- **Don't replace structural HTML comparison with byte-exact render goldens.** It would
  churn on every whitespace/formatter change and teach reviewers to rubber-stamp. The
  structural compare is a *feature*. (Design doc §6 already resolved this correctly.)
- **Don't pin `generated.x.go.golden` for many more cases.** They churn on every helper/
  formatting change and `-update` invites blind acceptance (G9). Keep the curated ~11; the
  render golden is the behavioral check.
- **Don't pursue multi-*module* fixtures or a `go.work` corpus.** The single-module import-
  rewrite already covers cross-package, which is all gsx's per-package go/packages codegen
  needs (design doc §7). go.work would add ~90 go.mod files for no new coverage.
- **Don't add a heavyweight third-party diff/snapshot library** (approvaltests, etc.). The
  existing diff output is adequate and the txtar+`-update` loop is already the snapshot
  workflow. Adding a dep here is pure overhead.
- **Don't migrate parser AST snapshotting into the corpus.** AST is the parser's product;
  it correctly stays in the parser layer (design doc §2). `ast.golden` as an opt-in escape
  hatch is the right amount.
- **Don't write a hand-rolled XSS "is this safe?" heuristic checker.** For R4/R5, the oracle
  is `html/template` (a real implementation) — diff against it; never approximate "safe."

---

## Sources

- Go `html/template` test suite (the escaping oracle): https://go.dev/src/html/template/template_test.go
- Contextual auto-escaping rationale (Go's model): https://security.googleblog.com/2009/03/reducing-xss-by-way-of-automatic.html
- templ testing approach (goquery / structural assertions): https://templ.guide/core-concepts/testing/
- closure-templates context-aware escaping matrix: https://github.com/google/closure-templates/blob/master/documentation/concepts/auto-escaping.md
- gsx corpus design doc: `docs/superpowers/specs/2026-06-20-corpus-testing-infrastructure-design.md`
