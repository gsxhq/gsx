package gen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAnalyzeModuleCrossPkg(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip()
	}
	root := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	must := func(p, c string) {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	must("components/input.gsx", "package components\n\ncomponent Input(name string) {\n\t<input name={ name }/>\n}\n")
	must("post.gsx", "package x\n\nimport \"example.com/x/components\"\n\ncomponent Post() {\n\t<main><components.Input name=\"a\"/></main>\n}\n")
	must("use.go", "package x\n\nimport \"example.com/x/components\"\n\nfunc use() { _ = components.Input }\n")

	componentsDir := filepath.Join(root, "components")
	refs, err := (lspAnalyzer{}).AnalyzeModule(componentsDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, cr := range refs {
		if cr.Name == "Input" {
			for _, r := range cr.Refs {
				files = append(files, filepath.Base(r.Filename))
			}
		}
	}
	got := strings.Join(files, ",")
	if !strings.Contains(got, "post.gsx") || !strings.Contains(got, "use.go") {
		t.Errorf("AnalyzeModule Input refs missing cross-pkg sites; got %q", got)
	}
}
