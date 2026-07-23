# Canonical Tag Serialization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Generated HTML uses spec-canonical tag shapes by default (`<div/>` → `<div></div>`, `<br/>`/`<br></br>` → `<br>`, void-with-children errors), with a gsx.toml `serialization = "verbatim"` opt-out that keeps today's authored-shape emission. Fixes issue #144.

**Architecture:** The parser/AST/formatter are untouched (`Element.Void` keeps meaning "authored self-closed"; `gsx fmt` stays faithful). All behavior lands at codegen's two element-tail emit sites via one shared helper consulting a new WHATWG void-element set. The mode rides `funcTables` (already threaded to every emit site) — stamped once per `generateFile` call from a new `codegen.Options.VerbatimTags` — and flows from gsx.toml / `gen.WithSerialization` through the existing config merge, folding into the cache key.

**Tech Stack:** Go 1.26.1 (pin in ci.yml), txtar corpus harness, BurntSushi/toml.

**Spec:** `docs/superpowers/specs/2026-07-23-canonical-tag-serialization-design.md`

## Global Constraints

- Go pinned to 1.26.1 (`GO_VERSION` in ci.yml) — a different minor re-introduces gofmt drift.
- Never hand-edit `.x.go` or `*.golden` — regenerate: corpus `go test ./internal/corpus -run TestCorpus -update`, examples `go test ./internal/corpus -run TestExamples -update`; then verify WITHOUT `-update`.
- `-update` also rewrites `coverage.golden`; a forgotten manifest bump fails the suite — always commit it with new cases.
- Inner dev loop: `make check`. Authoritative pre-merge gate: `make ci` (uncached). The gate's exit code must be read directly — never chained through `||`.
- No hand-rolled heuristics; the void set is the WHATWG list verbatim.
- Runtime (root package) stays standard-library only. This plan touches no runtime code.
- Commit after every task with a conventional-commits message.

---

### Task 1: Void set + canonical tail emission (default mode)

**Files:**
- Modify: `internal/codegen/htmlnames.go` (append `voidElementNames`)
- Modify: `internal/codegen/filters.go:149-152` (`funcTables` gains `verbatimTags bool`)
- Modify: `internal/codegen/emit.go` (new helper `emitOpenTagEnd`; rewire the two tail sites at ~:1329 in `emitManualSpreadElement` and ~:1904 in `genNode`)
- Create: `internal/corpus/testdata/cases/elements/selfclose_nonvoid.txtar`
- Create: `internal/corpus/testdata/cases/elements/selfclose_svg.txtar`
- Create: `internal/corpus/testdata/cases/elements/void_canonical.txtar`
- Create: `internal/corpus/testdata/cases/elements/selfclose_spread.txtar`
- Create: `internal/corpus/testdata/cases/multispread/selfclose_fold.txtar`
- Create: `internal/corpus/testdata/cases/element-literals/selfclose-value.txtar`

**Interfaces:**
- Produces: `voidElementNames map[string]bool` (package codegen), `emitOpenTagEnd(b *bytes.Buffer, el *ast.Element, verbatim bool, bag *diag.Bag) (complete, ok bool)`, `funcTables.verbatimTags bool` (zero value = canonical). Task 2 extends `emitOpenTagEnd`; Task 3 stamps `verbatimTags`.

- [ ] **Step 1: Write the failing corpus cases**

`internal/corpus/testdata/cases/elements/selfclose_nonvoid.txtar` — hand-author every section (goldens verified by the harness, refreshed by `-update` later):

```
# Issue #144: a self-closed NON-VOID element must expand to an explicit
# open+close pair in canonical (default) serialization — browsers ignore the
# trailing slash on non-void elements and would treat <div/> as an open tag,
# swallowing every following sibling.
-- input.gsx --
package views

component Separator() {
	<section>
		<div role="separator"/>
		<span/>
		<p>after</p>
	</section>
}
-- invoke --
Separator()
-- diagnostics.golden --
-- render.golden --
<section><div role="separator"></div><span></span><p>after</p></section>
```

`internal/corpus/testdata/cases/elements/selfclose_svg.txtar`:

```
# Foreign-content (SVG) names expand uniformly: <path/> → <path></path> is
# equally valid in SVG, so canonical mode needs no foreign-name table and no
# ancestor tracking (spec decision: uniform expansion).
-- input.gsx --
package views

component Icon() {
	<svg viewBox="0 0 24 24">
		<path d="M0 0h24v24H0z"/>
		<circle cx="12" cy="12" r="10"/>
	</svg>
}
-- invoke --
Icon()
-- diagnostics.golden --
-- render.golden --
<svg viewBox="0 0 24 24"><path d="M0 0h24v24H0z"></path><circle cx="12" cy="12" r="10"></circle></svg>
```

`internal/corpus/testdata/cases/elements/void_canonical.txtar`:

```
# Canonical serialization of VOID elements: the authored trailing slash is
# dropped (<br/> → <br>; WHATWG: the slash "has no effect" and the spec's own
# serializer omits it), and an authored empty close pair collapses
# (<br></br> → <br> — browsers discard the invalid </br> anyway).
-- input.gsx --
package views

component Voids() {
	<div>
		<br/>
		<br></br>
		<img src="/a.png" alt="a"/>
		<input type="text" required/>
		<hr/>
	</div>
}
-- invoke --
Voids()
-- diagnostics.golden --
-- render.golden --
<div><br><br><img src="/a.png" alt="a"><input type="text" required><hr></div>
```

`internal/corpus/testdata/cases/elements/selfclose_spread.txtar` (the `emitManualSpreadElement` tail at emit.go:1329 — the exact gsxui `Separator` shape from issue #144):

```
# The manual-spread tail (single top-level spread) must expand a self-closed
# non-void element too — this is the exact gsxui Separator shape from #144.
-- input.gsx --
package views

import "github.com/gsxhq/gsx"

component Separator(attrs gsx.Attrs) {
	<div role="separator" { attrs... }/>
}
-- invoke --
Separator(gsx.Attrs{{Key: "class", Value: "h-px"}})
-- diagnostics.golden --
-- render.golden --
<div role="separator" class="h-px"></div>
```

`internal/corpus/testdata/cases/multispread/selfclose_fold.txtar` (the fold path funnels into `emitManualSpreadElement`; pins that the synthetic-spread re-entry keeps `Void`):

```
# Fold path (>=2 spreads) on a self-closed non-void element: foldElementSpreads
# re-enters emitManualSpreadElement with a synthetic single spread while keeping
# el.Void — the expansion must still apply there.
-- input.gsx --
package views

import "github.com/gsxhq/gsx"

component Two(p gsx.Attrs, q gsx.Attrs) {
	<div { p... } { q... }/>
}
-- invoke --
Two(gsx.Attrs{{Key: "id", Value: "d"}}, gsx.Attrs{{Key: "data-x", Value: "1"}})
-- diagnostics.golden --
-- render.golden --
<div id="d" data-x="1"></div>
```

`internal/corpus/testdata/cases/element-literals/selfclose-value.txtar` (expression-position element literals go through the same genNode tail):

```
# Context: element literal in Go-expression position — the expansion applies
# to expression-position elements exactly as to component-body children.
-- input.gsx --
package demo

var sep = <div role="separator"/>

component Uses() {
	<section>{ sep }<p>after</p></section>
}
-- invoke --
Uses()
-- diagnostics.golden --
-- render.golden --
<section><div role="separator"></div><p>after</p></section>
```

- [ ] **Step 2: Run the new cases to verify they fail**

Run: `go test ./internal/corpus -run 'TestCorpus/elements' -v 2>&1 | tail -20` (and the same for `multispread`, `element-literals`)
Expected: FAIL — rendered output still contains `<div role="separator"/>`, `<br/>` etc. (today's verbatim emission), plus a coverage.golden mismatch for the unregistered cases.

- [ ] **Step 3: Add the void set to htmlnames.go**

Append to `internal/codegen/htmlnames.go`:

```go
// voidElementNames is the WHATWG void-element set
// (https://html.spec.whatwg.org/multipage/syntax.html#void-elements): elements
// that have no end tag. Unlike htmlElementNames above (diagnostic-only), this
// table IS consulted by emit: it drives canonical tag serialization
// (emitOpenTagEnd) and the void-children diagnostic. Exact-match lowercase —
// gsx HTML tags are written lowercase (an uppercase first letter is a
// component tag).
var voidElementNames = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true,
	"hr": true, "img": true, "input": true, "link": true, "meta": true,
	"param": true, "source": true, "track": true, "wbr": true,
}
```

- [ ] **Step 4: Add `verbatimTags` to funcTables**

In `internal/codegen/filters.go`:

```go
type funcTables struct {
	filters   filterTable
	renderers rendererTable
	// verbatimTags selects authored-shape tag serialization (gsx.toml
	// `serialization = "verbatim"`). It rides funcTables because the tables are
	// already threaded to every element emit site; the zero value is the
	// canonical default. Stamped per generateFile call (see generateFile), so
	// funcTables construction/caching sites never carry it.
	verbatimTags bool
}
```

- [ ] **Step 5: Add emitOpenTagEnd and rewire both tail sites**

In `internal/codegen/emit.go`, near the two tail sites:

```go
// emitOpenTagEnd finishes el's open tag and reports whether the element is
// complete (nothing after the open tag — the caller skips children and the
// close tag). Canonical mode (default) emits spec-canonical shapes: a void
// element closes with a bare `>` and never gets an end tag (authored `<br/>`
// and `<br></br>` both serialize as `<br>`); every other element — self-closed
// or not — is followed by the caller's children+`</tag>` path, so `<div/>`
// expands to `<div></div>` (#144: browsers ignore the slash on non-void
// elements). Verbatim mode reproduces the authored shape byte-for-byte.
func emitOpenTagEnd(b *bytes.Buffer, el *ast.Element, verbatim bool, bag *diag.Bag) (complete, ok bool) {
	if voidElementNames[el.Tag] {
		if verbatim {
			if el.Void {
				emitS(b, "/>")
				return true, true
			}
			emitS(b, ">") // authored close pair: caller writes </tag>
			return false, true
		}
		emitS(b, ">")
		return true, true
	}
	if verbatim && el.Void {
		emitS(b, "/>")
		return true, true
	}
	emitS(b, ">")
	return false, true
}
```

At BOTH sites (`emitManualSpreadElement` after `ni.emitGuard(b)`, and the `*ast.Element` arm of `genNode`), replace:

```go
	if el.Void {
		emitS(b, "/>")
		return true
	}
	emitS(b, ">")
```

with (site 2 uses `t` for the element variable):

```go
	complete, ok := emitOpenTagEnd(b, el, table.verbatimTags, bag)
	if !ok {
		return false
	}
	if complete {
		return true
	}
```

The existing style/script/children loops and `emitS(b, "</"+el.Tag+">")` after this point stay exactly as they are — for a canonical non-void self-closed element `Children` is empty, so the loops no-op and only the close tag lands. (`bag` and the `ok` branch are unused until Task 2 adds the void-children diagnostic; keeping the signature now avoids re-touching both sites.)

- [ ] **Step 6: Run the new cases again**

Run: `go test ./internal/corpus -run 'TestCorpus/(elements|multispread|element-literals)' 2>&1 | tail -20`
Expected: the six new cases' render assertions PASS; coverage.golden still FAILS (manifest not yet regenerated), and several EXISTING cases now fail because their goldens pin `<br/>`/`<img .../>` shapes.

- [ ] **Step 7: Regenerate all goldens and inspect the churn**

Run:
```bash
go test ./internal/corpus -run TestCorpus -update
go test ./internal/corpus -run TestExamples -update
git diff --stat
git diff -- internal/corpus/testdata examples | grep '^[-+]' | grep -v '^[-+][-+]' | sort | uniq -c | sort -rn | head -40
```
Expected churn is ONLY: void elements losing their `/` (render.golden and generated.x.go.golden static strings), the six new cases gaining goldens, and coverage.golden gaining the new case rows. Any diff that is not a dropped void slash or a new case is a bug — stop and investigate before committing. `examples/210-void-elements.txtar` render.golden becomes `<div><img src="/avatar.png" alt="User"><br><input type="text" required disabled></div>`.

- [ ] **Step 8: Verify clean without -update, then the full suite**

Run: `go test ./internal/corpus && go test ./... 2>&1 | grep -v '^ok' | head -30`
Expected: corpus PASS; if any other package pins `<br/>`-style output in expectations (root-package render tests, gen e2e tests), update those expected strings to the canonical shape — they are hand-written expectations, not goldens.

- [ ] **Step 9: Commit**

```bash
git add -A
git commit -m "feat(codegen): canonical tag serialization — expand self-closed non-void, bare void tags (#144)"
```

---

### Task 2: void-with-children diagnostic

**Files:**
- Modify: `internal/codegen/emit.go` (`emitOpenTagEnd` gains the children check)
- Create: `internal/corpus/testdata/cases/diagnostics/void_children.txtar`
- Create: `internal/corpus/testdata/cases/diagnostics/void_children_spread.txtar`

**Interfaces:**
- Consumes: `emitOpenTagEnd`, `voidElementNames` (Task 1).
- Produces: diagnostic code `"void-children"`, message `void element <br> cannot have children` — both modes. Task 4's verbatim case relies on it firing under `verbatim` too.

- [ ] **Step 1: Write the failing diagnostic cases**

`internal/corpus/testdata/cases/diagnostics/void_children.txtar` (error cases carry no invoke/render sections — mirror `reserved_param_ctx.txtar`):

```
# A void element with children has no valid HTML meaning in ANY serialization
# mode: content can't live inside <br>, and silently relocating or dropping it
# would hide an authoring mistake. Hard error. (An EMPTY close pair <br></br>
# is NOT an error — canonical mode collapses it; see elements/void_canonical.)
-- input.gsx --
package views

component Bad() {
	<p><br>text</br></p>
}
-- diagnostics.golden --
4:5: void element <br> cannot have children
```

The golden's `line:col` must match the element's `Pos()` — after writing the implementation, run the case and copy the REPORTED position into the golden if it differs, then re-verify the message text is exact.

`internal/corpus/testdata/cases/diagnostics/void_children_spread.txtar` (spread tail site):

```
# Same diagnostic through the manual-spread tail (emitManualSpreadElement).
-- input.gsx --
package views

import "github.com/gsxhq/gsx"

component Bad(attrs gsx.Attrs) {
	<img { attrs... }>caption</img>
}
-- diagnostics.golden --
6:2: void element <img> cannot have children
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/corpus -run 'TestCorpus/diagnostics' 2>&1 | tail -10`
Expected: FAIL — no diagnostic is produced today (the cases render `<br>` and drop the text via Task 1's canonical void path).

- [ ] **Step 3: Add the children check to emitOpenTagEnd**

At the top of the `voidElementNames[el.Tag]` branch, before the verbatim check:

```go
	if voidElementNames[el.Tag] {
		if len(el.Children) > 0 {
			bag.Errorf(el.Pos(), el.End(), "void-children",
				"void element <%s> cannot have children", el.Tag)
			return false, false
		}
		...
```

Note: `<br></br>` (empty close pair) has `len(el.Children) == 0` and stays legal. A whitespace-only body like `<br> </br>` follows whatever wsnorm leaves: a newline-separated body collapses to no children (legal), a same-line interior space may survive as a Text child and error — that is acceptable and now pinned by whichever shape the corpus case captures.

- [ ] **Step 4: Run the diagnostic cases, fix golden positions**

Run: `go test ./internal/corpus -run 'TestCorpus/diagnostics' -v 2>&1 | tail -20`
Expected: the two new cases PASS once the `line:col` in each golden matches the reported position (adjust the golden by hand to the reported value — positions are facts, the message text is the assertion).

- [ ] **Step 5: Regenerate coverage, verify, commit**

```bash
go test ./internal/corpus -run TestCorpus -update
go test ./internal/corpus
git add -A
git commit -m "feat(codegen): error on void elements with children"
```

---

### Task 3: Options + gsx.toml `serialization` plumbing

**Files:**
- Modify: `internal/codegen/module.go` (`Options.VerbatimTags bool`, `DirOptions.VerbatimTags *bool`, per-dir resolution, thread into `generateFile` call at ~:1585)
- Modify: `internal/codegen/emit.go` (`generateFile` gains `verbatimTags bool` param, stamps `table.verbatimTags`)
- Modify: `gen/configfile.go` (`tomlConfig.Serialization`, apply into `config`, `mergeConfig` gate)
- Modify: `gen/main.go` (`config` fields, thread through `runGenerate`/`runInfo` call chain)
- Create: `gen/serialization.go` (`Serialization` type + parser + `WithSerialization` option)
- Modify: `gen/cache.go` (`moduleGenerateConfig.verbatimTags`, thread into `generateModule`)
- Modify: `gen/cache_pipeline.go` (genOpts + keyConfig at ~:111/:132)
- Modify: `gen/cachekey.go` (`cacheKeyConfig.verbatimTags`, fold into hash)
- Modify: `gen/dev.go` ~:98, `gen/watchsession.go` ~:69 (same threading)
- Test: `gen/serialization_test.go`, extend `gen/configfile_test.go`, `gen/cachekey_test.go`

**Interfaces:**
- Consumes: `funcTables.verbatimTags` (Task 1).
- Produces: `gen.Serialization` (`gen.SerializationCanonical` zero-default, `gen.SerializationVerbatim`), `gen.WithSerialization(Serialization) Option`, gsx.toml top-level key `serialization = "canonical" | "verbatim"`, `codegen.Options.VerbatimTags bool`, `codegen.DirOptions.VerbatimTags *bool` (nil = inherit Options). Task 4's corpus loader sets the DirOptions field.
- **Deliberate spec point:** NO `GSX_SERIALIZATION` env var — `gen/envconfig.go`'s registry is restricted to knobs that legitimately vary dev↔prod, and serialization is a project-wide semantic choice (an env override would make output differ between machines). Precedence is option > config.

- [ ] **Step 1: Write failing gen tests**

`gen/serialization_test.go`:

```go
package gen

import "testing"

func TestParseSerialization(t *testing.T) {
	cases := []struct {
		in      string
		want    Serialization
		wantErr bool
	}{
		{"canonical", SerializationCanonical, false},
		{"verbatim", SerializationVerbatim, false},
		{"", SerializationCanonical, false}, // key absent → default
		{"Verbatim", 0, true},               // exact spelling only, like minify levels
		{"strict", 0, true},
	}
	for _, c := range cases {
		got, err := parseSerialization(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("parseSerialization(%q) err=%v wantErr=%v", c.in, err, c.wantErr)
			continue
		}
		if err == nil && got != c.want {
			t.Errorf("parseSerialization(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestWithSerializationOverridesConfigFile(t *testing.T) {
	base := config{serialization: SerializationVerbatim} // as if loaded from gsx.toml
	var opts config
	for _, o := range []Option{WithSerialization(SerializationCanonical)} {
		o(&opts)
	}
	merged := mergeConfig(base, opts)
	if merged.serialization != SerializationCanonical || !merged.serializationSet {
		t.Fatalf("option must win over config file: got %v set=%v", merged.serialization, merged.serializationSet)
	}
}

func TestMergeConfigKeepsFileSerialization(t *testing.T) {
	base := config{serialization: SerializationVerbatim}
	merged := mergeConfig(base, config{})
	if merged.serialization != SerializationVerbatim {
		t.Fatalf("file-layer serialization lost in merge: %v", merged.serialization)
	}
}
```

Add `TestComputeKey_SerializationChangesKey` by copying `TestComputeKey_MinifyChangesKey` (`gen/cachekey_minify_test.go:41`) wholesale — same projection/setup — and varying `cacheKeyConfig.verbatimTags` (false vs true) instead of the minify booleans, asserting the two keys differ.

Also extend the gsx.toml decode test in `gen/configfile_test.go`: a config file containing `serialization = "verbatim"` decodes into `config.serialization == SerializationVerbatim`; `serialization = "bogus"` returns an error naming the key and both valid values.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./gen -run 'Serialization|VerbatimTags' 2>&1 | head`
Expected: compile FAILURE (`Serialization` undefined).

- [ ] **Step 3: Implement the gen layer**

`gen/serialization.go`:

```go
package gen

import "fmt"

// Serialization selects how codegen serializes element tag shapes. The
// canonical default emits spec-canonical HTML: self-closed non-void elements
// expand to an explicit open+close pair (browsers ignore the trailing slash on
// non-void elements — issue #144), void elements drop the meaningless slash
// and any authored empty close pair (<br/> and <br></br> both emit <br>).
// Verbatim reproduces authored shapes byte-for-byte.
type Serialization uint8

const (
	SerializationCanonical Serialization = iota
	SerializationVerbatim
)

// parseSerialization maps the gsx.toml / user-facing spelling to the level.
// The empty string (key absent) is the canonical default.
func parseSerialization(s string) (Serialization, error) {
	switch s {
	case "", "canonical":
		return SerializationCanonical, nil
	case "verbatim":
		return SerializationVerbatim, nil
	}
	return 0, fmt.Errorf("serialization: %q: want \"canonical\" or \"verbatim\"", s)
}
```

In `gen/options.go`, next to `WithMinifyLevel` (mirror its doc style):

```go
// WithSerialization pins the tag-shape serialization mode, overriding the
// gsx.toml `serialization` key (code is the more deliberate layer). The
// default is SerializationCanonical.
func WithSerialization(s Serialization) Option {
	return func(cfg *config) {
		cfg.serialization = s
		cfg.serializationSet = true
	}
}
```

In `gen/main.go` `config` struct, next to `minifyLevelSet`:

```go
	serialization    Serialization // tag-shape serialization; zero = canonical
	serializationSet bool          // true once WithSerialization pinned it
```

In `gen/configfile.go`: add `Serialization string `toml:"serialization"`` to `tomlConfig`; in the function that converts `tomlConfig` into `config`, parse it via `parseSerialization` (error propagates like other config errors, via `cfg.errs` or the load error path — follow whichever the surrounding fields use); in `mergeConfig`, mirror the `minifyLevelSet` sentinel block at :329-337:

```go
	merged.serialization = base.serialization
	merged.serializationSet = base.serializationSet
	if opts.serializationSet {
		merged.serialization = opts.serialization
		merged.serializationSet = true
	}
```

- [ ] **Step 4: Implement the codegen layer**

`internal/codegen/module.go`:
- `Options` gains `VerbatimTags bool` (doc: "emit authored tag shapes verbatim instead of canonical serialization; gsx.toml `serialization = \"verbatim\"`").
- `DirOptions` gains `VerbatimTags *bool // nil = inherit Options.VerbatimTags` (nil-inherit like `Classifier`).
- Add a resolver next to `dirOptionsFor`'s other consumers:

```go
// verbatimTagsFor resolves the tag-serialization mode for dir: a DirOptions
// override wins, else the module-wide Options value.
func (m *Module) verbatimTagsFor(dir string) bool {
	if d, ok := m.dirOptionsFor(dir); ok && d.VerbatimTags != nil {
		return *d.VerbatimTags
	}
	return m.opts.VerbatimTags
}
```

- At the `generateFile` call (~module.go:1585), pass `m.verbatimTagsFor(<dir>)` — the surrounding code knows the package dir (the same value used to resolve per-dir filters/classifier for `a`; if the analysis result `a` already carries a resolved dir or per-dir options, use that instead of re-resolving — follow how `a.merger`/`a.classifier` got there).

`internal/codegen/emit.go` — `generateFile` signature gains `verbatimTags bool` (place it after `cssMinify, jsMinify bool`), and immediately after the `cls == nil` guard stamps the local copy:

```go
	table.verbatimTags = verbatimTags
```

`funcTables` is passed by value, so no construction/caching site changes. Fix every other `generateFile` caller the compiler reports (skeleton/diagnostic paths pass `false` unless they have a Module in scope to resolve properly — check each: a caller with per-dir context should resolve, an analysis-only caller can pass `false` because tag-shape bytes don't affect type-checking, but the void-children diagnostic in Task 2 fires identically either way).

- [ ] **Step 5: Thread gen → codegen**

Compiler-guided, all mechanical:
- `runGenerate` (gen/main.go:226 call + its definition) gains `verbatimTags bool`, passed as `merged.serialization == SerializationVerbatim`.
- `moduleGenerateConfig` (gen/cache.go) gains `verbatimTags bool`; set in the literal at cache.go:58.
- `prep.genOpts` (gen/cache_pipeline.go:~111) gains `VerbatimTags: config.verbatimTags`.
- `prep.keyConfig` (gen/cache_pipeline.go:~132) gains `verbatimTags: config.verbatimTags`.
- `cacheKeyConfig` (gen/cachekey.go) gains `verbatimTags bool`; fold into the hash by extending the second Fprintf: append `serial:%d` inside the existing `minify=css:%d,js:%d` segment → `minify=css:%d,js:%d\x00serial=%d\x00`, passing `b2i(config.verbatimTags)`.
- `gen/dev.go:~98` and `gen/watchsession.go:~69` mirror the cssMinify/jsMinify threading with `merged.serialization == SerializationVerbatim` / `s.cfg.verbatimTags` (add the field to the watch session's cfg struct the same way cssMinify is carried).
- `runInfo` is NOT extended (no new output) — out of scope.

- [ ] **Step 6: Run the tests**

Run: `go test ./gen -run 'Serialization|VerbatimTags|Config' 2>&1 | tail -10` then `go build ./... && go test ./internal/corpus ./gen 2>&1 | tail -5`
Expected: PASS. Corpus unchanged (default canonical).

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "feat(gen): gsx.toml serialization key + WithSerialization option, cache-key fold"
```

---

### Task 4: Corpus per-case serialization + verbatim-mode cases

**Files:**
- Modify: `internal/corpus/loader.go` (`caseToml.Serialization`, `caseDoc` field + validation)
- Modify: `internal/corpus/batch.go` (PerDir wiring at ~:216-260)
- Create: `internal/corpus/testdata/cases/serialization/verbatim_selfclose.txtar`
- Create: `internal/corpus/testdata/cases/serialization/verbatim_void.txtar`
- Create: `internal/corpus/testdata/cases/serialization/verbatim_void_children_error.txtar`

**Interfaces:**
- Consumes: `codegen.DirOptions.VerbatimTags *bool` (Task 3), diagnostic `"void-children"` (Task 2).
- Produces: corpus cases may carry `serialization = "verbatim"` in their `gsx.toml` section.

- [ ] **Step 1: Write the failing verbatim cases**

`internal/corpus/testdata/cases/serialization/verbatim_selfclose.txtar`:

```
# serialization = "verbatim" restores authored-shape emission: a self-closed
# non-void element ships as authored (the author owns the bytes and the
# consequences — #144's default fix is the canonical mode).
-- gsx.toml --
serialization = "verbatim"
-- input.gsx --
package views

component Sep() {
	<div><span role="separator"/></div>
}
-- invoke --
Sep()
-- diagnostics.golden --
-- render.golden --
<div><span role="separator"/></div>
```

`internal/corpus/testdata/cases/serialization/verbatim_void.txtar`:

```
# Verbatim keeps authored void shapes: the slash stays, an authored close pair
# stays.
-- gsx.toml --
serialization = "verbatim"
-- input.gsx --
package views

component Voids() {
	<div><br/><br></br><img src="/a.png" alt="a"/></div>
}
-- invoke --
Voids()
-- diagnostics.golden --
-- render.golden --
<div><br/><br></br><img src="/a.png" alt="a"/></div>
```

`internal/corpus/testdata/cases/serialization/verbatim_void_children_error.txtar`:

```
# The void-children error is mode-independent: verbatim mode is about tag
# SHAPES, not about admitting meaningless markup.
-- gsx.toml --
serialization = "verbatim"
-- input.gsx --
package views

component Bad() {
	<p><br>text</br></p>
}
-- diagnostics.golden --
4:5: void element <br> cannot have children
```

(Copy the exact `line:col` from Task 2's `void_children.txtar` — same source shape.)

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/corpus -run 'TestCorpus/serialization' 2>&1 | tail -10`
Expected: FAIL — the loader rejects the unknown `serialization` key (strict TOML decode) or the render comes out canonical.

- [ ] **Step 3: Wire the corpus loader and batch**

`internal/corpus/loader.go`:
- `caseToml` gains `Serialization string `toml:"serialization"``.
- `caseDoc` gains `verbatimTags *bool`.
- Where the loader consumes the decoded `caseToml` (the `f.Name == "gsx.toml"` arm at ~:117), validate and store:

```go
		if ct.Serialization != "" {
			switch ct.Serialization {
			case "canonical":
				v := false
				c.verbatimTags = &v
			case "verbatim":
				v := true
				c.verbatimTags = &v
			default:
				return nil, fmt.Errorf("gsx.toml: serialization: %q: want \"canonical\" or \"verbatim\"", ct.Serialization)
			}
		}
```

`internal/corpus/batch.go` (~:230): extend the PerDir gate so a case with ONLY a serialization override still gets DirOptions entries — change

```go
		if cs.c.classMerger == nil && len(cs.c.filterPkgs) == 0 && cs.c.classifier == nil {
			continue
		}
```

to also check `cs.c.verbatimTags == nil`, and add `VerbatimTags: cs.c.verbatimTags` to the `codegen.DirOptions{...}` literal a few lines below.

- [ ] **Step 4: Run, regenerate coverage, verify**

```bash
go test ./internal/corpus -run 'TestCorpus/serialization'
go test ./internal/corpus -run TestCorpus -update
go test ./internal/corpus
```
Expected: all PASS; coverage.golden gains the three rows; nothing else churns.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "test(corpus): per-case serialization override + verbatim-mode cases"
```

---

### Task 5: Docs + example

**Files:**
- Modify: `examples/210-void-elements.txtar` (doc prose only — summary line)
- Create: `examples/211-self-closing-non-void.txtar`
- Modify: `docs/guide/syntax/elements.md`
- Modify: `docs/guide/config.md`

**Interfaces:** none new. Keep docs CONCISE — behavior plainly stated, rationale lives in the spec (repeated feedback: docs were "too wordy").

- [ ] **Step 1: New example**

`examples/211-self-closing-non-void.txtar` (follow 210's `-- doc --` header shape; page/category values copied from 210, pageOrder after it):

```
-- doc --
name: Self-closing non-void elements
summary: <div/> is JSX-style shorthand — gsx expands non-void self-closed elements to an explicit open+close pair in the HTML it generates.
category: Elements
page: elements
pageOrder: 21
-- input.gsx --
package views

component Divider() {
	<section>
		<div role="separator"/>
		<p>Content after the divider stays outside it.</p>
	</section>
}
-- invoke --
Divider()
-- render.golden --
<section><div role="separator"></div><p>Content after the divider stays outside it.</p></section>
```

In `examples/210-void-elements.txtar`, update the `-- doc --` summary to reflect canonical shapes:

```
summary: Void elements like <br/>, <img/>, and <input/> need no closing tag — gsx serializes them canonically, without the trailing slash.
```

Run: `go test ./internal/corpus -run TestExamples -update && go test ./internal/corpus -run TestExamples`
Expected: PASS; 211 gains its render.golden identical to the hand-written one; check whether an examples manifest/`docs/guide/syntax/_generated` regeneration step exists for new example files (look at how the most recently added `examples/*.txtar` was wired in `git log --oneline --follow` on a neighbor) and run the same regeneration.

- [ ] **Step 2: Guide pages**

`docs/guide/syntax/elements.md` — add a short "Tag serialization" section (placement: near the existing void-elements/self-closing prose; match the page's heading level). Content, concise:

```markdown
## Tag serialization

gsx generates canonical HTML tag shapes regardless of how you write them:

- `<div/>` (any non-void element, including SVG) renders as `<div></div>` —
  browsers ignore the `/` on non-void elements and would treat `<div/>` as an
  unclosed open tag.
- `<br/>` and `<br></br>` both render as `<br>`.
- A void element with children (`<br>text</br>`) is a compile error.

Set `serialization = "verbatim"` in `gsx.toml` to emit authored shapes
unchanged.
```

`docs/guide/config.md` — add the key where top-level keys are documented, matching the surrounding format:

```markdown
### `serialization`

`"canonical"` (default) or `"verbatim"`. Canonical emits spec-canonical tag
shapes (`<div/>` → `<div></div>`, `<br/>` → `<br>`); verbatim emits tags as
authored. Programmatic override: `gen.WithSerialization`.
```

None of the added prose contains literal `{{ }}`, so no `::: v-pre` wrapping is needed; keep it that way.

- [ ] **Step 3: Verify and commit**

Run: `go test ./internal/corpus -run 'TestExamples|TestCorpus' 2>&1 | tail -5`
Expected: PASS.

```bash
git add -A
git commit -m "docs: canonical tag serialization — guide, config key, example 211"
```

---

### Task 6: Full gate, adversarial review, ship

- [ ] **Step 1: Run the authoritative gate**

Run: `make ci`
Expected: exit 0, read directly (`echo $status` immediately after in fish; never `make ci || ...`). Fix anything red (gofmt/`gsx fmt` drift from new files is the usual suspect: `gofmt -w` the named files / run the repo's fmt target).

- [ ] **Step 2: Independent adversarial review**

Dispatch a reviewer subagent that does NOT read this plan first, with the brief: "Review the diff of this branch against main for the canonical-tag-serialization feature (issue #144, spec in docs/superpowers/specs/2026-07-23-canonical-tag-serialization-design.md). Build and run throwaway probe programs (a scratch module using `go run ./cmd/gsx generate`) — do not just read the diff. Probe at least: nested self-closed elements inside if/for bodies; `<script/>`; a self-closed element as a component child slot; verbatim mode via a real gsx.toml in a scratch project; cache behavior when flipping the serialization key between two runs (the .x.go must regenerate — the key fold); `gsx fmt` leaving `<div/>` untouched; LSP diagnostics for `<br>text</br>` if feasible." Fix findings; re-run `make ci`.

- [ ] **Step 3: Ship**

Use superpowers:finishing-a-development-branch: push branch, open a PR titled `feat: canonical tag serialization — expand self-closed non-void elements (#144)` with a body linking issue #144 (`Closes #144`) and summarizing the mode table from the spec. After merge: comment on gsxui's `docs/jsx-parity.md` ledger (sibling repo) is a follow-up, not part of this PR.
