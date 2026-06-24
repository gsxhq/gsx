package gen

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// initNI drives initWith non-interactively (no TTY, no real subprocess) so
// scaffold-style tests never depend on the ambient terminal.
func initNI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	noop := func(_ []string, _ string, _, _ io.Writer) error { return nil }
	code := initWith(args, strings.NewReader(""), &out, &errb, false, noop)
	return code, out.String(), errb.String()
}

// recordingRunner records the commands it runs and returns the scripted error
// for the failAt-th call (failAt < 0 ⇒ never fail).
func recordingRunner(failAt int, failErr error) (*[][]string, stepRunner) {
	var calls [][]string
	run := func(args []string, dir string, stdout, stderr io.Writer) error {
		calls = append(calls, args)
		if failAt >= 0 && len(calls)-1 == failAt {
			return failErr
		}
		return nil
	}
	return &calls, run
}

func TestInitInteractiveRunsAllSteps(t *testing.T) {
	dir := t.TempDir()
	calls, run := recordingRunner(-1, nil)
	var out, errb bytes.Buffer
	code := initWith([]string{"--module", "demo", dir},
		strings.NewReader("y\ny\ny\n"), &out, &errb, true, run)
	if code != 0 {
		t.Fatalf("exit %d; stderr=%q", code, errb.String())
	}
	if len(*calls) != 3 {
		t.Fatalf("expected 3 steps, got %d: %v", len(*calls), *calls)
	}
	if (*calls)[0][1] != "get" || (*calls)[2][0] != "npm" {
		t.Fatalf("unexpected order: %v", *calls)
	}
	if !strings.Contains(out.String(), "task dev") {
		t.Fatalf("final 'task dev' missing: %q", out.String())
	}
}

func TestInitInteractiveSkipsOnNo(t *testing.T) {
	dir := t.TempDir()
	calls, run := recordingRunner(-1, nil)
	var out, errb bytes.Buffer
	code := initWith([]string{"--module", "demo", dir},
		strings.NewReader("n\ny\ny\n"), &out, &errb, true, run)
	if code != 0 {
		t.Fatalf("exit %d; %q", code, errb.String())
	}
	if len(*calls) != 2 {
		t.Fatalf("expected 2 steps (one skipped), got %d: %v", len(*calls), *calls)
	}
	if (*calls)[0][0] != "go" || (*calls)[0][1] != "mod" {
		t.Fatalf("first run should be `go mod tidy`, got %v", (*calls)[0])
	}
}

func TestInitInteractiveStopsOnFailure(t *testing.T) {
	dir := t.TempDir()
	calls, run := recordingRunner(1, fmt.Errorf("boom")) // 2nd step fails
	var out, errb bytes.Buffer
	code := initWith([]string{"--module", "demo", dir},
		strings.NewReader("y\ny\ny\n"), &out, &errb, true, run)
	if code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if len(*calls) != 2 {
		t.Fatalf("should stop after the failing step, ran %d", len(*calls))
	}
	if !strings.Contains(errb.String(), "npm install") {
		t.Fatalf("remaining steps not printed: %q", errb.String())
	}
}

func TestInitYesRunsWithoutPrompt(t *testing.T) {
	dir := t.TempDir()
	calls, run := recordingRunner(-1, nil)
	var out, errb bytes.Buffer
	// interactive=false but --yes ⇒ run all, no stdin consumed.
	code := initWith([]string{"--yes", "--module", "demo", dir},
		strings.NewReader(""), &out, &errb, false, run)
	if code != 0 {
		t.Fatalf("exit %d; %q", code, errb.String())
	}
	if len(*calls) != 3 {
		t.Fatalf("--yes should run all 3 steps, got %d", len(*calls))
	}
}

func TestInitDefault(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "myapp")
	code, out, errb := initNI(t, target)
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
	code, _, errb := initNI(t, "--module", "example.com/foo", dir)
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
	code, _, errb := initNI(t, "--template", "bogus", dir)
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
	code, _, errb := initNI(t, dir)
	if code != 2 {
		t.Fatalf("expected exit 2 for existing go.mod, got %d", code)
	}
	if !strings.Contains(errb, "--force") {
		t.Fatalf("error should mention --force: %q", errb)
	}
	// --force proceeds:
	code, _, errb = initNI(t, "--force", dir)
	if code != 0 {
		t.Fatalf("--force should succeed, got %d; %q", code, errb)
	}
}

func TestInitRefusesExistingPackageJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errb := initNI(t, dir)
	if code != 2 {
		t.Fatalf("expected exit 2 for existing package.json, got %d", code)
	}
	if !strings.Contains(errb, "--force") {
		t.Fatalf("error should mention --force: %q", errb)
	}
}

func TestInitFlagsAfterDir(t *testing.T) {
	dir := t.TempDir()
	code, _, errb := initNI(t, dir, "--module", "example.com/after")
	if code != 0 {
		t.Fatalf("exit %d; stderr=%q", code, errb)
	}
	gomod, _ := os.ReadFile(filepath.Join(dir, "go.mod"))
	if !strings.Contains(string(gomod), "module example.com/after") {
		t.Fatalf("flag after dir ignored: go.mod = %s", gomod)
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
		"Taskfile.yml", "web/main.js", "web/style.css", "web/counter.js", "dist/.gitkeep",
		".gitignore", "README.md", ".env.example", ".env", "logos.go",
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

func TestInitTaskfileParses(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping Taskfile-parse gate in -short mode")
	}
	if _, err := exec.LookPath("task"); err != nil {
		t.Skip("task not on PATH")
	}

	dir := t.TempDir()
	if code, _, errb := initNI(t, "--module", "taskfiledemo", dir); code != 0 {
		t.Fatalf("init failed: %d %s", code, errb)
	}

	cmd := exec.Command("task", "--list")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("task --list failed: %v\n%s", err, out)
	}
}

func TestInitScaffoldCompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping scaffold-compiles integration test in -short mode")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	// Locate the local gsx module root (two dirs up from this test file's pkg).
	gsxRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	if code, _, errb := initNI(t, "--module", "gsxinitdemo", dir); code != 0 {
		t.Fatalf("init failed: %d %s", code, errb)
	}

	// GOFLAGS=-mod=mod lets `go` auto-add the gsx require (resolved via the
	// replace below) and update go.sum, instead of erroring under -mod=readonly.
	run := func(name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v failed: %v\n%s", name, args, err, out)
		}
	}
	// Point gsx at the in-progress local checkout (it is not tagged yet); vite +
	// wgo resolve from the proxy at the versions pinned in the template go.mod.
	run("go", "mod", "edit", "-replace", "github.com/gsxhq/gsx="+gsxRoot)
	// Generate .x.go from app.gsx (go run resolves cmd/gsx via the replace),
	// then build the whole module.
	run("go", "run", "github.com/gsxhq/gsx/cmd/gsx", "generate", ".")
	run("go", "build", "./...")
}
