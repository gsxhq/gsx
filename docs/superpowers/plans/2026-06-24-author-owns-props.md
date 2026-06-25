# author-owns-Props component model — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** A gsx component whose sole non-receiver param is an author-declared struct uses that type directly (no generated `<Name>Props`); inline-param components keep the generated path; nullary components generate no `Props`. Plus the spread operator migrates to Go-convention trailing `{ x... }` (overloaded for whole-struct splat).

**Architecture:** Spec `docs/superpowers/specs/2026-06-24-author-owns-props-design.md` (read it first). The change is centred in `internal/codegen` (`componentPropFieldsFor`, `childPropsLiteral`, the byo type-driven field set, external-`Props` preliminary type-load) plus the parser/printer (spread token) and a coordinated `tree-sitter-gsx` grammar update. emit ≡ probe is preserved by building the byo field-build/splat identically on both paths from the one resolved `Props` type.

**Tech Stack:** Go; `go/types` + `go/packages` (codegen resolution); the gsx parser/printer; `tree-sitter-gsx` (grammar.js); the txtar corpus harness.

## Global Constraints

- **emit ≡ probe:** every tag's `ChildProps`/byo literal + splat is emitted identically (modulo `rtPkg` alias `gsx`/`_gsxrt`) by `genChildComponent` (emit) and `emitProbes` (probe). Never diverge.
- **Runtime stays stdlib-only.** Escaping/auto-unwrap/pipeline behavior unchanged.
- **LSP preserved:** the codegen skeleton stays valid Go, `analysis.go`'s `ExprMap` is populated for attr/interp value exprs, and `//line` maps are correct. The `internal/lsp` corpus must stay green.
- **Per syntax change, ship per-context corpus coverage** (`internal/corpus/testdata/cases/**`); `go test ./internal/corpus/ -update` regenerates goldens AND `coverage.golden` (run twice to converge; a forgotten manifest bump fails the suite).
- After each task: `go build ./...` + `go vet ./...` + `go test ./...` green; `gsx fmt` faithful+idempotent over the corpus. Bump `internal/codegen/version.go` when emitted `.x.go` for a previously-valid input changes.
- **The default field matcher and the heuristic must match the spec exactly** (§2, §5): single non-receiver named-struct param → byo; inline → generated; nullary (no children/attrs) → no `Props`. Field match order: identifier→Capitalize, kebab→Camel, else fall through to the `Attrs` field.

---

### Task 1: Spread operator → Go-convention trailing `{ x... }`

**Files:**
- Modify: `parser/markup.go` (spread parsing — currently leading `{...expr}`), `parser/*` spread helper
- Modify: `internal/printer/printer.go:411-413` (`SpreadAttr` prints `{...`)
- Modify: corpus cases using `{...x}` + the rewritten structpages examples
- Test: `internal/corpus/testdata/cases/attrs/spread_trailing.txtar`, `.../spread_leading_rejected.txtar`

**Interfaces — Produces:** trailing-dots spread `{ expr... }` parses to the existing `*ast.SpreadAttr{Expr}` (AST unchanged — only the surface syntax/printer change). Leading `{...expr}` is no longer valid.

- [ ] **Step 1: Failing corpus** `attrs/spread_trailing.txtar`: `component C(attrs gsx.Attrs) { <div { attrs... }>x</div> }`, invoke `C(CProps{Attrs: gsx.Attrs{"id":"m"}})`, render `<div id="m">x</div>`. (Today: parse error — trailing not recognized.) Run `go test ./internal/corpus/ -run 'TestCorpus/attrs/spread_trailing'` → FAIL.
- [ ] **Step 2: Implement parser** — in the attribute parser (`parseAttrs`/`parseAttr` in `parser/markup.go`), recognize a brace attr whose inner Go expression ends with `...` (trailing) as a `SpreadAttr` (Expr = inner minus the trailing `...`, trimmed). Remove/replace the leading-`{...}` recognition. Keep `{ children... }`? No — children is `{children}` (no dots). Note `{ x... }` (trailing dots) is the spread; `{...x}` is no longer special (becomes an ordinary `{ }` interp of `...x`, which fails Go resolution → effectively rejected). Mirror the parser change for the component-tag position (used by Task 4's splat) — both element and component accept `{ expr... }`.
- [ ] **Step 3: Implement printer** — `internal/printer/printer.go` `*ast.SpreadAttr` case: print `{ ` + expr + ` }` with trailing `...` (i.e. `{ %s... }`) instead of leading `{...%s}`. Keep idempotent.
- [ ] **Step 4: Rejection case** `attrs/spread_leading_rejected.txtar`: `<div {...attrs}>x</div>` → a clear diagnostic (the leading form is gone). Pin the diagnostic.
- [ ] **Step 5: Migrate** every existing `{...x}` in `internal/corpus/testdata/cases/**` and in the rewritten structpages examples to `{ x... }`. Use `gsx fmt` to rewrite where possible; grep `grep -rl '{\.\.\.' internal/corpus/testdata/cases/` to find them.
- [ ] **Step 6: Run** `go test ./...` + `gsx fmt` idempotence over the corpus. `-update` corpus goldens. Green.
- [ ] **Step 7: Commit** `feat(parser): spread operator { x... } (Go-convention trailing); migrate corpus`.

---

### Task 2: Nullary unification — no `Props` for childless/attr-less nullary components

**Files:**
- Modify: `internal/codegen/analyze.go` (`componentPropFieldsFor` / the props-struct synthesis gate), `internal/codegen/emit.go` (`genComponent` props-struct emission, `childPropsLiteral` call-site emission for a nullary child)
- Test: `internal/corpus/testdata/cases/components/nullary_no_props.txtar`

**Interfaces — Consumes:** the existing Attrs/Children synthesis gate in `componentPropFieldsFor` (analyze.go ~150-165). **Produces:** a nullary component (zero non-receiver params, `usesChildren==false`, no fallthrough Attrs) emits `func Name() gsx.Node` (function) / `func (p T) Name() gsx.Node` (method) with NO props struct; child invocation emits `Name()` / `recvVar.Name()` (no `NameProps{}`).

- [ ] **Step 1: Failing corpus** `components/nullary_no_props.txtar`: `component Box() { <div>x</div> }` and `component Page() { <Box/> }`; pin `generated.x.go.golden` showing `func Box() gsx.Node` and the call `Box()` (NOT `Box(BoxProps{})`). Run → FAIL (today emits empty `BoxProps`).
- [ ] **Step 2: Implement** — in `componentPropFieldsFor`, a component with zero declared params AND `!usesChildren` AND no Attrs synthesis → record it as "no props struct" (a set/flag the emit + probe both read). In `genComponent` (emit.go), emit `func Name() gsx.Node` when no-props; in `buildSkeleton`/`emitComponentSkeleton`, mirror (`func Name() gsx.Node`). In `childPropsLiteral`'s callers (`genChildComponent`, `emitProbes`), when the child is no-props, emit `Name()` / `recvVar.Name()` with no literal. (Method components are ALREADY no-props when nullary — unify the function path to match; reuse that branch.)
- [ ] **Step 3:** Audit existing nullary corpus cases (e.g. `child_no_props`, `not_eligible_no_field`, anything emitting `XProps{}` for a nullary X) — update goldens via `-update`; EYEBALL that `func X()` / `X()` replaced `func X(XProps)` / `X(XProps{})`.
- [ ] **Step 4: Run** `go test ./...`; bump `version.go`. Green.
- [ ] **Step 5: Commit** `feat(codegen): nullary components generate no Props (function == method)`.

---

### Task 3: byo heuristic + field-build codegen

**Files:**
- Modify: `internal/codegen/analyze.go` (`componentPropFieldsFor` → derive byo vs generated; `resolveTypesPkg*` → preliminary type-load of external `Props` fields), `internal/codegen/emit.go` (`childPropsLiteral` byo branch)
- Test: `props/byo_single_struct.txtar` (seeded, RED), `props/byo_external_props.txtar`, `props/heuristic_boundaries.txtar`

**Interfaces — Consumes:** `go/types` resolution of each component's sole non-receiver param. **Produces:** `isByoComponent(propsType) bool`; for a byo component, `byoFields(structType) []fieldInfo` (exported field names + which are `gsx.Node` + whether an `Attrs gsx.Attrs` / `Children gsx.Node` field exists), derived from the resolved struct (or parsed GoChunk decl when in-`.gsx`). `childPropsLiteral` emits `Author Props{Field: val, …}` directly (no `<Name>Props`).

- [ ] **Step 1:** Make `props/byo_single_struct.txtar` (seeded) the RED anchor — confirm it fails today.
- [ ] **Step 2: Heuristic** — in `componentPropFieldsFor`, after parsing params, classify each component: sole non-receiver param whose declared type is a named struct → **byo** (record the props type = that struct's type name); else generated/nullary (existing path). The struct-ness check needs the resolved type — but the field SET for a `.gsx`-declared struct can be read from its GoChunk AST without resolution. For an EXTERNAL struct, add a **preliminary `go/packages` load** of the dir's existing `.go` files (valid Go without the `.gsx`) to enumerate the struct's exported fields BEFORE building the skeleton (mirror the `gsx.Val` probe discipline; see spec §9). Produce a `byoFields` map keyed by props-type-name → field info, available to both emit and probe.
- [ ] **Step 3: Field-build emit** — in `childPropsLiteral`, when the child is byo: split the tag's attrs using `byoFields` + the field matcher (Task 5; for now the default identifier+Capitalize rule): a matching field → `Field: value` (node fields get `gsx.Val`/`gsx.Text` promotion as today via the `nodeProps`-equivalent derived from the struct's `gsx.Node` fields); a non-matching attr → the `Attrs` field bag (error if no `Attrs gsx.Attrs` field — spec §6); children → the `Children` field (error if no `Children gsx.Node` field; auto-add only for `.gsx`-declared structs). Emit `propsType{…}` (the author type) — same under `gsx`/`_gsxrt`. Update the probe (`emitProbes`) to build the identical literal.
- [ ] **Step 4: External-Props case** `props/byo_external_props.txtar`: a component `component Card(p cardData)` where `type cardData struct{ Title string }` is in a sibling `data.go` (use the txtar `-- data.go --` section); `<Card title="Hi"/>` → `Card(cardData{Title:"Hi"})`; pin render + generated. Proves the preliminary type-load.
- [ ] **Step 5: Heuristic boundaries** `props/heuristic_boundaries.txtar`: a single-scalar-param component (generated), a multi-param component (generated), a single-struct-param component (byo) in one file; pin each generated signature.
- [ ] **Step 6: Children/Attrs missing-field errors** — corpus `props/byo_children_missing.txtar` (`{children}` but no `Children` field → clear error) and `props/byo_attrs_missing.txtar` (unmatched attr but no `Attrs` field → clear error).
- [ ] **Step 7: Run** `go test ./...` (incl. LSP package — must stay green); `-update`; bump `version.go`. EYEBALL byo goldens emit the author type and are emit≡probe. Green.
- [ ] **Step 8: Commit** `feat(codegen): bring-your-own Props — single struct param used directly (field-build)`.

---

### Task 4: whole-struct splat `{ x... }` on a component

**Files:**
- Modify: `internal/codegen/emit.go` (`childPropsLiteral` — splat path), parser already accepts `{ x... }` from Task 1 in the tag position
- Test: `props/byo_splat.txtar` (tag + method forms)

**Interfaces — Consumes:** Task 1's `{ expr... }` token in a component tag's attr list (a `SpreadAttr` on a component). **Produces:** a `SpreadAttr` on a byo component → `Name(expr)` (whole-struct splat), bypassing field-build. All-or-nothing (a splat plus other attrs is an error).

- [ ] **Step 1: Failing corpus** `props/byo_splat.txtar`: `component Card(p Props)` + `component Page(d Props) { <Card { d... }/> }` → `Card(d)`; and a method form `<p.Content { pd... }/>` → `p.Content(pd)`. Pin generated `Card(d)` + render. Run → FAIL.
- [ ] **Step 2: Implement** — in `childPropsLiteral`, when a byo component's attrs contain a `SpreadAttr` (the `{ x... }` splat): emit `propsType-target(expr)` directly — `Name(expr)` — instead of building a literal. Error if combined with other attrs ("a `{ x... }` splat passes the whole prop value; remove the other attrs"). Mirror in the probe. (On an ELEMENT a `SpreadAttr` stays the gsx.Attrs attribute spread — unchanged; the component-vs-element context selects behavior, per spec §8.)
- [ ] **Step 3:** Add `props/byo_splat_mixed_rejected.txtar` (splat + another attr → error). Pin diagnostic.
- [ ] **Step 4: Run** `go test ./...`; `-update`; bump `version.go`. Green.
- [ ] **Step 5: Commit** `feat(codegen): whole-struct splat { x... } on byo components`.

---

### Task 5: field matcher — kebab→Camel default + `gen.WithFieldMatcher`

**Files:**
- Create: `internal/codegen/fieldmatch.go` (default matcher), Test: `internal/codegen/fieldmatch_test.go`
- Modify: `gen/main.go` (add `WithFieldMatcher` option), `internal/codegen/*` (thread the matcher into `childPropsLiteral`'s byo split), `gen/info.go` (report in `gsx info --json`)
- Test: `props/byo_kebab_fields.txtar`, `internal/codegen/fieldmatch_test.go`

**Interfaces — Produces:** `type FieldMatcher func(attr string, fields []string) (field string, ok bool)`; `func defaultFieldMatcher(attr string, fields []string) (string, bool)` (identifier→Capitalize, kebab→Camel, else `"",false`); `gen.WithFieldMatcher(FieldMatcher)`. Default threaded everywhere a byo attr→field decision is made (Task 3 Step 3 used the inline default — replace with this).

- [ ] **Step 1: Failing unit test** `fieldmatch_test.go`: `defaultFieldMatcher("variant", []string{"Variant"})` → `("Variant", true)`; `("full-width", []string{"FullWidth"})` → `("FullWidth", true)`; `("aria-label", []string{"AriaLabel"})` → `("AriaLabel", true)`; `("data-id", []string{"Variant"})` → `("", false)`. Run → FAIL (undefined).
- [ ] **Step 2: Implement** `fieldmatch.go`: capitalize-first for identifiers; split kebab on `-`, Title-case each segment, join (`full-width`→`FullWidth`); return the field iff it's in `fields`, else `("", false)`. Pure, stdlib-only.
- [ ] **Step 3:** Thread `FieldMatcher` (default = `defaultFieldMatcher`) through codegen to the byo attr-split in `childPropsLiteral`; replace Task 3's inline default. `gen.WithFieldMatcher` sets it; fold into the build-cache manifest + `gsx info --json` (mirror `WithJSAttrs`).
- [ ] **Step 4: Corpus** `props/byo_kebab_fields.txtar`: a byo `Props{FullWidth bool; AriaLabel string; Attrs gsx.Attrs}` with `<Button full-width aria-label="Close" data-id="7"/>` → `Props{FullWidth:true, AriaLabel:"Close", Attrs:gsx.Attrs{"data-id":"7"}}`; pin generated + render.
- [ ] **Step 5: Run** `go test ./...`; `-update`. Green.
- [ ] **Step 6: Commit** `feat(codegen): byo field matcher (identifier + kebab→Camel) + gen.WithFieldMatcher`.

---

### Task 6: method pass-through + structpages end-to-end

**Files:**
- Verify/extend: `internal/codegen/*` (the byo heuristic from Task 3 already covers method components — confirm the receiver-excluded sole param drives byo)
- Test: `props/byo_method_shared_props.txtar` (seeded, RED), and re-validate the structpages examples
- Modify: the rewritten `~/personal/structpages/examples/{htmx-render-target,blog}` — remove the gap-#2 workarounds

- [ ] **Step 1:** Make `props/byo_method_shared_props.txtar` (seeded) GREEN: confirm `component (p home) Page(d pageData)` + `Content(d pageData)` both emit `func (p home) Page(d pageData) gsx.Node` / `func (p home) Content(d pageData) gsx.Node` (direct param). If Task 3's heuristic already excludes the receiver and treats the sole param as byo, this passes; otherwise fix the receiver-exclusion. Pin generated (both signatures) + render.
- [ ] **Step 2: Add** `props/byo_method_trio.txtar`: `Page`/`Content`/`Partial` all `(d pageData)`, invoking each via `home{}.Content(pageData{…})` etc. — proves one author type shared across three methods.
- [ ] **Step 3: Re-validate structpages** — in the rewritten `htmx-render-target` and `blog` examples, revert the Props workarounds (return the author `XProps` from `Props()`; restore the `Page`/`Content` split that blog had to collapse). Rebuild + the httptest smoke (full page + HTMX partial) green. (These live in the structpages repo, not gsx — run `go build`/`go test` there.)
- [ ] **Step 4: Run** gsx `go test ./...`; the structpages examples build+smoke. `-update` gsx corpus. Green.
- [ ] **Step 5: Commit** (gsx) `test(corpus): method pass-through — Page/Content/Partial share one author Props`.

---

### Task 7: tooling sync — `tree-sitter-gsx` grammar + docs guide

**Files:**
- Modify: `~/personal/gsxhq/tree-sitter-gsx/grammar.js` (`spread_attribute`; remove `optional('?')`; add component splat), regenerate; `tree-sitter-gsx/test/`, `queries/`
- Modify: `~/personal/gsxhq/gsx/docs/guide/syntax.md` (component-props model, trailing spread/splat)

- [ ] **Step 1: grammar.js** — `spread_attribute: $ => seq('{', $.go_expr, '...', '}')` (trailing) replacing leading `seq('{','...',$.go_expr,'}')`; remove the stale `optional('?')` on `expr_attribute` and interpolation holes (the `?` marker is gone); ensure a component tag accepts the same `{ expr... }` as a splat node (reuse `spread_attribute` in the tag attr position, or a `splat` alias). 
- [ ] **Step 2:** `cd tree-sitter-gsx && npx tree-sitter generate && npx tree-sitter test` — update `test/` corpus for the new spread/splat syntax and removed `?`; update `queries/highlights.scm`/`injections.scm` for any renamed node.
- [ ] **Step 3: docs** — `gsx/docs/guide/syntax.md`: document the author-owns-Props model (heuristic, field-build + `{ x... }` splat, explicit `Children`/`Attrs`), and the trailing spread `{ x... }`. (The site `gsxhq.github.io` syncs this at build — no site edit needed.)
- [ ] **Step 4: Commit** — two commits, one per repo: tree-sitter-gsx `feat(grammar): trailing spread/splat { x... }; drop removed ? marker`; gsx `docs(guide): author-owns-Props + trailing spread`.

---

### Task 8: LSP guard + final whole-branch review

**Files:**
- Verify: `internal/lsp/*` corpus stays green (no code change expected; the guard task confirms the redesign didn't break the skeleton/ExprMap/`//line`)
- Test: `internal/lsp/*_test.go`

- [ ] **Step 1:** Run the LSP test package against a byo component fixture: tag-name→decl and Go-expr (an attr value)→gopls both resolve; `ExprMap` covers byo field-build value exprs. Add an LSP test fixture with a byo component if none exists.
- [ ] **Step 2:** If any LSP test fails, fix the codegen skeleton (keep it valid Go + ExprMap populated + `//line` correct) — do NOT change LSP behavior; the redesign must keep the contract.
- [ ] **Step 3: Run** full `go build ./... && go vet ./... && go test ./...` green; `gsx fmt` idempotent over the corpus.
- [ ] **Step 4: Commit** `test(lsp): byo component resolves (skeleton/ExprMap/line preserved)`.
- [ ] **Step 5:** Dispatch the final whole-branch independent review (spec compliance + emit≡probe + LSP + tree-sitter sync) before merge.

---

## Self-Review

**Spec coverage:** §2 heuristic → T2 (nullary) + T3 (byo/generated split). §3 byo model → T3. §4 field-build + splat → T3 + T4. §5 field matcher → T5. §6 explicit Children/Attrs + contract-signature → T3 (errors) + T2 (nullary). §7 method pass-through → T6. §8 spread migration → T1. §9 codegen/external-Props/emit≡probe → T3. §10 LSP → T8. §11 migration → T1/T2/T6. §12 testing → corpus cases across T1–T6. §13 risks → addressed (preliminary load T3, spread migration T1, emit≡probe T3, LSP T8). §14 tooling → T7.

**Placeholder scan:** the deep codegen steps (T3) reference exact files + the spec's design + the seeded corpus anchors rather than fabricated full code, because the implementation must be written against the current `childPropsLiteral`/`componentPropFieldsFor` (which the implementer reads). The concrete TDD anchors (corpus `.txtar` inputs + render goldens, the `fieldmatch_test` cases) are complete. This is the same execution model as the prior gsx plans in this repo.

**Type consistency:** `isByoComponent`/`byoFields`/`FieldMatcher`/`defaultFieldMatcher`/`WithFieldMatcher` consistent across T3/T5; `SpreadAttr` (unchanged AST) across T1/T4; `propsType`/`rtPkg` reused as today.

## Risks

- **T3 external-`Props` preliminary type-load** is the riskiest new machinery — pin with `byo_external_props`; mirror `gsx.Val`'s probe discipline.
- **T1 spread migration is breaking** — wide but mechanical; `gsx fmt` rewrites; corpus + examples migrated in T1.
- **emit ≡ probe for byo** — derive the field set from the one resolved `Props` type for both paths; EYEBALL goldens.
- **LSP** — T8 is the guard; keep ExprMap/`//line` intact.
