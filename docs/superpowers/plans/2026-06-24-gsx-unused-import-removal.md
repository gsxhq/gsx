# Unused-Import Removal on Format — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Formatting a `.gsx` file (via `gsx fmt` and the LSP `textDocument/formatting`) removes imports the file declares but does not use, using the Go type-checker as the source of truth.

**Architecture:** codegen already type-checks an in-memory skeleton per `.gsx` file and maps "imported and not used" errors back to the `.gsx` import line. This plan (1) exposes the unused set from codegen, (2) adds an `astutil`-based removal transform to `internal/gsxfmt`, and (3) wires both the LSP handler and `gsx fmt` to it, with graceful fallback to syntactic formatting.

**Tech Stack:** Go, `go/types`/`go/packages` (already used), `golang.org/x/tools/go/ast/astutil` (already a dep, `v0.46.0`), `internal/gsxfmt` (parse → wsnorm → print).

## Global Constraints

- Detection is by **exact position correlation** (a type error resolves to a hoisted import's `.gsx` line) — never by matching the error message text to decide *whether* an import is unused. (The error message's quoted path may be used only to disambiguate *which* import when several share one source line.)
- **Never remove under uncertainty:** if the package has any type error that is NOT an unused-import error (or fails to load), remove nothing — format only.
- `internal/codegen` must NOT import `internal/gsxfmt`. codegen exposes its own `UnusedImport` type; `gen` converts to `gsxfmt.ImportRef`.
- Blank (`_`) and dot (`.`) imports are never removed (go/types never reports them unused; they never enter the set).
- `.go` files are untouched.
- Commit messages end with: `Claude-Session: https://claude.ai/code/session_01BmdcjM8wGhjPdBqKimNuMU`
- Run tests with the module's normal `go test`; module-loading tests are skipped under `-short`.

---

## File Structure

- `internal/codegen/batch.go` — add `UnusedImport` type + `PackageResult.UnusedImports` field + `detectUnusedImports` helper + call it in the analysis loop. Update the `buildSkeleton` call site.
- `internal/codegen/analyze.go` — change `buildSkeleton` to also return the file's `[]importSpec`. Update its one internal call site.
- `internal/codegen/analyze_test.go` — update the `buildSkeleton` call (signature change).
- `internal/codegen/unused_imports_test.go` — **new**: detection unit tests.
- `internal/gsxfmt/imports.go` — **new**: `ImportRef` type + `removeImports`/`deleteChunkImports`.
- `internal/gsxfmt/gsxfmt.go` — add `FormatRemovingImports`.
- `internal/gsxfmt/imports_test.go` — **new**: removal-transform unit tests.
- `internal/lsp/analysis.go` — add `Package.UnusedImports` field.
- `internal/lsp/format.go` — handler looks up the package's unused set and calls `FormatRemovingImports`.
- `gen/lsp.go` — convert `codegen.UnusedImport` → `gsxfmt.ImportRef` onto `lsp.Package`.
- `gen/fmt.go` — `-no-imports` flag, `analyzeUnusedImports`, and `FormatRemovingImports` in the loop.
- `gen/formatting_e2e_test.go` — add LSP removal e2e.
- `gen/fmt_test.go` — add CLI removal/fallback e2e.

---

## Task 1: codegen — detect and expose unused imports

**Files:**
- Modify: `internal/codegen/analyze.go` (buildSkeleton signature + return + internal call site)
- Modify: `internal/codegen/analyze_test.go` (call-site signature)
- Modify: `internal/codegen/batch.go` (PackageResult field, UnusedImport type, detectUnusedImports, call site, wiring)
- Test: `internal/codegen/unused_imports_test.go` (new)

**Interfaces:**
- Produces:
  - `type UnusedImport struct { Name, Path string }` (in `internal/codegen`, `batch.go`). `Name` is `""` for a default import.
  - `PackageResult.UnusedImports map[string][]UnusedImport` — keyed by `.gsx` file path; nil/empty when nothing is safely removable.
  - `buildSkeleton(...) (string, []*gsxast.Component, []importSpec, error)` — now also returns the file's hoisted import specs (with resolved `.pos`).
- Consumes: existing `importSpec{name, path, srcOff, pos}`, `packages.Package.TypeErrors`, the gsx parse `fset`.

- [ ] **Step 1: Write the failing detection test**

Create `internal/codegen/unused_imports_test.go`:

```go
package codegen

import (
	"path/filepath"
	"testing"
)

// writeFile is defined in the codegen test package (see navindex_test.go).

// TestUnusedImportsDetected: a .gsx that imports "strings" and "os" but uses
// neither lists both in UnusedImports; a used import is absent.
func TestUnusedImportsDetected(t *testing.T) {
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("../..")
	writeFile(t, dir, "go.mod",
		"module example.com/u\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	// imports strings (unused), os (unused), fmt (USED in the interp).
	writeFile(t, dir, "card.gsx",
		"package u\n\nimport (\n\t\"strings\"\n\t\"os\"\n\t\"fmt\"\n)\n\ncomponent Card(name string) {\n\t<p>{ fmt.Sprintf(\"%s\", name) }</p>\n}\n")

	out, err := GeneratePackagesWithFilters(dir, []string{dir}, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	pr := out[dir]
	if pr == nil {
		t.Fatalf("no result for %s", dir)
	}
	gsxPath := filepath.Join(dir, "card.gsx")
	got := map[string]bool{}
	for _, u := range pr.UnusedImports[gsxPath] {
		got[u.Path] = true
	}
	if !got["strings"] || !got["os"] {
		t.Errorf("want strings+os unused, got %v (all: %+v)", got, pr.UnusedImports)
	}
	if got["fmt"] {
		t.Errorf("fmt is used but was reported unused")
	}
}

// TestUnusedImportsGateOnOtherError: when the package has a NON-import type
// error, UnusedImports stays empty even though an unused import is present —
// removing under uncertainty is unsafe.
func TestUnusedImportsGateOnOtherError(t *testing.T) {
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("../..")
	writeFile(t, dir, "go.mod",
		"module example.com/u\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	// "strings" is unused AND there is an undefined symbol (a non-import error).
	writeFile(t, dir, "card.gsx",
		"package u\n\nimport \"strings\"\n\ncomponent Card() {\n\t<p>{ Nope() }</p>\n}\n")

	out, err := GeneratePackagesWithFilters(dir, []string{dir}, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	pr := out[dir]
	if pr == nil {
		t.Fatalf("no result")
	}
	if n := len(pr.UnusedImports); n != 0 {
		t.Errorf("expected NO removals under an unrelated error, got %+v", pr.UnusedImports)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails to compile**

Run: `go test ./internal/codegen/ -run TestUnusedImports -count=1`
Expected: build failure — `pr.UnusedImports` undefined, `UnusedImport` undefined.

- [ ] **Step 3: Change `buildSkeleton` to return the file's import specs**

In `internal/codegen/analyze.go`, change the signature (around line 232):

```go
func buildSkeleton(file *gsxast.File, table filterTable, propFields, nodeProps map[string]map[string]bool, fset *token.FileSet) (string, []*gsxast.Component, []importSpec, error) {
```

Update its three `return`s:
- The two error returns `return "", nil, err` (around lines 251 and 296) become `return "", nil, nil, err`.
- The final `return sb.String(), comps, nil` (around line 350) becomes `return sb.String(), comps, imports, nil`.

(`imports` is the local `[]importSpec` already built by the splitChunk loop near the top of the function.)

- [ ] **Step 4: Update `buildSkeleton`'s internal call sites**

`internal/codegen/analyze.go` line ~59 (in the type-resolution path) — it does not need the imports:

```go
		skel, comps, _, err := buildSkeleton(file, table, propFields, nodeProps, fset)
```

`internal/codegen/analyze_test.go` line ~242:

```go
	skel, _, _, err := buildSkeleton(file, table, propFields, nodeProps, fset)
```

- [ ] **Step 5: Capture the import specs in the batch skeleton-build loop**

In `internal/codegen/batch.go`, declare an accumulator next to `skelCompsByPath` (line ~182):

```go
	skelCompsByPath := map[string][]*gsxast.Component{}
	importsByDir := map[string][]importSpec{} // dir → hoisted import specs across its files
```

Update the call site (line ~201) to capture imports and accumulate them per dir:

```go
		for path, file := range files {
			skel, comps, imps, err := buildSkeleton(file, table, pf, np, fset)
			if err != nil {
```

After `skelCompsByPath[absXpath] = comps` (line ~219), add:

```go
			importsByDir[dir] = append(importsByDir[dir], imps...)
```

- [ ] **Step 6: Add the `UnusedImport` type, the field, and `detectUnusedImports`**

In `internal/codegen/batch.go`, add the type near `PackageResult` (after the struct, around line 60):

```go
// UnusedImport is one import a .gsx file declares but never references, as
// determined by the type-checker. Name is "" for a default import.
type UnusedImport struct {
	Name string
	Path string
}
```

Add the field to `PackageResult` (after `NavIndex`):

```go
	NavIndex   []NavRef            // navigable Go references → .gsx targets (func, props-struct, field)

	// UnusedImports lists, per .gsx file path, the imports the file declares but
	// does not use — safe to drop on format. Empty unless the package's ONLY type
	// errors are unused-import errors (else removal is unsafe).
	UnusedImports map[string][]UnusedImport
```

Add the helper (anywhere in `batch.go`, e.g. just above `GeneratePackages`):

```go
// detectUnusedImports correlates the package's type errors with the .gsx
// positions of its hoisted imports. An error landing on a hoisted import's .gsx
// line IS that import's "imported and not used" error (the skeleton emits one
// //line per import). If ANY type error does not land on a hoisted import, the
// analysis is unreliable and nothing is returned — never remove under
// uncertainty. Returns nil when there are no type errors (nothing unused).
func detectUnusedImports(pkg *packages.Package, imports []importSpec, gsxFset *token.FileSet) map[string][]UnusedImport {
	if len(pkg.TypeErrors) == 0 || len(imports) == 0 {
		return nil
	}
	type posKey struct {
		file string
		line int
	}
	byPos := map[posKey][]importSpec{}
	for _, imp := range imports {
		p := gsxFset.Position(imp.pos)
		k := posKey{p.Filename, p.Line}
		byPos[k] = append(byPos[k], imp)
	}
	out := map[string][]UnusedImport{}
	for _, e := range pkg.TypeErrors {
		ep := e.Fset.Position(e.Pos)
		specs, ok := byPos[posKey{ep.Filename, ep.Line}]
		if !ok {
			return nil // a non-import type error → remove nothing
		}
		spec := specs[0]
		if len(specs) > 1 {
			spec = pickImportByPath(specs, e.Msg)
		}
		out[ep.Filename] = append(out[ep.Filename], UnusedImport{Name: spec.name, Path: spec.path})
	}
	return out
}

// pickImportByPath disambiguates several imports sharing one .gsx line using the
// path go/types names in the error (`"<path>" imported ...`). Falls back to the
// first spec if the path is not found.
func pickImportByPath(specs []importSpec, msg string) importSpec {
	if i := strings.IndexByte(msg, '"'); i >= 0 {
		if j := strings.IndexByte(msg[i+1:], '"'); j >= 0 {
			path := msg[i+1 : i+1+j]
			for _, s := range specs {
				if s.path == path {
					return s
				}
			}
		}
	}
	return specs[0]
}
```

(`strings` and `packages` are already imported in `batch.go`.)

- [ ] **Step 7: Populate `res.UnusedImports` in the analysis loop**

In `internal/codegen/batch.go`, immediately after `res.NavIndex = navIndex` (line ~428):

```go
		res.NavIndex = navIndex
		res.UnusedImports = detectUnusedImports(pkg, importsByDir[pkgDir], fset)
```

(`fset` here is the gsx parse FileSet in scope in this function; `pkgDir` is the package's dir.)

- [ ] **Step 8: Run the detection tests**

Run: `go test ./internal/codegen/ -run TestUnusedImports -count=1`
Expected: PASS (both tests).

- [ ] **Step 9: Run the full codegen + gen package tests (no regression)**

Run: `go test ./internal/codegen/ ./gen/ -count=1`
Expected: PASS (the `buildSkeleton` signature change and existing import-diag tests still pass).

- [ ] **Step 10: Commit**

```bash
git add internal/codegen/analyze.go internal/codegen/analyze_test.go internal/codegen/batch.go internal/codegen/unused_imports_test.go
git commit -m "feat(codegen): detect unused .gsx imports via type-checker, expose UnusedImports

Correlate go/types 'imported and not used' errors to each hoisted import's
.gsx line; gate on the package having no other type errors. buildSkeleton now
returns the file's import specs.

Claude-Session: https://claude.ai/code/session_01BmdcjM8wGhjPdBqKimNuMU"
```

---

## Task 2: gsxfmt — the import-removal transform

**Files:**
- Create: `internal/gsxfmt/imports.go`
- Modify: `internal/gsxfmt/gsxfmt.go` (add `FormatRemovingImports`)
- Test: `internal/gsxfmt/imports_test.go` (new)

**Interfaces:**
- Produces:
  - `type ImportRef struct { Name, Path string }` (in `internal/gsxfmt`).
  - `func FormatRemovingImports(name string, src []byte, unused []ImportRef) ([]byte, error)` — like `Format`, but first drops every import in `unused` from the file's pass-through Go chunks. Empty/nil `unused` ⇒ identical to `Format`.
- Consumes: existing `gsxfmt.Format` pipeline (`parser.ParseFile`, `wsnorm.Normalize`, `printer.Fprint`); `golang.org/x/tools/go/ast/astutil`.

- [ ] **Step 1: Write the failing transform tests**

Create `internal/gsxfmt/imports_test.go`:

```go
package gsxfmt

import (
	"strings"
	"testing"
)

func mustFormat(t *testing.T, src string, unused []ImportRef) string {
	t.Helper()
	out, err := FormatRemovingImports("x.gsx", []byte(src), unused)
	if err != nil {
		t.Fatalf("FormatRemovingImports: %v", err)
	}
	return string(out)
}

// TestRemoveSingleImport: a lone `import "strings"` is removed entirely.
func TestRemoveSingleImport(t *testing.T) {
	src := "package x\n\nimport \"strings\"\n\ncomponent C() {\n\t<p>hi</p>\n}\n"
	got := mustFormat(t, src, []ImportRef{{Path: "strings"}})
	if strings.Contains(got, "strings") {
		t.Fatalf("strings import not removed:\n%s", got)
	}
}

// TestRemoveOneOfBlock: one unused spec drops from an import block; the used one
// and the block survive.
func TestRemoveOneOfBlock(t *testing.T) {
	src := "package x\n\nimport (\n\t\"strings\"\n\t\"fmt\"\n)\n\ncomponent C() {\n\t<p>{ fmt.Sprint(1) }</p>\n}\n"
	got := mustFormat(t, src, []ImportRef{{Path: "strings"}})
	if strings.Contains(got, "\"strings\"") {
		t.Fatalf("strings not removed:\n%s", got)
	}
	if !strings.Contains(got, "\"fmt\"") {
		t.Fatalf("fmt wrongly removed:\n%s", got)
	}
}

// TestRemoveAllImports: removing the only spec leaves no empty import block.
func TestRemoveAllImports(t *testing.T) {
	src := "package x\n\nimport (\n\t\"strings\"\n)\n\ncomponent C() {\n\t<p>hi</p>\n}\n"
	got := mustFormat(t, src, []ImportRef{{Path: "strings"}})
	if strings.Contains(got, "import") {
		t.Fatalf("empty import block left behind:\n%s", got)
	}
}

// TestRemoveAliasedImport: an aliased import is removed by (name, path).
func TestRemoveAliasedImport(t *testing.T) {
	src := "package x\n\nimport sx \"strings\"\n\ncomponent C() {\n\t<p>hi</p>\n}\n"
	got := mustFormat(t, src, []ImportRef{{Name: "sx", Path: "strings"}})
	if strings.Contains(got, "strings") {
		t.Fatalf("aliased import not removed:\n%s", got)
	}
}

// TestBlankImportPreserved: a blank import is never in the unused set, so it
// survives even when another import is removed.
func TestBlankImportPreserved(t *testing.T) {
	src := "package x\n\nimport (\n\t_ \"embed\"\n\t\"strings\"\n)\n\ncomponent C() {\n\t<p>hi</p>\n}\n"
	got := mustFormat(t, src, []ImportRef{{Path: "strings"}})
	if !strings.Contains(got, "_ \"embed\"") {
		t.Fatalf("blank import wrongly removed:\n%s", got)
	}
}

// TestNoUnusedIsPlainFormat: empty unused ⇒ identical to Format.
func TestNoUnusedIsPlainFormat(t *testing.T) {
	src := "package x\n\nimport \"strings\"\n\ncomponent C() {\n\t<p>{ strings.ToUpper(\"x\") }</p>\n}\n"
	plain, err := Format("x.gsx", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	got := mustFormat(t, src, nil)
	if got != string(plain) {
		t.Fatalf("empty unused diverged from Format:\nremoving:\n%s\nformat:\n%s", got, plain)
	}
}

// TestRemoveIdempotent: removing then re-removing is stable.
func TestRemoveIdempotent(t *testing.T) {
	src := "package x\n\nimport \"strings\"\n\ncomponent C() {\n\t<p>hi</p>\n}\n"
	once := mustFormat(t, src, []ImportRef{{Path: "strings"}})
	twice := mustFormat(t, once, []ImportRef{{Path: "strings"}})
	if once != twice {
		t.Fatalf("not idempotent:\nonce:\n%s\ntwice:\n%s", once, twice)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/gsxfmt/ -run 'TestRemove|TestBlank|TestNoUnused' -count=1`
Expected: build failure — `ImportRef` and `FormatRemovingImports` undefined.

- [ ] **Step 3: Create the removal mechanics**

Create `internal/gsxfmt/imports.go`:

```go
package gsxfmt

import (
	goformat "go/format"
	goparser "go/parser"
	"go/token"
	"strings"

	"golang.org/x/tools/go/ast/astutil"

	gsxast "github.com/gsxhq/gsx/ast"
)

// ImportRef identifies an import to remove from a .gsx file's pass-through Go
// chunk. Name is "" for a default import (e.g. `import "strings"`), or the
// explicit alias for an aliased import (e.g. `import sx "strings"`).
type ImportRef struct {
	Name string
	Path string
}

// removeImports drops every import named in `unused` from the file's GoChunks,
// in place. A chunk that does not parse on its own, or holds none of the unused
// imports, is left untouched.
func removeImports(f *gsxast.File, unused []ImportRef) {
	if len(unused) == 0 {
		return
	}
	for _, d := range f.Decls {
		gc, ok := d.(*gsxast.GoChunk)
		if !ok {
			continue
		}
		if rewritten, changed := deleteChunkImports(gc.Src, unused); changed {
			gc.Src = rewritten
		}
	}
}

// deleteChunkImports parses one Go chunk (wrapped with a synthetic package
// clause), deletes the named imports via astutil, and reprints. Returns the
// rewritten chunk and whether anything changed. The chunk is gofmt'd here; the
// gsx printer also gofmt's chunks on output, so the result is stable.
func deleteChunkImports(src string, unused []ImportRef) (string, bool) {
	const pkg = "package _gsxp\n"
	fset := token.NewFileSet()
	file, err := goparser.ParseFile(fset, "", pkg+src, goparser.ParseComments)
	if err != nil {
		return src, false // not standalone-valid Go; leave it
	}
	changed := false
	for _, u := range unused {
		if astutil.DeleteNamedImport(fset, file, u.Name, u.Path) {
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
	out := b.String()
	// Drop the synthetic "package _gsxp" line we prepended.
	if nl := strings.IndexByte(out, '\n'); nl >= 0 {
		out = out[nl+1:]
	}
	return strings.TrimSpace(out), true
}
```

(astutil operates on the `*ast.File` returned by `goparser.ParseFile`; no direct `go/ast` reference is needed in this file.)

- [ ] **Step 4: Add `FormatRemovingImports`**

In `internal/gsxfmt/gsxfmt.go`, add after `Format`:

```go
// FormatRemovingImports formats src exactly like Format, but first removes every
// import listed in `unused` from the file's pass-through Go chunks. With an empty
// or nil `unused` it is identical to Format. A parse error is returned unchanged
// (the caller decides whether to surface or ignore it).
func FormatRemovingImports(name string, src []byte, unused []ImportRef) ([]byte, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		return nil, err
	}
	removeImports(f, unused)
	wsnorm.Normalize(f)
	var b bytes.Buffer
	if err := printer.Fprint(&b, f); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
```

- [ ] **Step 5: Run the transform tests**

Run: `go test ./internal/gsxfmt/ -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/gsxfmt/imports.go internal/gsxfmt/gsxfmt.go internal/gsxfmt/imports_test.go
git commit -m "feat(gsxfmt): FormatRemovingImports — drop named imports via astutil

New ImportRef + removal transform: parse → astutil.DeleteNamedImport on each
GoChunk → wsnorm → print. Empty unused set is identical to Format.

Claude-Session: https://claude.ai/code/session_01BmdcjM8wGhjPdBqKimNuMU"
```

---

## Task 3: LSP — transport the unused set and remove on format

**Files:**
- Modify: `internal/lsp/analysis.go` (add `Package.UnusedImports`)
- Modify: `gen/lsp.go` (convert + populate)
- Modify: `internal/lsp/format.go` (use it in `handleFormatting`)
- Test: `gen/formatting_e2e_test.go` (add e2e)

**Interfaces:**
- Consumes: `codegen.PackageResult.UnusedImports` (Task 1), `gsxfmt.ImportRef` + `gsxfmt.FormatRemovingImports` (Task 2).
- Produces: `lsp.Package.UnusedImports map[string][]gsxfmt.ImportRef`.

- [ ] **Step 1: Write the failing LSP e2e test**

Append to `gen/formatting_e2e_test.go`:

```go
// TestFormattingRemovesUnusedImport: textDocument/formatting on a .gsx with an
// unused import returns an edit whose text drops that import.
func TestFormattingRemovesUnusedImport(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	must := func(p, c string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod", "module example.com/u\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	src := "package u\n\nimport \"strings\"\n\ncomponent C() {\n\t<p>hi</p>\n}\n"
	must("c.gsx", src)
	uri := "file://" + filepath.Join(dir, "c.gsx")

	edits := formattingEdits(t, uri, src)
	if len(edits) != 1 {
		t.Fatalf("want 1 edit, got %d: %+v", len(edits), edits)
	}
	if strings.Contains(edits[0].NewText, "strings") {
		t.Fatalf("formatting did not drop the unused import:\n%s", edits[0].NewText)
	}
}
```

- [ ] **Step 2: Run it to verify failure**

Run: `go test ./gen/ -run TestFormattingRemovesUnusedImport -count=1`
Expected: FAIL — the edit still contains `strings` (handler does not yet remove imports).

- [ ] **Step 3: Add the `Package.UnusedImports` field**

In `internal/lsp/analysis.go`, add to the `Package` struct (after `NavIndex`) and ensure `gsxfmt` is imported:

```go
	NavIndex   []NavRef // navigable Go references → .gsx targets (func, props-struct, field)

	// UnusedImports lists, per .gsx file path, imports that file declares but does
	// not use — what formatting may safely drop. Empty when analysis is unreliable.
	UnusedImports map[string][]gsxfmt.ImportRef
```

Add the import to `internal/lsp/analysis.go`'s import block:

```go
	"github.com/gsxhq/gsx/internal/gsxfmt"
```

- [ ] **Step 4: Populate it in `gen/lsp.go`**

In `gen/lsp.go`, build the converted map and set it on the returned `lsp.Package` (alongside `cross`/`nav`):

```go
	unused := make(map[string][]gsxfmt.ImportRef, len(pr.UnusedImports))
	for path, imps := range pr.UnusedImports {
		refs := make([]gsxfmt.ImportRef, len(imps))
		for i, u := range imps {
			refs[i] = gsxfmt.ImportRef{Name: u.Name, Path: u.Path}
		}
		unused[path] = refs
	}
	return &lsp.Package{
		Diags:         pr.Diags,
		GSXFset:       pr.GSXFset,
		Fset:          pr.Fset,
		Info:          pr.Info,
		ExprMap:       pr.ExprMap,
		Files:         pr.GSXFiles,
		CrossIndex:    cross,
		NavIndex:      nav,
		UnusedImports: unused,
	}, nil
```

Add `"github.com/gsxhq/gsx/internal/gsxfmt"` to `gen/lsp.go`'s imports.

- [ ] **Step 5: Use the unused set in `handleFormatting`**

In `internal/lsp/format.go`, replace the `gsxfmt.Format(...)` call with a lookup + `FormatRemovingImports`. The current handler (after fetching `text`) reads:

```go
	formatted, err := gsxfmt.Format(uriToPath(uri), []byte(text))
	if err != nil || string(formatted) == text {
		return s.reply(f.ID, []TextEdit{}) // invalid mid-edit, or already canonical
	}
```

Replace with:

```go
	path := uriToPath(uri)
	var unused []gsxfmt.ImportRef
	if pkg := s.pkgs[filepath.Dir(path)]; pkg != nil {
		unused = pkg.UnusedImports[path] // nil when analysis is unavailable/unreliable
	}
	formatted, err := gsxfmt.FormatRemovingImports(path, []byte(text), unused)
	if err != nil || string(formatted) == text {
		return s.reply(f.ID, []TextEdit{}) // invalid mid-edit, or already canonical
	}
```

Ensure `path/filepath` is imported in `format.go` (add `"path/filepath"` if absent). The later `endPosition(text, s.enc)` call and the rest of the handler are unchanged.

- [ ] **Step 6: Run the LSP e2e + the existing formatting tests**

Run: `go test ./gen/ -run TestFormatting -count=1`
Expected: PASS — `TestFormattingRemovesUnusedImport` plus the existing `TestFormattingReformats`/`AlreadyCanonical`/`Advertised`.

- [ ] **Step 7: Run the lsp package tests (field addition compiles)**

Run: `go test ./internal/lsp/ -count=1`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/lsp/analysis.go internal/lsp/format.go gen/lsp.go gen/formatting_e2e_test.go
git commit -m "feat(lsp): textDocument/formatting drops unused imports

Carry codegen's per-file unused-import set onto lsp.Package and feed it to
gsxfmt.FormatRemovingImports. Degrades to syntactic format when no analyzed
package is available.

Claude-Session: https://claude.ai/code/session_01BmdcjM8wGhjPdBqKimNuMU"
```

---

## Task 4: CLI — `gsx fmt` removes by default, with `-no-imports` and fallback

**Files:**
- Modify: `gen/fmt.go` (`-no-imports` flag, `analyzeUnusedImports`, `FormatRemovingImports` in the loop)
- Test: `gen/fmt_test.go` (add removal/fallback e2e)

**Interfaces:**
- Consumes: `codegen.GeneratePackagesWithFilters` + `codegen.PackageResult.UnusedImports` (Task 1), `gsxfmt.FormatRemovingImports`/`ImportRef` (Task 2), `moduleRoot` (`gen/modroot.go`), `attrclass.Builtin()`.

- [ ] **Step 1: Write the failing CLI e2e tests**

Append to `gen/fmt_test.go` (it already has `fmtCapture`):

```go
// TestFmtRemovesUnusedImport: `gsx fmt -w` drops an unused import by default.
func TestFmtRemovesUnusedImport(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	w := func(p, c string) {
		if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w("go.mod", "module example.com/u\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	p := filepath.Join(dir, "c.gsx")
	w("c.gsx", "package u\n\nimport \"strings\"\n\ncomponent C() {\n\t<p>hi</p>\n}\n")

	if code, _, errb := fmtCapture(t, []string{"-w", p}); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errb)
	}
	after, _ := os.ReadFile(p)
	if strings.Contains(string(after), "strings") {
		t.Fatalf("unused import not removed by default:\n%s", after)
	}
}

// TestFmtNoImportsKeepsUnused: `-no-imports` skips removal (syntactic only).
func TestFmtNoImportsKeepsUnused(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	w := func(p, c string) {
		if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w("go.mod", "module example.com/u\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	p := filepath.Join(dir, "c.gsx")
	w("c.gsx", "package u\n\nimport \"strings\"\n\ncomponent C() {\n\t<p>hi</p>\n}\n")

	if code, _, errb := fmtCapture(t, []string{"-no-imports", "-w", p}); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errb)
	}
	after, _ := os.ReadFile(p)
	if !strings.Contains(string(after), "strings") {
		t.Fatalf("-no-imports should keep the import:\n%s", after)
	}
}

// TestFmtOutsideModuleFallsBack: a .gsx not in any module is still formatted
// (syntactically); the unused import is kept and the exit code is success.
func TestFmtOutsideModuleFallsBack(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := t.TempDir() // no go.mod
	p := filepath.Join(dir, "c.gsx")
	if err := os.WriteFile(p, []byte("package u\n\nimport   \"strings\"\n\ncomponent C() {\n\t<p>hi</p>\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errb := fmtCapture(t, []string{"-w", p})
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s (formatting must not fail outside a module)", code, errb)
	}
	after, _ := os.ReadFile(p)
	if !strings.Contains(string(after), "strings") {
		t.Fatalf("outside a module the import must be kept (no analysis):\n%s", after)
	}
	if strings.Contains(string(after), "import   \"strings\"") {
		t.Fatalf("file was not syntactically formatted:\n%s", after)
	}
}
```

- [ ] **Step 2: Run them to verify failure**

Run: `go test ./gen/ -run 'TestFmtRemovesUnusedImport|TestFmtNoImports|TestFmtOutsideModule' -count=1`
Expected: FAIL — `-no-imports` is an unknown flag (exit 2) and removal does not happen.

- [ ] **Step 3: Add the `-no-imports` flag**

In `gen/fmt.go` `runFmt`, add to the flag block (after `diff`):

```go
		write     bool
		list      bool
		diff      bool
		noImports bool
	)
	fs.BoolVar(&write, "w", false, "write result to (source) file instead of stdout")
	fs.BoolVar(&list, "l", false, "list files whose formatting differs")
	fs.BoolVar(&diff, "d", false, "display diffs instead of rewriting files")
	fs.BoolVar(&noImports, "no-imports", false, "do not remove unused imports (skip module analysis)")
```

- [ ] **Step 4: Compute the unused set before the loop**

In `gen/fmt.go` `runFmt`, after `files, err := gsxFiles(paths)` returns successfully and before `exit := 0`:

```go
	var unusedByPath map[string][]gsxfmt.ImportRef
	if !noImports {
		unusedByPath = analyzeUnusedImports(files)
	}
```

- [ ] **Step 5: Use `FormatRemovingImports` in the loop**

In `gen/fmt.go` `runFmt`, replace `formatted, err := formatGsx(path, orig)` with a lookup keyed by absolute path (codegen reports absolute `.gsx` paths):

```go
		abs, _ := filepath.Abs(path)
		formatted, err := gsxfmt.FormatRemovingImports(path, orig, unusedByPath[abs])
```

(`FormatRemovingImports` with a nil slice is exactly `Format`, so `-no-imports`, analysis failure, and "no unused imports" all degrade correctly.)

- [ ] **Step 6: Add `analyzeUnusedImports`**

In `gen/fmt.go`, add the helper (and the imports it needs):

```go
// analyzeUnusedImports best-effort computes, per absolute .gsx path, the imports
// the file declares but does not use, by analyzing each containing directory's
// package. Directories not in a module, or that fail to load, are skipped — the
// caller then formats those files syntactically (no removal). Keys are absolute.
func analyzeUnusedImports(files []string) map[string][]gsxfmt.ImportRef {
	out := map[string][]gsxfmt.ImportRef{}
	dirs := map[string]bool{}
	for _, f := range files {
		dirs[filepath.Dir(f)] = true
	}
	for dir := range dirs {
		root, _, err := moduleRoot(dir)
		if err != nil {
			continue // not in a module → syntactic fallback
		}
		absDir, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		res, err := codegen.GeneratePackagesWithFilters(root, []string{absDir}, nil, attrclass.Builtin(), nil, nil, nil)
		if err != nil {
			continue
		}
		pr := res[absDir]
		if pr == nil {
			continue
		}
		for gsxPath, imps := range pr.UnusedImports {
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
	return out
}
```

Update `gen/fmt.go`'s imports: add `"github.com/gsxhq/gsx/internal/codegen"` and `"github.com/gsxhq/gsx/internal/attrclass"` (keep the existing `"github.com/gsxhq/gsx/internal/gsxfmt"`).

- [ ] **Step 7: Run the CLI tests**

Run: `go test ./gen/ -run 'TestFmt' -count=1`
Expected: PASS — the three new tests plus the existing `gsx fmt` suite (`TestFmtDefaultStdout`, `TestFmtListUnformatted`, `TestFmtWriteIdempotent`, etc.).

- [ ] **Step 8: Run the full suite**

Run: `go test ./... -count=1`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add gen/fmt.go gen/fmt_test.go
git commit -m "feat(cli): gsx fmt removes unused imports by default (-no-imports opts out)

Analyze each file's package for unused imports and feed them to
gsxfmt.FormatRemovingImports. Files outside a module, or whose package fails to
load, fall back to syntactic formatting — gsx fmt never regresses.

Claude-Session: https://claude.ai/code/session_01BmdcjM8wGhjPdBqKimNuMU"
```

---

## Self-Review notes (addressed)

- **Spec coverage:** detection + gate (Task 1) ↔ spec §3.1, §5; astutil edit (Task 2) ↔ §4; LSP transport+handler (Task 3) ↔ §3, §5; CLI default-on + `-no-imports` + fallback (Task 4) ↔ §5, §6. Testing (§7) is distributed across each task's tests.
- **Type consistency:** `codegen.UnusedImport{Name,Path}` (Task 1) → converted to `gsxfmt.ImportRef{Name,Path}` (Task 2) in `gen/lsp.go` (Task 3) and `gen/fmt.go` (Task 4). `FormatRemovingImports(name string, src []byte, unused []ImportRef)` is referenced identically in Tasks 3 and 4. `Package.UnusedImports map[string][]gsxfmt.ImportRef` (Task 3) is keyed by `.gsx` path, matching `PackageResult.UnusedImports` (Task 1).
- **Path keys:** codegen reports absolute `.gsx` paths; Task 4 looks up by `filepath.Abs(path)` and Task 3 by `uriToPath(uri)` (absolute). Both normalize to the codegen key form.
- **Reliability gate** lives entirely in `detectUnusedImports` (Task 1): any non-import type error ⇒ nil ⇒ no removal anywhere; consumers need no extra guard.
