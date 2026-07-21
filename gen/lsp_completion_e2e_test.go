package gen

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

// newCompletionE2EFixture builds a temp module with a page package (User{Name
// string} in types.go, a page.gsx opened via SetOverride with a valid { user.Name
// } body) and returns the warm lspAnalyzer alongside the package dir and the
// page.gsx absolute path. It mirrors the hover e2e fixture's temp-module
// scaffolding (gen/lsp_hover_e2e_test.go) but drives the analyzer directly
// rather than through the JSON-RPC framing, since completion tasks call
// Analyzer methods straight. Grows through later completion tasks.
func newCompletionE2EFixture(t *testing.T) (a lspAnalyzer, dir, pagePath string) {
	t.Helper()
	root := t.TempDir()
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) string {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	write("go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	write("page/types.go", "package page\n\ntype User struct{ Name string }\n")
	pagePath = write("page/page.gsx", "package page\n\ncomponent Home(user User) {\n\t<div>{ user.Name }</div>\n}\n")

	a = newLSPAnalyzer(config{}, io.Discard)
	if _, err := a.SetOverride(pagePath, []byte("package page\n\ncomponent Home(user User) {\n\t<div>{ user.Name }</div>\n}\n")); err != nil {
		t.Fatalf("SetOverride: %v", err)
	}
	dir = filepath.Dir(pagePath)
	return a, dir, pagePath
}

func TestAnalyzeEphemeralViaAnalyzer(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	a, dir, pagePath := newCompletionE2EFixture(t)
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

	generated := filepath.Join(dir, "page.x.go")
	if _, err := os.Stat(generated); !os.IsNotExist(err) {
		t.Fatalf("physical generated file exists: %s", generated)
	}
}
