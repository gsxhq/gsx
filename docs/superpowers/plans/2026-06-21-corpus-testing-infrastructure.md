# Corpus Testing Infrastructure Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the 2378-line `internal/codegen/e2e_test.go` monolith (91 per-test `go run`s, ~49s) with one txtar fixture spine under `internal/corpus` that builds+runs every case in a single `go build`, pins tiered goldens, and exposes a coverage index.

**Architecture:** A `corpus` package gains pure, unit-tested helpers — a txtar case **loader**, a quoted-import-path **rewriter**, a **batch** assembler/runner, an HTML structural **comparer**, and a **coverage** index builder. A single `TestCorpus` orchestrates: load all cases → run parser+codegen in-process and compare diagnostics/generated/ast facets → assemble all renderable cases into one shared temp module → one `go build` + run → split NUL-delimited output → compare each to `render.golden`. Cross-package cases fold into the same build via module-path rewriting. e2e tests migrate feature-area by feature-area until the monolith is deleted.

**Tech Stack:** Go 1.26, `internal/txtar` (vendored), `internal/codegen.GeneratePackage`, `parser.ParseFile`, `ast.Fprint`, `golang.org/x/net/html` (structural HTML compare), `go build`/`os/exec`.

## Global Constraints

- Go version floor in synthesized `go.mod` files: `go 1.26.1` (matches existing test harness strings).
- Generated test module path: `corpustest`; each case's import root is `corpustest/cases/<casedir>` where `<casedir>` is the case's path under `testdata/cases/` with `/` → `_`.
- The gsx runtime is wired via `require github.com/gsxhq/gsx v0.0.0` + `replace github.com/gsxhq/gsx => <repoRootAbs>`, where `repoRootAbs = filepath.Abs("../..")` from the `internal/corpus` package dir.
- `Node.Render(ctx context.Context, w io.Writer) error` is the render entry point.
- Reserved generated identifier in entry wrappers: `GsxEntryRender` (exported, must not collide with user code).
- Pure helpers (no `*testing.T`, no `testing` import) live in regular `.go` files so they get their own unit tests; only `*testing.T` orchestration lives in `_test.go`.
- Golden updates happen only under `-update`; never write goldens during a normal run.
- `unexported` for all new helper types/functions unless cross-package use requires otherwise (per repo convention).
- Run all `go` commands from the worktree root `/Users/jackieli/personal/gsxhq/gsx/.claude/worktrees/corpus-infrastructure`.

---

## File Structure

All new code lives in `internal/corpus/` (package `corpus`, an internal test-support package):

- `loader.go` — `caseDoc` type + `loadCase(path string) (*caseDoc, error)`: parse a txtar into named sections, classify single- vs multi-package layout, expose facet presence and source/golden files.
- `rewrite.go` — `rewriteImportPath(src []byte, oldPath, newPath string) []byte`: rewrite quoted import paths whose path equals `oldPath` or is under `oldPath + "/"`. Leaves all other imports (stdlib, gsx runtime, third-party) untouched.
- `batch.go` — assemble a shared temp module from all renderable cases, generate per-case entry wrappers + a fan-in `main.go`, `go build` + run once, split NUL-delimited stdout into `map[caseName]string`.
- `htmlcompare.go` — `htmlStructuralDiff(got, want string) (diff string, err error)`: whitespace- and attr-order-insensitive structural HTML comparison (moved verbatim from `e2e_test.go`).
- `coverage.go` — `coverageReport(cases []*caseDoc) []byte`: one line per case with its pinned facets, plus a TOTAL line.
- `corpus_test.go` — REPLACES current `TestPipeline`. `TestCorpus` orchestrator + `-update`. (The `update` flag and txtar archive helpers `setSection`/`checkSection`/`writeArchive` already exist here and are reused.)
- `loader_test.go`, `rewrite_test.go`, `htmlcompare_test.go`, `coverage_test.go` — unit tests for the pure helpers.
- `testdata/cases/**/*.txtar` — migrated fixtures (new tree; see Phase 1+).
- `examples_test.go` — unchanged.

Removed at the end: `internal/codegen/e2e_test.go` (helpers move to `corpus`), and `internal/corpus/testdata/pipeline/` (moved into `testdata/cases/`).

---

## Phase 0 — Infrastructure

### Task 1: Case loader + facet model

**Files:**
- Create: `internal/corpus/loader.go`
- Test: `internal/corpus/loader_test.go`

**Interfaces:**
- Produces:
  - `type caseDoc struct { name string; dir string; archive *txtar.Archive; files map[string][]byte; invoke []byte; goldens map[string][]byte; multiPkg bool; modulePath string }`
    - `name`: case path under `testdata/cases/`, slash-separated, no `.txtar` (e.g. `attrs/expr_attrs`).
    - `dir`: importable suffix, `name` with `/`→`_` (e.g. `attrs_expr_attrs`).
    - `files`: source files (anything not a `*.golden`, not `invoke`) keyed by their txtar name (e.g. `input.gsx`, `ui/button.gsx`, `go.mod`).
    - `invoke`: bytes of the `invoke` section (nil if absent).
    - `goldens`: section name → bytes for `diagnostics.golden`, `render.golden`, `generated.x.go.golden`, `ast.golden` (only those present).
    - `multiPkg`: true when any source file name contains `/` (a subdirectory package) or a `go.mod` section is present.
    - `modulePath`: declared module path from `go.mod` if present, else `""`.
  - `func loadCase(path string) (*caseDoc, error)`
  - `func (c *caseDoc) renderable() bool` — true when `c.invoke != nil`.
  - `func (c *caseDoc) facets() []string` — sorted facet tags: always `"diag"`; `"render"` if `render.golden` present; `"gen"` if `generated.x.go.golden` present; `"ast"` if `ast.golden` present; if non-renderable and diagnostics non-empty, `"diag(error)"` replaces `"diag"`.

- [ ] **Step 1: Write the failing test**

```go
package corpus

import "testing"

func TestLoadCaseSinglePackage(t *testing.T) {
	c, err := loadCase("testdata/loadertest/single.txtar")
	if err != nil {
		t.Fatal(err)
	}
	if c.name != "loadertest/single" {
		t.Errorf("name = %q, want loadertest/single", c.name)
	}
	if c.dir != "loadertest_single" {
		t.Errorf("dir = %q, want loadertest_single", c.dir)
	}
	if c.multiPkg {
		t.Errorf("multiPkg = true, want false")
	}
	if string(c.invoke) != "Greeting(GreetingProps{Name: \"X\"})\n" {
		t.Errorf("invoke = %q", c.invoke)
	}
	if _, ok := c.files["input.gsx"]; !ok {
		t.Errorf("missing input.gsx in files")
	}
	if _, ok := c.goldens["render.golden"]; !ok {
		t.Errorf("missing render.golden in goldens")
	}
	if !c.renderable() {
		t.Errorf("renderable() = false, want true")
	}
}
```

- [ ] **Step 2: Create the fixture, then run the test to verify it fails**

Create `internal/corpus/testdata/loadertest/single.txtar`:

```
-- input.gsx --
package views

component Greeting(name string) { <p>Hi {name}</p> }
-- invoke --
Greeting(GreetingProps{Name: "X"})
-- diagnostics.golden --
-- render.golden --
<p>Hi X</p>
```

Run: `go test ./internal/corpus/ -run TestLoadCaseSinglePackage -v`
Expected: FAIL with `undefined: loadCase`.

- [ ] **Step 3: Write minimal implementation**

```go
package corpus

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gsxhq/gsx/internal/txtar"
)

type caseDoc struct {
	name       string
	dir        string
	archive    *txtar.Archive
	files      map[string][]byte
	invoke     []byte
	goldens    map[string][]byte
	multiPkg   bool
	modulePath string
}

var goldenSections = map[string]bool{
	"diagnostics.golden":    true,
	"render.golden":         true,
	"generated.x.go.golden": true,
	"ast.golden":            true,
}

// loadCase parses one txtar case file. name is derived from path relative to
// testdata/cases (or any testdata/<root>): the portion after "testdata/<root>/"
// minus the .txtar suffix.
func loadCase(path string) (*caseDoc, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	arc := txtar.Parse(data)
	c := &caseDoc{
		archive: arc,
		files:   map[string][]byte{},
		goldens: map[string][]byte{},
	}
	// name: strip leading "testdata/<root>/" and trailing ".txtar".
	rel := filepath.ToSlash(path)
	if i := strings.Index(rel, "testdata/"); i >= 0 {
		rel = rel[i+len("testdata/"):]
		if j := strings.IndexByte(rel, '/'); j >= 0 {
			rel = rel[j+1:]
		}
	}
	c.name = strings.TrimSuffix(rel, ".txtar")
	c.dir = strings.ReplaceAll(c.name, "/", "_")

	for _, f := range arc.Files {
		switch {
		case f.Name == "invoke":
			c.invoke = f.Data
		case goldenSections[f.Name]:
			c.goldens[f.Name] = f.Data
		default:
			c.files[f.Name] = f.Data
			if strings.Contains(f.Name, "/") {
				c.multiPkg = true
			}
			if f.Name == "go.mod" {
				c.multiPkg = true
				c.modulePath = parseModulePath(f.Data)
			}
		}
	}
	return c, nil
}

func parseModulePath(gomod []byte) string {
	for _, line := range strings.Split(string(gomod), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module "))
		}
	}
	return ""
}

func (c *caseDoc) renderable() bool { return c.invoke != nil }

func (c *caseDoc) facets() []string {
	var out []string
	diag := "diag"
	if !c.renderable() && len(c.goldens["diagnostics.golden"]) > 0 {
		diag = "diag(error)"
	}
	out = append(out, diag)
	if _, ok := c.goldens["render.golden"]; ok {
		out = append(out, "render")
	}
	if _, ok := c.goldens["generated.x.go.golden"]; ok {
		out = append(out, "gen")
	}
	if _, ok := c.goldens["ast.golden"]; ok {
		out = append(out, "ast")
	}
	sort.Strings(out)
	return out
}

var _ = fmt.Sprintf
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/corpus/ -run TestLoadCaseSinglePackage -v`
Expected: PASS.

- [ ] **Step 5: Add a multi-package loader test and fixture, verify pass**

Add to `loader_test.go`:

```go
func TestLoadCaseMultiPackage(t *testing.T) {
	c, err := loadCase("testdata/loadertest/multi.txtar")
	if err != nil {
		t.Fatal(err)
	}
	if !c.multiPkg {
		t.Errorf("multiPkg = false, want true")
	}
	if c.modulePath != "example.com/app" {
		t.Errorf("modulePath = %q, want example.com/app", c.modulePath)
	}
	if _, ok := c.files["ui/button.gsx"]; !ok {
		t.Errorf("missing ui/button.gsx")
	}
}
```

Create `internal/corpus/testdata/loadertest/multi.txtar`:

```
-- go.mod --
module example.com/app

go 1.26.1
-- ui/button.gsx --
package ui

component Button(label string) { <button>{label}</button> }
-- pages/home.gsx --
package pages

import "example.com/app/ui"

component Home() { <ui.Button label="Go"/> }
-- invoke --
pages.Home(pages.HomeProps{})
-- diagnostics.golden --
-- render.golden --
<button>Go</button>
```

Run: `go test ./internal/corpus/ -run TestLoadCase -v`
Expected: PASS (both).

- [ ] **Step 6: Commit**

```bash
git add internal/corpus/loader.go internal/corpus/loader_test.go internal/corpus/testdata/loadertest/
git commit -m "corpus: txtar case loader + facet model"
```

---

### Task 2: Module-path rewriter

**Files:**
- Create: `internal/corpus/rewrite.go`
- Test: `internal/corpus/rewrite_test.go`

**Interfaces:**
- Produces: `func rewriteImportPath(src []byte, oldPath, newPath string) []byte` — replaces any quoted import string `"oldPath"` or `"oldPath/..."` with the `newPath`-prefixed equivalent. Operates on quoted occurrences only (the substring `"oldPath` immediately followed by `"` or `/`). Returns `src` unchanged when `oldPath == ""`.

- [ ] **Step 1: Write the failing test**

```go
package corpus

import "testing"

func TestRewriteImportPath(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{
			name: "exact match",
			in:   `import "example.com/app"`,
			want: `import "corpustest/cases/x"`,
		},
		{
			name: "subpackage",
			in:   `import "example.com/app/ui"`,
			want: `import "corpustest/cases/x/ui"`,
		},
		{
			name: "leaves stdlib and gsx untouched",
			in:   `import ("context"; _ "github.com/gsxhq/gsx")`,
			want: `import ("context"; _ "github.com/gsxhq/gsx")`,
		},
		{
			name: "no false prefix match",
			in:   `import "example.com/application"`,
			want: `import "example.com/application"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(rewriteImportPath([]byte(tc.in), "example.com/app", "corpustest/cases/x"))
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRewriteImportPathEmptyOld(t *testing.T) {
	in := `import "anything"`
	if got := string(rewriteImportPath([]byte(in), "", "corpustest/cases/x")); got != in {
		t.Errorf("got %q, want unchanged", got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/corpus/ -run TestRewriteImportPath -v`
Expected: FAIL with `undefined: rewriteImportPath`.

- [ ] **Step 3: Write minimal implementation**

```go
package corpus

import "bytes"

// rewriteImportPath rewrites quoted import strings equal to oldPath or under
// oldPath+"/" so their prefix becomes newPath. Only quoted occurrences are
// touched: the match must be preceded by a double quote and followed by either
// a double quote (exact) or a slash (subpackage). This avoids rewriting a
// longer sibling path like "example.com/application".
func rewriteImportPath(src []byte, oldPath, newPath string) []byte {
	if oldPath == "" {
		return src
	}
	var out bytes.Buffer
	needle := []byte(`"` + oldPath)
	for {
		i := bytes.Index(src, needle)
		if i < 0 {
			out.Write(src)
			break
		}
		end := i + len(needle)
		// Next byte after the matched path must be `"` or `/` to be a real
		// path boundary (not a longer sibling).
		if end < len(src) && (src[end] == '"' || src[end] == '/') {
			out.Write(src[:i])
			out.WriteByte('"')
			out.WriteString(newPath)
			src = src[end:]
		} else {
			out.Write(src[:end])
			src = src[end:]
		}
	}
	return out.Bytes()
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/corpus/ -run TestRewriteImportPath -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/corpus/rewrite.go internal/corpus/rewrite_test.go
git commit -m "corpus: quoted import-path rewriter for batch module folding"
```

---

### Task 3: HTML structural comparer (move from e2e)

**Files:**
- Create: `internal/corpus/htmlcompare.go`
- Test: `internal/corpus/htmlcompare_test.go`

**Interfaces:**
- Produces: `func htmlStructuralDiff(got, want string) (string, error)` — parses both as HTML and structurally compares, ignoring insignificant inter-element whitespace and attribute order. Returns `("", nil)` when equivalent; a human-readable diff string when not; a non-nil error only when parsing fails.

- [ ] **Step 1: Write the failing test**

```go
package corpus

import "testing"

func TestHTMLStructuralDiff(t *testing.T) {
	if d, err := htmlStructuralDiff("<p>Hi  X</p>", "<p>Hi X</p>"); err != nil || d != "" {
		t.Errorf("collapse-ws: diff=%q err=%v", d, err)
	}
	if d, err := htmlStructuralDiff(`<a id="1" class="x">y</a>`, `<a class="x" id="1">y</a>`); err != nil || d != "" {
		t.Errorf("attr-order: diff=%q err=%v", d, err)
	}
	if d, _ := htmlStructuralDiff("<p>A</p>", "<p>B</p>"); d == "" {
		t.Errorf("expected a diff for differing text")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/corpus/ -run TestHTMLStructuralDiff -v`
Expected: FAIL with `undefined: htmlStructuralDiff`.

- [ ] **Step 3: Write the implementation**

Copy the helper bodies from `internal/codegen/e2e_test.go` (`compareNodes`, `significantChildren`, `attrSet`, `collapseWS`, `nodeLabel`, and the `wsRun` regexp) into `htmlcompare.go` under `package corpus`, and add the entry point:

```go
package corpus

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/net/html"
)

func htmlStructuralDiff(got, want string) (string, error) {
	gotTree, err := html.Parse(strings.NewReader(got))
	if err != nil {
		return "", fmt.Errorf("parse got HTML: %w", err)
	}
	wantTree, err := html.Parse(strings.NewReader(want))
	if err != nil {
		return "", fmt.Errorf("parse want HTML: %w", err)
	}
	return compareNodes(gotTree, wantTree), nil
}

var wsRun = regexp.MustCompile(`\s+`)

// compareNodes, significantChildren, attrSet, collapseWS, nodeLabel:
// copied verbatim from internal/codegen/e2e_test.go (lines 39-110).
```

(Paste the five helpers exactly as they exist in `e2e_test.go:39-110`.)

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/corpus/ -run TestHTMLStructuralDiff -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/corpus/htmlcompare.go internal/corpus/htmlcompare_test.go
git commit -m "corpus: structural HTML comparer (moved from codegen e2e)"
```

---

### Task 4: Codegen core — AST/parser-diag + the single `generate` path

> **Performance contract (why this is split):** `codegen.GeneratePackage` runs
> `go/packages` type-checking and is the dominant per-case cost (~100-300ms),
> not `go run`. The baseline 91 `go run`s each also pay one `GeneratePackage`.
> The win comes from calling `GeneratePackage` **exactly once per case** and one
> shared `go build`. So `GeneratePackage` lives in ONE place — `generate` — used
> by the batch (renderable cases) and the non-renderable facet path. Never call
> it twice for the same case.

**Files:**
- Create: `internal/corpus/codegen.go`
- Test: `internal/corpus/codegen_test.go`

**Interfaces:**
- Consumes: `caseDoc` (Task 1), `rewriteImportPath` (Task 2), `parser.ParseFile`, `ast.Fprint`, `codegen.GeneratePackage`.
- Produces:
  - `func caseModuleDir(tmp string, c *caseDoc) string` — `<tmp>/cases/<c.dir>`.
  - `func caseImportRoot(c *caseDoc) string` — `"corpustest/cases/" + c.dir`.
  - `func mustTempModule(repoRoot string) string` — temp dir + synthesized `go.mod` (module `corpustest`, gsx replace).
  - `func (c *caseDoc) astAndParserDiag() (astDump []byte, parserDiag []byte, single bool)` — parses `input.gsx` (single-package shorthand only); returns the `ast.Fprint` dump and any parser diagnostic text; `single=false` for multi-package cases.
  - `func (c *caseDoc) generate(moduleDir, importRoot string) (genConcat []byte, diag []byte)` — THE single code-generation path. Writes the case's sources under `moduleDir` (rewriting `c.modulePath → importRoot` for multi-package), runs `GeneratePackage` per package dir, writes each generated `.x.go` next to its source, and returns the concatenation of generated source (sorted by gsx path) plus any codegen diagnostics. Caller owns `moduleDir`'s lifecycle.
  - `func (c *caseDoc) packageDirs() []string`, `func packageNameOf(src []byte) string` (helpers).

- [ ] **Step 1: Write the implementation**

```go
package corpus

import (
	"bytes"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/codegen"
	"github.com/gsxhq/gsx/parser"
)

func caseModuleDir(tmp string, c *caseDoc) string { return filepath.Join(tmp, "cases", c.dir) }
func caseImportRoot(c *caseDoc) string            { return "corpustest/cases/" + c.dir }

// mustTempModule creates a temp dir with a go.mod wiring the gsx replace.
func mustTempModule(repoRoot string) string {
	tmp, err := os.MkdirTemp("", "corpuscase")
	if err != nil {
		panic(err)
	}
	gomod := "module corpustest\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => " + repoRoot + "\n"
	if err := os.WriteFile(filepath.Join(tmp, "go.mod"), []byte(gomod), 0o644); err != nil {
		panic(err)
	}
	return tmp
}

// astAndParserDiag parses a single-package case's input.gsx. It returns the AST
// dump and any parser diagnostic text. single=false for multi-package cases
// (no input.gsx shorthand) — those carry no AST facet.
func (c *caseDoc) astAndParserDiag() (astDump []byte, parserDiag []byte, single bool) {
	src, has := c.files["input.gsx"]
	if !has || c.multiPkg {
		return nil, nil, false
	}
	file, perr := parser.ParseFile(token.NewFileSet(), "input.gsx", src, 0)
	var dump, diag bytes.Buffer
	if file != nil {
		ast.Fprint(&dump, file)
	}
	if perr != nil {
		diag.WriteString(perr.Error())
		diag.WriteByte('\n')
	}
	return dump.Bytes(), diag.Bytes(), true
}

// generate is the SINGLE place GeneratePackage is invoked. It writes sources
// (rewriting the module path for multi-package cases), generates each package,
// writes the .x.go next to its source, and returns concatenated generated
// source + codegen diagnostics.
func (c *caseDoc) generate(moduleDir, importRoot string) (genConcat []byte, diag []byte) {
	var d bytes.Buffer
	for name, data := range c.files {
		if name == "go.mod" {
			continue
		}
		if c.multiPkg {
			data = rewriteImportPath(data, c.modulePath, importRoot)
		}
		dst := filepath.Join(moduleDir, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(dst), 0o755)
		os.WriteFile(dst, data, 0o644)
	}
	var parts []string
	for _, dir := range c.packageDirs() {
		pkgDir := filepath.Join(moduleDir, filepath.FromSlash(dir))
		gen, err := codegen.GeneratePackage(pkgDir)
		if err != nil {
			d.WriteString(err.Error())
			d.WriteByte('\n')
			continue
		}
		gsxPaths := make([]string, 0, len(gen))
		for p := range gen {
			gsxPaths = append(gsxPaths, p)
		}
		sort.Strings(gsxPaths)
		for _, p := range gsxPaths {
			out := rewriteImportPath(gen[p], c.modulePath, importRoot) // no-op when modulePath==""
			base := strings.TrimSuffix(filepath.Base(p), ".gsx")
			os.WriteFile(filepath.Join(pkgDir, base+".x.go"), out, 0o644)
			parts = append(parts, string(out))
		}
	}
	return []byte(strings.Join(parts, "")), d.Bytes()
}

// packageDirs returns the distinct directories (relative to module root)
// containing .gsx files, sorted. "." for module-root files.
func (c *caseDoc) packageDirs() []string {
	seen := map[string]bool{}
	for name := range c.files {
		if !strings.HasSuffix(name, ".gsx") {
			continue
		}
		seen[filepath.ToSlash(filepath.Dir(name))] = true
	}
	var out []string
	for d := range seen {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

func packageNameOf(src []byte) string {
	for _, line := range strings.Split(string(src), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "package ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "package "))
		}
	}
	return "views"
}
```

- [ ] **Step 2: Write a unit test exercising AST + generate, run it**

Add to `internal/corpus/codegen_test.go`:

```go
package corpus

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAstAndParserDiagClean(t *testing.T) {
	c, _ := loadCase("testdata/loadertest/single.txtar")
	dump, diag, single := c.astAndParserDiag()
	if !single {
		t.Fatal("single = false, want true")
	}
	if len(diag) != 0 {
		t.Errorf("unexpected parser diag: %s", diag)
	}
	if len(dump) == 0 {
		t.Errorf("expected non-empty AST dump")
	}
}

func TestGenerateSingleClean(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	c, _ := loadCase("testdata/loadertest/single.txtar")
	tmp := mustTempModule(repoRoot)
	defer os.RemoveAll(tmp)
	gen, diag := c.generate(caseModuleDir(tmp, c), caseImportRoot(c))
	if len(diag) != 0 {
		t.Errorf("unexpected codegen diag: %s", diag)
	}
	if len(gen) == 0 {
		t.Errorf("expected non-empty generated output")
	}
}
```

Run: `go test ./internal/corpus/ -run 'TestAstAndParserDiagClean|TestGenerateSingleClean' -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/corpus/codegen.go internal/corpus/codegen_test.go
git commit -m "corpus: codegen core — AST/parser-diag + single generate path"
```

---

### Task 5: Batch assembler + runner

**Files:**
- Create: `internal/corpus/batch.go`
- Test: `internal/corpus/batch_test.go`

**Interfaces:**
- Consumes: `caseDoc` (Task 1), `generate`/`caseModuleDir`/`caseImportRoot`/`mustTempModule`/`packageNameOf` (Task 4), `rewriteImportPath` (Task 2).
- Produces:
  - `type renderResult struct { html string; diagnostics []byte; generated []byte }`
  - `func renderBatch(repoRoot string, cases []*caseDoc) (map[string]*renderResult, error)` — for every `renderable()` case: calls `generate` once (this is where renderable cases get their codegen — NOT in Task 7), records `diagnostics`/`generated`; for cases that generated cleanly, writes an entry wrapper and adds them to one fan-in `main.go`; runs a single `go run .`; splits NUL-delimited output and fills each result's `html`. Returns an empty (non-nil) map when no renderable cases.

**Per-case assembly rules:**
- Module dir = `caseModuleDir(tmp, c)` = `<tmp>/cases/<c.dir>`; import root = `caseImportRoot(c)` = `corpustest/cases/<c.dir>`. `generate` handles source-writing + rewrite + codegen.
- A case whose `generate` returns non-empty diagnostics has no valid `.x.go`, so it is NOT added to the build; its `renderResult` carries the diagnostics and an empty `html` (Task 7 surfaces this as a diagnostics mismatch — a renderable case is expected to compile cleanly).
- Single-package: entry wrapper (`GsxEntryRender`) goes in-package alongside the generated code; `invoke` is bare.
- Multi-package: entry wrapper goes in a `gsxentry` subpackage that imports ONLY the packages the `invoke` references (matched by package name → import path), avoiding Go's unused-import error; `invoke` is package-qualified.
- Entry body uses aliased imports `_gsxctx "context"` and `_gsxio "io"` so it never clashes with user imports: `func GsxEntryRender(ctx _gsxctx.Context, w _gsxio.Writer) error { return (<invoke>).Render(ctx, w) }`.
- `main.go` imports each built case's entry package aliased `case<N>` and emits, per case: `os.Stdout.WriteString("\x00CASE <name>\x00\n")` then `case<N>.GsxEntryRender(ctx, os.Stdout)`.

- [ ] **Step 1: Write the failing test**

```go
package corpus

import (
	"path/filepath"
	"testing"
)

func TestRenderBatchSingleAndMulti(t *testing.T) {
	if testing.Short() {
		t.Skip("skip batch build in -short")
	}
	repoRoot, _ := filepath.Abs("../..")
	single, _ := loadCase("testdata/loadertest/single.txtar")
	multi, _ := loadCase("testdata/loadertest/multi.txtar")
	out, err := renderBatch(repoRoot, []*caseDoc{single, multi})
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := htmlStructuralDiff(out["loadertest/single"].html, "<p>Hi X</p>"); d != "" {
		t.Errorf("single: %s\ngot %q", d, out["loadertest/single"].html)
	}
	if len(out["loadertest/single"].generated) == 0 {
		t.Errorf("single: expected generated bytes recorded")
	}
	if d, _ := htmlStructuralDiff(out["loadertest/multi"].html, "<button>Go</button>"); d != "" {
		t.Errorf("multi: %s\ngot %q", d, out["loadertest/multi"].html)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/corpus/ -run TestRenderBatchSingleAndMulti -v`
Expected: FAIL with `undefined: renderBatch`.

- [ ] **Step 3: Write the implementation**

```go
package corpus

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

type renderResult struct {
	html        string
	diagnostics []byte
	generated   []byte
}

const caseMarkerPrefix = "\x00CASE "
const caseMarkerSuffix = "\x00"

func renderBatch(repoRoot string, cases []*caseDoc) (map[string]*renderResult, error) {
	res := map[string]*renderResult{}
	tmp := mustTempModule(repoRoot)
	defer os.RemoveAll(tmp)

	var imports, dispatch bytes.Buffer
	built := 0
	for _, c := range cases {
		if !c.renderable() {
			continue
		}
		moduleDir := caseModuleDir(tmp, c)
		root := caseImportRoot(c)
		gen, diag := c.generate(moduleDir, root) // the single codegen for renderables
		res[c.name] = &renderResult{diagnostics: diag, generated: gen}
		if len(diag) > 0 {
			continue // codegen failed; not buildable
		}
		entryPkg, err := c.writeEntry(moduleDir, root)
		if err != nil {
			return nil, fmt.Errorf("case %s: %w", c.name, err)
		}
		alias := fmt.Sprintf("case%d", built)
		built++
		fmt.Fprintf(&imports, "\t%s %q\n", alias, entryPkg)
		fmt.Fprintf(&dispatch, "\tos.Stdout.WriteString(%q)\n\t_ = %s.GsxEntryRender(ctx, os.Stdout)\n",
			caseMarkerPrefix+c.name+caseMarkerSuffix+"\n", alias)
	}
	if built == 0 {
		return res, nil
	}

	main := "package main\n\nimport (\n\t\"context\"\n\t\"os\"\n" + imports.String() + ")\n\nfunc main() {\n\tctx := context.Background()\n" + dispatch.String() + "}\n"
	if err := os.WriteFile(filepath.Join(tmp, "main.go"), []byte(main), 0o644); err != nil {
		return nil, err
	}

	cmd := exec.Command("go", "run", ".")
	cmd.Dir = tmp
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("batch go run: %w\n%s", err, stderr.String())
	}
	for name, html := range splitBatchOutput(stdout.String()) {
		if r := res[name]; r != nil {
			r.html = html
		}
	}
	return res, nil
}

// writeEntry writes the GsxEntryRender wrapper (codegen already ran in generate)
// and returns the import path of the package that holds it.
func (c *caseDoc) writeEntry(moduleDir, root string) (string, error) {
	entry := "import (\n\t_gsxctx \"context\"\n\t_gsxio \"io\"\n)\n\nfunc GsxEntryRender(ctx _gsxctx.Context, w _gsxio.Writer) error {\n\treturn (" + string(bytes.TrimSpace(c.invoke)) + ").Render(ctx, w)\n}\n"

	if c.multiPkg {
		entryDir := filepath.Join(moduleDir, "gsxentry")
		if err := os.MkdirAll(entryDir, 0o755); err != nil {
			return "", err
		}
		// Import only packages the invoke references, by package name.
		nameToPath := map[string]string{}
		for _, dir := range c.packageDirs() {
			nameToPath[c.packageNameInDir(dir)] = root + "/" + dir
		}
		var imps bytes.Buffer
		for name := range referencedQualifiers(c.invoke) {
			if p, ok := nameToPath[name]; ok {
				fmt.Fprintf(&imps, "\t%s %q\n", name, p)
			}
		}
		body := "package gsxentry\n\nimport (\n" + imps.String() + ")\n\n" + entry
		if err := os.WriteFile(filepath.Join(entryDir, "entry.go"), []byte(body), 0o644); err != nil {
			return "", err
		}
		return root + "/gsxentry", nil
	}

	pkgName := packageNameOf(c.files["input.gsx"])
	body := "package " + pkgName + "\n\n" + entry
	if err := os.WriteFile(filepath.Join(moduleDir, "gsxentry.go"), []byte(body), 0o644); err != nil {
		return "", err
	}
	return root, nil
}

// packageNameInDir returns the package clause of the first .gsx file in dir.
func (c *caseDoc) packageNameInDir(dir string) string {
	for name, data := range c.files {
		if strings.HasSuffix(name, ".gsx") && filepath.ToSlash(filepath.Dir(name)) == dir {
			return packageNameOf(data)
		}
	}
	return "views"
}

var qualifierRe = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\.`)

// referencedQualifiers returns the set of identifiers used as `ident.` in src
// (a superset of package qualifiers; non-package matches are harmless because
// they won't match a known package name).
func referencedQualifiers(src []byte) map[string]bool {
	out := map[string]bool{}
	for _, m := range qualifierRe.FindAllSubmatch(src, -1) {
		out[string(m[1])] = true
	}
	return out
}

func splitBatchOutput(out string) map[string]string {
	res := map[string]string{}
	for _, p := range strings.Split(out, caseMarkerPrefix) {
		end := strings.Index(p, caseMarkerSuffix)
		if end < 0 {
			continue
		}
		res[p[:end]] = strings.TrimPrefix(p[end+len(caseMarkerSuffix):], "\n")
	}
	return res
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/corpus/ -run TestRenderBatchSingleAndMulti -v`
Expected: PASS (both single and multi render correctly).

- [ ] **Step 5: Commit**

```bash
git add internal/corpus/batch.go internal/corpus/batch_test.go
git commit -m "corpus: batch assembler — single generate, one build/run for all cases"
```

---

### Task 6: Coverage index

**Files:**
- Create: `internal/corpus/coverage.go`
- Test: `internal/corpus/coverage_test.go`

**Interfaces:**
- Consumes: `caseDoc.name`, `caseDoc.facets()`.
- Produces: `func coverageReport(cases []*caseDoc) []byte` — cases sorted by name, one line `"<name>\t<facets space-joined>\n"`, followed by `"TOTAL: <n> cases (render: <r>, error: <e>, gen-pinned: <g>)\n"`.

- [ ] **Step 1: Write the failing test**

```go
package corpus

import "testing"

func TestCoverageReport(t *testing.T) {
	cases := []*caseDoc{
		{name: "b/two", goldens: map[string][]byte{"diagnostics.golden": {}, "render.golden": []byte("x")}, invoke: []byte("X()")},
		{name: "a/one", goldens: map[string][]byte{"diagnostics.golden": []byte("err")}},
	}
	want := "a/one\tdiag(error)\nb/two\tdiag render\nTOTAL: 2 cases (render: 1, error: 1, gen-pinned: 0)\n"
	if got := string(coverageReport(cases)); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/corpus/ -run TestCoverageReport -v`
Expected: FAIL with `undefined: coverageReport`.

- [ ] **Step 3: Write minimal implementation**

```go
package corpus

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
)

func coverageReport(cases []*caseDoc) []byte {
	sorted := append([]*caseDoc(nil), cases...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].name < sorted[j].name })
	var buf bytes.Buffer
	var render, errc, gen int
	for _, c := range sorted {
		f := c.facets()
		fmt.Fprintf(&buf, "%s\t%s\n", c.name, strings.Join(f, " "))
		for _, tag := range f {
			switch tag {
			case "render":
				render++
			case "diag(error)":
				errc++
			case "gen":
				gen++
			}
		}
	}
	fmt.Fprintf(&buf, "TOTAL: %d cases (render: %d, error: %d, gen-pinned: %d)\n", len(sorted), render, errc, gen)
	return buf.Bytes()
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/corpus/ -run TestCoverageReport -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/corpus/coverage.go internal/corpus/coverage_test.go
git commit -m "corpus: coverage index report"
```

---

### Task 7: TestCorpus orchestrator (replaces TestPipeline)

**Files:**
- Modify: `internal/corpus/corpus_test.go`

**Interfaces:**
- Consumes: `loadCase`, `caseDoc.astAndParserDiag`, `caseDoc.generate`, `caseModuleDir`/`caseImportRoot`/`mustTempModule` (Task 4), `renderBatch`/`renderResult` (Task 5), `htmlStructuralDiff` (Task 3), `coverageReport` (Task 6), and existing `setSection`/`writeArchive`/`update`.

**Behavior (single-generate — `GeneratePackage` runs at most once per case):**
- Glob `testdata/cases/**/*.txtar` (recursive). Load each.
- Run `renderBatch` once; renderable cases get their codegen there (and their `diagnostics`/`generated`/`html`).
- Per case, compute the diagnostics+generated facets WITHOUT re-generating renderables:
  - **parser-error case** (single-package, `astAndParserDiag` returns non-empty diag): diagnostics = parser diag; no codegen. (Preserves the existing parser cases exactly.)
  - **renderable case**: diagnostics/generated come from the batch result.
  - **non-renderable, parses clean** (codegen-error cases, or compiles-clean-but-not-rendered): one `generate` into a throwaway temp module.
- Compare/`-update` each facet: `ast.golden` (if single), `diagnostics.golden` (always; absent ⇒ expect empty), `generated.x.go.golden` (if present).
- For renderable cases: enforce the safety rule (no diagnostics ⇒ `render.golden` must exist) and compare/`-update` `render.golden` via `htmlStructuralDiff`.
- Write/compare `testdata/coverage.golden` via `coverageReport`.

- [ ] **Step 1: Replace `TestPipeline` with `TestCorpus`**

```go
func TestCorpus(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	var files []string
	filepath.WalkDir("testdata/cases", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".txtar") {
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)
	if len(files) == 0 {
		t.Fatal("no testdata/cases/**/*.txtar")
	}

	var cases []*caseDoc
	paths := map[string]string{} // case name -> txtar path
	for _, p := range files {
		c, err := loadCase(p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		cases = append(cases, c)
		paths[c.name] = p
	}

	// Single batch render (the only place renderable cases are generated).
	batch, err := renderBatch(repoRoot, cases)
	if err != nil {
		t.Fatalf("renderBatch: %v", err)
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			astDump, parserDiag, single := c.astAndParserDiag()

			// Resolve diagnostics + generated facets without re-generating renderables.
			var diagGot, genGot []byte
			switch {
			case single && len(parserDiag) > 0:
				diagGot = parserDiag // parser-error case; no codegen
			case c.renderable():
				if r := batch[c.name]; r != nil {
					diagGot, genGot = r.diagnostics, r.generated
				}
			default:
				tmp := mustTempModule(repoRoot)
				genGot, diagGot = c.generate(caseModuleDir(tmp, c), caseImportRoot(c))
				os.RemoveAll(tmp)
			}

			if single {
				checkOrUpdateFacet(t, c, "ast.golden", astDump, paths[c.name])
			}
			checkOrUpdateFacet(t, c, "diagnostics.golden", diagGot, paths[c.name])
			checkOrUpdateFacet(t, c, "generated.x.go.golden", genGot, paths[c.name])

			if c.renderable() {
				if len(diagGot) == 0 {
					if _, ok := c.goldens["render.golden"]; !ok && !*update {
						t.Fatalf("renderable case has no render.golden (run -update)")
					}
				}
				gotHTML := ""
				if r := batch[c.name]; r != nil {
					gotHTML = r.html
				}
				if *update {
					setSection(c.archive, "render.golden", []byte(gotHTML))
					writeArchive(t, paths[c.name], c.archive)
				} else {
					diff, derr := htmlStructuralDiff(gotHTML, string(c.goldens["render.golden"]))
					if derr != nil {
						t.Fatal(derr)
					}
					if diff != "" {
						t.Errorf("%s: render mismatch (%s)\n--- got ---\n%s\n--- want ---\n%s",
							c.name, diff, gotHTML, c.goldens["render.golden"])
					}
				}
			}
		})
	}

	checkOrUpdateCoverage(t, cases)
}
```

> **Note on `-update` ordering:** the render branch and `checkOrUpdateFacet`
> both call `setSection`+`writeArchive` on the same archive. That is safe
> because each `setSection` mutates the in-memory archive and re-serializes the
> whole file; the last write wins and contains every prior section. Keep the
> render `setSection` AFTER the facet ones (as above) so a regenerated archive
> carries both updated facets and render output.

Add helpers in `corpus_test.go`:

```go
// checkOrUpdateFacet compares one computed facet to its golden section. The
// ast/generated sections are only enforced when present in the archive;
// diagnostics is always enforced (absent ⇒ expect empty).
func checkOrUpdateFacet(t *testing.T, c *caseDoc, sec string, got []byte, path string) {
	t.Helper()
	_, present := c.goldens[sec]
	if sec != "diagnostics.golden" && !present {
		return // optional facet not pinned
	}
	if *update {
		// Only (re)write the section if it already exists, or for diagnostics
		// when there is something to record, to avoid spurious empty sections.
		if present || sec == "diagnostics.golden" {
			setSection(c.archive, sec, got)
			writeArchive(t, path, c.archive)
		}
		return
	}
	if !bytes.Equal(got, c.goldens[sec]) {
		t.Errorf("%s: %s mismatch\n--- got ---\n%s\n--- want ---\n%s", c.name, sec, got, c.goldens[sec])
	}
}

func checkOrUpdateCoverage(t *testing.T, cases []*caseDoc) {
	t.Helper()
	got := coverageReport(cases)
	const golden = "testdata/coverage.golden"
	if *update {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read coverage golden (run -update): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("coverage changed (run -update):\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}
```

Update imports in `corpus_test.go` (add `io/fs`, `sort`, `strings`; keep `bytes`, `flag`, `os`, `path/filepath`, `testing`). Remove the now-unused `go/token`, `ast`, `parser`, `txtar` direct imports if the old `TestPipeline` body is deleted (the loader/codegen files own those now).

- [ ] **Step 2: Delete the old `TestPipeline` function and its now-redundant `checkSection`** (the `setSection`/`writeArchive` helpers stay; `checkSection` is replaced by `checkOrUpdateFacet`).

- [ ] **Step 3: Move the existing parser cases into the new tree**

```bash
mkdir -p internal/corpus/testdata/cases/parser
git mv internal/corpus/testdata/pipeline/*.txtar internal/corpus/testdata/cases/parser/
rmdir internal/corpus/testdata/pipeline
```

- [ ] **Step 4: Regenerate goldens and verify**

Run: `go test ./internal/corpus/ -run TestCorpus -update`
Then: `go test ./internal/corpus/ -run TestCorpus -v`
Expected: PASS. Inspect `git diff testdata/cases/parser/` — the existing `ast.golden`/`diagnostics.golden` sections must be unchanged (only `coverage.golden` is newly created).

- [ ] **Step 5: Commit**

```bash
git add internal/corpus/corpus_test.go internal/corpus/testdata/
git commit -m "corpus: TestCorpus orchestrator + migrate parser cases + coverage.golden"
```

---

## Phase 1+ — Migrate the codegen e2e suite

Each migration task converts one feature area's `e2e_test.go` functions into txtar cases and deletes the Go funcs. They share this **recipe** (spelled out once; every area task applies it):

> **Migration recipe (per e2e test function):**
> 1. Identify the test's `files map[string]string` and its `invocation` (the `renderPackage`/`generatePackageErr` argument).
> 2. Create `internal/corpus/testdata/cases/<area>/<scenario>.txtar`:
>    - One `-- <filename> --` section per entry in `files` (e.g. `input.gsx` for `views.gsx`; keep sibling `.go` filenames like `model.go`). For multi-package tests, use subdirectory paths and add a `-- go.mod --`.
>    - For render tests: `-- invoke --` with the invocation **stripped of the `p.` alias** (e.g. `p.Profile(p.ProfileProps{...})` → `Profile(ProfileProps{...})`).
>    - For `generatePackageErr` tests: **no** `invoke` section; the codegen error is captured into `diagnostics.golden`.
>    - Leave goldens empty; they are filled by `-update`.
> 3. Run `go test ./internal/corpus/ -run 'TestCorpus/<area>' -update`, then inspect the generated `render.golden` / `diagnostics.golden` and confirm it matches the old test's asserted HTML / expected error substring.
> 4. Delete the migrated Go test function(s) from `e2e_test.go`.
> 5. Run `go test ./internal/corpus/ ./internal/codegen/` — both green.
> 6. Confirm parity: number of deleted Go funcs == number of new `.txtar` files this task.
> 7. Commit.

**Worked example — `TestRenderFieldAccess` → `cases/interpolation/field_access.txtar`:**

```
-- model.go --
package views

type User struct {
	Name string
	Age  int
}
-- input.gsx --
package views

component Profile(user User) {
	<p>{user.Name} is {user.Age}</p>
}
-- invoke --
Profile(ProfileProps{User: User{Name: "Alice", Age: 30}})
-- diagnostics.golden --
-- render.golden --
<p>Alice is 30</p>
```

(`render.golden` is produced by `-update`; the example shows the expected result so the implementer can verify against the old `assertHTMLEqual(t, got, "<p>Alice is 30</p>")`.)

The area tasks below list the `e2e_test.go` functions to convert (grouped by the `cases/` directory they target). Each is one task ending in a commit.

- [ ] **Task 8 — elements/interpolation/fragment.** Convert: `TestRenderFieldAccess`, `TestRenderInterpTypes`, `TestRenderMixedChunk`, `TestRenderStaticAndBoolAttrs`, `TestRenderFragment`, `TestRenderNodeSlice`, `TestProbeAcceptsMultiValueExpr`, `TestRenderNamedScalarTypes`. Target dirs: `cases/elements/`, `cases/interpolation/`. Apply recipe; commit `corpus: migrate elements/interpolation cases`.

- [ ] **Task 9 — attrs + conditional attrs.** Convert: `TestRenderExprAttrs`, `TestRenderExprAttrURLBlocked`, `TestRenderExprAttrJSRejected`, `TestRenderExprAttrXSS`, `TestRenderCondAttrBool`, `TestRenderCondAttrElseTypedExprs`, `TestRenderCondAttrElseIf`, `TestRenderCondAttrInterleaved`, `TestRenderCondAttrParamOnlyInBranch`. Target: `cases/attrs/`, `cases/security/`. Commit `corpus: migrate attrs + cond-attr cases`.

- [ ] **Task 10 — class + spread.** Convert: `TestRenderComposableClass`, `TestRenderComposableClassEscaping`, `TestRenderElementSpread`, `TestRenderComposableStyleRejected`. Target: `cases/class/`. Commit `corpus: migrate class/spread cases`.

- [ ] **Task 11 — pipelines (interp + attr).** Convert: `TestRenderPipelineBare`, `TestRenderPipelineChain`, `TestRenderPipelineParam`, `TestRenderPipelineJoin`, `TestRenderPipelineParamArg`, `TestRenderPipelineLoopVar`, `TestPipelineUnknownFilter`, `TestPipelineArityMismatch`, `TestPipelineTryRejected`, `TestRenderTryUnwrap`, `TestRenderAttrPipelinePlain`, `TestRenderAttrPipelineEscaped`, `TestRenderAttrPipelineURL`, `TestRenderAttrPipelineURLOK`, `TestAttrPipelineJSRejected`, `TestAttrPipelineUnknownFilter`, `TestAttrPipelineTryStageRejected`. Target: `cases/pipelines/`. Commit `corpus: migrate pipeline cases`.

- [ ] **Task 12 — control flow.** Convert: `TestRenderIf`, `TestRenderSwitch`, `TestRenderForLoop`, `TestRenderGoBlock`. Target: `cases/control_flow/`. Commit `corpus: migrate control-flow cases`.

- [ ] **Task 13 — child components + slots.** Convert: `TestRenderChildComponentProps`, `TestRenderChildComponentPropsFeaturedFalse`, `TestRenderChildComponentStaticProp`, `TestRenderChildComponentNoProps`, `TestChildComponentPropPipelineErrors`, `TestChildComponentPropTryErrors`, `TestChildComponentHyphenAttrFallsThrough`, `TestChildComponentSpreadErrors`, `TestChildComponentClassAttrErrors`, `TestRenderChildrenSlot`, `TestRenderChildrenSlotEmpty`, `TestRenderChildrenSlotBinding`, `TestRenderChildrenSlotInterleaved`, `TestRenderNamedSlot`, `TestRenderNamedSlotWithChildren`, `TestRenderNamedSlotBinding`, `TestRenderNamedSlotInterleaved`, `TestRenderMultipleNamedSlots`, `TestRenderNodeParamStandalone`, `TestRenderNamedSlotBadName`. Target: `cases/components/`. Commit `corpus: migrate child-component + slot cases`.

- [ ] **Task 14 — methods.** Convert: `TestMethodOnlyFileGenerates`, `TestRenderMethodNullary`, `TestRenderMethodWithParam`, `TestRenderMethodPointerReceiver`, `TestMethodUnnamedReceiverError`, `TestMethodReservedRecvVarCtx`, `TestRenderMethodInvocationChain`, `TestRenderMethodSameNameDifferentReceivers`, `TestRenderMethodInvocationNullary`, `TestRenderMethodInvocationParam`, `TestRenderMethodInvocationLoopVarBinding`, `TestRenderMethodAndFunctionMixed`, `TestRenderMethodInvocationSlotInterleaved`. Target: `cases/methods/`. Commit `corpus: migrate method-component cases`.

- [ ] **Task 15 — fallthrough.** Convert: `TestFallthroughComposedClassMerge`, `TestFallthroughStaticClassMerge`, `TestFallthroughNoClassRootWithBag`, `TestFallthroughNoClassEmptyBag`, `TestFallthroughRootWins`, `TestFallthroughEmptyBagNoop`, `TestFallthroughNotEligibleNoField`, `TestReservedParamAttrs`, `TestCallSiteFallthroughButton`, `TestCallSiteDeclaredVsUndeclaredIdentifier`, `TestCallSiteNonIdentifierFallthrough`, `TestCallSiteClassMerge`, `TestCallSiteRootWins`, `TestCallSiteFallthroughNotEligible`, `TestCallSiteNoFallthroughUnchanged`, `TestCallSiteMethodFallthrough`, `TestManualFallthroughPlacement`, `TestManualFallthroughDisablesAuto`, `TestManualFallthroughWithout`, `TestManualFallthroughNullaryMethod`, `TestManualAutoStillWorks`. Target: `cases/fallthrough/`. Commit `corpus: migrate fallthrough cases`.

- [ ] **Task 16 — diagnostics (reserved params, collisions) + cross-package.** Convert: `TestRenderParamNameCollision`, `TestReservedParamCtx`, `TestRenderCtxInInterp`, `TestReservedParamGsxPrefix`, `TestReservedParamEmittedImport`, `TestRenderPointerNode`, `TestValueNodePointerReceiverCleanError`, `TestInterleavedImportsCleanError`, `TestReservedParamChildren`, `TestRenderCrossFileAndComponent`, `TestRenderMultiGsxPackage`, `TestRenderRealGsxsharedFile`. Target: `cases/diagnostics/`, `cases/xpkg/`. **For `TestRenderMultiGsxPackage` / cross-file cases, use the multi-package txtar shape** (subdirectory packages + `go.mod`) to exercise the rewrite path. Commit `corpus: migrate diagnostics + cross-package cases`.

- [ ] **Task 17 — curate `generated.x.go.golden` subset + example end-to-end.** For these cases, add a `-- generated.x.go.golden --` section (filled via `-update`) because the lowering shape is the thing under test: `cases/codegen-shape/fallthrough_merge` (from `TestFallthroughComposedClassMerge`'s sibling already migrated — add the gen facet here), `cases/codegen-shape/greeting` (port from `internal/codegen/codegen_test.go:TestGenerateSource`, replacing `testdata/greeting.x.go.golden`). Also migrate `TestExample12EndToEnd` → `cases/codegen-shape/example12`. Run `-update`, verify the gen goldens are sensible Go. Commit `corpus: curate generated-Go shape subset`.

- [ ] **Task 18 — retire the monolith and stragglers.**
  - Confirm `e2e_test.go` contains only the helper funcs now living in `corpus` (`assertHTMLEqual`, `compareNodes`, …, `renderPackage`, `generatePackageErr`) and no remaining `Test*` functions except any genuinely-meta ones.
  - Keep as Go tests (do NOT migrate): `TestLineDirectives` (asserts `//line` substrings — a source-shape meta assertion) and `TestRenderEndToEnd` may be deleted as redundant once `cases/codegen-shape/greeting` covers it.
  - Delete `e2e_test.go` and the now-orphaned `testdata/greeting.x.go.golden`.
  - Move `TestLineDirectives` into `codegen_test.go` if not already there (it is).
  - Run: `go test ./...` (full suite) and capture timing of `./internal/corpus/`.
  - Commit `corpus: delete e2e_test.go monolith; suite on one fixture spine`.

- [ ] **Task 19 — final verification.**
  - Run: `go test ./... -count=1` → all green.
  - Run: `gopls check -severity=hint internal/corpus/*.go` → no unused-symbol findings.
  - Compare suite timing against the ~49s baseline; record the new corpus timing in the commit message.
  - Verify `coverage.golden` TOTAL count ≈ the original 115 e2e funcs minus intentionally-merged/dropped ones; note any deltas.
  - Commit any golden refreshes; `corpus: final verification + timing`.

---

## Notes / Deferred (from spec §7)

- **Synthetic module path** for single-package shorthand: resolved as `corpustest` (module) with case import root `corpustest/cases/<dir>`; no per-case `go.mod` needed.
- **`examples_coverage.golden`** stays as the separate parser-level tracker; not folded in.
- **Multi-module** fixtures, third-party diff libraries: out of scope.
- **Batch build failure localization**: `renderBatch` wraps the failing `go run`'s stderr, which names the offending subpackage dir under `cases/<dir>/` — directly mapping back to a case. Per-case fallback build is a future add only if needed.
