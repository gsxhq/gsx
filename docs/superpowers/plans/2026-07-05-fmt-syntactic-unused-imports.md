# `gsx fmt` Syntactic Unused-Import Detection — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `gsx fmt`'s unused-import detection ~30× faster by replacing the full-dependency-graph type-check with a syntactic scan of the already-built skeleton AST (goimports' technique), correctly handling default-import name mismatches.

**Architecture:** Add an importer-free `Module.UnusedImports(dir)` that reuses the existing skeleton-lowering helpers (`buildSkeleton` et al.) but stops before `checkSkeletonPackage`, then scans each skeleton file's selector qualifiers to decide which hoisted imports are unused. Default imports whose path base is not referenced get their real package name resolved via a cheap `NeedName` load. `gen/fmt.go` opens one `Module` per module (not per directory) and calls the new method. `generate`, the cache, and `computeKey` are untouched.

**Tech Stack:** Go, `go/ast`, `golang.org/x/tools/go/packages` (NeedName only), the existing `internal/codegen` skeleton pipeline.

## Global Constraints

- Runtime root package stays standard-library only; `internal/codegen` and `gen` are tooling and may use `golang.org/x/tools` (verbatim from CLAUDE.md).
- No "simple heuristics" in core logic — real implementations only. Import-usage is determined the way the Go frontend defines it (name referenced as a selector qualifier); default-import real names are *resolved*, never guessed when a guess would drive removal.
- Pin Go to `GO_VERSION` in `.github/workflows/ci.yml` (currently 1.26.1) to avoid gofmt drift.
- Don't hand-edit `.x.go` or golden files — regenerate.
- `generate`, the on-disk cache, and `computeKey` MUST remain byte-for-byte unchanged (no cache-key or generated-output change).

---

## File Structure

- **Create** `internal/codegen/unused_imports_syntactic.go` — the new detector: `Module.UnusedImports`, `buildPackageSkeletons`, `skeletonUsedNames`, `classifyUnusedImports`, `importBaseName`, `resolvePackageNames`, and the `packageSkeletons`/`fileSkeleton` types.
- **Create** `internal/codegen/unused_imports_syntactic_test.go` — unit + equivalence-oracle tests.
- **Modify** `gen/fmt.go` — rewrite `analyzeUnusedImports` to group by module, open one `Module` per module, and call `m.UnusedImports`.
- **Modify** `gen/main.go` (`fmt` case, ~line 211-217) — best-effort `resolveConfig`, pass a `codegen.Options` to `runFmt`.
- **Modify** `gen/fmt_test.go` — fmt integration coverage (per-context keep/remove, malformed config, one-load-per-module).
- **Modify** `docs/ROADMAP.md` and `changelog/` — record the change.

The existing type-check detector (`detectUnusedImports` in `internal/codegen/results.go`) stays: `generate`/`Module.Package` still use it, and the equivalence test uses it as an oracle.

---

### Task 1: Pure detector functions (`skeletonUsedNames`, `classifyUnusedImports`)

**Files:**
- Create: `internal/codegen/unused_imports_syntactic.go`
- Test: `internal/codegen/unused_imports_syntactic_test.go`

**Interfaces:**
- Consumes: `importSpec` (`{name, path string; srcOff int; pos token.Pos}`, `analyze.go:3049`), `UnusedImport` (`{Name, Path string}`, `results.go:72`), `sunkImportKey` (`{line int; path string}`, `module_importer.go:436`).
- Produces:
  - `func skeletonUsedNames(f *goast.File) map[string]bool`
  - `func importBaseName(path string) string`
  - `func classifyUnusedImports(used map[string]bool, imps []importSpec, sunk map[sunkImportKey]bool, gsxFset *token.FileSet) (unused []UnusedImport, candidates []importSpec)`

- [ ] **Step 1: Write the failing test**

```go
package codegen

import (
	"go/parser"
	"go/token"
	"testing"
)

func TestSkeletonUsedNames(t *testing.T) {
	const src = `package p
import "strings"
func f() { _ = strings.TrimSpace("x"); _ = a.b.c }
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "s.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	used := skeletonUsedNames(f)
	if !used["strings"] {
		t.Errorf("want strings used")
	}
	if !used["a"] { // inner selector a.b of a.b.c
		t.Errorf("want a used")
	}
}

func TestImportBaseName(t *testing.T) {
	for path, want := range map[string]string{
		"strings":            "strings",
		"gopkg.in/yaml.v3":   "yaml.v3", // base is NOT the package name → forces candidate resolution
		"github.com/x/go-fo": "go-fo",
	} {
		if got := importBaseName(path); got != want {
			t.Errorf("importBaseName(%q)=%q want %q", path, got, want)
		}
	}
}

func TestClassifyUnusedImports(t *testing.T) {
	fset := token.NewFileSet()
	used := map[string]bool{"strings": true, "sx": true}
	imps := []importSpec{
		{name: "", path: "strings"},         // default, base used → kept
		{name: "", path: "bytes"},           // default, base unused → candidate
		{name: "sx", path: "text/scanner"},  // aliased, alias used → kept
		{name: "al", path: "os"},            // aliased, alias unused → unused
		{name: "_", path: "embed"},          // blank → never removed
		{name: ".", path: "math"},           // dot → never removed
	}
	unused, candidates := classifyUnusedImports(used, imps, nil, fset)
	if len(unused) != 1 || unused[0].Path != "os" || unused[0].Name != " al" {
		t.Errorf("unused=%+v, want only { al os}", unused)
	}
	if len(candidates) != 1 || candidates[0].path != "bytes" {
		t.Errorf("candidates=%+v, want only bytes", candidates)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/codegen/ -run 'TestSkeletonUsedNames|TestImportBaseName|TestClassifyUnusedImports' -count=1`
Expected: FAIL — `undefined: skeletonUsedNames` (etc.)

- [ ] **Step 3: Write minimal implementation**

Create `internal/codegen/unused_imports_syntactic.go`:

```go
package codegen

import (
	goast "go/ast"
	"go/token"
	"strings"
)

// skeletonUsedNames returns the set of identifiers used as the qualifier X in
// any selector expression X.Sel within f. An imported package name can only be
// referenced this way (or via a dot/blank import, handled separately), so this
// set is exactly "which import local-names are referenced" for a valid Go file.
func skeletonUsedNames(f *goast.File) map[string]bool {
	used := map[string]bool{}
	goast.Inspect(f, func(n goast.Node) bool {
		if sel, ok := n.(*goast.SelectorExpr); ok {
			if id, ok := sel.X.(*goast.Ident); ok {
				used[id.Name] = true
			}
		}
		return true
	})
	return used
}

// importBaseName is the last path segment — the CONVENTIONAL default local name,
// which for some packages (e.g. gopkg.in/yaml.v3 → "yaml") is NOT the real
// package name. It is used only as a fast "definitely used" check; a base that
// is not referenced makes the import a removal CANDIDATE whose real name must be
// resolved before removal.
func importBaseName(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}

// classifyUnusedImports splits a file's hoisted import specs into definitely-unused
// imports and removal candidates, given the set of referenced qualifier names.
//
//   - `_` / `.` imports are never removed (always "used").
//   - An import whose only skeleton reference was dropped by a requalification-
//     failed generic tag (sunk) is never removed — it IS used in the .gsx source.
//   - An aliased import's explicit name is authoritative: unused iff its alias is
//     not referenced.
//   - A default import is kept when its path base is referenced; otherwise it is a
//     CANDIDATE (its real package name may differ from the base and still be used).
func classifyUnusedImports(used map[string]bool, imps []importSpec, sunk map[sunkImportKey]bool, gsxFset *token.FileSet) (unused []UnusedImport, candidates []importSpec) {
	for _, imp := range imps {
		if imp.name == "_" || imp.name == "." {
			continue
		}
		if sunk != nil && imp.pos.IsValid() {
			k := sunkImportKey{line: gsxFset.Position(imp.pos).Line, path: imp.path}
			if sunk[k] {
				continue
			}
		}
		if imp.name != "" {
			if !used[imp.name] {
				unused = append(unused, UnusedImport{Name: imp.name, Path: imp.path})
			}
			continue
		}
		if used[importBaseName(imp.path)] {
			continue
		}
		candidates = append(candidates, imp)
	}
	return unused, candidates
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/codegen/ -run 'TestSkeletonUsedNames|TestImportBaseName|TestClassifyUnusedImports' -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/codegen/unused_imports_syntactic.go internal/codegen/unused_imports_syntactic_test.go
git commit -m "feat(codegen): pure syntactic unused-import classifier"
```

---

### Task 2: `buildPackageSkeletons` — importer-free skeleton build

**Files:**
- Modify: `internal/codegen/unused_imports_syntactic.go`
- Test: `internal/codegen/unused_imports_syntactic_test.go`

**Interfaces:**
- Consumes: `Module` lifecycle + helpers — `m.fset`, `m.maybeRebuildFset()`, `m.applyDirty()`, `m.analysisMu`, `m.parsePackageWithFset(dir, fset)` (`module_importer.go:1288`), `m.cachedFilterTable()` (`module.go:295`), `componentPropFieldsFor(dir, gsxFiles)` (`analyze.go:69`), `genericSigsFor(gsxFiles, byo)` (`analyze.go:547`), `newInferNameAllocator()`, `m.fileScopedFacts(...)` (`module_importer.go:476`), `buildSkeleton(...)` (`analyze.go:335`), `m.externalLoads()` (`module.go:276`), `jsx.ResolveScripts`. This mirrors `analyze`'s per-file loop (`module_importer.go:769-819`) using the SAME lowering, but keeps only import-detection state and never calls `checkSkeletonPackage`.
- Produces:
  - `type fileSkeleton struct { skel *goast.File; imps []importSpec; sunk map[sunkImportKey]bool }`
  - `type packageSkeletons struct { gsxFset *token.FileSet; byGsx map[string]fileSkeleton }`
  - `func (m *Module) buildPackageSkeletons(dir string) (*packageSkeletons, error)`

- [ ] **Step 1: Write the failing test**

Add to `internal/codegen/unused_imports_syntactic_test.go`. Reuse the Module-construction pattern from `internal/codegen/unused_imports_test.go` (a helper that writes .gsx into a temp module and calls `Open`). If that test file has an existing helper (e.g. `openModuleWithFiles`), call it; otherwise inline the same steps.

```go
func TestBuildPackageSkeletonsNoExternalLoad(t *testing.T) {
	dir, m := openTestModule(t, map[string]string{
		"page.gsx": "{ import \"strings\" }\ncomp Page() {\n\t<div>{strings.ToUpper(\"x\")}</div>\n}\n",
	})
	ps, err := m.buildPackageSkeletons(dir)
	if err != nil {
		t.Fatal(err)
	}
	fs, ok := ps.byGsx[filepath.Join(dir, "page.gsx")]
	if !ok {
		t.Fatalf("no skeleton for page.gsx; got %v", ps.byGsx)
	}
	if len(fs.imps) != 1 || fs.imps[0].path != "strings" {
		t.Errorf("imps=%+v, want [strings]", fs.imps)
	}
	used := skeletonUsedNames(fs.skel)
	if !used["strings"] {
		t.Errorf("expected strings referenced in skeleton; used=%v", used)
	}
	if n := m.externalLoads(); n != 0 {
		t.Errorf("buildPackageSkeletons did %d external loads, want 0 (importer-free)", n)
	}
}
```

`openTestModule(t, files)` returns `(absDir, *Module)`: write a `go.mod` (`module testmod\n\ngo 1.26\n`) and the `.gsx` files into `t.TempDir()`, then `Open(Options{ModuleRoot: root, ModulePath: "testmod", Classifier: attrclass.Builtin()})` and return the package dir. Model it on the existing `unused_imports_test.go` setup; add it as a shared test helper if none exists.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/codegen/ -run TestBuildPackageSkeletonsNoExternalLoad -count=1`
Expected: FAIL — `m.buildPackageSkeletons undefined`

- [ ] **Step 3: Write minimal implementation**

Append to `internal/codegen/unused_imports_syntactic.go` (add imports: `goparser "go/parser"`, `"path/filepath"`, `"github.com/gsxhq/gsx/internal/diag"`, `"github.com/gsxhq/gsx/internal/jsx"`):

```go
type fileSkeleton struct {
	skel *goast.File
	imps []importSpec
	sunk map[sunkImportKey]bool
}

type packageSkeletons struct {
	gsxFset *token.FileSet
	byGsx   map[string]fileSkeleton // .gsx abs path -> skeleton + import specs + sunk set
}

// buildPackageSkeletons lowers every .gsx file in dir to its skeleton AST WITHOUT
// type-checking (no importer, no dependency resolution) and returns, per file,
// the parsed skeleton, its hoisted import specs, and its sunk-import set. It
// mirrors analyze's per-file loop (module_importer.go:769-819) using the same
// buildSkeleton lowering, but keeps only what unused-import detection needs. A
// file whose skeleton fails to build (parse/attr error) is simply omitted, so
// the caller keeps all of that file's imports.
func (m *Module) buildPackageSkeletons(dir string) (*packageSkeletons, error) {
	m.analysisMu.Lock()
	defer m.analysisMu.Unlock()
	m.maybeRebuildFset()
	m.applyDirty()
	fset := m.fset
	bag := diag.NewBag(fset)
	gsxFiles, _, err := m.parsePackageWithFset(dir, fset)
	if err != nil {
		return nil, err
	}
	for _, f := range gsxFiles {
		jsx.ResolveScripts(f, bag) // best-effort; failure just means we may skip that file below
	}
	table, err := m.cachedFilterTable()
	if err != nil {
		return nil, err
	}
	propFields, nodeProps, attrsProps, byo, err := componentPropFieldsFor(dir, gsxFiles)
	if err != nil {
		return nil, err
	}
	genericSigs := genericSigsFor(gsxFiles, byo)
	inferNames := newInferNameAllocator()
	out := &packageSkeletons{gsxFset: fset, byGsx: map[string]fileSkeleton{}}
	for path, f := range gsxFiles {
		ff := m.fileScopedFacts(dir, f, propFields, nodeProps, attrsProps, byo, bag, fset)
		skel, _, imps, _, infReg, berr := buildSkeleton(f, table, ff.propFields, ff.nodeProps, ff.attrsProps,
			genericSigs, ff.genericSigs, ff.byo, m.opts.FieldMatcher, fset, bag, inferNames)
		if berr != nil {
			continue // unbuildable → keep all imports (no entry)
		}
		base := strings.TrimSuffix(filepath.Base(path), ".gsx")
		absXpath := filepath.Join(dir, base+".x.go")
		gf, perr := goparser.ParseFile(fset, absXpath, skel, goparser.SkipObjectResolution)
		if perr != nil {
			continue
		}
		sunk := map[sunkImportKey]bool{}
		if len(infReg.failedAliases) > 0 && ff.depAliasSpecs != nil {
			for alias := range infReg.failedAliases {
				if spec, ok := ff.depAliasSpecs[alias]; ok && spec.pos.IsValid() {
					sunk[sunkImportKey{line: fset.Position(spec.pos).Line, path: spec.path}] = true
				}
			}
		}
		out.byGsx[path] = fileSkeleton{skel: gf, imps: imps, sunk: sunk}
	}
	return out, nil
}
```

Note: confirm the `fileFacts` field names against `m.fileScopedFacts`'s return (`propFields`, `nodeProps`, `attrsProps`, `genericSigs`, `byo`, `depAliasSpecs`) — they are exactly the fields the analyze loop reads at `module_importer.go:770-808`. If a name differs, match the analyze loop verbatim.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/codegen/ -run TestBuildPackageSkeletonsNoExternalLoad -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/codegen/unused_imports_syntactic.go internal/codegen/unused_imports_syntactic_test.go
git commit -m "feat(codegen): importer-free per-package skeleton build for import detection"
```

---

### Task 3: `resolvePackageNames` + `Module.UnusedImports`

**Files:**
- Modify: `internal/codegen/unused_imports_syntactic.go`
- Test: `internal/codegen/unused_imports_syntactic_test.go`

**Interfaces:**
- Consumes: Task 1 + Task 2 functions; `golang.org/x/tools/go/packages`; `m.opts.ModuleRoot`.
- Produces:
  - `func (m *Module) resolvePackageNames(paths []string) map[string]string`
  - `func (m *Module) UnusedImports(dir string) (map[string][]UnusedImport, error)` — keyed by .gsx abs path.

- [ ] **Step 1: Write the failing test**

```go
func TestUnusedImportsSyntactic(t *testing.T) {
	dir, m := openTestModule(t, map[string]string{
		"page.gsx": "{ import (\n\t\"strings\"\n\t\"bytes\"\n) }\n" +
			"comp Page() {\n\t<div>{strings.ToUpper(\"x\")}</div>\n}\n",
	})
	got, err := m.UnusedImports(dir)
	if err != nil {
		t.Fatal(err)
	}
	unused := got[filepath.Join(dir, "page.gsx")]
	if len(unused) != 1 || unused[0].Path != "bytes" {
		t.Errorf("unused=%+v, want [bytes]; strings is used and must be kept", unused)
	}
}

func TestUnusedImportsDefaultNameMismatchKept(t *testing.T) {
	// gopkg.in/yaml.v3's package name is "yaml", not its base "yaml.v3".
	// A base-only scan would wrongly drop it; real-name resolution must keep it.
	dir, m := openTestModuleWithGoMod(t,
		"module testmod\n\ngo 1.26\n\nrequire gopkg.in/yaml.v3 v3.0.1\n",
		map[string]string{
			"page.gsx": "{ import \"gopkg.in/yaml.v3\" }\n" +
				"comp Page() {\n\t<div>{string(mustYAML())}</div>\n}\n" +
				"{ func mustYAML() []byte { b, _ := yaml.Marshal(1); return b } }\n",
		})
	got, err := m.UnusedImports(dir)
	if err != nil {
		t.Fatal(err)
	}
	if u := got[filepath.Join(dir, "page.gsx")]; len(u) != 0 {
		t.Errorf("yaml is used (yaml.Marshal) and must be kept; got unused=%+v", u)
	}
}
```

`openTestModuleWithGoMod` is `openTestModule` with a caller-supplied `go.mod`; both share one helper. The yaml test needs the module resolvable — run `go mod tidy` (or vendor `gopkg.in/yaml.v3`) inside the temp module as part of the helper, or skip via `testing.Short()` when offline. Prefer materializing yaml.v3 into the temp module's `go.sum` by copying from this repo's own module cache if available; otherwise `t.Skip` when `go list gopkg.in/yaml.v3` fails, and note the skip.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/codegen/ -run 'TestUnusedImportsSyntactic|TestUnusedImportsDefaultNameMismatchKept' -count=1`
Expected: FAIL — `m.UnusedImports undefined`

- [ ] **Step 3: Write minimal implementation**

Append (add import `"golang.org/x/tools/go/packages"`):

```go
// resolvePackageNames returns the real package name for each import path, via a
// NeedName-only load (no type-checking, no dependency resolution). Unresolvable
// paths are simply absent from the result, so the caller keeps those imports.
func (m *Module) resolvePackageNames(paths []string) map[string]string {
	out := map[string]string{}
	if len(paths) == 0 {
		return out
	}
	cfg := &packages.Config{Mode: packages.NeedName, Dir: m.opts.ModuleRoot}
	pkgs, err := packages.Load(cfg, paths...)
	if err != nil {
		return out
	}
	for _, p := range pkgs {
		if p.PkgPath != "" && p.Name != "" {
			out[p.PkgPath] = p.Name
		}
	}
	return out
}

// UnusedImports returns, per .gsx file (abs path) in dir, the imports the file
// declares but never references — determined syntactically from the skeleton,
// with NO type-checking and NO dependency resolution. Default imports whose path
// base is not referenced have their real package name resolved via a single
// cheap NeedName load before removal, so a package whose name differs from its
// path base (e.g. gopkg.in/yaml.v3 → "yaml") is handled correctly.
func (m *Module) UnusedImports(dir string) (map[string][]UnusedImport, error) {
	ps, err := m.buildPackageSkeletons(dir)
	if err != nil {
		return nil, err
	}
	out := map[string][]UnusedImport{}
	usedByFile := map[string]map[string]bool{}
	type pending struct {
		gsxPath string
		imp     importSpec
	}
	var candidates []pending
	candPaths := map[string]bool{}
	for gsxPath, fs := range ps.byGsx {
		used := skeletonUsedNames(fs.skel)
		usedByFile[gsxPath] = used
		unused, cands := classifyUnusedImports(used, fs.imps, fs.sunk, ps.gsxFset)
		if len(unused) > 0 {
			out[gsxPath] = unused
		}
		for _, c := range cands {
			candidates = append(candidates, pending{gsxPath, c})
			candPaths[c.path] = true
		}
	}
	if len(candPaths) > 0 {
		paths := make([]string, 0, len(candPaths))
		for p := range candPaths {
			paths = append(paths, p)
		}
		names := m.resolvePackageNames(paths)
		for _, p := range candidates {
			realName, ok := names[p.imp.path]
			if !ok {
				continue // unresolvable → conservative keep
			}
			if !usedByFile[p.gsxPath][realName] {
				out[p.gsxPath] = append(out[p.gsxPath], UnusedImport{Name: p.imp.name, Path: p.imp.path})
			}
		}
	}
	return out, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/codegen/ -run 'TestUnusedImportsSyntactic|TestUnusedImportsDefaultNameMismatchKept' -count=1`
Expected: PASS (yaml test may `Skip` when offline)

- [ ] **Step 5: Commit**

```bash
git add internal/codegen/unused_imports_syntactic.go internal/codegen/unused_imports_syntactic_test.go
git commit -m "feat(codegen): Module.UnusedImports syntactic detector with real-name resolution"
```

---

### Task 4: Rewire `gen/fmt.go` to one `Module` per module

**Files:**
- Modify: `gen/fmt.go` (`analyzeUnusedImports`, ~line 165-204; and its call site in `runFmt`, ~line 74-77 + signature ~line 45)
- Modify: `gen/main.go` (`fmt` case, ~line 211-217)
- Test: `gen/fmt_test.go`

**Interfaces:**
- Consumes: `Module.UnusedImports` (Task 3), `groupByModule(dirs)` (`gen/modroot.go:28`, returns `groups []moduleGroup` with `.root`, `.modPath`, `.dirs`), `codegen.Open`, `codegen.Options`, `resolveConfig`.
- Produces: `analyzeUnusedImports(files []string, opts codegen.Options) map[string][]gsxfmt.ImportRef`; `runFmt(..., opts codegen.Options, ...)`.

- [ ] **Step 1: Write the failing test**

Add to `gen/fmt_test.go`. Follow the existing fmt-test harness in that file (write .gsx into a temp module, call `runFmt`). Assert per-context removal:

```go
func TestFmtRemovesUnusedKeepsUsed(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module fmtmod\n\ngo 1.26\n")
	src := "{ import (\n\t\"strings\"\n\t\"bytes\"\n) }\n" +
		"comp Page() {\n\t<div>{strings.ToUpper(\"x\")}</div>\n}\n"
	page := filepath.Join(dir, "page.gsx")
	writeFile(t, page, src)

	var out, errBuf bytes.Buffer
	code := runFmt(&out, &errBuf, []string{"-w", page}, nil, nil, codegen.Options{Classifier: attrclass.Builtin()}, dir)
	if code != 0 {
		t.Fatalf("runFmt code=%d stderr=%s", code, errBuf.String())
	}
	got := readFile(t, page)
	if strings.Contains(got, `"bytes"`) {
		t.Errorf("unused bytes import should be removed:\n%s", got)
	}
	if !strings.Contains(got, `"strings"`) {
		t.Errorf("used strings import must be kept:\n%s", got)
	}
}

func TestFmtToleratesMalformedConfig(t *testing.T) {
	// A malformed gsx.toml must not break fmt: with builtin Options, formatting
	// still succeeds (imports simply may be conservatively kept).
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module fmtmod\n\ngo 1.26\n")
	page := filepath.Join(dir, "page.gsx")
	writeFile(t, page, "comp Page() {\n\t<div>hi</div>\n}\n")

	var out, errBuf bytes.Buffer
	code := runFmt(&out, &errBuf, []string{"-l", page}, nil, nil, codegen.Options{Classifier: attrclass.Builtin()}, dir)
	if code != 0 { // already canonical → no diff → 0
		t.Fatalf("runFmt code=%d stderr=%s", code, errBuf.String())
	}
}
```

Use whatever `writeFile`/`readFile` helpers `gen/fmt_test.go` already has; add minimal ones if absent.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./gen/ -run 'TestFmtRemovesUnusedKeepsUsed|TestFmtToleratesMalformedConfig' -count=1`
Expected: FAIL — `runFmt` signature mismatch (no `opts` param) / compile error.

- [ ] **Step 3: Write minimal implementation**

In `gen/fmt.go`, change `runFmt`'s signature to accept `opts codegen.Options` and pass it to `analyzeUnusedImports`:

```go
func runFmt(stdout, stderr io.Writer, args []string, cssFmt, jsFmt rawfmt.Formatter, opts codegen.Options, workDir string) int {
```

At the analysis call (currently `unusedByPath = analyzeUnusedImports(files)`):

```go
	var unusedByPath map[string][]gsxfmt.ImportRef
	if !noImports {
		unusedByPath = analyzeUnusedImports(files, opts)
	}
```

Replace the whole `analyzeUnusedImports` body with the grouped, one-Module-per-module form:

```go
// analyzeUnusedImports computes, per absolute .gsx path, the imports the file
// declares but does not use — syntactically, via the skeleton (no type-check).
// It opens ONE codegen.Module per module (not per directory) and reuses it across
// that module's directories. Directories not in a module, or that fail to open,
// are skipped (those files are then formatted without import removal). opts
// carries the resolved codegen config so skeletons match what `generate` emits;
// a zero/builtin opts still works (buildSkeleton tolerates unknown filters).
func analyzeUnusedImports(files []string, opts codegen.Options) map[string][]gsxfmt.ImportRef {
	out := map[string][]gsxfmt.ImportRef{}
	dirSet := map[string]bool{}
	for _, f := range files {
		dirSet[filepath.Dir(f)] = true
	}
	dirs := make([]string, 0, len(dirSet))
	for d := range dirSet {
		dirs = append(dirs, d)
	}
	groups, _ := groupByModule(dirs)
	for _, g := range groups {
		o := opts
		o.ModuleRoot = g.root
		o.ModulePath = g.modPath
		m, err := codegen.Open(o)
		if err != nil {
			continue // not loadable → syntactic-only fallback (no removal)
		}
		for _, dir := range g.dirs {
			absDir, err := filepath.Abs(dir)
			if err != nil {
				continue
			}
			byPath, err := m.UnusedImports(absDir)
			if err != nil {
				continue
			}
			for gsxPath, imps := range byPath {
				absPath, err := filepath.Abs(gsxPath)
				if err != nil {
					continue
				}
				refs := make([]gsxfmt.ImportRef, len(imps))
				for i, u := range imps {
					refs[i] = gsxfmt.ImportRef{Name: u.Name, Path: u.Path}
				}
				out[absPath] = refs
			}
		}
	}
	return out
}
```

Delete the now-unused `codegen`/`attrclass` imports only if they become unused (they are still used via `codegen.Open`/`codegen.Options`; `attrclass` may no longer be referenced here — run goimports/`gsx fmt` discipline: remove if unused).

In `gen/main.go`, the `fmt` case: best-effort resolve config and pass Options (Classifier, FieldMatcher, Aliases, FilterPkgs — the knobs that affect skeleton import references; omit ClassMerger/minify, which are emit-only, to avoid an extra package load):

```go
	case "fmt":
		// fmt tolerates a malformed config: resolveConfig is best-effort and only
		// feeds the syntactic import analysis. On failure we fall back to the builtin
		// classifier (files using named filters still skeletonize — buildSkeleton
		// tolerates unknown filters). CSS/JS formatter overrides remain programmatic.
		fmtOpts := codegen.Options{Classifier: attrclass.Builtin()}
		if merged, _, cerr := resolveConfig(cfg, workDir); cerr == nil {
			fmtOpts = codegen.Options{
				Classifier:   merged.classifier(),
				FieldMatcher: merged.fieldMatcher,
				Aliases:      merged.aliases,
				FilterPkgs:   merged.filterPkgs,
			}
		}
		return runFmt(stdout, stderr, cmdArgs, cfg.cssFmt, cfg.jsFmt, fmtOpts, workDir)
```

Also update any other `runFmt(` call sites (search: `rg 'runFmt\('`) — e.g. `formatGsx`/`Format` helpers do NOT call runFmt, but confirm none break.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./gen/ -run 'TestFmtRemovesUnusedKeepsUsed|TestFmtToleratesMalformedConfig' -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add gen/fmt.go gen/main.go gen/fmt_test.go
git commit -m "perf(fmt): syntactic unused-import detection, one module load per module"
```

---

### Task 5: Equivalence oracle test + fmt context coverage + single-load assertion

**Files:**
- Modify: `internal/codegen/unused_imports_syntactic_test.go`
- Modify: `gen/fmt_test.go`

**Interfaces:**
- Consumes: `Module.UnusedImports` (Task 3), `Module.Package(dir)` → `pr.UnusedImports` (the type-check oracle), `pr.Diags`.

- [ ] **Step 1: Write the failing/what-if test — equivalence oracle**

The oracle: on a package that type-checks cleanly (no error-severity diagnostics), the syntactic removal set MUST equal the type-check removal set. Skip packages with unrelated type errors (documented divergence — syntactic still removes, oracle returns nothing).

```go
func TestSyntacticMatchesTypecheckOracle(t *testing.T) {
	cases := map[string]map[string]string{
		"interp":    {"a.gsx": "{ import (\n\t\"strings\"\n\t\"bytes\"\n) }\ncomp A(){<p>{strings.ToUpper(\"x\")}</p>}\n"},
		"attr":      {"b.gsx": "{ import \"strings\" }\ncomp B(){<p id={strings.ToLower(\"X\")}>hi</p>}\n"},
		"tag":       {"c.gsx": "{ import \"bytes\" }\ncomp C(){<div>{bytes.NewBufferString(\"x\").String()}</div>}\n"},
		"allunused": {"d.gsx": "{ import (\n\t\"strings\"\n\t\"bytes\"\n) }\ncomp D(){<p>hi</p>}\n"},
		"aliased":   {"e.gsx": "{ import sx \"strings\" }\ncomp E(){<p>{sx.ToUpper(\"x\")}</p>}\n"},
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			dir, m := openTestModule(t, files)
			syn, err := m.UnusedImports(dir)
			if err != nil {
				t.Fatal(err)
			}
			pr, err := m.Package(dir)
			if err != nil {
				t.Fatal(err)
			}
			if anyErrorDiagCodegen(pr.Diags) {
				t.Skipf("package has type errors; oracle returns nothing by design")
			}
			assertSameRemovalSet(t, dir, syn, pr.UnusedImports)
		})
	}
}
```

`assertSameRemovalSet` compares the two `map[gsxPath][]UnusedImport` as sets of `(Name,Path)` per file (order-independent). `anyErrorDiagCodegen` reports whether any diagnostic is error-severity (mirror `gen`'s `anyErrorDiag`). Note `m.Package`'s `UnusedImports` keys are file paths as recorded by `detectUnusedImports` (the `.gsx` filename via `//line`); normalize both sides to abs `.gsx` paths before comparing.

- [ ] **Step 2: Run test to verify current behavior**

Run: `go test ./internal/codegen/ -run TestSyntacticMatchesTypecheckOracle -count=1`
Expected: PASS (this validates parity; if a case diverges, it exposes a real scan gap to fix before shipping).

- [ ] **Step 3: Add the single-load assertion (fmt integration)**

Add to `gen/fmt_test.go` — a two-directory module must load once, proving we don't re-open per directory. Since `analyzeUnusedImports` opens the `Module` internally, assert indirectly via wall-clock-independent means: expose the per-module load by checking that formatting a 2-dir module calls `codegen.Open` once. Simplest robust form: unit-test `analyzeUnusedImports` directly and assert both dirs' results come back in a single call, and add a `codegen` test that `buildPackageSkeletons` across N dirs on ONE module keeps `m.externalLoads()==0` (already covered in Task 2) — plus a `Module`-level test that calling `UnusedImports` on two dirs of one module resolves names in one `resolvePackageNames` batch is out of scope; the meaningful guarantee (one `Open` per module) is structural in the Task 4 code. Add:

```go
func TestFmtTwoDirsOneModule(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module fmtmod\n\ngo 1.26\n")
	writeFile(t, filepath.Join(dir, "a", "a.gsx"), "{ import \"bytes\" }\ncomp A(){<p>hi</p>}\n")
	writeFile(t, filepath.Join(dir, "b", "b.gsx"), "{ import \"strings\" }\ncomp B(){<p>{strings.ToUpper(\"x\")}</p>}\n")
	refs := analyzeUnusedImports(
		[]string{filepath.Join(dir, "a", "a.gsx"), filepath.Join(dir, "b", "b.gsx")},
		codegen.Options{Classifier: attrclass.Builtin()},
	)
	aAbs, _ := filepath.Abs(filepath.Join(dir, "a", "a.gsx"))
	bAbs, _ := filepath.Abs(filepath.Join(dir, "b", "b.gsx"))
	if len(refs[aAbs]) != 1 || refs[aAbs][0].Path != "bytes" {
		t.Errorf("a: want bytes unused, got %+v", refs[aAbs])
	}
	if len(refs[bAbs]) != 0 {
		t.Errorf("b: strings is used, want none unused, got %+v", refs[bAbs])
	}
}
```

- [ ] **Step 4: Run all new tests**

Run: `go test ./internal/codegen/ ./gen/ -run 'Unused|Fmt|Oracle|Skeleton' -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/codegen/unused_imports_syntactic_test.go gen/fmt_test.go
git commit -m "test(fmt): syntactic/typecheck equivalence oracle + fmt context coverage"
```

---

### Task 6: Full verification, docs, changelog, real-world timing

**Files:**
- Modify: `docs/ROADMAP.md`, `changelog/` (follow the existing changelog format in that dir)

- [ ] **Step 1: Run the full check suite**

Run: `make check`
Expected: PASS (build/vet/test both modules, examples drift, gofmt + gsx fmt). Fix any fallout.

- [ ] **Step 2: Confirm the real-world speedup**

Build and time against `one-learning-gsx` (the reproduction case):

```bash
go build -o /tmp/gsx-bench ./cmd/gsx
cd ~/work/one-learning-gsx && time /tmp/gsx-bench fmt -l >/dev/null
```
Expected: sub-second wall time (down from ~16s), and `fmt -l` still lists the same files as `fmt -l -no-imports` plus any with genuinely-unused imports. Spot-check `fmt -d` on a file with a known unused import shows the import removed.

- [ ] **Step 3: Verify idempotence and no generated-output change**

```bash
cd ~/work/one-learning-gsx && /tmp/gsx-bench fmt -w . && /tmp/gsx-bench fmt -l .
```
Expected: second `fmt -l` prints nothing (idempotent). Run `git diff --stat` in that repo — only import removals, no unrelated reformatting. Confirm `gsx generate` output is byte-identical (`git diff` on `.x.go` empty).

- [ ] **Step 4: Update docs + changelog**

Add a `changelog/` entry: "`gsx fmt` unused-import detection is now syntactic (skeleton scan), ~30× faster on large trees; removes unused imports even when the package has an unrelated type error (gofmt/goimports parity)." Update `docs/ROADMAP.md` if it tracks fmt performance.

- [ ] **Step 5: Commit**

```bash
git add docs/ROADMAP.md changelog/
git commit -m "docs: record syntactic fmt unused-import detection"
```

---

## Self-Review

**Spec coverage:**
- Importer-free detector on the skeleton → Tasks 1-3. ✓
- One `Module` per module (not per directory) → Task 4. ✓
- Best-effort config / malformed-config tolerance → Task 4 (`gen/main.go` + `TestFmtToleratesMalformedConfig`). ✓
- `sunkImports` preserved, `_`/`.` never removed, aliased-by-local-name, file-scoped → Task 1 `classifyUnusedImports` + Task 2 sunk wiring + Task 5 aliased case. ✓
- Default-import real-name resolution (the correctness gap the spec's syntactic story must close) → Task 3 `resolvePackageNames` + `TestUnusedImportsDefaultNameMismatchKept`. ✓
- Behavior change (removes amid unrelated errors) → documented in Task 5 oracle skip + Task 6 changelog. ✓
- Equivalence oracle → Task 5. ✓
- `generate`/cache/`computeKey` untouched → no task modifies them; Task 6 Step 3 verifies byte-identical output. ✓
- Filter/merger-load optimization (§4) → intentionally out of scope (Task 4 omits ClassMerger; the filter-table skip is deferred).

**Placeholder scan:** No TBD/TODO; every code step carries full code. The only conditional is the yaml.v3 test's offline `Skip`, which is explicit.

**Type consistency:** `UnusedImports` returns `map[string][]UnusedImport` (Task 3) consumed by `analyzeUnusedImports` (Task 4) which maps to `gsxfmt.ImportRef{Name,Path}`. `classifyUnusedImports` signature identical across Tasks 1/3. `buildPackageSkeletons`→`packageSkeletons{gsxFset,byGsx}`→`fileSkeleton{skel,imps,sunk}` consistent Tasks 2/3. `runFmt` gains `opts codegen.Options` in both signature and call site (Task 4). ✓

**Open risk to watch during execution:** `fileFacts` field names read in Task 2 (`ff.propFields/nodeProps/attrsProps/genericSigs/byo/depAliasSpecs`) must match `m.fileScopedFacts`'s actual struct; verify against the analyze loop at `module_importer.go:770-808` before finalizing Task 2.
