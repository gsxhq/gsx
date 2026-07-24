# Bare `//` Line Comments in Child Content — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A line-start `//` in child-content position becomes a source-only comment; a bare comment touching text is a hard diagnostic; `pre`/`textarea` subtrees are exempt.

**Architecture:** The parser's text lexer stops at line-start `//` (suppressed inside whitespace-significant subtrees) and emits the existing `ast.Comment` node with a new `Bare` flag; codegen already drops `ast.Comment`, so rendering is free. A post-wsnorm analyze-phase walk reports the "comment touches text" error through the diagnostics bag. The printer gains a bare-comment layout; fmt never converts between spellings.

**Tech Stack:** Go (root `gsx` module, stdlib-only runtime constraint untouched — all changes are parser/codegen/printer tooling), txtar corpora.

**Spec:** `docs/superpowers/specs/2026-07-24-bare-line-comments-content-design.md` — read it first; it defines the recognition rule, the adjacency legality rule, exemptions, and the future-relaxation note.

## Global Constraints

- **Work in a new git worktree** (user requirement): create it with the `superpowers:using-git-worktrees` skill before Task 1; branch name `bare-line-comments`.
- Runtime (root package) stays standard-library only; every file touched here is tooling (parser/ast/internal), which is fine.
- Every syntax/codegen change ships corpus cases per context (`internal/corpus/testdata/cases/**`) and fmt-corpus cases (`internal/gsxfmt/testdata/cases/`).
- Regenerate semantic goldens with `go test ./internal/corpus -run TestCorpus -update`, then verify without `-update`. Same for fmt: `go test ./internal/gsxfmt -run TestFmtCorpus -update`.
- Never hand-edit `.x.go` or golden files.
- Diagnostic message (exact, from spec): `bare // comment cannot touch text content; use {// …} for a comment or {"// …"} to render it`. Code: `bare-comment-touches-text`.
- Inner loop gate: `make check`. Pre-merge gate: `make ci` (authoritative; merge only on its real exit status — never `||`-chain past it).
- Go pinned to `GO_VERSION` in `.github/workflows/ci.yml` (1.26.1).

## Key facts for someone new to this codebase

- Parser lives in the public `parser` package (`parser/markup.go` is markup; the `parser` struct is in `parser/parser.go`). It is a hand-written recursive-descent scanner over `p.src []byte`(string-like) with cursor `p.i`; positions via `p.posAt(offset)`.
- Text nodes come from `parseTextCtx` (`parser/markup.go:53`), which consumes until `<`, `{`, or (in control bodies) `}`. Three loops consume child content and must all learn the new branch: `parseMarkupUntilCloseWS` (markup.go:257 — component bodies, control bodies, markup-attribute slots), `parseCaseBody` (markup.go:521), `parseChildren` (markup.go:952 — element/fragment children).
- `<script>`/`<style>` bodies never reach these loops (`parseRawTextBody`, markup.go:900) — naturally exempt.
- `ast.Comment` (`ast/ast.go:285`) is the source-only content comment; codegen drops it (`internal/codegen/emit.go:2091` no-op case); the printer renders it in braced form (`internal/printer/printer.go:722`).
- `wsnorm` (`internal/wsnorm/wsnorm.go`) normalizes whitespace post-parse; its `preserveTags` table (line 63: pre, textarea, script, style) defines whitespace-significant subtrees. `wsnorm.Normalize` runs once in `parsedGSXFile` (`internal/codegen/module_importer.go:1829`) before the parse cache, so every analyzed AST is post-wsnorm.
- Analyze-phase positioned diagnostics are reported via `bag.Errorf(pos, end, code, msg)`; a good model is the reserved-body-bindings loop at `internal/codegen/module_importer.go:1279-1288`, which walks `f.Decls` per file inside the shared analysis path (LSP and generate both flow through it).
- Corpus diagnostics golden format is `line:col: message` (see `internal/corpus/testdata/cases/diagnostics/class_part_undefined_ident.txtar`).
- Docs examples: `examples/NNN-*.txtar` with a `-- doc --` header (`page: comments` routes into `docs/guide/syntax/comments.md` includes); regenerate artifacts with `make examples`.

---

### Task 1: AST flag, shared preserve table, parser recognition, minimal printer

**Files:**
- Modify: `ast/ast.go` (Comment struct, ~line 281)
- Modify: `ast/print.go` (~line 87)
- Modify: `internal/wsnorm/wsnorm.go` (~line 63: export the preserve predicate)
- Modify: `parser/parser.go` (parser struct: new field)
- Modify: `parser/markup.go` (parseTextCtx, the three loops, parseElement)
- Modify: `internal/printer/printer.go` (~line 722)
- Test: `parser/barecomment_test.go` (new)

**Interfaces:**
- Produces: `ast.Comment.Bare bool` (true = bare `//` line; false = braced `{// }`/`{/* */}`). `wsnorm.IsPreserveTag(tag string) bool` (exported; replaces unexported `isPreserveTag` at its call sites inside wsnorm). Parser helpers `(p *parser) atBareContentComment() bool` and `(p *parser) parseBareComment() *ast.Comment` — Task 2/3 rely on the parser emitting `*ast.Comment{Bare: true}` nodes in child content outside pre/textarea subtrees.

- [ ] **Step 1: Write failing parser tests**

Create `parser/barecomment_test.go`. Adapt the parse-and-inspect boilerplate from an existing test in `parser/markup_test.go` (use the same `ParseFile`/fixture helper that file uses — do not invent a new harness). Test bodies:

```go
// Each test parses a component and inspects the body/children slices.
// helper: parse(t, src) *ast.File — reuse the pattern from markup_test.go.

func TestBareCommentBetweenTags(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<div>\n\t\t<span>a</span>\n\t\t// note\n\t\t<span>b</span>\n\t</div>\n}\n"
	// Expect div.Children to contain, in order (ignoring whitespace Text nodes):
	// Element(span), Comment{Bare: true, Text: "note"}, Element(span).
}

func TestBareCommentSplitsTextRun(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<p>\n\t\thello\n\t\t// note\n\t\tworld\n\t</p>\n}\n"
	// Expect p.Children: Text("\n\t\thello\n\t\t"), Comment{Bare: true, Text: "note"}, Text("\n\t\tworld\n\t").
	// (Recognition is parse-level; legality is Task 2's analyzer check.)
}

func TestMidLineSlashesStayText(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<p>hello // world</p>\n}\n"
	// Expect exactly one Text child "hello // world"; no Comment node.
}

func TestTagOnSameLineStaysText(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<p>// hi</p>\n}\n"
	// `//` preceded by `<p>` on its line — not line-start. One Text child "// hi".
}

func TestPreSubtreeVerbatim(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<pre><code>\n// display this\nx\n</code></pre>\n}\n"
	// No Comment nodes anywhere under <pre>; the // line is inside a Text node.
}

func TestTextareaVerbatim(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<textarea>\n// literal\n</textarea>\n}\n"
	// No Comment nodes; text verbatim.
}

func TestBareCommentInControlBody(t *testing.T) {
	src := "package p\n\ncomponent C(xs []string) {\n\t<ul>\n\t\t{ for _, x := range xs {\n\t\t\t// per-item note\n\t\t\t<li>{x}</li>\n\t\t} }\n\t</ul>\n}\n"
	// ForMarkup.Body contains Comment{Bare: true, Text: "per-item note"}.
}

func TestBareCommentInCaseBody(t *testing.T) {
	src := "package p\n\ncomponent C(n int) {\n\t<div>\n\t\t{ switch n {\n\t\tcase 1:\n\t\t\t// one\n\t\t\t<b>1</b>\n\t\t} }\n\t</div>\n}\n"
	// CaseClause.Body contains Comment{Bare: true, Text: "one"}.
}

func TestEmptyBareComment(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<div>\n\t\t<span>a</span>\n\t\t//\n\t\t<span>b</span>\n\t</div>\n}\n"
	// Comment{Bare: true, Text: ""} between the spans.
}

func TestCRLFLineStart(t *testing.T) {
	src := "package p\r\n\r\ncomponent C() {\r\n\t<div>\r\n\t\t<span>a</span>\r\n\t\t// note\r\n\t\t<span>b</span>\r\n\t</div>\r\n}\r\n"
	// \r before line-start whitespace must not defeat recognition;
	// Comment{Bare: true, Text: "note"} (no trailing \r in Text).
}

func TestFileStartLine(t *testing.T) {
	// Line-start scan hitting offset 0 (no preceding newline) must not panic.
	// A // at file start is Go code territory (package clause), so just assert
	// a normal file still parses: reuse any existing minimal fixture.
}
```

Fill in the assertions concretely (walk `f.Decls[0].(*ast.Component).Body`, index into `.Children`, type-assert). Skip whitespace-only `*ast.Text` nodes only where a test says "ignoring whitespace" — assert exact node sequences elsewhere.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./parser -run 'BareComment|MidLineSlashes|TagOnSameLine|PreSubtree|Textarea|EmptyBare|CRLF|FileStart' -v`
Expected: FAIL — Comment nodes not produced (text swallows the `//` lines), and `Bare` field does not exist (compile error first; add the field in Step 3).

- [ ] **Step 3: Implement**

`ast/ast.go` — extend Comment and update its doc (the parenthetical about bare `//` being text is now wrong):

```go
// Comment is a source-only content comment between child nodes: braced
// `{/* text */}` / `{// text }`, or a bare line-start `// text` (Bare=true).
// Unlike HTMLComment it is NOT rendered — codegen drops it, the formatter
// preserves it and never converts between the braced and bare spellings.
type Comment struct {
	span
	Text  string
	Block bool // true = /* */, false = //
	Bare  bool // true = bare line-start `//` (no braces); implies Block=false
}
```

`ast/print.go` line 88 — include the new field:

```go
case *Comment:
	if _, err := fmt.Fprintf(w, "%sComment block=%t bare=%t text=%q\n", indent, n.Block, n.Bare, n.Text); err != nil {
```

`internal/wsnorm/wsnorm.go` — export the predicate (keep the table unexported):

```go
// IsPreserveTag reports whether tag (any case) roots a whitespace-significant
// subtree (pre, textarea) or a raw-text body (script, style). The parser uses
// the same predicate to suppress bare `//` content-comment recognition.
func IsPreserveTag(tag string) bool {
	return preserveTags[strings.ToLower(tag)]
}
```

Rename the existing unexported `isPreserveTag` call sites within wsnorm to `IsPreserveTag` and delete the old function.

`parser/parser.go` — add to `type parser struct`:

```go
	// preserveDepth counts enclosing pre/textarea elements; >0 suppresses bare
	// `//` content-comment recognition (their text is verbatim display content).
	preserveDepth int
```

`parser/markup.go` — the recognition primitives:

```go
// atBareContentComment reports whether the cursor sits at a line-start `//` in
// child content: the next two bytes are slashes and every byte between the
// previous newline (or file start) and the cursor is space, tab, or CR.
// Suppressed inside pre/textarea subtrees, whose text is verbatim.
func (p *parser) atBareContentComment() bool {
	if p.preserveDepth > 0 || !p.at("//") {
		return false
	}
	for j := p.i - 1; j >= 0; j-- {
		switch p.src[j] {
		case ' ', '\t', '\r':
		case '\n':
			return true
		default:
			return false
		}
	}
	return true // file start counts as line start
}

// parseBareComment consumes a bare `//` line comment to end of line (the '\n'
// is left for the following text node / skipSpace). Cursor must satisfy
// atBareContentComment.
func (p *parser) parseBareComment() *ast.Comment {
	start := p.i
	p.i += 2 // past '//'
	for !p.eof() && p.src[p.i] != '\n' {
		p.i++
	}
	n := &ast.Comment{Text: strings.TrimSpace(p.src[start+2 : p.i]), Bare: true}
	ast.SetSpan(n, p.posAt(start), p.posAt(p.i))
	return n
}
```

`parseTextCtx` (markup.go:56-62) — add the stop condition so a `//` line splits a text run:

```go
	for !p.eof() {
		b := p.src[p.i]
		if b == '<' || b == '{' || (inBlock && b == '}') {
			break
		}
		if b == '/' && p.atBareContentComment() {
			break
		}
		p.i++
	}
```

Add the consuming branch to all three loops, BEFORE their text-default branch (identical shape in each):

- `parseMarkupUntilCloseWS` (markup.go:266 switch): new case after the `'{'` case:

```go
		case p.atBareContentComment():
			nodes = append(nodes, p.parseBareComment())
```

- `parseCaseBody` (markup.go:537 switch): same new case after the `'{'` case.
- `parseChildren` (markup.go:987, after the `'{'` block, before `parseText`):

```go
		if p.atBareContentComment() {
			nodes = append(nodes, p.parseBareComment())
			continue
		}
```

`parseElement` (markup.go:840) — wrap the children parse of preserve-tag elements. Import `"github.com/gsxhq/gsx/internal/wsnorm"`:

```go
	if wsnorm.IsPreserveTag(tag) {
		p.preserveDepth++
	}
	children, closeNamePos, err := p.parseChildren(tag)
	if wsnorm.IsPreserveTag(tag) {
		p.preserveDepth--
	}
```

(Script/style never reach this line — `isRawTextTag` returns earlier — so the increment only ever fires for pre/textarea, but using the shared predicate keeps one table.)

`internal/printer/printer.go` (markup switch, line 722) — minimal bare layout so round-tripping doesn't silently convert spellings (Task 4 pins layout with fmt-corpus cases):

```go
	case *ast.Comment:
		if v.Bare {
			// Bare `//` line comment: must own its source line — printed
			// mid-line it would reparse as text. BreakParent forces the
			// enclosing children group to break so the line joiner puts it
			// (and the following sibling) on fresh lines.
			return pretty.Concat(pretty.Text("// "), pretty.Text(v.Text), pretty.BreakParent)
		}
		if v.Block { ... existing braced cases unchanged ... }
```

- [ ] **Step 4: Run the new tests**

Run: `go test ./parser -run 'BareComment|MidLineSlashes|TagOnSameLine|PreSubtree|Textarea|EmptyBare|CRLF|FileStart' -v`
Expected: PASS.

- [ ] **Step 5: Run the full inner-loop gate and reconcile goldens**

Run: `make check`
Expected: the corpus `parse_snapshot` golden for comments now differs (print format gained `bare=%t`). Any OTHER failure is a real regression — investigate before touching goldens. In particular: a pre-existing corpus or fmt case containing a line-start `//` in content would change meaning (text → comment); if one exists, inspect it — between tags the new meaning is the intended behavior change (regen the golden), touching text it becomes Task 2's error (adjust the case only if it was incidental, and say so in the commit message).

Regenerate: `go test ./internal/corpus -run TestCorpus -update && go test ./internal/corpus -run TestCorpus`
Then: `go test ./internal/gsxfmt -run TestFmtCorpus` (regen with `-update` only if a diff is understood and intended).
Then: `make check`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add ast/ parser/ internal/wsnorm/ internal/printer/ internal/corpus/
git commit -m "feat(parser): recognize bare // line comments in child content"
```

---

### Task 2: "comment touches text" analyzer diagnostic

**Files:**
- Create: `internal/codegen/barecomment.go`
- Modify: `internal/codegen/module_importer.go` (hook next to the reserved-body-bindings loop, ~line 1279)
- Test: `internal/codegen/barecomment_test.go` (new)

**Interfaces:**
- Consumes: `ast.Comment.Bare` from Task 1; post-wsnorm ASTs (the walk sees `parsedGSXFile` output, where whitespace-only text neighbors of a legal bare comment have already been dropped — so "touches text" is exactly "immediate sibling is `*ast.Text`").
- Produces: `checkBareComments(f *gsxast.File) []bareCommentViolation` where `type bareCommentViolation struct { pos, end token.Pos }`; a bag error with code `bare-comment-touches-text`.

- [ ] **Step 1: Write the failing test**

Create `internal/codegen/barecomment_test.go`. Model the module setup on an existing diagnostics wire test in `internal/codegen/module_diag_test.go` (same in-memory module + Generate/Analyze + inspect diagnostics pattern — reuse its helpers, don't build new scaffolding). Cases:

```go
func TestBareCommentTouchingTextErrors(t *testing.T) {
	// component C() { <p>\nhello\n// note\nworld\n</p> }
	// Expect exactly one error diagnostic, code "bare-comment-touches-text",
	// message `bare // comment cannot touch text content; use {// …} for a comment or {"// …"} to render it`,
	// positioned at the `//` token (assert line:col).
}

func TestBareCommentOneSidedTextErrors(t *testing.T) {
	// <p>\n// note\nhello\n</p> — text on ONE side only (top of paragraph): still an error.
}

func TestBareCommentBetweenTagsClean(t *testing.T) {
	// <div>\n<span>a</span>\n// note\n<span>b</span>\n</div> — no diagnostics;
	// generated output renders <div><span>a</span><span>b</span></div>.
}

func TestBareCommentNonTextNeighborsClean(t *testing.T) {
	// Neighbors of every non-text kind are legal: hole `{name}`, control flow,
	// braced comment {/* x */}, HTML comment <!-- x -->, body boundary
	// (comment as sole child). One component exercising each; no diagnostics.
}

func TestBracedCommentTouchingTextStillLegal(t *testing.T) {
	// <p>{/* hidden */}Visible</p> — Bare=false comments are exempt (existing
	// behavior, pinned by corpus comments/content_comment); no diagnostics.
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/codegen -run TestBareComment -v`
Expected: FAIL — no diagnostics produced (checker doesn't exist).

- [ ] **Step 3: Implement**

`internal/codegen/barecomment.go`:

```go
package codegen

import (
	"go/token"

	gsxast "github.com/gsxhq/gsx/ast"
)

// bareCommentViolation is a bare `//` content comment whose immediate sibling
// is text. Detected post-wsnorm: a LEGAL bare comment's whitespace neighbors
// (always newline-bearing, by the line-start rule) have been dropped by
// normalization, so any surviving adjacent *ast.Text is real content touching
// the comment across only whitespace — exactly the spec's "touches text".
type bareCommentViolation struct {
	pos, end token.Pos
}

// checkBareComments walks every child-content list in f and returns the bare
// comments that touch text. Braced comments (Bare=false) are exempt — they
// have always been legal against text. Preserve subtrees need no special
// case: the parser never produces Bare comments inside them.
func checkBareComments(f *gsxast.File) []bareCommentViolation {
	var out []bareCommentViolation
	check := func(nodes []gsxast.Markup) {
		for i, n := range nodes {
			c, ok := n.(*gsxast.Comment)
			if !ok || !c.Bare {
				continue
			}
			touches := false
			if i > 0 {
				_, touches = nodes[i-1].(*gsxast.Text)
			}
			if !touches && i+1 < len(nodes) {
				_, touches = nodes[i+1].(*gsxast.Text)
			}
			if touches {
				out = append(out, bareCommentViolation{pos: c.Pos(), end: c.End()})
			}
		}
	}
	gsxast.Inspect(f, func(n gsxast.Node) bool {
		switch v := n.(type) {
		case *gsxast.Component:
			check(v.Body)
		case *gsxast.Element:
			check(v.Children)
		case *gsxast.Fragment:
			check(v.Children)
		case *gsxast.MarkupAttr:
			check(v.Value)
		case *gsxast.IfMarkup:
			check(v.Then)
			check(v.Else)
		case *gsxast.ForMarkup:
			check(v.Body)
		case *gsxast.CaseClause:
			check(v.Body)
		}
		return true
	})
	return out
}
```

Verify the switch is exhaustive over `[]Markup`-carrying nodes by comparing against `ast.Inspect`'s own dispatch (`ast/ast.go:56`) and the "Children by type" doc at `ast/ast.go:695` — script/style `Segments` and `CSSSegments` are raw-text (no Comment nodes possible) and are intentionally omitted; if the field survey turns up another `[]Markup` field, add it and a test.

Hook in `internal/codegen/module_importer.go`, immediately after the reserved-body-bindings loop (after line 1288), same per-file scope (`f`, `bag` in scope):

```go
		for _, v := range checkBareComments(f) {
			bag.Errorf(v.pos, v.end, "bare-comment-touches-text",
				"bare // comment cannot touch text content; use {// …} for a comment or {\"// …\"} to render it")
		}
```

This is error severity: generation fails (exit 1) and the emit/build path is gated exactly like the reserved-identifier error above it; the file is NOT deleted from the set, so its other diagnostics still surface, and the LSP (which flows through this same analysis loop) shows the error live.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/codegen -run TestBareComment -v`
Expected: PASS.

- [ ] **Step 5: Inner-loop gate**

Run: `make check`
Expected: PASS (no existing corpus case has a bare comment touching text yet).

- [ ] **Step 6: Commit**

```bash
git add internal/codegen/barecomment.go internal/codegen/barecomment_test.go internal/codegen/module_importer.go
git commit -m "feat(codegen): error when a bare // content comment touches text"
```

---

### Task 3: semantic corpus cases

**Files:**
- Create: `internal/corpus/testdata/cases/comments/bare_between_tags.txtar`
- Create: `internal/corpus/testdata/cases/comments/bare_contexts.txtar`
- Create: `internal/corpus/testdata/cases/comments/bare_preserve_exempt.txtar`
- Create: `internal/corpus/testdata/cases/comments/bare_midline_text.txtar`
- Create: `internal/corpus/testdata/cases/comments/bare_touches_text_err.txtar`
- Modify (regenerated): `internal/corpus/testdata/cases/comments/parse_snapshot.txtar` goldens if affected, `coverage.golden` manifest
- Modify: `internal/corpus/testdata/cases/comments/content_comment.txtar` (header comment only — its "A bare // in text content is NOT a comment" prose is now stale; its mid-line example is still correct, reword the header to say mid-line)

**Interfaces:**
- Consumes: Tasks 1-2 behavior. No new APIs.

Corpus case format: `# header` comment, `-- input.gsx --`, `-- invoke --`, `-- diagnostics.golden --`, `-- render.golden --` (+ `generated.x.go.golden` pinned by the harness). Model every file on `comments/content_comment.txtar`; error-case diagnostics format on `diagnostics/class_part_undefined_ident.txtar` (`line:col: message`).

- [ ] **Step 1: Write the cases (inputs and empty/expected goldens; `-update` fills the rest)**

`bare_between_tags.txtar` — the headline behavior, render byte-identical to deleting the comment lines:

```
# Bare line-start // between child tags is a source-only comment: retained in
# the AST (fmt preserves it) and absent from rendered HTML. Render output is
# byte-identical to deleting the comment lines.
-- input.gsx --
package views

component C() {
	<div>
		<span>a</span>
		// note between tags
		//
		<span>b</span>
	</div>
}
-- invoke --
C()
-- diagnostics.golden --
-- render.golden --
<div><span>a</span><span>b</span></div>
```

`bare_contexts.txtar` — one case per remaining child-content context (standing per-context rule): component body root, fragment children, `{if}` body, `{for}` body, `switch` case body, child-prop (`header={ <div>… }` markup attribute), element literal in Go-expr position (`var x = <div>…</div>` inside a GoBlock or expression — copy the shape from `cases/element-literals/`), comment adjacent to a hole (`{name}` neighbor is legal), comment adjacent to `<!-- html -->` and `{/* braced */}` comments. Multiple components in one input file are fine (see multi-component corpus cases for the pattern). Every context's render golden must equal the comment-deleted render.

`bare_preserve_exempt.txtar`:

```
# Inside pre/textarea subtrees a line-start // is verbatim display content —
# never a comment, never an error — including nested elements (<pre><code>).
-- input.gsx --
package views

component C() {
	<pre><code>
// rendered verbatim
x := 1
</code></pre>
	<textarea>
// also verbatim
</textarea>
}
-- invoke --
C()
-- diagnostics.golden --
-- render.golden --
(fill via -update; must contain both // lines verbatim)
```

`bare_midline_text.txtar` — mid-line `//` (after text, after a tag on the same line) stays literal text; reuse the trailing-slashes example shape from `content_comment.txtar` plus `<p>// hi</p>`.

`bare_touches_text_err.txtar` — the error cases: both-sides text, one-side text (top of paragraph), empty `//` next to text. Expected diagnostics golden, three lines of `line:col: bare // comment cannot touch text content; use {// …} for a comment or {"// …"} to render it` (exact line:col determined on first run — write your best guess, `-update` does NOT rewrite diagnostics goldens if the harness pins them strictly; check how `diagnostics/` cases were authored and follow that).

- [ ] **Step 2: Run to see failures**

Run: `go test ./internal/corpus -run 'TestCorpus/comments' -v`
(Note: `-run` splits on `/` per segment — quote the whole pattern.)
Expected: FAIL — missing generated goldens for the new cases.

- [ ] **Step 3: Regenerate and verify**

Run: `go test ./internal/corpus -run TestCorpus -update`
(Also rewrites `coverage.golden` — a forgotten manifest bump fails the suite, so the `-update` covers it.)
Then verify clean: `go test ./internal/corpus -run TestCorpus`
Expected: PASS. **Read the regenerated `render.golden` and `generated.x.go.golden` diffs** — confirm renders are byte-identical to comment-deletion and no sanitization/codegen shape changed.

- [ ] **Step 4: Inner-loop gate**

Run: `make check`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/corpus/testdata/
git commit -m "test(corpus): bare // content comment cases per context"
```

---

### Task 4: formatter layout + fmt corpus

**Files:**
- Modify: `internal/printer/printer.go` (only if Step 2 shows layout defects — Task 1 installed the candidate layout)
- Create: `internal/gsxfmt/testdata/cases/bare_comment.txtar`
- Create: `internal/gsxfmt/testdata/cases/bare_comment_contexts.txtar`

**Interfaces:**
- Consumes: printer `*ast.Comment` Bare branch from Task 1.
- Produces: pinned layout — a bare comment always occupies its own line at body indent; fmt never rewrites `// x` ↔ `{// x }`.

- [ ] **Step 1: Write fmt-corpus cases**

Format: `input.gsx` + `fmt.golden` (layout pinning — see any existing case in `internal/gsxfmt/testdata/cases/`). Cases to pin:

`bare_comment.txtar`:
- comment between tags in a multi-line element → stays on its own line at child indent;
- comment with weird source indentation → reindented to child indent;
- element that WOULD fit on one line but contains a bare comment → forced multi-line (BreakParent), comment on its own line, following sibling on a fresh line;
- `//`(empty) preserved as `//`;
- braced `{// x }` in the same file → stays braced (no conversion either direction).

`bare_comment_contexts.txtar`: control-flow body, case body, fragment, child-prop slot — comment lines keep their own lines at each context's indent.

Author `fmt.golden` by hand with the expected layout; if unsure of an edge, run `-update` and REVIEW the output rather than trusting it.

- [ ] **Step 2: Run the fmt corpus**

Run: `go test ./internal/gsxfmt -run TestFmtCorpus -v`
Expected: either PASS, or failures showing layout defects (e.g. comment glued mid-line after a sibling — which would reparse as text and MUST be fixed in the printer, not the golden). The fmt harness's faithfulness check (reparse-and-compare) is the authority: if it reports a structural diff, the printer emitted a bare comment in a position where it lexes back as text — fix `internal/printer/printer.go` (the Bare branch or the children joiner interaction) until reparse is clean. Do not weaken any golden to paper over a faithfulness failure.

- [ ] **Step 3: Verify idempotence and full fmt suite**

Run: `go test ./internal/gsxfmt`
Expected: PASS (idempotence harness runs over all cases).

- [ ] **Step 4: Inner-loop gate**

Run: `make check`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/gsxfmt/testdata/ internal/printer/
git commit -m "feat(fmt): bare // content comment layout"
```

---

### Task 5: docs, example, ROADMAP

**Files:**
- Modify: `docs/guide/syntax/comments.md`
- Create: `examples/218-bare-line-comments.txtar`
- Modify (regenerated): `docs/guide/syntax/_generated/**`, `docs/examples.json`, `playground/server/examples.json` via `make examples`
- Modify: `docs/ROADMAP.md` (mark the feature; one line)

**Interfaces:** none.

- [ ] **Step 1: Write the example**

`examples/218-bare-line-comments.txtar` (model on `217-content-comments-preserved.txtar`):

```
-- doc --
name: Bare line comments
summary: A line that starts with // between child tags is a source-only comment; next to text it is an error, and mid-line // is literal text.
category: Elements
page: comments
pageOrder: 35
-- input.gsx --
package views

component Nav() {
	<nav>
		<a href="/">Home</a>
		// hidden from the HTML
		<a href="/about">About</a>
	</nav>
}
-- invoke --
Nav()
-- render.golden --
<nav><a href="/">Home</a><a href="/about">About</a></nav>
```

- [ ] **Step 2: Update `docs/guide/syntax/comments.md`**

Keep it concise (standing feedback: behavior plainly, rationale in the spec). Changes:
- Table row "Between child nodes": Source-only column becomes `// …` (line start), `{/* … */}`, `{// … }`.
- Replace the stale sentence "Bare `//` or `/* */` between child nodes is text, not a comment, and is rendered." with:

```markdown
A line that starts with `//` between child nodes is a source-only comment. It
may not touch text content — next to a text line it is a compile error; use
`{// …}` to comment or `{"// …"}` to render the slashes. Mid-line `//` is
always literal text, and inside `pre`/`textarea` every `//` renders verbatim.
```

- Add the generated include where the new example lands (follow how pageOrder maps to the `<!--@include: ./_generated/comments/NNN-….md-->` lines; `make examples` names the file).
- No literal `{{ }}` is being added, so no `::: v-pre` concern.

- [ ] **Step 3: Regenerate and check drift**

Run: `make examples`
Then: `git diff --stat docs/ playground/server/examples.json`
Expected: new generated include + examples.json entries; commit them (CI's `ci-examples` drift gate requires it).

- [ ] **Step 4: ROADMAP**

Add/adjust one line in `docs/ROADMAP.md` under the appropriate shipped/section noting bare `//` content comments.

- [ ] **Step 5: Gate and commit**

Run: `make check`
Expected: PASS.

```bash
git add docs/ examples/ playground/server/examples.json
git commit -m "docs: bare // line comments in child content"
```

---

### Task 6: sibling projects (separate repos, one commit each)

**Files (neighboring repos under `~/personal/gsxhq/`):**
- Modify: `../tree-sitter-gsx/grammar.js` (+ its corpus tests under `test/corpus/`)
- Modify: `../vscode-gsx/syntaxes/` TextMate grammar
- Modify: `../gsxhq.github.io/` CodeMirror gsx mode (search for the existing comment token rules)

**Interfaces:** none (highlighting only — gsx has no LSP semantic tokens; editor comment coloring comes from these grammars).

- [ ] **Step 1: tree-sitter-gsx**

Add a content-position line-comment rule: a `//` at line start (the grammar can approximate line-start as "preceded by whitespace containing a newline" via token precedence — follow how the existing `{// }` / tag-interior comment rules are structured) through end of line, scoped as a comment node. Add corpus tests: between tags → comment node; mid-line → text; inside `<pre>` — if the grammar cannot see subtree significance, highlighting a `//` line in `<pre>` as a comment is an accepted, documented approximation (grammar files can't run the analyzer); note it in the test file. Run the repo's own test command (`tree-sitter test`).

- [ ] **Step 2: vscode-gsx**

Add the matching TextMate pattern (line-start `//` in content scope → `comment.line.double-slash.gsx`). Follow the repo's existing content-vs-tag scope split. Run its grammar tests if present; otherwise verify with a sample file in the extension host.

- [ ] **Step 3: website CodeMirror**

Update the CodeMirror gsx mode used by `gsxhq.github.io` playground/docs to color line-start `//` in content as a comment. (The `/guide` prose there is a synced copy of gsx `docs/guide` — do NOT edit prose there; Task 5's docs flow through the sync.)

- [ ] **Step 4: Commit each repo**

One commit per repo with message `feat: bare // line comments in gsx child content`. Do not tag/release vscode-gsx here — releases are tag-gated and a separate decision.

---

### Task 7: final gates and PR

- [ ] **Step 1: Full authoritative gate**

Run: `make ci`
Expected: PASS — check the real exit status directly (never `||`-chain past it; a red gate must block the merge). Also run `make lint`.

- [ ] **Step 2: Adversarial review**

Per repo convention, run one independent adversarial review before merging: a reviewer that builds throwaway probe programs (e.g. a scratch module with bare comments in every context, a pre-subtree probe, a touches-text probe verifying exit code 1 and message text, a `gsx fmt` round-trip probe on a file mixing bare and braced comments) — not just a diff read.

- [ ] **Step 3: PR**

```bash
git push -u origin bare-line-comments
gh pr create --title "feat: bare // line comments in child content" --body "Implements docs/superpowers/specs/2026-07-24-bare-line-comments-content-design.md

- line-start // in child content = source-only comment
- error when a bare comment touches text (code bare-comment-touches-text)
- pre/textarea subtrees + script/style exempt
- fmt preserves spelling; corpus + fmt-corpus cases per context

https://claude.ai/code/session_01GiUqqvjkyy4rE6hScnC576"
```

Sibling-repo PRs/commits (Task 6) land after the gsx PR merges.
