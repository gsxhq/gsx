# Imported Component Props Hardening — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the seven confirmed review findings in commit `5aa6c73` (imported component prop discovery) so cross-package attr fallthrough is correct, cached, deterministic, and cache-key-safe, with per-context corpus coverage and docs.

**Architecture:** Imported-package prop facts move from an uncached package-wide alias-keyed merge into (a) a Module-level cache keyed by dep dir, invalidated through the existing reverse-dep closure, and (b) per-file qualified views (Go import aliases are file-scoped). The explicit `attrs={{ }}` ordered-attrs literal composes with the fallthrough bag via `.Merge` instead of emitting a duplicate struct field. The gen cache key learns to walk `.gsx`-hoisted import edges that `go list` cannot see.

**Tech Stack:** Go 1.26.1, `internal/codegen` (module analysis + emitter), `gen` (incremental cache), txtar corpus (`internal/corpus`).

## Global Constraints

- Runtime (root `gsx` package) is standard-library only; all work here is in `internal/codegen`, `gen`, corpus, docs — none touches the runtime.
- **Never hand-edit `.x.go` or golden files.** Regenerate with `go test ./internal/corpus -run TestCorpus -update`, then re-run WITHOUT `-update` to verify. A new corpus case requires the regenerated `coverage.golden` in the same commit.
- Every syntax/codegen behavior change ships a corpus case per context, plus unit tests for runtime-independent logic.
- Inner loop: `make check`. Before merge: `make ci` (Go pinned to 1.26.1 via `GO_VERSION` in ci.yml).
- Literal `{{ }}` in `docs/guide/**` prose MUST be wrapped in a `::: v-pre` block (VitePress parses `{{ }}` as Vue interpolation and the docs build fails).
- Call-site spelling: **lowercase `attrs={{ }}` is canonical** in corpus cases and docs (capitalize-first matching makes `Attrs={{ }}` equivalent; one corpus case pins the equivalence).
- No "simple heuristics": every fix is the real mechanism (per-file scoping, cache invalidation through the existing closure, real merge semantics).
- Work happens in worktree `.claude/worktrees/imported-props-hardening`, branch `worktree-imported-props-hardening`, based on `5aa6c73`.

### Semantic decisions locked by review discussion (do not relitigate)

1. **Fallthrough is knowledge-driven.** With a known field set (`declared != nil`), unmatched attrs go to the `Attrs` bag. With an unknown field set (`declared == nil`: external-module gsx packages, plain Go packages, dot imports, or a dep whose analysis failed), identifier attrs are **assumed props** (loud compile error beats silent wrong HTML). This plan makes `declared != nil` the norm for all in-module imports and makes every fallback **visible** (warning diagnostic) — it does not change the blind-regime default.
2. **`attrs={{ }}` targeting the synthesized bag stays accepted when `declared == nil`** (enables hand-written Go components with an `Attrs gsx.Attrs` field; the skeleton type-check catches a missing field — pinned by a corpus diagnostics case, not reverted).
3. **Merge order when `attrs={{ }}` combines with other bag contributors:** base literal of bare/fallthrough attrs first, then `{ spread... }` / conditional-attr merges in source order, then the `attrs={{ }}` literal last via `.Merge`. Two `attrs={{ }}` literals on one element are an error.

---

### Task 1: Module-cached imported-package prop facts

Fixes the warm-path regression (uncached per-analyze dep re-parse + `packages.Load`) and lays the base for Tasks 2–3. Facts are cached per dep dir and invalidated by the existing reverse-closure mechanism (`SetOverride` → `applyDirty` → `invalidateLocked` already seeds the dep dir itself).

**Files:**
- Modify: `internal/codegen/module.go` (Module struct ~line 95–115, `Open` ~line 143, `rebuildFset` ~line 336)
- Modify: `internal/codegen/module_importer.go` (`invalidateLocked` ~line 159; new type + method near `mergeImportedComponentPropFields` ~line 284)
- Test: `internal/codegen/depfacts_test.go` (new)

**Interfaces:**
- Produces: `type depPropFacts struct { pkgName string; propFields, nodeProps, attrsProps map[string]map[string]bool; byo *byoData }` and `func (m *Module) importedPropFacts(depDir string) (*depPropFacts, error)`. Task 2 consumes both.

- [ ] **Step 1: Write the failing test**

```go
package codegen

import (
	"os"
	"path/filepath"
	"testing"
)

// writeDepFactsModule lays out a two-package module on disk:
// ui/card.gsx defines Card(title string) using { attrs... };
// pages/home.gsx imports ui.
func writeDepFactsModule(t *testing.T) (root, uiDir, pagesDir string) {
	t.Helper()
	root = t.TempDir()
	uiDir = filepath.Join(root, "ui")
	pagesDir = filepath.Join(root, "pages")
	for _, d := range []string{uiDir, pagesDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(root, "go.mod"), "module example.com/app\n\ngo 1.26.1\n")
	writeFile(t, filepath.Join(uiDir, "card.gsx"), `package ui

component Card(title string) {
	<div class="card" { attrs... }>{title}</div>
}
`)
	writeFile(t, filepath.Join(pagesDir, "home.gsx"), `package pages

import "example.com/app/ui"

component Home() {
	<ui.Card title="t" class="x"/>
}
`)
	return root, uiDir, pagesDir
}

func writeFile(t *testing.T, path, src string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestImportedPropFactsCachedAndInvalidated(t *testing.T) {
	root, uiDir, _ := writeDepFactsModule(t)
	m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	f1, err := m.importedPropFacts(uiDir)
	if err != nil {
		t.Fatal(err)
	}
	if !f1.propFields["CardProps"]["Title"] || !f1.propFields["CardProps"]["Attrs"] {
		t.Fatalf("CardProps fields = %v; want Title and Attrs", f1.propFields["CardProps"])
	}
	f2, err := m.importedPropFacts(uiDir)
	if err != nil {
		t.Fatal(err)
	}
	if f1 != f2 {
		t.Fatal("second lookup did not hit the cache (different *depPropFacts)")
	}
	// A content change to the dep invalidates its cached facts.
	m.SetOverride(filepath.Join(uiDir, "card.gsx"), []byte(`package ui

component Card(title string, variant string) {
	<div class="card" { attrs... }>{title}</div>
}
`))
	m.Invalidate(uiDir)
	f3, err := m.importedPropFacts(uiDir)
	if err != nil {
		t.Fatal(err)
	}
	if f3 == f1 {
		t.Fatal("facts not recomputed after invalidation")
	}
	if !f3.propFields["CardProps"]["Variant"] {
		t.Fatalf("recomputed CardProps fields = %v; want Variant", f3.propFields["CardProps"])
	}
}
```

If a helper named `writeFile` already exists in the package's tests, reuse it and drop this copy (compile error will tell you).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/codegen -run TestImportedPropFactsCachedAndInvalidated -count=1`
Expected: FAIL — `m.importedPropFacts undefined`.

- [ ] **Step 3: Implement**

In `internal/codegen/module.go`, add to the `Module` struct (after `pkgResults`):

```go
	depFacts         map[string]*depPropFacts   // abs dep dir -> cached imported prop facts (see importedPropFacts)
```

Update the `mu` comment to include `depFacts`. In `Open`, add `depFacts: map[string]*depPropFacts{},` to the literal. In `rebuildFset`, add `m.depFacts = map[string]*depPropFacts{}` next to the `pkgResults` reset.

In `internal/codegen/module_importer.go`, extend `invalidateLocked`:

```go
func (m *Module) invalidateLocked(dirs []string) {
	for d := range m.reverseClosure(dirs) {
		delete(m.pkgTypes, d)
		delete(m.pkgResults, d)
		delete(m.depFacts, d)
	}
}
```

Add (replacing nothing yet — `mergeImportedComponentPropFields` is removed in Task 2):

```go
// depPropFacts is the cached per-dep-dir prop-fact bundle consumed by the
// per-file qualified merge (fileScopedFacts): everything call-site attr
// splitting needs to treat an imported component like a same-package one.
// Derived syntactically by componentPropFieldsFor (plus its BYO external
// load), so it holds no fset positions and survives fset rebuilds — it is
// still reset there for uniformity. Invalidation: invalidateLocked deletes
// the entry whenever the dep dir is in the dirty closure.
type depPropFacts struct {
	pkgName    string
	propFields map[string]map[string]bool
	nodeProps  map[string]map[string]bool
	attrsProps map[string]map[string]bool
	byo        *byoData
}

// importedPropFacts returns dir's prop facts, deriving and caching them on
// first use. Callers run under analysisMu (analyze), so derivation is
// single-flight; m.mu guards the map for Invalidate callers.
func (m *Module) importedPropFacts(depDir string) (*depPropFacts, error) {
	m.mu.Lock()
	if f, ok := m.depFacts[depDir]; ok {
		m.mu.Unlock()
		return f, nil
	}
	m.mu.Unlock()
	files, pkgName, err := m.parsePackageWithFset(depDir, m.fset)
	if err != nil {
		return nil, err
	}
	propFields, nodeProps, attrsProps, byo, err := componentPropFieldsFor(depDir, files)
	if err != nil {
		return nil, err
	}
	f := &depPropFacts{pkgName: pkgName, propFields: propFields, nodeProps: nodeProps, attrsProps: attrsProps, byo: byo}
	m.mu.Lock()
	if m.depFacts == nil {
		m.depFacts = map[string]*depPropFacts{}
	}
	m.depFacts[depDir] = f
	m.mu.Unlock()
	return f, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/codegen -run TestImportedPropFactsCachedAndInvalidated -count=1`
Expected: PASS. Also run `go test ./internal/codegen -count=1` (whole package still green).

- [ ] **Step 5: Commit**

```bash
git add internal/codegen/module.go internal/codegen/module_importer.go internal/codegen/depfacts_test.go
git commit -m "feat(codegen): cache imported-package prop facts per dep dir"
```

---

### Task 2: Per-file alias-scoped qualified facts, BYO merge, and visible fallback

Fixes the file-scoped-alias collision (nondeterministic last-write-wins), imported BYO components (discarded 4th return → nonexistent `<Pkg>Props` type at call sites), and silent error swallowing (now a positioned Warning). Replaces `mergeImportedComponentPropFields`/`collectImportSpecs`/`mergeQualifiedPropMap` wholesale.

**Files:**
- Modify: `internal/codegen/module_importer.go` (delete lines 284–338: `mergeImportedComponentPropFields`, `collectImportSpecs`, `mergeQualifiedPropMap`; rewire `analyze` ~line 474–511; extend `analyzed` struct ~line 409)
- Modify: `internal/codegen/byo.go` (clone/merge helpers)
- Modify: `internal/codegen/module.go` (`Generate` ~line 455 and the `generateFile` call in `Package` ~line 404 — pass per-file facts)
- Test: `internal/codegen/depfacts_test.go` (extend), corpus cases `internal/corpus/testdata/cases/xpkg/imported_alias_collision.txtar`, `internal/corpus/testdata/cases/xpkg/imported_byo.txtar`
- Test corpus regen: `internal/corpus/testdata/coverage.golden`

**Interfaces:**
- Consumes: `importedPropFacts(depDir) (*depPropFacts, error)` from Task 1.
- Produces: `analyzed.factsByFile map[string]*fileFacts` where `type fileFacts struct { propFields, nodeProps, attrsProps map[string]map[string]bool; byo *byoData }`, keyed by gsx path. Tasks 3–4 rely on per-file facts being what `buildSkeleton` and `generateFile` receive.

- [ ] **Step 1: Write the failing determinism test**

Append to `internal/codegen/depfacts_test.go`:

```go
// TestFileScopedAliasNoCollision: pages/a.gsx imports app/ui as ui;
// pages/b.gsx imports app/widgets under the SAME alias ui. Both packages
// define Panel but with different props. Each file must split attrs against
// ITS OWN import, and output must be stable across repeated generation.
func TestFileScopedAliasNoCollision(t *testing.T) {
	root := t.TempDir()
	mk := func(rel, src string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, p, src)
	}
	mk("go.mod", "module example.com/app\n\ngo 1.26.1\n")
	// ui.Panel has a Variant prop; widgets.Panel does NOT (variant falls to its bag).
	mk("ui/panel.gsx", `package ui

component Panel(variant string) {
	<section data-variant={variant} { attrs... }>{children}</section>
}
`)
	mk("widgets/panel.gsx", `package widgets

component Panel() {
	<aside { attrs... }>{children}</aside>
}
`)
	mk("pages/a.gsx", `package pages

import "example.com/app/ui"

component A() {
	<ui.Panel variant="big" class="x">a</ui.Panel>
}
`)
	mk("pages/b.gsx", `package pages

import ui "example.com/app/widgets"

component B() {
	<ui.Panel variant="big" class="x">b</ui.Panel>
}
`)
	m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	pagesDir := filepath.Join(root, "pages")
	out, diags, err := m.Generate(pagesDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range diags {
		t.Logf("diag: %+v", d)
	}
	aGen := string(out[filepath.Join(pagesDir, "a.gsx")])
	bGen := string(out[filepath.Join(pagesDir, "b.gsx")])
	// a.gsx: ui = app/ui, Panel HAS Variant → caller-set prop, class falls through.
	if !strings.Contains(aGen, "Variant:") {
		t.Errorf("a.gsx should set Variant prop on ui.Panel; got:\n%s", aGen)
	}
	// b.gsx: ui = app/widgets, Panel has NO Variant → variant AND class fall to the bag.
	if strings.Contains(bGen, "Variant:") {
		t.Errorf("b.gsx must NOT set a Variant prop on widgets.Panel; got:\n%s", bGen)
	}
	if !strings.Contains(bGen, `{Key: "variant", Value: "big"}`) {
		t.Errorf("b.gsx should send variant to the Attrs bag; got:\n%s", bGen)
	}
	// Determinism: a fresh Module over the same tree yields identical bytes.
	m2, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	out2, _, err := m2.Generate(pagesDir)
	if err != nil {
		t.Fatal(err)
	}
	for p := range out {
		if string(out[p]) != string(out2[p]) {
			t.Errorf("nondeterministic output for %s", p)
		}
	}
}
```

Add `"strings"` to the test file imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/codegen -run TestFileScopedAliasNoCollision -count=1`
Expected: FAIL — with the current package-wide merge, one of the two assertions fails (which one is map-order dependent; run with `-count=5` to observe the flip if curious).

- [ ] **Step 3: Implement byoData helpers**

In `internal/codegen/byo.go` add:

```go
// cloneForFile returns a copy of b whose maps can be extended with imported
// qualified entries without mutating the package-wide byo shared across files.
// nullaryFuncs is shared (never extended by the qualified merge — a bare
// imported func call is not in scope for prop discovery).
func (b *byoData) cloneForFile() *byoData {
	return &byoData{
		compStruct:   maps.Clone(b.compStruct),
		structs:      maps.Clone(b.structs),
		inGsx:        maps.Clone(b.inGsx),
		nullaryFuncs: b.nullaryFuncs,
	}
}

// mergeQualified publishes dep's byo facts under a file-scoped import alias.
// Function-component keys ".Card" become ".<alias>.Card" — exactly what
// childInvocation looks up for a `<alias.Card>` tag — and struct type names
// "CardData" become "<alias>.CardData", the qualified type the emitter writes.
// Method components are skipped (a method tag never resolves through an
// import alias); nullaryFuncs are not merged (same reason as cloneForFile).
func (b *byoData) mergeQualified(alias string, dep *byoData) {
	for key, structName := range dep.compStruct {
		if !strings.HasPrefix(key, ".") {
			continue // method component: not invocable through a qualified tag
		}
		b.compStruct["."+alias+key] = alias + "." + structName
	}
	for name, st := range dep.structs {
		b.structs[alias+"."+name] = st
	}
	for name, in := range dep.inGsx {
		b.inGsx[alias+"."+name] = in
	}
}
```

Add `"maps"` and `"strings"` to byo.go imports if absent.

- [ ] **Step 4: Implement per-file facts in module_importer.go**

Delete `mergeImportedComponentPropFields`, `collectImportSpecs`, and `mergeQualifiedPropMap` (lines 284–338). Add:

```go
// fileFacts is the per-.gsx-file view of prop facts: the package's own facts
// plus, for each gsx package imported BY THIS FILE, the dep's facts qualified
// under the file's alias. Go import aliases are file-scoped, so these views
// must be too — a package-wide alias merge collides when two files bind the
// same alias to different packages.
type fileFacts struct {
	propFields map[string]map[string]bool
	nodeProps  map[string]map[string]bool
	attrsProps map[string]map[string]bool
	byo        *byoData
}

// fileImportSpecs extracts f's hoisted import specs with resolved .gsx
// positions (mirroring buildSkeleton's spec-position block: gc.Src starts
// exactly at gc.Pos(), so chunk offset + intra-chunk offset is the absolute
// .gsx offset). Chunk parse errors are skipped here — buildSkeleton surfaces
// them with a clean diagnostic.
func fileImportSpecs(f *gsxast.File, fset *token.FileSet) []importSpec {
	var specs []importSpec
	for _, d := range f.Decls {
		gc, ok := d.(*gsxast.GoChunk)
		if !ok {
			continue
		}
		imps, _, _, err := splitChunk(gc.Src)
		if err != nil {
			continue
		}
		if fset != nil && gc.Pos().IsValid() {
			if tf := fset.File(gc.Pos()); tf != nil {
				base := fset.Position(gc.Pos()).Offset
				for i := range imps {
					imps[i].pos = tf.Pos(base + imps[i].srcOff)
				}
			}
		}
		specs = append(specs, imps...)
	}
	return specs
}

// fileScopedFacts builds the per-file fact view for one parsed .gsx file.
// base facts are shared (not copied) when the file imports no gsx packages;
// otherwise shallow clones are extended with alias-qualified dep entries.
// A dep whose facts cannot be derived (parse/analysis error) is skipped with
// a positioned Warning: its components fall back to the assume-prop regime
// (declared == nil) instead of silently flip-flopping between regimes.
func (m *Module) fileScopedFacts(dir string, f *gsxast.File, propFields, nodeProps, attrsProps map[string]map[string]bool, byo *byoData, bag *diag.Bag, fset *token.FileSet) *fileFacts {
	out := &fileFacts{propFields: propFields, nodeProps: nodeProps, attrsProps: attrsProps, byo: byo}
	cloned := false
	seen := map[string]bool{} // alias+"\x00"+depDir: dedupe repeated specs
	for _, spec := range fileImportSpecs(f, fset) {
		if spec.name == "." || spec.name == "_" {
			continue // dot/blank imports carry no qualified tags
		}
		depDir, ok := dirForImportPath(m.opts.ModuleRoot, m.opts.ModulePath, spec.path)
		if !ok || depDir == dir || !m.isGsxPackage(depDir) {
			continue
		}
		facts, err := m.importedPropFacts(depDir)
		if err != nil {
			pos := fset.Position(spec.pos)
			bag.Add(diag.Diagnostic{
				Start: pos, End: pos, Severity: diag.Warning, Code: "imported-props-unavailable", Source: "codegen",
				Message: fmt.Sprintf("cannot analyze imported gsx package %q: %v; its component props are not discovered (identifier attrs on its components are treated as prop fields)", spec.path, err),
			})
			continue
		}
		alias := spec.name
		if alias == "" {
			alias = facts.pkgName
		}
		key := alias + "\x00" + depDir
		if seen[key] {
			continue
		}
		seen[key] = true
		if !cloned {
			out.propFields = maps.Clone(propFields)
			out.nodeProps = maps.Clone(nodeProps)
			out.attrsProps = maps.Clone(attrsProps)
			out.byo = byo.cloneForFile()
			cloned = true
		}
		for name, fields := range facts.propFields {
			out.propFields[alias+"."+name] = fields
		}
		for name, fields := range facts.nodeProps {
			out.nodeProps[alias+"."+name] = fields
		}
		for name, fields := range facts.attrsProps {
			out.attrsProps[alias+"."+name] = fields
		}
		out.byo.mergeQualified(alias, facts.byo)
	}
	return out
}
```

Note the inner field maps are shared with the cache — they are read-only downstream (`matchField`/`childPropsLiteral` never mutate). State this in a comment if the reviewer asks, but do not deep-copy.

- [ ] **Step 5: Rewire analyze and the two generateFile call sites**

In `analyze` (module_importer.go): delete the `m.mergeImportedComponentPropFields(...)` line (was after `componentPropFieldsFor`). Add a `factsByFile` map and build each file's view inside the skeleton loop, using it for `buildSkeleton`:

```go
	factsByFile := map[string]*fileFacts{}
	for path, f := range gsxFiles {
		ff := m.fileScopedFacts(dir, f, propFields, nodeProps, attrsProps, byo, bag, fset)
		factsByFile[path] = ff
		skel, comps, imps, ctrlOff, berr := buildSkeleton(f, table, ff.propFields, ff.nodeProps, ff.attrsProps, ff.byo, m.opts.FieldMatcher, fset)
		// ... existing error handling and loop body unchanged ...
	}
```

Extend the `analyzed` struct with `factsByFile map[string]*fileFacts` (document: "per-file fact views; propFields/nodeProps/attrsProps/byo keep the package-local base facts") and set it in the return literal.

In `module.go`, change BOTH `generateFile` call sites (`Package` ~line 404, `Generate` ~line 455) from `a.propFields, a.nodeProps, a.attrsProps, a.byo` to the per-file view, e.g. in `Generate`:

```go
		for path, f := range a.gsxFiles {
			ff := a.factsByFile[path]
			gen, ok := generateFile(f, a.resolved, a.table, ff.propFields, ff.nodeProps, ff.attrsProps, ff.byo,
				a.gsxFset, m.opts.Classifier, m.opts.FieldMatcher, bag, m.opts.CSSMin, m.opts.JSMin, m.opts.CSSMinify, m.opts.JSMinify, m.opts.ClassMerger)
```

Mirror at the `Package` site (same substitution; keep its surrounding code unchanged). If a `factsByFile` entry could be nil there (it cannot — both loops iterate `a.gsxFiles`, the same keys analyze populated), don't add a nil guard.

- [ ] **Step 6: Run tests**

Run: `go test ./internal/codegen -run 'TestFileScopedAliasNoCollision|TestImportedPropFactsCachedAndInvalidated' -count=1` → PASS.
Run: `go test ./internal/codegen ./internal/corpus -count=1` → PASS (existing xpkg corpus case must keep identical goldens: the single-import case produces the same facts through the new path).

- [ ] **Step 7: Add the corpus cases**

Create `internal/corpus/testdata/cases/xpkg/imported_alias_collision.txtar` (model the file layout on the existing `xpkg/imported_attrs_fallthrough.txtar` — `go.mod`, packages, `-- invoke --`, goldens):

```
# Two files in one package bind the SAME import alias to DIFFERENT gsx
# packages (legal Go — aliases are file-scoped). Each file's call sites must
# split attrs against ITS OWN import: ui.Panel has a Variant prop, but
# widgets.Panel (aliased ui in b.gsx) does not, so there variant falls
# through to the bag.
-- go.mod --
module example.com/app

go 1.26.1
-- ui/panel.gsx --
package ui

component Panel(variant string) {
	<section data-variant={variant} { attrs... }>{children}</section>
}
-- widgets/panel.gsx --
package widgets

component Panel() {
	<aside { attrs... }>{children}</aside>
}
-- pages/a.gsx --
package pages

import "example.com/app/ui"

component A() {
	<ui.Panel variant="big" class="x">a</ui.Panel>
}
-- pages/b.gsx --
package pages

import ui "example.com/app/widgets"

component B() {
	<ui.Panel variant="big" class="x">b</ui.Panel>
}
-- invoke --
pages.A()
pages.B()
-- diagnostics.golden --
-- render.golden --
```

(Check how multi-component invocation works in existing cases — if `-- invoke --` takes one expression, use a wrapper: add `component Both() { <A/> <B/> }` to a.gsx and invoke `pages.Both()`.)

Create `internal/corpus/testdata/cases/xpkg/imported_byo.txtar`:

```
# An imported BYO component (sole author-struct param) gets the same
# call-site attr splitting as a local one: the qualified struct type
# (ui.CardData) is emitted, class falls through to its Attrs field, and
# children bind to its Children field.
-- go.mod --
module example.com/app

go 1.26.1
-- ui/card.gsx --
package ui

type CardData struct {
	Title    string
	Attrs    gsx.Attrs
	Children gsx.Node
}

component Card(d CardData) {
	<div class="card" { d.Attrs... }><h2>{d.Title}</h2>{d.Children}</div>
}
-- pages/home.gsx --
package pages

import "example.com/app/ui"

component Home() {
	<ui.Card title="T" class="x">body</ui.Card>
}
-- invoke --
pages.Home()
-- diagnostics.golden --
-- render.golden --
```

Before finalizing, check an existing BYO corpus case (`grep -rl "component.*(d " internal/corpus/testdata/cases | head`) for the exact BYO syntax (struct in GoChunk, `{ d.Attrs... }` spread form, whether `import "github.com/gsxhq/gsx"` must be explicit in the GoChunk) and mirror it.

Regenerate goldens: `go test ./internal/corpus -run TestCorpus -update`, inspect the new `generated.x.go.golden` (a.gsx must contain `Variant: "big"`, b.gsx must contain `{Key: "variant"...}`; imported_byo must construct `ui.CardData{...}`), then `go test ./internal/corpus -count=1` → PASS.

- [ ] **Step 8: Add the dep-failure warning test**

Append to `depfacts_test.go`:

```go
// A dep with a transient parse error must degrade VISIBLY: the importer still
// generates (assume-prop regime) and carries a positioned warning naming the dep.
func TestImportedPropFactsFailureWarns(t *testing.T) {
	root, uiDir, pagesDir := writeDepFactsModule(t)
	m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	m.SetOverride(filepath.Join(uiDir, "card.gsx"), []byte("package ui\n\ncomponent Card( {\n"))
	_, diags, err := m.Generate(pagesDir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range diags {
		if d.Code == "imported-props-unavailable" && d.Severity == diag.Warning {
			found = true
			if d.Start.Line == 0 {
				t.Errorf("warning should be positioned at the import spec; got %+v", d)
			}
		}
	}
	if !found {
		t.Fatalf("expected imported-props-unavailable warning; diags = %+v", diags)
	}
}
```

Add `"github.com/gsxhq/gsx/internal/diag"` to imports. Note: `Generate(pages)` will also carry type errors from the broken dep import — the test only asserts the warning exists alongside them. Run: `go test ./internal/codegen -run TestImportedPropFactsFailureWarns -count=1` → PASS (if it fails because the warning is missing, the `fileScopedFacts` error branch has a bug — fix it, don't weaken the test).

- [ ] **Step 9: Commit**

```bash
git add internal/codegen internal/corpus/testdata
git commit -m "fix(codegen): file-scoped imported prop facts with BYO merge and visible fallback"
```

---

### Task 3: Compose `attrs={{ }}` with the fallthrough bag (no duplicate Attrs field)

Fixes the confirmed duplicate-field bug: `<ui.Panel attrs={{ "data-a": "1" }} data-b="2">` currently emits `Attrs:` twice → raw go/types error, package skipped. New semantics (locked decision 3): the literal merges LAST via `.Merge(...)`; two literals error.

**Files:**
- Modify: `internal/codegen/emit.go` (`childPropsLiteral` ~lines 2742–2812, `propFieldEntry` ~line 2449, hoist pass ~lines 2354–2378)
- Test: corpus `internal/corpus/testdata/cases/xpkg/imported_attrs_literal_merge.txtar` + a same-package sibling (Step 5 locates the right dir), `internal/codegen/fieldmatch_test.go` (Task 4 covers matcher units)

**Interfaces:**
- Consumes: per-file facts from Task 2 (no signature changes needed here).
- Produces: `propFieldEntry` gains `oaLit string` (the bare `gsx.Attrs{…}` literal text) and `oaMergePrefix string` (the composed bag expression, "" when the literal stands alone). The hoist pass and final-literal assembly honor both.

- [ ] **Step 1: Write the failing corpus case**

Create `internal/corpus/testdata/cases/xpkg/imported_attrs_literal_merge.txtar` (same layout discipline as `imported_attrs_fallthrough.txtar`):

```
# An explicit attrs={{ }} literal composes with other bag contributors
# instead of emitting a duplicate Attrs field: bare attrs form the base
# literal, spreads merge in source order, and the attrs={{ }} literal
# merges LAST.
-- go.mod --
module example.com/app

go 1.26.1
-- ui/panel.gsx --
package ui

component Panel() {
	<section { attrs... }>{children}</section>
}
-- pages/home.gsx --
package pages

import "example.com/app/ui"

component Home() {
	<ui.Panel data-a="1" attrs={{ "data-b": "2" }}>p</ui.Panel>
}
-- invoke --
pages.Home()
-- diagnostics.golden --
-- render.golden --
<section data-a="1" data-b="2">p</section>
```

Run: `go test ./internal/corpus -run 'TestCorpus/xpkg/imported_attrs_literal_merge' -count=1`
Expected: FAIL — today this produces the `duplicate field name Attrs in struct literal` type error (empty output + diagnostic), not the golden render. (The corpus harness will complain about missing goldens/manifest first — that still demonstrates the case can't pass; optionally run once with `-update` to see the duplicate-field diagnostic land in `diagnostics.golden`, then revert that golden.)

- [ ] **Step 2: Implement in childPropsLiteral**

In `propFieldEntry` (emit.go ~2449) add two fields with doc comments:

```go
	oaLit         string // bare `<rtPkg>.Attrs{…}` literal text for an Attrs-targeted ordered-attrs attr (fieldName == "Attrs")
	oaMergePrefix string // composed fallthrough-bag expression the literal merges onto; "" when the literal stands alone
```

In the `*ast.OrderedAttrsAttr` case (~2742): after `matchOrderedAttrsField` and `validateMatchedField`, detect a duplicate Attrs-targeted literal, and record `oaLit`:

```go
		case *ast.OrderedAttrsAttr:
			fn, ok := matchOrderedAttrsField(declared, t.Name, fm)
			if !ok {
				// ... existing ordered-attrs-no-field error unchanged ...
			}
			if verr := validateMatchedField(fn, t.Name, propsType, declared); verr != nil {
				// ... existing bad-field-match error unchanged ...
			}
			if fn == "Attrs" && attrsLitIdx >= 0 {
				msg := fmt.Sprintf("duplicate ordered-attrs literal targeting the Attrs bag on <%s>; combine the pairs into one {{ }} literal", el.Tag)
				return nil, "", nil, &attrError{pos: t.Pos(), end: t.End(), code: "ordered-attrs-duplicate", msg: msg}
			}
			// ... existing pairEntries loop unchanged ...
			var sb strings.Builder
			fmt.Fprintf(&sb, "%s.Attrs{", rtPkg)
			for _, pr := range t.Pairs {
				// ... existing probeWrap pair loop unchanged, writing into sb ...
			}
			sb.WriteString("}")
			lit := sb.String()
			if fn == "Attrs" {
				attrsLitIdx = len(fields)
			}
			fields = append(fields, propFieldEntry{
				str:       fn + ": " + lit,
				fieldName: fn,
				oa:        t,
				oaPairs:   pairEntries,
				oaLit:     lit,
			})
```

Declare `attrsLitIdx := -1` next to the `bag`/`mergeChain` declarations (~2602). Note the literal string no longer embeds `fn:` inside the Sprintf — build `lit` bare and prefix `fn + ": "` when appending, as shown.

Rewrite the final bag-append block (~2802–2812) to compose:

```go
	if len(bag) > 0 || len(mergeChain) > 0 {
		// BYO: unmatched attrs route to an explicit `Attrs gsx.Attrs` field. Missing
		// → a clear error (the author adds it and spreads it in the markup).
		if isByoChild && !byoStr.hasAttrs {
			msg := fmt.Sprintf("attribute on <%s> matches no field of its Props type %s and %s has no `Attrs gsx.Attrs` field", el.Tag, propsType, propsType)
			return nil, "", nil, &attrError{pos: el.Pos(), end: el.End(), code: "byo-missing-attrs", msg: msg}
		}
		attrsExpr := fmt.Sprintf("%s.Attrs{%s}", rtPkg, strings.Join(bag, ", "))
		attrsExpr += strings.Join(mergeChain, "")
		if attrsLitIdx >= 0 {
			// An explicit attrs={{ }} literal coexists with other bag
			// contributors: fold them into ONE Attrs field — the composed bag
			// first, the literal merged last — instead of emitting a duplicate
			// struct field. The hoist pass rebuilds this composition when pair
			// values are hoisted, keyed off oaMergePrefix.
			fields[attrsLitIdx].oaMergePrefix = attrsExpr
			fields[attrsLitIdx].str = fmt.Sprintf("Attrs: %s.Merge(%s)", attrsExpr, fields[attrsLitIdx].oaLit)
		} else {
			fields = append(fields, propFieldEntry{str: "Attrs: " + attrsExpr})
		}
	}
```

- [ ] **Step 3: Teach the hoist pass the composed form**

In the `case fe.oa != nil:` branch of the hoist pass (~2354), the rebuilt string must honor the prefix. Replace the literal assembly:

```go
			case fe.oa != nil:
				// Hoist tuple/call pairs and rebuild the gsx.Attrs{…}
				// literal; non-call pairs stay inline (see the ExprAttr note).
				var sb strings.Builder
				sb.WriteString("gsx.Attrs{")
				for j, pr := range fe.oaPairs {
					// ... existing per-pair hoist switch unchanged, writing {Key,Value} into sb ...
				}
				sb.WriteString("}")
				if fe.oaMergePrefix != "" {
					fieldEntries[i].str = fmt.Sprintf("Attrs: %s.Merge(%s)", fe.oaMergePrefix, sb.String())
				} else {
					fieldEntries[i].str = fmt.Sprintf("%s: %s", fe.fieldName, sb.String())
				}
```

(The old code wrote `fmt.Fprintf(&sb, "%s: gsx.Attrs{", fe.fieldName)` — the field-name prefix moves out of the builder.)

Evaluation-order note for the reviewer: `oaMergePrefix` may contain call expressions (spreads/conditional attrs). Those already evaluate inside the final literal today; hoisted pair temps evaluate just before the `Node` call, i.e. before the prefix's calls. That reordering is observable only via side-effecting attr expressions interleaved with side-effecting spread expressions — accept it and say so in the commit message (it mirrors the pre-existing hoist behavior for prop fields vs. bag values).

- [ ] **Step 4: Regenerate and verify the corpus case**

```bash
go test ./internal/corpus -run TestCorpus -update
go test ./internal/corpus ./internal/codegen -count=1
```

Inspect `imported_attrs_literal_merge.txtar`'s `generated.x.go.golden`: exactly ONE `Attrs:` entry of the form `Attrs: gsx.Attrs{{Key: "data-a", Value: "1"}}.Merge(gsx.Attrs{{Key: "data-b", Value: "2"}})`. Render golden must match the case's pinned `<section data-a="1" data-b="2">p</section>`.

- [ ] **Step 5: Add the same-package and tuple-interplay cases**

Find the directory that holds same-package ordered-attrs cases: `grep -rl '{{' internal/corpus/testdata/cases --include='*.txtar' | head` and pick the dir with `bagProp={{`-style cases. Add two cases there (same-package `component Panel() { <section { attrs... }>{children}</section> }`):

1. `attrs_literal_merge.txtar` — `<Panel data-a="1" attrs={{ "data-b": "2" }} { extra... }>p</Panel>` with `extra := gsx.Attrs{{Key: "data-c", Value: "3"}}` bound via a Go chunk or an inline var (mirror how existing cases bind spread sources). Pins order: base bag (data-a), then `.Merge(extra)`, then `.Merge(literal)`. Render golden: `<section data-a="1" data-c="3" data-b="2">p</section>` — CHECK actual merge render order against the runtime's Attrs.Merge before pinning; write what the runtime actually does.
2. `attrs_literal_duplicate_error.txtar` — `<Panel attrs={{ "a": "1" }} attrs={{ "b": "2" }}/>` with `diagnostics.golden` pinning the `ordered-attrs-duplicate` message and empty generated output (mirror an existing error-case txtar for the golden shape).

Also extend an existing tuple-pair case (or add `attrs_literal_merge_tuple.txtar`): `<Panel data-a="1" attrs={{ "data-b": f() }}/>` where `f` returns `(string, error)` — pins that hoisting rebuilds the composed `.Merge` form (generated golden shows `_gsxv0, _gsxerr := f()` hoist AND the single merged Attrs entry).

Regenerate (`-update`), verify without `-update`.

- [ ] **Step 6: Commit**

```bash
git add internal/codegen/emit.go internal/corpus/testdata
git commit -m "fix(codegen): compose attrs={{ }} with the fallthrough bag instead of duplicating the Attrs field"
```

---

### Task 4: matchOrderedAttrsField unit tests + declared-nil corpus pins

Pins locked decision 2 (declared-nil acceptance) and closes the unit-test gap for the matcher.

**Files:**
- Test: `internal/codegen/fieldmatch_test.go` (extend; create if absent)
- Test: corpus `internal/corpus/testdata/cases/xpkg/go_component_attrs_literal.txtar`, `internal/corpus/testdata/cases/xpkg/go_component_attrs_literal_missing.txtar`

- [ ] **Step 1: Write the matcher unit tests**

```go
func TestMatchOrderedAttrsField(t *testing.T) {
	cases := []struct {
		name     string
		declared map[string]bool
		attr     string
		want     string
		ok       bool
	}{
		{"declared prop field", map[string]bool{"Extra": true}, "extra", "Extra", true},
		{"synthesized Attrs lowercase", map[string]bool{"Attrs": true}, "attrs", "Attrs", true},
		{"synthesized Attrs capitalized", map[string]bool{"Attrs": true}, "Attrs", "Attrs", true},
		{"no Attrs field declared", map[string]bool{"Title": true}, "attrs", "", false},
		{"nil declared assumes Attrs", nil, "attrs", "Attrs", true},
		{"nil declared non-attrs identifier", nil, "extra", "Extra", true}, // assume-prop regime
		{"nil declared kebab falls through", nil, "data-x", "", false},
		{"kebab never targets Attrs", map[string]bool{"Attrs": true}, "at-trs", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := matchOrderedAttrsField(tc.declared, tc.attr, nil)
			if got != tc.want || ok != tc.ok {
				t.Errorf("matchOrderedAttrsField(%v, %q) = (%q, %v); want (%q, %v)", tc.declared, tc.attr, got, ok, tc.want, tc.ok)
			}
		})
	}
}
```

Verify the `"nil declared non-attrs identifier"` expectation against `matchField`'s nil-declared branch before running — it returns `(Extra, true)` for identifier attrs; if the test disagrees with the code, the TEST is wrong (this task changes no behavior).

Run: `go test ./internal/codegen -run TestMatchOrderedAttrsField -count=1` → PASS (this is a characterization test; it should pass immediately — its value is pinning the contract).

- [ ] **Step 2: Corpus: hand-written Go component with an Attrs field (positive)**

Create `internal/corpus/testdata/cases/xpkg/go_component_attrs_literal.txtar`. Model the hand-written-Go-component mechanics on the existing `xpkg/cross_package.txtar` (open it first; mirror its go.mod/runtime import shape exactly):

```
# attrs={{ }} on a component from a plain Go package (no .gsx → no prop
# discovery, declared == nil): accepted, and the hand-written Props type's
# Attrs field receives the bag. Pins locked decision: blind-regime
# acceptance enables hand-written components with an Attrs field.
-- go.mod --
module example.com/app

go 1.26.1
-- uigo/widget.go --
package uigo

import "github.com/gsxhq/gsx"

type WidgetProps struct {
	Attrs gsx.Attrs
}

func Widget(p gsx.Node) ... // REPLACE: copy the working hand-written component shape from cross_package.txtar
-- pages/home.gsx --
package pages

import "example.com/app/uigo"

component Home() {
	<uigo.Widget attrs={{ "data-x": "1" }}/>
}
-- invoke --
pages.Home()
-- diagnostics.golden --
-- render.golden --
```

The `widget.go` body must actually render its Attrs (e.g. build a `<span>` via the runtime API used in cross_package.txtar) so `render.golden` proves the bag arrived. Regenerate goldens with `-update`; verify.

- [ ] **Step 3: Corpus: missing Attrs field (negative, pins the failure shape)**

Create `go_component_attrs_literal_missing.txtar`: same layout but `WidgetProps` has NO Attrs field. `diagnostics.golden` pins the resulting go/types error (`unknown field Attrs in struct literal ...`) and there is no render golden content (package skipped). This documents the known-rough failure mode the review accepted; if a later change improves the diagnostic, this golden is where it shows up.

Regenerate with `-update`; verify without.

- [ ] **Step 4: Commit**

```bash
git add internal/codegen/fieldmatch_test.go internal/corpus/testdata
git commit -m "test(codegen): pin matchOrderedAttrsField contract and declared-nil attrs={{ }} behavior"
```

---

### Task 5: Cache key walks `.gsx`-hoisted dependency edges

Fixes the confirmed stale-cache bug: an importer's output depends on its dep's `.gsx` content, but `computeKey` discovers in-module deps only via `go list` over `.go`/`.x.go` — a dep reachable solely through a `.gsx` import with no on-disk `.x.go` is invisible.

**Files:**
- Create: `internal/codegen/gsximports.go`
- Modify: `gen/cachekey.go` (`computeKey` ~line 139, new `gsxDepDirs`)
- Modify: `gen/cache.go` (~line 118, thread `moduleRoot`)
- Test: `internal/codegen/gsximports_test.go`, `gen/cachekey_test.go` (extend existing or create)

**Interfaces:**
- Produces: `codegen.GsxHoistedImportPaths(dir string) []string` (exported; gen already imports `internal/codegen`). `computeKey` gains a `moduleRoot string` parameter.

- [ ] **Step 1: Write the failing key test**

In `gen/cachekey_test.go` (check for an existing computeKey test to extend; otherwise create):

```go
// A dep reachable ONLY through a .gsx-hoisted import (no .x.go on disk, so
// go list has no edge) must still be folded into the importer's cache key:
// editing the dep changes the key. Covers the transitive chain
// pages → ui → icons as well.
func TestComputeKeyGsxOnlyDeps(t *testing.T) {
	root := t.TempDir()
	mk := func(rel, src string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("go.mod", "module example.com/app\n\ngo 1.26.1\n")
	mk("icons/icon.gsx", "package icons\n\ncomponent Dot() {\n\t<i/>\n}\n")
	mk("ui/card.gsx", "package ui\n\nimport \"example.com/app/icons\"\n\ncomponent Card() {\n\t<icons.Dot/>\n}\n")
	mk("pages/home.gsx", "package pages\n\nimport \"example.com/app/ui\"\n\ncomponent Home() {\n\t<ui.Card/>\n}\n")

	graph, err := loadGraph(root)
	if err != nil {
		t.Fatal(err)
	}
	pagesDir := filepath.Join(root, "pages")
	key := func() string {
		k, err := computeKey(pagesDir, graph, "example.com/app", "gm", "gs", "bctx", "cid", nil, nil, "cls", false, false, false, nil, root)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	k1 := key()
	// Direct .gsx-only dep edit changes the key.
	mk("ui/card.gsx", "package ui\n\nimport \"example.com/app/icons\"\n\ncomponent Card(variant string) {\n\t<icons.Dot/>\n}\n")
	k2 := key()
	if k1 == k2 {
		t.Fatal("editing ui (direct .gsx-only dep) did not change pages' cache key")
	}
	// Transitive .gsx-only dep edit changes the key.
	mk("icons/icon.gsx", "package icons\n\ncomponent Dot() {\n\t<b/>\n}\n")
	k3 := key()
	if k2 == k3 {
		t.Fatal("editing icons (transitive .gsx-only dep) did not change pages' cache key")
	}
}
```

Match `computeKey`'s current parameter order when appending `moduleRoot` last (adjust the call if you place the param elsewhere; keep test and signature in sync). Note `loadGraph` may return no entry for pure-.gsx dirs — that's the point.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./gen -run TestComputeKeyGsxOnlyDeps -count=1`
Expected: FAIL — compile error first (`computeKey` has no moduleRoot param); after adding the param but before implementing the walk, k1 == k2.

- [ ] **Step 3: Implement GsxHoistedImportPaths in codegen**

Create `internal/codegen/gsximports.go`:

```go
package codegen

import (
	"go/token"
	"os"
	"path/filepath"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/attrclass"
	gsxparser "github.com/gsxhq/gsx/parser"
)

// GsxHoistedImportPaths returns the import paths hoisted from the GoChunks of
// every .gsx file in dir (disk content only — no Module overrides). It exists
// for gen's incremental cache key: an importer's generated output depends on
// its dep's .gsx-declared component props, but `go list` cannot see a dep
// whose only edge is a .gsx import with no .x.go on disk yet. Unparseable
// files are skipped: a .gsx that cannot parse cannot generate output, so a
// missed edge cannot serve stale output for it.
func GsxHoistedImportPaths(dir string) []string {
	matches, _ := filepath.Glob(filepath.Join(dir, "*.gsx"))
	fset := token.NewFileSet()
	var out []string
	for _, p := range matches {
		src, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		f, perrs := gsxparser.ParseFileWithClassifier(fset, p, src, 0, attrclass.Builtin())
		if len(perrs) > 0 {
			continue
		}
		for _, d := range f.Decls {
			gc, ok := d.(*gsxast.GoChunk)
			if !ok {
				continue
			}
			imps, _, _, err := splitChunk(gc.Src)
			if err != nil {
				continue
			}
			for _, s := range imps {
				out = append(out, s.path)
			}
		}
	}
	return out
}
```

(Confirm the `attrclass` import path by checking `module.go`'s imports; if `ParseFileWithClassifier` accepts a nil classifier — check `parser/file.go:41` — prefer nil and drop the import.)

Unit test `internal/codegen/gsximports_test.go`:

```go
func TestGsxHoistedImportPaths(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.gsx"), "package p\n\nimport (\n\t\"example.com/app/ui\"\n\tw \"example.com/app/widgets\"\n)\n\ncomponent A() {\n\t<ui.X/>\n\t<w.Y/>\n}\n")
	writeFile(t, filepath.Join(dir, "broken.gsx"), "package p\n\ncomponent B( {\n")
	got := GsxHoistedImportPaths(dir)
	want := []string{"example.com/app/ui", "example.com/app/widgets"}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("GsxHoistedImportPaths = %v; want %v", got, want)
	}
}
```

- [ ] **Step 4: Implement the closure walk in gen**

In `gen/cachekey.go`, add:

```go
// gsxDepDirs returns every in-module package dir reachable from dir through
// the union of .gsx-hoisted import edges and go-list dep edges, excluding dir
// itself. go list alone misses a dep whose only edge is a .gsx import with no
// .x.go on disk, so the walk follows both edge kinds transitively.
func gsxDepDirs(dir string, graph map[string]pkgInfo, moduleRoot, modPath string) []string {
	byDir := map[string]pkgInfo{}
	for _, p := range graph {
		if p.Dir != "" {
			byDir[filepath.Clean(p.Dir)] = p
		}
	}
	dirFor := func(importPath string) (string, bool) {
		if importPath == modPath {
			return filepath.Clean(moduleRoot), true
		}
		if !strings.HasPrefix(importPath, modPath+"/") {
			return "", false
		}
		rel := strings.TrimPrefix(importPath, modPath+"/")
		return filepath.Join(moduleRoot, filepath.FromSlash(rel)), true
	}
	seen := map[string]bool{dir: true}
	queue := []string{dir}
	var out []string
	for len(queue) > 0 {
		d := queue[0]
		queue = queue[1:]
		var neighborPaths []string
		neighborPaths = append(neighborPaths, codegen.GsxHoistedImportPaths(d)...)
		if p, ok := byDir[d]; ok {
			neighborPaths = append(neighborPaths, p.Deps...)
		}
		for _, ip := range neighborPaths {
			dd, ok := dirFor(ip)
			if !ok || seen[dd] {
				continue
			}
			if _, err := os.Stat(dd); err != nil {
				continue // import of a not-yet-existing package: nothing to hash
			}
			seen[dd] = true
			out = append(out, dd)
			queue = append(queue, dd)
		}
	}
	sort.Strings(out)
	return out
}
```

In `computeKey`, add the trailing parameter `moduleRoot string` and REPLACE the `self.Deps` loop (lines ~153–172) with the unified walk:

```go
	// Dep hashes: every in-module package reachable through go-list edges OR
	// .gsx-hoisted import edges. The .gsx walk (gsxDepDirs) is what keeps the
	// key honest when a dep's .x.go is not on disk (fresh checkout, cleaned
	// outputs): the dep's component prop fields drive this package's attr
	// splitting, so its .gsx content must be an input to the key.
	var depHashes []string
	for _, depDir := range gsxDepDirs(dir, graph, moduleRoot, modPath) {
		dh, err := dirSourceHash(depDir)
		if err != nil {
			return "", err
		}
		rel, rerr := filepath.Rel(moduleRoot, depDir)
		if rerr != nil {
			rel = depDir
		}
		depHashes = append(depHashes, filepath.ToSlash(rel)+":"+dh)
	}
	sort.Strings(depHashes)
```

(Dep labels switch from import path to module-relative dir — fine: any label change just busts every existing cache entry once, which is safe by construction.) External/stdlib deps stay pinned by goMod/goSum/buildCtx exactly as before — `dirFor` filters to the module prefix.

Update the caller in `gen/cache.go` (~line 118) to pass `moduleRoot` (the variable feeding `loadGraph` in the same function — confirm its name there).

- [ ] **Step 5: Run tests**

Run: `go test ./gen ./internal/codegen -count=1`
Expected: PASS including `TestComputeKeyGsxOnlyDeps` (k1 ≠ k2 ≠ k3). Existing gen cache tests must still pass; if a test pinned old dep-label formats, update it to the new labels (that's the one-time bust).

- [ ] **Step 6: Commit**

```bash
git add internal/codegen/gsximports.go internal/codegen/gsximports_test.go gen/cachekey.go gen/cache.go gen/cachekey_test.go
git commit -m "fix(gen): fold .gsx-only dependency edges into the incremental cache key"
```

---

### Task 6: Canonical lowercase spelling, spelling-equivalence pin, docs

**Files:**
- Modify: `internal/corpus/testdata/cases/xpkg/imported_attrs_fallthrough.txtar` (flip `Attrs={{` → `attrs={{`; goldens unchanged — already verified live in review)
- Create: same-package corpus case `ordered_attrs_target_attrs.txtar` in the dir found in Task 3 Step 5
- Modify: `docs/guide/syntax.md` (ordered-attrs section, ~line 66)
- Modify: `docs/ROADMAP.md` (mark imported-props discovery shipped/hardened if listed)

- [ ] **Step 1: Flip the existing case to lowercase**

Edit `imported_attrs_fallthrough.txtar`: `<ui.Panel Attrs={{ ... }}>` → `<ui.Panel attrs={{ ... }}>`. Run `go test ./internal/corpus -run 'TestCorpus/xpkg/imported_attrs_fallthrough' -count=1` — must PASS with UNCHANGED goldens (byte-identical output is the point being pinned).

- [ ] **Step 2: Same-package + spelling-equivalence case**

Create `ordered_attrs_target_attrs.txtar` in the same dir as the other ordered-attrs cases:

```
# The synthesized Attrs bag can be targeted explicitly with an ordered-attrs
# literal on a SAME-PACKAGE component. Canonical spelling is lowercase
# attrs={{ }}; the capitalized form is equivalent via capitalize-first attr
# matching — both render identically.
-- input.gsx --
package p

component Panel() {
	<section { attrs... }>{children}</section>
}

component Home() {
	<Panel attrs={{ "data-a": "1" }}>x</Panel>
	<Panel Attrs={{ "data-a": "1" }}>x</Panel>
}
-- invoke --
p.Home()
-- render.golden --
```

(Match the single-package txtar layout of neighbors in that dir — some dirs use `input.gsx`, others use named files; mirror exactly.) Regenerate with `-update`; verify the two `<section>` outputs in `render.golden` are identical; verify without `-update`.

- [ ] **Step 3: Docs**

In `docs/guide/syntax.md`, find the ordered-attrs `name={{ … }}` passage (~line 66, "binds to a gsx.Attrs prop"). Extend it, wrapped in `::: v-pre` (check whether the section already sits in one):

- `attrs={{ "key": value, … }}` targets the component's synthesized attrs bag explicitly — same destination as writing the attrs individually; lowercase `attrs` is the canonical spelling.
- Merge order when combined with other fallthrough attributes: bare attrs form the base, `{ expr... }` spreads and conditional attrs merge in source order, the `attrs={{ }}` literal merges last. Two `attrs={{ }}` literals on one element are an error.
- Imported components (same module): component props are discovered automatically, so bare-attr fallthrough and `attrs={{ }}` behave exactly as for same-package components. For components gsx cannot analyze (other modules, plain Go packages), identifier attrs are treated as prop fields and `attrs={{ }}` requires the Props type to declare `Attrs gsx.Attrs`; when a dep can't be analyzed, generation continues with a `imported-props-unavailable` warning.

Check `docs/guide/` for a components/cross-package page (`ls docs/guide`) and add the imported-components paragraph there if a more natural home exists (link from syntax.md instead of duplicating). Build check: if `make ci`'s docs job is out of scope locally, at minimum grep your added prose for unwrapped `{{`.

- [ ] **Step 4: ROADMAP review**

Open `docs/ROADMAP.md`; if imported-component prop discovery or the one-learning migration blockers are tracked, update status lines to reflect this work. If nothing applies, note that in the commit message body instead of inventing an entry.

- [ ] **Step 5: Commit**

```bash
git add internal/corpus/testdata docs/guide docs/ROADMAP.md
git commit -m "docs+corpus: canonical lowercase attrs={{ }}, spelling pin, imported-props docs"
```

---

### Task 7: Full verification + adversarial review

- [ ] **Step 1: Full CI mirror**

Run: `make ci` (uncached, both modules, examples drift, gofmt + gsx fmt). Expected: PASS. Fix anything it flags (gofmt drift in new files is the usual suspect).

- [ ] **Step 2: Warm-path sanity probe**

Write a throwaway probe (scratchpad, not committed): open a Module over a 3-package tree (pages→ui→icons), `Generate(pages)` once, then loop 50× `SetOverride(pages/home.gsx, tweaked)` + `Generate(pages)` and assert via the `externalLoads()`/`filterTableLoads()` test hooks (and a counter on `importedPropFacts` cache hits if needed) that dep packages are NOT re-parsed per edit and no `packages.Load` runs in the warm loop. This is the finding-3 regression check that unit tests approximate but a live loop proves.

- [ ] **Step 3: Independent adversarial review**

Per repo convention, dispatch an independent adversarial reviewer (fresh subagent) that builds throwaway probe programs against the worktree — at minimum: the alias-collision flip test under `-count=10`, a stale-cache reproduction of finding 1's exact scenario (generate → clean .x.go → edit dep → generate; assert fresh output), and the duplicate-Attrs probe from the original review (must now render). Reviewer findings feed fixes before merge.

- [ ] **Step 4: Squash-review the branch and hand off**

Verify `git log` tells a reviewable story on top of `5aa6c73`; then use superpowers:finishing-a-development-branch to integrate (merge to main — note main itself is still unpushed, so the final push publishes `5aa6c73` + this hardening together).

---

## Self-Review Notes

- Finding→task map: 1→Task 5, 2→Task 2, 3→Task 1 (+7.2 probe), 4→Task 2, 5→Task 3, 6→Task 2 (warning), 7→Task 4 (pins), 8→Tasks 3/4/6 (corpus+units), 9→Task 6 (docs). All nine covered.
- Known intentional non-goals: dot-imports still skipped (no qualified tags); dep `nullaryFuncs` not merged (bare imported func calls unchanged); external-module gsx packages stay in the assume-prop regime; evaluation-order caveat in Task 3 Step 3 accepted and documented.
- Two spots the implementer must resolve against live code (flagged inline, with the lookup command): the corpus `-- invoke --` multi-component shape (Task 2 Step 7) and the hand-written Go component body (Task 4 Step 2) — both are copy-from-existing-case, not design decisions.
