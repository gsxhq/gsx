# Bag Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resolve everything at the leaf element — forwarding precedence for declared `gsx.Attrs` prop-field spreads, generate-time URL extraction through the tag-aware sinks, and call-site bag concatenation instead of `.Merge()` chains — per `docs/superpowers/specs/2026-07-10-bag-spread-hardening-design.md`.

**Architecture:** Part C swaps `childPropsLiteral`'s `.Merge()` chain composition for a single `ConcatAttrs` call (leaf render semantics already resolve duplicates). Part B extends the forwarding classifier from the token `attrs` to a byo component's declared bag field (`p.Attrs`), routing those elements through the existing `emitFallthroughAttrs` machinery. Part A adds a `Get`-extraction block to `emitFallthroughAttrs` that routes `[[urlAttrs]]`-classified names through new RawURL-aware writer methods, with the name set resolved at generate time. Forwarding emission is emit-pass-only — the probe needs no per-element parity (verified: `bagSpreadIndex`/`emitFallthroughAttrs` exist only in emit.go; shared expression builders carry emit≡probe).

**Tech Stack:** Go, existing gsx internals (`internal/codegen`, `internal/attrclass`, root runtime), txtar corpus.

## Global Constraints

- Worktree `.claude/worktrees/bag-spread-hardening`, branch `worktree-bag-spread-hardening`. EVERY bash command starts with `cd /Users/jackieli/personal/gsxhq/gsx/.claude/worktrees/bag-spread-hardening && `; verify `git branch --show-current` before committing.
- Runtime (root `gsx` package) stays standard-library only. It MAY import `internal/attrclass` (same module, dep-free) if needed — but prefer not to.
- No new `packages.Load`. No `Spread` signature change (spec: hand-written `gw.Spread` keeps today's contract).
- Never hand-edit goldens. Regen: `go test ./internal/corpus -run TestCorpus -update`, verify without `-update`. Coverage manifest regenerates with it.
- **Part C equivalence proof is a hard gate:** after the Task 3 regen, `git diff --stat` over `internal/corpus/testdata` must show changes ONLY in `*.txtar` `generated.x.go.golden` sections and `coverage.golden` — if any `render.golden` or `diagnostics.golden` content changes, semantics drifted: STOP, diagnose, do not commit.
- URL name matching is case-insensitive everywhere (attrclass `Rule.matches` lowercases; extraction must too).
- Inner loop `go test ./internal/codegen ./internal/corpus`; per task `make check`; before merge `make ci` + `make lint`.
- Commit per task, conventional messages, each with trailer:
  `Claude-Session: https://claude.ai/code/session_01CNYE1sfyXuf7taK4mfUiqa`

## Shared code anchors (read before editing; line numbers current at branch point 0bdcba3)

| What | Where |
|---|---|
| Element dispatch + one-spread rejection | `emit.go:1605-1634` (`genNode` `*ast.Element` case; `bagSpreadIndex` at 1615) |
| `bagSpreadIndex` (token-based classifier to extend) | `emit.go:1346` (doc 1338-1345); `valueIdents` `analyze.go:3469` |
| `emitManualSpreadElement` (derived-bag temp hoist, tag, nonce) | `emit.go:1284-1336` |
| `emitFallthroughAttrs` (THE forwarding block) | `emit.go:785-1186`: Has-guards 852-866; forced-after 821-848 + 1147-1184; `ClassMerged` 1003; `StyleMerged` 1018-1053; residual `Spread(...Without("class","style",forced...))` 1061-1070 |
| Static `href={x}` under guard already emits `gw.URL` | `emitExprAttr` `emit.go:3345` (`cls.Context==CtxURL && !isRawURL`); sink method `urlWriterMethod` `emit.go:2634-2639` |
| `childPropsLiteral` bag composition (Part C target) | `emit.go:4712`; `bag`/`mergeChain`/`attrsLitIdx` 4772-4775; `.Merge(spread)` 4925, `.Merge(cond)` 4942; final composition 5023-5043; `oaMergePrefix` hoist rebuilds at `emit.go:4024-4025`, `4408-4409`, `4634-4635` (attrsOnlyBagExpr sibling); probe caller `analyze.go:1364` (`probeWrap=true`), attrs-only probe `analyze.go:1323` |
| byo param binding (`p` = author's name, body uses `p.Attrs` verbatim) | `emit.go:587-609` (param name at 599/601); generated/manual components bind `attrs := _gsxp.Attrs` at `emit.go:682` (probe mirror `analyze.go:1109`) |
| `attrsProps` facts (propsType → gsx.Attrs field-name set) | built `analyze.go:99-128` (`attrsOut`); threaded through emit fns + `emitContext.attrsProps` `emit.go:1749` — keyed by CHILD propsType today; Part B needs the CURRENT component's entry |
| attrclass | `internal/attrclass/attrclass.go`: `Context` 84-126; `builtinURL` 159-164 (href, src, action, formaction, poster, cite, ping, data, background, manifest, xlink:href, hx-get/post/put/delete/patch); `Rules()` 135-140 (user rules only); `URLSink(tag,name)` 190-207 (`src`→image on img/source/input; `poster`→video; `background`→any); `Fingerprint()` 147-155 |
| Config → classifier | `gen/configfile.go:29-37` (`URLAttrs []tomlRule`), merge `234-245`; `gen/options.go:251` `WithURLAttrs`; `gen/main.go:100-102` `cfg.classifier()`; threaded `cache.go:90/96` |
| class_merger emitted-expression precedent | `emit.go:130-137` (mergeExpr decision), `311-315` (conditional import under `_gsxcm`), `classMergeExpr` `emit.go:408-413` |
| Corpus per-case config | `internal/corpus/loader.go:32-35` (`caseToml`: ClassMerger, FilterPackages only), parse 100-118; flow `batch.go:114-142` (`codegen.DirOptions{FilterPkgs, ClassMerger}`); `codegen.go:72-79` (no Classifier set); `DirOptions` `internal/codegen/module.go:31-34` (no Classifier field — Part A adds one) |
| Runtime Spread / Attrs | `attrs.go:166` (`Spread`), `Attrs` doc + `Merge` (class/style-aware) `attrs.go:126` |

---

### Task 1: runtime helpers — `ConcatAttrs`, `URLVal`, `URLImageVal`

**Files:**
- Modify: `attrs.go` (ConcatAttrs near Merge), `writer.go` (URL value methods near `URL`/`URLImage`)
- Test: `attrs_test.go`, `writer_test.go`

**Interfaces:**
- Produces: `func ConcatAttrs(bags ...Attrs) Attrs` (root pkg) — single-allocation concatenation, nil segments skipped, nil result for zero total entries. Consumed by Task 3's emission as `_gsxrt.ConcatAttrs(...)`.
- Produces: `func (gw *Writer) URLVal(v any)` and `func (gw *Writer) URLImageVal(v any)` — write a complete attribute VALUE (no quotes/name): `gsx.RawURL` → verbatim but attribute-escaped (`AttrValue(string(raw))`); anything else → stringified (`toStr`) then the corresponding sanitizer (`writeURL` / `writeURLImage`). Consumed by Task 5's extraction blocks.

- [ ] **Step 1: Failing tests**

```go
// attrs_test.go
func TestConcatAttrs(t *testing.T) {
	a := Attrs{{Key: "a", Value: "1"}}
	b := Attrs{{Key: "b", Value: "2"}, {Key: "a", Value: "3"}}
	got := ConcatAttrs(a, nil, b)
	want := Attrs{{Key: "a", Value: "1"}, {Key: "b", Value: "2"}, {Key: "a", Value: "3"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if ConcatAttrs() != nil || ConcatAttrs(nil, Attrs{}) != nil {
		t.Fatalf("empty concat must be nil")
	}
	// input bags must not be aliased: mutating the result must not touch a or b
	got[0].Value = "mut"
	if a[0].Value != "1" {
		t.Fatalf("ConcatAttrs aliased its input")
	}
}
```

```go
// writer_test.go — follow the file's existing writer-test harness style (buffer + W())
func TestURLVal(t *testing.T) {
	cases := []struct{ v any; want string }{
		{"https://x/y", "https://x/y"},
		{"javascript:alert(1)", "about:invalid#gsx"},
		{RawURL("app://z"), "app://z"},
		{RawURL(`a"b`), "a&#34;b"}, // RawURL still attribute-escaped
	}
	for _, c := range cases {
		var buf bytes.Buffer
		gw := W(&buf)
		gw.URLVal(c.v)
		if err := gw.Err(); err != nil || buf.String() != c.want {
			t.Fatalf("URLVal(%v) = %q, %v; want %q", c.v, buf.String(), err, c.want)
		}
	}
}

func TestURLImageVal(t *testing.T) {
	var buf bytes.Buffer
	gw := W(&buf)
	gw.URLImageVal("data:image/png;base64,AAAA")
	if got := buf.String(); got != "data:image/png;base64,AAAA" {
		t.Fatalf("got %q", got)
	}
	buf.Reset()
	gw = W(&buf)
	gw.URLVal("data:image/png;base64,AAAA") // nav sink rejects data:
	if got := buf.String(); got != "about:invalid#gsx" {
		t.Fatalf("nav sink must reject data:image, got %q", got)
	}
}
```

Adjust expected escaping (`&#34;` vs `&quot;`) to what `writeHTML` actually produces — check an existing `AttrValue` test.

- [ ] **Step 2:** `go test . -run 'TestConcatAttrs|TestURLVal|TestURLImageVal' -count=1` → FAIL (undefined).

- [ ] **Step 3: Implement**

```go
// attrs.go — next to Merge
// ConcatAttrs concatenates bags in order into one new bag, preserving every
// pair (duplicates included). It does NOT dedupe or class-merge: rendering
// resolves duplicates at the leaf (Spread is last-wins on scalar keys and
// aggregates class/style), and Get/Has are last-wins by contract — so
// concatenation is observably equivalent to eager Merge for every consumer
// of the documented Attrs semantics. Generated call sites use it instead of
// .Merge() chains (one allocation instead of one per link). nil segments are
// skipped; a zero-entry result is nil.
func ConcatAttrs(bags ...Attrs) Attrs {
	n := 0
	for _, b := range bags {
		n += len(b)
	}
	if n == 0 {
		return nil
	}
	out := make(Attrs, 0, n)
	for _, b := range bags {
		out = append(out, b...)
	}
	return out
}
```

```go
// writer.go — next to URL/URLImage
// URLVal writes v as a navigational-URL attribute value: a gsx.RawURL is the
// author's vouch and is emitted verbatim (still attribute-escaped); any other
// value is stringified and scheme-sanitized like URL. Generated code uses it
// for URL-classified bag attributes, where the value is dynamic (any).
func (gw *Writer) URLVal(v any) {
	if gw.err != nil {
		return
	}
	if r, ok := v.(RawURL); ok {
		gw.err = writeHTML(gw.w, string(r))
		return
	}
	gw.err = writeURL(gw.w, toStr(v))
}

// URLImageVal is URLVal for image-resource sinks (data:image/* permitted).
func (gw *Writer) URLImageVal(v any) {
	if gw.err != nil {
		return
	}
	if r, ok := v.(RawURL); ok {
		gw.err = writeHTML(gw.w, string(r))
		return
	}
	gw.err = writeURLImage(gw.w, toStr(v))
}
```

- [ ] **Step 4:** tests pass; `go test . -count=1` (whole root pkg) green.
- [ ] **Step 5:** Commit `feat(runtime): ConcatAttrs and RawURL-aware URLVal/URLImageVal writers`.

---

### Task 2: Part C — call sites concatenate

**Files:**
- Modify: `internal/codegen/emit.go` (composition at 5023-5043; `oaMergePrefix` rebuilds at 4024-4025, 4408-4409, 4634-4635; the `.Merge(` appends at 4925/4942 become plain segment collection)
- Create: `internal/corpus/testdata/cases/attrs/dup_iteration.txtar`

**Interfaces:**
- Consumes: `_gsxrt.ConcatAttrs` (Task 1).
- Produces: call-site bag expressions of the form `_gsxrt.ConcatAttrs(_gsxrt.Attrs{…base…}, spreadExpr, condExpr, …, oaLit)` — Tasks 3/5 read goldens containing this shape.

- [ ] **Step 1: Read the composition region** (`emit.go:4712-5043`) and the three `oaMergePrefix` rebuild sites in full before editing.

- [ ] **Step 2: Restructure composition.** Replace the string `mergeChain []string` of `.Merge(x)` suffixes with `segments []string` of bare expressions, collected at the same two sites (4925: `segments = append(segments, spreadExpr)`; 4942: `segments = append(segments, condExpr)`). Final composition (5023-5043):
  - base only, no segments, no OA literal → keep today's plain `"<rtPkg>.Attrs{…}"` (no wrapper call — zero churn for the common simple case).
  - otherwise → `"<rtPkg>.ConcatAttrs(" + join(all parts, ", ") + ")"` where parts = base literal (omit if `len(bag)==0`), segments in order, and the OA literal LAST when `attrsLitIdx >= 0` (this preserves merge-last; the field entry becomes `Attrs: <concatExpr>` with `oaMergePrefix` carrying the concat prefix so the hoist rebuilds compose the same way — update all three rebuild sites from `prefix.Merge(lit)` to `<rtPkg>.ConcatAttrs(prefixParts…, lit)`; keep the `oaMergePrefix` field storing whatever representation makes the rebuild simplest, and document it).
  - `attrsOnlyBagExpr` (4634-4635 region) shares the builder — verify it composes identically.
  The probe path calls the same builder with `probeWrap=true` — no separate change, but confirm the probe expression compiles (ConcatAttrs is a plain call; `_gsxrt` alias exists in skeletons).

- [ ] **Step 3: Regen + THE EQUIVALENCE GATE.**

```bash
go test ./internal/corpus -run TestCorpus -update
go test ./internal/corpus -run TestCorpus -count=1
git diff --stat internal/corpus/testdata | tail -20
git diff internal/corpus/testdata -- '*render.golden*' | head   # must be EMPTY
```

Inspect the txtar diffs: only `generated.x.go.golden` sections may change (`.Merge(` chains → `ConcatAttrs(`), plus `coverage.golden` if cases were added. Any render/diagnostics change = STOP per Global Constraints. Quote 2-3 before/after generated snippets in your report.

- [ ] **Step 4: `dup_iteration.txtar`** — pin the observable contract change:

```
# Call-site bags concatenate; a component iterating its bag sees duplicates
# (within the documented duplicate-tolerant contract). Render resolves
# last-wins as always.
-- input.gsx --
package views

import "github.com/gsxhq/gsx"

type echoProps struct {
	Attrs gsx.Attrs
}

component echoLen(p echoProps) {
	<div data-n={len(p.Attrs)}>{p.Attrs.Get("a")}</div>
}

component Page(extra gsx.Attrs) {
	<echoLen a="1" { extra... }/>
}
-- invoke --
Page(PageProps{Extra: gsx.Attrs{{Key: "a", Value: "2"}}})
-- diagnostics.golden --
-- generated.x.go.golden --
-- render.golden --
<div data-n="2">2</div>
```

Check `Get` returns `(any, bool)` — adjust the interpolation (`{p.Attrs.Get("a")}` returns a tuple; use the documented pattern for that, or `{ p.Attrs.Take("a") }`-style single value — simplest: a GoBlock `{{ v, _ := p.Attrs.Get("a") }}` then `{v}`; mirror an existing corpus case that reads a bag). The pins that matter: `data-n="2"` (duplicates visible to len) and value `2` (Get last-wins).

- [ ] **Step 5:** Full gate `go test ./internal/codegen ./internal/corpus -count=1` + `make check` → green. Benchmark spot-check: `go test . -bench 'BenchmarkRootAttr|BenchmarkClass' -benchtime 2x -run xx | tail -5` (no regression expected; allocs should drop where chains existed — quote numbers).
- [ ] **Step 6:** Commit `feat(codegen): call-site bags concatenate; leaf resolves (ConcatAttrs)`.

---

### Task 3: Part B — declared bag fields are forwarding positions

**Files:**
- Modify: `internal/codegen/emit.go` (`bagSpreadIndex` + `genNode` element case + whatever threading gets the CURRENT component's bag facts there)
- Create: `internal/corpus/testdata/cases/fallthrough/byo_bag_defaults.txtar`, `byo_bag_forced.txtar`, `byo_bag_class_merge.txtar`, `byo_bag_derived.txtar`, `byo_bag_two_spreads.txtar`, `local_bag_inline.txtar`

**Interfaces:**
- Consumes: existing `emitManualSpreadElement`/`emitFallthroughAttrs` unchanged; `attrsProps` facts; byo param name (`emit.go:599/601`).
- Produces: `bagSpreadIndex(attrs []ast.Attr, bagBases []string) (int, bool, error)` — extended signature; `bagBases` holds the spread-base spellings valid in the CURRENT component (`"attrs"` always; plus `"<param>.<Field>"` for each gsx.Attrs field of a byo component's struct). Task 5 consumes the same classified elements.

- [ ] **Step 1: Failing corpus case first** — `byo_bag_defaults.txtar`:

```
# composition.md §Precedence must hold for a byo component's declared bag:
# statics before { p.Attrs... } are defaults, caller overrides; no duplicate
# attributes in output.
-- input.gsx --
package views

import "github.com/gsxhq/gsx"

type props struct {
	Attrs gsx.Attrs
}

component Chip(p props) {
	<span a="b" data-k="v" { p.Attrs... }>x</span>
}

component Page() {
	<Chip a="c"/>
}
-- invoke --
Page()
-- diagnostics.golden --
-- generated.x.go.golden --
-- render.golden --
<span a="c" data-k="v">x</span>
```

Run `go test ./internal/corpus -run TestCorpus -count=1` → FAIL today (renders `a="b" a="c"`, and the html-structural compare sees the attr set differ). Record the failure output.

- [ ] **Step 2: Compute the current component's bag bases.** In `genComponent` (or wherever the byo param name is parsed, `emit.go:587-609`): for a byo component, look up the struct's gsx.Attrs field set — it is published in `attrsProps` under the STRUCT type name (built at `analyze.go:99-128`; byo publication per the `componentPropFieldsFor` doc) — and build `bagBases = ["attrs", "<param>.<Field>"...]`. For generated/manual components, `bagBases = ["attrs"]` (unchanged behavior). Thread `bagBases` down to `genNode`'s element case (follow how `recvVar`/`recvTypeName` are threaded; if the fan-out is wide, prefer adding it to the existing `emitContext` struct — `emit.go:1749` — if that context reaches genNode; read first, choose the narrower).

- [ ] **Step 3: Extend `bagSpreadIndex`.** Current: matches a SpreadAttr whose expr's `valueIdents` contain `attrs`. Extended: match if EITHER (a) `valueIdents(expr)["attrs"]` (existing), or (b) the expr, after trimming, starts with one of the dotted bases followed by end-of-expr, `.`, or `(`-chain — i.e. `p.Attrs`, `p.Attrs.Without(…)`, `p.Attrs.Merge(x)` all match base `p.Attrs`. Implement the base match on the token stream or with a precise prefix check that requires a word boundary (must NOT match `pp.Attrs` or `p.AttrsX`). Return which base matched if the callers need it (they don't today — `emitManualSpreadElement` hoists the whole expr into a temp for any non-`attrs` expr, which now includes `p.Attrs`: verify the hoist at `emit.go:1290-1300` handles it and the emitted temp is evaluated once).
  Also update the second-spread error path (`emit.go:1622-1632`) — two spreads where either references any base is the same generate-time error; `byo_bag_two_spreads.txtar` pins the message (write the case with `{ p.Attrs... }` twice; let -update capture the existing error wording).

- [ ] **Step 4: Route classified elements through `emitManualSpreadElement`.** The dispatch at `emit.go:1605-1634` already does this once `bagSpreadIndex` matches — verify no other call sites of `bagSpreadIndex` need the new argument (grep; update all).

- [ ] **Step 5: Remaining corpus cases** (each mirrors an existing `fallthrough/` case's shape — read `cases/fallthrough/call_site_caller_wins.txtar` and `static_class_merge.txtar` first):
  - `byo_bag_forced.txtar`: static AFTER `{ p.Attrs... }` wins over caller value; render pins forced value, generated golden pins unguarded emission after the spread.
  - `byo_bag_class_merge.txtar`: root `class="card"` + caller class through the bag → ONE merged class attribute (`class="card hl"` shape); style analog optional.
  - `byo_bag_derived.txtar`: `{ p.Attrs.Without("id")... }` — id dropped, rest forwarded with defaults semantics; generated golden pins the `_gsxvN` temp hoist.
  - `local_bag_inline.txtar`: a GoBlock local `{{ b := gsx.Attrs{{Key: "a", Value: "c"}} }}` spread `<span a="b" { b... }>` — pins that locals KEEP today's inline duplicate-emitting behavior (render golden will show the duplicate-attr artifact per the html compare's attr-set semantics — if the structural compare makes this unpinnable, pin via generated golden only and say so).
- [ ] **Step 6:** Regen, verify, audit (`git status`: only new/changed fallthrough cases + coverage + emit.go). Full gate + `make check`. NOTE: existing byo corpus cases that spread `p.Attrs` (grep `p.Attrs...` and the attrsonly cases' `{ p.Attrs... }`) will legitimately change generated+render goldens — the render changes must all be duplicate-elimination/merge improvements; justify EACH in the report (this is the one task where render.golden may move).
- [ ] **Step 7:** Commit `feat(codegen): declared gsx.Attrs prop-field spreads get forwarding precedence`.

---

### Task 4: corpus harness — per-case `[[urlAttrs]]`

**Files:**
- Modify: `internal/corpus/loader.go` (caseToml + caseDoc), `internal/corpus/batch.go` (DirOptions flow), `internal/corpus/codegen.go` (if the per-dir plumbing needs it), `internal/codegen/module.go` (DirOptions gains `Classifier *attrclass.Classifier`)
- Test: covered by Task 5's corpus cases; this task's gate is "existing suite still green + a throwaway case proves the field reaches codegen"

**Interfaces:**
- Produces: `caseToml.URLAttrs []struct{ Name, Prefix string }` (toml `urlAttrs`), flowing to `codegen.DirOptions.Classifier` built via `attrclass.New(attrclass.Rules{URL: rules}, nil)`; per-dir classifier overrides `Options.Classifier` for that dir. Consumed by Task 5's cases.

- [ ] **Step 1:** Read how `DirOptions.ClassMerger` is consumed per-dir inside codegen (grep `PerDir`/`DirOptions` in `internal/codegen/*.go`) and mirror for `Classifier`, including the generation cache key: `attrclass.Fingerprint()` exists (attrclass.go:147-155) for exactly this — find where the ClassMerger affects the per-dir cache/fingerprint (if it does) and treat Classifier the same; if per-dir inputs don't feed a cache key in the corpus path, note that and move on (the corpus regenerates cold).
- [ ] **Step 2:** Extend `caseToml` (loader.go:32-35) with `URLAttrs []caseURLRule` (`toml:"urlAttrs"`), validate exactly-one-of via `attrclass.Rule.Valid`, store resolved `[]attrclass.Rule` on `caseDoc`, thread through `batch.go:136-139` into `DirOptions`.
- [ ] **Step 3:** Prove the plumbing with a temporary case (a `gsx.toml` with `[[urlAttrs]] name = "data-x"` and an element `data-x={expr}` — the ELEMENT path already consumes the classifier, so the render golden showing sanitization proves threading). Keep it as a permanent case: `internal/corpus/testdata/cases/urlattrs/element_custom_rule.txtar` (also closes a coverage gap: no corpus case exercises `[[urlAttrs]]` today — verify with grep and say so).
- [ ] **Step 4:** Full gate; commit `feat(corpus): per-case [[urlAttrs]] rules via DirOptions.Classifier`.

---

### Task 5: Part A — URL extraction at forwarding elements

**Files:**
- Modify: `internal/attrclass/attrclass.go` (+`URLExactNames() []string`, `URLPrefixes() []string`), `internal/attrclass/attrclass_test.go`
- Modify: `internal/codegen/emit.go` (`emitFallthroughAttrs`)
- Modify: `attrs.go` (+`Attrs.WithoutFunc` only if prefixes require it — see Step 4)
- Create: `internal/corpus/testdata/cases/urlattrs/bag_href_nav.txtar`, `bag_src_image_split.txtar`, `bag_custom_rule.txtar`, `bag_prefix_rule.txtar`, `bag_rawurl.txtar`, `bag_hx_get.txtar`, `bag_case_variant.txtar`

**Interfaces:**
- Consumes: `gw.URLVal`/`gw.URLImageVal` (Task 1), forwarding classification incl. declared bags (Task 3), per-case rules (Task 4).
- Produces: extraction emission inside `emitFallthroughAttrs`, of the exact shape the spec pins:

```go
if _gsxv0, ok := attrs.Get("href"); ok {
	_gsxgw.S(" href=\"")
	_gsxgw.URLVal(_gsxv0)
	_gsxgw.S("\"")
}
```

- [ ] **Step 1: attrclass API, failing test first.** `URLExactNames()` returns built-ins ∪ user exact-name URL rules, lowercased, sorted, deduped (deterministic generated code); `URLPrefixes()` returns lowercased user URL prefixes, sorted. Unit test both against `Builtin()` (16 names, no prefixes) and a `New` with one exact + one prefix rule.
- [ ] **Step 2: Extraction emission.** In `emitFallthroughAttrs`, immediately before the residual-spread emission (1061-1070), for each `name` in `cls.URLExactNames()`:
  - skip if `name` is in `forcedNames` (a forced static owns the attribute; the residual `Without` already drops the bag's copy);
  - skip if a static attr with that name sits BEFORE the spread (it already emits under `!bag.Has(name)` with `gw.URL` per `emitExprAttr` — the bag's copy must then be extracted too: the guard pair means exactly one of the two renders; so DO still extract — the static's guard handles absence, the extraction's `Get` handles presence. Re-read 852-866 and reason about each combination in a comment; the invariant: for each URL name, at most one attribute renders, statics-before lose to bag, forced statics win);
  - emit the `Get` block above using a fresh `interpTemp` local (follow how other `_gsxvN` temps are minted) and the sink method chosen by `attrclass.URLSink(tag, name)` → `URLVal` vs `URLImageVal`;
  - add every extracted name to the residual `Without(...)` list (1065-1070).
  Keys are matched by exact lowercase name; the bag may carry case-variant keys (`HREF`) — `Get` is case-SENSITIVE. Decide with evidence: check what element-path classification does for a case-variant STATIC attr (attrclass lowercases the name at classification, but the emitted attribute keeps author case). For bags, match case-insensitively: either extend `Get` usage to a small case-insensitive scan helper or normalize in the extraction loop — the corpus case `bag_case_variant.txtar` pins whichever behavior you implement; it must NOT let `HREF` bypass sanitization (that is the adversarial finding class this closes).
- [ ] **Step 3: Prefix rules.** Only when `cls.URLPrefixes()` is non-empty, emit before the residual spread a loop:

```go
for _, _gsxkv := range attrs {
	if _gsxrt.URLPrefixMatch(_gsxkv.Key, _gsxprefixes) { … URLVal write with the key … }
}
```

  and switch the residual to exclude prefix-matched keys. Design the minimal runtime support for this (e.g. `URLPrefixMatch(key string, prefixes []string) bool` + `Attrs.WithoutFunc(func(string) bool) Attrs`) — implement in the root package with unit tests IN THIS TASK. All prefix-matched keys use the STRICT nav sink (`URLVal`) — prefixes are user rules; `URLSink`'s image split only applies to built-in names. Emit the `_gsxprefixes` slice as a package-level generated var or inline literal — inline literal per site is simplest and collision-free.
- [ ] **Step 4: Corpus cases.** Each on a byo bag component (exercising Task 3's path) unless noted:
  - `bag_href_nav.txtar`: caller passes `href={expr}` where expr is a `javascript:` string → render pins `href="about:invalid#gsx"`; sibling call with `href="/ok"` renders unchanged. Pin generated golden (the Get-extraction block).
  - `bag_src_image_split.txtar`: `data:image/png…` via bag onto `<img { p.Attrs... }/>` (passes, `URLImageVal`) and onto `<a { p.Attrs... }>` as `href` (rejected). Two components, one case.
  - `bag_custom_rule.txtar`: case `gsx.toml` `[[urlAttrs]] name = "data-href"` → bag `data-href={evil}` sanitized.
  - `bag_prefix_rule.txtar`: `[[urlAttrs]] prefix = "data-url-"` → bag `data-url-next={evil}` sanitized via the prefix loop; an unrelated `data-x` untouched and IN ORDER.
  - `bag_rawurl.txtar`: caller passes `href={gsx.RawURL("app://z")}` → renders `app://z`.
  - `bag_hx_get.txtar`: `hx-get="/path"` via bag → untouched `/path` (built-in classified, relative passes).
  - `bag_case_variant.txtar`: bag key `HREF` with `javascript:` value → must NOT render the payload.
- [ ] **Step 5:** Regen + verify; audit — this task may change existing goldens ONLY where a forwarding element's generated code gains extraction blocks and its bag carried URL-classified names (grep the diff; render goldens change only where a corpus case actually passed an unsafe URL through a bag — expected: none of the existing cases do; justify any exception). Full gate + `make check`.
- [ ] **Step 6:** Commit `feat(codegen): URL-classified bag attributes sanitize at the leaf`.

---

### Task 6: docs + contract updates

**Files:**
- Modify: `attrs.go` (Attrs + Spread doc comments), `docs/guide/syntax/attributes.md`, `docs/guide/syntax/props.md`, `docs/guide/syntax/composition.md`, `docs/ROADMAP.md`

- [ ] **Step 1:** `attrs.go` docs: the Attrs security note and Spread's "NOT URL-sanitized" sentence become: generated forwarding elements extract URL-classified attributes (per `[[urlAttrs]]`) through the tag-aware sinks with `gsx.RawURL` opt-out BEFORE the residual `Spread`; `Spread` itself still writes plain attribute-escaped values, and hand-written `Spread` callers own their sinks. `ConcatAttrs`/`Merge` docs cross-reference (Merge = userland eager composition; generated code concatenates).
- [ ] **Step 2:** `composition.md` §Precedence: state it now covers the implicit bag AND declared `gsx.Attrs` prop fields incl. derived expressions; local bag variables spread inline (link ROADMAP follow-up). §Derived bags: add the `p.Attrs` spelling.
- [ ] **Step 3:** `attributes.md` + `props.md`: shrink the PR #72 interim security paragraphs to the new contract (bags sanitize URL attributes at the element exactly like static attrs; `RawURL` opts out; CSS/JS remain literal-opt-in). Keep `{{ }}` inside `v-pre`/fences; grep-verify.
- [ ] **Step 4:** ROADMAP: follow-up entries (local-bag forwarding; call-site literal trust-marking) and mark the bag-hardening item shipped if one exists.
- [ ] **Step 5:** Commit `docs: bag hardening contracts (leaf-resolution, URL extraction, concat)`.

---

### Task 7: final validation + PR

- [ ] **Step 1:** `make ci` + `make lint` green at HEAD. `git diff origin/main --stat` audit: runtime (attrs.go/writer.go+tests), internal/attrclass, internal/codegen/emit.go, internal/corpus (harness + cases + coverage), docs, spec, plan — nothing else.
- [ ] **Step 2:** One-learning revalidation (throwaway worktree exactly like the attrs-only run; its report is the template): `gsx generate` + `go build ./...` + `go test ./ui/...` with this branch's binary + replace; its templates carry htmx/Datastar attrs through bags — diff a few rendered pages against the pre-change binary output; URL-classified attrs must be the ONLY behavioral deltas, and only where values were unsafe (expected: none).
- [ ] **Step 3:** Adversarial probe review (throwaway probe programs, not diff-reading), minimum probes: multi-hop bag forwarding (bag → component → bag → leaf) with a `javascript:` href injected at hop 1; conflicting URL keys across concatenated segments (last-wins must hold post-concat); prefix rule + Datastar `data-on-*` ordering; duplicate/case-variant key smuggling (`href` + `HREF` in one bag); `RawURL` at every hop; forced-static-vs-bag URL attr on one element; a byo bag component ALSO carrying declared non-bag fields (field-matching untouched); `ConcatAttrs` aliasing (mutate result, check inputs).
- [ ] **Step 4:** Fix wave if needed (one fixer, full findings list), re-verify, then PR via `superpowers:finishing-a-development-branch`: title `feat: bag hardening — resolve precedence, class merge, and URL sinks at the leaf`, body summarizing spec §Design + the equivalence-gate proof + validation results.
