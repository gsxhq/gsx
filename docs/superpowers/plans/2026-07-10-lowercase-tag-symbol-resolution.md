# Lowercase Tag Symbol Resolution Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Lowercase tags resolve against package-level declarations (component call) with leaf-element fallback, self-exclusion for the wrapper pattern, and two safety diagnostics — per `docs/superpowers/specs/2026-07-10-lowercase-tag-symbol-resolution-design.md`.

**Architecture:** A single resolve pass runs at the top of `Module.analyze` (the choke point for Generate, Package/LSP, and the corpus): it computes the package's declared-name set syntactically and stamps every `ast.Element` with a new `IsComponent bool` field. All 11 codegen call sites and 3 LSP call sites of the old string-based `isComponentTag` become field reads — no signature threading. The parser's type-arg gate loosens to any tag; a leaf tag carrying type args becomes a codegen error. Diagnostics (self-reference warning, wrapper-cycle warning) hang off the same pass.

**Tech Stack:** Go, `go/parser` (syntax-only — **no `packages.Load` anywhere in this feature**), txtar corpus.

## Global Constraints

- Work in a git worktree branch (e.g. `lowercase-tag-resolution`) via `superpowers:using-git-worktrees`.
- Runtime (root `gsx` package) stays stdlib-only; all changes here are in `ast/`, `parser/`, `internal/codegen/`, `internal/corpus/`, `internal/lsp/`, docs.
- No `packages.Load` on any new path. The decl-name scan is `go/parser` syntax-only (risk gate in spec).
- Every behavior change ships a corpus case; regen with `go test ./internal/corpus -run TestCorpus -update`, then verify WITHOUT `-update`. Never hand-edit goldens.
- Inner loop: `make check`. Before merge: `make ci` (Go pinned to `GO_VERSION` in ci.yml, currently 1.26.1).
- The parser must NEVER set `Element.IsComponent` — it is a codegen-computed semantic fact, so the gsxfmt faithfulness harness (parse↔print equality) is unaffected. `go test ./internal/gsxfmt` must stay green with zero fmt-corpus changes.
- No reserved HTML table in *resolution*. The WHATWG element list appears ONLY in the self-reference diagnostic (Task 6).
- Spec risk gates: if generation perf measurably regresses (Task 9 measures) or the call-site flip stops being mechanical, STOP and reassess with the user.

---

### Task 1: Package decl-name set (`packageDeclNames`)

**Files:**
- Create: `internal/codegen/declnames.go`
- Test: `internal/codegen/declnames_test.go`

**Interfaces:**
- Consumes: existing patterns `packageTypeNames` (`internal/codegen/byo.go:247`) and `gsxChunkTypeNames` (`internal/codegen/byo.go:288`).
- Produces: `func packageDeclNames(dir string, files map[string]*gsxast.File) map[string]bool` — the set of package-level declared bare names. Task 2 consumes it.

Rules (from spec): count package-level `func` (NO methods), `var`, `type`, `const` from hand-written `.go` files (skip `_test.go`, `.x.go`); count `.gsx` declarations: `component` decls WITHOUT receiver (`c.Recv == ""`), plus top-level decl names inside `GoChunk.Src` and `GoWithElements` parts. Do NOT count import names (never scanned — we only read decls). Build tags ignored (textual presence, PR #43 stance).

- [ ] **Step 1: Write the failing test**

```go
package codegen

import (
	"os"
	"path/filepath"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/parser"
)

func parseGSXForTest(t *testing.T, src string) *gsxast.File {
	t.Helper()
	f, err := parser.Parse("test.gsx", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return f
}
// NOTE: check parser's exact entry (grep `func Parse` in parser/) and reuse an
// existing codegen test helper if one parses .gsx already.

func TestPackageDeclNames(t *testing.T) {
	dir := t.TempDir()
	// Hand-written .go: func, method, var, type, const, import.
	if err := os.WriteFile(filepath.Join(dir, "helpers.go"), []byte(`package views

import "time"

func data() string { return time.Now().String() }
func (p page) method() {}
var count int
type page struct{}
const limit = 3
`), 0o644); err != nil {
		t.Fatal(err)
	}
	// _test.go and .x.go must be skipped.
	os.WriteFile(filepath.Join(dir, "x_test.go"), []byte("package views\nfunc testOnly() {}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "gen.x.go"), []byte("package views\nfunc generated() {}\n"), 0o644)

	gsx := parseGSXForTest(t, `package views

component card() {
	<div>x</div>
}

component (p page) row() {
	<li>x</li>
}

func chunkFunc() string { return "" }
`)
	got := packageDeclNames(dir, map[string]*gsxast.File{"a.gsx": gsx})

	for _, want := range []string{"data", "count", "page", "limit", "card", "chunkFunc"} {
		if !got[want] {
			t.Errorf("missing %q", want)
		}
	}
	for _, absent := range []string{"method", "row", "time", "testOnly", "generated"} {
		if got[absent] {
			t.Errorf("must not contain %q", absent)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/codegen -run TestPackageDeclNames -v`
Expected: FAIL — `undefined: packageDeclNames`

- [ ] **Step 3: Implement `internal/codegen/declnames.go`**

```go
package codegen

import (
	goast "go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
)

// packageDeclNames returns the set of package-level declared bare names for
// the package in dir: top-level func (methods excluded), var, type, and const
// names from hand-written .go files plus the package's .gsx files (receiver-
// less component decls, and decls inside GoChunk / GoWithElements Go source).
// Import names are never counted (imports are file-scoped, not declarations).
// Syntax-only (go/parser), build-tag-oblivious, skips _test.go and .x.go —
// same file-walk rules as packageTypeNames/packageNullaryFuncs (byo.go).
// This set is the resolution input for lowercase tags: see resolveComponentTags.
func packageDeclNames(dir string, files map[string]*gsxast.File) map[string]bool {
	out := map[string]bool{}
	collectGoDecls := func(f *goast.File) {
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *goast.FuncDecl:
				if d.Recv == nil && d.Name != nil {
					out[d.Name.Name] = true
				}
			case *goast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *goast.TypeSpec:
						out[s.Name.Name] = true
					case *goast.ValueSpec: // var + const
						for _, n := range s.Names {
							out[n.Name] = true
						}
					}
				}
			}
		}
	}
	if dir != "" {
		if entries, err := os.ReadDir(dir); err == nil {
			fset := token.NewFileSet()
			for _, e := range entries {
				name := e.Name()
				if e.IsDir() || !strings.HasSuffix(name, ".go") ||
					strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, ".x.go") {
					continue
				}
				f, perr := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
				if perr != nil || f == nil {
					continue
				}
				collectGoDecls(f)
			}
		}
	}
	scanChunk := func(src string) {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "", "package _gsxp\n"+src, 0)
		if f == nil && err != nil {
			return
		}
		collectGoDecls(f)
	}
	for _, file := range files {
		for _, d := range file.Decls {
			switch t := d.(type) {
			case *gsxast.Component:
				if t.Recv == "" {
					out[t.Name] = true
				}
			case *gsxast.GoChunk:
				scanChunk(t.Src)
			case *gsxast.GoWithElements:
				// Reconstruct parseable Go: element parts become `nil`
				// placeholders (offsets don't matter — names only).
				var b strings.Builder
				for _, p := range t.Parts {
					if gt, ok := p.(gsxast.GoText); ok {
						b.WriteString(gt.Src)
					} else {
						b.WriteString("nil")
					}
				}
				scanChunk(b.String())
			}
		}
	}
	return out
}
```

NOTE for implementer: verify `gsxast.GoText` is a value (not pointer) GoPart — `func (GoText) goPartNode()` at `ast/ast.go:180` says value. Adjust the type switch if needed.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/codegen -run TestPackageDeclNames -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/codegen/declnames.go internal/codegen/declnames_test.go
git commit -m "feat(codegen): packageDeclNames — syntactic package-level bare-name set"
```

---

### Task 2: `Element.IsComponent` stamp + resolve pass

**Files:**
- Modify: `ast/ast.go` (Element struct, ~line 226; IsComponentTag doc ~line 780)
- Create: `internal/codegen/tagresolve.go`
- Test: `internal/codegen/tagresolve_test.go`
- Modify: `internal/codegen/module_importer.go` (analyze, ~line 762 after the ResolveScripts loop)

**Interfaces:**
- Consumes: `packageDeclNames(dir, files) map[string]bool` (Task 1); `diag.Bag` (`internal/diag/diag.go:50`) — not used for reports yet (Tasks 4/6 add reports), pass it now so signatures don't churn.
- Produces: `Element.IsComponent bool` stamped on EVERY element in every file the analyze path touches; `func resolveComponentTags(file *gsxast.File, declNames map[string]bool, bag *diag.Bag)`. Tasks 3–8 consume the field.

- [ ] **Step 1: Add the AST field**

In `ast/ast.go`, add to `Element` (after `AttrsMultiline`):

```go
	// IsComponent is the resolved component-vs-leaf decision for this tag:
	// true for capital-first/dotted tags, and for lowercase tags that match a
	// package-level declaration (with self-exclusion inside the declaration of
	// the same name). Set by codegen's resolveComponentTags pass — NEVER by the
	// parser — so formatter round-trip equality is unaffected. Codegen and LSP
	// read this field instead of re-deriving component-ness from the tag string.
	IsComponent bool
```

Update the `IsComponentTag` doc comment (ast.go:780-784): it is no longer the single source of truth for codegen — it remains the *syntactic* rule (capital/dotted) used as one input by `resolveComponentTags`, and the parser no longer consumes it for type-arg admission after Task 4. Rewrite:

```go
// IsComponentTag reports the SYNTACTIC component rule: dotted (ui.Button,
// p.item) or ASCII-uppercase-first tags are always components. It is one
// input to codegen's resolveComponentTags pass, which additionally resolves
// lowercase tags against the package's declared names and stamps the result
// on Element.IsComponent. Read the stamp, not this function, when deciding
// how a specific element lowers.
```

- [ ] **Step 2: Write the failing test**

```go
package codegen

import (
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
	"go/token"
)

// collectStamps walks the file and returns tag -> IsComponent for every element.
// Duplicate tags append "#2", "#3", ... in walk order so tests can address them.
func collectStamps(f *gsxast.File) map[string]bool {
	out := map[string]bool{}
	var walk func(nodes []gsxast.Markup)
	record := func(el *gsxast.Element) {
		key := el.Tag
		for i := 2; ; i++ {
			if _, dup := out[key]; !dup {
				break
			}
			key = el.Tag + "#" + string(rune('0'+i))
		}
		out[key] = el.IsComponent
	}
	walk = func(nodes []gsxast.Markup) {
		forEachElement(nodes, func(el *gsxast.Element) { record(el) })
	}
	for _, d := range f.Decls {
		switch t := d.(type) {
		case *gsxast.Component:
			walk(t.Body)
		case *gsxast.GoWithElements:
			for _, p := range t.Parts {
				if el, ok := p.(*gsxast.Element); ok {
					record(el)
					walk(el.Children)
				}
			}
		}
	}
	return out
}

func TestResolveComponentTags(t *testing.T) {
	f := parseGSXForTest(t, `package views

component card() {
	<div class="c">{children}</div>
}

component div() {
	<div>{children}</div>
}

component Page() {
	<card/>
	<span/>
	<my-widget/>
	<ui.Button/>
	<div/>
}
`)
	declNames := map[string]bool{"card": true, "div": true, "Page": true}
	bag := diag.NewBag(token.NewFileSet())
	resolveComponentTags(f, declNames, bag)
	got := collectStamps(f)

	want := map[string]bool{
		"div":       false, // inside component div: self-exclusion → leaf
		"div#2":     false, // wait — see ordering note below
		"card":      true,  // in Page: resolves to component
		"span":      false, // undeclared lowercase → leaf
		"my-widget": false, // dashed, never resolves
		"ui.Button": true,  // dotted, always component
	}
	// Ordering note: card's body <div> walks first (leaf: no exclusion needed,
	// "div" IS declared → true!). Assert precisely instead:
	_ = want
	// Inside card's body, <div> must be TRUE (div is a declared component,
	// card ≠ div — no exclusion). Inside div's body, <div> must be FALSE
	// (self-exclusion). Inside Page, <div> must be TRUE.
	if !got["div"] { // card's body
		t.Error("inside card: <div> should resolve to the div component")
	}
	if got["div#2"] { // div's own body
		t.Error("inside component div: <div> must be the leaf (self-exclusion)")
	}
	if !got["div#3"] { // Page's body
		t.Error("inside Page: <div> should resolve to the div component")
	}
	if !got["card"] || got["span"] || got["my-widget"] || !got["ui.Button"] {
		t.Errorf("stamps wrong: %v", got)
	}
}

func TestResolveComponentTagsMethodExclusion(t *testing.T) {
	f := parseGSXForTest(t, `package views

type page struct{}

component (p page) item() {
	<item/>
}
`)
	bag := diag.NewBag(token.NewFileSet())
	resolveComponentTags(f, map[string]bool{"item": true, "page": true}, bag)
	got := collectStamps(f)
	if got["item"] {
		t.Error("inside method item: <item> must be leaf (exclusion keyed by name, methods included)")
	}
}

func TestResolveComponentTagsGoWithElements(t *testing.T) {
	f := parseGSXForTest(t, `package views

component chip() {
	<b>x</b>
}

var chip2 = <div><chip/></div>

var chip3 = <chip3/>
`)
	bag := diag.NewBag(token.NewFileSet())
	resolveComponentTags(f, map[string]bool{"chip": true, "chip2": true, "chip3": true}, bag)
	got := collectStamps(f)
	if !got["chip"] {
		t.Error("<chip/> inside var chip2 initializer should resolve")
	}
	if got["chip3"] {
		t.Error("<chip3/> inside var chip3 must be leaf (self-exclusion in var initializer)")
	}
}
```

The test needs a general element walker `forEachElement` — Step 3 creates it in tagresolve.go (exported within the package). Model on `forEachComponentTagElement` (`analyze.go:2318`) but UNFILTERED and covering every markup construct that can contain an `*ast.Element`.

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/codegen -run TestResolveComponentTags -v`
Expected: FAIL — `undefined: resolveComponentTags` / `undefined: forEachElement`

- [ ] **Step 4: Implement `internal/codegen/tagresolve.go`**

```go
package codegen

import (
	goast "go/ast"
	goparser "go/parser"
	"go/token"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
)

// forEachElement visits every *gsxast.Element under nodes (children, markup
// attrs, fragments, control flow) in source order. Unlike
// forEachComponentTagElement it does not filter by tag kind — the resolve
// pass IS the thing that decides tag kind.
//
// COMPLETENESS: this must cover every construct that can contain an Element.
// Cross-check against the printer's markup walk (internal/printer) and
// forEachComponentTagElement + walkMarkupAttrs. If interpolation holes can
// embed element literals (ast.go's Interp — see "Interp.Embedded" in the
// GoPart doc, ast/ast.go:126), add that case too — write the completeness
// test FIRST (Step 2 pattern), one element per context.
func forEachElement(nodes []gsxast.Markup, fn func(*gsxast.Element)) {
	for _, n := range nodes {
		switch t := n.(type) {
		case *gsxast.Element:
			fn(t)
			walkMarkupAttrs(t.Attrs, func(value []gsxast.Markup) {
				forEachElement(value, fn)
			})
			forEachElement(t.Children, fn)
		case *gsxast.Fragment:
			forEachElement(t.Children, fn)
		case *gsxast.ForMarkup:
			forEachElement(t.Body, fn)
		case *gsxast.IfMarkup:
			forEachElement(t.Then, fn)
			forEachElement(t.Else, fn)
		case *gsxast.SwitchMarkup:
			for _, cc := range t.Cases {
				forEachElement(cc.Body, fn)
			}
			// + Interp embedded parts if the AST supports them (see doc above).
		}
	}
}

// resolveTag applies the resolution rule from the 2026-07-10 spec:
// capital/dotted → component; lowercase Go identifier → component iff a
// package-level declaration with that name exists AND the tag is not the
// enclosing declaration's own name (self-exclusion, wrapper pattern);
// everything else (dashes, unknown names) → leaf.
func resolveTag(tag string, declNames map[string]bool, exclude string) bool {
	if gsxast.IsComponentTag(tag) {
		return true
	}
	if !token.IsIdentifier(tag) {
		return false // <my-widget> etc. can never name a Go declaration
	}
	return tag != exclude && declNames[tag]
}

// resolveComponentTags stamps Element.IsComponent on every element in file.
// exclude for a Component body is the component's bare name (methods included
// — exclusion is keyed by name); for a GoWithElements, each element's
// enclosing top-level declaration name.
func resolveComponentTags(file *gsxast.File, declNames map[string]bool, bag *diag.Bag) {
	stampAll := func(nodes []gsxast.Markup, exclude string) {
		forEachElement(nodes, func(el *gsxast.Element) {
			el.IsComponent = resolveTag(el.Tag, declNames, exclude)
		})
	}
	for _, d := range file.Decls {
		switch t := d.(type) {
		case *gsxast.Component:
			stampAll(t.Body, t.Name)
		case *gsxast.GoWithElements:
			excludes := goWithElementsExcludes(t)
			for i, p := range t.Parts {
				el, ok := p.(*gsxast.Element)
				if !ok {
					continue
				}
				exclude := excludes[i]
				el.IsComponent = resolveTag(el.Tag, declNames, exclude)
				walkMarkupAttrs(el.Attrs, func(value []gsxast.Markup) {
					stampAll(value, exclude)
				})
				stampAll(el.Children, exclude)
			}
		}
	}
}

// goWithElementsExcludes maps each part index of g to the name of the
// top-level Go declaration enclosing it, by re-parsing the reconstructed
// source with `nil` placeholders for element parts and matching part byte
// offsets against declaration spans. Element parts outside any declaration
// (unlikely) get "".
func goWithElementsExcludes(g *gsxast.GoWithElements) map[int]string {
	out := map[int]string{}
	const header = "package _gsxp\n"
	var b strings.Builder
	b.WriteString(header)
	partOff := map[int]int{} // part index -> byte offset in reconstructed src
	for i, p := range g.Parts {
		partOff[i] = b.Len()
		if gt, ok := p.(gsxast.GoText); ok {
			b.WriteString(gt.Src)
		} else {
			b.WriteString("nil") // placeholder occupying the element's slot
		}
	}
	fset := token.NewFileSet()
	f, err := goparser.ParseFile(fset, "", b.String(), 0)
	if f == nil && err != nil {
		return out
	}
	declName := func(d goast.Decl) (string, bool) {
		switch dd := d.(type) {
		case *goast.FuncDecl:
			if dd.Recv == nil && dd.Name != nil {
				return dd.Name.Name, true
			}
			if dd.Name != nil { // method: exclusion keyed by name per spec
				return dd.Name.Name, true
			}
		case *goast.GenDecl:
			// var x = <el/>: one spec's first name is the exclusion. Multi-name
			// specs (var a, b = ..., ...) use the first name — good enough for
			// a diagnostic-grade exclusion; document if it ever matters.
			for _, spec := range dd.Specs {
				if vs, ok := spec.(*goast.ValueSpec); ok && len(vs.Names) > 0 {
					return vs.Names[0].Name, true
				}
			}
		}
		return "", false
	}
	tf := fset.File(f.Pos())
	for i := range g.Parts {
		if _, isEl := g.Parts[i].(*gsxast.Element); !isEl {
			continue
		}
		pos := tf.Pos(partOff[i])
		for _, d := range f.Decls {
			if d.Pos() <= pos && pos < d.End() {
				if name, ok := declName(d); ok {
					out[i] = name
				}
				break
			}
		}
	}
	return out
}
```

- [ ] **Step 5: Write the completeness test** (one element per containing construct — body, children, markup-attr value, fragment, if/for/switch, GoWithElements part; add interp-hole if the AST supports it):

```go
func TestForEachElementCompleteness(t *testing.T) {
	f := parseGSXForTest(t, `package views

component Page(items []string, on bool) {
	<a1/>
	<div><a2/></div>
	<Card slot={<a3/>}/>
	<>
		<a4/>
	</>
	if on {
		<a5/>
	} else {
		<a6/>
	}
	for _, it := range items {
		<a7>{it}</a7>
	}
	switch {
	case on:
		<a8/>
	}
}
`)
	seen := map[string]bool{}
	for _, d := range f.Decls {
		if c, ok := d.(*gsxast.Component); ok {
			forEachElement(c.Body, func(el *gsxast.Element) { seen[el.Tag] = true })
		}
	}
	for _, tag := range []string{"a1", "a2", "a3", "a4", "a5", "a6", "a7", "a8", "div", "Card"} {
		if !seen[tag] {
			t.Errorf("forEachElement missed <%s>", tag)
		}
	}
}
```

(Adjust the `slot={<a3/>}` markup-attr spelling to the corpus' canonical form — see `internal/corpus/testdata/cases/components/` for a markup-attr example.)

- [ ] **Step 6: Wire into analyze**

In `internal/codegen/module_importer.go`, in `func (m *Module) analyze` — after the `scriptErr` block (`gsxFiles = nil` short-circuit, ~line 763) and before `componentPropFieldsFor`:

```go
	// Resolve component-vs-leaf for every tag BEFORE any skeleton/probe/emit
	// walk consults it (analyze.go's emitProbes reads the stamp). Lowercase
	// tags resolve against the package's declared names; see tagresolve.go
	// and docs/superpowers/specs/2026-07-10-lowercase-tag-symbol-resolution-design.md.
	declNames := packageDeclNames(dir, gsxFiles)
	for _, f := range gsxFiles {
		resolveComponentTags(f, declNames, bag)
	}
```

- [ ] **Step 7: Run tests**

Run: `go test ./internal/codegen -run 'TestResolveComponentTags|TestForEachElement' -v && go test ./internal/gsxfmt && go build ./...`
Expected: PASS (fmt corpus untouched — parser never sets the field)

- [ ] **Step 8: Commit**

```bash
git add ast/ast.go internal/codegen/tagresolve.go internal/codegen/tagresolve_test.go internal/codegen/module_importer.go
git commit -m "feat(codegen): resolveComponentTags stamps Element.IsComponent (lowercase decl resolution + self-exclusion)"
```

---

### Task 3: Flip the 11 codegen call sites; behavior lands; core corpus cases

**Files:**
- Modify: `internal/codegen/attrsonly.go:44`, `internal/codegen/analyze.go:301,1312,2322,2778,3249,3290`, `internal/codegen/emit.go:1741,2295,3715,3618`
- Create: `internal/corpus/testdata/cases/lowertags/*.txtar` (6 cases below)
- Modify (regen): `internal/corpus/testdata/coverage.golden`

**Interfaces:**
- Consumes: `Element.IsComponent` (Task 2).
- Produces: lowercase resolution IS live end-to-end. LSP flip is Task 8; parser type-args Task 4.

- [ ] **Step 1: Flip each call site** (mechanical; line numbers may drift — match on the shown code):

| Site | Old | New |
|---|---|---|
| `attrsonly.go:44` | `if !isComponentTag(el.Tag) \|\| el.TypeArgs != ""` | `if !el.IsComponent \|\| el.TypeArgs != ""` |
| `analyze.go:301` | `if !isComponentTag(el.Tag) \|\| strings.Contains(el.Tag, ".")` | `if !el.IsComponent \|\| strings.Contains(el.Tag, ".")` |
| `analyze.go:1312` | `if isComponentTag(t.Tag) {` | `if t.IsComponent {` |
| `analyze.go:2322` (`forEachComponentTagElement`) | `if isComponentTag(t.Tag) {` | `if t.IsComponent {` |
| `analyze.go:2778` (`collectExprs`) | `if isComponentTag(t.Tag) {` | `if t.IsComponent {` |
| `analyze.go:3249` (`collectAttrExprSrc`) | `if isComponentTag(t.Tag) {` | `if t.IsComponent {` |
| `analyze.go:3290` (`collectChildPropExprSrc`) | `if isComponentTag(t.Tag) {` | `if t.IsComponent {` |
| `emit.go:1741` (`genNode`) | `if isComponentTag(t.Tag) {` | `if t.IsComponent {` |
| `emit.go:2295` (`scopeUsesNumeric`) | component check on `t.Tag` | `t.IsComponent` |
| `emit.go:3715` (`usesAttrs`) | component check on `el.Tag` | `el.IsComponent` |

Then delete the now-unused delegator `func isComponentTag(tag string) bool` at `emit.go:3618` **if** `gopls check`/`go vet` confirms zero remaining references (some sites in this table were found by an earlier grep at different line numbers — sweep the whole package: `grep -rn "isComponentTag" internal/codegen/ | grep -v _test`).

- [ ] **Step 2: Verify existing behavior unchanged**

Run: `go test ./internal/corpus -run TestCorpus -count=1 && go test ./internal/codegen -count=1`
Expected: PASS with ZERO golden changes. If any existing case fails, it declares a lowercase symbol colliding with a lowercase tag in use — that's the new rule firing. Inspect each individually; per spec (pre-release, rule replaces old one) update the case deliberately and note it in the commit message. Do NOT blanket `-update` before understanding each diff.

- [ ] **Step 3: Add core corpus cases** — create `internal/corpus/testdata/cases/lowertags/` with these six files:

`resolves_func.txtar`:
```
# Lowercase tag resolves to a same-package component; undeclared lowercase
# stays a leaf. The resolution rule per the 2026-07-10 spec.
-- input.gsx --
package views

component card() {
	<div class="card">{children}</div>
}

component Page() {
	<card>hi</card>
	<span>bye</span>
}
-- invoke --
Page()
-- diagnostics.golden --
-- generated.x.go.golden --
-- render.golden --
<div class="card">hi</div><span>bye</span>
```
(render.golden content here is indicative — always author the case, run `-update`, then READ the regenerated golden to confirm it matches the spec'd semantics before committing.)

`resolves_crossfile.txtar`:
```
# Resolution sees declarations from a sibling .gsx AND a sibling .go of the
# same package.
-- badge.gsx --
package views

component badge() {
	<em>B</em>
}
-- helpers.go --
package views
-- input.gsx --
package views

component Page() {
	<badge/>
}
-- invoke --
Page()
-- diagnostics.golden --
-- render.golden --
<em>B</em>
```

`wrapper_self_exclusion.txtar`:
```
# THE wrapper pattern: inside component div's own body, <div> is the leaf
# element (self-exclusion) — no infinite recursion, no extra syntax.
-- input.gsx --
package views

component div() {
	<div class="wrapped">{children}</div>
}

component Page() {
	<div>hi</div>
}
-- invoke --
Page()
-- diagnostics.golden --
-- generated.x.go.golden --
-- render.golden --
<div class="wrapped">hi</div>
```

`wrapper_composition.txtar`:
```
# Inside one wrapper, ANOTHER wrapped element name still resolves to its
# component (exclusion covers exactly one name); each bottoms out at its leaf.
-- input.gsx --
package views

component div() {
	<div class="d"><span>{children}</span></div>
}

component span() {
	<span class="s">{children}</span>
}

component Page() {
	<div>hi</div>
}
-- invoke --
Page()
-- diagnostics.golden --
-- render.golden --
<div class="d"><span class="s">hi</span></div>
```

`leaf_fallback.txtar`:
```
# Undeclared lowercase and dashed custom-element tags render as-is.
-- input.gsx --
package views

component Page() {
	<article>a</article>
	<my-widget attr="1">w</my-widget>
}
-- invoke --
Page()
-- diagnostics.golden --
-- render.golden --
<article>a</article><my-widget attr="1">w</my-widget>
```
(Drop the stray line — final case body is just the two leaf elements.)

`noncapture_and_error.txtar`:
```
# (1) An import name must NOT capture a tag (<time> stays a leaf despite
# `import "time"` in a sibling). (2) A _test.go-only declaration must not
# capture (<gadget> stays a leaf). (3) A non-invocable capture (var data int
# + <data>) IS a component invocation and fails loudly in the type-checked
# skeleton — go build is the arbiter, no silent fallback.
-- model.go --
package views

import "time"

var _ = time.Now
var data int
-- helpers_test.go --
package views

func gadget() {}
-- input.gsx --
package views

component Page() {
	<time>t</time>
	<gadget>g</gadget>
	<data>d</data>
}
-- invoke --
Page()
-- diagnostics.golden --
(author with -update: expect a type error naming `data`, e.g. "cannot call non-function data"; neither <time> nor <gadget> may appear in any diagnostic)
-- render.golden --
```

`resolves_var_value.txtar`:
```
# A lowercase tag resolves to a package-level VAR holding a component value
# (element-literal var), not just funcs — "any decl counts". Model the var
# syntax on an existing element-literal corpus case (grep cases/ for a
# top-level `var x = <`); the attrs-only invocation path handles the call.
-- input.gsx --
package views

var chip = <em class="chip">c</em>

component Page() {
	<chip/>
}
-- invoke --
Page()
-- diagnostics.golden --
-- render.golden --
(author with -update; expect the em element's HTML. If element-literal vars
turn out not to be invocable as components today, pin the actual behavior —
loud build error — and note it; do NOT silently drop the case.)
```

- [ ] **Step 4: Regenerate goldens, then verify**

Run: `go test ./internal/corpus -run 'TestCorpus/lowertags' -update && go test ./internal/corpus -run TestCorpus -count=1`
Expected: PASS; READ every regenerated golden and confirm it matches the spec semantics (especially `wrapper_self_exclusion` — the generated `.x.go` must show the inner `<div>` as HTML bytes, not a call). `coverage.golden` is rewritten by `-update`; commit it.

- [ ] **Step 5: Commit**

```bash
git add internal/codegen internal/corpus/testdata
git commit -m "feat(codegen): lowercase tags resolve to package symbols — flip call sites, corpus cases"
```

---

### Task 4: Parser type-arg loosening + leaf-typearg error

**Files:**
- Modify: `parser/markup.go:741` (drop the gate), `parser/markup.go:806` (delete `canHaveTypeArgs`)
- Modify: `parser/markup_test.go:142` (`TestRejectHTMLElementTypeArgs`)
- Modify: `internal/codegen/tagresolve.go` (emit the error)
- Test: corpus case `lowertags/typeargs_on_leaf.txtar`

**Interfaces:**
- Consumes: `resolveComponentTags` (Task 2) as the natural place to report — it is the first pass that KNOWS a tag is a leaf.
- Produces: parse-anything/`codegen`-rejects semantics for `<tag[T]>`.

- [ ] **Step 1: Update the parser test** — rewrite `TestRejectHTMLElementTypeArgs` (markup_test.go:142) to assert `<div[int]/>` now PARSES with `TypeArgs == "int"` (rename to `TestHTMLElementTypeArgsParse`). Run: `go test ./parser -run TypeArgs -v` → FAIL (still rejected).

- [ ] **Step 2: Remove the gate** in `parser/markup.go` (~741):

```go
	if p.peek() == '[' {
		end, ok := bracketEnd(p.src, p.i)
		...
```
(delete the `if !canHaveTypeArgs(tag) { return nil, p.errorf(...) }` block and the `canHaveTypeArgs` func at :806). Run: `go test ./parser -count=1` → PASS.

- [ ] **Step 3: Report the codegen error.** In `resolveComponentTags`'s element stamping (both Component-body and GoWithElements paths — put it inside the shared closure):

```go
		el.IsComponent = resolveTag(el.Tag, declNames, exclude)
		if !el.IsComponent && el.TypeArgs != "" {
			bag.Errorf(el.Pos(), el.End(), "type-args-on-element",
				"type arguments on HTML element <%s>: type args are only valid on component tags", el.Tag)
		}
```
(Check `Bag.Errorf` signature at `internal/diag/diag.go:84`: `(pos, end token.Pos, code, format string, args ...any)`.)

- [ ] **Step 4: Corpus case** `lowertags/typeargs_on_leaf.txtar`:

```
# Type args on a tag that resolves to a leaf are a codegen error (the parser
# now admits [..] on any tag; resolution decides).
-- input.gsx --
package views

component Page() {
	<list[int]>x</list[int]>
}
-- invoke --
Page()
-- diagnostics.golden --
(author with -update; expect: type arguments on HTML element <list>)
-- render.golden --
```
Also add a positive twin in the same file's spirit: a `lowertags/typeargs_on_resolved.txtar` where `component list[T any](v T)` is declared and `<list[int] v={1}/>` invokes it (verify generics + lowercase compose; model the tag syntax on an existing `xpkg/generic_*` case).

- [ ] **Step 5: Regen + full check + commit**

```bash
go test ./internal/corpus -run 'TestCorpus/lowertags' -update
go test ./internal/corpus -run TestCorpus -count=1 && go test ./parser ./internal/codegen -count=1
git add parser internal/codegen internal/corpus/testdata
git commit -m "feat(parser,codegen): admit type args on any tag; leaf+typeargs is a codegen error"
```

---

### Task 5: Corpus harness — warnings must not suppress gen/render

The harness currently treats ANY diagnostic as an error case: `internal/corpus/batch.go:175-181` sets `hasErr = true` on `len(pr.Diags) > 0`, skipping generated/render collection — but production `Module.Generate` gates only on type errors/signature conflicts and still emits alongside warnings. Tasks 6–7 add warning cases that must pin render output too, so fix the harness to mirror production.

**Files:**
- Modify: `internal/corpus/batch.go:175-181` (`hasErr` condition), `internal/corpus/batch.go:400-407` (`formatDiagLine`)

- [ ] **Step 1:** Change the `hasErr` trigger from `len(pr.Diags) > 0` to "any diagnostic with `Severity == diag.Error`":

```go
	hasErr := false
	for _, d := range pr.Diags {
		if d.Severity == diag.Error {
			hasErr = true
			break
		}
	}
```
(Adapt to the surrounding code's actual shape at batch.go:175-181.)

- [ ] **Step 2:** Make severity visible in goldens — in `formatDiagLine`, prefix non-error severities:

```go
	sev := ""
	if d.Severity != diag.Error {
		sev = d.Severity.String() + ": " // "warning: ", "info: ", "hint: "
	}
```
prepended to the message in both the positioned and positionless branches.

- [ ] **Step 3:** Verify zero existing golden changes (no warning-emitting corpus case exists today):

Run: `go test ./internal/corpus -run TestCorpus -count=1`
Expected: PASS untouched.

- [ ] **Step 4: Commit**

```bash
git add internal/corpus/batch.go
git commit -m "fix(corpus): warnings no longer suppress gen/render facets; severity prefix in diagnostics goldens"
```

---

### Task 6: Self-reference warning (WHATWG-table diagnostic)

**Files:**
- Create: `internal/codegen/htmlnames.go` (spec element-name table — DIAGNOSTIC-ONLY per spec)
- Modify: `internal/codegen/tagresolve.go`
- Test: `internal/codegen/htmlnames_test.go`, corpus `lowertags/selfref_warning.txtar`

- [ ] **Step 1:** `internal/codegen/htmlnames.go` — the WHATWG HTML living-standard element list (all ~115 current elements: `a` through `wbr`, including `math` and `svg` roots), as `var htmlElementNames = map[string]bool{...}`. Doc comment MUST state: *"Used ONLY by the self-reference diagnostic in resolveComponentTags — never by resolution itself (see the 2026-07-10 spec: no reserved table in resolution)."* Source the list from the WHATWG spec index; include a small unit test spot-checking `div`, `slot`, `search`, `template` present and `item`, `card` absent.

- [ ] **Step 2:** Emit the warning where self-exclusion fires. In `resolveComponentTags`, exclusion currently happens silently inside `resolveTag`; restructure so the caller can observe it:

```go
		excluded := el.Tag == exclude && token.IsIdentifier(el.Tag) &&
			!gsxast.IsComponentTag(el.Tag) && declNames[el.Tag]
		el.IsComponent = !excluded && resolveTag(el.Tag, declNames, exclude)
		if excluded && !htmlElementNames[el.Tag] {
			bag.Report(el.Pos(), el.End(), diag.Warning, "self-reference-leaf", "codegen",
				"<%s> inside the declaration of %q renders as a leaf element; for recursion call %s(...) in a Go hole",
				el.Tag, exclude, el.Tag)
		}
```

- [ ] **Step 3:** Corpus case `lowertags/selfref_warning.txtar` — non-HTML self-named tag warns AND still renders (Task 5 made that possible):

```
# A self-named tag that is NOT a real HTML element almost certainly intended
# recursion — warn, render as leaf. A self-named tag that IS an HTML element
# (the wrapper pattern) must NOT warn (wrapper_self_exclusion.txtar).
-- input.gsx --
package views

component item() {
	<item>x</item>
}

component Page() {
	<item/>
}
-- invoke --
Page()
-- diagnostics.golden --
(author with -update; expect: warning: <item> inside the declaration of "item" ... at the inner element's position)
-- render.golden --
<item>x</item>
```

- [ ] **Step 4:** Regen + verify wrapper_self_exclusion still has EMPTY diagnostics (div IS in the table — no warning). Run full corpus. Commit:

```bash
go test ./internal/corpus -run 'TestCorpus/lowertags' -update && go test ./internal/corpus -run TestCorpus -count=1
git add internal/codegen internal/corpus/testdata
git commit -m "feat(codegen): self-reference-leaf warning for non-HTML self-named tags"
```

---

### Task 7: Wrapper-cycle warning

**Files:**
- Create: `internal/codegen/tagcycle.go`
- Test: `internal/codegen/tagcycle_test.go`, corpus `lowertags/cycle_warning.txtar` + `lowertags/cycle_conditional_ok.txtar`

**Interfaces:**
- Consumes: stamped files + `declNames`; called from `analyze` right after the resolve loop (Task 2's insertion point).
- Produces: `func reportWrapperCycles(files map[string]*gsxast.File, bag *diag.Bag)` — warning per unconditional cycle.

Semantics (spec): nodes = lowercase-invocable component declarations (bare name, `Recv == ""`); edge A→B when A's body contains an element with `IsComponent == true` whose tag is a lowercase simple name of another node, and the element is NOT nested under `IfMarkup`/`ForMarkup`/`SwitchMarkup` (a `for` can run zero times — conditional) — CondAttr does not gate children, ignore it. A cycle with any conditional edge is not reported. Report each cycle once, at the position of the edge element that closes it, message: `wrapper cycle div → span → div will recurse infinitely at render`.

- [ ] **Step 1: Unit test first** (build files via `parseGSXForTest`, run resolve + `reportWrapperCycles`, assert on `bag.Sorted()`):

```go
func TestWrapperCycleWarning(t *testing.T) {
	f := parseGSXForTest(t, `package views

component div() {
	<div><span>{children}</span></div>
}

component span() {
	<span><div>{children}</div></span>
}
`)
	bag := diag.NewBag(token.NewFileSet())
	declNames := map[string]bool{"div": true, "span": true}
	resolveComponentTags(f, declNames, bag)
	reportWrapperCycles(map[string]*gsxast.File{"a.gsx": f}, bag)
	var warns []diag.Diagnostic
	for _, d := range bag.Sorted() {
		if d.Severity == diag.Warning && d.Code == "wrapper-cycle" {
			warns = append(warns, d)
		}
	}
	if len(warns) != 1 {
		t.Fatalf("want exactly 1 wrapper-cycle warning, got %d: %v", len(warns), warns)
	}
	if !strings.Contains(warns[0].Message, "div") || !strings.Contains(warns[0].Message, "span") {
		t.Errorf("cycle message should name both components: %s", warns[0].Message)
	}
}

func TestWrapperCycleConditionalEdgeExempt(t *testing.T) {
	f := parseGSXForTest(t, `package views

component div(deep bool) {
	if deep {
		<span>{children}</span>
	}
	<div>{children}</div>
}

component span() {
	<span><div>{children}</div></span>
}
`)
	bag := diag.NewBag(token.NewFileSet())
	declNames := map[string]bool{"div": true, "span": true}
	resolveComponentTags(f, declNames, bag)
	reportWrapperCycles(map[string]*gsxast.File{"a.gsx": f}, bag)
	for _, d := range bag.Sorted() {
		if d.Code == "wrapper-cycle" {
			t.Fatalf("conditional edge must exempt the cycle: %v", d)
		}
	}
}
```

- [ ] **Step 2:** Implement `internal/codegen/tagcycle.go`:

```go
package codegen

import (
	"go/token"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
)

type cycleEdge struct {
	to string
	el *gsxast.Element
}

// collectUnconditionalEdges gathers, per receiver-less component, the
// component-resolved lowercase simple tags in its body NOT nested under
// if/for/switch markup (those legitimately break recursion).
func collectUnconditionalEdges(c *gsxast.Component, nodes map[string]bool) []cycleEdge {
	var out []cycleEdge
	var walk func(ms []gsxast.Markup)
	walk = func(ms []gsxast.Markup) {
		for _, n := range ms {
			switch t := n.(type) {
			case *gsxast.Element:
				if t.IsComponent && !strings.Contains(t.Tag, ".") &&
					!gsxast.IsComponentTag(t.Tag) && nodes[t.Tag] {
					out = append(out, cycleEdge{to: t.Tag, el: t})
				}
				walkMarkupAttrs(t.Attrs, func(v []gsxast.Markup) { walk(v) })
				walk(t.Children)
			case *gsxast.Fragment:
				walk(t.Children)
				// IfMarkup / ForMarkup / SwitchMarkup: conditional — do not descend.
			}
		}
	}
	walk(c.Body)
	return out
}

// reportWrapperCycles warns on every cycle in the unconditional
// component→component tag graph. Self-loops are impossible (self-exclusion
// stamps them leaf); mutual wrapper cycles compile clean and die at render
// with a stack overflow, hence the warning. See the 2026-07-10 spec.
func reportWrapperCycles(files map[string]*gsxast.File, bag *diag.Bag) {
	nodes := map[string]bool{}
	for _, f := range files {
		for _, d := range f.Decls {
			if c, ok := d.(*gsxast.Component); ok && c.Recv == "" {
				nodes[c.Name] = true
			}
		}
	}
	edges := map[string][]cycleEdge{}
	for _, f := range files {
		for _, d := range f.Decls {
			if c, ok := d.(*gsxast.Component); ok && c.Recv == "" {
				edges[c.Name] = append(edges[c.Name], collectUnconditionalEdges(c, nodes)...)
			}
		}
	}
	// DFS with 3-color marking; report the edge that closes each cycle once.
	const (
		white = 0
		grey  = 1
		black = 2
	)
	color := map[string]int{}
	var stack []string
	reported := map[string]bool{}
	var visit func(n string)
	visit = func(n string) {
		color[n] = grey
		stack = append(stack, n)
		for _, e := range edges[n] {
			switch color[e.to] {
			case white:
				visit(e.to)
			case grey:
				// Found a cycle: slice stack from e.to's position.
				i := len(stack) - 1
				for i >= 0 && stack[i] != e.to {
					i--
				}
				path := append(append([]string{}, stack[i:]...), e.to)
				key := strings.Join(path, "→")
				if !reported[key] {
					reported[key] = true
					bag.Report(e.el.Pos(), e.el.End(), diag.Warning, "wrapper-cycle", "codegen",
						"wrapper cycle %s will recurse infinitely at render", strings.Join(path, " → "))
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[n] = black
	}
	for n := range nodes {
		if color[n] == white {
			visit(n)
		}
	}
}
```
NOTE: iteration over `nodes`/`files` maps is nondeterministic — the same cycle can be reported from different entry points across runs, producing different `key`s (`a→b→a` vs `b→a→b`). Canonicalize: rotate `path` so it starts at the lexicographically smallest name before building `key` and the message. Add this to the unit test (run detection twice, expect identical output) — corpus goldens REQUIRE determinism.

- [ ] **Step 3:** Call it in `analyze` after the resolve loop (Task 2 insertion point):

```go
	reportWrapperCycles(gsxFiles, bag)
```

- [ ] **Step 4:** Corpus cases:

`lowertags/cycle_warning.txtar` — the mutual wrapper pair from the unit test, `invoke` on a third `Page()` component that uses NEITHER (rendering must not recurse: pin render + the warning in diagnostics.golden).
`lowertags/cycle_conditional_ok.txtar` — the conditional variant, EMPTY diagnostics.golden, pinned render.

- [ ] **Step 5:** Regen, full test, commit:

```bash
go test ./internal/codegen -run TestWrapperCycle -v
go test ./internal/corpus -run 'TestCorpus/lowertags' -update && go test ./internal/corpus -run TestCorpus -count=1
git add internal/codegen internal/corpus/testdata
git commit -m "feat(codegen): wrapper-cycle warning on unconditional lowercase component cycles"
```

---

### Task 8: LSP parity

**Files:**
- Modify: `internal/lsp/definition_attr.go:123`, `internal/lsp/definition_attrsonly.go:156`, `internal/lsp/hover.go:197`
- Test: extend existing hover/definition tests (find them: `grep -rn "func TestHover\|func TestDefinition" internal/lsp/*_test.go`)

**Interfaces:**
- Consumes: `Package.Files` — these ASTs come from `Module.analyze` (`gen/lsp.go` → `codegen.Module.Package`), so they carry the stamps already.
- Produces: hover + go-to-definition + attr-definition work on lowercase component tags.

- [ ] **Step 1:** Flip the three element-gated sites from `isComponentTag(el.Tag)` to `el.IsComponent`. Keep `isSimpleComponentTag`/`splitDottedTag` for their other callers (cross-package resolution etc. — sweep `grep -rn "isComponentTag\|isSimpleComponentTag" internal/lsp/`); any remaining caller that gates an ELEMENT should read the stamp; string-based helpers stay only where no element is in hand.

- [ ] **Step 2:** Verify tag→declaration resolution finds lowercase components: `hover.go`'s `componentAtTag` ends in `resolveTagComponent(pkg, tag)` — read it and confirm it matches by component NAME (case-agnostic logic); fix if it assumes exported names.

- [ ] **Step 3:** Add tests mirroring an existing hover/gd test but with `component card()` + `<card/>`: hover on `<card` yields the component signature; gd jumps to the declaration; gd on an attr name of `<card title="x"/>` jumps to the `title` param. Also a NEGATIVE: `<span/>` (undeclared) hovers as plain element (no component hover).

- [ ] **Step 4:** Run + commit:

```bash
go test ./internal/lsp -count=1
git add internal/lsp
git commit -m "feat(lsp): hover/definition on lowercase component tags (read the IsComponent stamp)"
```

---

### Task 9: Sweep, docs, ROADMAP, perf gate, CI

**Files:**
- Modify: `docs/guide/**` (syntax reference: the components/capitalization section — locate with `grep -rn "uppercase\|capital" docs/guide/`), `docs/ROADMAP.md`
- No sibling-repo changes (surface syntax unchanged; highlighting note only)

- [ ] **Step 1: In-repo collision sweep.** `make check` builds examples + playground; additionally grep for lowercase tags colliding with decls: run `go run ./cmd/gsx generate ./examples/... && go build ./...` in each example module per make targets. Any new diagnostics = collisions; fix by renaming per spec.

- [ ] **Step 2: Docs.** Update the guide's component-naming section: the resolution rule (3 bullets from spec §"The rule"), the wrapper pattern example, self-exclusion, the recursion caveat + cycle warning. Wrap any literal `{{ }}` in `::: v-pre`. Note: static highlighters show lowercase components as elements; the LSP corrects in-editor.

- [ ] **Step 3: ROADMAP.** Add two follow-ups: (a) param-qualifier dotted method tags mislower (`component List(p page) { <p.Item/> }` → `p.ItemProps is not a type`) — pre-existing, probe-found; (b) LSP semantic tokens for lowercase component tags.

- [ ] **Step 4: Perf gate (spec risk gate — REQUIRED).** Measure before/after on main vs branch:

```bash
# in each checkout:
go build -o /tmp/gsx-bench ./cmd/gsx
time /tmp/gsx-bench generate ./examples/<largest-example>   # 3 runs, note median
go test ./internal/corpus -run TestCorpus -count=1          # note wall time
```
Expected: within noise (the added work is one `go/parser` pass over package `.go` files — the same files `dirSourceHash` already reads — plus an AST walk). If generation wall time regresses measurably (>2-3%), STOP: profile, report to the user per the spec's abort criteria.

- [ ] **Step 5: Full CI + fmt corpus untouched check**

```bash
make ci
git status --short internal/gsxfmt/testdata   # must be empty
```

- [ ] **Step 6: Commit**

```bash
git add docs
git commit -m "docs: lowercase-tag resolution guide section + ROADMAP follow-ups"
```

---

## Final verification (before merge)

Per repo convention: one **independent adversarial reviewer** with live probe programs (not just diff reading) before merging — probe at minimum: wrapper pattern end-to-end render, mutual-cycle stack behavior, `_test.go`/import non-capture, a package where a `.go` decl is ADDED while `gsx generate --watch` runs (stamp updates on regen), and `gsx fmt` idempotence on a file using lowercase component tags.
