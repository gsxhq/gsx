# Module Symbol Graph Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Find-references and go-to-definition work for every Go symbol authored in `.gsx` (and every component), from `.gsx` or `.go` cursors, across the whole module — replacing the component-only `CrossIndex`/`NavIndex` with one symbol graph built on `sourceintel`.

**Architecture:** The per-package `sourceintel.Index` (already exact and complete for `.gsx` skeleton files) is widened to hand-written `.go` siblings (identity-mapped) and to gsx-only edges (`<Tag/>`, `attr=`, `|> pipe`, variant declarations) as extra occurrences; objects get a stable `ObjectKey` (`objectpath`, name fallback, per-package local ordinal); a `SymbolGraph` merges every package's index plus reverse-dependency Go-only packages (type-checked through the existing module importer with a `types.Info`). The LSP references and `.go`-definition handlers read the graph; `CrossRef`/`NavRef` and their builders are deleted.

**Tech Stack:** Go 1.26.1, `go/types`, `golang.org/x/tools/go/types/objectpath` (tooling only — root package stays stdlib-only), existing `internal/sourceintel`, `internal/codegen`, `internal/lsp`, `gen`.

**Spec:** `docs/superpowers/specs/2026-08-17-module-symbol-graph-design.md`

## Global Constraints

- Root `gsx` package must remain standard-library only; `objectpath` is used only under `internal/sourceintel` (tooling).
- Never call `packages.Load` on a new path; reverse-dep analysis reuses retained syntax + `moduleImporter`.
- Test cost unit = one `codegen.Module` open (~0.3 s forever). Replace deleted Module-opening tests with the new ones one-for-one; extend existing fixtures otherwise.
- Only exact `SourceMap` spans enter the index/graph; nothing approximate.
- Inner loop: run the single affected test (`-run`, `-count=1`); `make ci` once at the end.
- Worktree: all work in `/Users/jackieli/personal/gsxhq/gsx/.claude/worktrees/module-symbol-graph` on branch `worktree-module-symbol-graph`. Every subagent must `cd` there and verify `git branch --show-current` prints `worktree-module-symbol-graph` before editing.
- If an existing hover/definition/completion test changes behavior after a task, STOP and report — do not update expectations to make it pass.
- Commit after every task with the trailer `Claude-Session: https://claude.ai/code/session_0196BSkNoZpBvwveFrbVEvMP`.

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/sourceintel/identity.go` (new) | `IdentitySourceMap` for hand-written `.go` files |
| `internal/sourceintel/index.go` (modify) | `BuildIndexWith(info, files, BuildOptions)`; canonical-object mapping applied on harvest and in `Definition`; `forEachOccurrence` for the graph |
| `internal/sourceintel/key.go` (new) | `ObjectKey`, `Keyer` |
| `internal/sourceintel/graph.go` (new) | `SymbolGraph`: merged, keyed, per-file occurrence tables |
| `internal/pipeshape/pipeshape.go` (new) | `Stages(node)`, `Walk(skel, n)` — moved from `internal/lsp/pipe.go` |
| `internal/codegen/symbol_extras.go` (new) | gsx-only occurrences + component canonicalizer for one package |
| `internal/codegen/module_importer.go` (modify) | companion `.go` MappedFiles; index built after positional planning with extras |
| `internal/codegen/go_package_index.go` (new) | reverse-dep Go-only package analysis with `types.Info`, identity index, cache |
| `internal/codegen/module.go` (modify) | `Module.SymbolGraph(gsxDirs)`; drop `CrossIndex`/`NavIndex` |
| `internal/codegen/crossnav.go` (delete) | — |
| `gen/lsp.go` (modify) | `AnalyzeModule` returns `*sourceintel.SymbolGraph` |
| `internal/lsp/references.go` (rewrite) | references over the graph |
| `internal/lsp/definition.go` (modify) | `handleGoDefinition` over the graph |
| `internal/lsp/server.go`, `analysis.go` (modify) | interface + cached graph; drop `CrossRef`/`NavRef` |
| `docs/guide/editor.md`, `docs/ROADMAP.md` (modify) | feature description |

---

### Task 1: sourceintel — identity SourceMap and `BuildIndexWith`

**Files:**
- Create: `internal/sourceintel/identity.go`
- Modify: `internal/sourceintel/index.go`
- Test: `internal/sourceintel/identity_test.go` (new), `internal/sourceintel/index_test.go`

**Interfaces:**
- Produces:
  ```go
  func IdentitySourceMap(path string, size int) (*SourceMap, error)
  type BuildOptions struct {
      Extra     []Occurrence                    // admitted only for paths present in files
      Canonical func(types.Object) types.Object // nil = identity; applied to every harvested/extra object and in Definition()
  }
  func BuildIndexWith(info *types.Info, files []MappedFile, opts BuildOptions) *Index
  func BuildIndex(info *types.Info, files []MappedFile) *Index // == BuildIndexWith(info, files, BuildOptions{})
  func (i *Index) forEachOccurrence(fn func(path string, o Occurrence)) // unexported, for graph.go
  ```

- [ ] **Step 1: Write failing tests**

`internal/sourceintel/identity_test.go`:
```go
package sourceintel

import (
	"crypto/sha256"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"
)

func TestIdentitySourceMapMapsGoFileOntoItself(t *testing.T) {
	const src = "package p\n\ntype T struct{}\n\nfunc use(t T) {}\n"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "helper.go", src, parser.AllErrors)
	if err != nil {
		t.Fatal(err)
	}
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}}
	if _, err := new(types.Config).Check("example.com/p", fset, []*ast.File{file}, info); err != nil {
		t.Fatal(err)
	}
	sm, err := IdentitySourceMap("helper.go", len(src))
	if err != nil {
		t.Fatal(err)
	}
	mapped := MappedFile{AST: file, TokenFile: fset.File(file.Pos()), SourceMap: sm,
		SourceVersion: SourceVersion{Size: len(src), SHA256: sha256.Sum256([]byte(src))}}
	index := BuildIndex(info, []MappedFile{mapped})

	useT := strings.LastIndex(src, "T)")
	occ, ok := index.At("helper.go", useT)
	if !ok || occ.Kind != IdentifierUse || occ.Object == nil || occ.Object.Name() != "T" {
		t.Fatalf("At(use T) = %+v, %v", occ, ok)
	}
	def, ok := index.Definition(occ.Object)
	want := Span{Path: "helper.go", Start: strings.Index(src, "T struct"), End: strings.Index(src, "T struct") + 1}
	if !ok || def != want {
		t.Fatalf("Definition = %+v, %v; want %+v", def, ok, want)
	}
	if !index.MatchesSource("helper.go", []byte(src)) {
		t.Fatal("MatchesSource must accept the identity-mapped bytes")
	}
	decls := index.Declarations("helper.go")
	if len(decls) != 2 {
		t.Fatalf("Declarations = %d, want 2 (T, use)", len(decls))
	}
}
```

Append to `internal/sourceintel/index_test.go`:
```go
func TestBuildIndexWithExtraOccurrencesAndCanonical(t *testing.T) {
	// generated: view.x.go declares Card twice (public + helper); authored view.gsx has one Card.
	const generated = "package p\n\nfunc Card(name string) {}\nfunc _gsxrenderCard(name string) { _ = name }\n"
	const authored = "component Card(name string) {}\n"
	segments := []Segment{
		{Source: spanForSubstring(authored, "Card", 0), GeneratedStart: strings.Index(generated, "Card"), GeneratedEnd: strings.Index(generated, "Card") + 4, Capabilities: Definition | Hover | Symbol},
	}
	info, mapped := parseAndCheckMappedFile(t, generated, authored, segments, nil)
	public := info.Defs[findIdent(t, mapped.AST, "Card", 0)]
	helper := info.Defs[findIdent(t, mapped.AST, "_gsxrenderCard", 0)]
	helperName := info.Defs[findIdent(t, mapped.AST, "name", 1)]
	publicName := info.Defs[findIdent(t, mapped.AST, "name", 0)]
	canonical := func(o types.Object) types.Object {
		switch o {
		case helper:
			return public
		case helperName:
			return publicName
		}
		return o
	}
	tagSpan := Span{Path: "view.gsx", Start: 40, End: 44} // pretend <Card/> site elsewhere in view.gsx
	index := BuildIndexWith(info, []MappedFile{mapped}, BuildOptions{
		Extra: []Occurrence{
			{Span: tagSpan, Kind: IdentifierUse, Object: helper},
			{Span: Span{Path: "other.gsx", Start: 0, End: 4}, Kind: IdentifierUse, Object: helper}, // unknown path: dropped
		},
		Canonical: canonical,
	})
	occ, ok := index.At("view.gsx", 40)
	if !ok || occ.Object != public {
		t.Fatalf("extra occurrence not canonicalised: %+v %v", occ, ok)
	}
	if _, ok := index.At("other.gsx", 0); ok {
		t.Fatal("extra occurrence on an unmapped path must be dropped")
	}
	if def, ok := index.Definition(helper); !ok || def != spanForSubstring(authored, "Card", 0) {
		t.Fatalf("Definition(helper) must resolve through canonical: %+v %v", def, ok)
	}
	if _, ok := index.Definition(helperName); ok {
		t.Fatal("helper param has no authored definition span in this fixture (public param name is not mapped); Definition must not invent one")
	}
	seen := 0
	index.forEachOccurrence(func(path string, o Occurrence) {
		if path == "view.gsx" && o.Object == public {
			seen++
		}
	})
	if seen != 2 { // the Def at "Card" plus the extra Use
		t.Fatalf("forEachOccurrence saw %d public occurrences, want 2", seen)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/sourceintel -run 'TestIdentitySourceMap|TestBuildIndexWithExtra' -count=1`
Expected: FAIL (undefined: IdentitySourceMap, BuildIndexWith, forEachOccurrence).

- [ ] **Step 3: Implement**

`internal/sourceintel/identity.go`:
```go
package sourceintel

// IdentitySourceMap maps an authored Go file onto itself: the "generated" text
// is the authored text, so every byte range maps 1:1 with every capability.
// Hand-written .go siblings and reverse-dependency Go packages enter the index
// through this map; their token.File offsets are their authored offsets.
func IdentitySourceMap(path string, size int) (*SourceMap, error) {
	return NewSourceMap(size, size, path, []Segment{{
		Source:         Span{Path: path, Start: 0, End: size},
		GeneratedStart: 0,
		GeneratedEnd:   size,
		Capabilities:   Definition | Hover | Symbol | Completion,
	}}, nil)
}
```

`internal/sourceintel/index.go` changes:
```go
type Index struct {
	occurrences  map[string][]Occurrence
	definitions  map[types.Object]Span
	declarations map[string][]Declaration
	sources      map[string]SourceVersion
	canonical    func(types.Object) types.Object // nil = identity
}

// BuildOptions extends BuildIndex with facts the type checker cannot see.
type BuildOptions struct {
	// Extra occurrences (component tag sites, attribute→parameter bindings,
	// pipe stage names, build-tag variant declarations). An occurrence whose
	// path is not one of files' source paths is dropped: version gating
	// (MatchesSource) must hold for every indexed path.
	Extra []Occurrence
	// Canonical maps analysis-only helper objects (a split component's body
	// func and its parameters) onto the authored object they stand for. It is
	// applied to every harvested and extra object and inside Definition, so
	// callers may pass either object.
	Canonical func(types.Object) types.Object
}

func BuildIndex(info *types.Info, files []MappedFile) *Index {
	return BuildIndexWith(info, files, BuildOptions{})
}

func BuildIndexWith(info *types.Info, files []MappedFile, opts BuildOptions) *Index {
	index := &Index{ /* maps as before */ canonical: opts.Canonical}
	// ... existing per-file harvest loop unchanged ...
	for _, o := range opts.Extra {
		if o.Object == nil || o.Kind == Expression {
			continue
		}
		if _, ok := index.sources[o.Span.Path]; !ok {
			continue
		}
		o.Object = index.canon(o.Object)
		index.addOccurrence(o)
		if o.Kind == IdentifierDefinition {
			if _, exists := index.definitions[Origin(o.Object)]; !exists {
				index.definitions[Origin(o.Object)] = o.Span
			}
		}
	}
	// ... existing sort/finalize unchanged ...
}

func (i *Index) canon(object types.Object) types.Object {
	if i.canonical == nil || object == nil {
		return object
	}
	return i.canonical(object)
}
```
In `addIdentifier`, replace `Object: object` with `Object: i.canon(object)` and key `definitions` by `Origin(i.canon(object))`. In `addFunctionDeclaration`/`addGeneralDeclaration`, canonicalise the `Object` stored on `Declaration` the same way. In `Definition`:
```go
func (i *Index) Definition(object types.Object) (Span, bool) {
	span, ok := i.definitions[Origin(i.canon(object))]
	return span, ok
}
```
Add:
```go
// forEachOccurrence visits every indexed occurrence (identifier and expression),
// per path in map order. Used by SymbolGraph.
func (i *Index) forEachOccurrence(fn func(path string, o Occurrence)) {
	for path, occurrences := range i.occurrences {
		for _, o := range occurrences {
			fn(path, o)
		}
	}
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/sourceintel -count=1`
Expected: PASS (all, including existing tests).

- [ ] **Step 5: Commit**

```bash
git add internal/sourceintel && git commit -m "sourceintel: identity SourceMap, BuildIndexWith extras + canonical objects"
```

---

### Task 2: sourceintel — `ObjectKey` and `Keyer`

**Files:**
- Create: `internal/sourceintel/key.go`, `internal/sourceintel/key_test.go`

**Interfaces:**
- Produces:
  ```go
  type ObjectKey string // "" = not keyable
  type Keyer struct{ /* enc objectpath.Encoder; pkgPath string; local map[types.Object]int */ }
  func NewKeyer(pkg *types.Package) *Keyer  // pkg may be nil (then no local keys)
  func (k *Keyer) Key(object types.Object) (ObjectKey, bool)
  ```
  Key format: `<pkg import path> + " " + <path>` where `<path>` is the objectpath when `objectpath.Encoder.For(Origin(obj))` succeeds; else the bare name for package-level objects (`obj.Parent() == obj.Pkg().Scope()`); else `"#<n>"` (per-Keyer ordinal) for objects of the Keyer's own package; else not keyable.

- [ ] **Step 1: Write failing test**

`internal/sourceintel/key_test.go`:
```go
package sourceintel

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"testing"
)

const keySrc = `package p
type T struct{ F int; g string }
func (T) M() {}
func (t *T) pm() {}
type unexp int
func (unexp) um() {}
var V, w int
const C = 1
func F(a int) { var local int; _ = local; _ = a }
func use() { _ = w; F(1) }
type G[X any] struct{ Z X }
func (g G[X]) GM(x X) X { return x }
func inst() { var gg G[int]; gg.GM(1); _ = gg.Z }
`

func checkKeySrc(t *testing.T) (*types.Package, *types.Info, *ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", keySrc, 0)
	if err != nil {
		t.Fatal(err)
	}
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}}
	pkg, err := (&types.Config{Importer: importer.Default()}).Check("example.com/p", fset, []*ast.File{file}, info)
	if err != nil {
		t.Fatal(err)
	}
	return pkg, info, file
}

func defOf(t *testing.T, info *types.Info, file *ast.File, name string, ordinal int) types.Object {
	t.Helper()
	obj := info.Defs[findIdent(t, file, name, ordinal)]
	if obj == nil {
		t.Fatalf("no def for %s#%d", name, ordinal)
	}
	return obj
}

func TestKeyerKeys(t *testing.T) {
	pkg, info, file := checkKeySrc(t)
	k := NewKeyer(pkg)
	want := map[string]struct {
		name    string
		ordinal int
	}{
		"example.com/p T":       {"T", 0},
		"example.com/p T.UF0":   {"F", 0},
		"example.com/p T.UF1":   {"g", 0},
		"example.com/p T.M0":    {"M", 0},
		"example.com/p T.M1":    {"pm", 0},
		"example.com/p unexp":   {"unexp", 0},
		"example.com/p unexp.M0": {"um", 0},
		"example.com/p V":       {"V", 0},
		"example.com/p w":       {"w", 0}, // unexported package-level var: name fallback
		"example.com/p C":       {"C", 0},
		"example.com/p F":       {"F", 1},
		"example.com/p F.PA0":   {"a", 0},
		"example.com/p use":     {"use", 0}, // unexported func: name fallback
		"example.com/p G":       {"G", 0},
		"example.com/p G.UF0":   {"Z", 0},
		"example.com/p G.M0":    {"GM", 0},
	}
	for wantKey, ident := range want {
		got, ok := k.Key(defOf(t, info, file, ident.name, ident.ordinal))
		if !ok || string(got) != wantKey {
			t.Errorf("Key(%s#%d) = %q, %v; want %q", ident.name, ident.ordinal, got, ok, wantKey)
		}
	}
	// local var: per-package ordinal, stable across calls, distinct from other locals
	local := defOf(t, info, file, "local", 0)
	k1, ok1 := k.Key(local)
	k2, _ := k.Key(local)
	if !ok1 || k1 != k2 || string(k1) != "example.com/p #0" {
		t.Fatalf("local key = %q %q %v", k1, k2, ok1)
	}
	// generic instance use resolves to origin key
	var gmUse types.Object
	for id, obj := range info.Uses {
		if id.Name == "GM" {
			gmUse = obj
		}
	}
	if got, _ := k.Key(gmUse); string(got) != "example.com/p G.M0" {
		t.Fatalf("instantiated method use key = %q", got)
	}
	// universe objects are not keyable
	if _, ok := k.Key(types.Universe.Lookup("int")); ok {
		t.Fatal("universe object must not be keyable")
	}
}

func TestKeyerStableAcrossIndependentChecks(t *testing.T) {
	_, info1, file1 := checkKeySrc(t)
	_, info2, file2 := checkKeySrc(t)
	k1, k2 := NewKeyer(nil), NewKeyer(nil)
	for _, name := range []string{"T", "F", "M", "a", "Z", "GM", "unexp", "w"} {
		a, _ := k1.Key(defOf(t, info1, file1, name, 0))
		b, _ := k2.Key(defOf(t, info2, file2, name, 0))
		if a == "" || a != b {
			t.Errorf("%s: %q vs %q", name, a, b)
		}
	}
	// a foreign local (Keyer for another package) is not keyable
	if _, ok := k1.Key(defOf(t, info1, file1, "local", 0)); ok {
		t.Fatal("nil-package Keyer must not key locals")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/sourceintel -run 'TestKeyer' -count=1` → FAIL (undefined: NewKeyer).

- [ ] **Step 3: Implement** `internal/sourceintel/key.go`

```go
package sourceintel

import (
	"go/types"
	"strconv"

	"golang.org/x/tools/go/types/objectpath"
)

// ObjectKey is the stable, cross-analysis identity of a Go object inside the
// module symbol graph. Two independent type-checks of the same source produce
// the same key for the same declaration, which pointer identity does not.
//
// Format: "<import path> <path>" where <path> is
//   - the objectpath (exported-reachable objects, unexported types and their
//     members, params, fields, type params);
//   - the bare name for package-level objects objectpath cannot address
//     (unexported funcs/vars/consts) — identical to what objectpath would emit;
//   - "#<n>" for the Keyer's own package's remaining objects (locals): only
//     referenced from within that package, so per-Keyer ordinals suffice.
type ObjectKey string

// Keyer assigns ObjectKeys for one type-checked package's objects. Objects of
// other packages are keyed too (they are what cross-package references point
// at), except their locals, which are never visible cross-package.
type Keyer struct {
	enc     objectpath.Encoder
	pkgPath string
	local   map[types.Object]int
}

func NewKeyer(pkg *types.Package) *Keyer {
	k := &Keyer{local: map[types.Object]int{}}
	if pkg != nil {
		k.pkgPath = pkg.Path()
	}
	return k
}

func (k *Keyer) Key(object types.Object) (ObjectKey, bool) {
	object = Origin(object)
	if object == nil || object.Pkg() == nil {
		return "", false // universe, builtins, nil
	}
	pkgPath := object.Pkg().Path()
	if path, err := k.enc.For(object); err == nil {
		return ObjectKey(pkgPath + " " + string(path)), true
	}
	if isPackageLevel(object) {
		return ObjectKey(pkgPath + " " + object.Name()), true
	}
	if k.pkgPath == "" || pkgPath != k.pkgPath {
		return "", false
	}
	n, ok := k.local[object]
	if !ok {
		n = len(k.local)
		k.local[object] = n
	}
	return ObjectKey(pkgPath + " #" + strconv.Itoa(n)), true
}

// isPackageLevel reports whether object is declared directly in its package
// scope (types, funcs without receiver, vars, consts). Methods and fields have
// no parent scope; locals, params and type params live in nested scopes.
func isPackageLevel(object types.Object) bool {
	switch o := object.(type) {
	case *types.PkgName, *types.Label, *types.Builtin, *types.Nil:
		return false
	case *types.Var:
		if o.IsField() {
			return false
		}
	case *types.Func:
		if sig, ok := o.Type().(*types.Signature); ok && sig.Recv() != nil {
			return false
		}
	}
	return object.Parent() == object.Pkg().Scope()
}
```
Check `go.mod` of the root module already requires `golang.org/x/tools` (it does — `gen` uses it). `internal/sourceintel` is not part of the runtime root package.

- [ ] **Step 4: Run tests** — `go test ./internal/sourceintel -count=1` → PASS. If any objectpath string in the table differs from what `objectpath` actually emits, fix the **table** to the real encoding (the encoding is authoritative), and note it in the commit message.

- [ ] **Step 5: Commit** — `git add internal/sourceintel && git commit -m "sourceintel: stable ObjectKey + Keyer"`

---

### Task 3: sourceintel — `SymbolGraph`

**Files:**
- Create: `internal/sourceintel/graph.go`, `internal/sourceintel/graph_test.go`

**Interfaces:**
- Produces:
  ```go
  type SymbolGraph struct{ /* occurrences map[string][]keyedOccurrence; definitions, references map[ObjectKey][]Span; sources map[string]SourceVersion */ }
  func NewSymbolGraph() *SymbolGraph
  func (g *SymbolGraph) AddIndex(index *Index, keyer *Keyer)      // merges one package's index
  func (g *SymbolGraph) At(path string, offset int) (ObjectKey, Span, bool)
  func (g *SymbolGraph) Definitions(key ObjectKey) []Span        // sorted by path, start; deduped
  func (g *SymbolGraph) References(key ObjectKey) []Span         // sorted, deduped; excludes definitions
  func (g *SymbolGraph) MatchesSource(path string, source []byte) bool
  func (g *SymbolGraph) Stats() (files, keys, occurrences int)
  ```

- [ ] **Step 1: Write failing test** `internal/sourceintel/graph_test.go`

```go
package sourceintel

import (
	"crypto/sha256"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"
)

// two packages: dep declares T and F; app uses dep.T and dep.F, plus a local.
func buildTwoPackageGraph(t *testing.T) (*SymbolGraph, string, string) {
	t.Helper()
	const depSrc = "package dep\n\ntype T struct{ X int }\n\nfunc F(t T) int { return t.X }\n"
	const appSrc = "package app\n\nimport \"example.com/dep\"\n\nfunc use() int {\n\tvar v dep.T\n\tv.X = 1\n\treturn dep.F(v)\n}\n"
	fset := token.NewFileSet()
	depFile, _ := parser.ParseFile(fset, "dep.go", depSrc, 0)
	appFile, _ := parser.ParseFile(fset, "app.go", appSrc, 0)
	newInfo := func() *types.Info {
		return &types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}, Types: map[ast.Expr]types.TypeAndValue{}}
	}
	depInfo := newInfo()
	depPkg, err := (&types.Config{Importer: importer.Default()}).Check("example.com/dep", fset, []*ast.File{depFile}, depInfo)
	if err != nil {
		t.Fatal(err)
	}
	// SECOND, independent check of dep — app imports this one, so object
	// pointers differ from depPkg's; keys must still agree.
	depInfo2 := newInfo()
	depPkg2, _ := (&types.Config{Importer: importer.Default()}).Check("example.com/dep", fset, []*ast.File{depFile}, depInfo2)
	appInfo := newInfo()
	imp := importerFunc(func(path string) (*types.Package, error) {
		if path == "example.com/dep" {
			return depPkg2, nil
		}
		return importer.Default().Import(path)
	})
	appPkg, err := (&types.Config{Importer: imp}).Check("example.com/app", fset, []*ast.File{appFile}, appInfo)
	if err != nil {
		t.Fatal(err)
	}
	mapped := func(file *ast.File, path, src string) MappedFile {
		sm, _ := IdentitySourceMap(path, len(src))
		return MappedFile{AST: file, TokenFile: fset.File(file.Pos()), SourceMap: sm, SourceVersion: SourceVersion{Size: len(src), SHA256: sha256.Sum256([]byte(src))}}
	}
	g := NewSymbolGraph()
	g.AddIndex(BuildIndex(depInfo, []MappedFile{mapped(depFile, "dep.go", depSrc)}), NewKeyer(depPkg))
	g.AddIndex(BuildIndex(appInfo, []MappedFile{mapped(appFile, "app.go", appSrc)}), NewKeyer(appPkg))
	return g, depSrc, appSrc
}

type importerFunc func(string) (*types.Package, error)

func (f importerFunc) Import(path string) (*types.Package, error) { return f(path) }

func TestSymbolGraphMergesCrossPackageByKey(t *testing.T) {
	g, depSrc, appSrc := buildTwoPackageGraph(t)
	key, span, ok := g.At("app.go", strings.Index(appSrc, "dep.T")+4)
	if !ok || string(key) != "example.com/dep T" || span.Path != "app.go" {
		t.Fatalf("At(app dep.T) = %q %+v %v", key, span, ok)
	}
	defs := g.Definitions(key)
	if len(defs) != 1 || defs[0] != (Span{Path: "dep.go", Start: strings.Index(depSrc, "T struct"), End: strings.Index(depSrc, "T struct") + 1}) {
		t.Fatalf("Definitions(T) = %+v", defs)
	}
	refs := g.References(key)
	if len(refs) != 2 { // dep.go "F(t T)" and app.go "dep.T"
		t.Fatalf("References(T) = %+v, want 2", refs)
	}
	xKey, _, _ := g.At("app.go", strings.Index(appSrc, "v.X")+2)
	if string(xKey) != "example.com/dep T.UF0" || len(g.References(xKey)) != 2 || len(g.Definitions(xKey)) != 1 {
		t.Fatalf("field X: key=%q refs=%+v defs=%+v", xKey, g.References(xKey), g.Definitions(xKey))
	}
	if !g.MatchesSource("app.go", []byte(appSrc)) || g.MatchesSource("app.go", []byte(appSrc+" ")) {
		t.Fatal("MatchesSource must gate on the exact indexed bytes")
	}
	if _, _, ok := g.At("nope.go", 0); ok {
		t.Fatal("unknown path must miss")
	}
	// local var v: keyed, references within app.go only
	vKey, _, ok := g.At("app.go", strings.Index(appSrc, "var v")+4)
	if !ok || !strings.HasPrefix(string(vKey), "example.com/app #") || len(g.References(vKey)) != 2 {
		t.Fatalf("local v: %q %v refs=%+v", vKey, ok, g.References(vKey))
	}
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./internal/sourceintel -run TestSymbolGraph -count=1` → FAIL.

- [ ] **Step 3: Implement** `internal/sourceintel/graph.go`

```go
package sourceintel

import "sort"

type keyedOccurrence struct {
	span Span
	key  ObjectKey
	kind OccurrenceKind
}

// SymbolGraph is the module-wide, keyed union of per-package indexes: every
// definition and reference span of every keyable object, plus per-file
// occurrence tables so a cursor in any indexed file resolves to a key.
type SymbolGraph struct {
	occurrences map[string][]keyedOccurrence // path → sorted by start, then shortest
	definitions map[ObjectKey][]Span
	references  map[ObjectKey][]Span
	sources     map[string]SourceVersion
	finalized   bool
}

func NewSymbolGraph() *SymbolGraph {
	return &SymbolGraph{
		occurrences: map[string][]keyedOccurrence{},
		definitions: map[ObjectKey][]Span{},
		references:  map[ObjectKey][]Span{},
		sources:     map[string]SourceVersion{},
	}
}

// AddIndex merges one package's index. Occurrences without a keyable object
// (expressions, universe objects, foreign locals) are skipped.
func (g *SymbolGraph) AddIndex(index *Index, keyer *Keyer) {
	if index == nil || keyer == nil {
		return
	}
	for path, version := range index.sources {
		g.sources[path] = version
	}
	index.forEachOccurrence(func(path string, o Occurrence) {
		if o.Kind == Expression || o.Object == nil {
			return
		}
		key, ok := keyer.Key(o.Object)
		if !ok {
			return
		}
		g.occurrences[path] = append(g.occurrences[path], keyedOccurrence{span: o.Span, key: key, kind: o.Kind})
		if o.Kind == IdentifierDefinition {
			g.definitions[key] = append(g.definitions[key], o.Span)
		} else {
			g.references[key] = append(g.references[key], o.Span)
		}
	})
	g.finalized = false
}

func (g *SymbolGraph) finalize() {
	if g.finalized {
		return
	}
	for path, occ := range g.occurrences {
		sort.SliceStable(occ, func(a, b int) bool {
			if occ[a].span.Start != occ[b].span.Start {
				return occ[a].span.Start < occ[b].span.Start
			}
			la, lb := occ[a].span.End-occ[a].span.Start, occ[b].span.End-occ[b].span.Start
			if la != lb {
				return la < lb
			}
			return occ[a].kind < occ[b].kind // definitions before uses on identical spans
		})
		g.occurrences[path] = occ
	}
	for k, spans := range g.definitions {
		g.definitions[k] = sortDedupSpans(spans)
	}
	for k, spans := range g.references {
		g.references[k] = sortDedupSpans(spans)
	}
	g.finalized = true
}

func sortDedupSpans(spans []Span) []Span {
	sort.Slice(spans, func(a, b int) bool {
		if spans[a].Path != spans[b].Path {
			return spans[a].Path < spans[b].Path
		}
		if spans[a].Start != spans[b].Start {
			return spans[a].Start < spans[b].Start
		}
		return spans[a].End < spans[b].End
	})
	out := spans[:0]
	for i, s := range spans {
		if i == 0 || s != spans[i-1] {
			out = append(out, s)
		}
	}
	return out
}

// At returns the key of the innermost identifier occurrence covering offset
// in path (definitions preferred over uses on identical spans).
func (g *SymbolGraph) At(path string, offset int) (ObjectKey, Span, bool) {
	g.finalize()
	occ := g.occurrences[path]
	best := -1
	for i, o := range occ {
		if o.span.Start > offset {
			break
		}
		if offset < o.span.End || (o.span.Start == o.span.End && offset == o.span.Start) {
			if best < 0 || (o.span.End-o.span.Start) < (occ[best].span.End-occ[best].span.Start) {
				best = i
			}
		}
	}
	if best < 0 {
		return "", Span{}, false
	}
	return occ[best].key, occ[best].span, true
}

func (g *SymbolGraph) Definitions(key ObjectKey) []Span {
	g.finalize()
	return append([]Span(nil), g.definitions[key]...)
}

func (g *SymbolGraph) References(key ObjectKey) []Span {
	g.finalize()
	return append([]Span(nil), g.references[key]...)
}

func (g *SymbolGraph) MatchesSource(path string, source []byte) bool {
	v, ok := g.sources[path]
	return ok && v.Matches(source)
}

func (g *SymbolGraph) Stats() (files, keys, occurrences int) {
	for _, o := range g.occurrences {
		occurrences += len(o)
	}
	seen := map[ObjectKey]bool{}
	for k := range g.definitions {
		seen[k] = true
	}
	for k := range g.references {
		seen[k] = true
	}
	return len(g.sources), len(seen), occurrences
}
```
Note on `At`: files are small enough for a linear scan bounded by `Start > offset`; if a profile later shows this on the hot path, switch to the same interval-tree trick `Index.At` uses. Do not pre-optimize now.

- [ ] **Step 4: Run tests** — `go test ./internal/sourceintel -count=1` → PASS.
- [ ] **Step 5: Commit** — `git add internal/sourceintel && git commit -m "sourceintel: SymbolGraph — keyed module-wide union of package indexes"`

---

### Task 4: `internal/pipeshape` — shared pipe walker

**Files:**
- Create: `internal/pipeshape/pipeshape.go`, `internal/pipeshape/pipeshape_test.go`
- Modify: `internal/lsp/pipe.go` (delete `walkPipe`, `pipeStages`, `ctxIdent`; call `pipeshape`), `internal/lsp/definition.go` (`hasPipeStages` → `len(pipeshape.Stages(n)) > 0`)

**Interfaces:**
- Produces:
  ```go
  package pipeshape
  const CtxIdent = "ctx"
  func Stages(node gsxast.Node) []gsxast.PipeStage
  func Walk(skel goast.Expr, n int) (selSel []*goast.Ident, selArgs [][]goast.Expr, seed goast.Expr, ok bool)
  ```
  Bodies are the existing `internal/lsp/pipe.go:16-48` and `:316-330` verbatim (including `unwrapParens` — move it too if only used there; otherwise copy a private `unwrapParens`).

- [ ] **Step 1: Write the test** `internal/pipeshape/pipeshape_test.go`: parse `upper(ctx, lower(x), "a")` with `go/parser.ParseExpr` and assert `Walk(e, 2)` returns `selSel` names `["lower","upper"]`, `selArgs[1]` length 1, seed ident `x`; assert `Walk(e, 3)` → `ok=false`; assert `Stages(&gsxast.Interp{Stages: []gsxast.PipeStage{{Name: "a"}}})` length 1 and `Stages(&gsxast.Element{})` nil. Note the lowering nests through the *subject*: with `ctx` injected the subject is `Args[1]`; write the expression as `pkg.upper(ctx, pkg.lower(x), "a")` so `Fun` is a `SelectorExpr`.
- [ ] **Step 2: Run** `go test ./internal/pipeshape -count=1` → FAIL (package missing).
- [ ] **Step 3: Implement** by moving the code; update `internal/lsp/pipe.go` and `definition.go` call sites (`walkPipe(` → `pipeshape.Walk(`, `pipeStages(` → `pipeshape.Stages(`).
- [ ] **Step 4: Run** `go test ./internal/pipeshape ./internal/lsp -count=1` → PASS; `go vet ./internal/lsp ./internal/pipeshape` clean.
- [ ] **Step 5: Commit** — `git commit -am "pipeshape: share the lowered-pipe walker between codegen and lsp"`

---

### Task 5: codegen — companion `.go` MappedFiles + gsx-only extras + canonical objects in the per-package index

**Files:**
- Create: `internal/codegen/symbol_extras.go`
- Modify: `internal/codegen/component_target_package.go` (expose `(path, file)` pairs), `internal/codegen/module_importer.go` (mapped files; move index build after positional planning), `internal/codegen/module.go` (reuse `a.componentCalls`)
- Test: `internal/codegen/symbol_index_test.go` (new — replaces `crossindex_test.go` + `navindex_test.go`, which Task 6 deletes; net Module count unchanged)

**Interfaces:**
- Consumes: Task 1 `BuildIndexWith`, Task 4 `pipeshape`.
- Produces (internal to codegen):
  ```go
  type companionGoFile struct { path string; file *goast.File }
  func (m *Module) companionGoSources(dir string, gsxFiles map[string]*gsxast.File) ([]companionGoFile, []string, error)
  // parseTargetCompanionGoFiles becomes a wrapper returning just the files.
  func componentCanonicalizer(objKey map[types.Object]string, publicByKey map[string]types.Object) func(types.Object) types.Object
  func gsxExtraOccurrences(calls map[*gsxast.Element]ComponentCallFact, componentDecls map[ComponentDeclKey][]sourceintel.VersionedSpan, pkgPath string, objByLogicalKey map[string]types.Object, exprMap map[gsxast.Node]goast.Expr, info *types.Info, gsxFset *token.FileSet, gsxFiles map[string]*gsxast.File) []sourceintel.Occurrence
  ```
  `analyzed` gains `componentCalls map[*gsxast.Element]ComponentCallFact` (built once in analyze; `Package`/ephemeral reuse it instead of calling `componentCallFacts` again).

- [ ] **Step 1: Write failing test** `internal/codegen/symbol_index_test.go`

```go
package codegen

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/sourceintel"
)

// One Module open for the whole per-package symbol-index surface (replaces
// crossindex_test.go + navindex_test.go).
func TestPackageSymbolIndex(t *testing.T) {
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("../..")
	writeFile(t, dir, "go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	const card = "package x\n\n// Card renders a card.\ntype Card struct{ Title string }\n\nfunc (c Card) title() string { return c.Title }\n\ncomponent (c Card) Render(size int) {\n\t<div>{ c.title() |> upper }{ size }</div>\n}\n\ncomponent Badge(label string) {\n\t<span>{ label }</span>\n}\n\ncomponent tiny() {\n\t<i/>\n}\n"
	const page = "package x\n\ncomponent Page() {\n\t<main>\n\t\t<Badge label=\"a\"/>\n\t\t<tiny/>\n\t\t{ Card{Title: \"t\"}.Render(1) }\n\t</main>\n}\n"
	const helper = "package x\n\nfunc use() Card { c := Card{}; _ = c.title(); _ = Badge; return c }\n"
	writeFile(t, dir, "card.gsx", card)
	writeFile(t, dir, "page.gsx", page)
	writeFile(t, dir, "helper.go", helper)
	m, err := Open(Options{ModuleRoot: dir, ModulePath: "example.com/x", FilterPkgs: []string{StdImportPath}})
	if err != nil {
		t.Fatal(err)
	}
	pr, err := m.Package(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(pr.Diags) != 0 {
		t.Fatalf("diags: %+v", pr.Diags)
	}
	g := sourceintel.NewSymbolGraph()
	g.AddIndex(pr.SourceIndex, sourceintel.NewKeyer(pr.Types))
	cardPath, pagePath, helperPath := filepath.Join(dir, "card.gsx"), filepath.Join(dir, "page.gsx"), filepath.Join(dir, "helper.go")
	at := func(path, src, needle string, occurrence int) sourceintel.ObjectKey {
		t.Helper()
		off := nth(src, needle, occurrence)
		key, _, ok := g.At(path, off)
		if !ok {
			t.Fatalf("no occurrence at %s:%d (%q#%d)", filepath.Base(path), off, needle, occurrence)
		}
		return key
	}
	files := func(spans []sourceintel.Span) map[string]int {
		out := map[string]int{}
		for _, s := range spans {
			out[filepath.Base(s.Path)]++
		}
		return out
	}
	// type Card: def in card.gsx; refs in card.gsx (recv, method recv), page.gsx literal, helper.go x3
	cardKey := at(cardPath, card, "Card struct", 0)
	if string(cardKey) != "example.com/x Card" {
		t.Fatalf("Card key = %q", cardKey)
	}
	if f := files(g.References(cardKey)); f["helper.go"] != 3 || f["page.gsx"] != 1 || f["card.gsx"] < 2 {
		t.Fatalf("Card refs = %v", f)
	}
	// helper.go cursor resolves to the same key (identity-mapped sibling)
	if k := at(helperPath, helper, "Card {", 0); k != cardKey {
		t.Fatalf("helper.go Card key = %q", k)
	}
	// unexported method title: def + 2 refs (card.gsx pipe seed, helper.go)
	titleKey := at(cardPath, card, "title()", 0)
	if f := files(g.References(titleKey)); f["card.gsx"] != 1 || f["helper.go"] != 1 {
		t.Fatalf("title refs = %v", f)
	}
	// component Badge: <Badge/> tag in page.gsx + helper.go value use
	badgeKey := at(cardPath, card, "Badge", 0)
	if f := files(g.References(badgeKey)); f["page.gsx"] != 1 || f["helper.go"] != 1 {
		t.Fatalf("Badge refs = %v", f)
	}
	if k := at(pagePath, page, "Badge", 0); k != badgeKey {
		t.Fatalf("tag cursor key = %q, want %q", k, badgeKey)
	}
	// param label: def + attr site in page.gsx + body use in card.gsx
	labelKey := at(cardPath, card, "label", 0)
	if f := files(g.References(labelKey)); f["page.gsx"] != 1 || f["card.gsx"] != 1 {
		t.Fatalf("label refs = %v (key %q)", f, labelKey)
	}
	if k := at(pagePath, page, "label=", 0); k != labelKey {
		t.Fatalf("attr cursor key = %q, want %q", k, labelKey)
	}
	// private component tiny: def + <tiny/> tag
	tinyKey := at(pagePath, page, "tiny", 0)
	if len(g.Definitions(tinyKey)) != 1 || files(g.References(tinyKey))["page.gsx"] != 1 {
		t.Fatalf("tiny: defs=%+v refs=%+v", g.Definitions(tinyKey), g.References(tinyKey))
	}
	// method component Render: page.gsx Go call + def
	renderKey := at(cardPath, card, "Render", 0)
	if f := files(g.References(renderKey)); f["page.gsx"] != 1 {
		t.Fatalf("Render refs = %v", f)
	}
	// pipe stage name upper → std filter func key, referenced from card.gsx
	upperKey := at(cardPath, card, "upper", 0)
	if !strings.HasPrefix(string(upperKey), StdImportPath+" ") || files(g.References(upperKey))["card.gsx"] != 1 {
		t.Fatalf("upper key=%q refs=%+v", upperKey, g.References(upperKey))
	}
	// param size: def and body use both in card.gsx
	sizeKey := at(cardPath, card, "size int", 0)
	if len(g.Definitions(sizeKey)) != 1 || files(g.References(sizeKey))["card.gsx"] != 1 {
		t.Fatalf("size: defs=%+v refs=%+v", g.Definitions(sizeKey), g.References(sizeKey))
	}
}

func nth(src, needle string, occurrence int) int {
	off := -1
	for i := 0; i <= occurrence; i++ {
		next := strings.Index(src[off+1:], needle)
		if next < 0 {
			return -1
		}
		off = off + 1 + next
	}
	return off
}
```
If `nth` already exists in the codegen test package under that name, reuse it.

- [ ] **Step 2: Run** `go test ./internal/codegen -run TestPackageSymbolIndex -count=1` → FAIL (helper.go cursor misses; tag/attr/pipe misses).

- [ ] **Step 3: Implement**

(a) `component_target_package.go`: split `parseTargetCompanionGoFiles` into `companionGoSources` returning `[]companionGoFile{path, file}` (path = the logical `compiledGoFiles` entry) plus import paths; keep `parseTargetCompanionGoFiles` as a wrapper that projects `.file`.

(b) `module_importer.go` `analyze`: where `companionFiles` are appended (≈:1428), when `purpose == analysisRetainedPackage`, for each `companionGoFile{path, file}`:
```go
src, ok := m.currentSource(path)
tf := fset.File(file.Pos())
if ok && tf != nil && tf.Size() == len(src) {
	sm, err := sourceintel.IdentitySourceMap(path, len(src))
	if err == nil {
		mappedFiles = append(mappedFiles, sourceintel.MappedFile{AST: file, TokenFile: tf, SourceMap: sm,
			SourceVersion: sourceintel.SourceVersion{Size: len(src), SHA256: sha256.Sum256(src)}})
	}
}
```
A size mismatch means the retained syntax and the current bytes disagree; the file is skipped so the index never publishes spans against bytes it was not built from (same fail-closed rule as `MatchesSource`). Add a counter `m.companionIndexSkips` (test hook) so a mismatch is observable, and a comment saying why.

(c) Move the `sourceIndex = sourceintel.BuildIndex(info, mappedFiles)` call from ≈:1546 to after `positionalPlan` is final (after the `planComponentPositionalCalls` block). There:
```go
var sourceIndex *sourceintel.Index
var componentCalls map[*gsxast.Element]ComponentCallFact
if purpose == analysisRetainedPackage {
	componentCalls = componentCallFacts(positionalPlan)
	publicByKey := publicComponentObjects(goFiles, info, objKey, componentPlan) // logicalKey → public func obj (or nil)
	canonical := componentCanonicalizer(objKey, publicByKey)
	extra := gsxExtraOccurrences(componentCalls, componentDecls, pkgPath, canonicalObjByLogicalKey(objKey, publicByKey), exprMap, info, fset, gsxFiles)
	sourceIndex = sourceintel.BuildIndexWith(info, mappedFiles, sourceintel.BuildOptions{Extra: extra, Canonical: canonical})
	m.mu.Lock(); m.sourceIndexBuildCount++; m.mu.Unlock()
}
```
`componentDecls` is built later in analyze today (≈:1714–1723); move that block above this point (it depends only on `localComponentProvenance` and `pkg.Imports()`, both available), or compute the index at the very end of analyze — either is fine as long as `componentDecls` is populated first. Store `componentCalls` on `analyzed`; in `module.go` `Package` and `analyzeEphemeralLocked` set `res.ComponentCalls = a.componentCalls`.

(d) `symbol_extras.go`:
```go
package codegen

// publicComponentObjects returns, per logical component key, the type object of
// the PUBLIC skeleton declaration (func named exactly c.Name), or nil when the
// component is emitted body-only (private components).
func publicComponentObjects(goFiles []*goast.File, info *types.Info, objKey map[types.Object]string, plan componentTargetPlan) map[string]types.Object {
	out := map[string]types.Object{}
	for obj, key := range objKey {
		if fn, ok := obj.(*types.Func); ok && plan.isPublicDeclaration(key, fn.Name()) { // implement via componentPlan.emission(c).public && c.Name == fn.Name()
			out[key] = obj
		}
	}
	return out
}
```
Implement `isPublicDeclaration` by whatever `componentTargetPlan` already knows (`emission(c).public`, `componentKey(c)`) — do not invent a second registry; if the plan can answer "is this func name the public name of logical key K", use that.

```go
// canonicalObjByLogicalKey picks the object every edge of a component attaches
// to: the public func when there is one, else the body func.
func canonicalObjByLogicalKey(objKey map[types.Object]string, publicByKey map[string]types.Object) map[string]types.Object {
	out := map[string]types.Object{}
	for obj, key := range objKey {
		if p := publicByKey[key]; p != nil {
			out[key] = p
			continue
		}
		if _, ok := out[key]; !ok {
			out[key] = obj
		}
	}
	return out
}

// componentCanonicalizer maps a split component's body func — and its
// parameters, receiver and type parameters, positionally — onto the public
// declaration's objects, so tag sites, attr bindings, Go callers and body uses
// all key to one symbol.
func componentCanonicalizer(objKey map[types.Object]string, publicByKey map[string]types.Object) func(types.Object) types.Object {
	alias := map[types.Object]types.Object{}
	for obj, key := range objKey {
		public := publicByKey[key]
		if public == nil || public == obj {
			continue
		}
		alias[obj] = public
		bodySig, ok1 := obj.Type().(*types.Signature)
		pubSig, ok2 := public.Type().(*types.Signature)
		if !ok1 || !ok2 {
			continue
		}
		aliasTuple(alias, bodySig.Params(), pubSig.Params())
		aliasTuple(alias, bodySig.Results(), pubSig.Results())
		if bodySig.Recv() != nil && pubSig.Recv() != nil {
			alias[bodySig.Recv()] = pubSig.Recv()
		}
		for i := 0; i < bodySig.TypeParams().Len() && i < pubSig.TypeParams().Len(); i++ {
			alias[bodySig.TypeParams().At(i).Obj()] = pubSig.TypeParams().At(i).Obj()
		}
	}
	if len(alias) == 0 {
		return nil
	}
	return func(o types.Object) types.Object {
		if c, ok := alias[sourceintel.Origin(o)]; ok {
			return c
		}
		return o
	}
}

func aliasTuple(alias map[types.Object]types.Object, from, to *types.Tuple) {
	for i := 0; i < from.Len() && i < to.Len(); i++ {
		alias[from.At(i)] = to.At(i)
	}
}

// gsxExtraOccurrences produces the reference/definition occurrences the type
// checker cannot see: component tag sites, attribute→parameter bindings, pipe
// stage names, and every build-tag variant's declaration span.
func gsxExtraOccurrences(calls map[*gsxast.Element]ComponentCallFact, componentDecls map[ComponentDeclKey][]sourceintel.VersionedSpan, pkgPath string, canonicalByLogicalKey map[string]types.Object, exprMap map[gsxast.Node]goast.Expr, info *types.Info, gsxFset *token.FileSet, gsxFiles map[string]*gsxast.File) []sourceintel.Occurrence {
	var out []sourceintel.Occurrence
	spanAt := func(pos token.Pos, length int) (sourceintel.Span, bool) {
		if !pos.IsValid() || length <= 0 {
			return sourceintel.Span{}, false
		}
		p := gsxFset.Position(pos)
		return sourceintel.Span{Path: p.Filename, Start: p.Offset, End: p.Offset + length}, true
	}
	// 1. tag sites + attr bindings
	for el, call := range calls {
		if el == nil || call.Target == nil {
			continue
		}
		local := el.Tag
		if i := strings.LastIndexByte(local, '.'); i >= 0 {
			local = local[i+1:]
		}
		if span, ok := spanAt(el.TagPos+token.Pos(len(el.Tag)-len(local)), len(local)); ok {
			out = append(out, sourceintel.Occurrence{Span: span, Kind: sourceintel.IdentifierUse, Object: call.Target})
		}
		for attr, param := range call.Params {
			name, ok := componentInputAttrName(attr)
			if !ok || param.Origin == nil {
				continue
			}
			if span, ok := spanAt(attr.Pos(), len(name)); ok {
				out = append(out, sourceintel.Occurrence{Span: span, Kind: sourceintel.IdentifierUse, Object: param.Origin})
			}
		}
	}
	// 2. pipe stage names — structural, mirrors lsp pipedTarget
	for node, skel := range exprMap {
		stages := pipeshape.Stages(node)
		if len(stages) == 0 {
			continue
		}
		selSel, _, _, ok := pipeshape.Walk(skel, len(stages))
		if !ok {
			continue
		}
		for i, st := range stages {
			obj := info.Uses[selSel[i]]
			if obj == nil {
				obj = info.Defs[selSel[i]]
			}
			if obj == nil {
				continue
			}
			if span, ok := spanAt(st.NamePos, len(st.Name)); ok {
				out = append(out, sourceintel.Occurrence{Span: span, Kind: sourceintel.IdentifierUse, Object: obj})
			}
		}
	}
	// 3. every variant's declaration span, on the canonical object
	for key, spans := range componentDecls {
		if key.PackagePath != pkgPath {
			continue
		}
		obj := canonicalByLogicalKey[key.ComponentKey]
		if obj == nil {
			continue
		}
		for _, vs := range spans {
			out = append(out, sourceintel.Occurrence{Span: vs.Span, Kind: sourceintel.IdentifierDefinition, Object: obj})
		}
	}
	return out
}
```
Verify during implementation that `componentDecls`' `ComponentKey` is the same string as `objKey`'s logical key (both come from `componentPlan.logicalKey`); if they differ, translate — do not guess: read `component_lsp_facts.go` `componentParamDeclarationFacts` which already joins the two.

- [ ] **Step 4: Run** `go test ./internal/codegen -run TestPackageSymbolIndex -count=1` → PASS. Then `go test ./internal/lsp -count=1` and `go test ./gen -run 'Definition|Hover|Pipe' -count=1` → PASS with no behavior change (see Global Constraints).

- [ ] **Step 5: Commit** — `git add -A internal/codegen && git commit -m "codegen: index hand-written .go siblings, gsx-only edges and variant decls in the package SourceIndex"`

---

### Task 6: codegen — delete `CrossIndex`/`NavIndex`

**Files:**
- Delete: `internal/codegen/crossnav.go`, `internal/codegen/crossnav_test.go`, `internal/codegen/crossindex_test.go`, `internal/codegen/navindex_test.go`
- Modify: `internal/codegen/results.go` (remove `CrossRef`, `NavRef`, `PackageResult.CrossIndex/NavIndex`), `internal/codegen/module.go` (both `buildCrossNav` calls + `addLocalComponentCallRefs`), `internal/codegen/module_test.go:237`, `override_lifecycle_test.go:198`, `variant_generate_test.go` (assertions at 197,210,375,403,444,516)
- Keep `objKey`/`compByKey` (still feed rename facts).

- [ ] **Step 1:** Rewrite the surviving assertions against `SourceIndex`: where a test asserted `pr.CrossIndex[".Home"]` exists, assert `pr.SourceIndex.Declarations(<gsx path>)` contains a `Declaration{Name: "Home", Kind: DeclarationFunction}`; where `variant_generate_test.go` asserted both variant `Decls`, assert `sourceintel.NewSymbolGraph()`+`AddIndex(pr.SourceIndex, NewKeyer(pr.Types))` → `Definitions(key)` has 2 spans in the two variant files, and `References` still lists `page.gsx`; where `NavIndex` was asserted (`main.go` `Card` → `card.gsx`), assert `graph.At(main.go, off)` key == the `Card` def key and `Definitions` lands on `card.gsx`.
- [ ] **Step 2:** Delete the files/fields; `go build ./... && go vet ./internal/codegen`.
- [ ] **Step 3:** `go test ./internal/codegen -run 'TestModulePackageRetainsAnalysis|TestOverrideLifecycle|TestVariant|TestPackageSymbolIndex' -count=1` → PASS. (`gen` and `lsp` will not compile until Task 9/10 — that is expected; do not touch them here.)
- [ ] **Step 4: Commit** — `git add -A internal/codegen && git commit -m "codegen: delete CrossIndex/NavIndex — the SourceIndex is the one symbol index"`

---

### Task 7: codegen — reverse-dependency Go-only package index

**Files:**
- Create: `internal/codegen/go_package_index.go`
- Modify: `internal/codegen/module_importer.go` (`shippingGoPackageWith` delegates to the new analysis; cache invalidation), `internal/codegen/module.go` (field + resets)
- Test: `internal/codegen/go_package_index_test.go` (new — ONE Module open; this is the "reverse-dep + module graph" fixture also used by Task 8)

**Interfaces:**
- Produces:
  ```go
  type goPackageAnalysis struct {
      pkg      *types.Package
      info     *types.Info
      files    []companionGoFile
      typeErrs []types.Error
      index    *sourceintel.Index // built lazily by GoPackageIndex
  }
  func (m *Module) goPackageAnalysisWith(dir string, mi *moduleImporter) (*goPackageAnalysis, error) // cached in m.goPkgAnalyses[dir]
  func (m *Module) reverseDependencyGoDirs() ([]string, error) // sorted; Go-only dirs whose transitive imports reach a gsx dir
  func (m *Module) GoPackageIndex(dir string) (*sourceintel.Index, *types.Package, error) // takes analysisMu
  ```

- [ ] **Step 1: Write failing test** `internal/codegen/go_package_index_test.go`

```go
package codegen

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/sourceintel"
)

// writeReverseDepModule: components/ (gsx) ← app/ (gsx, imports components)
// ← cmd/ (Go-only main, imports both) ; util/ (Go-only, imports nothing gsx).
func writeReverseDepModule(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	repoRoot, _ := filepath.Abs("../..")
	writeFile(t, root, "go.mod", "module example.com/rd\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	writeFile(t, root, "components/input.gsx", "package components\n\ntype Size int\n\ncomponent Input(name string, size Size) {\n\t<input name={ name }/>\n}\n")
	writeFile(t, root, "app/page.gsx", "package app\n\nimport \"example.com/rd/components\"\n\ntype Home struct{}\n\ncomponent (h Home) Page() {\n\t<main><components.Input name=\"a\" size={ components.Size(1) }/></main>\n}\n")
	writeFile(t, root, "cmd/main.go", "package main\n\nimport (\n\t\"example.com/rd/app\"\n\t\"example.com/rd/components\"\n)\n\nfunc main() {\n\tvar h app.Home\n\t_ = h.Page\n\t_ = components.Input\n\tvar s components.Size\n\t_ = s\n}\n")
	writeFile(t, root, "util/util.go", "package util\n\nfunc U() {}\n")
	return root
}

func TestReverseDependencyGoPackageIndex(t *testing.T) {
	root := writeReverseDepModule(t)
	m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/rd", FilterPkgs: []string{StdImportPath}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Package(filepath.Join(root, "app")); err != nil { // warms the inventory
		t.Fatal(err)
	}
	dirs, err := m.reverseDependencyGoDirs()
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) != 1 || filepath.Base(dirs[0]) != "cmd" {
		t.Fatalf("reverseDependencyGoDirs = %v, want [cmd]", dirs)
	}
	index, pkg, err := m.GoPackageIndex(dirs[0])
	if err != nil {
		t.Fatal(err)
	}
	mainPath := filepath.Join(root, "cmd", "main.go")
	src := "package main\n\nimport (\n\t\"example.com/rd/app\"\n\t\"example.com/rd/components\"\n)\n\nfunc main() {\n\tvar h app.Home\n\t_ = h.Page\n\t_ = components.Input\n\tvar s components.Size\n\t_ = s\n}\n"
	occ, ok := index.At(mainPath, strings.Index(src, "app.Home")+4)
	if !ok || occ.Object == nil || occ.Object.Name() != "Home" {
		t.Fatalf("main.go Home occurrence: %+v %v", occ, ok)
	}
	k := sourceintel.NewKeyer(pkg)
	key, _ := k.Key(occ.Object)
	if string(key) != "example.com/rd/app Home" {
		t.Fatalf("Home key from main.go = %q", key)
	}
	// second call is cached (no re-check)
	before := m.goPackageAnalysisCount()
	if _, _, err := m.GoPackageIndex(dirs[0]); err != nil {
		t.Fatal(err)
	}
	if m.goPackageAnalysisCount() != before {
		t.Fatal("GoPackageIndex must reuse the cached analysis")
	}
	// editing app/page.gsx invalidates cmd's analysis (reverse closure)
	m.Invalidate(filepath.Join(root, "app"))
	if _, _, err := m.GoPackageIndex(dirs[0]); err != nil {
		t.Fatal(err)
	}
	if m.goPackageAnalysisCount() != before+1 {
		t.Fatalf("expected re-analysis after dependency invalidation, count %d → %d", before, m.goPackageAnalysisCount())
	}
}
```
(`m.goPackageAnalysisCount()` is a test hook counter incremented per real check.)

- [ ] **Step 2: Run** → FAIL (undefined methods).

- [ ] **Step 3: Implement** `internal/codegen/go_package_index.go`

```go
package codegen

// goPackageAnalysisWith type-checks one Go-only main-module package inside the
// shipping declaration universe — retained cmd/go syntax + moduleImporter, no
// packages.Load — retaining a types.Info so the package's identifiers can join
// the symbol graph. Unlike shippingGoPackageWith's importer contract, the
// analysis is retained even when the package has type errors (navigation wants
// whatever resolved); callers that need a sound *types.Package check typeErrs.
func (m *Module) goPackageAnalysisWith(dir string, mi *moduleImporter) (*goPackageAnalysis, error) {
	dir = filepath.Clean(dir)
	m.mu.Lock()
	if a := m.goPkgAnalyses[dir]; a != nil {
		m.mu.Unlock()
		return a, nil
	}
	m.mu.Unlock()
	mi.seen[dir] = true
	defer delete(mi.seen, dir)
	sourcePackage, found, ready := m.targetSourcePackage(dir)
	if !ready || !found {
		return nil, fmt.Errorf("codegen: shipping source inventory has no Go-only package for %s", dir)
	}
	files, importPaths, err := m.companionGoSources(dir, nil)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("codegen: shipping Go-only package %s has no retained compiled source", dir)
	}
	asts := make([]*goast.File, len(files))
	for i, f := range files { asts[i] = f.file }
	if err := m.rejectExternalBackedgeImports(asts); err != nil {
		return nil, err
	}
	typeEnvironment, err := m.typeCheckEnvironmentForDir(dir)
	if err != nil {
		return nil, err
	}
	m.recordImports(dir, importPaths)
	a := &goPackageAnalysis{files: files, info: &types.Info{
		Defs: map[*goast.Ident]types.Object{}, Uses: map[*goast.Ident]types.Object{},
		Types: map[goast.Expr]types.TypeAndValue{}, Selections: map[*goast.SelectorExpr]*types.Selection{},
	}}
	config := types.Config{
		Importer: mi, Sizes: typeEnvironment.sizes, GoVersion: typeEnvironment.goVersion,
		Error: func(err error) {
			if typeErr, ok := err.(types.Error); ok {
				a.typeErrs = append(a.typeErrs, typeErr)
			}
		},
	}
	a.pkg = types.NewPackage(sourcePackage.pkgPath, sourcePackage.name)
	_ = types.NewChecker(&config, m.fset, a.pkg, a.info).Files(asts)
	if mi.cycleErr != nil {
		return nil, mi.cycleErr
	}
	m.mu.Lock()
	if m.goPkgAnalyses == nil {
		m.goPkgAnalyses = map[string]*goPackageAnalysis{}
	}
	m.goPkgAnalyses[dir] = a
	m.goPackageAnalyses++
	m.mu.Unlock()
	return a, nil
}
```
Then make `shippingGoPackageWith` = `a, err := m.goPackageAnalysisWith(dir, mi)`; on `err` return it; `if mi.sourceErr != nil { return nil, mi.sourceErr }`; `if err := typeErrorsAsSourceError(a.typeErrs); err != nil { return nil, err }`; then publish `m.pkgTypes[dir] = a.pkg` as today. (Behavior preserved: a with-errors package is still not published as an importable type package.)

Note: `mi.sourceErr` set while importing a broken gsx dependency does not discard the analysis (partial info is what navigation wants).

```go
// GoPackageIndex returns the identity-mapped symbol index of one Go-only
// package (see goPackageAnalysisWith). Fails soft on type errors.
func (m *Module) GoPackageIndex(dir string) (*sourceintel.Index, *types.Package, error) {
	m.analysisMu.Lock()
	defer m.analysisMu.Unlock()
	m.maybeRebuildFset()
	m.applyDirty()
	ext, err := m.externalImporter()
	if err != nil {
		return nil, nil, err
	}
	a, err := m.goPackageAnalysisWith(dir, newModuleImporter(m, ext))
	if err != nil {
		return nil, nil, err
	}
	m.mu.Lock()
	idx := a.index
	m.mu.Unlock()
	if idx != nil {
		return idx, a.pkg, nil
	}
	var mapped []sourceintel.MappedFile
	for _, f := range a.files {
		src, ok := m.currentSource(f.path)
		tf := m.fset.File(f.file.Pos())
		if !ok || tf == nil || tf.Size() != len(src) {
			continue // fail closed: bytes and retained syntax disagree
		}
		sm, err := sourceintel.IdentitySourceMap(f.path, len(src))
		if err != nil {
			continue
		}
		mapped = append(mapped, sourceintel.MappedFile{AST: f.file, TokenFile: tf, SourceMap: sm,
			SourceVersion: sourceintel.SourceVersion{Size: len(src), SHA256: sha256.Sum256(src)}})
	}
	idx = sourceintel.BuildIndex(a.info, mapped)
	m.mu.Lock()
	a.index = idx
	m.mu.Unlock()
	return idx, a.pkg, nil
}

// reverseDependencyGoDirs lists every Go-only main-module package whose
// transitive imports reach a gsx package. Edges come from the retained syntax
// (import specs), resolved through sourcePackageDir; the inventory must be ready
// (any Package/typesPackage call primes it).
func (m *Module) reverseDependencyGoDirs() ([]string, error) {
	m.mu.Lock()
	ready := m.sourceInventoryReady
	packages := make(map[string]projectSourcePackage, len(m.sourcePackages))
	maps.Copy(packages, m.sourcePackages)
	gsx := make(map[string]bool, len(m.sourceGsxDirs))
	maps.Copy(gsx, m.sourceGsxDirs)
	m.mu.Unlock()
	if !ready {
		return nil, errors.New("codegen: source inventory not ready")
	}
	importsOf := func(p projectSourcePackage) []string {
		var out []string
		for _, path := range p.compiledGoFiles {
			f := p.syntaxByFile[path]
			if f == nil { continue }
			for _, spec := range f.Imports {
				if ip, err := strconv.Unquote(spec.Path.Value); err == nil {
					if dir, ok := m.sourcePackageDir(ip); ok { out = append(out, dir) }
				}
			}
		}
		return out
	}
	memo := map[string]bool{}
	var reaches func(dir string, stack map[string]bool) bool
	reaches = func(dir string, stack map[string]bool) bool {
		if gsx[dir] { return true }
		if v, ok := memo[dir]; ok { return v }
		if stack[dir] { return false }
		stack[dir] = true
		p, ok := packages[dir]
		result := false
		if ok {
			for _, dep := range importsOf(p) {
				if reaches(dep, stack) { result = true; break }
			}
		}
		delete(stack, dir)
		memo[dir] = result
		return result
	}
	var out []string
	for dir := range packages {
		if !gsx[dir] && reaches(dir, map[string]bool{}) {
			out = append(out, dir)
		}
	}
	sort.Strings(out)
	return out, nil
}
```
Module fields: `goPkgAnalyses map[string]*goPackageAnalysis`, `goPackageAnalyses int` (+ `func (m *Module) goPackageAnalysisCount() int` test hook). Invalidation: in `invalidateScopeLocked` per-dir add `delete(m.goPkgAnalyses, d)`; in `invalidateConfiguredSourceStateLocked` and `rebuildFset` reset the map. `SetOverride`/`ClearOverride`/`RefreshDisk` already seed the closure with the dir, so `.go` edits in `cmd/` invalidate `cmd`'s analysis; a `.gsx` edit in `app/` reaches `cmd` through `importedBy` because `recordImports(dir, importPaths)` above records `cmd → app`.

- [ ] **Step 4: Run** `go test ./internal/codegen -run 'TestReverseDependencyGoPackageIndex|TestPackageSymbolIndex' -count=1` → PASS; also `go test ./internal/codegen -run 'Shipping|Importer|Invalidat' -count=1` (existing importer tests) → PASS.
- [ ] **Step 5: Commit** — `git add -A internal/codegen && git commit -m "codegen: reverse-dependency Go-only package analysis with types.Info + identity index"`

---

### Task 8: codegen — `Module.SymbolGraph`

**Files:**
- Modify: `internal/codegen/go_package_index.go` (add the method), test in `go_package_index_test.go` (same fixture, same Module open — extend `TestReverseDependencyGoPackageIndex` or add a subtest sharing the opened Module via a `t.Run` inside it)

**Interfaces:**
- Produces:
  ```go
  // SymbolGraph merges the retained analysis of every listed gsx package dir
  // (Package) with every reverse-dependency Go-only package (GoPackageIndex).
  // Un-analyzable dirs are skipped (partial graph), matching find-references'
  // historical tolerance. Returns an error only when nothing could be built.
  func (m *Module) SymbolGraph(gsxDirs []string) (*sourceintel.SymbolGraph, error)
  ```

- [ ] **Step 1: Write failing test** — inside `TestReverseDependencyGoPackageIndex` add after the existing assertions:
```go
	t.Run("module graph", func(t *testing.T) {
		g, err := m.SymbolGraph([]string{filepath.Join(root, "components"), filepath.Join(root, "app")})
		if err != nil {
			t.Fatal(err)
		}
		inputGSX := filepath.Join(root, "components", "input.gsx")
		inputSrc := "package components\n\ntype Size int\n\ncomponent Input(name string, size Size) {\n\t<input name={ name }/>\n}\n"
		key, _, ok := g.At(inputGSX, strings.Index(inputSrc, "Input"))
		if !ok || string(key) != "example.com/rd/components Input" {
			t.Fatalf("Input key = %q %v", key, ok)
		}
		byFile := map[string]int{}
		for _, s := range g.References(key) {
			byFile[filepath.Base(s.Path)]++
		}
		if byFile["page.gsx"] != 1 || byFile["main.go"] != 1 {
			t.Fatalf("Input refs = %v; want page.gsx tag + main.go value", byFile)
		}
		// main.go cursor → definitions in .gsx
		mainKey, _, ok := g.At(mainPath, strings.Index(src, "components.Size")+11)
		if !ok {
			t.Fatal("main.go Size cursor missed")
		}
		defs := g.Definitions(mainKey)
		if len(defs) != 1 || filepath.Base(defs[0].Path) != "input.gsx" {
			t.Fatalf("Size defs = %+v", defs)
		}
		// Home type: def in page.gsx, ref in main.go
		homeKey, _, _ := g.At(mainPath, strings.Index(src, "app.Home")+4)
		if d := g.Definitions(homeKey); len(d) != 1 || filepath.Base(d[0].Path) != "page.gsx" {
			t.Fatalf("Home defs = %+v", d)
		}
	})
```
- [ ] **Step 2: Run** → FAIL (undefined SymbolGraph).
- [ ] **Step 3: Implement**
```go
func (m *Module) SymbolGraph(gsxDirs []string) (*sourceintel.SymbolGraph, error) {
	g := sourceintel.NewSymbolGraph()
	added := 0
	var firstErr error
	for _, dir := range gsxDirs {
		pr, err := m.Package(dir)
		if err != nil || pr == nil || pr.SourceIndex == nil {
			if err != nil && firstErr == nil { firstErr = err }
			continue
		}
		g.AddIndex(pr.SourceIndex, sourceintel.NewKeyer(pr.Types))
		added++
	}
	goDirs, err := m.reverseDependencyGoDirs()
	if err != nil && firstErr == nil {
		firstErr = err
	}
	for _, dir := range goDirs {
		idx, pkg, err := m.GoPackageIndex(dir)
		if err != nil {
			if firstErr == nil { firstErr = err }
			continue
		}
		g.AddIndex(idx, sourceintel.NewKeyer(pkg))
		added++
	}
	if added == 0 && firstErr != nil {
		return nil, firstErr
	}
	return g, nil
}
```
- [ ] **Step 4: Run** the test → PASS. `go vet ./internal/codegen`.
- [ ] **Step 5: Commit** — `git commit -am "codegen: Module.SymbolGraph — gsx packages + reverse-dependency Go packages"`

---

### Task 9: gen — `AnalyzeModule` returns the graph; LSP interface + fakes

**Files:**
- Modify: `gen/lsp.go` (`AnalyzeModule`, `adaptPackageResult`, delete `crossRefKeyForFunc` if now unused), `internal/lsp/server.go` (interface, cache fields, `invalidateNonSymbolModuleIndexes`), `internal/lsp/analysis.go` (delete `CrossRef`, `NavRef`, `Package.CrossIndex/NavIndex`), test fakes: `internal/lsp/references_cache_test.go` (`moduleRefsAnalyzer`), `internal/lsp/definition_test.go` (`analyzedLSPModule` copies `CrossIndex` — remove), every other test constructing `Package{CrossIndex:…}` (grep).
- Test: `gen/references_crosspkg_test.go` (rewrite assertion)

**Interfaces:**
- Produces:
  ```go
  // lsp.Analyzer
  AnalyzeModule(dir string, override map[string][]byte) (*sourceintel.SymbolGraph, error)
  // Server fields
  moduleGraph      *sourceintel.SymbolGraph
  moduleGraphValid bool
  ```

- [ ] **Step 1:** Rewrite `gen/references_crosspkg_test.go`'s assertion: `g, err := newLSPAnalyzer(config{}, nil).AnalyzeModule(componentsDir, nil)`; find `key, _, ok := g.At(<root>/components/input.gsx, strings.Index(src,"Input"))`; count `g.References(key)` by base filename → `post.gsx: 1`, `use.go: 1`. Also add: `g.At(<root>/use.go, offset of "components.Input"+11)` yields the same key.
- [ ] **Step 2:** `go test ./gen -run TestAnalyzeModuleCrossPkg -count=1` → compile FAIL.
- [ ] **Step 3:** Implement `gen/lsp.go`:
```go
func (a lspAnalyzer) AnalyzeModule(dir string, _ map[string][]byte) (*sourceintel.SymbolGraph, error) {
	root, modPath, err := moduleRoot(dir)
	if err != nil { return nil, err }
	dirs, err := discoverDirs([]string{root})
	if err != nil { return nil, err }
	merged := resolveConfigBestEffort(dir, a.optCfg, a.warnw)
	m, _, err := a.module(root, modPath, merged)
	if err != nil { return nil, err }
	return m.SymbolGraph(dirs)
}
```
Delete phases 2–5, `crossRefKeyForFunc` (confirm no other user with `gopls check` / grep), and the `cross`/`nav` conversions in `adaptPackageResult`. In `internal/lsp/server.go` change the interface + fields (`moduleRefs []CrossRef` → `moduleGraph *sourceintel.SymbolGraph`, `moduleRefsValid` → `moduleGraphValid`) and update `invalidateNonSymbolModuleIndexes`. In `analysis.go` delete `CrossRef`, `NavRef`, and the two fields. Update the fake analyzer:
```go
type moduleRefsAnalyzer struct {
	moduleCalls int
	moduleGraph *sourceintel.SymbolGraph
	moduleErr   error
	pkg         *Package
	overrides   []map[string][]byte
}
func (a *moduleRefsAnalyzer) AnalyzeModule(dir string, override map[string][]byte) (*sourceintel.SymbolGraph, error) { a.moduleCalls++; a.overrides = append(a.overrides, override); return a.moduleGraph, a.moduleErr }
```
Fix every other fake `Analyzer` in `internal/lsp/*_test.go` and `gen/*_test.go` to the new signature (grep `AnalyzeModule(`).
- [ ] **Step 4:** `go build ./... && go vet ./gen ./internal/lsp` (tests in `internal/lsp` that assert references still fail until Task 10 — acceptable; `go test ./gen -run TestAnalyzeModuleCrossPkg -count=1` → PASS).
- [ ] **Step 5: Commit** — `git commit -am "gen/lsp: AnalyzeModule publishes the module SymbolGraph; drop CrossRef/NavRef"`

---

### Task 10: lsp — references over the graph

**Files:**
- Rewrite: `internal/lsp/references.go`
- Modify: `internal/lsp/references_cache_test.go`, `internal/lsp/variant_nav_test.go`, `internal/lsp/source_text_test.go`, `internal/lsp/watched_files_test.go` (wherever `moduleRefs`/`CrossRef` were built)
- Test: `internal/lsp/references_graph_test.go` (new; uses `analyzedLSPModule` ONCE — extend the existing 3-file module in `definition_source_index_test.go` if it already declares a Go type + sibling `.go`; otherwise one new Module open here, justified: it replaces `variant_nav_test`'s CrossRef path)

**Interfaces:**
- Produces:
  ```go
  func (s *Server) moduleSymbolGraph(sources *requestSourceSnapshot, dir string) *sourceintel.SymbolGraph // lazily refreshed; nil on failure
  func packageSymbolGraph(pkg *Package) *sourceintel.SymbolGraph                                        // per-package fallback
  func symbolAt(graph *sourceintel.SymbolGraph, path, text string, offset int) (sourceintel.ObjectKey, bool) // MatchesSource-gated
  ```

- [ ] **Step 1: Write failing tests**

Update `TestReferencesIncludesAllVariantDecls` (`variant_nav_test.go`): build `g := sourceintel.NewSymbolGraph(); g.AddIndex(pr.SourceIndex, sourceintel.NewKeyer(pr.Types))`, pass `&moduleRefsAnalyzer{moduleGraph: g, pkg: <adapted pkg>}`; keep the assertions (`icon_a.gsx`, `page.gsx` present). Update `references_cache_test.go` fakes to return a graph built from a tiny synthetic index (use `parseAndCheckMappedFile`-style helper: it lives in `sourceintel` tests, so add a small local helper `synthGraph(t, path, source string)` in `references_cache_test.go` that parses `source` as Go with an identity map and returns the graph + a use offset) — the cache tests only assert call counts/invalidation, so any keyable symbol suffices.

New `internal/lsp/references_graph_test.go`:
```go
func TestReferencesOverSymbolGraph(t *testing.T) {
	const page = "package page\n\ntype Model struct{ N int }\n\nfunc (m Model) inc() Model { m.N++; return m }\n\ncomponent Card(title string) {\n\t<p>{ title }</p>\n}\n\ncomponent Page(m Model) {\n\t<main><Card title=\"x\"/>{ m.inc().N }</main>\n}\n"
	const helper = "package page\n\nfunc use() { _ = Model{}.inc(); _ = Card }\n"
	pkg, path := analyzedLSPModule(t, map[string]string{"page/page.gsx": page, "page/helper.go": helper}, "page/page.gsx")
	g := sourceintel.NewSymbolGraph()
	g.AddIndex(pkg.SourceIndex, sourceintel.NewKeyer(pkg.Types))
	a := &moduleRefsAnalyzer{moduleGraph: g, pkg: pkg}
	uri := pathToURI(path)
	helperURI := pathToURI(filepath.Join(filepath.Dir(path), "helper.go"))
	cases := []struct {
		name          string
		fromURI, text string
		needle        string
		wantURIs      map[string]int // base filename → count (includeDeclaration=true)
	}{
		{"type from gsx decl", uri, page, "Model struct", map[string]int{"page.gsx": 4, "helper.go": 1}},
		{"method from helper.go", helperURI, helper, "inc()", map[string]int{"page.gsx": 2, "helper.go": 1}},
		{"component from tag cursor", uri, page, "<Card", map[string]int{"page.gsx": 2, "helper.go": 1}},
		{"param from attr cursor", uri, page, "title=", map[string]int{"page.gsx": 3}},
		{"field", uri, page, "N int", map[string]int{"page.gsx": 3}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			off := strings.Index(tc.text, tc.needle)
			if strings.HasPrefix(tc.needle, "<") { off++ }
			pos := positionForByteOffset(tc.text, off, encUTF16)
			out := drive(t, a, initFrame()+didOpenFrame(uri, page)+didOpenFrame(helperURI, helper)+refsFrame(7, tc.fromURI, pos.Line, pos.Character)+exitFrame())
			got := map[string]int{}
			for _, loc := range referenceLocations(t, out, 7) { // helper: parse result of id 7 into []Location
				got[filepath.Base(uriToPath(loc.URI))]++
			}
			if !maps.Equal(got, tc.wantURIs) {
				t.Fatalf("got %v want %v\n%s", got, tc.wantURIs, out)
			}
		})
	}
}
```
Write `referenceLocations(t, out, id)` next to `definitionLocation` in `definition_source_index_test.go` (same JSON parsing, `[]Location`). Adjust the expected counts to the true count if a case is off by the receiver/param occurrences you did not enumerate — but every case MUST include the cross-file hit (`helper.go` or the tag/attr site); those are the behaviors under test.

- [ ] **Step 2: Run** `go test ./internal/lsp -run 'TestReferences' -count=1` → FAIL.

- [ ] **Step 3: Implement** `internal/lsp/references.go`:
```go
package lsp

import (
	"encoding/json"
	"path/filepath"

	"github.com/gsxhq/gsx/internal/sourceintel"
)

// handleReferences answers textDocument/references from the module symbol
// graph: the occurrence under the cursor (any indexed .gsx or .go file) names
// an ObjectKey; the reply is every definition (when includeDeclaration) and
// reference span of that key, module-wide. When the module graph cannot be
// built, the retained per-package index answers same-package requests.
func (s *Server) handleReferences(f frame) error {
	var p referenceParams
	if err := json.Unmarshal(f.Params, &p); err != nil {
		return s.reply(f.ID, []Location{})
	}
	if !s.diskViewValid {
		return s.reply(f.ID, []Location{})
	}
	path := uriToPath(p.TextDocument.URI)
	sources := s.sourceSnapshot()
	text, ok := sources.sourceString(path)
	if !ok {
		return s.reply(f.ID, []Location{})
	}
	off := byteOffsetForPosition(text, p.Position.Line, p.Position.Character, s.enc)
	graph := s.moduleSymbolGraph(sources, filepath.Dir(path))
	key, found := symbolAt(graph, path, text, off)
	if !found {
		graph = packageSymbolGraph(s.pkgs[filepath.Dir(path)])
		key, found = symbolAt(graph, path, text, off)
	}
	if !found {
		return s.reply(f.ID, []Location{})
	}
	locs := make([]Location, 0)
	if p.Context.IncludeDeclaration {
		for _, span := range graph.Definitions(key) {
			if location, ok := sources.locationForSpan(span); ok {
				locs = append(locs, location)
			}
		}
	}
	for _, span := range graph.References(key) {
		if location, ok := sources.locationForSpan(span); ok {
			locs = append(locs, location)
		}
	}
	return s.reply(f.ID, locs)
}

func (s *Server) moduleSymbolGraph(sources *requestSourceSnapshot, dir string) *sourceintel.SymbolGraph {
	if !s.moduleGraphValid {
		if g, err := s.analyzer.AnalyzeModule(dir, sources.openGSXOverrides()); err == nil {
			s.moduleGraph = g
			s.moduleGraphValid = true
		}
	}
	return s.moduleGraph
}

func packageSymbolGraph(pkg *Package) *sourceintel.SymbolGraph {
	if pkg == nil || pkg.SourceIndex == nil {
		return nil
	}
	g := sourceintel.NewSymbolGraph()
	g.AddIndex(pkg.SourceIndex, sourceintel.NewKeyer(pkg.Types))
	return g
}

// symbolAt resolves the cursor to a key, fail-closed on stale bytes: the graph
// must have been built from exactly text.
func symbolAt(graph *sourceintel.SymbolGraph, path, text string, offset int) (sourceintel.ObjectKey, bool) {
	if graph == nil || !graph.MatchesSource(path, []byte(text)) {
		return "", false
	}
	key, _, ok := graph.At(path, offset)
	return key, ok
}
```
Delete `identifyCrossRef`, `refreshModuleReferences`. `locationForSpan` needs `sources.sourceString(span.Path)` — for `.go` spans that reads disk/open buffer, as it does for `.gsx`.

- [ ] **Step 4: Run** `go test ./internal/lsp -count=1` → PASS (all).
- [ ] **Step 5: Commit** — `git add -A internal/lsp && git commit -m "lsp: find-references over the module symbol graph — every symbol, .gsx and .go cursors"`

---

### Task 11: lsp — definition from `.go` over the graph

**Files:**
- Modify: `internal/lsp/definition.go` (`handleGoDefinition`, delete `posCoversCursor` if unused)
- Test: extend `internal/lsp/references_graph_test.go` with `TestGoDefinitionOverSymbolGraph` (same fixture strings; one more `analyzedLSPModule` call is acceptable only if the previous test's `pkg` cannot be shared — share it: turn both into subtests of one `TestSymbolGraphHandlers` that opens the module once).

- [ ] **Step 1: Write failing test** (subtest):
```go
	t.Run("go definition", func(t *testing.T) {
		for _, tc := range []struct{ needle, wantFile string }{
			{"Model{}", "page.gsx"}, // type declared in .gsx
			{"inc()", "page.gsx"},   // method declared in .gsx
			{"Card }", "page.gsx"},  // component
			{"use()", "helper.go"},  // graph answers .go decls too (uniform rule)
		} {
			off := strings.Index(helper, tc.needle)
			pos := positionForByteOffset(helper, off, encUTF16)
			out := drive(t, a, initFrame()+didOpenFrame(uri, page)+didOpenFrame(helperURI, helper)+definitionFrame(9, helperURI, pos)+exitFrame())
			got := definitionLocation(t, out, 9)
			if got == nil || filepath.Base(uriToPath(got.URI)) != tc.wantFile {
				t.Fatalf("%s: got %+v want %s\n%s", tc.needle, got, tc.wantFile, out)
			}
		}
	})
```
`definitionLocation` must accept both a single `Location` and a one-element `[]Location` result; extend it if it does not.

- [ ] **Step 2: Run** → FAIL.
- [ ] **Step 3: Implement**:
```go
// handleGoDefinition answers definition for a .go cursor from the module
// symbol graph (any dir in the module — a Go-only package importing gsx
// packages included). The graph answers with everything it knows: .gsx and
// .go declarations alike; the editor merges this with gopls' answer.
func (s *Server) handleGoDefinition(f frame, path string, sources *requestSourceSnapshot) error {
	var p textDocumentPositionParams
	if err := json.Unmarshal(f.Params, &p); err != nil {
		return s.reply(f.ID, nil)
	}
	text, ok := sources.sourceString(path)
	if !ok {
		return s.reply(f.ID, nil)
	}
	off := byteOffsetForPosition(text, p.Position.Line, p.Position.Character, s.enc)
	graph := s.moduleSymbolGraph(sources, filepath.Dir(path))
	key, found := symbolAt(graph, path, text, off)
	if !found {
		graph = packageSymbolGraph(s.pkgs[filepath.Dir(path)])
		key, found = symbolAt(graph, path, text, off)
	}
	if !found {
		return s.reply(f.ID, nil)
	}
	var locs []Location
	for _, span := range graph.Definitions(key) {
		if location, ok := sources.locationForSpan(span); ok {
			locs = append(locs, location)
		}
	}
	switch len(locs) {
	case 0:
		return s.reply(f.ID, nil)
	case 1:
		return s.reply(f.ID, locs[0])
	default:
		return s.reply(f.ID, locs)
	}
}
```
Remove `posCoversCursor` and `lineStartOffset` if no remaining callers (`gopls check -severity=hint internal/lsp/definition.go`).
- [ ] **Step 4: Run** `go test ./internal/lsp -count=1` → PASS.
- [ ] **Step 5: Commit** — `git commit -am "lsp: go-to-definition from .go over the module symbol graph"`

---

### Task 12: gen end-to-end + the template-repo probe

**Files:**
- Modify: `gen/references_e2e_test.go` (`TestReferencesTagCursorEmpty` → `TestReferencesTagCursor` expecting hits), `gen/references_crosspkg_e2e_test.go` (`crossPkgModule` gains `cmd/main.go` importing `components` and a `.go`-only cursor case), `gen/go_definition_e2e_test.go` (add: `gd` from `cmd/main.go` on `components.Input` → `components/input.gsx`; `gd` from a `.go` file on a `.gsx`-declared *type*)

- [ ] **Step 1:** Write the e2e cases (they drive `runLSP` like their neighbours; open the `.go` file with `"languageId": "go"`). Extend `crossPkgModule` with:
  `must("cmd/main.go", "package main\n\nimport \"example.com/x/components\"\n\nfunc main() { _ = components.Input }\n")`.
- [ ] **Step 2:** `go test ./gen -run 'TestReferences|TestGoToGsxDefinition|TestAnalyzeModuleCrossPkg' -count=1` → the new cases FAIL until… they should PASS already if Tasks 5–11 are complete; if any fails, fix the implementation, not the test.
- [ ] **Step 3: Live probe against the user's template repo** (evidence for the original report). Build `go build -o /private/tmp/claude-501/-Users-jackieli-personal-gsxhq-gsx/937e7755-96c8-4473-9305-b90a0b68bf4f/scratchpad/gsx-graph ./cmd/gsx` and run the stdio probe script at `/private/tmp/claude-501/-Users-jackieli-personal-gsxhq-gsx/937e7755-96c8-4473-9305-b90a0b68bf4f/scratchpad/probe.py` with that binary (`python3 probe.py <binary>` from the scratchpad dir). Expected now: `refs smoke.gsx 5 6` (type `Smoke`) lists `pages.go` (`Smoke` field), `home.gsx` (`Smoke{}`) and the decl; `def pages.go 18 2` (`Home`) → `home.gsx`; `def pages.go 19 2` → `smoke.gsx`; `refs smoke.gsx 7 21` (`Page`) → its decl (+ any callers). Paste the probe output into the commit message body.
- [ ] **Step 4: Commit** — `git commit -am "gen: e2e — references/definition for .gsx-declared types from .go and tag cursors"`

---

### Task 13: docs + ROADMAP + spec follow-ups

**Files:**
- Modify: `docs/guide/editor.md` (rows 76–77 and the paragraph at ~87), `docs/ROADMAP.md` (line 39 status cell, lines 539–540, 768), spec (`## Non-goals` add: build-tag variant *loser* declarations of non-component Go objects are not indexed — go/types assigns them no object; component variants are covered via `ComponentDecls`).

- [ ] **Step 1:** editor.md rows become:
  `| Go to definition | Every Go symbol and component, .gsx ↔ .go, module-wide (Go-only packages importing gsx packages included). |`
  `| Find references | Every Go symbol declared or used in .gsx — types, funcs, methods, fields, params, locals, components (tag sites and attribute bindings), pipe filters — from .gsx or .go cursors, module-wide. |`
  and the ~87 paragraph: keep the "external packages" caveat only for *definitions inside* external packages; references to external symbols list module-local sites. Keep it to two sentences (docs must be concise).
- [ ] **Step 2:** ROADMAP: update line 39 wording ("+ find-references" → "+ module-wide symbol graph (references/definition for every authored Go symbol)"), lines 539–540 → `- [x] **Find-references / .go definition** — module symbol graph (`sourceintel.SymbolGraph`): every Go symbol authored in `.gsx`, `.go` siblings and Go-only importer packages; tag/attr/pipe edges. Spec: docs/superpowers/specs/2026-08-17-module-symbol-graph-design.md`, line 768 remove the "references cover project components" deferral (keep "external/non-project *definitions*").
- [ ] **Step 3:** `git commit -am "docs: module symbol graph — references/definition coverage"`

---

### Task 14: final gate + adversarial review

- [ ] **Step 1:** `gopls check -severity=hint` on every modified file; `make lint`.
- [ ] **Step 2:** `make ci` (authoritative, ~4–5 min). Its exit code decides; paste the tail.
- [ ] **Step 3:** Independent adversarial review (fresh subagent, builds throwaway probes, not diff-reading): must at minimum probe (a) a `.go` buffer edit in a reverse-dep dir followed by `gr` — stale-graph gating; (b) generic component instantiation refs; (c) a private component tag; (d) a package with type errors still answering references; (e) a symlinked module root (darwin `/tmp` → `/private/tmp`) — `.go` span paths vs the LSP's `sourcePath()` normalization; (f) memory: `SymbolGraph` must not retain `*ast.File`s (`assertNoRetainedSourceInputs`-style check like `TestIndexDoesNotRetainASTOrSourceBytes`).
- [ ] **Step 4:** Fix findings (each with a test), re-run `make ci`, then finishing-a-development-branch.
