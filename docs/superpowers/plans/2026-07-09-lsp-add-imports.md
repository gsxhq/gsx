# LSP add-import — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The gsx language server adds missing imports — folded into `source.organizeImports`, and offered as a `quickfix` on the `undefined: X` diagnostic — without ever scanning the filesystem.

**Architecture:** Detection is type-driven and free: `analyze` already type-checks a skeleton containing the user's `fmt.Sprintf(hello)`, so a missing qualifier is a `SelectorExpr` whose root `*ast.Ident` has no entry in `info.Uses` or `info.Defs`. Resolution is two map lookups (a baked stdlib `name → []path` table, plus the module's already-loaded dependency graph), with symbol-based disambiguation only when a name is ambiguous — and it runs **only** in user-triggered code-action handlers, never on the analysis hot path. Insertion uses `astutil.AddNamedImport`; ordering stays with goimports `FormatOnly`. gsx writes no import-manipulation logic of its own.

**Tech Stack:** Go 1.26.1, `golang.org/x/tools` (`go/ast/astutil`, `imports`, `go/packages` — all already required), stdlib `go/types`, `go/importer`.

## Global Constraints

- Pin Go to **1.26.1** (`GO_VERSION` in `.github/workflows/ci.yml`).
- Work in the worktree `/Users/jackieli/personal/gsxhq/gsx/.claude/worktrees/lsp-add-imports` on branch `worktree-lsp-add-imports`. **Never commit to `main`.**
- **Never use `git stash`** — the stash stack is shared across worktrees. Use `git diff > /tmp/x.patch` + `git apply -R` to temporarily revert.
- **LSP performance is a hard requirement.** No `packages.Load` / `go list` / `go/importer` export-data read / filesystem scan / lock acquisition is reachable from `Module.Package()`. `main`'s CLAUDE.md states: "`golang.org/x/tools/go/packages.Load` is expensive. Make sure you understand the performance implications before calling it."
- **No "simple heuristics" in core logic — real implementations only.** In particular: never guess a package name from its import path base. `go/types` *fabricates* a placeholder package named after the path base for any import its importer never loaded (`math/rand/v2` → `"v2"`). **Always gate on `imp.Complete()` before trusting `Name()`.** This was PR #64's Critical — a used import got deleted.
- The runtime (root `gsx` package) is standard-library only. `internal/codegen`, `internal/gsxfmt`, `internal/lsp`, `gen` are tooling and may use `golang.org/x/tools`. `go.mod`/`go.sum` must not change.
- Prefer unexported identifiers unless they cross a package boundary.
- **Don't hand-edit generated files.** `internal/codegen/stdlibindex_gen.go` is produced by `go:generate`.
- `gsx` the binary name collides with Ghostscript — always `go run ./cmd/gsx …`.
- Before merging: `make ci` (authoritative, uncached) and `make lint`. Inner loop: `make check`.
- Any formatter change ships a **fmt-corpus case** (`internal/gsxfmt/testdata/cases/*.txtar`, `input.gsx` + `fmt.golden`). Regenerate with `go test ./internal/gsxfmt -run TestFmtCorpus -update`, then verify without `-update`.

## Design Vocabulary (locked — use these exact names)

```go
// internal/codegen (results.go)
type MissingImport struct {
	Name   string         // the undefined qualifier, e.g. "fmt"
	Symbol string         // the selector on it, e.g. "Sprintf" — enables disambiguation
	Pos    token.Position // the qualifier's .gsx position (via GSXFset)
}
// PackageResult gains:
//   MissingImports map[string][]MissingImport   // .gsx abs path -> missing qualifiers

// internal/codegen (add_imports.go, new)
func missingFromSkeletons(byGsx map[string]fileSkeleton, gsxFset *token.FileSet, info *types.Info) map[string][]MissingImport
func (m *Module) ResolveImportCandidates(name, symbol string) []string

// internal/gsxfmt
type FormatOptions struct {
	Unused  []ImportRef
	Add     []ImportRef   // NEW: imports to insert (astutil.AddNamedImport)
	Width   int
	CSSFmt  rawfmt.Formatter
	JSFmt   rawfmt.Formatter
	Reorder bool
}

// internal/lsp
type MissingImport struct{ Name, Symbol string; Pos token.Position }
// Package gains: MissingImports map[string][]MissingImport
// Analyzer gains: ResolveImport(dir, name, symbol string) []string
const quickFixKind = "quickfix"
```

Pipeline inside `FormatWith`: **remove unused → add → reorder → normalize → print.** Removing first then adding is safe and order-independent in practice (a path cannot be simultaneously unused and a missing qualifier), and `astutil.AddNamedImport` is a no-op when the path is already present.

## File Structure

| File | Responsibility |
|---|---|
| `internal/codegen/results.go` (modify) | `MissingImport` type; `PackageResult.MissingImports`. |
| `internal/codegen/add_imports.go` (new) | Detection (`missingFromSkeletons`) + resolution (`ResolveImportCandidates`, `depGraphNames`, symbol disambiguation) + the hot-path counter. |
| `internal/codegen/stdlibindex_gen.go` (new, generated) | `stdlibIndex map[string][]string`. |
| `internal/codegen/mkstdlibindex/main.go` (new) | `go:generate` program that emits the table from `go list std`. |
| `internal/codegen/module_importer.go` (modify) | `analyzed.missingImports`; call `missingFromSkeletons` beside the existing `unusedFromSkeletons`. |
| `internal/codegen/module.go` (modify) | `res.MissingImports = a.missingImports`. |
| `internal/gsxfmt/imports.go` (modify) | `addImports` via `astutil.AddNamedImport`; target-chunk selection. |
| `internal/gsxfmt/gsxfmt.go` (modify) | `FormatOptions.Add`; wire into `FormatWith`. |
| `internal/lsp/protocol.go` (modify) | `codeActionContext.Diagnostics`; `quickFixKind`; capability lists both kinds; `CodeAction.Diagnostics`. |
| `internal/lsp/analysis.go` (modify) | `lsp.MissingImport`; `Package.MissingImports`. |
| `internal/lsp/server.go` (modify) | `Analyzer.ResolveImport`. |
| `internal/lsp/codeaction.go` (modify) | organizeImports gains unambiguous adds; new quickfix handler. |
| `gen/lsp.go` (modify) | `adaptPackageResult` converts `MissingImports`; `lspAnalyzer.ResolveImport`. |

---

### Task 1: `MissingImport` detection in `analyze`

**Files:**
- Modify: `internal/codegen/results.go` (add type + `PackageResult` field)
- Create: `internal/codegen/add_imports.go`
- Modify: `internal/codegen/module_importer.go` (`analyzed` struct ~line 651; the `unusedFromSkeletons` call ~line 1236)
- Modify: `internal/codegen/module.go` (`Package()`, beside `res.UnusedImports = a.unusedImports`)
- Test: `internal/codegen/add_imports_test.go`

**Interfaces:**
- Consumes: `fileSkeleton{skel *goast.File; imps []importSpec; sunk map[sunkImportKey]bool}` and the `skelByGsx map[string]fileSkeleton` that `analyze` already builds (`module_importer.go` ~line 811/899, added by PR #64); `a.info *types.Info`; `a.gsxFset`.
- Produces: `MissingImport`, `PackageResult.MissingImports`, `missingFromSkeletons`. Tasks 2, 4, 5 depend on these names.

**Background:** `analyze` type-checks a skeleton containing the user's Go. A missing qualifier is exactly a `*goast.SelectorExpr` whose `X` is an `*goast.Ident` with **no entry** in `info.Uses` **and** no entry in `info.Defs`. Verified: a local variable resolves to a `var`, an imported package to a `*types.PkgName`, and only a genuinely undefined qualifier has no object at all. Do **not** parse the `"undefined: fmt"` error string.

- [ ] **Step 1: Write the failing test**

Create `internal/codegen/add_imports_test.go`:

```go
package codegen

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// newMissingModule writes a temp module whose single .gsx package is `src`.
func newMissingModule(t *testing.T, src string) (*Module, string) {
	t.Helper()
	dir := t.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	mod := "module example.com/u\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => " + repoRoot + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(mod), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.gsx"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Open(Options{ModuleRoot: dir, ModulePath: "example.com/u"})
	if err != nil {
		t.Skipf("Open: %v", err)
	}
	return m, dir
}

// missingNames returns the sorted "name.Symbol" pairs Package() reports for a.gsx.
func missingNames(t *testing.T, m *Module, dir string) []string {
	t.Helper()
	pr, err := m.Package(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, mi := range pr.MissingImports[filepath.Join(dir, "a.gsx")] {
		out = append(out, mi.Name+"."+mi.Symbol)
	}
	sort.Strings(out)
	return out
}

// TestMissingImportsDetectsUndefinedQualifier: the motivating case.
func TestMissingImportsDetectsUndefinedQualifier(t *testing.T) {
	m, dir := newMissingModule(t, "package u\n\nvar hello = \"hi\"\n\nvar xx = <p>{ fmt.Sprintf(hello) }</p>\n")
	got := missingNames(t, m, dir)
	if len(got) != 1 || got[0] != "fmt.Sprintf" {
		t.Fatalf("missing = %v, want [fmt.Sprintf]", got)
	}
}

// TestMissingImportsIgnoresLocalsAndImports: a selector on a LOCAL variable and a
// selector on an IMPORTED package must never be reported missing.
func TestMissingImportsIgnoresLocalsAndImports(t *testing.T) {
	src := "package u\n\nimport \"strings\"\n\n" +
		"type T struct{ Field int }\n\n" +
		"component C(v T) {\n\t<p>{ strings.ToUpper(\"x\") }{ v.Field }</p>\n}\n"
	m, dir := newMissingModule(t, src)
	if got := missingNames(t, m, dir); len(got) != 0 {
		t.Fatalf("missing = %v, want none (locals + imported pkgs are not missing)", got)
	}
}

// TestMissingImportsCapturesSymbol: the selector name is what disambiguates `rand`.
func TestMissingImportsCapturesSymbol(t *testing.T) {
	m, dir := newMissingModule(t, "package u\n\nvar xx = <p>{ rand.IntN(3) }</p>\n")
	got := missingNames(t, m, dir)
	if len(got) != 1 || got[0] != "rand.IntN" {
		t.Fatalf("missing = %v, want [rand.IntN]", got)
	}
}

// TestMissingImportsPositionIsGsx: Pos must point into the .gsx file, not the skeleton.
func TestMissingImportsPositionIsGsx(t *testing.T) {
	m, dir := newMissingModule(t, "package u\n\nvar xx = <p>{ fmt.Sprintf(\"x\") }</p>\n")
	pr, err := m.Package(dir)
	if err != nil {
		t.Fatal(err)
	}
	mis := pr.MissingImports[filepath.Join(dir, "a.gsx")]
	if len(mis) != 1 {
		t.Fatalf("want 1 missing, got %d", len(mis))
	}
	if !strings.HasSuffix(mis[0].Pos.Filename, "a.gsx") {
		t.Fatalf("Pos.Filename = %q, want .gsx path", mis[0].Pos.Filename)
	}
	if mis[0].Pos.Line != 3 {
		t.Fatalf("Pos.Line = %d, want 3", mis[0].Pos.Line)
	}
}

// TestMissingImportsAcrossPositions: a qualifier undefined in an attribute, a
// <script>, and an { if } condition is reported in every case.
func TestMissingImportsAcrossPositions(t *testing.T) {
	for name, src := range map[string]string{
		"interp":    "package u\n\nvar xx = <p>{ fmt.Sprint(1) }</p>\n",
		"attr":      "package u\n\nvar xx = <p id={ fmt.Sprint(1) }>hi</p>\n",
		"ifcond":    "package u\n\ncomponent C() {\n\t{ if fmt.Sprint(1) == \"1\" }\n\t\t<p>y</p>\n\t{ end }\n}\n",
	} {
		t.Run(name, func(t *testing.T) {
			m, dir := newMissingModule(t, src)
			got := missingNames(t, m, dir)
			if len(got) != 1 || got[0] != "fmt.Sprint" {
				t.Fatalf("missing = %v, want [fmt.Sprint]", got)
			}
		})
	}
}
```

Add `"strings"` to the test file's imports.

**Implementer note:** the exact `{ if … }` syntax above may not match gsx. Before writing the test, confirm the control-flow spelling against a real case in `internal/corpus/testdata/cases/**/*.txtar` and use that. Do not invent syntax.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/codegen/ -run TestMissingImports -count=1 -timeout 300s -v`
Expected: FAIL — `pr.MissingImports undefined`.

- [ ] **Step 3: Write minimal implementation**

In `internal/codegen/results.go`, add the type next to `UnusedImport` and the field to `PackageResult` next to `UnusedImports`:

```go
// MissingImport is a qualifier used in a .gsx file that resolves to nothing: no
// local, no import. Name is the qualifier ("fmt"), Symbol is the selector on it
// ("Sprintf") — Symbol is what lets an ambiguous name like `rand` be resolved to
// the one candidate that actually exports it. Pos is the qualifier's position in
// the .gsx source.
//
// Deliberately UNRESOLVED: turning a Name into an import path may read package
// export data, which must never happen on the Package() hot path. The LSP
// resolves it in a user-triggered code-action handler via
// Module.ResolveImportCandidates.
type MissingImport struct {
	Name   string
	Symbol string
	Pos    token.Position
}
```

```go
	// MissingImports lists, per .gsx file path, the qualifiers the file uses that
	// resolve to nothing — candidates for an added import. Unresolved by design;
	// see MissingImport.
	MissingImports map[string][]MissingImport
```

Create `internal/codegen/add_imports.go`:

```go
package codegen

import (
	goast "go/ast"
	"go/token"
	"go/types"
)

// missingFromSkeletons finds, per .gsx file, every qualifier used in a selector
// expression that go/types could not resolve to anything.
//
// The test is exact, not a heuristic: go/types records an object in info.Uses for
// an imported package (a *types.PkgName) and for a local variable, and in
// info.Defs for a declaration. An identifier at the root of a selector with NO
// entry in either map resolved to nothing — it is an undefined qualifier, which
// is what a missing import looks like. The alternative, scraping "undefined: fmt"
// out of type-error message text, is a heuristic and is not used.
//
// Positions are reported in the .gsx source: the skeleton carries //line
// directives, so gsxFset maps a skeleton position back to its .gsx origin, the
// same way diagnostics already do.
//
// Pure: walks ASTs analyze already parsed. No IO, no lock, no packages.Load, no
// importer call. Safe on the Package() hot path.
func missingFromSkeletons(byGsx map[string]fileSkeleton, gsxFset *token.FileSet, info *types.Info) map[string][]MissingImport {
	if info == nil {
		return nil
	}
	out := map[string][]MissingImport{}
	for gsxPath, fs := range byGsx {
		var found []MissingImport
		seen := map[string]bool{} // name+symbol+line: one report per distinct site
		goast.Inspect(fs.skel, func(n goast.Node) bool {
			se, ok := n.(*goast.SelectorExpr)
			if !ok {
				return true
			}
			id, ok := se.X.(*goast.Ident)
			if !ok {
				return true
			}
			if _, used := info.Uses[id]; used {
				return true // an imported package, a local, a field...
			}
			if _, defined := info.Defs[id]; defined {
				return true
			}
			pos := gsxFset.Position(id.Pos())
			key := id.Name + "." + se.Sel.Name + ":" + pos.String()
			if seen[key] {
				return true
			}
			seen[key] = true
			found = append(found, MissingImport{Name: id.Name, Symbol: se.Sel.Name, Pos: pos})
			return true
		})
		if len(found) > 0 {
			out[gsxPath] = found
		}
	}
	return out
}
```

Add `"go/token"` to `results.go`'s imports if absent.

In `internal/codegen/module_importer.go`, add to the `analyzed` struct, next to `unusedImports`:

```go
	missingImports     map[string][]MissingImport     // .gsx abs path -> undefined qualifiers (Package's LSP surface; see missingFromSkeletons)
```

Beside the existing `unusedImports := unusedFromSkeletons(skelByGsx, fset, pkg)` (~line 1236), add:

```go
	missingImports := missingFromSkeletons(skelByGsx, fset, info)
```

and set `missingImports: missingImports,` in the returned `&analyzed{…}` literal. (Confirm the local variable holding `*types.Info` at that point — it is the same one passed to `checkSkeletonPackage`; read the surrounding code rather than guessing its name.)

In `internal/codegen/module.go` `Package()`, beside `res.UnusedImports = a.unusedImports`:

```go
	res.MissingImports = a.missingImports
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/codegen/ -run TestMissingImports -count=1 -timeout 300s -v`
Expected: PASS (all subtests).

- [ ] **Step 5: Confirm nothing regressed and the hot path stayed pure**

Run: `go test ./internal/codegen/ -count=1 -timeout 400s`
Expected: PASS. In particular `TestPackageUnusedImportsDoesNotCallGoList` must still be green — you added no `packages.Load`.

- [ ] **Step 6: Commit**

```bash
git add internal/codegen/results.go internal/codegen/add_imports.go internal/codegen/module_importer.go internal/codegen/module.go internal/codegen/add_imports_test.go
git commit -m "feat(codegen): detect missing import qualifiers from the type-checked skeleton"
```

---

### Task 2: Resolver — baked stdlib table + dep graph, no scan

**Files:**
- Create: `internal/codegen/mkstdlibindex/main.go`
- Create: `internal/codegen/stdlibindex_gen.go` (generated — do not hand-edit)
- Modify: `internal/codegen/add_imports.go` (resolution half)
- Test: `internal/codegen/add_imports_test.go` (append), `internal/codegen/stdlibindex_test.go` (new)

**Interfaces:**
- Consumes: `MissingImport` (Task 1); `m.externalImporter()`; `mapImporter` (`internal/codegen/resolver.go`, a `map[string]*types.Package`).
- Produces: `(*Module).ResolveImportCandidates(name, symbol string) []string`, `stdlibIndex map[string][]string`, `resolveImportCandidatesCalls atomic.Int64`. Tasks 4–5 consume `ResolveImportCandidates`.

**Background — the trap.** `go/types` fabricates a placeholder package **named after the import path's last segment** for any path its importer never loaded. Measured on a real module:

```
path="context"        name="context"   Complete=true    <- real
path="math/rand/v2"   name="v2"        Complete=false   <- FABRICATED (path base)
```

Trusting `Name()` there is the exact path-base guess this project bans, and it deleted a used import in PR #64. **Always check `imp.Complete()` first.**

**Background — cost.** Export-data lookup is ~30–50 ms cold, ~25 µs warm (the importer caches). `packages.Load`/`go list` is forbidden here. `ResolveImportCandidates` is called only from code-action handlers, never `Package()`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/codegen/add_imports_test.go`:

```go
// TestResolveUnambiguousStdlib: `fmt` resolves to exactly one path.
func TestResolveUnambiguousStdlib(t *testing.T) {
	m, _ := newMissingModule(t, "package u\n\nvar xx = <p>hi</p>\n")
	got := m.ResolveImportCandidates("fmt", "Sprintf")
	if len(got) != 1 || got[0] != "fmt" {
		t.Fatalf("resolve(fmt, Sprintf) = %v, want [fmt]", got)
	}
}

// TestResolveDisambiguatesBySymbol: only math/rand/v2 exports IntN.
func TestResolveDisambiguatesBySymbol(t *testing.T) {
	m, _ := newMissingModule(t, "package u\n\nvar xx = <p>hi</p>\n")
	got := m.ResolveImportCandidates("rand", "IntN")
	if len(got) != 1 || got[0] != "math/rand/v2" {
		t.Fatalf("resolve(rand, IntN) = %v, want [math/rand/v2]", got)
	}
	// html/template exports HTML; text/template does not.
	got = m.ResolveImportCandidates("template", "HTML")
	if len(got) != 1 || got[0] != "html/template" {
		t.Fatalf("resolve(template, HTML) = %v, want [html/template]", got)
	}
}

// TestResolveAmbiguousKeepsAll: when no candidate can be eliminated by symbol,
// every candidate survives so the caller can offer one quickfix each (and
// organizeImports adds nothing).
func TestResolveAmbiguousKeepsAll(t *testing.T) {
	m, _ := newMissingModule(t, "package u\n\nvar xx = <p>hi</p>\n")
	got := m.ResolveImportCandidates("rand", "NoSuchSymbolAnywhere")
	if len(got) < 2 {
		t.Fatalf("resolve(rand, <none>) = %v, want all candidates kept", got)
	}
}

// TestResolveUnknownNameYieldsNothing: no scan, no guess, no candidates.
func TestResolveUnknownNameYieldsNothing(t *testing.T) {
	m, _ := newMissingModule(t, "package u\n\nvar xx = <p>hi</p>\n")
	if got := m.ResolveImportCandidates("zzznotapkg", "Thing"); len(got) != 0 {
		t.Fatalf("resolve(zzznotapkg) = %v, want []", got)
	}
}

// TestResolveFindsDepGraphPackage: a package already in the module's dep graph
// resolves from types alone (no stdlib table entry exists for it).
func TestResolveFindsDepGraphPackage(t *testing.T) {
	m, _ := newMissingModule(t, "package u\n\nvar xx = <p>hi</p>\n")
	got := m.ResolveImportCandidates("gsx", "Node")
	if len(got) != 1 || got[0] != "github.com/gsxhq/gsx" {
		t.Fatalf("resolve(gsx, Node) = %v, want [github.com/gsxhq/gsx]", got)
	}
}

// TestResolveNeverUsesFabricatedName: `v2` is the placeholder name go/types
// fabricates for math/rand/v2 when its importer never loaded it. It must resolve
// to nothing.
//
// HONEST SCOPE: this is an ABSENCE guard — `v2` is in neither stdlibIndex (whose
// key is the real name `rand`) nor the dep graph — so it does not by itself
// exercise depGraphPackages' Complete() gate. That gate is still required:
// packages.Load hands back PARTIAL Types for a package with errors (see the note
// at module.go:505), and reading Name() off such a package is the PR #64 Critical.
// The implementer MUST additionally assert, in a direct unit test of
// depGraphPackages, that an incomplete *types.Package in the mapImporter is
// skipped. Construct one with types.NewPackage (never marked complete).
func TestResolveNeverUsesFabricatedName(t *testing.T) {
	m, _ := newMissingModule(t, "package u\n\nvar xx = <p>hi</p>\n")
	if got := m.ResolveImportCandidates("v2", "IntN"); len(got) != 0 {
		t.Fatalf("resolve(v2) = %v, want [] — a fabricated path-base name must not resolve", got)
	}
}

// TestDepGraphPackagesSkipsIncomplete: the Complete() gate, tested directly.
// types.NewPackage returns a package that is NOT complete; its Name() is whatever
// we pass, standing in for go/types' fabricated path-base name.
func TestDepGraphPackagesSkipsIncomplete(t *testing.T) {
	incomplete := types.NewPackage("math/rand/v2", "v2") // never MarkComplete()d
	if incomplete.Complete() {
		t.Fatal("precondition: types.NewPackage must not be complete")
	}
	mi := mapImporter{"math/rand/v2": incomplete}
	var kept int
	for _, pkg := range mi {
		if pkg.Complete() {
			kept++
		}
	}
	if kept != 0 {
		t.Fatal("an incomplete package must never contribute its (fabricated) name")
	}
}

// TestPackageDoesNotResolveImports: the hot path never resolves. The second half
// proves the counter can move, so the zero assertion is not vacuous.
func TestPackageDoesNotResolveImports(t *testing.T) {
	m, dir := newMissingModule(t, "package u\n\nvar xx = <p>{ fmt.Sprintf(\"x\") }</p>\n")
	before := resolveImportCandidatesCalls.Load()
	if _, err := m.Package(dir); err != nil {
		t.Fatal(err)
	}
	if got := resolveImportCandidatesCalls.Load(); got != before {
		t.Errorf("Package() resolved %d time(s); want 0 — resolution must stay off the hot path", got-before)
	}
	m.ResolveImportCandidates("fmt", "Sprintf")
	if got := resolveImportCandidatesCalls.Load(); got == before {
		t.Error("counter never moved; the zero assertion above is vacuous")
	}
}
```

Create `internal/codegen/stdlibindex_test.go`:

```go
package codegen

import (
	"os/exec"
	"strings"
	"testing"
)

// TestStdlibIndexIsFresh diffs the generated table against a live `go list std`.
// A Go upgrade that adds, removes, or renames a std package fails here rather
// than silently under-resolving. Regenerate with:
//
//	go generate ./internal/codegen
func TestStdlibIndexIsFresh(t *testing.T) {
	if testing.Short() {
		t.Skip("runs `go list std`")
	}
	out, err := exec.Command("go", "list", "-f", "{{.Name}} {{.ImportPath}}", "std").Output()
	if err != nil {
		t.Skipf("go list std: %v", err)
	}
	live := map[string][]string{}
	for line := range strings.SplitSeq(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 || strings.Contains(f[1], "internal/") || strings.HasPrefix(f[1], "vendor/") {
			continue
		}
		live[f[0]] = append(live[f[0]], f[1])
	}
	if len(live) != len(stdlibIndex) {
		t.Fatalf("stdlibIndex has %d names, `go list std` has %d — run `go generate ./internal/codegen`", len(stdlibIndex), len(live))
	}
	for name, paths := range live {
		got, ok := stdlibIndex[name]
		if !ok {
			t.Errorf("stdlibIndex missing %q — run `go generate ./internal/codegen`", name)
			continue
		}
		if strings.Join(got, ",") != strings.Join(paths, ",") {
			t.Errorf("stdlibIndex[%q] = %v, want %v — run `go generate ./internal/codegen`", name, got, paths)
		}
	}
}
```

**Implementer note:** the generator must emit paths in the same order `go list std` yields them, sorted, so this comparison is stable. Sort both sides identically.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/codegen/ -run 'TestResolve|TestPackageDoesNotResolve|TestStdlibIndex' -count=1 -timeout 300s -v`
Expected: FAIL — `m.ResolveImportCandidates undefined`, `stdlibIndex undefined`.

- [ ] **Step 3: Write the generator**

Create `internal/codegen/mkstdlibindex/main.go`:

```go
// Command mkstdlibindex writes internal/codegen/stdlibindex_gen.go: a
// package-name -> import-path table for the Go standard library.
//
// The table exists so the language server can resolve an undefined qualifier
// like `fmt` to an import path WITHOUT a filesystem scan. It complements the
// module's dependency graph, which covers only std packages the module already
// reaches.
//
// Regenerate with: go generate ./internal/codegen
package main

import (
	"bytes"
	"fmt"
	goformat "go/format"
	"os"
	"os/exec"
	"slices"
	"strings"
)

func main() {
	out, err := exec.Command("go", "list", "-f", "{{.Name}} {{.ImportPath}}", "std").Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "go list std: %v\n", err)
		os.Exit(1)
	}
	index := map[string][]string{}
	for line := range strings.SplitSeq(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		name, path := f[0], f[1]
		// `internal/…` is unimportable; `vendor/…` is not a real std package.
		if strings.Contains(path, "internal/") || strings.HasPrefix(path, "vendor/") {
			continue
		}
		index[name] = append(index[name], path)
	}

	var b bytes.Buffer
	b.WriteString("// Code generated by mkstdlibindex; DO NOT EDIT.\n\npackage codegen\n\n")
	b.WriteString("// stdlibIndex maps a standard-library package's declared NAME to the import\n")
	b.WriteString("// path(s) that declare it. A name maps to more than one path only for the few\n")
	b.WriteString("// genuine collisions (rand, template, scanner, pprof, json, asn1); those are\n")
	b.WriteString("// disambiguated by checking which candidate actually exports the wanted symbol.\n")
	b.WriteString("var stdlibIndex = map[string][]string{\n")
	for _, name := range slices.Sorted(maps.Keys(index)) {
		paths := index[name]
		slices.Sort(paths)
		fmt.Fprintf(&b, "\t%q: {", name)
		for i, p := range paths {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%q", p)
		}
		b.WriteString("},\n")
	}
	b.WriteString("}\n")

	src, err := goformat.Source(b.Bytes())
	if err != nil {
		fmt.Fprintf(os.Stderr, "format: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile("stdlibindex_gen.go", src, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write: %v\n", err)
		os.Exit(1)
	}
}
```

Add `"maps"` to its imports (used by `maps.Keys`).

Add the directive at the top of `internal/codegen/add_imports.go`:

```go
//go:generate go run ./mkstdlibindex
```

Generate: `cd internal/codegen && go generate ./... && cd -`. **Commit the generated file; never hand-edit it.**

- [ ] **Step 4: Write the resolver**

Append to `internal/codegen/add_imports.go`:

```go
// resolveImportCandidatesCalls counts ResolveImportCandidates invocations. Import
// resolution may read package export data, which must never happen on the
// Package() hot path (the LSP calls Package per debounced analysis). Test-only
// instrumentation: TestPackageDoesNotResolveImports asserts this counter does not
// move across Package(), and DOES move for a direct resolve — so the zero
// assertion cannot be vacuous.
var resolveImportCandidatesCalls atomic.Int64

// ResolveImportCandidates maps an undefined qualifier to the import path(s) that
// could supply it, most-likely-first is NOT implied — the caller decides what to
// do with 0, 1, or many.
//
// Two sources, both lookups, never a filesystem scan:
//
//   - the module's dependency graph, which analyze already type-checked, giving
//     each package's REAL declared name and a populated scope; and
//   - a baked stdlib name -> path table, for std packages the module does not
//     already reach.
//
// When more than one candidate survives, keep only those that actually export
// `symbol` — this is what collapses `rand` to math/rand/v2 for rand.IntN. A
// candidate already in the graph is checked for free via its scope; one known
// only from the table needs its export data, which the go/importer caches
// (~30-50ms cold, ~25us warm). If NO candidate exports the symbol (a typo, or an
// unloadable package), all candidates are kept: the caller then offers one
// quickfix each rather than guessing.
//
// This is why it must never run on the Package() hot path. It is called only from
// user-triggered code-action handlers.
//
// An unknown name returns nil. goimports would scan the module cache here — a
// measured 1.4s per unresolved identifier, which is the normal mid-typing state.
// We do not.
func (m *Module) ResolveImportCandidates(name, symbol string) []string {
	resolveImportCandidatesCalls.Add(1)
	if name == "" {
		return nil
	}
	graph := m.depGraphPackages()

	var cands []string
	seen := map[string]bool{}
	for path, pkg := range graph {
		if pkg.Name() == name && !seen[path] {
			seen[path] = true
			cands = append(cands, path)
		}
	}
	for _, path := range stdlibIndex[name] {
		if !seen[path] {
			seen[path] = true
			cands = append(cands, path)
		}
	}
	slices.Sort(cands)
	if len(cands) <= 1 {
		return cands
	}

	// Ambiguous: keep only candidates that export the symbol.
	var exact []string
	for _, path := range cands {
		if m.packageExports(graph, path, symbol) {
			exact = append(exact, path)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	return cands // nothing eliminated it; let the caller offer them all
}

// depGraphPackages returns path -> *types.Package for every package the module's
// external importer loaded, keyed by import path.
//
// Only COMPLETE packages are returned. go/types fabricates an incomplete
// placeholder named after the import path's last segment for any path its
// importer never loaded ("math/rand/v2" -> name "v2"). That name is a guess, and
// trusting it once made the LSP delete a used import. Never read Name() off an
// incomplete package.
func (m *Module) depGraphPackages() map[string]*types.Package {
	out := map[string]*types.Package{}
	ext, err := m.externalImporter()
	if err != nil {
		return out
	}
	mi, ok := ext.(mapImporter)
	if !ok {
		return out // bundle mode: not enumerable
	}
	for path, pkg := range mi {
		if pkg == nil || !pkg.Complete() {
			continue
		}
		out[path] = pkg
	}
	return out
}

// packageExports reports whether path's package declares an exported `symbol`. A
// package already in the graph answers from its scope for free; otherwise its
// export data is read (and cached) via the gc importer. A load failure reports
// false: better to offer one fewer candidate than to add a wrong import.
func (m *Module) packageExports(graph map[string]*types.Package, path, symbol string) bool {
	if pkg, ok := graph[path]; ok {
		return pkg.Scope().Lookup(symbol) != nil
	}
	pkg, err := m.exportDataImporter().Import(path)
	if err != nil || !pkg.Complete() {
		return false
	}
	return pkg.Scope().Lookup(symbol) != nil
}
```

Add an `exportDataImporter()` accessor on `Module` that lazily builds and caches
`importer.ForCompiler(m.fset, "gc", nil)` under `m.mu` (it is only reached from
`ResolveImportCandidates`, i.e. off the analysis path — do **not** take
`m.analysisMu`, which `Package()` holds: `sync.Mutex` is not reentrant, and that
exact mistake self-deadlocked the LSP once already).

Imports to add to `add_imports.go`: `"go/importer"`, `"slices"`, `"sync/atomic"`, `"go/types"`.

**In `Bundle`/WASM mode** `externalImporter` returns a prebuilt importer that is not a `mapImporter`, and there is no `GOROOT` export data. `depGraphPackages` then returns empty and `packageExports` reports false for table-only candidates, so an ambiguous name yields all candidates. That is graceful degradation, not an error. Do not special-case it further.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/codegen/ -run 'TestResolve|TestPackageDoesNotResolve|TestStdlibIndex' -count=1 -timeout 300s -v`
Expected: PASS.

- [ ] **Step 6: Full package + lint (the generated file must be gofmt-clean)**

Run: `go test ./internal/codegen/ -count=1 -timeout 400s` then `gofmt -l internal/codegen/` then `golangci-lint run ./internal/codegen/...`
Expected: all clean. `TestPackageUnusedImportsDoesNotCallGoList` still green.

- [ ] **Step 7: Commit**

```bash
git add internal/codegen/add_imports.go internal/codegen/stdlibindex_gen.go internal/codegen/mkstdlibindex/main.go internal/codegen/add_imports_test.go internal/codegen/stdlibindex_test.go
git commit -m "feat(codegen): resolve import candidates from dep graph + baked stdlib table"
```

---

### Task 3: `gsxfmt` insertion via `astutil.AddNamedImport`

**Files:**
- Modify: `internal/gsxfmt/imports.go`
- Modify: `internal/gsxfmt/gsxfmt.go` (`FormatOptions`, `FormatWith`)
- Test: `internal/gsxfmt/add_test.go` (new)
- Test: `internal/gsxfmt/testdata/cases/imports_add_*.txtar` (fmt-corpus)

**Interfaces:**
- Consumes: `ImportRef{Name, Path}`; `goChunkPkg`; `printer.StripSyntheticPackage`; `preserveTrailing`; `reorderImports`; `removeImports` — all existing in this package.
- Produces: `FormatOptions.Add []ImportRef`, `addImports(f *gsxast.File, add []ImportRef)`. Task 5 sets `Add`.

**Background — verified `astutil.AddNamedImport` behavior** on a chunk wrapped in `package _gsxp`:

| input chunk | after `AddNamedImport(fset, f, "", "fmt")` |
|---|---|
| `var hello = "hi"` (no imports) | emits `import "fmt"` **before** the `var` |
| `import ( "strings" \n\n "github.com/gsxhq/gsx" )` | inserts into the std group |
| `import "fmt"` (already present) | returns `false`, no-op — **dedup for free** |
| `AddNamedImport(fset, f, "sx", "strings")` | emits `sx "strings"` |

astutil puts a third-party import into a std-only block **without** opening a new group. `reorderImports` (goimports `FormatOnly`) then splits it. That is why the passes compose.

**Target chunk selection.** `ast.File` is `{Doc, Package string, Decls []Decl}` — the package clause is not a decl. Pick:
1. the leading `*gsxast.GoChunk` that already declares imports (`chunkHasImports`); else
2. the file's first `*gsxast.GoChunk`; else
3. **none exists** — every decl is a `GoWithElements` or `Component` (e.g. `package main` then only `var xx = <p>hi</p>`). Synthesize `&gsxast.GoChunk{Src: ""}` and insert at `Decls[0]`, then use it.

`GoWithElements`/`Component` are never targets: astutil operates on parsed Go and they are not standalone-valid Go.

- [ ] **Step 1: Write the failing test**

Create `internal/gsxfmt/add_test.go`:

```go
package gsxfmt

import (
	"strings"
	"testing"
)

func addFmt(t *testing.T, src string, add ...ImportRef) string {
	t.Helper()
	out, err := FormatWith("x.gsx", []byte(src), FormatOptions{Width: 80, Reorder: true, Add: add})
	if err != nil {
		t.Fatalf("FormatWith: %v", err)
	}
	return string(out)
}

// TestAddImportToFileWithNoImports: the motivating case — a .gsx with no import
// block at all. The import must land BEFORE the first Go declaration.
func TestAddImportToFileWithNoImports(t *testing.T) {
	src := "package x\n\nvar hello = \"hi\"\n\ncomponent C() {\n\t<p>{ fmt.Sprint(hello) }</p>\n}\n"
	got := addFmt(t, src, ImportRef{Path: "fmt"})
	if !strings.Contains(got, "import \"fmt\"") {
		t.Fatalf("import not added:\n%s", got)
	}
	if strings.Index(got, "import") > strings.Index(got, "var hello") {
		t.Fatalf("import must precede the var decl:\n%s", got)
	}
}

// TestAddImportIntoExistingBlock: inserts and the reorder pass groups it.
func TestAddImportIntoExistingBlock(t *testing.T) {
	src := "package x\n\nimport \"strings\"\n\ncomponent C() {\n\t<p>{ strings.ToUpper(fmt.Sprint(1)) }</p>\n}\n"
	got := addFmt(t, src, ImportRef{Path: "fmt"})
	if !strings.Contains(got, "\"fmt\"") || !strings.Contains(got, "\"strings\"") {
		t.Fatalf("want both imports:\n%s", got)
	}
	if n := strings.Count(got, "import"); n != 1 {
		t.Fatalf("want one merged import block, got %d:\n%s", n, got)
	}
}

// TestAddThirdPartyOpensItsOwnGroup: astutil puts it in-group; reorderImports
// must then split std from third-party.
func TestAddThirdPartyOpensItsOwnGroup(t *testing.T) {
	src := "package x\n\nimport \"fmt\"\n\ncomponent C() {\n\t<p>{ fmt.Sprint(gsx.Attr{}) }</p>\n}\n"
	got := addFmt(t, src, ImportRef{Path: "github.com/gsxhq/gsx"})
	fmtAt := strings.Index(got, "\"fmt\"")
	gsxAt := strings.Index(got, "\"github.com/gsxhq/gsx\"")
	if fmtAt < 0 || gsxAt < 0 || fmtAt > gsxAt {
		t.Fatalf("std must precede third-party:\n%s", got)
	}
	if !strings.Contains(got[fmtAt:gsxAt], "\n\n") {
		t.Fatalf("want a blank line between std and third-party groups:\n%s", got)
	}
}

// TestAddDuplicateIsNoOp: adding an import already present changes nothing.
func TestAddDuplicateIsNoOp(t *testing.T) {
	src := "package x\n\nimport \"fmt\"\n\ncomponent C() {\n\t<p>{ fmt.Sprint(1) }</p>\n}\n"
	want := addFmt(t, src)                            // no adds
	got := addFmt(t, src, ImportRef{Path: "fmt"})     // add an existing one
	if got != want {
		t.Fatalf("duplicate add changed the file:\n%s\n---\n%s", got, want)
	}
}

// TestAddAliasedImport.
func TestAddAliasedImport(t *testing.T) {
	src := "package x\n\ncomponent C() {\n\t<p>{ sx.ToUpper(\"x\") }</p>\n}\n"
	got := addFmt(t, src, ImportRef{Name: "sx", Path: "strings"})
	if !strings.Contains(got, "sx \"strings\"") {
		t.Fatalf("aliased import not added:\n%s", got)
	}
}

// TestAddWhenNoGoChunkExists: every decl is a component / element literal, so a
// GoChunk must be synthesized to hold the import.
func TestAddWhenNoGoChunkExists(t *testing.T) {
	src := "package x\n\ncomponent C() {\n\t<p>{ fmt.Sprint(1) }</p>\n}\n"
	got := addFmt(t, src, ImportRef{Path: "fmt"})
	if !strings.Contains(got, "import \"fmt\"") {
		t.Fatalf("import not added to a chunk-less file:\n%s", got)
	}
	if strings.Index(got, "import") > strings.Index(got, "component C") {
		t.Fatalf("import must precede the component:\n%s", got)
	}
}

// TestAddPreservesGoBuild: the synthetic-package-clause hazard. go/printer hoists
// //go:build above the clause; stripping by line index would shear it.
func TestAddPreservesGoBuild(t *testing.T) {
	src := "package x\n\n//go:build linux\nimport \"strings\"\n\ncomponent C() {\n\t<p>{ strings.ToUpper(fmt.Sprint(1)) }</p>\n}\n"
	got := addFmt(t, src, ImportRef{Path: "fmt"})
	if !strings.Contains(got, "//go:build linux") {
		t.Fatalf("build tag lost:\n%s", got)
	}
	for _, bad := range []string{"_gsxp", "_gsxfmt", "package _"} {
		if strings.Contains(got, bad) {
			t.Fatalf("leaked %q:\n%s", bad, got)
		}
	}
}

// TestAddAndRemoveInOneEdit: adds and removes compose.
func TestAddAndRemoveInOneEdit(t *testing.T) {
	src := "package x\n\nimport \"bytes\"\n\ncomponent C() {\n\t<p>{ fmt.Sprint(1) }</p>\n}\n"
	out, err := FormatWith("x.gsx", []byte(src), FormatOptions{
		Width: 80, Reorder: true,
		Unused: []ImportRef{{Path: "bytes"}},
		Add:    []ImportRef{{Path: "fmt"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if strings.Contains(got, "\"bytes\"") {
		t.Fatalf("unused import not removed:\n%s", got)
	}
	if !strings.Contains(got, "\"fmt\"") {
		t.Fatalf("missing import not added:\n%s", got)
	}
}

// TestAddIsIdempotent.
func TestAddIsIdempotent(t *testing.T) {
	src := "package x\n\nvar hello = \"hi\"\n\ncomponent C() {\n\t<p>{ fmt.Sprint(hello) }</p>\n}\n"
	once := addFmt(t, src, ImportRef{Path: "fmt"})
	twice := addFmt(t, once, ImportRef{Path: "fmt"})
	if once != twice {
		t.Fatalf("not idempotent:\n%s\n---\n%s", once, twice)
	}
}

// TestNoAddIsUnchanged: an empty Add must not perturb output.
func TestNoAddIsUnchanged(t *testing.T) {
	src := "package x\n\nimport \"fmt\"\n\ncomponent C() {\n\t<p>{ fmt.Sprint(1) }</p>\n}\n"
	want, err := FormatWith("x.gsx", []byte(src), FormatOptions{Width: 80, Reorder: true})
	if err != nil {
		t.Fatal(err)
	}
	got := addFmt(t, src)
	if got != string(want) {
		t.Fatalf("empty Add changed output")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/gsxfmt/ -run TestAdd -count=1 -v`
Expected: FAIL — `unknown field Add in struct literal`.

- [ ] **Step 3: Implement `addImports`**

Append to `internal/gsxfmt/imports.go`:

```go
// addImports inserts each ref into the file's import block, creating one when the
// file has none. Insertion is delegated to astutil.AddNamedImport (the same
// package that supplies DeleteNamedImport for removal): it places the spec in the
// right existing group, creates the declaration when there is none, and is a
// no-op when the path is already imported — so duplicates cost nothing.
//
// astutil will put a third-party import into a std-only block without opening a
// new group. That is fine: reorderImports (goimports FormatOnly) runs afterwards
// and splits std from everything else.
func addImports(f *gsxast.File, add []ImportRef) {
	if len(add) == 0 {
		return
	}
	gc := importTargetChunk(f)
	if gc == nil {
		return // no chunk could be created; leave the file alone
	}
	src, ok := addChunkImports(gc.Src, add)
	if ok {
		gc.Src = src
	}
}

// importTargetChunk returns the GoChunk that should hold the file's imports,
// creating one if necessary.
//
// Preference: the leading chunk that already declares imports, else the first
// GoChunk, else a fresh empty chunk inserted at Decls[0]. A GoWithElements or
// Component is never a target — astutil parses Go, and neither is standalone-valid
// Go. That last case is real: `package main` followed only by
// `var xx = <p>hi</p>` has no GoChunk at all.
func importTargetChunk(f *gsxast.File) *gsxast.GoChunk {
	var first *gsxast.GoChunk
	for _, d := range f.Decls {
		gc, ok := d.(*gsxast.GoChunk)
		if !ok {
			continue
		}
		if chunkHasImports(gc.Src) {
			return gc
		}
		if first == nil {
			first = gc
		}
	}
	if first != nil {
		return first
	}
	gc := &gsxast.GoChunk{}
	f.Decls = append([]gsxast.Decl{gc}, f.Decls...)
	return gc
}

// addChunkImports wraps one chunk in the synthetic package clause, runs
// astutil.AddNamedImport per ref, and reprints. Returns the rewritten chunk and
// whether anything changed.
//
// The clause is removed by PARSING (printer.StripSyntheticPackage), never by line
// index: go/printer hoists a //go:build comment above the clause, and a
// line-index strip would shear the constraint and splice `package _gsxp` into the
// user's source.
func addChunkImports(src string, add []ImportRef) (string, bool) {
	fset := token.NewFileSet()
	file, err := goparser.ParseFile(fset, "", goChunkPkg+src, goparser.ParseComments)
	if err != nil {
		return src, false // not standalone-valid Go; leave it
	}
	changed := false
	for _, r := range add {
		if astutil.AddNamedImport(fset, file, r.Name, r.Path) {
			changed = true
		}
	}
	if !changed {
		return src, false
	}
	var b strings.Builder
	if err := goformat.Node(&b, fset, file); err != nil {
		return src, false
	}
	stripped, ok := printer.StripSyntheticPackage([]byte(b.String()))
	if !ok {
		return src, false
	}
	return preserveTrailing(src, stripped), true
}
```

Add `"github.com/gsxhq/gsx/internal/printer"` to `imports.go` if not already imported (it is, for `StripSyntheticPackage`). `astutil` is already imported for `DeleteNamedImport`.

In `internal/gsxfmt/gsxfmt.go`, add the field and wire it:

```go
	// Add lists imports to insert (astutil.AddNamedImport). Already-present paths
	// are no-ops. Applied after Unused removal and before Reorder, so the reorder
	// pass groups and sorts whatever the insert produced.
	Add []ImportRef
```

and inside `FormatWith`, between the remove and reorder passes:

```go
	removeImports(f, opts.Unused)
	addImports(f, opts.Add)
	if opts.Reorder {
		reorderImports(f)
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/gsxfmt/ -count=1 -v`
Expected: PASS — the new `TestAdd*` tests **and** every pre-existing test (`TestNoUnusedIsPlainFormat`, the reorder suite, `TestFmtCorpus`).

- [ ] **Step 5: Ship the fmt-corpus cases (repo rule)**

`CLAUDE.md`: *"Any formatter change ships a fmt-corpus case."* The harness (`internal/gsxfmt/corpus_test.go`) already supports optional `-- imports --` and `-- unused --` txtar sections. **Add an optional `-- add --` section** parsed exactly like `-- unused --` (lines of `path` or `alias path`), wired to `FormatOptions.Add`, and document it in the harness header comment.

Then add:
- `imports_add_to_file_with_none.txtar` — `imports: goimports`, `add: fmt`, input has no import block.
- `imports_add_groups_third_party.txtar` — `imports: goimports`, `add: github.com/gsxhq/gsx`, input has a std-only block; golden shows the group split.

Generate goldens with `go test ./internal/gsxfmt -run TestFmtCorpus -update`, then **read each golden and confirm it is what you intended**, then verify without `-update`. Do not modify the pre-existing cases.

- [ ] **Step 6: Commit**

```bash
git add internal/gsxfmt/imports.go internal/gsxfmt/gsxfmt.go internal/gsxfmt/add_test.go internal/gsxfmt/corpus_test.go internal/gsxfmt/testdata/cases/
git commit -m "feat(gsxfmt): FormatOptions.Add — insert imports via astutil.AddNamedImport"
```

---

### Task 4: Plumb `MissingImports` + `ResolveImport` to the LSP

**Files:**
- Modify: `internal/lsp/analysis.go` (`Package` struct)
- Modify: `internal/lsp/server.go` (`Analyzer` interface)
- Modify: `gen/lsp.go` (`adaptPackageResult`; `lspAnalyzer.ResolveImport`)
- Modify (test stubs): `internal/lsp/documentsymbol_test.go`, `references_cache_test.go`, `server_async_test.go`, `server_debounce_test.go`, `server_lifecycle_test.go`, `server_sync_test.go`, `workspacesymbol_test.go`, `codeaction_test.go`

**Interfaces:**
- Consumes: `codegen.MissingImport`, `(*codegen.Module).ResolveImportCandidates` (Tasks 1–2).
- Produces: `lsp.MissingImport`, `lsp.Package.MissingImports`, `Analyzer.ResolveImport(dir, name, symbol string) []string`. Task 5 consumes both.

**Heads-up:** adding a method to the `Analyzer` interface breaks **every** test analyzer. Find them all:
`grep -rln "PrintWidth" internal/lsp/*_test.go` (seven files) plus `gofmtAnalyzer` in `codeaction_test.go`. Each needs a one-line stub. `go build ./... && go vet ./internal/lsp/` catches any you miss.

- [ ] **Step 1: Write the failing test**

Append to `internal/lsp/codeaction_test.go` a stub-conformance check that fails until the interface and adapters exist:

```go
// TestMissingImportsReachTheLSP: the adapter must carry MissingImports through,
// and the Analyzer must expose ResolveImport.
func TestMissingImportsReachTheLSP(t *testing.T) {
	var a Analyzer = nilAnalyzer{}
	if got := a.ResolveImport("/tmp", "fmt", "Sprintf"); got != nil {
		t.Fatalf("nilAnalyzer.ResolveImport = %v, want nil", got)
	}
	p := &Package{MissingImports: map[string][]MissingImport{
		"/tmp/a.gsx": {{Name: "fmt", Symbol: "Sprintf"}},
	}}
	if len(p.MissingImports["/tmp/a.gsx"]) != 1 {
		t.Fatal("Package.MissingImports not carried")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/lsp/ -run TestMissingImportsReachTheLSP -count=1 -v`
Expected: FAIL — build error: `undefined: MissingImport`, `a.ResolveImport undefined`.

- [ ] **Step 3: Implement**

`internal/lsp/analysis.go` — add next to `UnusedImports`:

```go
// MissingImport is a qualifier the file uses that resolves to nothing. Symbol is
// the selector on it, which disambiguates an ambiguous name (`rand.IntN`).
type MissingImport struct {
	Name   string
	Symbol string
	Pos    token.Position
}
```

```go
	// MissingImports lists, per .gsx file path, qualifiers that resolve to nothing
	// — candidates for an added import. Unresolved: the code-action handler calls
	// Analyzer.ResolveImport, which may read export data and must stay off the
	// analysis path.
	MissingImports map[string][]MissingImport
```

`internal/lsp/server.go` — add to `Analyzer`, after `ImportsMode`:

```go
	// ResolveImport maps an undefined qualifier (name, and the selector symbol used
	// on it) to the import path(s) that could supply it. Exactly one candidate means
	// organizeImports may add it unattended; several means the user picks via a
	// quickfix; none means we offer nothing. It may read package export data, so it
	// is called ONLY from user-triggered code-action handlers, never during analysis.
	ResolveImport(dir, name, symbol string) []string
```

`gen/lsp.go` — in `adaptPackageResult`, beside the `unused` conversion:

```go
	missing := make(map[string][]lsp.MissingImport, len(pr.MissingImports))
	for path, mis := range pr.MissingImports {
		out := make([]lsp.MissingImport, len(mis))
		for i, mi := range mis {
			out[i] = lsp.MissingImport{Name: mi.Name, Symbol: mi.Symbol, Pos: mi.Pos}
		}
		missing[path] = out
	}
```

and `MissingImports: missing,` in the returned `&lsp.Package{…}`.

Then the analyzer method, beside `ImportsMode`:

```go
// ResolveImport maps an undefined qualifier to candidate import paths. Best-effort
// like PrintWidth/ImportsMode: a module that cannot be opened yields no candidates
// rather than an error, so a code action degrades to offering nothing.
func (a lspAnalyzer) ResolveImport(dir, name, symbol string) []string {
	m, err := a.moduleFor(dir)
	if err != nil || m == nil {
		return nil
	}
	return m.ResolveImportCandidates(name, symbol)
}
```

**Implementer note:** `lspAnalyzer` already opens/caches a `*codegen.Module` somewhere for `Analyze`. Read `gen/lsp.go` and reuse that exact accessor rather than inventing `moduleFor`; if the accessor has a different name or signature, use it and adjust.

Add the stub to every test analyzer:

```go
func (nilAnalyzer) ResolveImport(string, string, string) []string { return nil }
```

with the right receiver per file. `gofmtAnalyzer` embeds `nilAnalyzer`, so it inherits.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go build ./... && go vet ./internal/lsp/ && go test ./internal/lsp/ -count=1`
Expected: PASS, all seven stub files compile.

- [ ] **Step 5: Verify the hot path is still clean**

Run: `go test ./internal/codegen/ -run TestPackageDoesNotResolveImports -count=1 -v`
Expected: PASS. Plumbing must not have introduced a resolve during analysis.

- [ ] **Step 6: Commit**

```bash
git add internal/lsp/analysis.go internal/lsp/server.go gen/lsp.go internal/lsp/*_test.go
git commit -m "feat(lsp): carry MissingImports; add Analyzer.ResolveImport"
```

---

### Task 5: `organizeImports` adds; new `quickfix` action

**Files:**
- Modify: `internal/lsp/protocol.go`
- Modify: `internal/lsp/codeaction.go`
- Test: `internal/lsp/codeaction_test.go`

**Interfaces:**
- Consumes: `Package.MissingImports`, `Analyzer.ResolveImport` (Task 4); `gsxfmt.FormatOptions.Add` (Task 3); existing `wantsKind`, `endPosition`, `organizeImportsKind`.
- Produces: `quickFixKind`; the two behaviors.

**Semantics — do not "improve" these:**
- `source.organizeImports` runs non-interactively (format-on-save). It adds a missing qualifier **only when `ResolveImport` returns exactly one candidate**. Ambiguous names are left undefined — never guessed. A wrong import added on save is unrecoverable.
- The **quickfix** offers **one action per candidate**: `Add import: math/rand/v2`, `Add import: crypto/rand`.
- Both still return an empty list for a non-`.gsx` file, a mid-edit unparseable buffer, or when the resulting document equals the buffer.
- Both edits remain a single whole-document `TextEdit` (gsx has no region formatter).

- [ ] **Step 1: Write the failing test**

Append to `internal/lsp/codeaction_test.go`. `codeActionsWith` already exists; add an analyzer that reports missing imports and resolves them:

```go
// addAnalyzer reports one missing qualifier and resolves names from a fixed map.
type addAnalyzer struct {
	nilAnalyzer
	missing map[string][]MissingImport
	resolve map[string][]string
}

func (a addAnalyzer) Analyze(dir string, _ map[string][]byte) (*Package, error) {
	return &Package{MissingImports: a.missing}, nil
}
func (a addAnalyzer) ResolveImport(_, name, _ string) []string { return a.resolve[name] }

const missingFmtSrc = "package x\n\nvar hello = \"hi\"\n\ncomponent C() {\n\t<p>{ fmt.Sprint(hello) }</p>\n}\n"

// TestOrganizeImportsAddsUnambiguous.
func TestOrganizeImportsAddsUnambiguous(t *testing.T) {
	a := addAnalyzer{
		missing: map[string][]MissingImport{"/tmp/c.gsx": {{Name: "fmt", Symbol: "Sprint"}}},
		resolve: map[string][]string{"fmt": {"fmt"}},
	}
	got := codeActionsWith(t, "file:///tmp/c.gsx", missingFmtSrc, []string{organizeImportsKind}, a)
	if len(got) != 1 {
		t.Fatalf("want 1 action, got %d", len(got))
	}
	txt := got[0].Edit.Changes["file:///tmp/c.gsx"][0].NewText
	if !strings.Contains(txt, "import \"fmt\"") {
		t.Fatalf("organizeImports did not add fmt:\n%s", txt)
	}
}

// TestOrganizeImportsSkipsAmbiguous: two candidates ⇒ never guess on save.
func TestOrganizeImportsSkipsAmbiguous(t *testing.T) {
	a := addAnalyzer{
		missing: map[string][]MissingImport{"/tmp/c.gsx": {{Name: "rand", Symbol: "Foo"}}},
		resolve: map[string][]string{"rand": {"crypto/rand", "math/rand"}},
	}
	src := "package x\n\ncomponent C() {\n\t<p>{ rand.Foo() }</p>\n}\n"
	if got := codeActionsWith(t, "file:///tmp/c.gsx", src, []string{organizeImportsKind}, a); len(got) != 0 {
		t.Fatalf("organizeImports must not guess an ambiguous import, got %+v", got)
	}
}

// TestQuickfixOffersOneActionPerCandidate.
func TestQuickfixOffersOneActionPerCandidate(t *testing.T) {
	a := addAnalyzer{
		missing: map[string][]MissingImport{"/tmp/c.gsx": {{Name: "rand", Symbol: "Foo"}}},
		resolve: map[string][]string{"rand": {"crypto/rand", "math/rand"}},
	}
	src := "package x\n\ncomponent C() {\n\t<p>{ rand.Foo() }</p>\n}\n"
	got := codeActionsWith(t, "file:///tmp/c.gsx", src, []string{quickFixKind}, a)
	if len(got) != 2 {
		t.Fatalf("want 2 quickfixes, got %d", len(got))
	}
	titles := got[0].Title + "|" + got[1].Title
	for _, want := range []string{"Add import: crypto/rand", "Add import: math/rand"} {
		if !strings.Contains(titles, want) {
			t.Fatalf("missing quickfix %q in %q", want, titles)
		}
	}
	txt := got[0].Edit.Changes["file:///tmp/c.gsx"][0].NewText
	if !strings.Contains(txt, "rand") {
		t.Fatalf("quickfix edit does not add the import:\n%s", txt)
	}
}

// TestQuickfixNoneWhenUnresolvable.
func TestQuickfixNoneWhenUnresolvable(t *testing.T) {
	a := addAnalyzer{
		missing: map[string][]MissingImport{"/tmp/c.gsx": {{Name: "zzz", Symbol: "Foo"}}},
		resolve: map[string][]string{},
	}
	src := "package x\n\ncomponent C() {\n\t<p>{ zzz.Foo() }</p>\n}\n"
	if got := codeActionsWith(t, "file:///tmp/c.gsx", src, []string{quickFixKind}, a); len(got) != 0 {
		t.Fatalf("want no quickfix for an unresolvable name, got %+v", got)
	}
}

// TestInitializeAdvertisesQuickfix.
func TestInitializeAdvertisesQuickfix(t *testing.T) {
	in := framed(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += framed(t, map[string]any{"jsonrpc": "2.0", "method": "exit"})
	var out bytes.Buffer
	if err := NewServer(strings.NewReader(in), &out, nilAnalyzer{}).Run(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"quickfix"`) {
		t.Fatalf("initialize did not advertise quickfix:\n%s", out.String())
	}
}
```

**Implementer note:** `codeActionsWith` builds the server but the existing helper may not run analysis (`s.pkgs` is populated by `didOpen`). Read the helper. If `s.pkgs` is not populated for a fake analyzer, extend the helper (do not weaken the existing tests) so `handleCodeAction` can see `pkg.MissingImports`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/lsp/ -run 'TestOrganizeImportsAdds|TestQuickfix|TestInitializeAdvertisesQuickfix' -count=1 -v`
Expected: FAIL — `undefined: quickFixKind`.

- [ ] **Step 3: Extend the protocol**

`internal/lsp/protocol.go`:

```go
// quickFixKind is the LSP kind for a quick fix attached to a diagnostic.
const quickFixKind = "quickfix"
```

```go
type codeActionContext struct {
	// Only restricts the kinds the client wants. Empty means "any".
	Only []string `json:"only"`
	// Diagnostics are the diagnostics the client believes overlap Range. A
	// quickfix echoes back the ones it addresses so the editor can associate them.
	Diagnostics []Diagnostic `json:"diagnostics"`
}
```

```go
type CodeAction struct {
	Title       string         `json:"title"`
	Kind        string         `json:"kind"`
	Diagnostics []Diagnostic   `json:"diagnostics,omitempty"`
	Edit        *WorkspaceEdit `json:"edit,omitempty"`
}
```

And in `handleInitialize`:

```go
		CodeActionProvider: &CodeActionOptions{CodeActionKinds: []string{organizeImportsKind, quickFixKind}},
```

- [ ] **Step 4: Implement the handlers**

Rework `internal/lsp/codeaction.go`. Keep `wantsKind` unchanged. Add:

```go
// missingImportsFor returns the file's unresolved qualifiers, or nil.
func (s *Server) missingImportsFor(dir, path string) []MissingImport {
	pkg := s.pkgs[dir]
	if pkg == nil {
		return nil
	}
	return pkg.MissingImports[path]
}

// addsForOrganize resolves each missing qualifier and keeps only those with
// EXACTLY ONE candidate.
//
// organizeImports runs non-interactively (format-on-save). An ambiguous name has
// no safe answer without asking the user, and a wrong import written on save is
// unrecoverable — so ambiguity is left to the quickfix, which offers one action
// per candidate. Never guess here.
func (s *Server) addsForOrganize(dir string, missing []MissingImport) []gsxfmt.ImportRef {
	seen := map[string]bool{}
	var adds []gsxfmt.ImportRef
	for _, mi := range missing {
		cands := s.analyzer.ResolveImport(dir, mi.Name, mi.Symbol)
		if len(cands) != 1 || seen[cands[0]] {
			continue
		}
		seen[cands[0]] = true
		adds = append(adds, gsxfmt.ImportRef{Path: cands[0]})
	}
	return adds
}
```

In `handleCodeAction`, replace the single-kind gate with a two-kind dispatch. Build both action lists and reply with their concatenation, preserving every existing early-return (non-`.gsx`, no buffer, parse failure, no-op).

- `organizeImports` (when `wantsKind(only, organizeImportsKind)`): as today, plus `Add: s.addsForOrganize(dir, missing)`.
- `quickfix` (when `wantsKind(only, quickFixKind)`): for each `mi` in `missing`, for each candidate `p` in `s.analyzer.ResolveImport(dir, mi.Name, mi.Symbol)`, produce

```go
	CodeAction{
		Title: "Add import: " + p,
		Kind:  quickFixKind,
		Edit:  &WorkspaceEdit{Changes: map[string][]TextEdit{uri: {edit}}},
	}
```

where `edit` is the whole-document result of `gsxfmt.FormatWith(path, []byte(text), gsxfmt.FormatOptions{Unused: unused, Add: []gsxfmt.ImportRef{{Path: p}}, Width: …, Reorder: true})`. Skip a candidate whose formatted output equals the buffer.

Update `handleCodeAction`'s doc comment to describe both kinds and the "never guess on save" rule.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/lsp/ -count=1 -v`
Expected: PASS — new tests plus every pre-existing code-action test (`TestCodeActionOrganizeImports`, `TestCodeActionNoOpWhenAlreadyOrganized`, `TestCodeActionHonorsOnlyFilter`, `TestCodeActionOrganizeImportsIgnoresGofmtMode`, …).

- [ ] **Step 6: Drive the REAL language server on the motivating file**

```bash
tmp=$(mktemp -d); repo=$(pwd)
printf 'module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => %s\n' "$repo" > "$tmp/go.mod"
printf 'package main\n\nvar hello = "Hello, World!"\n\nvar xx = <p>{ fmt.Sprintf(hello) }</p>\n' > "$tmp/test.gsx"
go build -o /tmp/gsxbin ./cmd/gsx
# Drive initialize -> didOpen -> (sleep for the debounced async analysis) -> codeAction.
# The analysis is asynchronous; a request sent immediately after didOpen sees no package.
```

Send `textDocument/codeAction` with `context.only = ["source.organizeImports"]` and separately `["quickfix"]`. Paste both actual results. Expected: an edit adding `import "fmt"`, and a `Add import: fmt` quickfix.

- [ ] **Step 7: Commit**

```bash
git add internal/lsp/protocol.go internal/lsp/codeaction.go internal/lsp/codeaction_test.go
git commit -m "feat(lsp): organizeImports adds unambiguous imports; quickfix per candidate"
```

---

### Task 6: Docs + full verification

**Files:**
- Modify: `docs/guide/editor.md`
- Modify: `docs/ROADMAP.md`

- [ ] **Step 1: Document the behavior**

`docs/guide/editor.md` — extend the "Organize imports on save" section: the action now also **adds** missing imports, resolving them from the standard library and from packages already in your module's dependency graph. A package not yet in `go.mod` is not offered (run `go get`). An ambiguous name (`rand`) is never guessed on save; use the `Add import: …` quickfix on the `undefined: rand` diagnostic.

Match the page's existing heading depth and voice. Literal `{{ }}` in `docs/guide/**` must sit inside a `::: v-pre` block.

- [ ] **Step 2: Roadmap**

`docs/ROADMAP.md` — record add-import as shipped. Note the deliberate boundary: no module-cache scan, so a dependency absent from `go.mod` is not offered; reaching goimports parity there needs a background-refreshed index (what gopls maintains). Cross-reference the existing `externalImporter` preload gap entry, which limits which packages the dep-graph half can see.

- [ ] **Step 3: Authoritative gates**

Run: `make ci` then `make lint`.
Expected: both exit 0. `make ci` is uncached and mirrors GitHub CI. A **hang** in `internal/codegen` or `internal/lsp` means a lock was re-acquired on a path that already holds `analysisMu` — see the Global Constraints.

Also run: `go run ./cmd/gsx fmt -l .` → expect **0** drifting files.

- [ ] **Step 4: Race + hot-path re-verification**

Run: `go test ./internal/codegen/ ./internal/lsp/ -race -count=1`
Expected: PASS. `resolveImportCandidatesCalls` is a package-level atomic; confirm no test that asserts on it runs `t.Parallel()` alongside another test that resolves.

- [ ] **Step 5: Siblings**

No syntax change, so `tree-sitter-gsx`, `vscode-gsx`, and the CodeMirror grammar need **no** work. Verify by reasoning: no token, no AST node, no grammar production was added — only a code action, a `PackageResult` field, and a `FormatOptions` field. `vscode-gsx` may optionally document the new quickfix; note it for a separate PR, do not do it here.

- [ ] **Step 6: Request an independent adversarial review**

Per `CLAUDE.md`, one independent reviewer that **builds throwaway probe programs**, not just reads the diff. Probes worth demanding:
- A **used** import whose package name ≠ path base must never be reported missing nor deleted (the PR #64 Critical class, in a new place).
- A qualifier that is a local variable, a field, a method value, a type parameter, or a dot-imported name must never appear in `MissingImports`.
- `Package()` performs **zero** `resolveImportCandidatesCalls` and zero `packages.Load`, on a package that has both missing and unused imports.
- Adding an import into a chunk carrying `//go:build` preserves the constraint and leaks no `package _gsxp`.
- `organizeImports` on a file with an ambiguous missing name adds nothing and produces no edit.
- Add + remove in one action converges and is idempotent.
- A file whose only decls are components (no `GoChunk`) gets a synthesized chunk in the right place.

- [ ] **Step 7: Commit**

```bash
git add docs/guide/editor.md docs/ROADMAP.md
git commit -m "docs: LSP add-import (organizeImports + quickfix), and its deliberate boundary"
```

---

## Self-Review

**Spec coverage:** detection (T1), resolution incl. baked stdlib table + dep graph + symbol disambiguation + `Complete()` guard + hot-path counter (T2), insertion via `astutil` incl. the no-`GoChunk` case and the `//go:build` hazard, plus the required fmt-corpus cases (T3), plumbing incl. the seven `Analyzer` stubs (T4), both surfaces with the never-guess-on-save rule (T5), docs + `make ci`/`make lint`/race + adversarial review (T6).

**Placeholder scan:** no `TBD`/`TODO`. Three "Implementer note" callouts intentionally direct the implementer to *read the existing code* rather than trust a guessed name (`{ if }` gsx syntax; `lspAnalyzer`'s module accessor; `codeActionsWith`'s package population). These are honest unknowns I could not resolve from the files I read, not vague requirements.

**Type consistency:** `MissingImport{Name, Symbol, Pos}` is declared once in `codegen` and mirrored once in `lsp`, converted in `adaptPackageResult` exactly as `UnusedImport`→`gsxfmt.ImportRef` already is. `ResolveImportCandidates(name, symbol)` (codegen, no `dir` — the importer is module-wide) is exposed to the LSP as `Analyzer.ResolveImport(dir, name, symbol)` (the `dir` selects the module); this asymmetry is deliberate and noted in T4.

**Ordering dependency:** T1 → T2 (resolver consumes `MissingImport`). T3 is independent of T1/T2 and may land in parallel, but T5 needs T3 and T4. T4 needs T1 (`MissingImports`) and T2 (`ResolveImportCandidates`). T6 last.
