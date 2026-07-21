# LSP Completion Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Full-surface `textDocument/completion` for `.gsx` files served from gsx's own warm analysis core — Go expressions, pipe filters, component tags/attrs, HTML tags/attrs/values.

**Architecture:** The handler classifies the cursor's gsx context from a (possibly repaired) parse of the live buffer, then enumerates candidates: Go contexts through a new synchronous ephemeral analysis (`Module.AnalyzeEphemeral`, warm, uncached), gsx-native contexts from warm facts (filter tables, `ComponentDecls`, `ComponentCallFact.Signature`), HTML from a vendored VS Code dataset. All edits are computed in authored-buffer coordinates. NO gopls proxying (see spec's rejected-approach section).

**Tech Stack:** Go stdlib (`go/types`, `go/token`, `go/ast`), existing gsx packages (`internal/codegen`, `internal/lsp`, `parser`, `ast`), vendored `@vscode/web-custom-data` JSON.

**Spec:** `docs/superpowers/specs/2026-07-21-lsp-completion-design.md` — read it first.

## Global Constraints

- No gopls proxying, no reverse remapping of client-visible ranges: every `TextEdit` is computed against the original live buffer.
- The phantom identifier is `_` (never a `_gsx`-prefixed name — the reserved-prefix check at `module_importer.go:1069-1090` DELETES files containing `_gsx` user identifiers from analysis).
- `AnalyzeEphemeral` must not poison any Module cache: `pkgResults` is never written; `pkgTypes[dir]` and `targetDeclProvenance[dir]` are snapshot/restored.
- Fail-soft: source-state problems (shell results, package-clause mismatch `(nil, err)`) return an empty completion list, never a JSON-RPC error.
- All new exported-to-LSP types follow the existing mirror pattern: `internal/codegen` type → copied field-by-field in `gen/lsp.go` `adaptPackageResult` → `internal/lsp` mirror type. `internal/lsp` must NOT import `internal/codegen`.
- Unexported names unless serialization or the cross-package mirror requires export.
- Run `make ci` before the final task's commit; run package tests per task.
- Commit messages: end with the session trailer if your harness instructions specify one.

## File Structure

| File | Responsibility |
|---|---|
| `internal/codegen/module_importer.go` (modify) | Move `componentTargetDeclarationProvenances` out of the error gate |
| `internal/codegen/results.go` (modify) | `FilterCandidate` type; `PackageResult.Filters` |
| `internal/codegen/module.go` (modify) | `AnalyzeEphemeral` + ephemeral source overlay in `currentSource` |
| `internal/codegen/module_ephemeral_test.go` (create) | AnalyzeEphemeral behavior + cache-preservation tests |
| `internal/lsp/protocol.go` (modify) | Completion wire types + `CompletionProvider` capability |
| `internal/lsp/server.go` (modify) | Dispatch case; capability in `handleInitialize`; `Analyzer` interface method |
| `internal/lsp/completion.go` (create) | `handleCompletion` orchestration |
| `internal/lsp/completion_repair.go` (create) | Parse-first repair chooser |
| `internal/lsp/completion_context.go` (create) | AST cursor-context classification |
| `internal/lsp/completion_go.go` (create) | Go scope/member enumeration |
| `internal/lsp/completion_items.go` (create) | Item construction, sortText tiers, token-span edits |
| `internal/lsp/completion_gsx.go` (create) | Pipe / component-tag / component-attr candidates |
| `internal/lsp/completion_html.go` (create) | HTML tag/attr/value candidates |
| `internal/lsp/analysis.go` (modify) | `lsp.Package.Filters` mirror field, `FilterCandidate` mirror |
| `internal/htmldata/` (create) | Vendored JSON, generator, generated table |
| `gen/lsp.go` (modify) | `AnalyzeEphemeral` Analyzer impl; adapt `Filters` |
| `gen/lsp_completion_e2e_test.go` (create) | End-to-end suite with real analyzer |
| `docs/guide/editor.md`, `docs/guide/status.md`, `docs/ROADMAP.md` (modify) | Docs |

---

### Task 1: ComponentDecls survive analysis errors

The probe proved `PackageResult.ComponentDecls` empties (2 → 0) whenever the package has a type error or bag error, because `componentTargetDeclarationProvenances` is gated at `internal/codegen/module_importer.go:1477`:

```go
var localComponentProvenance map[string]componentTargetDeclarationProvenance
if !bag.HasErrors() && len(typeErrs) == 0 {
    localComponentProvenance, err = componentTargetDeclarationProvenances(gsxFiles, parsed.sources, fset, componentPlan)
    ...
}
```

Its inputs (`gsxFiles, parsed.sources, fset, componentPlan`) are all available before type-checking — they are syntactic facts. Component-tag completion (and existing tag hover/definition) needs them mid-edit.

**Files:**
- Modify: `internal/codegen/module_importer.go:1476-1482`
- Test: `internal/codegen/module_ephemeral_test.go` (create — this file grows across Tasks 1–3)

**Interfaces:**
- Produces: `PackageResult.ComponentDecls` non-empty on type-error packages (consumed by Task 12).

- [ ] **Step 1: Write the failing test**

Create `internal/codegen/module_ephemeral_test.go`. Model the fixture helper on the existing Module tests in this package (see `module_test.go` for how they build a temp module dir — reuse an existing helper if one fits; the shape needed is: temp dir with `go.mod` requiring gsx via `replace`, a `page` package containing `types.go` with `type User struct{ Name string }`, `other.gsx` declaring `component Other() { <div>ok</div> }`, and `page.gsx` set via `SetOverride`):

```go
package codegen

import "testing"

// componentDeclsSurviveTypeErrors: a type error in one file must not empty the
// package's syntactic component-declaration facts (spec: tag completion works
// mid-edit; probe 2026-07-21 showed 2 → 0 before the fix).
func TestComponentDeclsSurviveTypeErrors(t *testing.T) {
	m, dir, pagePath := newEphemeralTestModule(t) // helper: see step notes below
	// Valid baseline: two components (Home in page.gsx, Other in other.gsx).
	m.SetOverride(pagePath, []byte("package page\n\ncomponent Home(user User) {\n\t<div>{ user.Name }</div>\n}\n"))
	res, err := m.Package(dir)
	if err != nil {
		t.Fatalf("baseline Package: %v", err)
	}
	if len(res.ComponentDecls) != 2 {
		t.Fatalf("baseline ComponentDecls = %d, want 2", len(res.ComponentDecls))
	}
	// Introduce a type error (User has no field Nam).
	m.SetOverride(pagePath, []byte("package page\n\ncomponent Home(user User) {\n\t<div>{ user.Nam }</div>\n}\n"))
	res, err = m.Package(dir)
	if err != nil {
		t.Fatalf("type-error Package: %v", err)
	}
	if len(res.ComponentDecls) != 2 {
		t.Fatalf("type-error ComponentDecls = %d, want 2 (syntactic facts must survive type errors)", len(res.ComponentDecls))
	}
}
```

`newEphemeralTestModule(t)` returns `(*Module, pkgDir string, pageGsxAbsPath string)`. Build it on whatever existing test scaffolding this package already has for `Open(Options{...})` against a temp dir (grep `Open(Options` in `internal/codegen/*_test.go` and copy the closest fixture). The fixture files:

```
go.mod:      module example.com/app  +  require github.com/gsxhq/gsx v0.0.0  +  replace → repo root
page/types.go:   package page\n\ntype User struct{ Name string }
page/other.gsx:  package page\n\ncomponent Other() {\n\t<div>ok</div>\n}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/codegen/ -run TestComponentDeclsSurviveTypeErrors -v`
Expected: FAIL with `type-error ComponentDecls = 0, want 2`

- [ ] **Step 3: Move the provenance computation out of the gate**

In `internal/codegen/module_importer.go`, change:

```go
var localComponentProvenance map[string]componentTargetDeclarationProvenance
if !bag.HasErrors() && len(typeErrs) == 0 {
    localComponentProvenance, err = componentTargetDeclarationProvenances(gsxFiles, parsed.sources, fset, componentPlan)
    if err != nil {
        return nil, err
    }
}
```

to:

```go
// Component declaration provenances are syntactic facts (inputs: parsed files,
// sources, plan — nothing type-checked), so they are computed even when the
// package has bag or type errors: tag completion/hover/definition must keep
// working mid-edit. A provenance error is soft here on an errored package —
// the package is already failing loudly; on a clean package it stays fatal.
localComponentProvenance, provErr := componentTargetDeclarationProvenances(gsxFiles, parsed.sources, fset, componentPlan)
if provErr != nil {
    if !bag.HasErrors() && len(typeErrs) == 0 {
        return nil, provErr
    }
    localComponentProvenance = nil
}
```

(Delete the now-unused `var localComponentProvenance ...` line; keep the declaration via `:=`. Note `err` was previously reused — make sure the surrounding code still compiles; use a fresh `provErr`.)

Read `componentTargetDeclarationProvenances`'s body before committing: confirm it dereferences nothing that is nil on the error path (it consumes `gsxFiles` which may have had reserved-prefix files deleted — that is fine, fewer files). If you find a genuine dependency on type-check success, STOP and surface it rather than working around it.

- [ ] **Step 4: Run the test and the package suite**

Run: `go test ./internal/codegen/ -run TestComponentDeclsSurviveTypeErrors -v`
Expected: PASS
Run: `go test ./internal/codegen/`
Expected: PASS (if existing tests assert ComponentDecls emptiness on error packages, they encode the bug — update them and say so in the commit message)

- [ ] **Step 5: Commit**

```bash
git add internal/codegen/module_importer.go internal/codegen/module_ephemeral_test.go
git commit -m "fix(codegen): component decl provenances survive bag/type errors

Syntactic facts were gated on a clean type-check, so ComponentDecls
emptied on any mid-edit error - killing tag completion/hover exactly
when needed. Provenance errors stay fatal on clean packages."
```

---

### Task 2: PackageResult.Filters — pipe-filter candidates from the warm table

`analyze` already resolves the per-dir filter table into `a.table` (`funcTables`, field `filters filterTable`; `internal/codegen/filters.go:98-135`). Surface the winners as a sorted candidate list on `PackageResult` so the LSP gets pipe candidates with zero extra loads.

**Files:**
- Modify: `internal/codegen/results.go` (add type + field), `internal/codegen/module.go` (populate in `Package`), `internal/lsp/analysis.go` (mirror), `gen/lsp.go` (adapt)
- Test: `internal/codegen/module_ephemeral_test.go`

**Interfaces:**
- Produces:
  ```go
  // internal/codegen/results.go
  type FilterCandidate struct {
      Name     string // template name, e.g. "upper"
      Pkg      string // winning package import path
      Func     string // exported Go func name, e.g. "Upper"
      WantsCtx bool
  }
  // PackageResult gains: Filters []FilterCandidate  (sorted by Name)
  ```
  ```go
  // internal/lsp/analysis.go — identical mirror:
  type FilterCandidate struct {
      Name, Pkg, Func string
      WantsCtx        bool
  }
  // lsp.Package gains: Filters []FilterCandidate
  ```
  Consumed by Task 11.

- [ ] **Step 1: Write the failing test**

Append to `internal/codegen/module_ephemeral_test.go`:

```go
func TestPackageResultFilters(t *testing.T) {
	m, dir, pagePath := newEphemeralTestModule(t)
	m.SetOverride(pagePath, []byte("package page\n\ncomponent Home(user User) {\n\t<div>{ user.Name |> upper }</div>\n}\n"))
	res, err := m.Package(dir)
	if err != nil {
		t.Fatalf("Package: %v", err)
	}
	if len(res.Filters) == 0 {
		t.Fatal("Filters empty; want std filters (upper, lower, trim, ...)")
	}
	names := map[string]FilterCandidate{}
	for i, f := range res.Filters {
		names[f.Name] = f
		if i > 0 && res.Filters[i-1].Name >= f.Name {
			t.Fatalf("Filters not sorted by Name at %d: %q >= %q", i, res.Filters[i-1].Name, f.Name)
		}
	}
	up, ok := names["upper"]
	if !ok {
		t.Fatalf("std filter upper missing; got %v", res.Filters)
	}
	if up.Func != "Upper" || up.Pkg != "github.com/gsxhq/gsx/std" {
		t.Fatalf("upper = %+v, want Func=Upper Pkg=github.com/gsxhq/gsx/std", up)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/codegen/ -run TestPackageResultFilters -v`
Expected: FAIL to compile (`res.Filters` undefined) — that counts as the failing state.

- [ ] **Step 3: Implement**

In `internal/codegen/results.go` add the `FilterCandidate` type (doc comment: "one pipeline-filter completion candidate, from the dir's resolved filter table") and the `Filters []FilterCandidate` field on `PackageResult`.

In `internal/codegen/module.go`, inside `Package()` where `res` is assembled (after `res.Diags = a.bag.Sorted()` is fine), add:

```go
res.Filters = filterCandidates(a.table)
```

and in `results.go` (or `filters.go`, wherever fits the file's responsibility — `filters.go` is the natural home):

```go
// filterCandidates flattens the resolved per-dir filter table into a sorted
// completion-candidate list. The table holds last-wins winners only, so there
// is no shadow information here (ResolveFilters reports shadows for gsx info).
func filterCandidates(t funcTables) []FilterCandidate {
	out := make([]FilterCandidate, 0, len(t.filters))
	for name, e := range t.filters {
		out = append(out, FilterCandidate{Name: name, Pkg: e.pkgPath, Func: e.funcName, WantsCtx: e.wantsCtx})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
```

In `internal/lsp/analysis.go` add the mirror `FilterCandidate` type and `Filters []FilterCandidate` on `Package`. In `gen/lsp.go` `adaptPackageResult`, copy:

```go
filters := make([]lsp.FilterCandidate, len(pr.Filters))
for i, fc := range pr.Filters {
	filters[i] = lsp.FilterCandidate{Name: fc.Name, Pkg: fc.Pkg, Func: fc.Func, WantsCtx: fc.WantsCtx}
}
// and in the returned &lsp.Package{...}: Filters: filters,
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/codegen/ -run TestPackageResultFilters -v && go build ./...`
Expected: PASS, clean build.

- [ ] **Step 5: Commit**

```bash
git add internal/codegen/results.go internal/codegen/filters.go internal/codegen/module.go internal/lsp/analysis.go gen/lsp.go
git commit -m "feat(codegen): surface resolved filter table as PackageResult.Filters"
```

---

### Task 3: Module.AnalyzeEphemeral — synchronous fresh analysis without cache poisoning

The completion entry point: analyze `dir` with one file's bytes replaced by a repaired buffer, without touching persistent overrides, dirty tracking, or caches.

**Files:**
- Modify: `internal/codegen/module.go`
- Test: `internal/codegen/module_ephemeral_test.go`

**Interfaces:**
- Produces:
  ```go
  // AnalyzeEphemeral runs one warm analysis of dir with absPath's source
  // replaced by src, WITHOUT recording the result: pkgResults is never
  // written, and the pkgTypes/targetDeclProvenance entries analyze writes
  // for dir are restored afterward. Dependency packages analyzed (and
  // cached) along the way use their real sources — that warmth is shared
  // and desirable. Serialized under analysisMu like Package/Generate.
  // Source-level breakage returns a diagnostics-only PackageResult (nil
  // Info/Types), mirroring Package's shell semantics.
  func (m *Module) AnalyzeEphemeral(dir, absPath string, src []byte) (*PackageResult, error)
  ```
  Consumed by Task 5 (`gen/lsp.go`).

- [ ] **Step 1: Write the failing tests**

Append to `internal/codegen/module_ephemeral_test.go`:

```go
func TestAnalyzeEphemeralBasics(t *testing.T) {
	m, dir, pagePath := newEphemeralTestModule(t)
	live := []byte("package page\n\ncomponent Home(user User) {\n\t<div>{ user.Name }</div>\n}\n")
	m.SetOverride(pagePath, live)
	base, err := m.Package(dir)
	if err != nil || base.Info == nil {
		t.Fatalf("baseline: %v info=%v", err, base.Info)
	}

	// Ephemeral: the phantom-repaired trailing-dot buffer. user._ typechecks
	// with a type error but full Info (probe 2026-07-21).
	patched := []byte("package page\n\ncomponent Home(user User) {\n\t<div>{ user._ }</div>\n}\n")
	eph, err := m.AnalyzeEphemeral(dir, pagePath, patched)
	if err != nil {
		t.Fatalf("AnalyzeEphemeral: %v", err)
	}
	if eph.Info == nil || eph.Types == nil {
		t.Fatal("ephemeral result missing Info/Types")
	}
	if len(eph.ExprMap) == 0 {
		t.Fatal("ephemeral ExprMap empty; want the user._ interp bridged")
	}
	if len(eph.Filters) == 0 {
		t.Fatal("ephemeral result missing Filters")
	}

	// The persistent view is untouched: Package(dir) returns the SAME cached
	// result pointer as before, and its facts reflect the live buffer.
	after, err := m.Package(dir)
	if err != nil {
		t.Fatalf("Package after ephemeral: %v", err)
	}
	if after != base {
		t.Fatal("AnalyzeEphemeral evicted the cached PackageResult; must not touch pkgResults")
	}
}

func TestAnalyzeEphemeralShellOnBrokenElsewhere(t *testing.T) {
	m, dir, pagePath := newEphemeralTestModule(t)
	// other.gsx is valid; break page.gsx structurally somewhere the repair
	// didn't fix (an unclosed tag on a DIFFERENT line than the "cursor").
	patched := []byte("package page\n\ncomponent Home(user User) {\n\t<div\n\t<span>{ user._ }</span>\n}\n")
	m.SetOverride(pagePath, []byte("package page\n\ncomponent Home(user User) {\n\t<div>{ user.Name }</div>\n}\n"))
	eph, err := m.AnalyzeEphemeral(dir, pagePath, patched)
	if err != nil {
		t.Fatalf("AnalyzeEphemeral must not hard-error on parse diags: %v", err)
	}
	if eph.Info != nil {
		t.Fatal("want diagnostics-only shell for unrepaired parse error")
	}
	if len(eph.Diags) == 0 {
		t.Fatal("shell result must carry the parse diagnostics")
	}
}

func TestAnalyzeEphemeralDoesNotDirty(t *testing.T) {
	m, dir, pagePath := newEphemeralTestModule(t)
	m.SetOverride(pagePath, []byte("package page\n\ncomponent Home(user User) {\n\t<div>{ user.Name }</div>\n}\n"))
	if _, err := m.Package(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AnalyzeEphemeral(dir, pagePath, []byte("package page\n\ncomponent Home(user User) {\n\t<div>{ user._ }</div>\n}\n")); err != nil {
		t.Fatal(err)
	}
	if got := m.dirtyDirs(); len(got) != 0 {
		t.Fatalf("ephemeral analysis dirtied %v; must leave dirty tracking untouched", got)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/codegen/ -run TestAnalyzeEphemeral -v`
Expected: compile FAIL (`AnalyzeEphemeral` undefined).

- [ ] **Step 3: Implement the ephemeral overlay + method**

In `internal/codegen/module.go`:

1. Add a field to `Module` (near `overrides`):

```go
ephemeral map[string][]byte // one-shot source overlay for AnalyzeEphemeral; non-nil only while it runs (under analysisMu)
```

2. Consult it first in `currentSource` (inside the existing `m.mu.Lock()` block):

```go
m.mu.Lock()
if e, ok := m.ephemeral[absPath]; ok {
	e = bytes.Clone(e)
	m.mu.Unlock()
	return e, true
}
ov, ok := m.overrides[absPath]
...
```

3. Add the method, modeled directly on `Package()` (`module.go:1228`):

```go
func (m *Module) AnalyzeEphemeral(dir, absPath string, src []byte) (*PackageResult, error) {
	m.analysisMu.Lock()
	defer m.analysisMu.Unlock()
	m.maybeRebuildFset()
	m.applyDirty()
	if err := m.validateConfiguredMergers(); err != nil {
		return nil, err
	}
	ext, err := m.externalImporter()
	if err != nil {
		return nil, err
	}

	// Install the one-shot overlay and snapshot the two cache entries analyze
	// writes for dir (module_importer.go: m.pkgTypes[dir], m.targetDeclProvenance[dir]).
	// recordImports edges are identical to what the live buffer's own analysis
	// records (the repair only patches bytes at the cursor), so they stand.
	m.mu.Lock()
	m.ephemeral = map[string][]byte{absPath: src}
	prevTypes, hadTypes := m.pkgTypes[dir]
	var prevProv map[string]componentTargetDeclarationProvenance
	var hadProv bool
	if m.targetDeclProvenance != nil {
		prevProv, hadProv = m.targetDeclProvenance[dir]
	}
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.ephemeral = nil
		if hadTypes {
			m.pkgTypes[dir] = prevTypes
		} else {
			delete(m.pkgTypes, dir)
		}
		if m.targetDeclProvenance != nil {
			if hadProv {
				m.targetDeclProvenance[dir] = prevProv
			} else {
				delete(m.targetDeclProvenance, dir)
			}
		}
		m.mu.Unlock()
	}()

	a, err := m.analyze(dir, newModuleImporter(m, ext), analysisRetainedPackage)
	if err != nil {
		if diags, ok := diagnosticsFromSourceError(err); ok {
			return &PackageResult{Files: map[string][]byte{}, Diags: diags}, nil
		}
		return nil, err
	}
	res := &PackageResult{
		Files:       map[string][]byte{},
		GSXFset:     a.gsxFset,
		Fset:        a.skelFset,
		Info:        a.info,
		Types:       a.pkg,
		GSXFiles:    a.gsxFiles,
		ExprMap:     a.exprMap,
		CtrlMap:     a.ctrlMap,
		SigTypes:    a.sigTypes,
		SourceIndex: a.sourceIndex,
	}
	res.Diags = a.bag.Sorted()
	res.CrossIndex, res.NavIndex = buildCrossNav(a.compByKey, a.objKey, a.gsxFiles, a.gsxFset, a.skelFset, a.info)
	res.ComponentCalls = componentCallFacts(a.positionalPlan)
	res.ComponentDecls = a.componentDecls
	res.Filters = filterCandidates(a.table)
	// NOT stored in m.pkgResults, NOT running generateFile (emit-side
	// diagnostics are irrelevant to completion), no param decl/ref facts
	// (rename-only surface).
	return res, nil
}
```

IMPORTANT verification while implementing: read `analyze`'s full body for any OTHER module-level cache writes keyed by `dir` beyond `pkgTypes`/`targetDeclProvenance`/`recordImports` (search `m.mu.Lock()` inside `module_importer.go` between lines 1032 and the return). If you find one, snapshot/restore it the same way and note it in the commit message. If a cache write is keyed by something else that ephemeral sources could corrupt (e.g. an inventory fact keyed by path), STOP and surface it.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/codegen/ -run 'TestAnalyzeEphemeral|TestComponentDecls|TestPackageResultFilters' -v && go test ./internal/codegen/`
Expected: PASS (full package suite too — the `currentSource` change is on a hot path).

- [ ] **Step 5: Commit**

```bash
git add internal/codegen/module.go internal/codegen/module_ephemeral_test.go
git commit -m "feat(codegen): Module.AnalyzeEphemeral - warm uncached analysis with a one-shot source overlay"
```

---

### Task 4: Completion protocol surface — wire types, capability, dispatch, stub handler

Ship the protocol plumbing with a handler that always returns an empty list, so every later task plugs into a live request path.

**Files:**
- Modify: `internal/lsp/protocol.go`, `internal/lsp/server.go`
- Create: `internal/lsp/completion.go`, `internal/lsp/completion_test.go`

**Interfaces:**
- Produces (protocol.go):
  ```go
  type CompletionOptions struct {
      TriggerCharacters []string `json:"triggerCharacters,omitempty"`
  }
  type completionParams struct {
      TextDocument textDocumentIdentifier `json:"textDocument"`
      Position     Position               `json:"position"`
  }
  type CompletionList struct {
      IsIncomplete bool             `json:"isIncomplete"`
      Items        []CompletionItem `json:"items"`
  }
  type CompletionItem struct {
      Label         string         `json:"label"`
      Kind          int            `json:"kind,omitempty"`
      Detail        string         `json:"detail,omitempty"`
      Documentation *MarkupContent `json:"documentation,omitempty"`
      SortText      string         `json:"sortText,omitempty"`
      FilterText    string         `json:"filterText,omitempty"`
      TextEdit      *TextEdit      `json:"textEdit,omitempty"`
  }
  // LSP CompletionItemKind constants used across tasks:
  const (
      ciKindText=1; ciKindMethod=2; ciKindFunction=3; ciKindConstructor=4; ciKindField=5;
      ciKindVariable=6; ciKindClass=7; ciKindInterface=8; ciKindModule=9; ciKindProperty=10;
      ciKindEnum=13; ciKindKeyword=14; ciKindEnumMember=20; ciKindConstant=21; ciKindStruct=22;
      ciKindTypeParameter=25
  )
  ```
  (Write the consts as a proper grouped `const` block, one per line, with the LSP names in a comment.)
- Produces (server.go): `CompletionProvider *CompletionOptions` on `serverCapabilities`; `case "textDocument/completion": return s.handleCompletion(f)`.
- Produces (completion.go): `func (s *Server) handleCompletion(f frame) error` — decode params, guard `.go` files / missing buffer / invalid disk view exactly like `handleHover` (`hover.go:58-78`), reply `CompletionList{IsIncomplete: false, Items: []CompletionItem{}}` for now. Reply `Items` must never be nil (clients treat null differently) — always a non-nil slice.

- [ ] **Step 1: Write the failing tests**

`internal/lsp/completion_test.go` — follow the pattern of an existing initialize-capability test (grep `DocumentSymbolProvider` or `handleInitialize` in `internal/lsp/*_test.go` and mirror the harness). Assertions:

```go
// 1. initialize result advertises completionProvider with trigger characters
//    [".", "<", ">", "\"", "|"] (exact set, exact order).
// 2. a textDocument/completion request against an opened .gsx buffer replies
//    {"isIncomplete":false,"items":[]} (not null).
// 3. a completion request for a .go URI replies null.
```

Write all three as real tests against the server harness used by `server_lifecycle_test.go` (in-memory transport + fake analyzer). The fake analyzer needs the new interface method — see Step 3.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/lsp/ -run TestCompletion -v`
Expected: compile FAIL (types undefined).

- [ ] **Step 3: Implement**

1. protocol.go: add the types above.
2. server.go `serverCapabilities`: add `CompletionProvider *CompletionOptions \`json:"completionProvider,omitempty"\`` after `HoverProvider`.
3. server.go `handleInitialize` result: add
   ```go
   CompletionProvider: &CompletionOptions{TriggerCharacters: []string{".", "<", ">", "\"", "|"}},
   ```
   (`>` covers the second char of `|>` — after typing `|>` the client fires with the pipe context ready; it also fires harmlessly after closing tags, where markup context returns empty.)
4. server.go `handle` switch: add the case (alphabetical placement near the other textDocument cases).
5. completion.go:

```go
package lsp

import (
	"encoding/json"
	"strings"
)

// handleCompletion answers textDocument/completion for a .gsx file. .go files
// are gopls's to complete (null). Source-state problems (mid-edit breakage,
// package-clause mismatch) yield an empty list, never an error: completion is
// advisory and must fail soft.
func (s *Server) handleCompletion(f frame) error {
	var p completionParams
	if err := json.Unmarshal(f.Params, &p); err != nil {
		return s.reply(f.ID, nil)
	}
	if !s.diskViewValid {
		return s.reply(f.ID, emptyCompletion())
	}
	path := uriToPath(p.TextDocument.URI)
	if strings.HasSuffix(path, ".go") {
		return s.reply(f.ID, nil) // gopls owns .go completion
	}
	sources := s.sourceSnapshot()
	text, ok := sources.sourceString(path)
	if !ok {
		return s.reply(f.ID, emptyCompletion())
	}
	_ = text // consumed from Task 6 on
	return s.reply(f.ID, emptyCompletion())
}

func emptyCompletion() CompletionList {
	return CompletionList{IsIncomplete: false, Items: []CompletionItem{}}
}
```

6. Add `AnalyzeEphemeral(dir, path string, content []byte) (*Package, error)` to the `Analyzer` interface in server.go with this doc comment:

```go
// AnalyzeEphemeral runs one synchronous warm analysis of dir with path's
// source replaced by content, WITHOUT touching override lifetime, caches, or
// dirty tracking. Used by completion on the (possibly repaired) live buffer.
// Fails soft: source-level breakage returns a diagnostics-only Package.
```

Update every fake `Analyzer` in tests to stub it (find them: `grep -rn "func (.*) Analyze(dir string" internal/lsp/ gen/`; e.g. `overrideLifetimeAnalyzer` in `server_async_test.go:32-83`). Stub body: `return nil, errors.New("not implemented")` unless the test needs more.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/lsp/ -run TestCompletion -v && go test ./internal/lsp/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/lsp/protocol.go internal/lsp/server.go internal/lsp/completion.go internal/lsp/completion_test.go internal/lsp/server_async_test.go
git commit -m "feat(lsp): advertise and route textDocument/completion (empty-list stub)"
```

(Include any other test files whose fakes you updated.)

---

### Task 5: gen/lsp.go — AnalyzeEphemeral Analyzer implementation

**Files:**
- Modify: `gen/lsp.go`
- Test: `gen/lsp_completion_e2e_test.go` (create; grows through Task 16)

**Interfaces:**
- Consumes: `codegen.Module.AnalyzeEphemeral` (Task 3), `adaptPackageResult` (Task 2 extended it with Filters).
- Produces: `func (a lspAnalyzer) AnalyzeEphemeral(dir, path string, content []byte) (*lsp.Package, error)`.

- [ ] **Step 1: Write the failing test**

`gen/lsp_completion_e2e_test.go` — model the module/analyzer fixture on `gen/lsp_hover_e2e_test.go` (same temp-module scaffolding):

```go
func TestAnalyzeEphemeralViaAnalyzer(t *testing.T) {
	// fixture: temp module, page/ with types.go (User{Name string}) + page.gsx
	// opened via SetOverride with { user.Name }.
	a, dir, pagePath := newCompletionE2EFixture(t) // helper mirroring the hover e2e fixture
	patched := []byte("package page\n\ncomponent Home(user User) {\n\t<div>{ user._ }</div>\n}\n")
	pkg, err := a.AnalyzeEphemeral(dir, pagePath, patched)
	if err != nil {
		t.Fatalf("AnalyzeEphemeral: %v", err)
	}
	if pkg.Info == nil || len(pkg.ExprMap) == 0 {
		t.Fatal("ephemeral lsp.Package missing Info/ExprMap")
	}
	if len(pkg.Filters) == 0 {
		t.Fatal("ephemeral lsp.Package missing Filters")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./gen/ -run TestAnalyzeEphemeralViaAnalyzer -v`
Expected: compile FAIL.

- [ ] **Step 3: Implement**

In `gen/lsp.go`, next to `Analyze` (`gen/lsp.go:564`), following its module-resolution shape exactly (resolve abs dir → `moduleRoot` → `a.module(...)` → call, adapt):

```go
func (a lspAnalyzer) AnalyzeEphemeral(dir, path string, content []byte) (*lsp.Package, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	root, modPath, err := moduleRoot(abs)
	if err != nil {
		return nil, err
	}
	merged := resolveConfigBestEffort(abs, a.optCfg, a.warnw)
	m, _, err := a.module(root, modPath, merged)
	if m == nil {
		return nil, err
	}
	pr, err := m.AnalyzeEphemeral(abs, filepath.Clean(absPath), content)
	if err != nil {
		return nil, err
	}
	return adaptPackageResult(pr), nil
}
```

Compare against `Analyze`'s actual body and mirror any detail it has that this sketch lacks (e.g. error joining, module-affected handling) — `Analyze` is the source of truth for the resolution dance.

- [ ] **Step 4: Run tests**

Run: `go test ./gen/ -run TestAnalyzeEphemeralViaAnalyzer -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add gen/lsp.go gen/lsp_completion_e2e_test.go
git commit -m "feat(gen): lspAnalyzer.AnalyzeEphemeral over the warm module"
```

---

### Task 6: Repair chooser — parse-first, deterministic patch set

**Files:**
- Create: `internal/lsp/completion_repair.go`, `internal/lsp/completion_repair_test.go`

**Interfaces:**
- Consumes: `gsxparser.ParseFileWithClassifier(fset, path, src, 0, nil)` (see `module_importer.go:1735` for the call shape; the LSP already imports the parser in `symbols.go` — mirror its flags/fallback conventions).
- Produces:
  ```go
  // repairResult is the buffer completion analyzes, plus what was done to it.
  type repairResult struct {
      src     []byte // patched bytes (== live buffer when patch == "")
      patch   string // inserted at off: "", "_", "/>", "\"/>", "}\"/>"...
      parsed  *gsxast.File // parse of src (nil only when unrepairable)
  }
  // repairAtCursor parses text; on failure tries a closed, ordered patch list
  // inserted at off, first parse wins. Deterministic; never touches bytes
  // before off.
  func repairAtCursor(fset *token.FileSet, path string, text string, off int) repairResult
  ```

The patch list, in order (each is `text[:off] + patch + text[off:]`):

1. `""` — the unpatched buffer (identifier prefixes like `user.Na` and `{ user. }` both PARSE as gsx; the trailing-dot problem is a *skeleton* problem handled by Task 7's phantom decision, not here).
2. `"_"` — phantom identifier: heals `{ x |> }` (empty pipe stage) and other missing-token spots.
3. `"/>"` — heals a half-typed open tag: `<Ca`, `<div cl`.
4. `"\"/>"` — heals an unclosed attribute string: `<div class="x`.
5. `"=\"\"/>"` — heals a dangling attr name at `=`: `<div class=`.
6. `"}/>"` — heals an unclosed expr attr: `<div class={x`.

**Failure mode:** none parses → `repairResult{src: []byte(text), patch: "", parsed: nil}`; the handler returns the empty list.

- [ ] **Step 1: Write the failing table test**

```go
func TestRepairAtCursor(t *testing.T) {
	// The § marker is the cursor; it is removed before calling.
	cases := []struct {
		name, src, wantPatch string
		wantParsed           bool
	}{
		{"valid buffer", "package p\n\ncomponent C() {\n\t<div>{ x§ }</div>\n}\n", "", true},
		{"trailing dot parses as gsx", "package p\n\ncomponent C() {\n\t<div>{ user.§ }</div>\n}\n", "", true},
		{"empty pipe stage", "package p\n\ncomponent C() {\n\t<div>{ x |> § }</div>\n}\n", "_", true},
		{"half-typed component tag", "package p\n\ncomponent C() {\n\t<div><Ca§</div>\n}\n", "/>", true},
		{"half-typed attr name", "package p\n\ncomponent C() {\n\t<div cl§\n}\n", "/>", true},
		{"unclosed attr string", "package p\n\ncomponent C() {\n\t<div class=\"x§\n}\n", "\"/>", true},
		{"dangling equals", "package p\n\ncomponent C() {\n\t<div class=§\n}\n", "=\"\"/>", false /* adjust: see step 3 */},
		{"unclosed expr attr", "package p\n\ncomponent C() {\n\t<div class={x§\n}\n", "}/>", true},
		{"unrepairable", "package p\n\ncomponent C() {\n\t<§<<%%\n}\n", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			off := strings.Index(tc.src, "§")
			text := strings.Replace(tc.src, "§", "", 1)
			r := repairAtCursor(token.NewFileSet(), "/tmp/x.gsx", text, off)
			if r.patch != tc.wantPatch {
				t.Fatalf("patch = %q, want %q", r.patch, tc.wantPatch)
			}
			if (r.parsed != nil) != tc.wantParsed {
				t.Fatalf("parsed = %v, want %v", r.parsed != nil, tc.wantParsed)
			}
		})
	}
}
```

NOTE on the `dangling equals` case: run the test and observe what the parser actually does with `class=` followed by a patch — the `wantPatch` values above for cases 5–8 are HYPOTHESES about parser recovery. Adjust expectations to observed reality (e.g. if `class=` + `"/>"` alone parses, the `=\"\"/>` patch is unreachable — delete it from the list). What must hold: every case that users hit mid-typing ends with `parsed != nil` under SOME patch, the patch list is minimal (delete unreachable entries), and the chosen patch is deterministic. If a case cannot be healed by any insert-at-cursor patch, surface it in the task summary rather than adding lookbehind deletion.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/lsp/ -run TestRepairAtCursor -v`
Expected: compile FAIL.

- [ ] **Step 3: Implement**

```go
package lsp

import (
	"go/token"

	gsxast "github.com/gsxhq/gsx/ast"
	gsxparser "github.com/gsxhq/gsx/parser"
)

// completionPatches is the closed, ordered repair set. Each is tried by
// inserting at the cursor and reparsing; the first parse wins. Bytes before
// the cursor are never modified, so every client-visible offset survives.
var completionPatches = []string{"", "_", "/>", "\"/>", "=\"\"/>", "}/>"}

func repairAtCursor(fset *token.FileSet, path string, text string, off int) repairResult {
	if off < 0 {
		off = 0
	}
	if off > len(text) {
		off = len(text)
	}
	for _, patch := range completionPatches {
		src := make([]byte, 0, len(text)+len(patch))
		src = append(src, text[:off]...)
		src = append(src, patch...)
		src = append(src, text[off:]...)
		f, perrs := gsxparser.ParseFileWithClassifier(fset, path, src, 0, nil)
		if len(perrs) == 0 && f != nil {
			return repairResult{src: src, patch: patch, parsed: f}
		}
	}
	return repairResult{src: []byte(text), patch: ""}
}
```

Check `symbols.go`'s parser fallback for the exact classifier argument the LSP passes (`nil` vs a default classifier) and mirror it. Each patch attempt uses a FRESH `token.NewFileSet()` if reusing one across failed parses grows it — check how `symbols.go` handles this; prefer a fresh fset per attempt, returning the winning one in `repairResult` (add a `fset *token.FileSet` field if needed by Task 7 — it is: classification resolves positions).

Add the `fset` field to `repairResult` now:

```go
type repairResult struct {
	src    []byte
	patch  string
	parsed *gsxast.File
	fset   *token.FileSet // resolves parsed's positions
}
```

- [ ] **Step 4: Run, adjust hypotheses to observed parser behavior, re-run**

Run: `go test ./internal/lsp/ -run TestRepairAtCursor -v`
Expected: PASS with the patch list pruned to what reality needs. Document any pruning in the commit message.

- [ ] **Step 5: Commit**

```bash
git add internal/lsp/completion_repair.go internal/lsp/completion_repair_test.go
git commit -m "feat(lsp): completion repair chooser - parse-first with a closed insert-at-cursor patch set"
```

---

### Task 7: Cursor-context classification

**Files:**
- Create: `internal/lsp/completion_context.go`, `internal/lsp/completion_context_test.go`

**Interfaces:**
- Consumes: `repairResult` (Task 6), `inspectWithEmbedded` (`mapping.go:27`), `nodeNavSpans` (`definition.go:69`), AST position fields (`ast/ast.go`).
- Produces:
  ```go
  type completionContext struct {
      kind      completionContextKind
      node      gsxast.Node   // the matched node (Interp, Element, PipeStage owner, ...)
      exprPos   token.Pos     // for Go contexts: the matched fragment's start (bridge anchor)
      element   *gsxast.Element // for tag/attr/value contexts
      attr      gsxast.Attr     // for attr-value context: the StaticAttr
      qualifier string        // tag context: "pkg" when completing <pkg.▮; "" otherwise
      phantom   bool          // a "_" patch was applied at the cursor
  }
  type completionContextKind int
  const (
      ctxNone completionContextKind = iota
      ctxGoExpr       // Interp/ExprAttr/SpreadAttr/ClassPart/OrderedPair/value-form/ctrl-clause/GoBlock/GoChunk/@{} holes
      ctxPipeStage    // after |> inside a pipeline
      ctxTag          // tag-name region after <
      ctxAttrName     // inside an open tag, attribute-name position
      ctxAttrValue    // inside a StaticAttr string value
      ctxSigType      // inside a component signature params region
  )
  // classifyCompletionContext locates the innermost construct containing off
  // in r.parsed and maps it to a completionContext. off is in ORIGINAL buffer
  // coordinates == patched coordinates (patches insert at off).
  func classifyCompletionContext(r repairResult, path string, off int) completionContext
  ```

Classification rules (implement in this order; first match wins):

1. **Pipe stage:** walk with `inspectWithEmbedded`; for each node's `nodeNavSpans` stages (`PipeStage.NamePos`, len(Name)) — cursor within `[nameStart, nameStart+len(Name)]` (note: INCLUSIVE end — cursor sits at the end of the token being typed; same for all rules below) → `ctxPipeStage`. Also: cursor after a `|>` whose stage was phantom-repaired — the phantom `_` IS the stage name at the cursor, matched by the same span rule with `phantom=true`.
2. **Go expr:** for each node's `nodeNavSpans` primary spans — cursor within `[start, start+len]` → `ctxGoExpr` with that span's pos. Additionally `GoChunk`: cursor within the chunk's span → `ctxGoExpr` (GoChunk is not in nodeNavSpans; handle explicitly — its skeleton bridge is the source index, see Task 9).
3. **Tag:** cursor within `[TagPos, TagPos+len(Tag)]` of an `*gsxast.Element`, or immediately after a `<` that begins an element whose Tag is empty after repair. Dotted prefix: if `Tag` contains `.` and the cursor is after the dot, set `qualifier` to the part before the dot.
4. **Attr name:** cursor inside an element's open tag (between `TagPos+len(Tag)` and the first child/self-close), not inside any attr's value span → `ctxAttrName` with the element. A `BoolAttr` whose name span contains the cursor is also `ctxAttrName` (completing the half-typed name `cl`).
5. **Attr value:** cursor inside a `StaticAttr`'s value span. The value span is derived from the attr's `span`: `valueEnd = attrEnd - 1` (closing quote), `valueStart = valueEnd - len(Value)` — verify against a parsed example in the test before trusting; adjust if the span excludes quotes.
6. **Sig type:** cursor within `[ParamsPos, ParamsPos+len(Params)]` of a `*gsxast.Component` → `ctxSigType`.
7. Nothing matched → `ctxNone` (markup text, js/css bodies, import strings — v1 returns empty).

- [ ] **Step 1: Write the failing table test**

Table test with § cursor markers over one fixture file exercising every kind:

```go
func TestClassifyCompletionContext(t *testing.T) {
	cases := []struct {
		name, src string
		want      completionContextKind
	}{
		{"interp ident", "package p\n\ncomponent C(x string) {\n\t<div>{ x§ }</div>\n}\n", ctxGoExpr},
		{"interp trailing dot", "package p\n\ncomponent C(u U) {\n\t<div>{ u.§ }</div>\n}\n", ctxGoExpr},
		{"pipe stage", "package p\n\ncomponent C(x string) {\n\t<div>{ x |> up§ }</div>\n}\n", ctxPipeStage},
		{"pipe stage empty", "package p\n\ncomponent C(x string) {\n\t<div>{ x |> § }</div>\n}\n", ctxPipeStage},
		{"tag", "package p\n\ncomponent C() {\n\t<div><Ca§</div>\n}\n", ctxTag},
		{"html tag", "package p\n\ncomponent C() {\n\t<di§\n}\n", ctxTag},
		{"attr name", "package p\n\ncomponent C() {\n\t<div cl§\n}\n", ctxAttrName},
		{"attr value", "package p\n\ncomponent C() {\n\t<input type=\"§\"/>\n}\n", ctxAttrValue},
		{"expr attr is go", "package p\n\ncomponent C(x string) {\n\t<div class={ x§ }/>\n}\n", ctxGoExpr},
		{"goblock", "package p\n\ncomponent C() {\n\t{{ x§ := 1 }}\n\t<div>{ x }</div>\n}\n", ctxGoExpr},
		{"gochunk", "package p\n\nfunc helper() string { return x§ }\n\ncomponent C() {\n\t<div/>\n}\n", ctxGoExpr},
		{"for clause", "package p\n\ncomponent C(xs []string) {\n\t{for _, v := range x§s}\n\t\t<div>{ v }</div>\n\t{/for}\n}\n", ctxGoExpr},
		{"sig params", "package p\n\ncomponent C(u Us§) {\n\t<div/>\n}\n", ctxSigType},
		{"markup text none", "package p\n\ncomponent C() {\n\t<div>hel§lo</div>\n}\n", ctxNone},
		{"dotted tag qualifier", "package p\n\nimport \"example.com/app/ui\"\n\ncomponent C() {\n\t<ui.§/>\n}\n", ctxTag},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			off := strings.Index(tc.src, "§")
			text := strings.Replace(tc.src, "§", "", 1)
			r := repairAtCursor(token.NewFileSet(), "/tmp/x.gsx", text, off)
			if r.parsed == nil {
				t.Fatal("fixture did not parse/repair")
			}
			got := classifyCompletionContext(r, "/tmp/x.gsx", off)
			if got.kind != tc.want {
				t.Fatalf("kind = %v, want %v", got.kind, tc.want)
			}
		})
	}
	// dotted tag: also assert qualifier == "ui"
}
```

Adjust the control-flow syntax in fixtures ({for}/{if}) to the real gsx syntax — copy from an existing corpus file (`internal/codegen/testdata/` txtar cases) rather than guessing.

- [ ] **Step 2: Run to verify failure** — compile FAIL.

- [ ] **Step 3: Implement** `classifyCompletionContext` per the ordered rules. Reuse `nodeNavSpans` verbatim — do not duplicate its span table. The walk mirrors `exprNodeAtOffset` (`definition.go:121`) but with inclusive span ends (`off <= start+len` instead of `<`): completion's cursor sits AFTER the last typed char. Write one shared helper `spanContainsForCompletion(start, length, off int) bool { return off >= start && off <= start+length }`.

- [ ] **Step 4: Run tests** — PASS, including the full package.

- [ ] **Step 5: Commit**

```bash
git add internal/lsp/completion_context.go internal/lsp/completion_context_test.go
git commit -m "feat(lsp): completion cursor-context classification over the repaired parse"
```

---

### Task 8: Items infrastructure — token span, sortText tiers, item builders

**Files:**
- Create: `internal/lsp/completion_items.go`, `internal/lsp/completion_items_test.go`

**Interfaces:**
- Produces:
  ```go
  // completionTokenSpan scans identifier bytes backward from off in text and
  // returns the [start, off) byte span of the token being completed. Identifier
  // bytes: letters, digits, '_', and for attr/tag contexts also '-'. A dot is
  // never part of the token (member completion replaces only the selector).
  func completionTokenSpan(text string, off int, allowDash bool) (start, end int)

  // sort tiers (lower sorts first). sortText = fmt.Sprintf("%02d%s", tier, label).
  const (
      tierLocal      = 5  // locals, params
      tierMember     = 10 // +embedding depth (10..29 clamped)
      tierPackage    = 30 // package-scope decls
      tierImported   = 40 // imported package names / their members
      tierUniverse   = 50
      tierKeyword    = 60
      tierContext    = 5  // context-native items: filters, components, attrs
      tierSecondary  = 20 // e.g. HTML tags merged under a capitalized prefix
  )

  // newCompletionItem builds an item whose TextEdit replaces [start,end) in
  // text with newText, in ORIGINAL buffer coordinates via rangeForSpan
  // (hover.go:256) and the negotiated encoding.
  func newCompletionItem(text string, start, end int, enc encoding, label, newText string, kind, tier int, detail string, doc *MarkupContent) CompletionItem
  ```
  `FilterText` is set to `newText` when `newText != label` (e.g. attr `name=""` inserts).

- [ ] **Step 1: Write the failing tests**

```go
func TestCompletionTokenSpan(t *testing.T) {
	cases := []struct{ name, text string; off int; allowDash bool; wantStart, wantEnd int }{
		{"mid ident", "{ user.Na }", 9, false, 7, 9},   // token "Na"
		{"after dot", "{ user. }", 7, false, 7, 7},      // empty token at cursor
		{"attr dash", "<div hx-ge", 10, true, 5, 10},    // token "hx-ge"
		{"start of file", "ab", 2, false, 0, 2},
		{"utf8 before token", "é{ x", 4, false, 3, 4},
	}
	...
}

func TestNewCompletionItemEdit(t *testing.T) {
	// UTF-16: a 𝔘 (surrogate pair) before the token shifts Character by 2.
	text := "{ 𝔘x.Na }"
	// compute offsets for the "Na" token; assert TextEdit.Range characters
	// match positionForByteOffset(text, ...) for encUTF16 and encUTF8.
	...
}
```

Fill in the assertions concretely against `positionForByteOffset` — the test must lock the UTF-16 behavior.

- [ ] **Step 2: Run to verify failure** — compile FAIL.

- [ ] **Step 3: Implement**

```go
func completionTokenSpan(text string, off int, allowDash bool) (int, int) {
	if off > len(text) {
		off = len(text)
	}
	start := off
	for start > 0 {
		r, size := utf8.DecodeLastRuneInString(text[:start])
		if r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) || (allowDash && r == '-') {
			start -= size
			continue
		}
		break
	}
	return start, off
}
```

`newCompletionItem` assembles the struct; `sortText` via `fmt.Sprintf("%02d%s", tier, label)`; range via `rangeForSpan(text, start, end, enc)`.

- [ ] **Step 4: Run tests** — PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/lsp/completion_items.go internal/lsp/completion_items_test.go
git commit -m "feat(lsp): completion item builders - token span, sort tiers, authored-coordinate edits"
```

---

### Task 9: Go identifier-position enumeration (scope walk)

**Files:**
- Create: `internal/lsp/completion_go.go`, `internal/lsp/completion_go_test.go`
- Modify: `internal/lsp/completion.go` (wire the ctxGoExpr path)

**Interfaces:**
- Consumes: ephemeral `*Package` (Task 5), `completionContext` (Task 7), bridge recipe (`hover.go:176-204`), items (Task 8).
- Produces:
  ```go
  // goCompletionItems enumerates candidates for a Go-context cursor bridged to
  // skelPos in pkg's skeleton. memberOnly selects the after-dot path (Task 10).
  func goCompletionItems(pkg *Package, skel ast.Expr, skelPos token.Pos, statementCtx bool, text string, start, end int, enc encoding) []CompletionItem
  ```

**Bridging in the handler (completion.go), for `ctxGoExpr` on Interp-family nodes:**

```go
skel := eph.ExprMap[cc.node]
if skel == nil { /* try CtrlMap / SigTypes below, else empty */ }
exprStart := eph.GSXFset.Position(cc.exprPos).Offset
skelPos := skel.Pos() + token.Pos(off-exprStart)
```

For `CtrlMap` nodes: `ref := eph.CtrlMap[cc.node]; skelPos := ref.ClauseStart + token.Pos(off-clauseStartOffset)` with `ref.Node` as the containing node (mirror `ctrlObjectAt`, `definition.go:685`). For `ctxSigType`: mirror `signatureTypeIdentAt` (`definition.go:187`). For `GoChunk`: no ExprMap entry exists — find the skeleton position via `pkg.SourceIndex` capability segments; if that is not directly exposed, use the innermost-scope-by-Info fallback below with a pos obtained from any occurrence: **simplest correct v1**: for GoChunk, find the innermost `types.Scope` whose source-mapped span contains the cursor via the scope-iteration approach (it needs only `Info.Scopes` positions, which live in skeleton coordinates that `//line` maps back to the .gsx — resolve each scope's `Pos()` through `pkg.Fset.Position` and compare `.Filename/.Offset` against the authored path/off). Write this as its own helper `innermostScopeAtAuthored(pkg, path, off)`.

**Scope enumeration:**

```go
// scopeCandidates walks from the innermost scope at pos outward, collecting
// visible names. Inner declarations shadow outer ones. Function-local objects
// declared after pos are excluded (Go's declaration-order rule); package
// scope and above are order-independent.
func scopeCandidates(pkg *Package, scope *types.Scope, pos token.Pos) []scopedObject {
	type scopedObject = struct {
		obj  types.Object
		tier int
	}
	seen := map[string]bool{}
	var out []scopedObject
	pkgScope := pkg.Types.Scope()
	for s := scope; s != nil; s = s.Parent() {
		local := s != types.Universe && s != pkgScope && !isFileScope(pkg, s)
		for _, name := range s.Names() {
			if seen[name] || name == "_" {
				continue
			}
			obj := s.Lookup(name)
			if local && obj.Pos().IsValid() && pos.IsValid() && obj.Pos() > pos {
				continue // declared after the cursor
			}
			seen[name] = true
			tier := tierLocal
			switch {
			case s == types.Universe:
				tier = tierUniverse
			case s == pkgScope:
				tier = tierPackage
			case isFileScope(pkg, s): // imported package names
				tier = tierImported
			}
			out = append(out, scopedObject{obj, tier})
		}
	}
	return out
}
```

`isFileScope`: a scope is a file scope iff its parent is the package scope AND it is not a function scope — in go/types, file scopes are exactly the children of the package scope that appear in `Info.Scopes[*ast.File]`. Implement by collecting `fileScopes := map[*types.Scope]bool` from `Info.Scopes` where the key's dynamic type is `*ast.File`.

**Finding the innermost scope** (no skeleton `*ast.File` retained in `lsp.Package`):

```go
// innermostScopeAt picks the smallest Info.Scopes entry containing pos, then
// the caller climbs Parent(). Falls back to the package scope.
func innermostScopeAt(pkg *Package, pos token.Pos) *types.Scope {
	var best *types.Scope
	for _, s := range pkg.Info.Scopes {
		if !s.Contains(pos) {
			continue
		}
		if best == nil || (s.Pos() >= best.Pos() && s.End() <= best.End()) {
			best = s
		}
	}
	if best == nil {
		return pkg.Types.Scope()
	}
	return best.Innermost(pos)
}
```

**Item mapping** per object: `*types.Var` field→ciKindField / other→ciKindVariable; `*types.Func`→ciKindFunction (ciKindMethod when signature has receiver); `*types.Const`→ciKindConstant; `*types.TypeName`→ciKindStruct/ciKindInterface by underlying (default ciKindClass); `*types.PkgName`→ciKindModule with detail = import path; detail otherwise `types.ObjectString(obj, qualifierFor(pkg))` TRIMMED to the type part (use `types.TypeString(obj.Type(), qualifierFor(pkg))` for vars/fields; full ObjectString for funcs).

**Keywords** (statementCtx only — GoBlock and GoChunk contexts): `if, for, switch, select, return, var, const, type, func, defer, go, break, continue, fallthrough, range, struct, interface, map, chan` — tier tierKeyword, kind ciKindKeyword.

- [ ] **Step 1: Write failing unit tests** against a hand-built typechecked fixture: typecheck a small SYNTHETIC Go package in the test (plain `go/types` on `go/parser` output — no gsx involved) and call `scopeCandidates`/`innermostScopeAt` directly:

```go
func TestScopeCandidates(t *testing.T) {
	src := `package p
import "strings"
var global = 1
func f(param int) {
	local := 2
	_ = local
	// POS
	after := 3
	_ = after
}`
	// parse+typecheck with Info{Scopes, Defs, Uses}; find POS offset; assert:
	// - local, param, global, strings, f present
	// - after ABSENT (declared after pos)
	// - strings has tierImported, global tierPackage, local/param tierLocal
	// - universe entries (println, error, true) present with tierUniverse
}
```

- [ ] **Step 2: Run to verify failure** — compile FAIL.
- [ ] **Step 3: Implement** per above; wire `ctxGoExpr` (ident path) in `handleCompletion`: call `s.analyzer.AnalyzeEphemeral(filepath.Dir(path), path, r.src)`, fail-soft on `err != nil` or `pkg.Info == nil` (→ empty list for Go contexts), bridge, enumerate, build items with the token span from Task 8.
- [ ] **Step 4: Run tests** — `go test ./internal/lsp/ -run 'TestScope|TestCompletion' -v` PASS.
- [ ] **Step 5: Commit**

```bash
git add internal/lsp/completion_go.go internal/lsp/completion_go_test.go internal/lsp/completion.go
git commit -m "feat(lsp): Go identifier completion - scope-chain enumeration at the bridged position"
```

---

### Task 10: Member-position enumeration (after `.`)

**Files:**
- Modify: `internal/lsp/completion_go.go`
- Test: `internal/lsp/completion_go_test.go`

**Interfaces:**
- Produces:
  ```go
  // memberCandidates enumerates selectable members of T: methods of T and *T
  // via types.NewMethodSet, plus fields found by breadth-first embedded-field
  // walk with promotion (shallower depth shadows deeper same-name). Unexported
  // members only when T's package == samePkg. Info.Selections is NOT used
  // (never allocated by the core — probe 2026-07-21).
  func memberCandidates(T types.Type, samePkg *types.Package) []memberObject
  type memberObject struct {
      obj   types.Object
      depth int // embedding depth, 0 = direct
  }
  ```

**Dispatch rule in `goCompletionItems`:** after bridging, `id := innermostIdent(skel, skelPos)` (`mapping.go:51`); if `id` is the `Sel` of an enclosing `*ast.SelectorExpr` (walk `skel` with `ast.Inspect` to find a SelectorExpr whose Sel == id):
- `X`'s type: `tv, ok := pkg.Info.Types[selExpr.X]`. If `X` resolves to a `*types.PkgName` (check `pkg.Info.Uses[xIdent]`): candidates = exported `Names()` of `pkgName.Imported().Scope()`, tier tierImported.
- Else `T := tv.Type` → `memberCandidates(T, pkg.Types)`, tier `tierMember+depth` (clamp 29).
Else → the Task 9 scope path.

**memberCandidates implementation:**

```go
func memberCandidates(T types.Type, samePkg *types.Package) []memberObject {
	var out []memberObject
	seen := map[string]bool{}
	include := func(obj types.Object, depth int) {
		if obj == nil || seen[obj.Name()] {
			return
		}
		if !obj.Exported() && (obj.Pkg() == nil || samePkg == nil || obj.Pkg() != samePkg) {
			return
		}
		seen[obj.Name()] = true
		out = append(out, memberObject{obj, depth})
	}
	// Methods: NewMethodSet on *T sees both pointer and value methods. For
	// non-addressable T this over-offers pointer methods; acceptable for
	// completion (the type checker flags misuse; matching gopls behavior).
	mset := types.NewMethodSet(types.NewPointer(T))
	for sel := range mset.Methods() { // go1.24 iterator; use i-loop if unavailable
		include(sel.Obj(), len(sel.Index())-1)
	}
	// Fields: BFS over embedded structs, depth-tracked, promotion shadowing.
	type queued struct {
		t     types.Type
		depth int
	}
	q := []queued{{T, 0}}
	for len(q) > 0 {
		cur := q[0]
		q = q[1:]
		t := cur.t
		if p, ok := t.Underlying().(*types.Pointer); ok {
			t = p.Elem()
		}
		st, ok := t.Underlying().(*types.Struct)
		if !ok {
			continue
		}
		for i := 0; i < st.NumFields(); i++ {
			f := st.Field(i)
			include(f, cur.depth)
			if f.Embedded() {
				q = append(q, queued{f.Type(), cur.depth + 1})
			}
		}
	}
	return out
}
```

(Check the go version in go.mod for the MethodSet iteration form; use `for i := 0; i < mset.Len(); i++ { sel := mset.At(i) ... }` — that is version-safe. Use it.)

NOTE promotion subtlety: `include` dedups by name in BFS order, so a depth-0 field shadows a depth-1 promoted field of the same name — correct. Methods are included before fields; a field/method name collision keeps the method — Go actually forbids selecting either ambiguously at the same depth; completion offering the method is acceptable.

- [ ] **Step 1: Write failing tests** — synthetic go/types fixture:

```go
func TestMemberCandidates(t *testing.T) {
	src := `package p
type Base struct{ Shared, base int }
type T struct {
	Base
	Name string
	priv int
}
func (T) M() {}
func (*T) PM() {}`
	// typecheck; T := lookup "T".Type()
	// same-package: want Name(0), priv(0), Base(0), Shared(1), base(1), M, PM
	// other-package (samePkg=nil): want Name, Base, Shared, M, PM only
	// depth: Shared == 1, Name == 0
}
```

Also a dispatch test: bridge a `u.Na` SelectorExpr fixture and assert the member path is taken; a plain `x` ident asserts the scope path.

- [ ] **Step 2: Run to verify failure** — FAIL.
- [ ] **Step 3: Implement**, wire into `goCompletionItems` and the handler (the trailing-dot case: `{ user. }` parses as gsx, skeleton fails → the HANDLER applies the phantom before AnalyzeEphemeral for Go contexts: if `cc.kind == ctxGoExpr` and `off > 0 && text[off-1] == '.'`, insert `_` at off into `r.src` before calling AnalyzeEphemeral, and treat the bridged `_` ident as an empty-prefix member cursor. This is the SECOND patch site — Task 6's chooser handles gsx-parse repairs; this handles skeleton repairs. Keep them separate and documented.)
- [ ] **Step 4: Run tests** — PASS.
- [ ] **Step 5: Commit**

```bash
git add internal/lsp/completion_go.go internal/lsp/completion_go_test.go internal/lsp/completion.go
git commit -m "feat(lsp): member completion after dot - method set + embedded-field BFS, phantom skeleton repair"
```

---

### Task 11: Pipe-filter completion

**Files:**
- Create: `internal/lsp/completion_gsx.go`, `internal/lsp/completion_gsx_test.go`
- Modify: `internal/lsp/completion.go`

**Interfaces:**
- Consumes: `pkg.Filters` (Task 2), `ctxPipeStage` (Task 7).
- Produces:
  ```go
  // filterItems: one item per resolved filter. label = Name, kind = Function,
  // detail = Pkg.Func ("+ ctx" suffix when WantsCtx), tier = tierContext.
  func filterItems(filters []FilterCandidate, text string, start, end int, enc encoding) []CompletionItem
  ```

**Handler wiring:** `ctxPipeStage` → ephemeral result's `Filters`; if the ephemeral result is a shell (nil Info AND empty Filters), fall back to the retained `s.pkgs[dir]`'s Filters (names are position-independent — safe under staleness); both empty → empty list.

- [ ] **Step 1: Failing test** — `filterItems` unit test (labels, detail rendering, sorted stable) + a handler-level test using a fake analyzer whose `AnalyzeEphemeral` returns a `*Package{Filters: ...}`:

```go
func TestFilterItems(t *testing.T) {
	fs := []FilterCandidate{
		{Name: "upper", Pkg: "github.com/gsxhq/gsx/std", Func: "Upper"},
		{Name: "urlFor", Pkg: "example.com/sp", Func: "URLFor", WantsCtx: true},
	}
	items := filterItems(fs, "{ x |> up }", 7, 9, encUTF8)
	// want labels [upper urlFor]; detail[0] = "github.com/gsxhq/gsx/std.Upper";
	// detail[1] ends with "(ctx)"; kinds ciKindFunction; edits replace [7,9).
}
```

- [ ] **Step 2: Run** — FAIL. **Step 3: Implement + wire.** **Step 4: Run** — PASS.
- [ ] **Step 5: Commit**

```bash
git add internal/lsp/completion_gsx.go internal/lsp/completion_gsx_test.go internal/lsp/completion.go
git commit -m "feat(lsp): pipe-stage completion from the resolved filter table"
```

---

### Task 12: Component-tag completion

**Files:**
- Modify: `internal/lsp/completion_gsx.go`, `internal/lsp/completion.go`
- Test: `internal/lsp/completion_gsx_test.go`

**Interfaces:**
- Consumes: `pkg.ComponentDecls` (`map[ComponentDeclKey][]sourceintel.VersionedSpan`, survives type errors after Task 1), `pkg.Types` (import qualifiers), `ctxTag` with `qualifier`.
- Produces:
  ```go
  // componentTagItems enumerates component candidates for a tag cursor.
  // qualifier == "": current-package components (ComponentKey without a dot —
  // receiver components are <recv.Name> and need a receiver expr; excluded in
  // v1, follow-up in spec) + imported-package qualifiers (one item per
  // imported gsx package that has decls, inserting "pkgname.").
  // qualifier != "": components of the import whose package NAME == qualifier.
  func componentTagItems(pkg *Package, qualifier string, capitalizedPrefix bool, text string, start, end int, enc encoding) []CompletionItem
  ```
  Kind ciKindFunction, tier tierContext; when `capitalizedPrefix` is false and the context will also merge HTML tags (Task 15), components still get tierContext and HTML tags tierContext too — capitalization instead flips which list gets tierSecondary (see Task 15 merge rule).

Resolution details:
- Current package path: `pkg.Types.Path()` (nil-guard: fall back to matching ComponentDeclKeys whose PackagePath equals none of the imports — simpler: require `pkg.Types != nil`, else offer nothing; the fail-soft retained-pkg fallback usually has Types).
- ComponentKey format: confirm against `crossRefKeyForFunc` (`gen/lsp.go:919`) and an existing ComponentDecls-consuming test — the plan's assumption: plain components use the bare name; receiver components contain a dot. Verify; if wrong, adapt the dot-exclusion rule and say so in the commit.
- Import qualifiers: iterate `pkg.Types.Imports()`; offer `imp.Name()` (insert `imp.Name()+"."`) only when some `ComponentDeclKey.PackagePath == imp.Path()` (i.e. it is a gsx package with components).
- `qualifier != ""`: find the import with `Name() == qualifier`; offer its ComponentKeys (dot-free ones).

**Handler wiring:** `ctxTag` → ephemeral pkg; shell → retained `s.pkgs[dir]` fallback; both missing → HTML-only (after Task 15; until then, empty).

- [ ] **Step 1: Failing tests** — hand-built `*Package` fixtures (the `definition_test.go` pattern): a Package with `Types` = a synthesized `types.Package` with one import, ComponentDecls containing `{pkgPath, "Card"}`, `{pkgPath, "Page.Row"}` (receiver — excluded), `{impPath, "Button"}`. Assert bare-cursor candidates = [Card, ui.] and qualified("ui") = [Button].
- [ ] **Step 2: Run** — FAIL. **Step 3: Implement + wire.** **Step 4: Run** — PASS.
- [ ] **Step 5: Commit**

```bash
git add internal/lsp/completion_gsx.go internal/lsp/completion_gsx_test.go internal/lsp/completion.go
git commit -m "feat(lsp): component tag completion from ComponentDecls (local + imported, qualified tags)"
```

---

### Task 13: Component-attribute completion

**Files:**
- Modify: `internal/lsp/completion_gsx.go`, `internal/lsp/completion.go`
- Test: `internal/lsp/completion_gsx_test.go`

**Interfaces:**
- Consumes: `pkg.ComponentCalls[element]` → `ComponentCallFact.Signature` (`*types.Signature`) and `.Params` (present attrs), reserved names rule (ctx/children/attrs are never attr candidates).
- Produces:
  ```go
  // componentAttrItems: candidates = Signature params minus reserved names
  // (ctx, children, attrs) minus params already bound by an authored attr on
  // this element (fact.Params values' Names). label = param name, kind =
  // ciKindField, detail = param type string via qualifierFor, newText =
  // name + "={}" is NOT inserted in v1 — plain name only (spec: no snippets).
  func componentAttrItems(pkg *Package, el *gsxast.Element, text string, start, end int, enc encoding) []CompletionItem
  ```
  No fact for the element (call not planned) → nil (fail-soft; spec records the fallback as a follow-up).

- [ ] **Step 1: Failing test** — hand-built Package: synthesize a `types.Signature` with params `(ctx context.Context, title string, count int, children gsx.Node)` (build with `types.NewSignatureType`/`types.NewVar` — no real gsx needed), an Element with one authored attr bound to `title`. Want candidates = [count] only.
- [ ] **Step 2: Run** — FAIL. **Step 3: Implement + wire** (ctxAttrName + `el.IsComponent` → this path; note `IsComponent` is stamped by codegen, so read it from the EPHEMERAL result's Files — the classification in Task 7 ran on the handler's own parse whose elements are unstamped. Bridge: locate the same element in `eph.Files[path]` by `TagPos` offset equality; write a small helper `elementAtTagOffset(eph, path, tagOff) *gsxast.Element`. If the ephemeral result is a shell, fall back to nil → empty.)
- [ ] **Step 4: Run** — PASS. **Step 5: Commit**

```bash
git add internal/lsp/completion_gsx.go internal/lsp/completion_gsx_test.go internal/lsp/completion.go
git commit -m "feat(lsp): component attribute completion from planned call signatures"
```

---

### Task 14: internal/htmldata — vendored VS Code HTML dataset + generator

**Files:**
- Create: `internal/htmldata/browsers.html-data.json` (vendored), `internal/htmldata/LICENSE.vendored`, `internal/htmldata/gen/main.go` (generator), `internal/htmldata/htmldata.go` (package doc + `//go:generate`), `internal/htmldata/table.gen.go` (generated), `internal/htmldata/htmldata_test.go`

**Interfaces:**
- Produces:
  ```go
  package htmldata

  type Value struct{ Name, Doc string }
  type Attribute struct {
      Name     string
      Doc      string  // markdown; includes MDN reference link when present
      ValueSet string  // key into ValueSets; "" = freeform; "v" = boolean-ish per vscode convention
  }
  type Tag struct {
      Name  string
      Doc   string
      Attrs []Attribute
  }
  var Tags []Tag                    // sorted by Name
  var GlobalAttributes []Attribute  // sorted by Name
  var ValueSets map[string][]Value
  // Boolean reports the vscode "v" valueSet convention (presence-only attr).
  func (a Attribute) Boolean() bool { return a.ValueSet == "v" }
  ```

- [ ] **Step 1: Vendor the data**

```bash
mkdir -p internal/htmldata/gen
curl -fsSL -o internal/htmldata/browsers.html-data.json https://unpkg.com/@vscode/web-custom-data/data/browsers.html-data.json
curl -fsSL -o internal/htmldata/LICENSE.vendored https://raw.githubusercontent.com/microsoft/vscode-custom-data/main/LICENSE
```

Verify: `head -c 400 internal/htmldata/browsers.html-data.json` shows `{"version":1.1,"tags":[...`. If unpkg is unreachable, STOP and surface it (do not hand-write data).

- [ ] **Step 2: Write the failing test**

```go
package htmldata

import "testing"

func TestGeneratedTable(t *testing.T) {
	if len(Tags) < 100 {
		t.Fatalf("Tags = %d, want the full HTML element set (>100)", len(Tags))
	}
	var div *Tag
	for i := range Tags {
		if Tags[i].Name == "div" {
			div = &Tags[i]
		}
	}
	if div == nil || div.Doc == "" {
		t.Fatal("div missing or undocumented")
	}
	var hasClass bool
	for _, a := range GlobalAttributes {
		if a.Name == "class" {
			hasClass = true
		}
	}
	if !hasClass {
		t.Fatal("global attribute class missing")
	}
	// input[type] must carry a value set with submit/button members.
	var input *Tag
	for i := range Tags {
		if Tags[i].Name == "input" {
			input = &Tags[i]
		}
	}
	if input == nil {
		t.Fatal("input missing")
	}
	var typeAttr *Attribute
	for i := range input.Attrs {
		if input.Attrs[i].Name == "type" {
			typeAttr = &input.Attrs[i]
		}
	}
	if typeAttr == nil || typeAttr.ValueSet == "" {
		t.Fatal("input[type] missing or without a value set")
	}
	found := false
	for _, v := range ValueSets[typeAttr.ValueSet] {
		if v.Name == "submit" {
			found = true
		}
	}
	if !found {
		t.Fatal("input[type] value set missing submit")
	}
	// hidden is boolean via the "v" set.
	var hidden bool
	for _, a := range GlobalAttributes {
		if a.Name == "hidden" && a.Boolean() {
			hidden = true
		}
	}
	if !hidden {
		t.Fatal("hidden not classified boolean (valueSet v)")
	}
}
```

- [ ] **Step 3: Write the generator** (`internal/htmldata/gen/main.go`)

The JSON schema (vscode-html-languageservice custom-data format): top level `{version, tags[], globalAttributes[], valueSets[]}`; `tags[]`: `{name, description, attributes[], references[]}`; `attributes[]`: `{name, description?, valueSet?, values?[]}`; `valueSets[]`: `{name, values[]}`; every `description` is EITHER a string OR `{kind, value}`. `references[]`: `{name, url}`.

```go
// Command gen regenerates table.gen.go from browsers.html-data.json
// (@vscode/web-custom-data, MIT — see LICENSE.vendored).
package main

// flexDesc decodes both description encodings.
type flexDesc struct{ Value string }

func (d *flexDesc) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		d.Value = s
		return nil
	}
	var o struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(b, &o); err != nil {
		return err
	}
	d.Value = o.Value
	return nil
}

// ... raw mirror structs for the schema above, then:
// docFor(desc flexDesc, refs []ref) string — desc.Value, plus
// "\n\n[MDN Reference](<url>)" for the first reference named "MDN Reference".
// main() reads the JSON, sorts tags/globalAttributes/attrs by name, emits
// table.gen.go via a text/template or fmt.Fprintf writer with %q escaping,
// header "// Code generated by internal/htmldata/gen. DO NOT EDIT.",
// and gofmt's the output with go/format.Source before writing.
```

Write the full generator (it is ~150 lines of mechanical decoding + printing; every struct field mirrors the schema names above). `htmldata.go` carries:

```go
// Package htmldata is the HTML tag/attribute/value completion dataset,
// generated from the vendored @vscode/web-custom-data browsers.html-data.json
// (MIT, LICENSE.vendored). Regenerate after replacing the JSON:
//
//go:generate go run ./gen
package htmldata
```

- [ ] **Step 4: Generate and test**

Run: `cd internal/htmldata && go generate ./... && cd ../.. && go test ./internal/htmldata/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/htmldata/
git commit -m "feat(htmldata): vendor @vscode/web-custom-data HTML dataset with a generated Go table"
```

---

### Task 15: HTML tag/attr/value completion + tag-position merge + htmx

**Files:**
- Create: `internal/lsp/completion_html.go`, `internal/lsp/completion_html_test.go`
- Modify: `internal/lsp/completion.go`, `internal/lsp/completion_gsx.go` (merge)

**Interfaces:**
- Consumes: `internal/htmldata` (Task 14), `gsx.IsBooleanAttr` (root package — check the exact exported name in `boolattr.go` before importing; internal/lsp may import the root gsx package, the root does not import internal/lsp), Element attr presence.
- Produces:
  ```go
  // htmlTagItems: one item per htmldata.Tag; kind ciKindProperty, doc = Tag.Doc
  // (markdown). Tier: tierContext normally; tierSecondary when the typed
  // prefix is capitalized (components lead).
  func htmlTagItems(prefixCapitalized bool, text string, start, end int, enc encoding) []CompletionItem

  // htmlAttrItems: per-tag attrs + GlobalAttributes, minus attrs already
  // present on el (by lowercase name). Boolean attrs (htmldata .Boolean() OR
  // gsx.IsBooleanAttr) insert the bare name; value attrs insert `name=""`
  // with FilterText = name. Plus hx-* attributes when htmx is enabled.
  func htmlAttrItems(el *gsxast.Element, tagName string, htmxEnabled bool, text string, start, end int, enc encoding) []CompletionItem

  // htmlValueItems: enumerated values for el's attr from its ValueSet; empty
  // for freeform attrs. kind ciKindEnumMember.
  func htmlValueItems(tagName, attrName string, text string, start, end int, enc encoding) []CompletionItem
  ```

**Merge rule (tag position):** `ctxTag` items = `componentTagItems(...) ++ htmlTagItems(...)`; when the typed prefix starts with an uppercase letter, components get tierContext and HTML tierSecondary; lowercase or empty prefix: both tierContext (client filter decides; lowercase components remain reachable per the lowercase-tag rule).

**htmx:** determined per-dir. Plumb it as `pkg.URLPresets []string` on `lsp.Package`/`PackageResult`: in codegen, populate from the same effective config `classifierFor(dir)` consults (`module.go:920-925` — follow it to where preset names live; if the preset names are not retrievable as strings, STOP and surface — do not infer from classifier rule contents). `htmxEnabled = slices.Contains(pkg.URLPresets, "htmx")`. The hx-* completion list: vendor a second custom-data JSON `internal/htmldata/htmx-data.json` in the same schema, generated table appended by the same generator (`HTMXAttributes []Attribute`). Content: transcribe the attribute list and one-line descriptions from https://htmx.org/reference/#attributes (all `hx-*` attributes on that page, each Doc ending with a link to `https://htmx.org/attributes/<name>/`). This is documented-data transcription, not invention; keep it complete (both the core table and the "additional attribute reference" table on that page).

- [ ] **Step 1: Failing tests** — table tests over the three item functions: div attrs exclude present `class`; `hidden` inserts bare name; `type` on input inserts `type=""`; input[type] values include submit; hx-get present only when htmxEnabled. Handler-level: `<di▮` yields div (Property, doc non-empty) merged after/with components per tier rule.
- [ ] **Step 2: Run** — FAIL. **Step 3: Implement + wire** (ctxTag merge, ctxAttrName html path, ctxAttrValue path).
- [ ] **Step 4: Run** — `go test ./internal/lsp/ ./internal/htmldata/` PASS.
- [ ] **Step 5: Commit**

```bash
git add internal/lsp/completion_html.go internal/lsp/completion_html_test.go internal/lsp/completion.go internal/lsp/completion_gsx.go internal/htmldata/ internal/codegen/ internal/lsp/analysis.go gen/lsp.go
git commit -m "feat(lsp): HTML tag/attr/value completion from vendored dataset; tag-position merge; htmx preset attrs"
```

---

### Task 16: End-to-end suite + latency benchmark

**Files:**
- Modify: `gen/lsp_completion_e2e_test.go`

**Interfaces:** consumes everything; this is the spec's acceptance list.

- [ ] **Step 1: Write the e2e tests** (real `lspAnalyzer` + real Server over in-memory transport, following `gen/lsp_hover_e2e_test.go`'s harness). One fixture module: `page/types.go` (`type User struct{ Name string; Age int }`), `page/other.gsx` (`component Other(title string) { <div>{ title }</div> }`), `ui/button.gsx` in a second package (`component Button(label string) { <button>{ label }</button> }`), `page/page.gsx` driven per-case via didOpen/didChange. Cases (each opens the buffer with a § cursor, sends `textDocument/completion`, asserts on labels):

```
1.  member:            { user.§ }         → contains Name, Age; edit replaces empty span at cursor
2.  member prefix:     { user.N§ }        → contains Name; TextEdit range covers the "N" token
3.  scope ident:       { us§ }            → contains user (param); contains Other (package scope, tierPackage sorts after)
4.  package member:    { strings.§ }      → (fixture imports strings) contains ToUpper; unexported absent
5.  pipe:              { user.Name |> u§ }→ contains upper, urlquery
6.  pipe empty stage:  { user.Name |> § } → non-empty filter list
7.  component tag:     <Ot§              → contains Other; contains ui. qualifier item
8.  qualified tag:     <ui.§             → contains Button
9.  component attr:    <Other t§/>       → contains title; ctx/children absent
10. html tag:          <di§              → contains div with markdown doc
11. html attr:         <div cl§          → contains class (inserts class=""), hidden (bare)
12. html value:        <input type="§"/> → contains submit
13. fail-soft other-file breakage: other.gsx overridden broken + { user.§ } → empty items, no error reply
14. package-clause mismatch: page.gsx `package pag` + completion → empty items, no error reply
15. utf16: body `{ 𝔘 := user; 𝔘.§ }` → member items with correct UTF-16 TextEdit ranges
16. go-file: completion on types.go URI → null
```

Write each as a real subtest with concrete buffer text and concrete expected labels (presence assertions, not full-list equality — the lists are large).

- [ ] **Step 2: Run** — `go test ./gen/ -run TestCompletionE2E -v`. Fix integration fallout; each fix belongs to the task that owns the broken unit (amend there mentally, but commit here — this is the integration task).

- [ ] **Step 3: Benchmark**

```go
func BenchmarkAnalyzeEphemeralWarm(b *testing.B) {
	// fixture from the e2e suite, pre-warmed with one Package() call;
	// b.Loop: AnalyzeEphemeral with the { user._ } buffer.
}
```

Run: `go test ./gen/ -bench BenchmarkAnalyzeEphemeralWarm -benchtime=10x -run XXX`
Record the observed latency in the final commit message and in `docs/guide/status.md` (Task 17). There is no pass/fail bar — this is the measured baseline the spec requires before any tuning talk.

- [ ] **Step 4: Full suite**

Run: `make ci`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add gen/lsp_completion_e2e_test.go
git commit -m "test(gen): completion end-to-end suite + warm ephemeral-analysis benchmark"
```

---

### Task 17: Docs + ROADMAP

**Files:**
- Modify: `docs/guide/editor.md`, `docs/guide/status.md`, `docs/ROADMAP.md`

- [ ] **Step 1: editor.md** — add completion to the feature list. Style: match the existing entries' brevity (the docs-concise rule: state behavior plainly, one line of rationale max). Content: completion works in Go expressions (identifiers, members), pipe stages, component tags/attributes, HTML tags/attributes/values; no snippet placeholders; auto-import completion not yet (quickfix covers it).
- [ ] **Step 2: status.md** — flip completion from pending to shipped; note the measured warm latency from Task 16.
- [ ] **Step 3: ROADMAP.md** — tick the completion design line (`docs/ROADMAP.md:566` area), pointing to the spec; add follow-up bullets from the spec's Follow-ups section (auto-import completion, expected-type ranking, snippets, typed pipe filtering, resolve, method-component tags).
- [ ] **Step 4: Verify docs build if the repo has a docs check** (`grep docs Makefile`; run what exists).
- [ ] **Step 5: Commit**

```bash
git add docs/guide/editor.md docs/guide/status.md docs/ROADMAP.md
git commit -m "docs: LSP completion shipped - editor guide, status, roadmap"
```

---

## Self-review checklist (ran at plan-write time)

- Spec coverage: rejected-proxy (constraint §Global), decisions 1–5 (Tasks 4–15), probe consequences (Tasks 1, 3, 10), classification table (Task 7), Go enumeration (9, 10), fail-soft ladder (4, 11–13, e2e 13–14), item mapping (8–15), tests incl. UTF-16 (8, 16), benchmark (16), docs (17), ComponentDecls fix (1). Import-path and js/css-body contexts: deliberately ctxNone (spec v1 exclusions).
- Known verify-points delegated to implementers are marked STOP-and-surface: provenance-gate dependencies (Task 1), extra cache writes in analyze (Task 3), parser recovery reality for the patch list (Task 6), StaticAttr value-span arithmetic (Task 7), ComponentKey format (Task 12), preset-name retrievability (Task 15). These are observation points, not design holes: the design does not change, only local mechanics.
- Type consistency: `FilterCandidate{Name,Pkg,Func,WantsCtx}` (Tasks 2→11), `repairResult{src,patch,parsed,fset}` (6→7), `completionContext` kinds (7→9–15), tier constants (8→9–15), `AnalyzeEphemeral(dir, path, content)` (3→5→9).
