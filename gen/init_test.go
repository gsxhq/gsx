package gen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestInitDefault(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "myapp")
	code, out, errb := runCapture(t, []string{"init", target})
	if code != 0 {
		t.Fatalf("exit %d; stderr=%q", code, errb)
	}
	gomod, err := os.ReadFile(filepath.Join(target, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gomod), "module myapp") {
		t.Fatalf("module not derived from dir basename: %s", gomod)
	}
	if !strings.Contains(out, "task dev") {
		t.Fatalf("next steps not printed: %q", out)
	}
}

func TestInitModuleFlag(t *testing.T) {
	dir := t.TempDir()
	code, _, errb := runCapture(t, []string{"init", "--module", "example.com/foo", dir})
	if code != 0 {
		t.Fatalf("exit %d; stderr=%q", code, errb)
	}
	gomod, _ := os.ReadFile(filepath.Join(dir, "go.mod"))
	if !strings.Contains(string(gomod), "module example.com/foo") {
		t.Fatalf("go.mod = %s", gomod)
	}
	pkg, _ := os.ReadFile(filepath.Join(dir, "package.json"))
	if !strings.Contains(string(pkg), "\"name\": \"foo\"") {
		t.Fatalf("package.json name not basename: %s", pkg)
	}
}

func TestInitUnknownTemplate(t *testing.T) {
	dir := t.TempDir()
	code, _, errb := runCapture(t, []string{"init", "--template", "bogus", dir})
	if code != 2 {
		t.Fatalf("expected exit 2, got %d", code)
	}
	if !strings.Contains(errb, "simple") {
		t.Fatalf("did not list available templates: %q", errb)
	}
}

func TestInitRefusesExistingProject(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errb := runCapture(t, []string{"init", dir})
	if code != 2 {
		t.Fatalf("expected exit 2 for existing go.mod, got %d", code)
	}
	if !strings.Contains(errb, "--force") {
		t.Fatalf("error should mention --force: %q", errb)
	}
	// --force proceeds:
	code, _, errb = runCapture(t, []string{"init", "--force", dir})
	if code != 0 {
		t.Fatalf("--force should succeed, got %d; %q", code, errb)
	}
}

func TestScaffoldRendersAndTransforms(t *testing.T) {
	src := fstest.MapFS{
		"tpl/go.mod.tmpl":      {Data: []byte("module «.Module»\n")},
		"tpl/app.gsx":          {Data: []byte("{{ x := 1 }}<p>«.Name»</p>")},
		"tpl/web/main.js":      {Data: []byte("import \"./style.css\";\n")},
		"tpl/dist/dot-gitkeep": {Data: []byte("")},
		"tpl/dot-gitignore":    {Data: []byte("/node_modules\n")},
	}
	dest := t.TempDir()
	if err := scaffold(src, "tpl", dest, tmplData{Module: "github.com/x/myapp", Name: "myapp"}, false); err != nil {
		t.Fatal(err)
	}

	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(dest, rel))
		if err != nil {
			t.Fatalf("missing %s: %v", rel, err)
		}
		return string(b)
	}
	// «.Module» substituted, .tmpl stripped:
	if got := read("go.mod"); got != "module github.com/x/myapp\n" {
		t.Fatalf("go.mod = %q", got)
	}
	// gsx {{ }} preserved (custom delims), «.Name» substituted:
	if got := read("app.gsx"); got != "{{ x := 1 }}<p>myapp</p>" {
		t.Fatalf("app.gsx = %q", got)
	}
	// verbatim file unchanged:
	if got := read("web/main.js"); got != "import \"./style.css\";\n" {
		t.Fatalf("main.js = %q", got)
	}
	// dot- → .  (both at root and nested):
	if got := read(".gitignore"); got != "/node_modules\n" {
		t.Fatalf(".gitignore = %q", got)
	}
	read("dist/.gitkeep") // exists
}

func TestTransformName(t *testing.T) {
	cases := map[string]string{
		"go.mod.tmpl":      "go.mod",
		"main.go.tmpl":     "main.go",
		"app.gsx":          "app.gsx",
		"dot-gitignore":    ".gitignore",
		"dist/dot-gitkeep": "dist/.gitkeep",
		"web/style.css":    "web/style.css",
	}
	for in, want := range cases {
		if got := transformName(in); got != filepath.FromSlash(want) {
			t.Errorf("transformName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRenderCustomDelims(t *testing.T) {
	// «» substituted; {{ }} and { } left alone.
	out, err := render([]byte("«.Name»: {{ go }} and { x }"), tmplData{Name: "n"})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "n: {{ go }} and { x }" {
		t.Fatalf("render = %q", out)
	}
}

func TestScaffoldSimpleTemplate(t *testing.T) {
	dest := t.TempDir()
	tpl := templates[defaultTemplate]
	if err := scaffold(initFS, tpl.root, dest, tmplData{Module: "example.com/demo", Name: "demo"}, false); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{
		"go.mod", "main.go", "app.gsx", "vite.config.ts", "package.json",
		"Taskfile.yml", "web/main.js", "web/style.css", "dist/.gitkeep",
		".gitignore", "README.md",
	} {
		if _, err := os.Stat(filepath.Join(dest, rel)); err != nil {
			t.Errorf("missing scaffolded file %s: %v", rel, err)
		}
	}
	gomod, _ := os.ReadFile(filepath.Join(dest, "go.mod"))
	if !strings.Contains(string(gomod), "module example.com/demo") {
		t.Errorf("go.mod missing substituted module: %s", gomod)
	}
	// No unrendered delimiters leaked anywhere:
	_ = filepath.Walk(dest, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		b, _ := os.ReadFile(p)
		if strings.ContainsAny(string(b), "«»") {
			t.Errorf("stray delimiter in %s", p)
		}
		return nil
	})
	// app.gsx kept its gsx statement block:
	appgsx, _ := os.ReadFile(filepath.Join(dest, "app.gsx"))
	if !strings.Contains(string(appgsx), "{{ assets := vite.FromContext(ctx).Entry") {
		t.Errorf("app.gsx lost its {{ }} block: %s", appgsx)
	}
}
