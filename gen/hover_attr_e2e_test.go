package gen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// H1 same-package: hover on the `comments` attribute → "comments []store.Comment".
func TestHoverAttrParamSamePkg(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	mk := func(p, c string) {
		t.Helper()
		full := filepath.Join(dir, p)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	mk("types.go", "package x\n\ntype Comment struct{ Body string }\n")
	src := "package x\n\ncomponent CommentsList(comments []Comment) {\n\t<ul></ul>\n}\n\ncomponent Page() {\n\t<CommentsList comments={nil}/>\n}\n"
	mk("comp.gsx", src)

	h := hoverAt(t, dir, "comp.gsx", src, "comments={", 0)
	if h == nil || !strings.Contains(h.Contents.Value, "comments []Comment") {
		t.Fatalf("hover on attr name = %+v, want contains 'comments []Comment'", h)
	}
}

func TestHoverAttrParamExactCase(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	mk := func(p, c string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	src := "package x\n\ncomponent Card(title string, Title bool) {\n\t<div/>\n}\n\ncomponent Page() {\n\t<Card title=\"ok\" Title={true}/>\n}\n"
	mk("comp.gsx", src)

	h := hoverAt(t, dir, "comp.gsx", src, "Title={", 0)
	if h == nil || !strings.Contains(h.Contents.Value, "Title bool") {
		t.Fatalf("hover on exact-case attr = %+v, want contains 'Title bool'", h)
	}
}

func TestHoverPlainGoCallableTarget(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	mk := func(p, c string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	mk("pill.go", "package x\n\nimport \"github.com/gsxhq/gsx\"\n\nfunc Pill(label string, attrs gsx.Attrs) gsx.Node {\n\treturn renderPill(label, attrs)\n}\n")
	src := "package x\n\nimport \"github.com/gsxhq/gsx\"\n\ncomponent renderPill(label string, attrs gsx.Attrs) {\n\t<span { attrs... }>{label}</span>\n}\n\ncomponent Page() {\n\t<Pill label=\"ok\"/>\n}\n"
	mk("comp.gsx", src)

	h := hoverAt(t, dir, "comp.gsx", src, "Pill label", 1)
	if h == nil || !strings.Contains(h.Contents.Value, "func Pill(label string, attrs gsx.Attrs) gsx.Node") {
		t.Fatalf("hover on plain-Go callable target = %+v, want Pill's exact signature", h)
	}
}

// H1 cross-package + H2: hover on `name` attr → "name string"; hover on the
// `components.Input` tag → "component Input(".
func TestHoverCrossPkgAttrAndTag(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	mk := func(p, c string) {
		t.Helper()
		full := filepath.Join(dir, p)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	mk("ui/components/comp.gsx", "package components\n\ncomponent Input(name string) {\n\t<input value={name}/>\n}\n")
	page := "package x\n\nimport \"example.com/x/ui/components\"\n\ncomponent Page() {\n\t<components.Input name=\"a\"/>\n}\n"
	mk("page.gsx", page)
	if _, err := Generate([]string{filepath.Join(dir, "ui", "components")}); err != nil {
		t.Fatalf("generate dep: %v", err)
	}

	// H1 cross-package: cursor on the `name` attribute.
	h := hoverAt(t, dir, "page.gsx", page, "name=\"a\"", 0)
	if h == nil || !strings.Contains(h.Contents.Value, "name string") {
		t.Fatalf("cross-pkg attr hover = %+v, want 'name string'", h)
	}
	// H2: cursor on the `Input` of `components.Input`.
	h = hoverAt(t, dir, "page.gsx", page, "components.Input", len("components.")+1)
	if h == nil || !strings.Contains(h.Contents.Value, "component Input(") {
		t.Fatalf("cross-pkg tag hover = %+v, want 'component Input('", h)
	}
}
