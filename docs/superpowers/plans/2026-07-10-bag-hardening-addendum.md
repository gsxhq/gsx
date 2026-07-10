# Bag Hardening Addendum — single-pass helper + htmx preset

> Addendum to `2026-07-10-bag-hardening.md`, folding in two review outcomes on the open PR #76: collapse the unrolled per-name URL extraction into one runtime helper (less generated code AND fewer bag scans), and move htmx URL attrs off the always-on default into an opt-in preset. SDD execution, per-task review, on branch `worktree-bag-spread-hardening`.

**Goal:** Replace the ~16 unrolled `GetFold` extraction blocks + `SpreadURLPrefixed` + `WithoutFold` residual at each forwarding element with a single `gw.SpreadForwarding(...)` call that classifies each bag key in one pass; and gate the five `hx-*` URL names behind an opt-in `htmx` preset instead of the built-in default.

## Global Constraints
- Worktree `.claude/worktrees/bag-spread-hardening`, branch `worktree-bag-spread-hardening`. EVERY bash command starts with `cd /Users/jackieli/personal/gsxhq/gsx/.claude/worktrees/bag-spread-hardening && `; verify branch before commit.
- Runtime (root `gsx`) stdlib-only, NO `internal/attrclass` import — the helper takes plain `[]string` slices; codegen (which knows the tag statically) resolves nav/image/prefix/excluded names and the per-tag sink split at generate time.
- Security parity is the gate: every property the unrolled form guaranteed must hold for the helper — case-insensitive matching (`HREF` can't smuggle), last-wins scalar dedup, tag-aware nav/image sink, `RawURL` passthrough, forced/excluded suppression, prefix rules. The adversarial probe set from Task 7 re-runs against the helper.
- Regenerate goldens with `-update`, verify without. Task 8 re-pins forwarding goldens (URL attrs now render in bag position, not hoisted — a deliberate, better-order change; justify). Task 9 re-pins element goldens (hx-* declassified) + forwarding navNames slices.
- Commit per task, conventional messages + trailer `Claude-Session: https://claude.ai/code/session_01CNYE1sfyXuf7taK4mfUiqa`.

## Anchors (current at merge HEAD)
- URL extraction emission: `internal/codegen/emit.go:1111-1237` (urlNames loop 1119-1183 emitting GetFold blocks via `urlWriterMethod(tag,name)+"Val"`; `SpreadURLPrefixed` call 1210; residual `.WithoutFold(...)` 1237).
- `urlWriterMethod(tag,name)` `emit.go:2873` (→ "URLImage" if `attrclass.URLSink(tag,name)==SinkImage`, else "URL").
- Runtime pattern to generalize: `SpreadURLPrefixed` `attrs.go:321-345` (single pass: `lastValidAttrIndexes`, `validAttrName`, `URLPrefixMatch`, `attrNameExcluded`, `URLVal`); `attrNameExcluded` `attrs.go:349`.
- attrclass: `builtinURL` map `attrclass.go:205` (contains the 5 hx-* to remove); `URLExactNames()` 164, `URLPrefixes()` 188, `URLSink` (nav/image); `New`/`Builtin`.
- Config: `gen/configfile.go:32` (`URLAttrs`), merge 245; `gen/options.go:251` (`WithURLAttrs`); `gen/main.go:100` (`classifier()` → `attrclass.New(Rules{URL: cfg.urlRules}, nil)`).
- Corpus harness: `internal/corpus/loader.go` (`caseToml.URLAttrs` from Task 4), `batch.go` DirOptions.Classifier.

---

### Task 8: single-pass `SpreadForwarding` helper

**Files:** `attrs.go` (+helper, near SpreadURLPrefixed), `attrs_test.go`; `internal/codegen/emit.go` (replace the 1111-1237 emission); re-pinned `urlattrs/*`, `fallthrough/*bag*` goldens.

**Interfaces:**
- Produces `func (gw *Writer) SpreadForwarding(ctx context.Context, a Attrs, navNames, imageNames, prefixes, excluded []string)` — one pass over `a` in order, honoring `lastValidAttrIndexes` (scalar last-wins) and `validAttrName`; per surviving key: if `attrNameExcluded(key, excluded)` skip; else if fold-matches `imageNames` → `URLImageVal`; else if fold-matches `navNames` OR `URLPrefixMatch(key, prefixes)` → `URLVal`; else the plain Spread write (bool→`BoolAttr`, else `AttrValue`). class/style stay in `excluded` (emitted separately via ClassMerged/StyleMerged as today).

- [ ] Step 1: unit tests first — a bag mixing nav (`href`), image (`src` — pass imageNames=["src"]), prefix (`data-url-x`), excluded (`class`), plain (`data-n`), a case-variant (`HREF`) with unsafe value, a `RawURL`, and a scalar duplicate — asserting one pass renders each correctly, `HREF` sanitized+lowercased-or-dropped-consistent, last-wins, order preserved for plain keys. Model on existing `SpreadURLPrefixed` tests.
- [ ] Step 2: implement the helper (generalize SpreadURLPrefixed; keep `attrNameExcluded`/`URLPrefixMatch`/`lastValidAttrIndexes`). Decide `SpreadURLPrefixed`'s fate: if no caller remains after Task 8's emission change, remove it + its tests (grep first); if kept for compat, note why.
- [ ] Step 3: codegen — replace the emission at emit.go:1111-1237 with: compute at generate time (tag known) `navNames`/`imageNames` by splitting `cls.URLExactNames()` through `urlWriterMethod(tag,·)`, `prefixes := cls.URLPrefixes()`, and the existing `excluded` set (class/style/forced/dropVar); emit one `_gsxgw.SpreadForwarding(ctx, <bagExpr>, <navLit>, <imgLit>, <prefixLit>, <excludedExpr>)`. Slices as deterministic sorted literals (or a shared generated var if identical across sites — inline literal is simplest/collision-free; keep sorted for determinism). The post-cond dynamic drop (`dropVar`) still flows into `excluded` exactly as now.
- [ ] Step 4: regen; audit — forwarding goldens collapse to the single call; URL attrs move to bag position (justify as order-preserving improvement); NO change to element (static-attr) goldens. Verify idempotence (double -update byte-identical). Full gate + `make check`.
- [ ] Step 5: commit `feat(codegen): single-pass SpreadForwarding replaces unrolled URL extraction`.

---

### Task 9: htmx URL attrs → opt-in preset

**Files:** `internal/attrclass/attrclass.go` (remove hx-* from builtinURL; add preset table + lookup), `attrclass_test.go`; `gen/configfile.go` + `gen/options.go` + `gen/main.go` (url_presets config + WithURLPreset + resolution); `internal/corpus/loader.go` (caseToml.URLPresets); `docs/guide/config.md`; re-pinned element + forwarding goldens; a corpus case pinning the preset.

**Interfaces:**
- attrclass: `func Preset(name string) (Rules, bool)` returning the named preset's rules (`htmx` = the five exact names `hx-get/post/put/delete/patch` as URL rules — NOT a `hx-` prefix, which would wrongly classify hx-swap/hx-target). `builtinURL` loses the five hx-* entries.
- gen: `WithURLPreset(names ...string) Option` and gsx.toml `url_presets = ["htmx"]` (`tomlConfig.URLPresets`), both resolving each name via `attrclass.Preset` (unknown name = clear config error) and appending its rules into `cfg.urlRules` before `classifier()`.
- corpus `caseToml.URLPresets []string` resolved the same way into the case Classifier.

- [ ] Step 1: attrclass — failing test: `Builtin().Context("hx-get")` is now `CtxPlain` (not CtxURL); `Preset("htmx")` returns the 5 rules; a classifier built with the htmx preset's rules classifies `hx-post` as CtxURL again; unknown preset → not-found. Then remove hx-* from builtinURL + add the preset table/lookup.
- [ ] Step 2: gen wiring — `WithURLPreset` + `url_presets` toml, resolution + error on unknown. Unit-test the resolution if gen has a config test; else rely on the corpus case.
- [ ] Step 3: corpus harness — `caseToml.URLPresets`, resolved into the per-case Classifier (mirror URLAttrs threading from Task 4).
- [ ] Step 4: corpus cases — `urlattrs/htmx_preset.txtar`: a case `gsx.toml` with `url_presets = ["htmx"]`, an element `<a hx-get={expr}/>` with a `javascript:` value → sanitized (proves preset re-enables). And a sibling/second case with NO preset showing `hx-get={javascript}` renders raw (default no longer classifies) — pin the new default honestly. A bag variant if cheap.
- [ ] Step 5: regen; audit — element goldens for existing cases using `hx-get/post/...` URL classification now render those unsanitized/plain (this is the intended default change — enumerate each changed case and confirm it's a hx-* declassification, nothing else); forwarding navNames literals shrink by the 5 names. Idempotence check. Full gate + `make check`.
- [ ] Step 6: docs — config.md: the `hx-*` line moves from "built-ins cover … htmx method attributes" to a documented `url_presets = ["htmx"]` opt-in; note the default change. ROADMAP if a line is warranted.
- [ ] Step 7: commit `feat(config): htmx URL attrs behind an opt-in preset, off by default`.

---

### Task 10: re-validation + PR update

- [ ] Step 1: `make ci` + `make lint` green at HEAD.
- [ ] Step 2: adversarial re-probe — the Task-7 security probes re-run against the helper build (multi-hop laundering, case-smuggle matrix, concat conflict, RawURL every position, prefix-vs-Datastar, forced-vs-bag, single-eval) — the helper must preserve every verdict. Plus a helper-specific probe: a URL key and a plain key with the SAME fold-name (degenerate) and a bag whose only keys are excluded (helper writes nothing). Throwaway probe programs, not diff reading.
- [ ] Step 3: one-learning revalidation with the preset — its gsx.toml gains `url_presets = ["htmx"]` in the throwaway; confirm generate/build/test parity with the pre-addendum branch binary and that hx-* URLs stay sanitized (preset on) while generated code shrinks to the single call. Diff a few pages.
- [ ] Step 4: final whole-branch review of the addendum commits (the pattern that caught 5 defects — do not skip); fix wave if needed.
- [ ] Step 5: update PR #76 body (single-call emission; htmx opt-in; note the default change for htmx users in a **Breaking for htmx projects** line: add `url_presets=["htmx"]`). Push.
