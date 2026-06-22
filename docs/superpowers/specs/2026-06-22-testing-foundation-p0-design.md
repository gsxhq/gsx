# Testing Foundation — Increment 1 (P0) Design

**Date:** 2026-06-22
**Status:** Approved (brainstorm), pending implementation plan
**Basis:** [2026-06-22-testing-architecture-review.md](2026-06-22-testing-architecture-review.md) (independent review; this implements its P0 group: R1, R2, R3, R10).

---

## 1. Goal & scope

Lay the first foundation stones for a comprehensive compiler + HTML-templating test suite (in the spirit of Go's `html/template`, rustc UI tests, TS baselines, Babel fixtures). This increment is the review's **P0 group**:

- **R1 — Differential escaper oracle vs `html/template`** (the #1 gap: the escaper's own comments name stdlib as its oracle, yet nothing checks parity).
- **R2 — Honest codegen coverage measurement** (the corpus exercises codegen in-process but `-cover` mis-reports it).
- **R3 — Position-annotated diagnostics**, *right-sized*: parser diagnostics already pin `line:col`; codegen diagnostics carry none and are deferred — so this increment delivers the **audit + convention doc**, not the annotation harness.
- **R10 — `internal/corpus/README.md`** (the contribution surface).

**Out of scope / explicitly preserved (anti-recommendations — do NOT change):** structural HTML comparison, the small curated `generated.x.go` golden set, no `go.work`, the single-batch corpus render. **Deferred** (with tradeoffs, §7): codegen position-threading + the `//~` harness, security payload×context matrix (R5), pipeline/codegen fuzzing (R6), document-level differential render (R4), parser round-trip (R7), CI matrix + perf guard (R8), coverage floor (R2b).

**Success criteria:**
- A continuously-run differential test asserts gsx escapers are byte-identical to `html/template`, with all known divergences captured in an explicit, commented allow-list (no silent skips).
- `make cover` reports honest cross-package coverage that attributes corpus-driven codegen execution to `internal/codegen`.
- The codegen-diagnostic position gap is audited (a checked-in list) and the diagnostic position convention is documented.
- `internal/corpus/README.md` lets a new contributor add a case unaided.
- The review doc's optimistic R3 framing is corrected.

---

## 2. R1 — Differential escaper oracle vs `html/template`

**Where:** new `escape_diff_test.go` in the root `gsx` package (next to `escape.go` + `fuzz_test.go`).

**What gsx exposes** (`escape.go`): `writeHTML(w, s)` (HTML text/attr escape), `urlSanitize(s)` (scheme allow-list → `about:invalid#gsx`, else returns `s` unchanged), `writeURL(w,s) = writeHTML(urlSanitize(s))`, `cssValueFilter(s)` (port of stdlib `css.go`, → `ZgotmplZ` on unsafe).

**Oracle per context** — render `{{.}}` through `html/template` in the matching context and compare:
| gsx | html/template oracle | how to obtain |
|---|---|---|
| `writeHTML(s)` | `template.HTMLEscapeString(s)` | exported — direct call |
| `writeURL(s)` (sanitize+escape) | rendered value of `<a href="{{.}}">` | `template.Execute` then extract the `href` value |
| `cssValueFilter(s)` | rendered value of `<style>{{.}}</style>` (or `style="{{.}}"`) | `template.Execute` then extract the value |

**Assertion:** byte-parity (`got == want`). Driven by (a) a curated input table (existing `FuzzCSSValueFilter` seeds + OWASP/known XSS vectors + boundary bytes) and (b) a new `FuzzEscaperMatchesStdlib(f)` that runs each fuzz input through every context comparison.

**Divergence policy:** a divergence is a **finding** — either a real bug (fix it) or a deliberate difference recorded in a commented allow-list, e.g.:
```go
// knownDivergences documents inputs where gsx INTENTIONALLY differs from
// html/template, with the reason. Every entry MUST have a justification; the
// differential test skips ONLY these exact (context,input) pairs.
var knownDivergences = map[diffKey]string{
    {ctxURL, "javascript:alert(1)"}: "gsx blocks unsafe URLs with about:invalid#gsx (a real URL) vs html/template's #ZgotmplZ sentinel — see escape.go:30",
    // ...
}
```

**Known pitfalls (must handle in the test):**
- **Layering:** the URL/CSS oracle is *render-and-extract* — `html/template` layers attribute HTML-escaping *on top of* URL/CSS filtering. The extraction must isolate the value as it appears in the rendered attribute, and gsx's `writeURL`/CSS emit path must be compared at the *same* layer (sanitize+attr-escape), not the raw filter alone.
- **Known sentinel divergence:** `urlSanitize` blocks with `about:invalid#gsx`; `html/template` uses `#ZgotmplZ`. The URL comparison WILL flag this → it is the first allow-list entry (intentional; `escape.go:30` documents it). CSS uses `ZgotmplZ` (matches stdlib), so CSS should not need a sentinel exception.
- The test is expected to surface at least one *unanticipated* divergence — investigate each before allow-listing.

---

## 3. R2 — Honest codegen coverage measurement

**Problem:** the corpus runs codegen (`emit.go`) **in-process** (`internal/corpus/batch.go` calls `codegen.GeneratePackages`; only the *render* is a `go run` subprocess), but `go test ./internal/corpus -cover` measures only `internal/corpus`'s own statements, so `internal/codegen` reads a false ~59.9% that hides genuinely dead branches.

**Fix (measurement, not new tests):** a new project-root **`Makefile`** (none exists today) with at least:
```
test:    ; go test ./... -count=1
cover:   ; go test -coverpkg=./... -coverprofile=cover.out ./... && go tool cover -func=cover.out | tail -1
cover-html: cover ; go tool cover -html=cover.out
```
`-coverpkg=./...` makes the corpus test's in-process codegen execution count toward `internal/codegen` coverage. Document `make cover` in the README. **No coverage floor/ratchet this increment** (R2b, deferred) — the goal is an honest, visible number so we can *see* which `emit.go`/escaper branches are untested.

**Pitfall:** `-coverpkg=./...` instruments every package, so the number includes test-only/CLI packages; report the `-func` summary and treat it as a baseline, not a gate (gating is R2b).

---

## 4. R3 — Position-annotated diagnostics (right-sized: audit + convention)

**Finding:** parser diagnostics already pin `line:col` in `diagnostics.golden` (e.g. `3:24: mismatched close tag`) — parser position-accuracy is *already* tested. Codegen diagnostics carry **no `.gsx` position** (`codegen: param name "ctx" is reserved`; the `fmt.Errorf("codegen:…")` sites don't thread token positions). Threading positions into codegen is a real codegen change (deferred, §7). Building a `//~` annotation harness *now* would have no consumer (parser is already covered; codegen positions don't exist yet) — premature infra.

**This increment delivers:**
1. **Audit artifact** — a checked-in list of every codegen diagnostic site lacking a `.gsx` position. Produced as a small `go test`-runnable generator OR a documented audit: grep `internal/codegen` for `fmt.Errorf("codegen:` / error-return sites, classify each as has-position / no-position, write the no-position list to `docs/superpowers/specs/codegen-diagnostic-position-audit.md` (the precise backlog for the next increment).
2. **Convention doc** — in `internal/corpus/README.md` (R10): diagnostics are pinned in `diagnostics.golden`; format is `[path:]line:col: message` where available; parser carries positions, codegen is the known gap.

The `//~` annotation harness is built **with** the codegen-position increment (§7), where it has real consumers.

---

## 5. R10 — `internal/corpus/README.md`

A one-page contributor guide next to the spine. Contents:
- **What the corpus is** — the txtar fixture spine, the single-batch render, structural HTML compare.
- **Facet table** — `input.gsx` (+ sibling `.go`), `invoke`, `diagnostics.golden`, `render.golden`, `generated.x.go.golden` (curated subset), `ast.golden` (parser-layer) — which are always-checked vs presence-based, and the parser-layer rule (pins `ast.golden` ⇒ no codegen).
- **The render-safety rule** — a renderable case (has `invoke`, no diagnostics) MUST have `render.golden`.
- **The `-update` workflow** — `go test ./internal/corpus -run TestCorpus -update`, and how to read `coverage.golden`.
- **Area→feature convention** — `testdata/cases/<area>/<scenario>.txtar`, naming.
- **Diagnostics position convention** (from R3) + a pointer to the audit.
- **`make cover`** (from R2) for seeing coverage.

---

## 6. File structure

- Create: `escape_diff_test.go` (root pkg) — R1 differential test + `FuzzEscaperMatchesStdlib` + `knownDivergences` allow-list.
- Create: `Makefile` (root) — `test`, `cover`, `cover-html`.
- Create: `internal/corpus/README.md` — R10.
- Create: `docs/superpowers/specs/codegen-diagnostic-position-audit.md` — R3 audit backlog.
- Modify: `docs/superpowers/specs/2026-06-22-testing-architecture-review.md` — correct the R3 "cheap" framing (note the codegen-position gap).
- (Possibly) extend `fuzz_test.go` if the new fuzzer fits better there; otherwise keep it in `escape_diff_test.go`.

No production code changes this increment (test/docs/tooling only) — unless R1 finds a real escaper bug, which would be fixed in `escape.go` (and noted).

---

## 7. Deferred work + tradeoffs / pitfalls

These are NOT in this increment. Recorded so they're not lost and the next implementer knows the traps. (High-level what/why also live in the review doc roadmap.)

- **Codegen position-threading** *(next increment; R3's real half).* Thread the relevant AST node's `.gsx` position into every `fmt.Errorf("codegen:…")` site so diagnostics read `line:col: codegen: …`.
  - **Pitfall:** touches many error sites across `internal/codegen` (`analyze.go`, `emit.go`, `batch.go`); **changes error output**, so it WILL break existing error-substring assertions — the corpus `diagnostics.golden` files and codegen unit tests must be regenerated/updated as part of it.
  - **Dependency:** consumes this increment's position audit (the site list).
  - **Pairs with:** building the `//~ ERROR` inline-annotation harness here (its first real consumer).
- **`//~ ERROR` annotation harness** *(deferred with the above).* rustc-style inline annotations checked against actual diagnostic spans.
  - **Pitfall:** must scan the **raw input text**, not the parsed AST (error-case inputs are deliberately malformed and may not parse); must coexist with `diagnostics.golden` without double-pinning the same fact.
- **R5 — security payload×context matrix.** XSS vectors × emitted contexts (text/attr-quoted/attr-unquoted/URL/CSS/raw-script/raw-style/comment). *Why deferred:* builds naturally on R1's oracle + context map. *Pitfall:* needs a generator that emits txtar cases from the payload table, kept deterministic.
- **R6 — pipeline/codegen fuzzing** (`FuzzPipelineNeverLeaks`, `FuzzURLSanitize`, `FuzzCodegenCompiles`). *Why deferred:* medium infra; reuses the batch harness. *Pitfall:* generating valid-ish `.gsx` + asserting no wrong-context leak is non-trivial; `FuzzCodegenCompiles` (output parses via `go/parser`) is the cheap subset.
- **R4 — document-level differential render vs `html/template`** for a curated dozen cases (validates the context classifier `emit.go:799+`, which R1 doesn't cover). *Pitfall:* requires hand-authored template twins; keep it small.
- **R7 — parser round-trip property** (parse→print→parse AST-equality over the corpus). *Pitfall:* relies on lossless printing; pair with the existing printer property tests.
- **R8 — CI matrix + perf guard** (2–3 Go versions × {linux,darwin}; `BenchmarkCorpusBatch` soft threshold). *Why deferred:* needs CI config; do once the above stabilizes.
- **R2b — coverage floor/ratchet.** *Depends on R2* making the number honest first; then gate `parser` + `internal/codegen` + escaper.

---

## 8. Testing / verification

- R1: `go test . -run TestEscaperMatchesStdlib -v` passes (with allow-list); `go test . -run FuzzEscaperMatchesStdlib -fuzz=... -fuzztime=30s` finds no un-allow-listed divergence in a short run. Each allow-list entry has a justification comment.
- R2: `make cover` runs green and prints a coverage % for `internal/codegen` materially higher than the old 59.9% (proving corpus codegen now counts). Record the before/after number in the plan.
- R3: the audit file lists the no-position codegen sites; README documents the convention.
- R10: README renders/reads correctly; a dry-run "add a case" following it works.
- Whole suite: `go test ./... -count=1` green; `go vet ./...` clean.
