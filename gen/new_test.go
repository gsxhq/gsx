package gen

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newNI drives newWith non-interactively (no TTY, no real subprocess) so
// scaffold-style tests never depend on the ambient terminal. workDir anchors
// relative dir resolution (see absAgainst).
func newNI(t *testing.T, workDir string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	noop := func(_ []string, _ string, _, _ io.Writer) error { return nil }
	code := newWith(args, strings.NewReader(""), &out, &errb, false, noop, workDir)
	return code, out.String(), errb.String()
}

func TestNewCreatesDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	code, out, errb := newNI(t, dir, "myapp")
	if code != 0 {
		t.Fatalf("exit %d; stderr=%q", code, errb)
	}
	gomod, err := os.ReadFile(filepath.Join(dir, "myapp", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gomod), "module myapp") {
		t.Fatalf("module not derived from dir basename: %s", gomod)
	}
	if !strings.Contains(out, "npm run dev") {
		t.Fatalf("next steps not printed: %q", out)
	}
}

func TestNewBareNonInteractiveIsUsageError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	code, _, errb := newNI(t, dir)
	if code != 2 {
		t.Fatalf("expected exit 2, got %d; stderr=%q", code, errb)
	}
	if !strings.Contains(errb, "directory") {
		t.Fatalf("expected usage error mentioning a directory argument, got %q", errb)
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
		t.Fatalf("bare non-interactive new must not scaffold into workDir")
	}
}

func TestNewBareInteractivePromptsName(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	calls, run := recordingRunner(-1, nil)
	var out, errb bytes.Buffer
	// "myproj" answers the project-name prompt; the blank line accepts the
	// template picker's default (--template wasn't set); three "y"s run all
	// setup steps.
	code := newWith(nil, strings.NewReader("myproj\n\ny\ny\ny\n"), &out, &errb, true, run, dir)
	if code != 0 {
		t.Fatalf("exit %d; stderr=%q", code, errb.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "myproj", "go.mod")); err != nil {
		t.Fatalf("myproj not scaffolded: %v", err)
	}
	if len(*calls) != 3 {
		t.Fatalf("expected 3 steps, got %d: %v", len(*calls), *calls)
	}
	if !strings.Contains(out.String(), "Project name") {
		t.Fatalf("expected a project-name prompt, got %q", out.String())
	}
	if !strings.Contains(out.String(), "Select a template") {
		t.Fatalf("expected a template picker prompt, got %q", out.String())
	}
}

func TestNewModuleFlag(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	code, _, errb := newNI(t, dir, "--module", "example.com/foo", "myapp")
	if code != 0 {
		t.Fatalf("exit %d; stderr=%q", code, errb)
	}
	gomod, _ := os.ReadFile(filepath.Join(dir, "myapp", "go.mod"))
	if !strings.Contains(string(gomod), "module example.com/foo") {
		t.Fatalf("go.mod = %s", gomod)
	}
	pkg, _ := os.ReadFile(filepath.Join(dir, "myapp", "package.json"))
	if !strings.Contains(string(pkg), "\"name\": \"foo\"") {
		t.Fatalf("package.json name not basename: %s", pkg)
	}
}

func TestNewRefusesExistingProject(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "myapp")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errb := newNI(t, dir, "myapp")
	if code != 2 {
		t.Fatalf("expected exit 2 for existing go.mod, got %d", code)
	}
	if !strings.Contains(errb, "--force") {
		t.Fatalf("error should mention --force: %q", errb)
	}
	// --force proceeds:
	code, _, errb = newNI(t, dir, "--force", "myapp")
	if code != 0 {
		t.Fatalf("--force should succeed, got %d; %q", code, errb)
	}
}

func TestNewFlagsAfterDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	code, _, errb := newNI(t, dir, "myapp", "--module", "example.com/after")
	if code != 0 {
		t.Fatalf("exit %d; stderr=%q", code, errb)
	}
	gomod, _ := os.ReadFile(filepath.Join(dir, "myapp", "go.mod"))
	if !strings.Contains(string(gomod), "module example.com/after") {
		t.Fatalf("flag after dir ignored: go.mod = %s", gomod)
	}
}

// withTestTemplates temporarily overrides the package-level templates
// registry, restoring the original on cleanup. Tests using it mutate global
// state and must NOT call t.Parallel(): Go's testing runner completes every
// non-parallel top-level test before any t.Parallel() test starts, so leaving
// these serial is what keeps them race-free against the rest of the suite.
func withTestTemplates(t *testing.T, tpls map[string]initTemplate) {
	t.Helper()
	orig := templates
	templates = tpls
	t.Cleanup(func() { templates = orig })
}

func TestNewInteractivePicksTemplateByNumber(t *testing.T) {
	withTestTemplates(t, map[string]initTemplate{
		"alpha": {name: "alpha", desc: "First starter.", root: "templates/init/simple", order: 0},
		"beta":  {name: "beta", desc: "Second starter.", root: "templates/init/simple", order: 1},
	})
	var out bytes.Buffer
	reader := bufio.NewReader(strings.NewReader("2\n"))
	got := promptTemplate(reader, &out, "alpha")
	if got != "beta" {
		t.Fatalf("promptTemplate(%q) = %q, want %q (list is order-sorted: alpha=1, beta=2); menu=%q", "2", got, "beta", out.String())
	}
}

func TestNewInteractivePicksByName(t *testing.T) {
	withTestTemplates(t, map[string]initTemplate{
		"alpha": {name: "alpha", desc: "First starter.", root: "templates/init/simple", order: 0},
		"beta":  {name: "beta", desc: "Second starter.", root: "templates/init/simple", order: 1},
	})
	var out bytes.Buffer
	reader := bufio.NewReader(strings.NewReader("alpha\n"))
	got := promptTemplate(reader, &out, "beta")
	if got != "alpha" {
		t.Fatalf("promptTemplate(%q) = %q, want %q", "alpha", got, "alpha")
	}
}

func TestNewInteractiveEmptyUsesDefault(t *testing.T) {
	withTestTemplates(t, map[string]initTemplate{
		"alpha": {name: "alpha", desc: "First starter.", root: "templates/init/simple", order: 0},
		"beta":  {name: "beta", desc: "Second starter.", root: "templates/init/simple", order: 1},
	})
	var out bytes.Buffer
	reader := bufio.NewReader(strings.NewReader("\n"))
	got := promptTemplate(reader, &out, "beta")
	if got != "beta" {
		t.Fatalf("promptTemplate(empty) = %q, want default %q", got, "beta")
	}
}

func TestNewInteractiveInvalidThenFallsBackToDefault(t *testing.T) {
	withTestTemplates(t, map[string]initTemplate{
		"alpha": {name: "alpha", desc: "First starter.", root: "templates/init/simple", order: 0},
		"beta":  {name: "beta", desc: "Second starter.", root: "templates/init/simple", order: 1},
	})
	var out bytes.Buffer
	reader := bufio.NewReader(strings.NewReader("bogus\nstillbogus\n"))
	got := promptTemplate(reader, &out, "beta")
	if got != "beta" {
		t.Fatalf("promptTemplate(invalid twice) = %q, want fallback default %q", got, "beta")
	}
	if !strings.Contains(out.String(), "not a valid template") {
		t.Fatalf("expected a reprompt message after the first invalid answer, got %q", out.String())
	}
}

// TestNewFromLocalFixture is the end-to-end shape the template repo's own CI
// will mirror (`gsx new --from . <tmp>` against its HEAD, per the plan): a
// full newWith run with --from pointed at a real on-disk fixture
// (gen/testdata/newfixture — not an fstest.MapFS, since localTemplateFS uses
// os.DirFS), asserting the scaffolded tree, the rewritten module, the
// manifest-stripped files, the generated .env secret, and the printed
// next-steps block, entirely offline.
//
// --module is given explicitly (unlike the embedded-template tests, which
// happily default to a bare dir-basename module such as "myapp"): personalize
// validates the target module with module.CheckPath, the strict "must be a
// real, publishable path" validator — a bare "myapp" has no dot in its first
// component and is correctly rejected. A fetched template does real import
// rewriting, so it requires a real path; an embedded template's go.mod
// substitution is a no-op string fill with no such requirement.
func TestNewFromLocalFixture(t *testing.T) {
	t.Parallel()
	fixture, err := filepath.Abs("testdata/newfixture")
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	calls, run := recordingRunner(-1, nil)
	var out, errb bytes.Buffer
	code := newWith([]string{"--from", fixture, "--module", "example.com/myapp", "--yes", "myapp"}, strings.NewReader(""), &out, &errb, false, run, dir)
	if code != 0 {
		t.Fatalf("exit %d; stderr=%q", code, errb.String())
	}

	target := filepath.Join(dir, "myapp")

	gomod, err := os.ReadFile(filepath.Join(target, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gomod), "module example.com/myapp") {
		t.Fatalf("go.mod not rewritten to the new module: %s", gomod)
	}

	mainGo, err := os.ReadFile(filepath.Join(target, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mainGo), `"example.com/myapp/pages"`) {
		t.Fatalf("main.go import not rewritten: %s", mainGo)
	}
	if strings.Contains(string(mainGo), "github.com/gsxhq/newfixture") {
		t.Fatalf("main.go still references the fixture's original module: %s", mainGo)
	}

	if _, err := os.Stat(filepath.Join(target, "docs", "README.md")); err == nil {
		t.Fatal("docs/README.md should have been stripped by gsx-template.json")
	}
	if _, err := os.Stat(filepath.Join(target, "gsx-template.json")); err == nil {
		t.Fatal("the gsx-template.json manifest itself should have been stripped")
	}

	env, err := os.ReadFile(filepath.Join(target, ".env"))
	if err != nil {
		t.Fatalf("expected .env with the generated secret: %v", err)
	}
	secret := dotEnvValue(t, string(env), "APP_SECRET")
	if len(secret) != 64 {
		t.Fatalf("APP_SECRET = %q (len %d), want 64 hex chars", secret, len(secret))
	}

	pkg, err := os.ReadFile(filepath.Join(target, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pkg), `"name": "myapp"`) {
		t.Fatalf("package.json name not rewritten: %s", pkg)
	}

	if len(*calls) != 3 {
		t.Fatalf("expected 3 setup steps, got %d: %v", len(*calls), *calls)
	}
	if !strings.Contains(out.String(), "npm run dev") {
		t.Fatalf("next-steps block missing from stdout: %q", out.String())
	}
}

func TestNewTemplateFlagSkipsPicker(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	calls, run := recordingRunner(-1, nil)
	var out, errb bytes.Buffer
	code := newWith([]string{"--template", "simple", "myapp"}, strings.NewReader("y\ny\ny\n"), &out, &errb, true, run, dir)
	if code != 0 {
		t.Fatalf("exit %d; stderr=%q", code, errb.String())
	}
	if strings.Contains(out.String(), "Select a template") {
		t.Fatalf("picker should be skipped when --template is explicit: %q", out.String())
	}
	if len(*calls) != 3 {
		t.Fatalf("expected 3 steps, got %d: %v", len(*calls), *calls)
	}
}
