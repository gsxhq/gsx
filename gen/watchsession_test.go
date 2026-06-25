package gen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A two-package module: pkg `comp` defines a component; pkg `views` references
// it. The whole-module resolver must let `views` regenerate with the cross-
// package ref resolved (no "cached importer: not loaded" error).
func TestNewModuleResolver_CrossPackage(t *testing.T) {
	root := t.TempDir()
	write := func(p, s string) {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Use the current go toolchain version so packages.Load does not refuse the module.
	// No go mod tidy: with a local replace directive the require+replace is sufficient
	// for packages.Load; tidy would strip "require github.com/gsxhq/gsx" because
	// there are no .go files importing it before the cold generate runs.
	write("go.mod", "module example.com/m\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+gsxModuleDir(t)+"\n")
	write("comp/card.gsx", "package comp\n\ncomponent Card(title string) {\n\t<div class=\"card\">{title}</div>\n}\n")
	write("views/page.gsx", "package views\n\nimport \"example.com/m/comp\"\n\ncomponent Page() {\n\t<comp.Card title=\"hi\"/>\n}\n")

	// Initial cold generate so comp's .x.go exists for the resolver to load.
	if _, err := generateCached([]string{filepath.Join(root, "comp"), filepath.Join(root, "views")}, nil, nil, nil, "", nil, false, nil, nil); err != nil {
		t.Fatalf("initial generate: %v", err)
	}

	r, err := newModuleResolver(root, nil, nil)
	if err != nil {
		t.Fatalf("newModuleResolver: %v", err)
	}
	res, err := r.Generate(filepath.Join(root, "views"), nil)
	if err != nil {
		t.Fatalf("warm Generate(views): %v (diags=%v)", err, res.Diags)
	}
	// The regenerated views/.x.go must call comp.Card.
	var got string
	for path, b := range res.Files {
		if strings.HasSuffix(path, "page.x.go") {
			got = string(b)
		}
	}
	if !strings.Contains(got, "comp.Card") {
		t.Fatalf("regenerated page.x.go does not reference comp.Card:\n%s", got)
	}
}

func mustRun(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

// gsxModuleDir returns the absolute path of this gsx module checkout, for the
// test fixture's replace directive.
func gsxModuleDir(t *testing.T) string {
	t.Helper()
	// gen/ is one level under the module root.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(wd)
}
