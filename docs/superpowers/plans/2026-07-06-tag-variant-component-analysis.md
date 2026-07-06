# Tag-variant Component Analysis Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let two `.gsx` files under disjoint `//go:build` tags declare the same component without breaking analysis: tolerate same-name + same-signature variants, raise a clean error on same-name + different-signature, and make the LSP show all variants.

**Architecture:** gsx never parses build constraints — generation stays build-context-independent and `go build` filters by tag. The fix is (1) suppress the go/types `redeclared` errors that arise from *cross-file* duplicate top-level decls so `Generate` stops skipping the whole package, (2) detect same-name-different-signature *component* collisions ourselves and report a friendly `duplicate-component` error that blocks emission, and (3) make the component cross-navigation index multi-valued so LSP go-to-definition / find-references return every variant. Emit is unchanged and relies on go/types resolving both duplicate bodies best-effort (probe-confirmed).

**Tech Stack:** Go, `go/types`, `go/ast`; gsx internal packages `internal/codegen`, `internal/lsp`, `internal/corpus` (txtar golden harness).

**Design doc:** `docs/superpowers/specs/2026-07-06-tag-variant-component-analysis-design.md`

## Global Constraints

- Runtime (module root `gsx`) is **standard-library only**. This work is entirely in `internal/codegen` / `internal/lsp` / tests (tooling), which may use `golang.org/x/tools` — but none is needed here; std `go/types`/`go/ast` suffice.
- gsx does **NOT** import `go/build/constraint` or `go/build.Context`, and does **NOT** evaluate GOOS/GOARCH/tags anywhere in this work. `//go:build` stays opaque pass-through text.
- **Don't hand-edit `.x.go` or `*.golden` files** — regenerate with `go test ./internal/corpus -run TestCorpus -update`, then verify without `-update`.
- Every codegen behavior change ships a **corpus case** (`internal/corpus/testdata/cases/**/*.txtar`).
- Pin Go to `GO_VERSION` in `.github/workflows/ci.yml` (currently 1.26.1) — a different minor reintroduces gofmt drift.
- Prefer **unexported** identifiers unless serialization requires export.
- Diagnostic code string for the different-signature error: **`duplicate-component`** (string codes, not numeric — matches existing `invalid-syntax` style).
- Run `make check` for the inner loop; `make ci` before merge.

---

### Task 1: Component signature canonicalization

Produce a canonical string for a component's *caller* signature so two variants can be compared for drop-in equivalence: props field set (name + normalized type), synthesized `Children`/`Attrs` presence, generic type params, and receiver type.

**Files:**
- Create: `internal/codegen/variantcollide.go`
- Test: `internal/codegen/variantcollide_test.go`

**Interfaces:**
- Produces: `func componentSignature(c *gsxast.Component) string` — a canonical, comparison-ready signature string. Deterministic; ignores param *order* (props map by name) and receiver *variable* name; includes receiver type + pointer-ness, normalized type-param source, each `fieldName(param)` + whitespace-normalized type, and the synthesized `Children`/`Attrs` pseudo-fields when `usesChildren`/`usesAttrs` report them.
- Consumes (existing helpers in `internal/codegen`): `parseParams(string) ([]param, error)` (`analyze.go`), `parseRecv(string) (var, ptr, typeName string, err error)` (returns 4 values; 2nd is the pointer marker), `fieldName(string) string`, `usesChildren([]gsxast.Markup) bool`, `usesAttrs([]gsxast.Markup) bool`.

- [ ] **Step 1: Write the failing test**

```go
package codegen

import (
	"testing"

	"github.com/gsxhq/gsx/parser"
	"go/token"
)

// parseComponent parses a single-component .gsx source and returns the Component.
func parseComponent(t *testing.T, src string) *gsxComponentForTest {
	t.Helper()
	return parseComponentsForTest(t, src)[0]
}

func TestComponentSignature(t *testing.T) {
	// Same props, different body → SAME signature (drop-in variant).
	a := mustParseComponent(t, "package v\ncomponent Icon(name string) {\n\t<span>{ name }</span>\n}\n")
	b := mustParseComponent(t, "package v\ncomponent Icon(name string) {\n\t<b>{ name }</b>\n}\n")
	if componentSignature(a) != componentSignature(b) {
		t.Fatalf("same-props variants must share a signature:\n a=%q\n b=%q", componentSignature(a), componentSignature(b))
	}

	// Different prop type → DIFFERENT signature.
	c := mustParseComponent(t, "package v\ncomponent Icon(name int) {\n\t<span>{ name }</span>\n}\n")
	if componentSignature(a) == componentSignature(c) {
		t.Fatalf("different prop type must differ: %q", componentSignature(a))
	}

	// Param order does not matter (props map by name).
	d := mustParseComponent(t, "package v\ncomponent Icon(x string, y string) { <i/> }\n")
	e := mustParseComponent(t, "package v\ncomponent Icon(y string, x string) { <i/> }\n")
	if componentSignature(d) != componentSignature(e) {
		t.Fatalf("param order must not affect signature")
	}

	// Children presence is part of the signature.
	f := mustParseComponent(t, "package v\ncomponent Box() { <div>{ children }</div> }\n")
	g := mustParseComponent(t, "package v\ncomponent Box() { <div/> }\n")
	if componentSignature(f) == componentSignature(g) {
		t.Fatalf("children presence must differ")
	}
}
```

Add this helper to the test file (parses via the real parser, returns the first `*gsxast.Component`):

```go
func mustParseComponent(t *testing.T, src string) *gsxast.Component {
	t.Helper()
	fset := token.NewFileSet()
	file, errs := parser.ParseFileWithClassifier(fset, "input.gsx", []byte(src), 0, nil)
	if len(errs) > 0 {
		t.Fatalf("parse: %v", errs)
	}
	for _, d := range file.Decls {
		if c, ok := d.(*gsxast.Component); ok {
			return c
		}
	}
	t.Fatal("no component")
	return nil
}
```

Add imports `gsxast "github.com/gsxhq/gsx/ast"` and remove the placeholder `parseComponent`/`parseComponentsForTest` references (they were illustrative — use `mustParseComponent`).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/codegen -run TestComponentSignature -v`
Expected: FAIL — `undefined: componentSignature`.

- [ ] **Step 3: Write minimal implementation**

```go
package codegen

import (
	"sort"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
)

// componentSignature returns a canonical string of a component's CALLER
// signature — what a `<Comp .../>` invocation type-checks against. Two
// components with the same componentKey that share this signature are drop-in
// build-tag variants (same name, different body); one with a different
// signature is a genuine conflict. The string is comparison-only (not parsed);
// it is order-independent over props (attrs map to fields by name) and ignores
// the receiver VARIABLE name.
func componentSignature(c *gsxast.Component) string {
	var b strings.Builder

	// Receiver: type + pointer-ness (a method vs func component, and the owning
	// type, are caller-visible; the receiver var name is not).
	if c.Recv != "" {
		if _, ptr, recvType, err := parseRecv(c.Recv); err == nil {
			b.WriteString("recv:")
			b.WriteString(ptr)
			b.WriteString(recvType)
		} else {
			b.WriteString("recv:<unparsable>")
		}
	}
	b.WriteByte('|')

	// Generic type params: normalized source.
	b.WriteString("tp:")
	b.WriteString(strings.Join(strings.Fields(c.TypeParams), " "))
	b.WriteByte('|')

	// Props: sorted "FieldName type" entries, plus synthesized Children/Attrs.
	var fields []string
	if params, err := parseParams(c.Params); err == nil {
		for _, p := range params {
			fields = append(fields, fieldName(p.name)+" "+strings.Join(strings.Fields(p.typ), " "))
		}
	}
	if usesChildren(c.Body) {
		fields = append(fields, "Children gsx.Node")
	}
	if usesAttrs(c.Body) {
		fields = append(fields, "Attrs gsx.Attrs")
	}
	sort.Strings(fields)
	b.WriteString("props:")
	b.WriteString(strings.Join(fields, ";"))
	return b.String()
}
```

Note: confirm `parseRecv`'s 2nd return is the pointer marker (`"*"` or `""`). If its shape differs, adapt the `ptr` usage — the canonical string only needs to be *stable and discriminating*, so include whatever pointer signal `parseRecv` exposes. If `param` uses field names other than `name`/`typ`, match them (`grep -n "type param struct" internal/codegen/*.go`).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/codegen -run TestComponentSignature -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/codegen/variantcollide.go internal/codegen/variantcollide_test.go
git commit -m "feat(codegen): componentSignature for tag-variant equivalence

Claude-Session: https://claude.ai/code/session_01GCvAa6qCFw2pZRdgwnAyxH"
```

---

### Task 2: Detect same-name / different-signature component collisions

Find components sharing a `componentKey` across **different files** whose signatures differ — these are the conflicts that get a friendly error.

**Files:**
- Modify: `internal/codegen/variantcollide.go`
- Test: `internal/codegen/variantcollide_test.go`

**Interfaces:**
- Produces:
  - `type conflictComp struct { path string; comp *gsxast.Component }`
  - `type signatureConflict struct { key string; comps []conflictComp }` — all cross-file decls of a key that has ≥2 distinct signatures.
  - `func detectSignatureConflicts(files map[string]*gsxast.File) []signatureConflict` — deterministic (sorted by key). Only considers `*gsxast.Component` decls; a key whose decls are all in one file, or all share one signature, produces no conflict.
- Consumes: `componentKey(c *gsxast.Component) string` (`analyze.go`), `componentSignature` (Task 1).

- [ ] **Step 1: Write the failing test**

```go
func TestDetectSignatureConflicts(t *testing.T) {
	filesOf := func(srcs map[string]string) map[string]*gsxast.File {
		out := map[string]*gsxast.File{}
		for name, src := range srcs {
			fset := token.NewFileSet()
			f, errs := parser.ParseFileWithClassifier(fset, name, []byte(src), 0, nil)
			if len(errs) > 0 {
				t.Fatalf("%s: %v", name, errs)
			}
			out[name] = f
		}
		return out
	}

	// Same name, same signature, different files → NO conflict (tolerated variant).
	same := filesOf(map[string]string{
		"a.gsx": "package v\ncomponent Icon(name string) { <a>{ name }</a> }\n",
		"b.gsx": "package v\ncomponent Icon(name string) { <b>{ name }</b> }\n",
	})
	if got := detectSignatureConflicts(same); len(got) != 0 {
		t.Fatalf("same-sig variants: want 0 conflicts, got %d", len(got))
	}

	// Same name, DIFFERENT signature, different files → conflict.
	diff := filesOf(map[string]string{
		"a.gsx": "package v\ncomponent Icon(name string) { <a>{ name }</a> }\n",
		"b.gsx": "package v\ncomponent Icon(name int) { <b>{ name }</b> }\n",
	})
	got := detectSignatureConflicts(diff)
	if len(got) != 1 || got[0].key != ".Icon" || len(got[0].comps) != 2 {
		t.Fatalf("diff-sig: want 1 conflict on .Icon with 2 comps, got %+v", got)
	}

	// Same name twice in ONE file → NOT our conflict (within-file; left to raw error).
	within := filesOf(map[string]string{
		"a.gsx": "package v\ncomponent Icon(name string) { <a/> }\ncomponent Icon(name int) { <b/> }\n",
	})
	if got := detectSignatureConflicts(within); len(got) != 0 {
		t.Fatalf("within-file dup: want 0 conflicts, got %d", len(got))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/codegen -run TestDetectSignatureConflicts -v`
Expected: FAIL — `undefined: detectSignatureConflicts`.

- [ ] **Step 3: Write minimal implementation**

Append to `internal/codegen/variantcollide.go`:

```go
import "sort" // already imported at top; keep one import block

type conflictComp struct {
	path string
	comp *gsxast.Component
}

type signatureConflict struct {
	key   string
	comps []conflictComp
}

// detectSignatureConflicts finds components that share a componentKey across
// DIFFERENT files but do not share a signature — a genuine ambiguity gsx
// cannot paper over. A key whose cross-file decls all share one signature is a
// tolerated build-tag variant (no conflict); a key declared twice in a single
// file is a within-file redeclaration left to the raw go/types error.
func detectSignatureConflicts(files map[string]*gsxast.File) []signatureConflict {
	type decl struct {
		path string
		comp *gsxast.Component
		sig  string
	}
	byKey := map[string][]decl{}
	// Iterate files in sorted path order for determinism.
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		for _, d := range files[p].Decls {
			c, ok := d.(*gsxast.Component)
			if !ok {
				continue
			}
			key := componentKey(c)
			byKey[key] = append(byKey[key], decl{p, c, componentSignature(c)})
		}
	}

	var out []signatureConflict
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		decls := byKey[key]
		// Distinct files that declare this key.
		fileSet := map[string]bool{}
		sigSet := map[string]bool{}
		for _, d := range decls {
			fileSet[d.path] = true
			sigSet[d.sig] = true
		}
		if len(fileSet) < 2 || len(sigSet) < 2 {
			continue // single-file (within-file) or all one signature (tolerated)
		}
		comps := make([]conflictComp, 0, len(decls))
		for _, d := range decls {
			comps = append(comps, conflictComp{d.path, d.comp})
		}
		out = append(out, signatureConflict{key: key, comps: comps})
	}
	return out
}
```

Merge the `sort` import into the existing import block rather than adding a second one.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/codegen -run TestDetectSignatureConflicts -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/codegen/variantcollide.go internal/codegen/variantcollide_test.go
git commit -m "feat(codegen): detect same-name different-signature component conflicts

Claude-Session: https://claude.ai/code/session_01GCvAa6qCFw2pZRdgwnAyxH"
```

---

### Task 3: Suppress cross-file redeclaration type errors

A `redeclared` / `other declaration of` error pair whose two decls live in **different** skeleton files is a tolerated cross-tag variant (component OR helper) and must be dropped so it does not fail the package. Same-file redeclarations are kept.

**Files:**
- Modify: `internal/codegen/variantcollide.go`
- Test: `internal/codegen/variantcollide_test.go`

**Interfaces:**
- Produces: `func suppressCrossFileRedeclarations(errs []types.Error) []types.Error` — returns `errs` with every redeclaration-class error dropped **iff** the group of errors sharing that declared name references ≥2 distinct files (via `e.Fset.Position(e.Pos).Filename`). Non-redeclaration errors pass through untouched; within-file redeclarations pass through.
- Consumes: `types.Error` (fields `Fset`, `Pos`, `Msg`).

- [ ] **Step 1: Write the failing test**

```go
import "go/types" // add to test imports

func TestSuppressCrossFileRedeclarations(t *testing.T) {
	fset := token.NewFileSet()
	fa := fset.AddFile("a.x.go", -1, 100)
	fb := fset.AddFile("b.x.go", -1, 100)
	posA := fa.Pos(10)
	posB := fb.Pos(10)

	// Cross-file redeclaration of Icon → both dropped.
	// Within-file redeclaration of Dup (both in a.x.go) → both kept.
	// An unrelated type error → kept.
	posA2 := fa.Pos(40)
	posA3 := fa.Pos(60)
	errs := []types.Error{
		{Fset: fset, Pos: posB, Msg: "Icon redeclared in this block"},
		{Fset: fset, Pos: posA, Msg: "other declaration of Icon"},
		{Fset: fset, Pos: posA2, Msg: "Dup redeclared in this block"},
		{Fset: fset, Pos: posA3, Msg: "other declaration of Dup"},
		{Fset: fset, Pos: posA, Msg: "undefined: Whatever"},
	}
	got := suppressCrossFileRedeclarations(errs)

	var msgs []string
	for _, e := range got {
		msgs = append(msgs, e.Msg)
	}
	// Icon pair gone; Dup pair + undefined kept.
	for _, e := range got {
		if strings.Contains(e.Msg, "Icon") {
			t.Fatalf("cross-file Icon redeclaration should be suppressed, got %q", e.Msg)
		}
	}
	if len(got) != 3 {
		t.Fatalf("want 3 kept (2 Dup + 1 undefined), got %d: %v", len(got), msgs)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/codegen -run TestSuppressCrossFileRedeclarations -v`
Expected: FAIL — `undefined: suppressCrossFileRedeclarations`.

- [ ] **Step 3: Write minimal implementation**

Append to `internal/codegen/variantcollide.go`:

```go
import "go/types" // merge into the existing import block

// redeclName extracts the declared name from a go/types redeclaration-class
// error message, or ("", false) if the error is not redeclaration-class. It
// recognizes both records go/types emits: "<name> redeclared in this block"
// (at the second decl) and "other declaration of <name>" (at the first).
func redeclName(msg string) (string, bool) {
	msg = strings.TrimSpace(msg)
	if i := strings.Index(msg, " redeclared"); i > 0 {
		return msg[:i], true
	}
	const p = "other declaration of "
	if strings.HasPrefix(msg, p) {
		return strings.TrimSpace(msg[len(p):]), true
	}
	return "", false
}

// suppressCrossFileRedeclarations drops redeclaration-class errors whose
// declared name is redeclared ACROSS files (a tolerated cross-tag variant of a
// component or helper). A same-file redeclaration keeps its error (a real
// within-file mistake), as do all non-redeclaration errors. gsx does not parse
// build tags; go build remains the arbiter of whether a cross-file same-name
// pair is an actual same-configuration duplicate.
func suppressCrossFileRedeclarations(errs []types.Error) []types.Error {
	// filesByName[name] = set of distinct filenames where a redeclaration-class
	// error for that name was reported.
	filesByName := map[string]map[string]bool{}
	for _, e := range errs {
		if name, ok := redeclName(e.Msg); ok {
			fn := e.Fset.Position(e.Pos).Filename
			if filesByName[name] == nil {
				filesByName[name] = map[string]bool{}
			}
			filesByName[name][fn] = true
		}
	}
	kept := errs[:0]
	for _, e := range errs {
		if name, ok := redeclName(e.Msg); ok && len(filesByName[name]) >= 2 {
			continue // cross-file variant: tolerate
		}
		kept = append(kept, e)
	}
	return kept
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/codegen -run TestSuppressCrossFileRedeclarations -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/codegen/variantcollide.go internal/codegen/variantcollide_test.go
git commit -m "feat(codegen): suppress cross-file redeclaration type errors

Claude-Session: https://claude.ai/code/session_01GCvAa6qCFw2pZRdgwnAyxH"
```

---

### Task 4: Wire suppression + conflicts into analysis and gate emission

Call the Task 3 filter in `analyze`, detect Task 2 conflicts, add friendly diagnostics, store conflicts on the analysis result, and widen the two emit guards so a different-signature conflict blocks emission.

**Files:**
- Modify: `internal/codegen/module_importer.go` (struct `analyzed` ~line 602; typeErrs filter region ~line 943; result assembly ~line 1114)
- Modify: `internal/codegen/module.go` (emit guards at ~line 405 and ~line 457)
- Test: `internal/codegen/variant_generate_test.go` (create)

**Interfaces:**
- Consumes: `suppressCrossFileRedeclarations`, `detectSignatureConflicts` (Tasks 2–3); `bag.Errorf(pos, end token.Pos, code string, format string, args ...any)` (`internal/diag`).
- Produces: new field `signatureConflicts []signatureConflict` on `analyzed`; the emit guards in `module.go` become `len(a.typeErrs) == 0 && len(a.signatureConflicts) == 0`.

- [ ] **Step 1: Write the failing test**

```go
package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeModule writes a go.mod + files and returns the dir.
func writeVariantModule(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	files["go.mod"] = "module variantprobe\n\ngo 1.26\n\nrequire github.com/gsxhq/gsx v0.0.0\n"
	for name, src := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestSameSigVariantGeneratesAllFiles(t *testing.T) {
	dir := writeVariantModule(t, map[string]string{
		"icon_linux.gsx":   "//go:build linux\n\npackage views\n\ncomponent Icon(name string) { <span>linux:{ name }</span> }\n",
		"icon_windows.gsx": "//go:build windows\n\npackage views\n\ncomponent Icon(name string) { <span>win:{ name }</span> }\n",
		"page.gsx":         "package views\n\ncomponent Page() { <Icon name=\"x\"/> }\n",
	})
	m := newTestModule(t, dir) // see note below
	out, diags, err := m.Generate(dir)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if hasError(diags) {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	// All three .gsx must produce .x.go — the redeclared must NOT skip the package.
	for _, want := range []string{"icon_linux.gsx", "icon_windows.gsx", "page.gsx"} {
		if _, ok := out[want]; !ok {
			t.Fatalf("missing generated output for %s; got keys %v", want, keysOf(out))
		}
	}
	// Each variant's generated code carries its //go:build directive.
	if !strings.Contains(string(out["icon_linux.gsx"]), "//go:build linux") {
		t.Fatalf("linux variant lost its build constraint")
	}
}

func TestDiffSigVariantIsCleanError(t *testing.T) {
	dir := writeVariantModule(t, map[string]string{
		"icon_linux.gsx":   "//go:build linux\n\npackage views\n\ncomponent Icon(name string) { <span>{ name }</span> }\n",
		"icon_windows.gsx": "//go:build windows\n\npackage views\n\ncomponent Icon(name int) { <span>{ name }</span> }\n",
	})
	m := newTestModule(t, dir)
	out, diags, err := m.Generate(dir)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !hasError(diags) {
		t.Fatalf("expected a duplicate-component error, got none")
	}
	foundClean := false
	for _, d := range diags {
		if d.Code == "duplicate-component" {
			foundClean = true
		}
		if strings.Contains(d.Message, "redeclared in this block") {
			t.Fatalf("raw redeclared error leaked: %q", d.Message)
		}
	}
	if !foundClean {
		t.Fatalf("no duplicate-component diagnostic in %v", diags)
	}
	if len(out) != 0 {
		t.Fatalf("diff-sig conflict must block emission, got %v", keysOf(out))
	}
}
```

Before writing production code, discover the existing test helpers for constructing a `Module` and inspecting diagnostics: `grep -rn "func New(\|func.*Module\b\|hasError\|newTestModule\|Severity == diag.Error" internal/codegen/*_test.go internal/codegen/module.go`. Reuse the real constructor (likely `codegen.New(opts)` / `NewModule`). Implement `newTestModule`, `hasError`, `keysOf` as thin local helpers in the test file if they don't already exist (a `diag.Diagnostic` has `.Severity`, `.Code`, `.Message`).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/codegen -run 'TestSameSigVariant|TestDiffSigVariant' -v`
Expected: FAIL — `TestSameSigVariant` fails because the redeclared error skips the whole package (empty `out`); `TestDiffSigVariant` fails because `signatureConflicts`/`duplicate-component` don't exist yet.

- [ ] **Step 3a: Add the struct field**

In `internal/codegen/module_importer.go`, add to `type analyzed struct` (near the `typeErrs` field, ~line 625):

```go
	signatureConflicts []signatureConflict // same-name different-signature component collisions (block emission)
```

- [ ] **Step 3b: Suppress redeclarations after the existing typeErrs filter**

In `analyze`, immediately AFTER the `sunkImports` filter block that ends with `typeErrs = kept` (~line 943, before the `quietSpans` loop), insert:

```go
	// Tolerate cross-file duplicate top-level decls (same-name build-tag
	// variants — components or helpers). gsx does not parse build tags; go
	// build filters by tag and is the arbiter of a real same-config duplicate.
	// Runs before the diagnostics loop below so a tolerated redeclaration never
	// becomes a bag diagnostic AND never lands in the stored a.typeErrs (which
	// gates emission). Same-file redeclarations are untouched.
	typeErrs = suppressCrossFileRedeclarations(typeErrs)
```

- [ ] **Step 3c: Detect conflicts and add friendly diagnostics**

In `analyze`, near where the result is assembled (before `return &analyzed{...}` ~line 1114), add:

```go
	// A same-name component declared with DIFFERENT signatures across files is a
	// genuine ambiguity (its cross-file redeclaration was suppressed above, so
	// nothing else will flag it). Report a clean duplicate-component error at
	// each site and record the conflict so emission is blocked (module.go gates
	// on len(signatureConflicts)==0 as well as len(typeErrs)==0).
	sigConflicts := detectSignatureConflicts(gsxFiles)
	for _, sc := range sigConflicts {
		var files []string
		for _, cc := range sc.comps {
			files = append(files, filepath.Base(cc.path))
		}
		for _, cc := range sc.comps {
			bag.Errorf(cc.comp.NamePos, cc.comp.NamePos+token.Pos(len(cc.comp.Name)), "duplicate-component",
				"component %s is declared with different signatures across build-tagged files (%s); build-tag variants must share the same signature — rename the variants or align their parameters",
				cc.comp.Name, strings.Join(files, ", "))
		}
	}
```

Confirm `gsxFiles` (the `map[string]*gsxast.File`) is in scope at this point in `analyze` (it is — it is iterated earlier in the skeleton-build loop). Confirm `filepath`, `token`, `strings` are imported (they are).

Then add `signatureConflicts: sigConflicts,` to the `return &analyzed{...}` literal.

- [ ] **Step 3d: Widen the emit guards**

In `internal/codegen/module.go`, change BOTH guards:

Line ~405 (`Package`):
```go
	if len(a.typeErrs) == 0 && len(a.signatureConflicts) == 0 {
```
Line ~457 (`Generate`):
```go
	if len(a.typeErrs) == 0 && len(a.signatureConflicts) == 0 {
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/codegen -run 'TestSameSigVariant|TestDiffSigVariant' -v`
Expected: PASS. Then the whole package: `go test ./internal/codegen` — expected PASS (no regressions).

- [ ] **Step 5: Commit**

```bash
git add internal/codegen/module_importer.go internal/codegen/module.go internal/codegen/variant_generate_test.go
git commit -m "feat(codegen): tolerate same-sig variants, block diff-sig conflicts

Suppress cross-file redeclarations so a same-name build-tag variant no longer
skips the whole package; detect same-name/different-signature component
collisions and report a clean duplicate-component error that blocks emission.

Claude-Session: https://claude.ai/code/session_01GCvAa6qCFw2pZRdgwnAyxH"
```

---

### Task 5: Corpus cases + `go build -tags` probe

Pin the behavior in the canonical txtar corpus and prove the generated variants actually compile-select under `go build -tags`.

**Files:**
- Create: `internal/corpus/testdata/cases/buildtags/variant_same_sig.txtar`
- Create: `internal/corpus/testdata/cases/buildtags/variant_diff_sig.txtar`
- Create: `internal/corpus/testdata/cases/buildtags/redeclare_within_file.txtar`
- Create: `internal/corpus/testdata/cases/buildtags/helper_variant.txtar`
- Create: `internal/codegen/buildtag_select_test.go`

**Interfaces:** none (golden + build tests).

- [ ] **Step 1: Write the same-signature corpus case**

`internal/corpus/testdata/cases/buildtags/variant_same_sig.txtar` — a host-neutral pair of constraints (`!never` / `never`) so `render.golden` runs everywhere and only ONE variant is active during the corpus render build:

```
-- icon.gsx --
//go:build !never

package views

component Icon(name string) {
	<span class="active">{ name }</span>
}
-- icon_never.gsx --
//go:build never

package views

component Icon(name string) {
	<span class="never">{ name }</span>
}
-- page.gsx --
package views

component Page() {
	<Icon name="x"/>
}
-- invoke --
Page()
-- diagnostics.golden --
-- render.golden --
<span class="active">x</span>
```

- [ ] **Step 2: Write the different-signature (error) corpus case**

`internal/corpus/testdata/cases/buildtags/variant_diff_sig.txtar`:

```
-- icon.gsx --
//go:build !never

package views

component Icon(name string) {
	<span>{ name }</span>
}
-- icon_never.gsx --
//go:build never

package views

component Icon(name int) {
	<span>{ name }</span>
}
-- invoke --
-- diagnostics.golden --
```

Leave `diagnostics.golden` empty for now; `-update` fills it. Verify after update that it contains a `duplicate-component` diagnostic and NO `redeclared in this block`. (No `render.golden` — an error case emits nothing.)

- [ ] **Step 3: Write the within-file redeclaration case (still an error)**

`internal/corpus/testdata/cases/buildtags/redeclare_within_file.txtar`:

```
-- input.gsx --
package views

component Icon(name string) {
	<a/>
}

component Icon(name string) {
	<b/>
}
-- invoke --
-- diagnostics.golden --
```

`-update` fills `diagnostics.golden`; verify it retains the raw `redeclared in this block` (within-file is NOT tolerated).

- [ ] **Step 4: Write the non-component helper-variant case**

`internal/corpus/testdata/cases/buildtags/helper_variant.txtar` — two files with a same-name GoChunk helper under disjoint tags, tolerated (generates):

```
-- icon.gsx --
//go:build !never

package views

{{
	const iconPath = "/active.svg"
}}

component Icon() {
	<img src={ iconPath }/>
}
-- icon_never.gsx --
//go:build never

package views

{{
	const iconPath = "/never.svg"
}}

component IconNever() {
	<img src={ iconPath }/>
}
-- invoke --
Icon()
-- diagnostics.golden --
-- render.golden --
<img src="/active.svg"/>
```

(Distinct component names — `Icon`/`IconNever` — isolate the *helper* `iconPath` collision from the component path; the point is that the duplicate `iconPath` const across files no longer fails the package.)

- [ ] **Step 5: Regenerate goldens and verify**

Run:
```bash
go test ./internal/corpus -run TestCorpus -update
go test ./internal/corpus -run TestCorpus
```
Expected: PASS. Inspect the four new `*.golden` blocks — confirm `variant_diff_sig` diagnostics show `duplicate-component` and no `redeclared`, and `redeclare_within_file` keeps the raw `redeclared`.

If the corpus harness does not pick up multi-`.gsx` cases automatically, check `internal/corpus/batch.go` (`packageNameInDir`, the `.gsx` glob) — cases here rely on every `*.gsx` in the case dir being one package. Confirm with `go test ./internal/corpus -run TestCorpus/buildtags -v`.

- [ ] **Step 6: Write the `go build -tags` selection test**

`internal/codegen/buildtag_select_test.go` — generate a variant module, write the `.x.go`, and confirm `go build -tags` picks the matching variant (mirrors the existing `TestBuildTagExcludesGeneratedFile` in `directive_passthrough_test.go` — read it first for the exact scaffold/module-wiring idiom):

```go
package codegen

import (
	"os/exec"
	"strings"
	"testing"
)

// Generates two same-sig Icon variants gated on a custom tag, writes .x.go,
// and asserts `go build -tags variantB` compiles the B body (and A is excluded).
func TestBuildTagSelectsVariant(t *testing.T) {
	// Reuse the directive_passthrough_test.go scaffolding helpers (probe module
	// with a real gsx dependency via go.mod replace). See that file for
	// writeProbeModule / runGoBuild equivalents; replicate its setup here.
	t.Skip("implement using the directive_passthrough_test.go scaffold idiom")
	_ = exec.Command
	_ = strings.TrimSpace
}
```

Replace the `t.Skip` with a real build once you have read `directive_passthrough_test.go`: two variants `//go:build variantA` / `//go:build variantB`, `Generate`, write outputs to disk, `go build -tags variantB ./...` succeeds, and a body-unique string (e.g. a distinct static text per variant) confirms selection. Do NOT leave the skip in the final commit.

- [ ] **Step 7: Run and commit**

Run: `go test ./internal/corpus ./internal/codegen -run 'TestCorpus|TestBuildTagSelectsVariant'`
Expected: PASS.

```bash
git add internal/corpus/testdata/cases/buildtags internal/codegen/buildtag_select_test.go
git commit -m "test(codegen): corpus + build-tag probe for tag-variant components

Claude-Session: https://claude.ai/code/session_01GCvAa6qCFw2pZRdgwnAyxH"
```

---

### Task 6: Multi-valued component cross-index (codegen side)

Make the cross-navigation index carry every variant's declaration position, so downstream LSP can show them all.

**Files:**
- Modify: `internal/codegen/results.go` (`CrossRef` ~line 17)
- Modify: `internal/codegen/module_importer.go` (`compByKey` build ~line 1033)
- Modify: `internal/codegen/crossnav.go`
- Test: `internal/codegen/crossnav_test.go` (create or extend)

**Interfaces:**
- Produces: `CrossRef` gains `Decls []token.Position` (all variant name positions, sorted by filename then offset). `Decl` stays as the primary/first for backward compatibility. The internal `compByKey` becomes `map[string][]*gsxast.Component`.
- Consumes: unchanged callers still read `CrossRef.Decl`/`.Refs`.

- [ ] **Step 1: Write the failing test**

```go
package codegen

import "testing"

func TestCrossIndexMultiValuedVariants(t *testing.T) {
	dir := writeVariantModule(t, map[string]string{
		"icon_a.gsx": "//go:build !never\n\npackage views\n\ncomponent Icon(name string) { <a>{ name }</a> }\n",
		"icon_b.gsx": "//go:build never\n\npackage views\n\ncomponent Icon(name string) { <b>{ name }</b> }\n",
	})
	m := newTestModule(t, dir)
	pkg, err := m.Package(dir) // retained analysis path used by the LSP
	if err != nil {
		t.Fatalf("Package: %v", err)
	}
	cr, ok := pkg.CrossIndex[".Icon"]
	if !ok {
		t.Fatal("no CrossIndex entry for .Icon")
	}
	if len(cr.Decls) != 2 {
		t.Fatalf("want 2 variant decls, got %d (%v)", len(cr.Decls), cr.Decls)
	}
	if !cr.Decl.IsValid() {
		t.Fatal("primary Decl must stay valid for back-compat")
	}
}
```

Check the retained-analysis accessor name with `grep -n "func (m \*Module) Package\|CrossIndex" internal/codegen/module.go internal/codegen/results.go`; adapt `m.Package(dir)` / `pkg.CrossIndex` to the real API.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/codegen -run TestCrossIndexMultiValuedVariants -v`
Expected: FAIL — `cr.Decls undefined` (compile error) → add the field first, then the length assertion fails (only 1 today).

- [ ] **Step 3a: Add the field**

`internal/codegen/results.go`:

```go
type CrossRef struct {
	Name  string
	Decl  token.Position   // primary (first) variant — back-compat
	Decls []token.Position // all variant declaration positions (≥1)
	Refs  []token.Position
}
```

- [ ] **Step 3b: Make compByKey multi-valued**

`internal/codegen/module_importer.go` ~line 1033: change

```go
	compByKey := map[string]*gsxast.Component{} // componentKey -> component
```
to
```go
	compByKey := map[string][]*gsxast.Component{} // componentKey -> component(s); >1 = build-tag variants
```
and the population (`compByKey[componentKey(c)] = c`) to append:
```go
		for _, c := range comps {
			key := componentKey(c)
			compByKey[key] = append(compByKey[key], c)
		}
```
Update the `analyzed` field type (`compByKey map[string]*gsxast.Component` ~line 621) to `map[string][]*gsxast.Component`.

- [ ] **Step 3c: Update buildCrossNav**

`internal/codegen/crossnav.go` — change the signature's `compByKey` param to `map[string][]*gsxast.Component`, and rework the two loops that iterate it:

```go
	index := map[string]CrossRef{}
	for key, comps := range compByKey {
		if len(comps) == 0 {
			continue
		}
		cr := CrossRef{Name: comps[0].Name}
		for _, c := range comps {
			cr.Decls = append(cr.Decls, gsxFset.Position(c.NamePos))
		}
		sortPositions(cr.Decls) // stable: filename then offset
		cr.Decl = cr.Decls[0]
		index[key] = cr
	}
```

The props-struct/field loop (`for _, c := range compByKey`) must iterate variants too:
```go
	for _, comps := range compByKey {
		for _, c := range comps {
			// ... existing per-component body, unchanged ...
		}
	}
```

In the `info.Uses` loop, `compByKey[key]` is now a slice — use `compByKey[key][0]` for the `c` used to build the NavRef target (any variant's NamePos is a valid jump target; the first is deterministic after sorting is applied at index-build time — here just take `[0]`), and guard `len(...) > 0`.

Add a small helper at the bottom of `crossnav.go`:
```go
func sortPositions(ps []token.Position) {
	sort.Slice(ps, func(i, j int) bool {
		if ps[i].Filename != ps[j].Filename {
			return ps[i].Filename < ps[j].Filename
		}
		return ps[i].Offset < ps[j].Offset
	})
}
```
and import `"sort"`.

- [ ] **Step 3d: Fix the call site**

`internal/codegen/module.go:413` — `buildCrossNav(a.compByKey, ...)` now passes the slice map; no textual change needed if the field type was updated, but rebuild to confirm the types line up.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/codegen -run TestCrossIndexMultiValuedVariants -v`
Then the whole package: `go test ./internal/codegen`.
Expected: PASS (existing cross-nav / LSP-bridge tests still green — `Decl` unchanged for single-decl components).

- [ ] **Step 5: Commit**

```bash
git add internal/codegen/results.go internal/codegen/module_importer.go internal/codegen/crossnav.go internal/codegen/module.go internal/codegen/crossnav_test.go
git commit -m "feat(codegen): multi-valued component cross-index for build-tag variants

Claude-Session: https://claude.ai/code/session_01GCvAa6qCFw2pZRdgwnAyxH"
```

---

### Task 7: LSP go-to-definition shows all variants

`textDocument/definition` on a `<Icon/>` tag returns every variant's location.

**Files:**
- Modify: `internal/lsp/analysis.go` (mirror `CrossRef` ~line 15 — add `Decls`)
- Modify: `internal/lsp/definition.go` (`componentTagDeclAt` ~line 612; `handleDefinition` ~line 400)
- Test: `internal/lsp/definition_test.go` (extend) or `internal/lsp/variant_nav_test.go` (create)

**Interfaces:**
- Consumes: `codegen.CrossRef.Decls` (Task 6), surfaced onto the LSP `Package.CrossIndex` (the LSP mirrors codegen's CrossRef — confirm the mirror/copy site with `grep -n "CrossIndex\|CrossRef{" internal/lsp/*.go`).
- Produces: `componentTagDeclAt(pkg, path, off) ([]token.Position, bool)` returning ALL variant decls; `handleDefinition` replies a `[]Location` when >1.

- [ ] **Step 1: Write the failing test**

Read `internal/lsp/definition_test.go` for the existing harness (how a `Server`/`Package` is built and how `handleDefinition` is driven). Then add:

```go
func TestDefinitionShowsAllVariants(t *testing.T) {
	// Build an LSP package with two same-sig Icon variants + a Page that uses <Icon/>.
	// Position the cursor on the "Icon" tag name in page.gsx and expect TWO locations.
	// (Follow the existing definition_test.go setup for constructing srv/pkg and
	//  issuing a textDocument/definition request; assert the result is a []Location
	//  of length 2, one per variant .gsx file.)
	t.Fatal("write against the definition_test.go harness")
}
```

Replace the `t.Fatal` scaffold with the concrete request/assert using the file's existing helpers (mirror an existing D2 tag-definition test and change the fixture to two variant files + assert 2 results).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/lsp -run TestDefinitionShowsAllVariants -v`
Expected: FAIL (single location today).

- [ ] **Step 3a: Mirror Decls onto the LSP CrossRef**

In `internal/lsp/analysis.go`, add `Decls []token.Position` to the LSP `CrossRef` struct, and wherever the LSP copies `codegen.CrossRef` → `lsp.CrossRef` (the mirror site), copy `Decls` too.

- [ ] **Step 3b: Return all variant decls from componentTagDeclAt**

`internal/lsp/definition.go` — change the signature and the CrossIndex lookup:

```go
func componentTagDeclAt(pkg *Package, path string, off int) ([]token.Position, bool) {
	// ... unchanged element-walk to detect the cursor is on a tag name ...
			if onOpen || onClose {
				key := "." + tag
				cr, ok := pkg.CrossIndex[key]
				if ok {
					decls := cr.Decls
					if len(decls) == 0 && cr.Decl.IsValid() {
						decls = []token.Position{cr.Decl}
					}
					if len(decls) > 0 {
						result = decls
						found = true
					}
				}
			}
	// ...
	return result, found
}
```
(Change the local `result` from `token.Position` to `[]token.Position`.)

- [ ] **Step 3c: Reply multiple locations**

`internal/lsp/definition.go:400` in `handleDefinition`:

```go
	if decls, ok := componentTagDeclAt(pkg, path, off); ok {
		if len(decls) == 1 {
			return s.reply(f.ID, s.locationForPos(decls[0]))
		}
		locs := make([]Location, 0, len(decls))
		for _, d := range decls {
			locs = append(locs, s.locationForPos(d))
		}
		return s.reply(f.ID, locs)
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/lsp -run TestDefinitionShowsAllVariants -v`
Then `go test ./internal/lsp`.
Expected: PASS (single-decl definition tests unchanged — they still get one `Location`).

- [ ] **Step 5: Commit**

```bash
git add internal/lsp/analysis.go internal/lsp/definition.go internal/lsp/variant_nav_test.go
git commit -m "feat(lsp): go-to-definition returns all build-tag component variants

Claude-Session: https://claude.ai/code/session_01GCvAa6qCFw2pZRdgwnAyxH"
```

---

### Task 8: LSP find-references includes all variant declarations

`textDocument/references` lists every variant's declaration alongside the reference sites.

**Files:**
- Modify: `internal/lsp/references.go` (~line 62 result assembly; ~line 76 `identifyCrossRef`)
- Test: `internal/lsp/variant_nav_test.go` (extend)

**Interfaces:**
- Consumes: `CrossRef.Decls` (Tasks 6–7).
- Produces: references result includes each `Decls` entry (deduped), not only `Decl`.

- [ ] **Step 1: Write the failing test**

Extend `internal/lsp/variant_nav_test.go`:

```go
func TestReferencesIncludesAllVariantDecls(t *testing.T) {
	// Two same-sig Icon variants + a Page using <Icon/>. Invoke find-references
	// from the tag site; expect BOTH variant declaration locations to appear in
	// the results (plus the reference site). Use the references_test.go harness.
	t.Fatal("write against the references_test.go harness")
}
```

Replace the scaffold using the existing `internal/lsp/references_test.go` helpers.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/lsp -run TestReferencesIncludesAllVariantDecls -v`
Expected: FAIL (only one decl today).

- [ ] **Step 3: Emit every variant decl**

`internal/lsp/references.go` — where the result locations are assembled (~line 62):

```go
	locs := make([]Location, 0, len(found.Refs)+len(found.Decls))
	seen := map[string]bool{}
	addDecl := func(p token.Position) {
		if !p.IsValid() {
			return
		}
		k := p.Filename + ":" + strconv.Itoa(p.Offset)
		if seen[k] {
			return
		}
		seen[k] = true
		locs = append(locs, s.locationForPos(p))
	}
	decls := found.Decls
	if len(decls) == 0 {
		decls = []token.Position{found.Decl}
	}
	for _, d := range decls {
		addDecl(d)
	}
	for _, r := range found.Refs {
		locs = append(locs, s.locationForRef(r)) // keep existing ref mapping
	}
```

Match the existing ref-location call (the code around line 63 already maps `found.Refs` — preserve that exact call; only the decl side changes). Import `"strconv"` if not present.

Also update `identifyCrossRef` (~line 76): a cursor on ANY variant decl should identify the ref — change the `posCoversCursor(cr.Decl, ...)` check to also scan `cr.Decls`:

```go
	for _, cr := range refs {
		if posCoversCursor(cr.Decl, path, curLine, curCol, len(cr.Name)) {
			return &cr
		}
		for _, d := range cr.Decls {
			if posCoversCursor(d, path, curLine, curCol, len(cr.Name)) {
				return &cr
			}
		}
		for _, r := range cr.Refs {
			// ... existing ref check ...
		}
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/lsp -run TestReferencesIncludesAllVariantDecls -v`
Then `go test ./internal/lsp`.
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/lsp/references.go internal/lsp/variant_nav_test.go
git commit -m "feat(lsp): find-references lists all build-tag component variant decls

Claude-Session: https://claude.ai/code/session_01GCvAa6qCFw2pZRdgwnAyxH"
```

---

### Task 9: Docs, ROADMAP, and full CI

Document the rule and flip the ROADMAP item; run the authoritative CI.

**Files:**
- Modify: `docs/guide/syntax.md` (§Build constraints)
- Modify: `docs/ROADMAP.md` (the "Tag-aware `.gsx` analysis" item ~line 543)

**Interfaces:** none.

- [ ] **Step 1: Document the variant rule**

In `docs/guide/syntax.md`, under the existing §Build constraints section, add prose (keep any literal `{{ }}` out of prose, or wrap in `::: v-pre` — but this section has none):

```markdown
### Build-tag component variants

Two `.gsx` files under mutually exclusive `//go:build` constraints may declare a
component with the **same name**, as long as they share the **same signature**
(same props, same generic parameters, same receiver). gsx generates a `.x.go`
for every file regardless of tags; `go build` then compiles exactly the variant
whose constraint matches the build, exactly as it does for Go's own
`foo_linux.go` / `foo_windows.go` files.

gsx does not evaluate build tags itself. If two same-named components have
**different** signatures, gsx reports a `duplicate-component` error — build-tag
variants must be drop-in replacements. A genuinely duplicated component with no
tags is reported by `go build` (the generated files collide), which remains the
authority on same-configuration duplicates.

Editor go-to-definition and find-references on such a component show **all**
variants, so you can jump to the platform you care about.
```

- [ ] **Step 2: Flip the ROADMAP item**

In `docs/ROADMAP.md`, change the `- [ ] **Tag-aware \`.gsx\` analysis**` bullet (~line 543) to `- [x]` and rewrite its body to reflect the shipped design:

```markdown
- [x] **Tag-aware `.gsx` analysis** — two `.gsx` files gated by disjoint
  `//go:build` tags may declare the same component when their signatures match:
  the cross-file `redeclared` type errors are suppressed so `Generate` emits all
  files (go build filters by tag and arbitrates real same-config duplicates),
  while a same-name/*different*-signature component collision is a clean
  `duplicate-component` error that blocks emission. gsx never parses build
  constraints. LSP go-to-definition / find-references are multi-valued over the
  variants. Non-component cross-file helper duplicates are tolerated (deferred to
  go build); within-file redeclarations stay hard errors. Spec
  `2026-07-06-tag-variant-component-analysis-design.md`.
```

- [ ] **Step 3: Run the authoritative CI**

Run: `make check`
Expected: PASS (build/vet/test both modules, examples drift, gofmt + gsx fmt). Fix any drift (`go test ./internal/corpus -run TestCorpus -update` if a golden or `coverage.golden` manifest needs a bump; `gofmt -w` / `go run ./cmd/gsx fmt` for formatting).

- [ ] **Step 4: Commit**

```bash
git add docs/guide/syntax.md docs/ROADMAP.md internal/corpus/testdata/cases
git commit -m "docs: build-tag component variants; flip ROADMAP tag-aware analysis

Claude-Session: https://claude.ai/code/session_01GCvAa6qCFw2pZRdgwnAyxH"
```

- [ ] **Step 5: Final full CI before merge**

Run: `make ci`
Expected: PASS (uncached, `-count=1`). This is the gate GitHub runs.

---

## Self-Review

**Spec coverage:**
- Detection + signature comparison → Tasks 1–2. ✓
- Suppress cross-file redeclarations (components + helpers) so `Generate` emits all → Task 3 + Task 4 (wiring + guard). ✓
- Different-signature → clean `duplicate-component` error blocking emission → Task 4. ✓
- Within-file redeclaration stays an error → Task 3 logic + Task 5 corpus. ✓
- Non-component helper cross-file dup tolerated → Task 3 (name-agnostic) + Task 5 corpus. ✓
- Build tags stay pure pass-through, no `constraint.Parse` → enforced by Global Constraints; no task imports it. ✓
- Emit unchanged, best-effort Info → no emit task; validated by Task 5 `render.golden` + build probe. ✓
- LSP multi-valued go-to-def + find-refs → Tasks 6–8. ✓
- Caching: no `computeKey` change → nothing to do (source content already keys the cache); called out here, no task needed. ✓
- Tests (corpus, build probe, LSP) → Tasks 5, 7, 8. Docs + ROADMAP → Task 9. ✓

**Placeholder scan:** Two tasks (7, 8 test steps and the Task 5 build probe) intentionally defer the exact test body to "write against the existing harness" because the LSP/build-probe test scaffolds are fixture-heavy and must mirror sibling tests verbatim — each names the exact sibling file to copy (`definition_test.go`, `references_test.go`, `directive_passthrough_test.go`) and the exact assertion (2 locations / both decls / tag-selected body). The `t.Fatal`/`t.Skip` scaffolds must be replaced before their commits; the steps say so explicitly.

**Type consistency:** `componentSignature(*gsxast.Component) string`, `detectSignatureConflicts(map[string]*gsxast.File) []signatureConflict`, `suppressCrossFileRedeclarations([]types.Error) []types.Error`, `signatureConflict{key string; comps []conflictComp}`, `conflictComp{path string; comp *gsxast.Component}`, `CrossRef.Decls []token.Position`, `compByKey map[string][]*gsxast.Component`, `componentTagDeclAt(...) ([]token.Position, bool)` — used consistently across Tasks 1–8.
