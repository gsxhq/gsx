# gsx Slice B — `tdewolff/parse` dep + safe built-in JS minifier Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Bring the `tdewolff/parse/v2` dependency into gsx's generator and use its JS lexer to minify holeless `<script>` content at codegen time (on by default), with a `gen.WithJSMinifier` extension seam — mirroring slice-2's CSS minification.

**Architecture:** A new `internal/jsmin` package: `minifyJS(string) string` is a tdewolff-lexer-driven *safe* JS minifier (strip comments keeping `/*!`, drop indentation, collapse intra-line whitespace, **keep every newline** so ASI is never altered; string/template/regex tokens emitted verbatim). `MinifyFile(*ast.File, ext)` walks the AST and minifies each `<script>` element's static `Text` (holeless — `<script>` interpolation does not exist yet, so `ext` always receives complete JS). Wired as a second pre-pass in `generateFile` next to `cssmin.MinifyFile`; `gen.WithJSMinifier` is threaded exactly like `WithCSSMinifier`.

**Tech Stack:** Go; new dep `github.com/tdewolff/parse/v2` (MIT, stdlib-only). Files: `internal/jsmin`, `internal/codegen/{emit.go,batch.go,codegen.go,version.go}`, `gen/{options.go,main.go,gen.go,cache.go}`.

## Global Constraints

- **New core dep: `github.com/tdewolff/parse/v2` only** (codegen-time; the runtime `gsx` package stays stdlib-only and untouched). Added via a real import in `internal/jsmin` + `go mod tidy`.
- **`internal/jsmin` is codegen-time** (may import tdewolff). It must NEVER be imported by the runtime root package.
- **Safe minify only — never change behavior.** Strip comments (keep `/*! … */`), drop indentation + collapse intra-line whitespace, but **KEEP every newline** (a dropped newline can change semantics via ASI). NO identifier mangling, NO value rewrites, NO statement reordering — those are the aggressive tier behind `WithJSMinifier`.
- **Never fuse tokens.** Because a single space or a newline always separates two same-line tokens (we only *collapse* whitespace, never delete it between tokens), `return x` never becomes `returnx`.
- **Preserve verbatim:** `StringToken`, `TemplateToken`/`TemplateStart/Middle/EndToken`, `RegExpToken`, and `/*! … */` bang comments.
- **`<script>` is always holeless** in this slice (no `<script>` interpolation yet) — so the pluggable minifier always receives complete JS, and there is no hole/sentinel logic.
- **On by default; codegen-output only** (`gsx fmt`/source untouched). Bump `codegen.Version()` so the incremental cache invalidates.
- After each task: `go build ./...` and `go test ./...` pass before committing.

---

### Task 1: `internal/jsmin` — the safe JS minifier (`minifyJS`)

**Files:**
- Create: `internal/jsmin/jsmin.go`
- Test: `internal/jsmin/jsmin_test.go`
- Modify: `go.mod`, `go.sum` (via `go mod tidy` once the import exists)

**Interfaces:**
- Produces: `func minifyJS(s string) string` (unexported).
- Consumes: `github.com/tdewolff/parse/v2` (`NewInputString`) + `github.com/tdewolff/parse/v2/js` (`NewLexer`, `Next`, token types).

- [ ] **Step 1: Write the failing test** — `internal/jsmin/jsmin_test.go`:

```go
package jsmin

import "testing"

func TestMinifyJS(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"drop indentation, keep newline", "function f() {\n\treturn 1;\n}", "function f() {\nreturn 1;\n}"},
		{"collapse intra-line spaces", "let   x   =   1", "let x = 1"},
		{"strip line comment, keep its newline (ASI)", "a()\n// note\nb()", "a()\n\nb()"},
		{"strip block comment", "a/* note */b", "a b"},
		{"keep bang comment", "/*! keep */\nx", "/*! keep */\nx"},
		{"string interior verbatim", `let s = "a  b\t c"`, `let s = "a  b\t c"`},
		{"template interior verbatim", "let s = `a  ${ x }  b`", "let s = `a  ${ x }  b`"},
		{"regex interior verbatim", "let r = /a  b/g", "let r = /a  b/g"},
		{"ASI newline preserved", "return\nx", "return\nx"},
		{"no token fusion", "return  x", "return x"},
		{"collapse blank lines", "a()\n\n\n\nb()", "a()\nb()"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		if got := minifyJS(tt.in); got != tt.want {
			t.Errorf("%s: minifyJS(%q) = %q, want %q", tt.name, tt.in, got, tt.want)
		}
	}
}

func TestMinifyJSIdempotent(t *testing.T) {
	for _, in := range []string{
		"function f(){\n\treturn 1\n}", "let x = `a ${y} b`", "a/* c */b", "/*! k */\nx",
		"let r = /a b/; let q = a / b", "return\nx",
	} {
		once := minifyJS(in)
		if twice := minifyJS(once); twice != once {
			t.Errorf("not idempotent: minifyJS(%q)=%q, again=%q", in, once, twice)
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/jsmin/`
Expected: FAIL — `package internal/jsmin` does not exist / `undefined: minifyJS`.

- [ ] **Step 3: Implement `internal/jsmin/jsmin.go`**

```go
// Package jsmin is gsx's codegen-time safe JS minifier: a tdewolff-lexer-driven
// pass over the static JS of <script> blocks. It strips comments and indentation
// and collapses intra-line whitespace, but KEEPS every newline so automatic
// semicolon insertion is never altered — it performs no value rewrites and never
// fuses tokens. String/template/regex literals and /*! … */ bang comments are
// emitted verbatim.
package jsmin

import (
	"strings"

	"github.com/tdewolff/parse/v2"
	"github.com/tdewolff/parse/v2/js"
)

// minifyJS applies the safe-minification set to a complete JS string. It is
// driven by tdewolff's JS lexer (correct strings/templates/regex/ASI), so no
// hand-rolled regex-vs-divide heuristic is needed.
func minifyJS(s string) string {
	if s == "" {
		return ""
	}
	l := js.NewLexer(parse.NewInputString(s))
	var out strings.Builder
	out.Grow(len(s))
	pendingSpace := false   // collapsed intra-line whitespace, not yet emitted
	pendingNewline := false // a newline (ASI-significant) seen, not yet emitted

	flush := func() {
		// Newline wins over space; both are dropped at the very start (out empty).
		if pendingNewline {
			if out.Len() > 0 {
				out.WriteByte('\n')
			}
		} else if pendingSpace {
			if out.Len() > 0 {
				out.WriteByte(' ')
			}
		}
		pendingSpace, pendingNewline = false, false
	}

	for {
		tt, data := l.Next()
		if tt == js.ErrorToken {
			break // EOF (or lex error — emit what we have)
		}
		switch tt {
		case js.WhitespaceToken:
			pendingSpace = true
		case js.LineTerminatorToken:
			pendingNewline = true // ASI-significant; collapse runs to one
		case js.CommentToken:
			// /* … */ block comment.
			if strings.HasPrefix(string(data), "/*!") {
				flush()
				out.Write(data) // bang comment: keep verbatim
			} else if bytesContainNewline(data) {
				pendingNewline = true // a removed multi-line comment is a line terminator
			} else {
				pendingSpace = true // a removed comment still separates tokens
			}
		case js.CommentLineTerminatorToken:
			// // … <newline> : strip the comment but KEEP the newline (ASI).
			pendingNewline = true
		default:
			// Identifier / keyword / punctuator / number / string / template /
			// regex — all emitted verbatim with their interior intact.
			flush()
			out.Write(data)
		}
	}
	return out.String()
}

func bytesContainNewline(b []byte) bool {
	for _, c := range b {
		if c == '\n' || c == '\r' {
			return true
		}
	}
	return false
}
```

Note on regex: tdewolff's `Next()` returns `DivToken`/`DivEqToken` for `/` and only re-lexes a regex when the caller invokes `l.RegExp()`. For *minification* we never need to distinguish — a `RegExpToken` (when the lexer already knows it is a regex from context) is emitted verbatim, and a `DivToken` is just a punctuator emitted verbatim too; either way the bytes are preserved. (The context engine in slice C1 is where `RegExp()` matters.) If a real regex literal with interior whitespace ever lexes as `Div … Div`, the interior tokens are still emitted verbatim with single-space collapse — confirm the `regex interior verbatim` test passes; if it does not, the implementer must drive `l.RegExp()` after a `DivToken` in expression position (report this as a finding rather than silently shipping a regex-mangling minifier).

- [ ] **Step 4: Add the dep + run the tests**

Run:
```bash
go mod tidy
go test ./internal/jsmin/
```
Expected: `go.mod` now requires `github.com/tdewolff/parse/v2`; both tests PASS. If `regex interior verbatim` fails, see the Step-3 regex note — report it.

- [ ] **Step 5: Commit**

```bash
git add internal/jsmin/jsmin.go internal/jsmin/jsmin_test.go go.mod go.sum
git commit -m "jsmin: safe tdewolff-lexer JS minifier (comments/whitespace only, ASI-preserving)"
```

---

### Task 2: `jsmin.MinifyFile` + fuzz

**Files:**
- Create: `internal/jsmin/file.go`
- Test: `internal/jsmin/file_test.go`, `internal/jsmin/fuzz_test.go`

**Interfaces:**
- Consumes: `minifyJS` (Task 1); `github.com/gsxhq/gsx/ast`.
- Produces: `func MinifyFile(f *ast.File, ext func(string) (string, error)) error`.

- [ ] **Step 1: Write the failing test** — `internal/jsmin/file_test.go`:

```go
package jsmin

import (
	"testing"

	"github.com/gsxhq/gsx/ast"
)

func scriptEl(text string) *ast.Element {
	return &ast.Element{Tag: "script", Children: []ast.Markup{&ast.Text{Value: text}}}
}
func fileWith(el *ast.Element) *ast.File {
	return &ast.File{Decls: []ast.Decl{&ast.Component{Name: "C", Body: []ast.Markup{el}}}}
}

func TestMinifyFileScript(t *testing.T) {
	f := fileWith(scriptEl("function f() {\n\treturn 1;\n}"))
	if err := MinifyFile(f, nil); err != nil {
		t.Fatal(err)
	}
	ch := f.Decls[0].(*ast.Component).Body[0].(*ast.Element).Children
	if len(ch) != 1 || ch[0].(*ast.Text).Value != "function f() {\nreturn 1;\n}" {
		t.Fatalf("minified = %#v", ch)
	}
}

func TestMinifyFileExt(t *testing.T) {
	ext := func(js string) (string, error) { return "EXT", nil }
	f := fileWith(scriptEl("var x=1"))
	if err := MinifyFile(f, ext); err != nil {
		t.Fatal(err)
	}
	ch := f.Decls[0].(*ast.Component).Body[0].(*ast.Element).Children
	if ch[0].(*ast.Text).Value != "EXT" {
		t.Fatalf("ext not applied: %#v", ch)
	}
}

func TestMinifyFileLeavesStyleAlone(t *testing.T) {
	f := fileWith(&ast.Element{Tag: "style", Children: []ast.Markup{&ast.Text{Value: "  .a { x: 1 }  "}}})
	if err := MinifyFile(f, nil); err != nil {
		t.Fatal(err)
	}
	ch := f.Decls[0].(*ast.Component).Body[0].(*ast.Element).Children
	if ch[0].(*ast.Text).Value != "  .a { x: 1 }  " {
		t.Fatalf("jsmin must not touch <style>: %q", ch[0].(*ast.Text).Value)
	}
}
```

- [ ] **Step 2: Run to verify it fails** — `go test ./internal/jsmin/ -run TestMinifyFile` → FAIL (`undefined: MinifyFile`).

- [ ] **Step 3: Implement `internal/jsmin/file.go`** — mirror `internal/cssmin/file.go`'s recursion, but match `<script>` (not `<style>`), and since `<script>` is always holeless, no sentinel logic:

```go
package jsmin

import (
	"fmt"
	"strings"

	"github.com/gsxhq/gsx/ast"
)

// MinifyFile minifies the static JS of every <script> element in f, in place.
// ext, if non-nil, minifies the script's JS (the pluggable extension point); a
// nil ext uses the built-in safe minifier. <script> content is always holeless
// (no <script> interpolation yet), so the whole body is one complete JS string.
func MinifyFile(f *ast.File, ext func(string) (string, error)) error {
	for _, d := range f.Decls {
		if comp, ok := d.(*ast.Component); ok {
			if err := minifyMarkup(comp.Body, ext); err != nil {
				return err
			}
		}
	}
	return nil
}

func minifyMarkup(nodes []ast.Markup, ext func(string) (string, error)) error {
	for _, n := range nodes {
		switch v := n.(type) {
		case *ast.Element:
			if strings.EqualFold(v.Tag, "script") {
				mc, err := minifyScriptChildren(v.Children, ext)
				if err != nil {
					return err
				}
				v.Children = mc
				continue
			}
			if err := minifyMarkup(v.Children, ext); err != nil {
				return err
			}
		case *ast.Fragment:
			if err := minifyMarkup(v.Children, ext); err != nil {
				return err
			}
		case *ast.IfMarkup:
			if err := minifyMarkup(v.Then, ext); err != nil {
				return err
			}
			if err := minifyMarkup(v.Else, ext); err != nil {
				return err
			}
		case *ast.ForMarkup:
			if err := minifyMarkup(v.Body, ext); err != nil {
				return err
			}
		case *ast.SwitchMarkup:
			for i := range v.Cases {
				if err := minifyMarkup(v.Cases[i].Body, ext); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func minifyScriptChildren(children []ast.Markup, ext func(string) (string, error)) ([]ast.Markup, error) {
	var sb strings.Builder
	for _, c := range children {
		if t, ok := c.(*ast.Text); ok {
			sb.WriteString(t.Value)
		}
	}
	src := sb.String()
	var min string
	if ext != nil {
		m, err := ext(src)
		if err != nil {
			return nil, fmt.Errorf("jsmin: external JS minifier: %w", err)
		}
		min = m
	} else {
		min = minifyJS(src)
	}
	if min == "" {
		return nil, nil
	}
	return []ast.Markup{&ast.Text{Value: min}}, nil
}
```

(Verify `ast` field names — `Component.Body`, `IfMarkup.Then/Else`, `ForMarkup.Body`, `SwitchMarkup.Cases[].Body` — against `ast/ast.go`; they match `internal/cssmin/file.go`.)

- [ ] **Step 4: Add the fuzzer** — `internal/jsmin/fuzz_test.go`:

```go
package jsmin

import "testing"

// FuzzMinifyJS asserts robustness: never panics, idempotent. minifyJS is a
// formatter (not a security boundary), so idempotence + no-panic is the property.
func FuzzMinifyJS(f *testing.F) {
	for _, s := range []string{
		"", "function f(){return 1}", "let x = `a ${b} c`", "a/* c */b", "/*! k */\nx",
		"let r=/a b/g", "return\nx", "a //x\nb", "`${`${x}`}`", "var x", "x=>x",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		once := minifyJS(s)
		if twice := minifyJS(once); twice != once {
			t.Fatalf("not idempotent:\n in=%q\n once=%q\n twice=%q", s, once, twice)
		}
	})
}
```

- [ ] **Step 5: Run tests + a bounded fuzz session**

Run:
```bash
go test ./internal/jsmin/
go test ./internal/jsmin/ -run x -fuzz 'FuzzMinifyJS$' -fuzztime 30s
```
Expected: all green; the fuzz session finds no crasher (no panic, idempotent). If a crasher appears, STOP and report the input (do not weaken the invariant).

- [ ] **Step 6: Commit**

```bash
git add internal/jsmin/file.go internal/jsmin/file_test.go internal/jsmin/fuzz_test.go
git commit -m "jsmin: MinifyFile (<script> holeless minify) + fuzz (no-panic/idempotent)"
```

---

### Task 3: Wire JS minify into codegen + `gen.WithJSMinifier` + Version bump

**Files:**
- Modify: `internal/codegen/emit.go` (`generateFile`: add the `jsmin.MinifyFile` pre-pass + param), `internal/codegen/version.go` (bump), `internal/codegen/batch.go` + `internal/codegen/codegen.go` (thread `jsMin`)
- Modify: `gen/options.go` (`WithJSMinifier`), `gen/main.go` (config field + `runGenerate`), `gen/gen.go` (`generate`), `gen/cache.go` (`generateCached`/`mustGen` + bypass)
- Test: `internal/corpus/testdata/cases/script/minify.txtar` (new); `gen/options_test.go` (append `WithJSMinifier` test)

**Interfaces:**
- Consumes: `jsmin.MinifyFile` (Task 2); the slice-2 `cssMin` threading as the exact template.
- Produces: `generateFile(..., cssMin, jsMin func(string)(string,error))`; `gen.WithJSMinifier(func(js string)(string,error)) Option`.

- [ ] **Step 1: Add the new corpus case** — `internal/corpus/testdata/cases/script/minify.txtar`:

```
-- input.gsx --
package views

component Page() {
	<script>
		function init() {
			return 1;
		}
	</script>
}
-- invoke --
Page(PageProps{})
-- generated.x.go.golden --
PLACEHOLDER
```

- [ ] **Step 2: Run to verify it fails / shows un-minified output** — `go test ./internal/corpus -run TestCorpus` → the new case fails (no golden); the eventual golden currently bakes the indented `<script>` verbatim, proving JS minify is not yet active.

- [ ] **Step 3: Thread `jsMin` exactly like `cssMin`.** This is the mechanical twin of slice 2's `WithCSSMinifier` threading. In each location where `cssMin` appears, add a parallel `jsMin func(string)(string,error)` as the next parameter / config field, and pass `nil` for it where `cssMin` is passed `nil`:
  - `gen/main.go`: add `jsMin func(string)(string,error)` to `config`; `runGenerate` gains it and forwards it; the dispatch call appends `cfg.jsMin`; `useCache := !nocacheFlag && cssMin == nil && jsMin == nil`.
  - `gen/options.go`: add
    ```go
    // WithJSMinifier installs a custom JS minifier for <script> blocks, replacing
    // the built-in safe minifier. It receives complete JS (<script> is holeless).
    func WithJSMinifier(min func(js string) (string, error)) Option {
        return func(cfg *config) { cfg.jsMin = min }
    }
    ```
  - `gen/gen.go`: `generate` gains `jsMin`; the public `Generate(paths)` passes `nil, nil`; `generateCached(paths, filterPkgs, cssMin==nil && jsMin==nil, cssMin, jsMin)`.
  - `gen/cache.go`: `generateCached` + `mustGen` gain `jsMin`; forward to `GeneratePackagesWithFilters`.
  - `internal/codegen/batch.go`: `GeneratePackagesWithFilters` gains `jsMin`, forwarded to `generateFile`; `GeneratePackages` passes `nil, nil`.
  - `internal/codegen/codegen.go`: `GeneratePackageWithFilters` gains `jsMin`; `GeneratePackage` passes `nil, nil`.
  - `internal/codegen/emit.go` `generateFile`: add the param and the pre-pass right after the cssmin one:
    ```go
    func generateFile(file *ast.File, resolved map[ast.Node]types.Type, table filterTable, structFields map[string]map[string]bool, fset *token.FileSet, cssMin, jsMin func(string) (string, error)) ([]byte, error) {
        interpTemp = 0
        if err := cssmin.MinifyFile(file, cssMin); err != nil {
            return nil, err
        }
        if err := jsmin.MinifyFile(file, jsMin); err != nil {
            return nil, err
        }
        ...
    ```
    Add `"github.com/gsxhq/gsx/internal/jsmin"` to the import block.
  - Then `grep -rn 'generateFile(\|GeneratePackagesWithFilters(\|GeneratePackageWithFilters(\|generateCached(\|mustGen(\|generate(' --include='*.go' .` and add the trailing `nil` (or `jsMin`) to every remaining call site, including tests.

- [ ] **Step 4: Bump the cache version** — `internal/codegen/version.go`: change `const version = "2"` to `const version = "3"`.

- [ ] **Step 5: Add the WithJSMinifier test** — append to `gen/options_test.go`:

```go
func TestWithJSMinifierOption(t *testing.T) {
	min := func(js string) (string, error) { return js, nil }
	var cfg config
	WithJSMinifier(min)(&cfg)
	if cfg.jsMin == nil {
		t.Fatal("WithJSMinifier did not set cfg.jsMin")
	}
}
```

- [ ] **Step 6: Build, regenerate goldens, verify**

Run:
```bash
go build ./...
go test ./internal/corpus -run TestCorpus -update
go test ./...
```
Inspect `internal/corpus/testdata/cases/script/minify.txtar`'s `generated.x.go.golden`: the `<script>` static write must now be minified — `_gsxgw.S("<script>function init() {\nreturn 1;\n}</script>")` (indentation dropped, newlines kept) — NOT the original indented JS. Confirm no other corpus golden changed except `<script>`-bearing ones (run `git diff --stat internal/corpus/testdata`). All packages green.

- [ ] **Step 7: Commit**

```bash
git add internal/codegen/ gen/ internal/corpus/testdata
git commit -m "codegen: minify <script> JS at codegen time (built-in safe, on by default) + WithJSMinifier; bump cache version"
```

---

## Self-Review

**Spec coverage (Slice B):** `tdewolff/parse` dep (Task 1) ✓; safe built-in JS minify keeping ASI newlines (Task 1) ✓; `MinifyFile` holeless `<script>` + fuzz (Task 2) ✓; generateFile pre-pass + `WithJSMinifier` seam + cache-bypass + Version bump (Task 3) ✓. CSS untouched (`TestMinifyFileLeavesStyleAlone`) ✓.

**Placeholder scan:** the only `PLACEHOLDER` is the `-update`-generated golden (Task 3 Step 1, by design). Every code step shows complete code.

**Type/name consistency:** `minifyJS(string) string`, `MinifyFile(*ast.File, func(string)(string,error)) error`, `WithJSMinifier(func(js string)(string,error)) Option`, `cfg.jsMin`, `generateFile(..., cssMin, jsMin …)`, `GeneratePackagesWithFilters(…, jsMin)` — consistent across tasks; the `jsMin` param mirrors `cssMin` everywhere.
