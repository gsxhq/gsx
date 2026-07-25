package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/diag"
)

// wantMsg is the exact "bare-comment-touches-text" diagnostic wording, pinned
// here so every case below asserts the same string the brief specifies.
const wantMsg = `bare // comment cannot touch text content; use {// …} for a comment or {"// …"} to render it`

// bareCommentModule lays out a temp module with pkgDir/views.gsx = src and
// returns its diagnostics from Generate — the same in-memory module +
// Generate + inspect-diagnostics pattern used throughout module_diag_test.go.
func bareCommentModule(t *testing.T, src string) []diag.Diagnostic {
	t.Helper()
	root := t.TempDir()
	repoRoot, _ := filepath.Abs("../..")
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(root, "views")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, pkgDir, "views.gsx", src)

	m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app", FilterPkgs: []string{StdImportPath}})
	if err != nil {
		t.Fatal(err)
	}
	_, diags, err := m.Generate(pkgDir)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	return diags
}

// bareCommentErrors filters diags down to bare-comment-touches-text errors.
func bareCommentErrors(diags []diag.Diagnostic) []diag.Diagnostic {
	var out []diag.Diagnostic
	for _, d := range diags {
		if d.Code == "bare-comment-touches-text" {
			out = append(out, d)
		}
	}
	return out
}

// TestBareCommentTouchingTextErrors: a bare comment with real text content on
// BOTH sides (post-wsnorm, across only newline whitespace) is an error.
func TestBareCommentTouchingTextErrors(t *testing.T) {
	t.Parallel()
	diags := bareCommentModule(t, "package views\n\ncomponent C() {\n\t<p>\nhello\n// note\nworld\n\t</p>\n}\n")

	if len(diags) != 1 {
		t.Fatalf("diagnostics = %d, want 1; all diagnostics: %+v", len(diags), diags)
	}
	d := diags[0]
	if d.Code != "bare-comment-touches-text" {
		t.Fatalf("code = %q, want %q", d.Code, "bare-comment-touches-text")
	}
	if d.Message != wantMsg {
		t.Fatalf("message = %q, want %q", d.Message, wantMsg)
	}
	// "// note" starts on line 6, column 1 (line-start, no leading whitespace).
	if d.Start.Line != 6 || d.Start.Column != 1 {
		t.Fatalf("position = %d:%d, want 6:1", d.Start.Line, d.Start.Column)
	}
}

// TestBareCommentOneSidedTextErrors: text on only ONE side (top of the
// paragraph; the leading whitespace-only text before the comment has already
// been dropped by wsnorm) is still an error.
func TestBareCommentOneSidedTextErrors(t *testing.T) {
	t.Parallel()
	diags := bareCommentModule(t, "package views\n\ncomponent C() {\n\t<p>\n// note\nhello\n\t</p>\n}\n")

	errs := bareCommentErrors(diags)
	if len(errs) != 1 {
		t.Fatalf("bare-comment-touches-text diagnostics = %d, want 1; all diagnostics: %+v", len(errs), diags)
	}
	d := errs[0]
	if d.Message != wantMsg {
		t.Fatalf("message = %q, want %q", d.Message, wantMsg)
	}
	// "// note" starts on line 5, column 1.
	if d.Start.Line != 5 || d.Start.Column != 1 {
		t.Fatalf("position = %d:%d, want 5:1", d.Start.Line, d.Start.Column)
	}
}

// TestBareCommentBetweenTagsClean: a bare comment between two element
// siblings (whitespace-only text on both sides, dropped by wsnorm) is legal;
// the comment itself never renders and the surrounding elements render
// adjacently.
func TestBareCommentBetweenTagsClean(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repoRoot, _ := filepath.Abs("../..")
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(root, "views")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, pkgDir, "views.gsx", "package views\n\ncomponent C() {\n\t<div>\n<span>a</span>\n// note\n<span>b</span>\n\t</div>\n}\n")

	m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app", FilterPkgs: []string{StdImportPath}})
	if err != nil {
		t.Fatal(err)
	}
	files, diags, err := m.Generate(pkgDir)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %+v", diags)
	}
	if len(files) == 0 {
		t.Fatal("expected generated files, got none")
	}
	var genSrc string
	for _, src := range files {
		genSrc += string(src)
	}
	// The comment drops out entirely and the two spans render adjacently — no
	// leftover whitespace, no comment text, in the static-write literals.
	if !strings.Contains(genSrc, `<span>a</span>`) || !strings.Contains(genSrc, `<span>b</span>`) {
		t.Fatalf("generated source missing expected spans:\n%s", genSrc)
	}
	if strings.Contains(genSrc, "note") {
		t.Fatalf("generated source leaked the comment text:\n%s", genSrc)
	}
	// Concatenate every static-write literal in source order and confirm it
	// renders exactly <div><span>a</span><span>b</span></div> with the bare
	// comment gone and no stray whitespace from its newline neighbors.
	var rendered strings.Builder
	for _, lit := range staticWriteLiterals(genSrc) {
		rendered.WriteString(lit)
	}
	if want := "<div><span>a</span><span>b</span></div>"; rendered.String() != want {
		t.Fatalf("rendered = %q, want %q; generated source:\n%s", rendered.String(), want, genSrc)
	}
}

// staticWriteLiterals extracts the string-literal argument of every
// `_gsxgw.S("...")` static-write call in generated source, in source order.
// Static content with no dynamic holes lowers to a flat sequence of these
// calls (see emit.go's coalesceStaticWrites); concatenating them reconstructs
// the fully-rendered static HTML for a component with no interpolation.
func staticWriteLiterals(src string) []string {
	const marker = `_gsxgw.S("`
	var out []string
	for {
		i := strings.Index(src, marker)
		if i < 0 {
			return out
		}
		src = src[i+len(marker):]
		j := strings.IndexByte(src, '"')
		if j < 0 {
			return out
		}
		lit := src[:j]
		// The literals here are plain HTML with no escaped quotes, so a raw
		// Go-string unquote is unnecessary; only `\"` (from an attribute like
		// id="x") would need unescaping, and this test's fixture has none.
		out = append(out, lit)
		src = src[j+1:]
	}
}

// TestBareCommentNonTextNeighborsClean: every non-text neighbor kind is legal
// against a bare comment — an interpolation hole, control-flow markup, a
// braced comment, an HTML comment, and the body boundary (comment as the sole
// child of an element).
func TestBareCommentNonTextNeighborsClean(t *testing.T) {
	t.Parallel()
	src := "package views\n\n" +
		"component C(name string) {\n" +
		"\t<div>\n" +
		"{name}\n" +
		"// c1\n" +
		"{ if true { <span/> } }\n" +
		"// c2\n" +
		"{/* hidden */}\n" +
		"// c3\n" +
		"<!-- html -->\n" +
		"// c4\n" +
		"{name}\n" +
		"\t</div>\n" +
		"\t<p>\n" +
		"// c5\n" +
		"\t</p>\n" +
		"}\n"
	diags := bareCommentModule(t, src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %+v", diags)
	}
}

// TestBracedCommentTouchingTextStillLegal: a braced comment (Bare=false) is
// exempt from this check — that has always been legal, pinned separately by
// the comments/content_comment corpus case.
func TestBracedCommentTouchingTextStillLegal(t *testing.T) {
	t.Parallel()
	diags := bareCommentModule(t, "package views\n\ncomponent C() {\n\t<p>{/* hidden */}Visible</p>\n}\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %+v", diags)
	}
}
