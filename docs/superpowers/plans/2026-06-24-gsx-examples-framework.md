# gsx Examples Framework Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a single-source, CI-checked examples gallery: one `examples/*.txtar` per example feeds the docs page, the playground presets, and a render test — and add Go-Playground-style multi-file support to the playground.

**Architecture:** Each example is a txtar at the gsx repo root carrying a `-- doc --` metadata block, one or more `package views` `.gsx` files, an `-- invoke --`, and a `-- render.golden --`. A dedicated test reuses the existing corpus batch harness to compile + `go run` + assert each golden. A generator (`internal/examplegen` + `cmd/gsx-examples`) emits `docs/guide/examples.md`, `docs/examples.json` (frontend presets), and `playground/server/examples.json` (backend cache-seed). The render server splits a txtar-format source into multiple files; the site syncs the presets JSON and the Vue editor edits the multi-file source inline.

**Tech Stack:** Go 1.26.1 (`internal/txtar`, `internal/corpus`, `go/types` resolver), VitePress + Vue 3 + CodeMirror 6 (site), stdlib-only playground server.

## Global Constraints

- Multi-file examples use **one package, `views`**; file names are flat (no `/`). Cross-package examples are excluded. (spec §Decisions)
- Every example has **exactly one** `-- invoke --` (one render entry point). (spec §Decisions)
- The playground source string for an example is: single file → the file bytes verbatim; multiple files → files joined in **Go-Playground txtar format** (`-- name.gsx --\n<bytes>`), files sorted by name. (spec §Components.4)
- `#try=` payload = `base64.StdEncoding.EncodeToString(json)` where `json` is a struct with tags `s` (playground source) and `i` (invoke), marshaled by `encoding/json` → `{"s":…,"i":…}`. This must round-trip through the Vue decoder (`atob` over UTF-8 → `JSON.parse` → `o.s`/`o.i`). (spec §Components.4)
- Generated artifacts (`examples.md`, both `examples.json`) are **committed**. (spec §Components.4)
- Generator failures (missing `doc`/source/`invoke`, a source file outside `package views`, a name with `/`) are **hard errors** (non-zero exit, names the file). (spec §Error handling)
- The render server must keep the **single-file path unchanged** (no `-- file --` separators ⇒ one `comp.gsx`, exactly as today). (spec §Components.7)
- Do not write "simple heuristics" — real implementations only (user CLAUDE.md). Prefer unexported Go identifiers unless serialization needs export.
- After every Go edit, verify with `go build ./...` / `go vet ./...` (gopls diagnostics in this repo are frequently stale; trust the compiler).

---

## File Structure

**gsx repo** (this worktree):
- `examples/NN-slug.txtar` — the 19 single-source fixtures (Create).
- `internal/corpus/loader.go` — add `doc` section handling + `examples/` name derivation (Modify).
- `internal/corpus/docmeta.go` — `docMeta` type + `parseDocMeta` (Create).
- `internal/corpus/examples_test.go` — the dedicated render test (Create).
- `internal/examplegen/examplegen.go` — fixture parsing, source-join, `#try=` payload, JSON + Markdown emit (Create).
- `internal/examplegen/examplegen_test.go` — unit + golden tests (Create).
- `internal/examplegen/testdata/` — hermetic generator fixtures (Create).
- `cmd/gsx-examples/main.go` — CLI wrapper (Create).
- `playground/server/render.go` — multi-file `splitSources` + `readGenerated` concat (Modify).
- `playground/server/render_test.go` — multi-file render test (Create).
- `playground/server/presets.go` — embed `examples.json` for the cache-seed (Modify).
- `playground/server/examples.json` — generated backend presets (Create, generated).
- `docs/guide/examples.md`, `docs/examples.json` — generated artifacts (Create, generated).
- `Makefile` — add `examples` target (Modify).

**site repo** (`/Users/jackieli/personal/gsxhq/gsxhq.github.io`):
- `scripts/sync-docs.mjs` — also copy `docs/examples.json` → theme (Modify).
- `.vitepress/theme/presets.generated.json` — committed generated presets (Create).
- `.vitepress/theme/GsxPlayground.vue` — import presets; multi-file separator decoration (Modify).
- `.vitepress/config.mts` — fix "Examples" nav + sidebar (Modify).

---

## Task 1: Loader — `doc` section, example names, `parseDocMeta`

**Files:**
- Modify: `internal/corpus/loader.go` (the `caseDoc` struct ~line 12; `loadCase` name derivation ~line 45; the section switch ~line 56)
- Create: `internal/corpus/docmeta.go`
- Test: `internal/corpus/docmeta_test.go` (Create), `internal/corpus/loader_test.go` (Modify)

**Interfaces:**
- Produces: `caseDoc.doc []byte`; `type docMeta struct { Name, Summary, Category string; Order int }`; `func parseDocMeta(b []byte) docMeta`.

- [ ] **Step 1: Write the failing test for `parseDocMeta`**

Create `internal/corpus/docmeta_test.go`:

```go
package corpus

import "testing"

func TestParseDocMeta(t *testing.T) {
	in := []byte("name: Control flow\nsummary: if / else.\ncategory: Control flow\norder: 40\n")
	got := parseDocMeta(in)
	if got.Name != "Control flow" || got.Summary != "if / else." ||
		got.Category != "Control flow" || got.Order != 40 {
		t.Fatalf("parseDocMeta = %+v", got)
	}
}

func TestParseDocMetaDefaults(t *testing.T) {
	got := parseDocMeta([]byte("name: X\nunknown: y\n"))
	if got.Name != "X" || got.Order != 0 || got.Summary != "" {
		t.Fatalf("defaults wrong: %+v", got)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/corpus/ -run TestParseDocMeta`
Expected: FAIL (`undefined: parseDocMeta`).

- [ ] **Step 3: Implement `docMeta` + `parseDocMeta`**

Create `internal/corpus/docmeta.go`:

```go
package corpus

import (
	"strconv"
	"strings"
)

// docMeta is the parsed `-- doc --` block of an example fixture: the human
// metadata that drives the docs page and the preset list.
type docMeta struct {
	Name     string
	Summary  string
	Category string
	Order    int
}

// parseDocMeta parses a `-- doc --` body of `key: value` lines. Unknown keys
// are ignored; a missing or unparseable `order` is 0.
func parseDocMeta(b []byte) docMeta {
	var m docMeta
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch key {
		case "name":
			m.Name = val
		case "summary":
			m.Summary = val
		case "category":
			m.Category = val
		case "order":
			if n, err := strconv.Atoi(val); err == nil {
				m.Order = n
			}
		}
	}
	return m
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/corpus/ -run TestParseDocMeta`
Expected: PASS.

- [ ] **Step 5: Add `doc` field + section handling + `examples/` name derivation**

In `internal/corpus/loader.go`, add `doc []byte` to the `caseDoc` struct (after `invoke []byte`):

```go
	invoke     []byte
	doc        []byte
```

In the `loadCase` name-derivation block, handle an `examples/` path (add after the existing `testdata/` block, before `rel = strings.TrimPrefix(rel, "cases/")`):

```go
	if i := strings.Index(rel, "testdata/"); i >= 0 {
		rel = rel[i+len("testdata/"):]
	} else if i := strings.Index(rel, "examples/"); i >= 0 {
		rel = rel[i+len("examples/"):]
	}
```

In the section `switch` inside `loadCase`, add a `doc` case **before** the `default`:

```go
		case f.Name == "doc":
			c.doc = f.Data
```

- [ ] **Step 6: Write a test that a `doc` section is metadata, not a file**

Add to `internal/corpus/loader_test.go`:

```go
func TestLoadCaseDocSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ex.txtar")
	src := "-- doc --\nname: Demo\norder: 5\n-- input.gsx --\npackage views\n\ncomponent A() { <p>x</p> }\n-- invoke --\nA(AProps{})\n-- render.golden --\n<p>x</p>\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := loadCase(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, isFile := c.files["doc"]; isFile {
		t.Fatal("doc must not be a source file")
	}
	if m := parseDocMeta(c.doc); m.Name != "Demo" || m.Order != 5 {
		t.Fatalf("doc meta = %+v", m)
	}
}
```

Ensure `loader_test.go` imports `os`, `path/filepath`, `testing` (add any missing).

- [ ] **Step 7: Run the corpus tests**

Run: `go test ./internal/corpus/ -count=1`
Expected: PASS (existing cases unaffected — none have a `doc` section).

- [ ] **Step 8: Commit**

```bash
git add internal/corpus/loader.go internal/corpus/docmeta.go internal/corpus/docmeta_test.go internal/corpus/loader_test.go
git commit -m "corpus: parse -- doc -- metadata section + examples/ name derivation"
```

---

## Task 2: Examples render test + migrate 5 fixtures + remove old playground cases

**Files:**
- Create: `examples/10-interpolation.txtar`, `examples/30-auto-escaping.txtar`, `examples/40-if-else.txtar`, `examples/80-children.txtar`, `examples/130-composable-class.txtar`
- Create: `internal/corpus/examples_test.go`
- Delete: `internal/corpus/testdata/cases/playground/*.txtar` (5 files)
- Modify: `internal/corpus/coverage.golden` (regenerated via `-update`)

**Interfaces:**
- Consumes: `loadCase`, `batchCodegen`, `htmlStructuralDiff`, `setSection`, `writeArchive`, `caseDoc.renderable()` (Task 1 / existing corpus).
- Produces: `TestExamples` (the CI gate for `examples/*.txtar`).

- [ ] **Step 1: Create the five migrated fixtures**

`examples/10-interpolation.txtar`:

```
-- doc --
name: Interpolation & props
summary: Components take a typed props struct; {expr} interpolates Go values, HTML-escaped.
category: Basics
order: 10
-- input.gsx --
package views

component Greeting(name string, count int) {
	<p>Hello, {name}! You have {count} messages.</p>
}
-- invoke --
Greeting(GreetingProps{Name: "World", Count: 3})
-- render.golden --
```

`examples/30-auto-escaping.txtar`:

```
-- doc --
name: Auto-escaping & safe raw
summary: User input is HTML-escaped by construction — no XSS. Use gsx.Raw / gsx.RawURL to opt out deliberately.
category: Basics
order: 30
-- input.gsx --
package views

// User input is HTML-escaped by construction — no XSS.
component Comment(body string) {
	<blockquote>{body}</blockquote>
}
-- invoke --
Comment(CommentProps{Body: "<img src=x onerror=alert(1)>"})
-- render.golden --
```

`examples/40-if-else.txtar`:

```
-- doc --
name: If / else
summary: Brace { if … else … } blocks contribute markup conditionally.
category: Control flow
order: 40
-- input.gsx --
package views

component Inbox(name string, count int) {
	<section>
		<h1>Hi {name}</h1>
		{ if count > 0 {
			<p class="badge">{count} new</p>
		} else {
			<p>all caught up</p>
		} }
	</section>
}
-- invoke --
Inbox(InboxProps{Name: "World", Count: 2})
-- render.golden --
```

`examples/80-children.txtar`:

```
-- doc --
name: Children
summary: A component renders its nested markup via {children} (like JSX children / a Vue default slot).
category: Components & composition
order: 80
-- input.gsx --
package views

component Card(title string) {
	<article class="card">
		<h3>{title}</h3>
		<div class="card__body">{children}</div>
	</article>
}

component Page() {
	<Card title="Hello"><em>composed</em></Card>
}
-- invoke --
Page(PageProps{})
-- render.golden --
```

`examples/130-composable-class.txtar`:

```
-- doc --
name: Composable class
summary: The class attribute takes "always" entries and "name": cond toggles (like clsx / Vue :class).
category: Styling
order: 130
-- input.gsx --
package views

component Tag(label string, active bool) {
	<span class={ "tag", "tag--active": active }>
		{label}
	</span>
}
-- invoke --
Tag(TagProps{Label: "stable", Active: true})
-- render.golden --
```

- [ ] **Step 2: Write the examples render test**

Create `internal/corpus/examples_test.go`:

```go
package corpus

import (
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestExamples compiles and renders every examples/*.txtar through the real
// pipeline (codegen + go run) and asserts its render.golden — the same harness
// TestCorpus uses. Run with -update to (re)generate the render.golden sections.
func TestExamples(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	examplesDir := filepath.Join(repoRoot, "examples")

	var files []string
	filepath.WalkDir(examplesDir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".txtar") {
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)
	if len(files) == 0 {
		t.Fatal("no examples/*.txtar found")
	}

	var cases []*caseDoc
	paths := map[string]string{}
	for _, p := range files {
		c, err := loadCase(p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if !c.renderable() {
			t.Fatalf("%s: example has no -- invoke --", c.name)
		}
		cases = append(cases, c)
		paths[c.name] = p
	}

	cg, err := batchCodegen(repoRoot, cases)
	if err != nil {
		t.Fatalf("batchCodegen: %v", err)
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			r := cg[c.name]
			if r == nil {
				t.Fatalf("no codegen result")
			}
			if len(r.diag) > 0 {
				t.Fatalf("example produced diagnostics (examples must be clean):\n%s", r.diag)
			}
			if *update {
				setSection(c.archive, "render.golden", []byte(r.html))
				writeArchive(t, paths[c.name], c.archive)
				return
			}
			diff, derr := htmlStructuralDiff(r.html, string(c.goldens["render.golden"]))
			if derr != nil {
				t.Fatal(derr)
			}
			if diff != "" {
				t.Errorf("render mismatch (%s)\n--- got ---\n%s\n--- want ---\n%s",
					diff, r.html, c.goldens["render.golden"])
			}
		})
	}
}
```

- [ ] **Step 3: Generate the goldens and verify they are correct**

Run: `go test ./internal/corpus/ -run TestExamples -update`
Then run: `go test ./internal/corpus/ -run TestExamples -count=1`
Expected: PASS. **Open each of the 5 `examples/*.txtar` and read the generated `render.golden`** — confirm the HTML is what the example should produce (e.g. `30-auto-escaping` renders `<blockquote>&lt;img src=x onerror=alert(1)&gt;</blockquote>`; `130-composable-class` includes `class="tag tag--active"`). A wrong-but-stable golden is a silent bug.

- [ ] **Step 4: Remove the superseded playground corpus cases**

```bash
git rm internal/corpus/testdata/cases/playground/auto_escaping.txtar \
       internal/corpus/testdata/cases/playground/composable_class.txtar \
       internal/corpus/testdata/cases/playground/composition.txtar \
       internal/corpus/testdata/cases/playground/control_flow.txtar \
       internal/corpus/testdata/cases/playground/interpolation.txtar
```

- [ ] **Step 5: Regenerate the corpus coverage golden and run the full suite**

Run: `go test ./internal/corpus/ -update -count=1` (updates `coverage.golden` after the playground-case removal)
Run: `go test ./internal/corpus/ -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add examples/ internal/corpus/examples_test.go internal/corpus/testdata/coverage.golden
git commit -m "examples: render-test harness + migrate 5 examples; drop playground corpus cases"
```

---

## Task 3: Multi-file render server

**Files:**
- Modify: `playground/server/render.go` (`renderIn` source map ~lines 298-314; `readGenerated` ~line 350)
- Test: `playground/server/render_test.go` (Create)

**Interfaces:**
- Produces: `func splitSources(gsxSrc string) (map[string][]byte, error)` (filename → normalized bytes); `renderIn` uses it; `readGenerated` concatenates all `.x.go`.

- [ ] **Step 1: Write the failing test for `splitSources`**

Create `playground/server/render_test.go`:

```go
package main

import "testing"

func TestSplitSourcesSingle(t *testing.T) {
	got, err := splitSources("package foo\n\ncomponent A() { <p>x</p> }\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 file, got %d", len(got))
	}
	b, ok := got["comp.gsx"]
	if !ok {
		t.Fatal("single-file source must map to comp.gsx")
	}
	if string(b) != "package views\n\ncomponent A() { <p>x</p> }\n" {
		t.Fatalf("package line not normalized: %q", b)
	}
}

func TestSplitSourcesMulti(t *testing.T) {
	src := "-- a.gsx --\npackage views\n\ncomponent A() { <p>a</p> }\n-- b.gsx --\npackage views\n\ncomponent B() { <p>b</p> }\n"
	got, err := splitSources(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["a.gsx"] == nil || got["b.gsx"] == nil {
		t.Fatalf("want a.gsx+b.gsx, got %v", keys(got))
	}
}

func TestSplitSourcesRejectsBadName(t *testing.T) {
	if _, err := splitSources("-- ../evil.gsx --\npackage views\n"); err == nil {
		t.Fatal("expected error for path-traversal file name")
	}
	if _, err := splitSources("-- sub/x.gsx --\npackage views\n"); err == nil {
		t.Fatal("expected error for nested file name")
	}
	if _, err := splitSources("-- notes.txt --\npackage views\n"); err == nil {
		t.Fatal("expected error for non-.gsx file name")
	}
}

func keys(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./playground/server/ -run TestSplitSources`
Expected: FAIL (`undefined: splitSources`).

- [ ] **Step 3: Implement `splitSources`**

In `playground/server/render.go`, add (near the other helpers; it uses the existing `pkgLine` regexp and the `internal/txtar` package — add `"github.com/gsxhq/gsx/internal/txtar"` and `"strings"` to imports if not present):

```go
// splitSources interprets the playground source as a Go-Playground-style txtar:
// if it contains `-- name.gsx --` markers, each file becomes its own entry;
// otherwise the whole source is a single comp.gsx. Every file's package line is
// normalized to `package views`. File names must be a bare `*.gsx` (no `/`, no
// `..`) so writes stay inside the views dir.
func splitSources(gsxSrc string) (map[string][]byte, error) {
	arc := txtar.Parse([]byte(gsxSrc))
	out := map[string][]byte{}
	if len(arc.Files) == 0 {
		out["comp.gsx"] = []byte(pkgLine.ReplaceAllString(gsxSrc, "package views"))
		return out, nil
	}
	for _, f := range arc.Files {
		name := f.Name
		if strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
			return nil, fmt.Errorf("invalid file name %q: must be a bare *.gsx", name)
		}
		if !strings.HasSuffix(name, ".gsx") {
			return nil, fmt.Errorf("invalid file name %q: must end in .gsx", name)
		}
		out[name] = []byte(pkgLine.ReplaceAllString(string(f.Data), "package views"))
	}
	return out, nil
}
```

Add `"fmt"` to imports if not already present.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./playground/server/ -run TestSplitSources`
Expected: PASS.

- [ ] **Step 5: Wire `splitSources` into `renderIn` + import checks**

In `renderIn` (`render.go`), replace the single-file flow. Find:

```go
	userGSX := pkgLine.ReplaceAllString(in.GSX, "package views")

	// 0) Pre-flight import check on user source ...
	if d := checkImportsSource(userGSX); d != nil {
		return renderResp{Diagnostics: []diagnostic{*d}, Ms: ms()}
	}
```

Replace with:

```go
	srcFiles, splitErr := splitSources(in.GSX)
	if splitErr != nil {
		return renderResp{Error: splitErr.Error(), Ms: ms()}
	}

	// 0) Pre-flight import check on each user source file.
	for _, b := range srcFiles {
		if d := checkImportsSource(string(b)); d != nil {
			return renderResp{Diagnostics: []diagnostic{*d}, Ms: ms()}
		}
	}
```

Then find the `resolver.Generate` call:

```go
	res, gerr := resolver.Generate(
		ws.viewDir,
		map[string][]byte{
			filepath.Join(ws.viewDir, "comp.gsx"): []byte(userGSX),
		},
	)
```

Replace its source map with one built from `srcFiles`:

```go
	srcOverride := map[string][]byte{}
	for name, b := range srcFiles {
		srcOverride[filepath.Join(ws.viewDir, name)] = b
	}
	res, gerr := resolver.Generate(ws.viewDir, srcOverride)
```

- [ ] **Step 6: Make `readGenerated` concatenate all `.x.go` (multi-file)**

Replace `readGenerated` so the Generated-Go tab shows every generated file (sorted), not just the first:

```go
func readGenerated(viewDir string) string {
	entries, _ := os.ReadDir(viewDir)
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".x.go") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	var sb strings.Builder
	for i, n := range names {
		if i > 0 {
			sb.WriteString("\n")
		}
		b, _ := os.ReadFile(filepath.Join(viewDir, n))
		sb.Write(b)
	}
	return sb.String()
}
```

Add `"sort"` to imports if not present.

- [ ] **Step 7: Add a multi-file integration test (build tag aware)**

A real render needs the module/toolchain, so guard it behind the pool. Add to `render_test.go`:

```go
func TestRenderMultiFile(t *testing.T) {
	if testing.Short() {
		t.Skip("multi-file render needs the toolchain; skipped in -short")
	}
	p, err := newPool(defaultGsxMod(), t.TempDir(), 1)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	src := "-- comp.gsx --\npackage views\n\ncomponent Card(title string) { <section>{title}{children}</section> }\n" +
		"-- page.gsx --\npackage views\n\ncomponent Page() { <Card title=\"Hi\"><em>x</em></Card> }\n"
	resp := p.render(renderReq{GSX: src, Invoke: "Page(PageProps{})"})
	if resp.Error != "" || len(resp.Diagnostics) > 0 {
		t.Fatalf("render error: %s diags=%v", resp.Error, resp.Diagnostics)
	}
	want := "<section>Hi<em>x</em></section>"
	if resp.HTML != want {
		t.Fatalf("HTML = %q want %q", resp.HTML, want)
	}
}
```

- [ ] **Step 8: Run the tests**

Run: `go build ./... && go test ./playground/server/ -count=1`
Expected: PASS (`TestRenderMultiFile` exercises the toolchain; if the environment forbids network/toolchain, run `go test ./playground/server/ -run TestSplitSources` and note the integration test is environment-gated).

- [ ] **Step 9: Commit**

```bash
git add playground/server/render.go playground/server/render_test.go
git commit -m "playground: multi-file render (txtar source split) + concat generated Go"
```

---

## Task 4: `examplegen` core — fixtures → source-join, presets JSON, `#try=` payload

**Files:**
- Create: `internal/examplegen/examplegen.go`
- Create: `internal/examplegen/examplegen_test.go`
- Create: `internal/examplegen/testdata/single.txtar`, `internal/examplegen/testdata/multi.txtar`

**Interfaces:**
- Produces:
  - `type Example struct { Name, Summary, Category, Source, Invoke string; Order int }`
  - `func Load(dir string) ([]Example, error)` — reads `*.txtar`, sorted by `(Order, filename)`.
  - `func tryPayload(source, invoke string) string` — the base64 `#try=` value.
  - `func presetsJSON(exs []Example) ([]byte, error)` — `[{name,category,source,invoke}]`.

- [ ] **Step 1: Create hermetic generator fixtures**

`internal/examplegen/testdata/single.txtar`:

```
-- doc --
name: Hello
summary: A greeting.
category: Basics
order: 10
-- input.gsx --
package views

component Hello() { <p>hi</p> }
-- invoke --
Hello(HelloProps{})
-- render.golden --
<p>hi</p>
```

`internal/examplegen/testdata/multi.txtar`:

```
-- doc --
name: Two files
summary: A lib file and a page file.
category: Components & composition
order: 20
-- lib.gsx --
package views

component Box() { <div>box</div> }
-- page.gsx --
package views

component Page() { <Box/> }
-- invoke --
Page(PageProps{})
-- render.golden --
<div>box</div>
```

- [ ] **Step 2: Write failing tests**

Create `internal/examplegen/examplegen_test.go`:

```go
package examplegen

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestLoadSortsAndJoins(t *testing.T) {
	exs, err := Load("testdata")
	if err != nil {
		t.Fatal(err)
	}
	if len(exs) != 2 {
		t.Fatalf("want 2 examples, got %d", len(exs))
	}
	if exs[0].Name != "Hello" || exs[1].Name != "Two files" {
		t.Fatalf("order wrong: %s, %s", exs[0].Name, exs[1].Name)
	}
	// single-file: verbatim source, package normalized? No — source is verbatim.
	if !strings.HasPrefix(exs[0].Source, "package views") {
		t.Fatalf("single source: %q", exs[0].Source)
	}
	// multi-file: txtar-joined, files sorted by name (lib before page).
	m := exs[1].Source
	if !strings.HasPrefix(m, "-- lib.gsx --\n") || !strings.Contains(m, "\n-- page.gsx --\n") {
		t.Fatalf("multi source not txtar-joined:\n%s", m)
	}
}

func TestTryPayloadRoundTrip(t *testing.T) {
	// Mirror the Vue decoder: base64 std → JSON → {s,i}.
	src, inv := "package views\n", "Hello(HelloProps{})"
	payload := tryPayload(src, inv)
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatal(err)
	}
	var o struct {
		S string `json:"s"`
		I string `json:"i"`
	}
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatal(err)
	}
	if o.S != src || o.I != inv {
		t.Fatalf("round-trip mismatch: %+v", o)
	}
}

func TestPresetsJSON(t *testing.T) {
	exs, _ := Load("testdata")
	b, err := presetsJSON(exs)
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]string
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got[0]["name"] != "Hello" || got[0]["source"] == "" || got[0]["invoke"] == "" || got[0]["category"] != "Basics" {
		t.Fatalf("preset[0] = %v", got[0])
	}
}
```

- [ ] **Step 3: Run to verify failure**

Run: `go test ./internal/examplegen/`
Expected: FAIL (`undefined: Load`).

- [ ] **Step 4: Implement `examplegen.go`**

Create `internal/examplegen/examplegen.go`:

```go
// Package examplegen turns the single-source examples/*.txtar fixtures into the
// docs Examples page and the playground preset lists. It is the one place that
// knows the playground source string format and the #try= payload encoding, so
// docs, frontend presets, and backend cache-seed can never drift.
package examplegen

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/gsxhq/gsx/internal/txtar"
)

// Example is one fully-parsed fixture.
type Example struct {
	Name     string
	Summary  string
	Category string
	Order    int
	Source   string  // playground source string (single verbatim, or txtar-joined)
	Invoke   string
	Files    []SourceFile // individual source files, for per-file docs blocks
}

// SourceFile is one .gsx file of an example.
type SourceFile struct {
	Name string
	Body string
}

// Load reads every *.txtar in dir into Examples, sorted by (Order, filename).
func Load(dir string) ([]Example, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	type loaded struct {
		ex   Example
		file string
	}
	var ls []loaded
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".txtar") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		ex, err := loadOne(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		ls = append(ls, loaded{ex, e.Name()})
	}
	sort.SliceStable(ls, func(i, j int) bool {
		if ls[i].ex.Order != ls[j].ex.Order {
			return ls[i].ex.Order < ls[j].ex.Order
		}
		return ls[i].file < ls[j].file
	})
	out := make([]Example, len(ls))
	for i, l := range ls {
		out[i] = l.ex
	}
	return out, nil
}

func loadOne(path string) (Example, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Example{}, err
	}
	arc := txtar.Parse(data)
	var ex Example
	var files []SourceFile
	var invoke string
	hasDoc := false
	for _, f := range arc.Files {
		switch {
		case f.Name == "doc":
			hasDoc = true
			parseDoc(&ex, f.Data)
		case f.Name == "invoke":
			invoke = strings.TrimSpace(string(f.Data))
		case strings.HasSuffix(f.Name, ".gsx"):
			if strings.ContainsAny(f.Name, "/\\") {
				return Example{}, fmt.Errorf("source file %q must be a bare *.gsx (one package)", f.Name)
			}
			if pkg := packageName(f.Data); pkg != "views" {
				return Example{}, fmt.Errorf("source file %q is package %q, must be views", f.Name, pkg)
			}
			files = append(files, SourceFile{Name: f.Name, Body: string(f.Data)})
		}
	}
	if !hasDoc {
		return Example{}, fmt.Errorf("missing -- doc -- section")
	}
	if len(files) == 0 {
		return Example{}, fmt.Errorf("no .gsx source files")
	}
	if invoke == "" {
		return Example{}, fmt.Errorf("missing -- invoke -- section")
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	ex.Files = files
	ex.Invoke = invoke
	ex.Source = joinSource(files)
	return ex, nil
}

// joinSource returns the playground source string: a single file verbatim, or
// multiple files in Go-Playground txtar format (sorted by name).
func joinSource(files []SourceFile) string {
	if len(files) == 1 {
		return files[0].Body
	}
	var b strings.Builder
	for _, f := range files {
		b.WriteString("-- " + f.Name + " --\n")
		body := f.Body
		if !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
		b.WriteString(body)
	}
	return b.String()
}

func parseDoc(ex *Example, b []byte) {
	for _, line := range strings.Split(string(b), "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		switch key {
		case "name":
			ex.Name = val
		case "summary":
			ex.Summary = val
		case "category":
			ex.Category = val
		case "order":
			if n, err := strconv.Atoi(val); err == nil {
				ex.Order = n
			}
		}
	}
}

func packageName(src []byte) string {
	for _, line := range strings.Split(string(src), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "package ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "package "))
		}
	}
	return ""
}

// tryPayload encodes {s:source, i:invoke} as the #try= hash value: JSON →
// std base64. Matches the Vue decoder (atob over UTF-8 → JSON.parse → o.s/o.i).
func tryPayload(source, invoke string) string {
	b, _ := json.Marshal(struct {
		S string `json:"s"`
		I string `json:"i"`
	}{source, invoke})
	return base64.StdEncoding.EncodeToString(b)
}

// presetsJSON renders the preset list for the frontend + backend.
func presetsJSON(exs []Example) ([]byte, error) {
	type preset struct {
		Name     string `json:"name"`
		Category string `json:"category"`
		Source   string `json:"source"`
		Invoke   string `json:"invoke"`
	}
	out := make([]preset, len(exs))
	for i, e := range exs {
		out[i] = preset{e.Name, e.Category, e.Source, e.Invoke}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
```

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/examplegen/ -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/examplegen/examplegen.go internal/examplegen/examplegen_test.go internal/examplegen/testdata/
git commit -m "examplegen: load fixtures, join multi-file source, #try= payload, presets JSON"
```

---

## Task 5: `examplegen` Markdown emit + `cmd/gsx-examples` + Makefile

**Files:**
- Modify: `internal/examplegen/examplegen.go` (add `RenderMarkdown`, `Generate`)
- Modify: `internal/examplegen/examplegen_test.go` (add markdown test)
- Create: `cmd/gsx-examples/main.go`
- Modify: `Makefile`

**Interfaces:**
- Consumes: `Load`, `tryPayload`, `presetsJSON` (Task 4).
- Produces: `func RenderMarkdown(exs []Example) []byte`; `func Generate(examplesDir, mdPath, jsonPaths ...string) error`.

- [ ] **Step 1: Write the failing markdown test**

Add to `internal/examplegen/examplegen_test.go`:

```go
func TestRenderMarkdown(t *testing.T) {
	exs, _ := Load("testdata")
	md := string(RenderMarkdown(exs))
	// category headings
	if !strings.Contains(md, "## Basics") || !strings.Contains(md, "## Components & composition") {
		t.Fatalf("missing category headings:\n%s", md)
	}
	// example heading + summary + a gsx fence + a playground link
	if !strings.Contains(md, "### Hello") || !strings.Contains(md, "A greeting.") {
		t.Fatalf("missing example heading/summary")
	}
	if !strings.Contains(md, "```gsx") {
		t.Fatalf("missing gsx code fence")
	}
	if !strings.Contains(md, "/playground#try=") {
		t.Fatalf("missing playground link")
	}
	// multi-file example shows per-file captions
	if !strings.Contains(md, "**lib.gsx**") || !strings.Contains(md, "**page.gsx**") {
		t.Fatalf("multi-file example missing per-file captions:\n%s", md)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/examplegen/ -run TestRenderMarkdown`
Expected: FAIL (`undefined: RenderMarkdown`).

- [ ] **Step 3: Implement `RenderMarkdown` + `Generate`**

Add to `internal/examplegen/examplegen.go`:

```go
// RenderMarkdown emits the docs Examples page: a fixed intro, then examples
// grouped under ## {category} headings (categories in first-seen order), each
// with its summary, one ```gsx block per source file (captioned when >1 file),
// and an Open-in-Playground link.
func RenderMarkdown(exs []Example) []byte {
	var b strings.Builder
	b.WriteString("# Examples\n\n")
	b.WriteString("A gallery of gsx features. Each example is compiled and checked in CI; click **Open in Playground** to run and edit it live.\n\n")
	b.WriteString("<!-- GENERATED by cmd/gsx-examples from examples/*.txtar — do not edit by hand. -->\n\n")

	var order []string
	seen := map[string]bool{}
	for _, e := range exs {
		if !seen[e.Category] {
			seen[e.Category] = true
			order = append(order, e.Category)
		}
	}
	for _, cat := range order {
		b.WriteString("## " + cat + "\n\n")
		for _, e := range exs {
			if e.Category != cat {
				continue
			}
			b.WriteString("### " + e.Name + "\n\n")
			if e.Summary != "" {
				b.WriteString(e.Summary + "\n\n")
			}
			for _, f := range e.Files {
				if len(e.Files) > 1 {
					b.WriteString("**" + f.Name + "**\n\n")
				}
				b.WriteString("```gsx\n")
				body := f.Body
				if !strings.HasSuffix(body, "\n") {
					body += "\n"
				}
				b.WriteString(body)
				b.WriteString("```\n\n")
			}
			b.WriteString("[▶ Open in Playground](/playground#try=" + tryPayload(e.Source, e.Invoke) + ")\n\n")
		}
	}
	return []byte(b.String())
}

// Generate loads examplesDir and writes the docs Markdown to mdPath and the
// preset JSON to each path in jsonPaths.
func Generate(examplesDir, mdPath string, jsonPaths ...string) error {
	exs, err := Load(examplesDir)
	if err != nil {
		return err
	}
	if err := os.WriteFile(mdPath, RenderMarkdown(exs), 0o644); err != nil {
		return err
	}
	pj, err := presetsJSON(exs)
	if err != nil {
		return err
	}
	for _, p := range jsonPaths {
		if err := os.WriteFile(p, pj, 0o644); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/examplegen/ -count=1`
Expected: PASS.

- [ ] **Step 5: Create the CLI**

Create `cmd/gsx-examples/main.go`:

```go
// Command gsx-examples regenerates the docs Examples page and the playground
// preset lists from the single-source examples/*.txtar fixtures. Run from the
// repo root (e.g. `make examples`). Generated files are committed.
package main

import (
	"flag"
	"log"

	"github.com/gsxhq/gsx/internal/examplegen"
)

func main() {
	examplesDir := flag.String("examples", "examples", "directory of *.txtar fixtures")
	mdOut := flag.String("md", "docs/guide/examples.md", "docs Markdown output path")
	docsJSON := flag.String("docs-json", "docs/examples.json", "frontend preset JSON output path")
	serverJSON := flag.String("server-json", "playground/server/examples.json", "backend preset JSON output path")
	flag.Parse()

	if err := examplegen.Generate(*examplesDir, *mdOut, *docsJSON, *serverJSON); err != nil {
		log.Fatalf("gsx-examples: %v", err)
	}
	log.Printf("generated %s, %s, %s", *mdOut, *docsJSON, *serverJSON)
}
```

- [ ] **Step 6: Add the Makefile target**

In `Makefile`, add `examples` to `.PHONY` and a target:

```make
.PHONY: test cover cover-html examples

examples:
	go run ./cmd/gsx-examples
```

- [ ] **Step 7: Build + verify the CLI compiles**

Run: `go build ./... && go vet ./internal/examplegen/ ./cmd/gsx-examples/`
Expected: clean.

- [ ] **Step 8: Commit**

```bash
git add internal/examplegen/examplegen.go internal/examplegen/examplegen_test.go cmd/gsx-examples/main.go Makefile
git commit -m "examplegen: docs Markdown emit + gsx-examples CLI + make examples"
```

---

## Task 6: Author Basics + Control-flow fixtures (attributes, loops, switch)

**Files:**
- Create: `examples/20-attributes.txtar`, `examples/50-loops.txtar`, `examples/60-switch.txtar`

**Interfaces:** Consumes the Task 2 render test (the gate). Syntax authority cited per fixture.

- [ ] **Step 1: Create the fixtures**

`examples/20-attributes.txtar` (syntax authority: `internal/corpus/testdata/cases/attrs/expr_attrs.txtar` + `attrs/cond_attr_bool_on.txtar`):

```
-- doc --
name: Attributes
summary: Expression attributes (attr={expr}), boolean attributes, and a conditional attribute via { if … { attr=… } }.
category: Basics
order: 20
-- input.gsx --
package views

component Link(url string, label string, external bool, featured bool) {
	<a href={url} data-count={3} aria-current={external} { if featured { class="featured" } }>{label}</a>
}
-- invoke --
Link(LinkProps{Url: "/p?q=a&b", Label: "Docs", External: true, Featured: true})
-- render.golden --
```

`examples/50-loops.txtar` (syntax authority: `control_flow/for.txtar`):

```
-- doc --
name: Loops over lists
summary: { for … := range … } renders markup per element using a real Go range loop.
category: Control flow
order: 50
-- input.gsx --
package views

type Item struct {
	Name  string
	Count int
}

component List(items []Item) {
	<ul>{ for _, it := range items {
		<li>{it.Name}: {it.Count}</li>
	} }</ul>
}
-- invoke --
List(ListProps{Items: []Item{{Name: "alpha", Count: 1}, {Name: "beta", Count: 2}}})
-- render.golden --
```

`examples/60-switch.txtar` (syntax authority: `control_flow/switch_default.txtar`):

```
-- doc --
name: Switch
summary: { switch … } selects one branch of markup; default handles the rest.
category: Control flow
order: 60
-- input.gsx --
package views

component Badge(kind string) {
	<span>{ switch kind {
	case "warn":
		<b>warning</b>
	case "err":
		<b>error</b>
	default:
		<b>info</b>
	} }</span>
}
-- invoke --
Badge(BadgeProps{Kind: "warn"})
-- render.golden --
```

- [ ] **Step 2: Generate goldens + verify clean and correct**

Run: `go test ./internal/corpus/ -run TestExamples -update`
Run: `go test ./internal/corpus/ -run TestExamples -count=1`
Expected: PASS with no `diagnostics` failures. **Read each new `render.golden`** and confirm it is correct (e.g. `20-attributes` renders `class="featured"` and the escaped `href`; `50-loops` renders two `<li>`; `60-switch` renders `<b>warning</b>`). If any example produced diagnostics, fix the gsx against the cited corpus case and re-run.

- [ ] **Step 3: Commit**

```bash
git add examples/20-attributes.txtar examples/50-loops.txtar examples/60-switch.txtar
git commit -m "examples: attributes, loops, switch"
```

---

## Task 7: Author Components & composition fixtures

**Files:**
- Create: `examples/70-components.txtar`, `examples/90-named-slots.txtar`, `examples/100-template-composition.txtar`, `examples/110-fallthrough-attrs.txtar`, `examples/120-method-components.txtar`

- [ ] **Step 1: Create the fixtures**

`examples/70-components.txtar` (authority: `components/child_props.txtar`):

```
-- doc --
name: Components & props
summary: Call a component with a typed props struct; boolean props pass bare (featured).
category: Components & composition
order: 70
-- input.gsx --
package views

component Card(title string, featured bool, count int) {
	<div class={ "card", "card-featured": featured }><h2>{title}</h2><span>{count}</span></div>
}

component Page(t string, n int) {
	<Card title={t} featured count={n}/>
}
-- invoke --
Page(PageProps{T: "Hi", N: 3})
-- render.golden --
```

`examples/90-named-slots.txtar` (authority: `components/named_slot_multiple.txtar`):

```
-- doc --
name: Named slots
summary: Pass markup into named gsx.Node props — header and footer slots.
category: Components & composition
order: 90
-- input.gsx --
package views

import "github.com/gsxhq/gsx"

component Panel(header gsx.Node, footer gsx.Node) {
	<div class="panel"><header>{header}</header><footer>{footer}</footer></div>
}

component Page() {
	<Panel header={ <h1>H</h1> } footer={ <small>F</small> }/>
}
-- invoke --
Page(PageProps{})
-- render.golden --
```

`examples/100-template-composition.txtar` — **multi-file** (authority: `components/children_slot.txtar` for `{children}`, `methods/render_method_with_param.txtar` for the method receiver; nullary-method invoke form authority: `methods/render_method_nullary.txtar` — if a nullary method does **not** take a Props arg, change the invoke to `(HomePage{Title: "Dashboard"}).Render()`):

```
-- doc --
name: Template composition
summary: A shared component library composed by a page method — multiple files, one render entry. This is the multi-file showcase.
category: Components & composition
order: 100
-- components.gsx --
package views

component Button(label string) {
	<button class="btn">{label}</button>
}

component Card(title string) {
	<section class="card"><h2>{title}</h2>{children}</section>
}
-- page.gsx --
package views

type HomePage struct {
	Title string
}

component (p HomePage) Render() {
	<main>
		<Card title={p.Title}>
			<Button label="Save"/>
		</Card>
	</main>
}
-- invoke --
(HomePage{Title: "Dashboard"}).Render(HomePageRenderProps{})
-- render.golden --
```

`examples/110-fallthrough-attrs.txtar` (authority: `fallthrough/call_site_button.txtar`):

```
-- doc --
name: Fallthrough attributes
summary: Undeclared call-site attributes (class, data-*, hx-*) fall through to the component's root element; class merges (caller-wins).
category: Components & composition
order: 110
-- input.gsx --
package views

component Button(variant string) {
	<button class="btn" data-variant={variant}>{children}</button>
}

component Page() {
	<Button variant="primary" class="w-full" data-test="x" hx-post="/go">Save</Button>
}
-- invoke --
Page(PageProps{})
-- render.golden --
```

`examples/120-method-components.txtar` (authority: `methods/render_method_with_param.txtar`):

```
-- doc --
name: Method components
summary: Components can be methods on a type, so page state lives on the receiver (p) and per-call data in the props struct.
category: Components & composition
order: 120
-- input.gsx --
package views

type UsersPage struct {
	Title string
	Sort  string
}

component (p UsersPage) Grid(sort string) {
	<div>{sort}-{p.Title}</div>
}
-- invoke --
(UsersPage{Title: "Team"}).Grid(UsersPageGridProps{Sort: "name"})
-- render.golden --
```

- [ ] **Step 2: Generate goldens + verify clean and correct**

Run: `go test ./internal/corpus/ -run TestExamples -update`
Run: `go test ./internal/corpus/ -run TestExamples -count=1`
Expected: PASS, no diagnostics. **Read each new golden.** Confirm `100-template-composition` renders `<main><section class="card"><h2>Dashboard</h2><button class="btn">Save</button></section></main>`. If the nullary-method invoke form is wrong, fix the `-- invoke --` per `methods/render_method_nullary.txtar` and re-run.

- [ ] **Step 3: Commit**

```bash
git add examples/70-components.txtar examples/90-named-slots.txtar examples/100-template-composition.txtar examples/110-fallthrough-attrs.txtar examples/120-method-components.txtar
git commit -m "examples: components, named slots, template composition (multi-file), fallthrough, method components"
```

---

## Task 8: Author Styling + Transforming + Interactive fixtures

**Files:**
- Create: `examples/140-style-blocks.txtar`, `examples/150-pipelines.txtar`, `examples/160-fragments.txtar`, `examples/170-forms.txtar`, `examples/180-js-and-islands.txtar`, `examples/190-full-document.txtar`

- [ ] **Step 1: Create the fixtures**

`examples/140-style-blocks.txtar` (authority: `style/block_interpolation.txtar`):

```
-- doc --
name: Style blocks
summary: A <style> block interpolates values with @{ … }; interpolated values are CSS-sanitized by construction.
category: Styling
order: 140
-- input.gsx --
package views

component Card(w int, userColor string) {
	<style>
		.card {
			width: @{ w }px;
			color: @{ userColor };
		}
	</style>
}
-- invoke --
Card(CardProps{W: 12, UserColor: "teal"})
-- render.golden --
```

`examples/150-pipelines.txtar` (authority: `pipelines/chain.txtar` + `pipelines/param.txtar`):

```
-- doc --
name: Pipelines / filters
summary: Transform values with typed filter pipelines — { x |> trim |> upper } — drawn from the gsx info registry.
category: Transforming values
order: 150
-- input.gsx --
package views

component Hi(name string) {
	<p>{ name |> trim |> upper }</p>
}
-- invoke --
Hi(HiProps{Name: "  ada  "})
-- render.golden --
```

`examples/160-fragments.txtar` (authority: `elements/fragment.txtar`):

```
-- doc --
name: Fragments
summary: A component can return multiple roots with no wrapper element using <>…</>.
category: Interactive & whole-page
order: 160
-- input.gsx --
package views

component Pair(a string, b string) {
	<><span>{a}</span><span>{b}</span></>
}
-- invoke --
Pair(PairProps{A: "x", B: "y"})
-- render.golden --
```

`examples/170-forms.txtar` (authority: `codegen-shape/example12_form.txtar` for the Field + `{...attrs}` fallthrough pattern):

```
-- doc --
name: Forms
summary: A reusable Field component forwards undeclared attributes ({...attrs}) onto its input, so callers add type/name/required without Field declaring them.
category: Interactive & whole-page
order: 170
-- input.gsx --
package views

component Field(label string) {
	<div class="field"><label>{label}</label><input class="control" {...attrs}/></div>
}

component LoginForm() {
	<form method="post" action="/login">
		<Field label="Email" type="email" name="email" required/>
		<Field label="Password" type="password" name="password" required/>
		<button type="submit">Sign in</button>
	</form>
}
-- invoke --
LoginForm(LoginFormProps{})
-- render.golden --
```

`examples/180-js-and-islands.txtar` (authority: `jsattr/click_rawjs.txtar` + `datajson/island_value.txtar`):

```
-- doc --
name: JS attributes & data islands
summary: @click={ gsx.RawJS(…) } emits a vouched event handler; a <script type="application/json"> island serializes typed Go data with @{ … } for client JS.
category: Interactive & whole-page
order: 180
-- input.gsx --
package views

import "github.com/gsxhq/gsx"

type Config struct {
	Env  string
	Beta bool
}

component Widget(cfg Config) {
	<div>
		<button @click={ gsx.RawJS("toggle()") }>Toggle</button>
		<script type="application/json" id="cfg">@{ cfg }</script>
	</div>
}
-- invoke --
Widget(WidgetProps{Cfg: Config{Env: "prod", Beta: true}})
-- render.golden --
```

`examples/190-full-document.txtar` (authority: `doctype/render.txtar`):

```
-- doc --
name: Full HTML document
summary: Render a whole page, including <!DOCTYPE html>, as one component.
category: Interactive & whole-page
order: 190
-- input.gsx --
package views

component Page(title string) {
	<!DOCTYPE html>
	<html lang="en">
		<head><title>{ title }</title></head>
		<body>hi</body>
	</html>
}
-- invoke --
Page(PageProps{Title: "Home"})
-- render.golden --
```

- [ ] **Step 2: Generate goldens + verify clean and correct**

Run: `go test ./internal/corpus/ -run TestExamples -update`
Run: `go test ./internal/corpus/ -run TestExamples -count=1`
Expected: PASS, no diagnostics. **Read each new golden.** Confirm `140-style-blocks` shows the interpolated CSS, `150-pipelines` renders `<p>ADA</p>`, `190-full-document` includes `<!DOCTYPE html>`. Fix any diagnostics against the cited corpus case and re-run.

- [ ] **Step 3: Run the full corpus suite**

Run: `go test ./internal/corpus/ -count=1`
Expected: PASS (all 19 examples + the corpus).

- [ ] **Step 4: Commit**

```bash
git add examples/140-style-blocks.txtar examples/150-pipelines.txtar examples/160-fragments.txtar examples/170-forms.txtar examples/180-js-and-islands.txtar examples/190-full-document.txtar
git commit -m "examples: style blocks, pipelines, fragments, forms, js+islands, full document"
```

---

## Task 9: Generate + commit artifacts

**Files:**
- Create (generated): `docs/guide/examples.md`, `docs/examples.json`, `playground/server/examples.json`

- [ ] **Step 1: Run the generator**

Run: `make examples`
Expected: log line naming the three written files.

- [ ] **Step 2: Sanity-check the artifacts**

- `docs/guide/examples.md`: open it; confirm six `##` category headings in order, 19 `###` example headings, the `100-template-composition` example shows `**components.gsx**` and `**page.gsx**` captions with two code blocks, and every example ends with a `/playground#try=` link.
- `docs/examples.json` and `playground/server/examples.json`: confirm they are byte-identical (`diff docs/examples.json playground/server/examples.json` → empty) and contain 19 entries each with `name`, `category`, `source`, `invoke`.

- [ ] **Step 3: Verify a `#try=` link decodes correctly**

Run (decodes the first link's payload and prints the JSON; uses python3 for
cross-platform base64 — macOS `base64` uses `-D`, GNU uses `-d`):

```bash
grep -o '#try=[A-Za-z0-9+/=]*' docs/guide/examples.md | head -1 | sed 's/#try=//' | python3 -c 'import sys,base64; print(base64.b64decode(sys.stdin.read().strip()).decode())'
```

Expected: `{"s":"package views...","i":"Greeting(GreetingProps{Name: \"World\", Count: 3})"}` (the interpolation example). Confirms the Go encoder matches the Vue decoder shape.

- [ ] **Step 4: Confirm the build still passes**

Run: `go build ./... && go test ./internal/examplegen/ ./internal/corpus/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add docs/guide/examples.md docs/examples.json playground/server/examples.json
git commit -m "examples: generate docs page + frontend/backend preset JSON"
```

---

## Task 10: Backend cache-seed from embedded `examples.json`

**Files:**
- Modify: `playground/server/presets.go`
- Test: `playground/server/presets_test.go` (Create)

**Interfaces:**
- Consumes: `playground/server/examples.json` (Task 9); `renderReq{GSX, Invoke}` (existing).
- Produces: `presets []renderReq` derived from the embedded JSON.

- [ ] **Step 1: Write the failing test**

Create `playground/server/presets_test.go`:

```go
package main

import "testing"

func TestPresetsFromEmbed(t *testing.T) {
	if len(presets) != 19 {
		t.Fatalf("want 19 embedded presets, got %d", len(presets))
	}
	for i, p := range presets {
		if p.GSX == "" || p.Invoke == "" {
			t.Fatalf("preset %d empty: %+v", i, p)
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./playground/server/ -run TestPresetsFromEmbed`
Expected: FAIL (currently 5 hardcoded presets, not 19).

- [ ] **Step 3: Replace the hardcoded list with the embed**

Rewrite `playground/server/presets.go`:

```go
package main

import (
	_ "embed"
	"encoding/json"
	"log"
)

// examplesJSON is generated by cmd/gsx-examples from the single-source
// examples/*.txtar fixtures (see internal/examplegen). It is the same preset
// list the docs page and the frontend dropdown use, so the cache-seed can never
// drift from the documented examples.
//
//go:embed examples.json
var examplesJSON []byte

// presets are the default examples, seeded into the response cache at startup so
// visitors get instant first renders. Built from the embedded examples.json.
var presets = mustLoadPresets()

func mustLoadPresets() []renderReq {
	var raw []struct {
		Source string `json:"source"`
		Invoke string `json:"invoke"`
	}
	if err := json.Unmarshal(examplesJSON, &raw); err != nil {
		log.Fatalf("presets: parse embedded examples.json: %v", err)
	}
	out := make([]renderReq, len(raw))
	for i, r := range raw {
		out[i] = renderReq{GSX: r.Source, Invoke: r.Invoke}
	}
	return out
}

// seedPresets renders each preset once so it is warm in the response cache.
// Intended to run in a background goroutine after the server starts listening.
func (p *pool) seedPresets() {
	for _, pr := range presets {
		p.render(pr)
	}
}
```

- [ ] **Step 4: Run the test**

Run: `go build ./... && go test ./playground/server/ -run TestPresetsFromEmbed`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add playground/server/presets.go playground/server/presets_test.go
git commit -m "playground: seed response cache from embedded generated examples.json"
```

---

## Task 11: Site — sync presets + Vue imports generated list

**Repo:** `/Users/jackieli/personal/gsxhq/gsxhq.github.io`

**Files:**
- Modify: `scripts/sync-docs.mjs`
- Create: `.vitepress/theme/presets.generated.json`
- Modify: `.vitepress/theme/GsxPlayground.vue` (the `const presets = [...]` block, ~lines 17-82)

**Interfaces:**
- Consumes: `docs/examples.json` from the gsx repo (Task 9). For local dev, the committed `presets.generated.json` keeps the import resolvable.

- [ ] **Step 1: Seed the committed presets file**

Copy the generated frontend presets into the theme as the committed default:

```bash
cp /Users/jackieli/personal/gsxhq/gsx/.claude/worktrees/docs-groundwork/docs/examples.json \
   /Users/jackieli/personal/gsxhq/gsxhq.github.io/.vitepress/theme/presets.generated.json
```

(After the gsx branch merges to main, this file is regenerated at build time by sync; the committed copy is the local-dev fallback.)

- [ ] **Step 2: Extend `sync-docs.mjs` to copy the presets JSON**

In `scripts/sync-docs.mjs`, after the guide copy/link block (end of file), add a presets copy. First compute the gsx repo root next to the guide source. Replace the final `resolveSource()` usage region by appending:

```js
// Also sync the generated playground presets (single source of the example
// gallery) into the theme, so the dropdown matches the docs Examples page.
function syncPresets() {
  let jsonSrc
  if (process.env.GSX_DOCS_SRC) {
    jsonSrc = resolve(process.env.GSX_DOCS_SRC, 'docs', 'examples.json')
  } else {
    const sibling = resolve('..', 'gsx', 'docs', 'examples.json')
    if (existsSync(sibling)) jsonSrc = sibling
  }
  const dest = resolve('.vitepress/theme/presets.generated.json')
  if (jsonSrc && existsSync(jsonSrc)) {
    cpSync(jsonSrc, dest)
    console.log(`copied presets: ${jsonSrc} -> ${dest}`)
  } else {
    console.log('presets: no docs/examples.json source; keeping committed presets.generated.json')
  }
}
syncPresets()
```

(Ensure `resolve`, `existsSync`, `cpSync` are already imported at the top — they are.)

- [ ] **Step 3: Replace the hardcoded presets in the Vue component**

In `.vitepress/theme/GsxPlayground.vue`, in the `<script setup>` block, add an import near the other imports (after the hljs imports):

```ts
import generatedPresets from './presets.generated.json'
```

Then replace the entire hardcoded `const presets = [ … ]` array (the block starting `// Example presets double as …` through its closing `]`) with:

```ts
// Example presets are generated from the single-source examples/*.txtar fixtures
// (see the gsx repo internal/examplegen). The docs Examples page, this dropdown,
// and the backend cache-seed all come from the same source — no drift.
const presets = generatedPresets as { name: string; category?: string; source: string; invoke: string }[]
```

- [ ] **Step 4: Build the site to verify the import + presets resolve**

Run:

```bash
cd /Users/jackieli/personal/gsxhq/gsxhq.github.io
GSX_DOCS_SRC=/Users/jackieli/personal/gsxhq/gsx/.claude/worktrees/docs-groundwork \
VITE_GSX_PLAYGROUND_API=https://gsx-playground-hpjfz4kanq-uc.a.run.app \
npm run build 2>&1 | tail -5
```

Expected: `build complete`. The build log should include `copied presets:` and the synced `guide/examples.md`.

- [ ] **Step 5: Commit (site repo)**

```bash
cd /Users/jackieli/personal/gsxhq/gsxhq.github.io
git add scripts/sync-docs.mjs .vitepress/theme/presets.generated.json .vitepress/theme/GsxPlayground.vue
git commit -m "playground: presets from generated examples.json (single source); sync step"
```

---

## Task 12: Site — multi-file separator decoration in the editor

**Repo:** `/Users/jackieli/personal/gsxhq/gsxhq.github.io`

**Files:**
- Modify: `.vitepress/theme/GsxPlayground.vue` (CodeMirror setup)

**Interfaces:** Consumes the existing CodeMirror editor instance. Produces a line decoration that styles `-- file --` separator lines so multi-file sources read as divided files (Go Playground style).

- [ ] **Step 1: Add a CodeMirror line decoration for separator lines**

In `.vitepress/theme/GsxPlayground.vue` `<script setup>`, import the decoration primitives alongside the existing CodeMirror imports (the file already imports from `@codemirror/view`; add `Decoration`, `ViewPlugin`, and `RangeSetBuilder` from `@codemirror/state` if not present):

```ts
import { Decoration, ViewPlugin } from '@codemirror/view'
import type { DecorationSet, ViewUpdate } from '@codemirror/view'
import { RangeSetBuilder } from '@codemirror/state'

// Highlight Go-Playground-style `-- file.gsx --` separator lines so multi-file
// sources read as dividers, not code.
const fileSeparator = /^--\s+\S+\s+--\s*$/
const separatorDeco = Decoration.line({ class: 'pg__sep-line' })
const separatorPlugin = ViewPlugin.fromClass(
  class {
    decorations: DecorationSet
    constructor(view: any) {
      this.decorations = this.build(view)
    }
    update(u: ViewUpdate) {
      if (u.docChanged || u.viewportChanged) this.decorations = this.build(u.view)
    }
    build(view: any): DecorationSet {
      const b = new RangeSetBuilder<Decoration>()
      for (const { from, to } of view.visibleRanges) {
        for (let pos = from; pos <= to; ) {
          const line = view.state.doc.lineAt(pos)
          if (fileSeparator.test(line.text)) b.add(line.from, line.from, separatorDeco)
          pos = line.to + 1
        }
      }
      return b.build()
    }
  },
  { decorations: (v) => v.decorations },
)
```

Add `separatorPlugin` to the editor's `extensions` array where the other extensions (keymap, theme) are registered.

- [ ] **Step 2: Style the decorated lines**

In the component's `<style scoped>`, add:

```css
.pg__sep-line {
  background: color-mix(in srgb, var(--accent) 12%, transparent);
  border-top: 1px solid var(--line);
  font-weight: 600;
}
```

- [ ] **Step 3: Verify visually**

Run the site dev server (or `npm run build`) and load the playground; select the **Template composition** preset. Confirm the `-- components.gsx --` and `-- page.gsx --` lines render with the divider styling and that editing across the separator still renders (the server splits it).

```bash
cd /Users/jackieli/personal/gsxhq/gsxhq.github.io
GSX_DOCS_SRC=/Users/jackieli/personal/gsxhq/gsx/.claude/worktrees/docs-groundwork npm run build 2>&1 | tail -3
```

Expected: `build complete` (no CodeMirror import errors). Visual confirmation is manual.

- [ ] **Step 4: Commit (site repo)**

```bash
cd /Users/jackieli/personal/gsxhq/gsxhq.github.io
git add .vitepress/theme/GsxPlayground.vue
git commit -m "playground: decorate -- file -- separators for multi-file editing"
```

---

## Task 13: Site — fix Examples nav + sidebar

**Repo:** `/Users/jackieli/personal/gsxhq/gsxhq.github.io`

**Files:**
- Modify: `.vitepress/config.mts` (nav line ~32; sidebar `/guide/` items ~39-44)

- [ ] **Step 1: Point the Examples nav item at the docs page**

In `.vitepress/config.mts`, replace:

```ts
      { text: 'Examples', link: 'https://github.com/gsxhq/gsx/tree/main/examples' },
```

with:

```ts
      { text: 'Examples', link: '/guide/examples' },
```

- [ ] **Step 2: Add Examples to the guide sidebar**

In the `/guide/` sidebar `items` array, add after the CLI entry:

```ts
            { text: 'CLI', link: '/guide/cli' },
            { text: 'Examples', link: '/guide/examples' },
```

- [ ] **Step 3: Build and verify the page resolves**

```bash
cd /Users/jackieli/personal/gsxhq/gsxhq.github.io
GSX_DOCS_SRC=/Users/jackieli/personal/gsxhq/gsx/.claude/worktrees/docs-groundwork npm run build 2>&1 | tail -3
test -f guide/examples.md && echo "examples page synced"
```

Expected: `build complete` and `examples page synced` (the generated `examples.md` was synced into `guide/`). Confirm no dead-link warnings for `/guide/examples`.

- [ ] **Step 4: Commit (site repo)**

```bash
cd /Users/jackieli/personal/gsxhq/gsxhq.github.io
git add .vitepress/config.mts
git commit -m "site: Examples nav + sidebar point at /guide/examples"
```

---

## Final verification (after all tasks)

- [ ] gsx repo: `go build ./... && go vet ./... && go test ./... -count=1` → all PASS (19 examples render, generator tests, multi-file render, embedded presets).
- [ ] gsx repo: `make examples && git diff --exit-code docs/guide/examples.md docs/examples.json playground/server/examples.json` → no diff (artifacts match fixtures).
- [ ] site repo: full build with `GSX_DOCS_SRC` set → `/guide/examples` renders with all six sections; the playground dropdown lists all 19; an "Open in Playground" link from the docs preloads the right source + invoke; the Template composition preset shows decorated `-- file --` separators and renders.
- [ ] Deploy: the gsx branch merges to `main` (so the site's next build syncs the new guide page + presets); push the site repo (Pages auto-deploys).
