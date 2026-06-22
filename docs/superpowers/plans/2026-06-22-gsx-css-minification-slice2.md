# gsx CSS Minification — Slice 2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Minify the static CSS of every `<style>` block at codegen time with a robust, stable, stdlib-only built-in minifier (on by default), plus a `gen.WithCSSMinifier` extension point for swapping in an aggressive minifier on holeless blocks.

**Architecture:** A new `internal/cssmin` package: `minifyCSS(string) string` is a real CSS-aware char scanner doing only guaranteed-safe transforms (strip comments keeping `/*!`, collapse whitespace runs to one space, remove whitespace around `{ } ; ,`, drop `;` before `}`, trim edges; preserve string/`url()` interiors). `MinifyFile(*ast.File, ext)` walks the AST and replaces each `<style>`'s children with their minified form — a holeless block uses `ext` (or the built-in if `ext` is nil); a block with `${ }` interpolation always uses the built-in via an opaque-sentinel pass that preserves hole-adjacent whitespace and the `*ast.Interp` pointers. It runs as a pre-pass in `generateFile`. The `gen.WithCSSMinifier` option threads the external minifier from the gen config down to `generateFile`.

**Tech Stack:** Go, stdlib only (the minifier is codegen-time but written stdlib-only). Existing packages: `internal/codegen`, `ast`, `gen`, `internal/corpus`.

## Global Constraints

- **`internal/cssmin` is stdlib-only.** No third-party imports.
- **Robust & stable: NO value rewrites.** The built-in performs ONLY whitespace/comment transforms. It must NEVER do `0px`→`0`, color shortening, longhand→shorthand, dedup, or any rule merging/reordering — those belong to a user-supplied `WithCSSMinifier`. It must be a real char scanner, **never regex**.
- **Never remove a significant space.** Collapsing a whitespace *run* to a single space is the only whitespace reduction in non-delimiter positions, so descendant combinators (`a b`), value separators (`1px 2px`), and `calc()`/`min`/`max`/`clamp` operators (`50% - 8px`) keep exactly one space. Whitespace is *removed* only immediately adjacent to `{ } ; ,`.
- **Hole-aware:** an `${ }` interpolation (`*ast.Interp`) is opaque; whitespace adjacent to it is collapsed to a single space but NEVER removed, and nothing merges across it. The original `*ast.Interp` pointers are reused (codegen's `resolved` map is keyed by them).
- **Preserve verbatim:** string literals (`"…"`/`'…'`), `url(…)` interiors, and `/*! … */` bang comments.
- **On by default; codegen-output only.** `gsx fmt` and `.gsx` source are untouched — minification happens only in `generateFile`.
- **Scope:** `<style>` only. `<script>`/JS minification is slice 3.
- After each task: `go build ./...` and `go test ./...` pass before committing.

---

### Task 1: `internal/cssmin` — the safe CSS minifier (`minifyCSS`)

**Files:**
- Create: `internal/cssmin/cssmin.go`
- Test: `internal/cssmin/cssmin_test.go`

**Interfaces:**
- Produces: `func minifyCSS(s string) string` (unexported), `func isURLOpen(s string, i int) bool`, `func isNameByte(c byte) bool`.
- Consumes: stdlib `strings` only.

- [ ] **Step 1: Write the failing test** — `internal/cssmin/cssmin_test.go`:

```go
package cssmin

import "testing"

func TestMinifyCSS(t *testing.T) {
	tests := []struct{ name, in, want string }{
		// --- the safe transforms ---
		{"collapse+trim", "  .a  {\n\tcolor:  red;\n}  ", ".a{color: red}"},
		{"strip comment", "a/* x */b", "a b"},
		{"keep bang comment", "/*! keep */\n.a{color:red}", "/*! keep */.a{color:red}"},
		{"drop semi before brace", ".a{color:red;}", ".a{color:red}"},
		{"ws around delimiters", ".a , .b { x:1 ; y:2 }", ".a,.b{x:1;y:2}"},
		{"comment between idents keeps separation", "a/**/b", "a b"},
		// --- must NOT break (historical naive-minifier breakages) ---
		{"calc spacing", ".a{width:calc(100% - 8px)}", ".a{width:calc(100% - 8px)}"},
		{"descendant combinator", ".a   .b{x:1}", ".a .b{x:1}"},
		{"value separators", ".a{margin:1px   2px   3px}", ".a{margin:1px 2px 3px}"},
		{"string interior", ".a{content:\"  a  b  \"}", ".a{content:\"  a  b  \"}"},
		{"string with brace/semicolon", ".a{content:\"x}y;z\"}", ".a{content:\"x}y;z\"}"},
		{"url unquoted spaces", ".a{background:url(data:image/svg+xml,<svg viewBox=\"0 0 8 8\">)}", ".a{background:url(data:image/svg+xml,<svg viewBox=\"0 0 8 8\">)}"},
		{"url quoted + format", "@font-face{src:url(f.woff2) format(\"woff2\")}", "@font-face{src:url(f.woff2) format(\"woff2\")}"},
		{"media and", "@media (min-width:30px) and (max-width:50px){.a{x:1}}", "@media (min-width:30px) and (max-width:50px){.a{x:1}}"},
		{"grid-template-areas", ".g{grid-template-areas:\"a a\" \"b c\"}", ".g{grid-template-areas:\"a a\" \"b c\"}"},
		{"ie star hack", ".a{*zoom:1;_height:1px}", ".a{*zoom:1;_height:1px}"},
		{"An+B", ".a:nth-child(2n + 1){x:1}", ".a:nth-child(2n + 1){x:1}"},
		{"unicode-range", "@font-face{unicode-range:U+0000-00FF, U+0131}", "@font-face{unicode-range:U+0000-00FF,U+0131}"},
		{"empty", "", ""},
		{"only comment", "/* gone */", ""},
	}
	for _, tt := range tests {
		if got := minifyCSS(tt.in); got != tt.want {
			t.Errorf("%s: minifyCSS(%q) = %q, want %q", tt.name, tt.in, got, tt.want)
		}
	}
}

func TestMinifyCSSIdempotent(t *testing.T) {
	for _, in := range []string{
		".a{color:red}", ".a , .b { x:1 ; }", "@media (a) and (b){.x{y:1}}",
		".a{width:calc(100% - 8px)}", ".a{content:\"  \"}", "/*! k */.a{x:1}",
	} {
		once := minifyCSS(in)
		if twice := minifyCSS(once); twice != once {
			t.Errorf("not idempotent: minifyCSS(%q)=%q, again=%q", in, once, twice)
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/cssmin/`
Expected: FAIL — `undefined: minifyCSS`.

- [ ] **Step 3: Implement `internal/cssmin/cssmin.go`**

```go
// Package cssmin is gsx's codegen-time CSS minifier: a robust, stable, safe pass
// over the static CSS of <style> blocks. It performs only whitespace/comment
// reductions that cannot change rendering — never value rewrites — and is
// hole-aware (an ${ } interpolation is opaque, its adjacent whitespace preserved).
package cssmin

import "strings"

// minifyCSS applies the safe-minification set to a complete CSS string: strips
// comments (keeping /*! … */), collapses each whitespace run to one space,
// removes whitespace adjacent to { } ; , drops a ; immediately before }, and
// trims the edges. String literals and url(…) interiors are preserved verbatim.
// It never rewrites values and never removes a space that could be significant.
func minifyCSS(s string) string {
	out := make([]byte, 0, len(s))
	pending := false // an unemitted whitespace run

	isDelim := func(c byte) bool { return c == '{' || c == '}' || c == ';' || c == ',' }
	flush := func(cur byte) {
		if !pending {
			return
		}
		pending = false
		if len(out) == 0 {
			return // leading whitespace: drop
		}
		if isDelim(out[len(out)-1]) || isDelim(cur) {
			return // adjacent to a delimiter: drop
		}
		out = append(out, ' ')
	}

	i, n := 0, len(s)
	for i < n {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f':
			pending = true
			i++
		case c == '/' && i+1 < n && s[i+1] == '*':
			closeIdx := strings.Index(s[i+2:], "*/")
			end := n
			if closeIdx >= 0 {
				end = i + 2 + closeIdx + 2
			}
			if i+2 < n && s[i+2] == '!' {
				flush('/')
				out = append(out, s[i:end]...) // keep /*! … */ verbatim
			} else {
				pending = true // a removed comment still separates tokens
			}
			i = end
		case c == '"' || c == '\'':
			flush(c)
			out = append(out, c)
			i++
			for i < n {
				out = append(out, s[i])
				if s[i] == '\\' && i+1 < n {
					out = append(out, s[i+1])
					i += 2
					continue
				}
				closed := s[i] == c
				i++
				if closed {
					break
				}
			}
		case (c == 'u' || c == 'U') && isURLOpen(s, i):
			flush(c)
			out = append(out, s[i:i+4]...) // "url("
			i += 4
			j := i
			for j < n && isSpace(s[j]) {
				j++
			}
			if j < n && (s[j] == '"' || s[j] == '\'') {
				continue // quoted url: main loop handles the string + ')'
			}
			for i < n && s[i] != ')' { // unquoted: copy interior verbatim
				out = append(out, s[i])
				i++
			}
			if i < n {
				out = append(out, ')')
				i++
			}
		default:
			flush(c)
			if c == '}' && len(out) > 0 && out[len(out)-1] == ';' {
				out = out[:len(out)-1] // drop a ; immediately before }
			}
			out = append(out, c)
			i++
		}
	}
	return string(out)
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f'
}

// isURLOpen reports whether s[i:] begins a url( function token: "url(" (case-
// insensitive) whose "url" is not the tail of a longer identifier.
func isURLOpen(s string, i int) bool {
	if i+4 > len(s) || !strings.EqualFold(s[i:i+4], "url(") {
		return false
	}
	return i == 0 || !isNameByte(s[i-1])
}

// isNameByte reports whether c can be part of a CSS identifier.
func isNameByte(c byte) bool {
	return c == '-' || c == '_' ||
		'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' ||
		'0' <= c && c <= '9' || c >= 0x80
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/cssmin/`
Expected: PASS (both `TestMinifyCSS` and `TestMinifyCSSIdempotent`).

- [ ] **Step 5: Commit**

```bash
git add internal/cssmin/cssmin.go internal/cssmin/cssmin_test.go
git commit -m "cssmin: safe stdlib CSS minifier (whitespace/comments only, no value rewrites)"
```

---

### Task 2: `cssmin.MinifyFile` — hole-aware AST integration

**Files:**
- Create: `internal/cssmin/file.go`
- Test: `internal/cssmin/file_test.go`

**Interfaces:**
- Consumes: `minifyCSS` (Task 1); `github.com/gsxhq/gsx/ast` (`*ast.File`, `*ast.Component`, `*ast.Element`, `*ast.Text`, `*ast.Interp`, `*ast.Fragment`, `*ast.IfMarkup`, `*ast.ForMarkup`, `*ast.SwitchMarkup`, `ast.Markup`).
- Produces: `func MinifyFile(f *ast.File, ext func(string) (string, error)) error`.

- [ ] **Step 1: Write the failing test** — `internal/cssmin/file_test.go`:

```go
package cssmin

import (
	"strings"
	"testing"

	"github.com/gsxhq/gsx/ast"
)

func styleEl(children ...ast.Markup) *ast.Element {
	return &ast.Element{Tag: "style", Children: children}
}
func fileWith(el *ast.Element) *ast.File {
	return &ast.File{Decls: []ast.Decl{&ast.Component{Name: "C", Body: []ast.Markup{el}}}}
}
func styleChildren(f *ast.File) []ast.Markup {
	return f.Decls[0].(*ast.Component).Body[0].(*ast.Element).Children
}

func TestMinifyFileHoleless(t *testing.T) {
	f := fileWith(styleEl(&ast.Text{Value: "  .a {\n  color: red;\n}  "}))
	if err := MinifyFile(f, nil); err != nil {
		t.Fatal(err)
	}
	ch := styleChildren(f)
	if len(ch) != 1 {
		t.Fatalf("got %d children, want 1", len(ch))
	}
	if got := ch[0].(*ast.Text).Value; got != ".a{color: red}" {
		t.Fatalf("minified = %q", got)
	}
}

func TestMinifyFileHoleyPreservesInterpAndAdjacentSpace(t *testing.T) {
	in := &ast.Interp{Expr: "a"}
	in2 := &ast.Interp{Expr: "b"}
	// "margin:  " <a> " " <b> "  ;"  -> margin and one space between the two holes
	f := fileWith(styleEl(
		&ast.Text{Value: ".x{margin:  "}, in, &ast.Text{Value: " "}, in2, &ast.Text{Value: "  ;}"},
	))
	if err := MinifyFile(f, nil); err != nil {
		t.Fatal(err)
	}
	ch := styleChildren(f)
	// The two interps survive (same pointers) with exactly one space between them.
	var interps []*ast.Interp
	var sb strings.Builder
	for _, c := range ch {
		switch v := c.(type) {
		case *ast.Interp:
			interps = append(interps, v)
			sb.WriteString("\x00")
		case *ast.Text:
			sb.WriteString(v.Value)
		}
	}
	if len(interps) != 2 || interps[0] != in || interps[1] != in2 {
		t.Fatalf("interp pointers not preserved: %#v", interps)
	}
	if got := sb.String(); got != ".x{margin: \x00 \x00}" {
		t.Fatalf("layout = %q, want %q", got, ".x{margin: \x00 \x00}")
	}
}

func TestMinifyFileExtHolelessOnly(t *testing.T) {
	ext := func(css string) (string, error) { return "EXT", nil }
	// Holeless -> ext is used.
	f := fileWith(styleEl(&ast.Text{Value: ".a{x:1}"}))
	if err := MinifyFile(f, ext); err != nil {
		t.Fatal(err)
	}
	if got := styleChildren(f)[0].(*ast.Text).Value; got != "EXT" {
		t.Fatalf("holeless ext = %q, want EXT", got)
	}
	// Holey -> ext is NOT used (built-in keeps the interp).
	in := &ast.Interp{Expr: "a"}
	f2 := fileWith(styleEl(&ast.Text{Value: ".a{x:"}, in, &ast.Text{Value: "}"}))
	if err := MinifyFile(f2, ext); err != nil {
		t.Fatal(err)
	}
	saw := false
	for _, c := range styleChildren(f2) {
		if _, ok := c.(*ast.Interp); ok {
			saw = true
		}
		if t2, ok := c.(*ast.Text); ok && strings.Contains(t2.Value, "EXT") {
			t.Fatal("ext was applied to a holey block")
		}
	}
	if !saw {
		t.Fatal("holey block lost its interp")
	}
}
```

(If `ast.File`/`ast.Decl`/`ast.Component` field names differ from `Decls`/`Name`/`Body`, adjust the helpers to the real names — check `ast/ast.go` first; `ast.Component` is the function/method-component decl with a `Body []ast.Markup`.)

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/cssmin/ -run TestMinifyFile`
Expected: FAIL — `undefined: MinifyFile`.

- [ ] **Step 3: Implement `internal/cssmin/file.go`**

```go
package cssmin

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gsxhq/gsx/ast"
)

// MinifyFile minifies the static CSS of every <style> element in f, in place.
// ext, if non-nil, minifies the full CSS of a HOLELESS <style> block (the
// pluggable extension point); a block containing ${ } interpolation always uses
// the built-in hole-aware minifier, because an external string->string minifier
// cannot reason across holes. A nil ext uses the built-in for every block.
func MinifyFile(f *ast.File, ext func(string) (string, error)) error {
	for _, d := range f.Decls {
		comp, ok := d.(*ast.Component)
		if !ok {
			continue
		}
		if err := minifyMarkup(comp.Body, ext); err != nil {
			return err
		}
	}
	return nil
}

// minifyMarkup recurses, replacing each <style> element's children in place.
func minifyMarkup(nodes []ast.Markup, ext func(string) (string, error)) error {
	for _, n := range nodes {
		switch v := n.(type) {
		case *ast.Element:
			if strings.EqualFold(v.Tag, "style") {
				mc, err := minifyStyleChildren(v.Children, ext)
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

// minifyStyleChildren returns the minified replacement for a <style> body. A
// holeless body is minified as one CSS string (via ext or the built-in). A body
// with interps is minified via an opaque sentinel pass that preserves the
// *ast.Interp pointers and their adjacent whitespace.
func minifyStyleChildren(children []ast.Markup, ext func(string) (string, error)) ([]ast.Markup, error) {
	hasInterp := false
	for _, c := range children {
		if _, ok := c.(*ast.Interp); ok {
			hasInterp = true
			break
		}
	}

	if !hasInterp {
		var sb strings.Builder
		for _, c := range children {
			if t, ok := c.(*ast.Text); ok {
				sb.WriteString(t.Value)
			}
		}
		css := sb.String()
		var min string
		if ext != nil {
			m, err := ext(css)
			if err != nil {
				return nil, fmt.Errorf("cssmin: external CSS minifier: %w", err)
			}
			min = m
		} else {
			min = minifyCSS(css)
		}
		if min == "" {
			return nil, nil
		}
		return []ast.Markup{&ast.Text{Value: min}}, nil
	}

	// Holey: replace each interp with a NUL-delimited index sentinel, minify, split
	// back. A NUL byte in the source CSS is pathological — bail to verbatim (no
	// minification) rather than risk a bad split.
	var sb strings.Builder
	var interps []*ast.Interp
	for _, c := range children {
		switch t := c.(type) {
		case *ast.Text:
			if strings.IndexByte(t.Value, 0) >= 0 {
				return children, nil
			}
			sb.WriteString(t.Value)
		case *ast.Interp:
			sb.WriteByte(0)
			sb.WriteString(strconv.Itoa(len(interps)))
			sb.WriteByte(0)
			interps = append(interps, t)
		}
	}
	return splitSentinels(minifyCSS(sb.String()), interps), nil
}

// splitSentinels reassembles a minified sentinel string into Text + Interp nodes.
// Each \x00<digits>\x00 run is replaced by interps[<digits>]; the spans between
// become Text nodes.
func splitSentinels(s string, interps []*ast.Interp) []ast.Markup {
	var out []ast.Markup
	var text strings.Builder
	i, n := 0, len(s)
	for i < n {
		if s[i] == 0 {
			j := i + 1
			for j < n && s[j] >= '0' && s[j] <= '9' {
				j++
			}
			if j < n && s[j] == 0 && j > i+1 {
				idx, _ := strconv.Atoi(s[i+1 : j])
				if text.Len() > 0 {
					out = append(out, &ast.Text{Value: text.String()})
					text.Reset()
				}
				if idx >= 0 && idx < len(interps) {
					out = append(out, interps[idx])
				}
				i = j + 1
				continue
			}
		}
		text.WriteByte(s[i])
		i++
	}
	if text.Len() > 0 {
		out = append(out, &ast.Text{Value: text.String()})
	}
	return out
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/cssmin/`
Expected: PASS (all Task 1 + Task 2 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/cssmin/file.go internal/cssmin/file_test.go
git commit -m "cssmin: MinifyFile — hole-aware <style> minification (ext for holeless, built-in for holey)"
```

---

### Task 3: Wire built-in minification into codegen (on by default)

**Files:**
- Modify: `internal/codegen/emit.go` (`generateFile`, ~line 21 — add the pre-pass + import)
- Modify: `internal/codegen/version.go` (bump the codegen-output version — the incremental cache folds it in)
- Test: `internal/corpus/testdata/cases/style/minify_block.txtar` (new), plus regenerating existing `<style>` goldens.

**Interfaces:**
- Consumes: `cssmin.MinifyFile(*ast.File, func(string)(string,error)) error` (Task 2).
- Produces: `generateFile` minifies `<style>` content (built-in) before emit. No signature change yet.

**Cache note (post-perf):** `gsx generate` is now backed by an incremental cache keyed on `codegen.Version()` (see `internal/codegen/version.go`, `gen/cachekey.go`). Turning minification on changes generated `.x.go` for unchanged input, so this task MUST bump `Version()` — otherwise the cache serves stale, un-minified output. (The corpus harness bypasses the cache, so its goldens regenerate regardless.)

- [ ] **Step 1: Write the failing golden case** — create `internal/corpus/testdata/cases/style/minify_block.txtar`:

```
-- input.gsx --
package views

component Page() {
	<style>
		.card {
			color: red;
			margin: 1px  2px;
		}
	</style>
}
-- invoke --
Page(PageProps{})
-- generated.x.go.golden --
PLACEHOLDER
```

- [ ] **Step 2: Run to verify it fails / shows un-minified output**

Run: `go test ./internal/corpus -run TestCorpus 2>&1 | head -20`
Expected: the new case FAILS the golden compare (no golden yet). Note: the `generated.x.go.golden` (once generated in Step 4) currently bakes the static CSS verbatim — e.g. `_gsxgw.S("<style>\n\t\t.card {\n\t\t\tcolor: red;...")` — proving minification is NOT yet active.

- [ ] **Step 3: Add the pre-pass to `generateFile`** in `internal/codegen/emit.go`. Add the import `"github.com/gsxhq/gsx/internal/cssmin"` to the file's import block, then insert the call as the first statement of `generateFile` (right after `interpTemp = 0`):

```go
func generateFile(file *ast.File, resolved map[ast.Node]types.Type, table filterTable, structFields map[string]map[string]bool, fset *token.FileSet) ([]byte, error) {
	interpTemp = 0
	// Minify the static CSS of <style> blocks (built-in safe minifier; the
	// WithCSSMinifier extension threads an override here in a later task). This
	// mutates only <style> Text nodes — Interp pointers (and so `resolved`) are
	// preserved. fmt does not run this, so source/`gsx fmt` are unaffected.
	if err := cssmin.MinifyFile(file, nil); err != nil {
		return nil, err
	}
	imports := map[string]bool{
```

Then bump `internal/codegen/version.go` so the incremental cache invalidates project-wide (minification changes generated `.x.go` for unchanged input):

```go
const version = "2"
```

- [ ] **Step 4: Regenerate goldens and verify minification is active**

Run: `go test ./internal/corpus -run TestCorpus -update`
Then inspect the new case: `go test ./internal/corpus -run TestCorpus` (must PASS), and confirm `minify_block.txtar`'s `generated.x.go.golden` now contains a single minified static write — `_gsxgw.S("<style>.card{color: red;margin: 1px 2px}</style>")` (indentation + newlines collapsed, `;` before `}` dropped, `margin:` value spaces collapsed to one) — NOT the original multi-line CSS. Replace the PLACEHOLDER by letting `-update` write it.

- [ ] **Step 5: Review the other regenerated goldens**

Run: `git diff --stat internal/corpus/testdata/cases` and skim the changed `<style>` goldens (e.g. `style/block_interpolation.txtar`, `style/block_stringer.txtar`, `codegen-shape/style_interp.txtar`, and any example with a `<style>`). For each, confirm the generated `.x.go.golden` static `<style>` strings are now minified and the `${ }` interps are preserved with sane spacing (e.g. `width: ` before a hole keeps one space). `render.golden` files may be unchanged (the corpus render compare is whitespace-insensitive). This is expected — do NOT revert them.

- [ ] **Step 6: Full suite**

Run: `go build ./... && go test ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/codegen/emit.go internal/codegen/version.go internal/corpus/testdata
git commit -m "codegen: minify <style> CSS at codegen time (built-in safe minifier, on by default); bump cache version"
```

---

### Task 4: `gen.WithCSSMinifier` extension point + threading

**Files:**
- Modify: `gen/main.go` (`config` struct: add `cssMin`; `runGenerate` threads `cfg.cssMin`)
- Modify: `gen/options.go` (add `WithCSSMinifier`)
- Modify: `gen/gen.go` (`generate`/public `Generate` thread `cssMin`)
- Modify: `gen/cache.go` (`generateCached` + `mustGen` thread `cssMin`; bypass cache when `cssMin != nil`)
- Modify: `internal/codegen/batch.go` (`GeneratePackagesWithFilters` + `GeneratePackages` gain the param; the `generateFile` call passes it)
- Modify: `internal/codegen/codegen.go` (`GeneratePackageWithFilters` + `GeneratePackage` gain the param; the `generateFile` call passes it)
- Modify: `internal/codegen/emit.go` (`generateFile` gains the param)
- Test: `gen/options_test.go` (extension-point behavior)

**Interfaces:**
- Consumes: `cssmin.MinifyFile` (Task 2); the `config`/`Option` machinery (`gen.Option = func(*config)`, `config{filterPkgs, cssMin, errs}`).
- Produces: `func WithCSSMinifier(min func(css string) (string, error)) Option`; `generateFile(..., cssMin func(string)(string,error))`; `GeneratePackagesWithFilters(moduleDir, dirs, filterPkgs, cssMin)`; `GeneratePackageWithFilters(dir, filterPkgs, cssMin)`.

**Threading note (post-perf):** `gsx generate` now flows `Main → runGenerate → generate → generateCached → (cache GENERATE phase or mustGen) → codegen.GeneratePackagesWithFilters → generateFile`. The custom minifier is a func (not hashable), so the incremental cache cannot key on it: **when `cssMin != nil`, bypass the cache** (run the no-cache `mustGen` path). The built-in (nil) path keeps the cache (its behavior is pinned by `Version()`, already bumped in Task 3).

- [ ] **Step 1: Write the failing test** — `gen/options_test.go` (create or append). It checks that `WithCSSMinifier` is invoked on a holeless `<style>` block and NOT on a holey one, by generating through the codegen entry point with a sentinel minifier:

```go
package gen

import (
	"strings"
	"testing"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/cssmin"
)

// This is a focused unit test of the threading contract at the cssmin layer:
// the same boundary gen.WithCSSMinifier relies on. (An end-to-end gen test needs
// a temp module; the corpus covers built-in end-to-end. Here we assert the ext
// func reaches holeless blocks only.)
func TestWithCSSMinifierBoundary(t *testing.T) {
	called := false
	ext := func(css string) (string, error) { called = true; return "/*ext*/" + css, nil }

	holeless := &ast.File{Decls: []ast.Decl{&ast.Component{Name: "C", Body: []ast.Markup{
		&ast.Element{Tag: "style", Children: []ast.Markup{&ast.Text{Value: ".a{x:1}"}}},
	}}}}
	if err := cssmin.MinifyFile(holeless, ext); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("ext not called on holeless block")
	}
	got := holeless.Decls[0].(*ast.Component).Body[0].(*ast.Element).Children[0].(*ast.Text).Value
	if !strings.HasPrefix(got, "/*ext*/") {
		t.Fatalf("ext output not used: %q", got)
	}

	called = false
	holey := &ast.File{Decls: []ast.Decl{&ast.Component{Name: "C", Body: []ast.Markup{
		&ast.Element{Tag: "style", Children: []ast.Markup{
			&ast.Text{Value: ".a{x:"}, &ast.Interp{Expr: "v"}, &ast.Text{Value: "}"},
		}},
	}}}}
	if err := cssmin.MinifyFile(holey, ext); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("ext must NOT be called on a holey block")
	}
}

func TestWithCSSMinifierOption(t *testing.T) {
	min := func(css string) (string, error) { return css, nil }
	var cfg config
	WithCSSMinifier(min)(&cfg)
	if cfg.cssMin == nil {
		t.Fatal("WithCSSMinifier did not set cfg.cssMin")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./gen -run 'TestWithCSSMinifier'`
Expected: FAIL — `cfg.cssMin undefined`, `WithCSSMinifier` undefined (the boundary test passes already, since it uses `cssmin.MinifyFile` directly — that's fine; the option test fails).

- [ ] **Step 3: Add the config field + option.** In `gen/main.go`, add to the `config` struct:

```go
type config struct {
	filterPkgs []string
	cssMin     func(string) (string, error)
	errs       []error
}
```

In `gen/options.go`, append:

```go
// WithCSSMinifier installs a custom CSS minifier for <style> blocks, replacing
// the built-in safe minifier on FULLY-STATIC (holeless) blocks. A block that
// contains ${ } interpolation always uses gsx's built-in hole-aware minifier, so
// the custom minifier only ever receives complete, valid CSS. Wrap any
// whole-buffer minifier (e.g. tdewolff) in this signature:
//
//	gen.Main(gen.WithCSSMinifier(func(css string) (string, error) { … }))
func WithCSSMinifier(min func(css string) (string, error)) Option {
	return func(cfg *config) { cfg.cssMin = min }
}
```

- [ ] **Step 4: Thread `cssMin` through codegen.** Add `cssMin func(string) (string, error)` as the FINAL parameter of these functions and forward it down to `generateFile`:

In `internal/codegen/emit.go` — `generateFile` gains the param and uses it in the pre-pass:
```go
func generateFile(file *ast.File, resolved map[ast.Node]types.Type, table filterTable, structFields map[string]map[string]bool, fset *token.FileSet, cssMin func(string) (string, error)) ([]byte, error) {
	interpTemp = 0
	if err := cssmin.MinifyFile(file, cssMin); err != nil {
		return nil, err
	}
```

In `internal/codegen/batch.go`:
- `GeneratePackagesWithFilters` (the Tier-0 batch entry, ~line 32): add the param —
  `func GeneratePackagesWithFilters(moduleDir string, dirs []string, filterPkgs []string, cssMin func(string) (string, error)) (map[string]*PackageResult, error)` — and at its `generateFile(file, resolved, table, pf, fset)` call (~line 235), add `cssMin` as the final arg.
- `GeneratePackages` (~line 249): forward `nil` — `return GeneratePackagesWithFilters(moduleDir, dirs, nil, nil)`.

In `internal/codegen/codegen.go`:
- `GeneratePackageWithFilters` (~line 46): add the param, and at its `generateFile(...)` call (~line 87) add `cssMin` as the final arg.
- `GeneratePackage` (~line 28): forward `nil` — `return GeneratePackageWithFilters(dir, []string{stdImportPath}, nil)`.

- [ ] **Step 5: Thread `cssMin` through gen + bypass the cache when a custom minifier is set.**

In `gen/cache.go`:
- `generateCached` (~line 14): add the param — `func generateCached(paths, filterPkgs []string, useCache bool, cssMin func(string) (string, error)) (Result, error)` — and forward `cssMin` into BOTH `codegen.GeneratePackagesWithFilters(...)` calls (the cache GENERATE phase ~line 90, and via `mustGen`).
- `mustGen` (~line 162): add the param — `func mustGen(root string, dirs, filterPkgs []string, cssMin func(string) (string, error), res *Result) ...` — and pass `cssMin` into its `codegen.GeneratePackagesWithFilters(root, dirs, filterPkgs, cssMin)` call. Update the `mustGen(...)` call site inside `generateCached` to pass `cssMin`.

In `gen/gen.go`:
- `generate` (~line 126): `func generate(paths []string, filterPkgs []string, cssMin func(string) (string, error)) (Result, error)`, returning `generateCached(paths, filterPkgs, cssMin == nil, cssMin)` — **`useCache` is `cssMin == nil`**, so a custom minifier bypasses the (un-keyable) cache.
- The public `Generate(paths)` wrapper (~line 116): `return generate(paths, nil, nil)`.

In `gen/main.go`:
- `runGenerate` (~line 177): add `cssMin func(string) (string, error)` as the final param; forward it into its `generate(...)` call (note `runGenerate` already carries a `noCache bool` — pass `cssMin` through to `generate`, which decides `useCache`).
- The `runGenerate(cmdArgs, stdout, stderr, quiet, verbose, false, cfg.filterPkgs)` dispatch call (~line 110): append `cfg.cssMin`.

Then `grep -rn 'generate(\|GeneratePackagesWithFilters(\|GeneratePackageWithFilters(\|generateFile(\|generateCached(\|mustGen(' --include='*.go' .` and add the new trailing argument (`nil` where there is no custom minifier) to any remaining call site, including tests.

- [ ] **Step 6: Build + run the gen tests + full suite**

Run: `go build ./... && go test ./gen -run TestWithCSSMinifier && go test ./...`
Expected: PASS. (If the build fails with "not enough arguments", you missed a threaded call site — add the trailing `nil`/`cssMin`.)

- [ ] **Step 7: Commit**

```bash
git add gen/ internal/codegen/
git commit -m "gen: WithCSSMinifier extension point; thread custom minifier to codegen (holeless only)"
```

---

### Task 5: ROADMAP + example note

**Files:**
- Modify: `docs/ROADMAP.md`
- Modify: `examples/02_text_escaping.gsx` (or wherever `ThemedCard` lives) — a one-line note

- [ ] **Step 1: ROADMAP** — find the CSS/minification roadmap line and mark the CSS-minification half shipped. Add under the CSS auto-sanitize entry (search for "CSS contexts auto-sanitize"):

```markdown
   - **CSS minification — DONE (slice 2):** `<style>` static CSS is minified at
     codegen time by a robust, stdlib-only built-in (whitespace/comments only, no
     value rewrites, hole-aware for `${ }`); `gen.WithCSSMinifier` swaps in an
     aggressive minifier (e.g. tdewolff) for holeless blocks. On by default;
     `gsx fmt`/source untouched. JS minification (`gen.WithJSMinifier`) is slice 3.
```

- [ ] **Step 2: Example note** — add a brief comment above `ThemedCard` in `examples/02_text_escaping.gsx`:

```gsx
// (<style> static CSS is auto-minified at build time; gsx.RawCSS / source formatting are unaffected.)
```

Run `go test ./internal/corpus -run Example -update && go test ./internal/corpus` — `02_text_escaping.gsx: ok` must hold.

- [ ] **Step 3: Commit**

```bash
git add docs/ROADMAP.md examples/02_text_escaping.gsx internal/corpus/testdata
git commit -m "docs: CSS minification shipped (slice 2) + example note"
```

---

## Self-Review

**Spec coverage (Component 5, CSS half):**
- Built-in `internal/cssmin` real tokenizer, safe set (comments/whitespace/`;}`/edges, no value rewrites) → Task 1. ✓
- Tokenizer preserves significant whitespace (combinators, value separators, calc, strings, url) → Task 1 tests. ✓
- Hole-aware (opaque `${ }`, adjacent whitespace preserved, interp pointers reused) → Task 2. ✓
- `WithCSSMinifier` extension, holeless-only, default built-in → Task 2 (boundary) + Task 4 (option + threading). ✓
- On by default, codegen-output only (fmt untouched) → Task 3. ✓
- `<style>` only; JS deferred → all tasks scope to `<style>`. ✓
- Historical-breakage corpus → Task 1 tests. ✓

**Placeholder scan:** the only `PLACEHOLDER`s are the two `-update`-generated golden sections (Task 3 Step 1, by design — the harness writes them); every code step has complete code.

**Type consistency:** `minifyCSS(string) string`, `MinifyFile(*ast.File, func(string)(string,error)) error`, `WithCSSMinifier(func(css string)(string,error)) Option`, `cfg.cssMin`, `generateFile(..., cssMin func(string)(string,error))`, `GeneratePackageWithFilters(dir, filterPkgs, cssMin)` — consistent across tasks. The ext-func type `func(string)(string,error)` is identical everywhere (`cssmin.MinifyFile`, the option, the threaded param).
