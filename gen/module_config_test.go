package gen

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// nestedConfigFixture is the layout of gsx issue #200 as it occurs in a real
// repository: a .git at the root, an outer module whose gsx.toml names a
// class_merger living in the outer module, and a nested module (its own
// go.mod) discovered by the same generate walk. The .git matters: config
// discovery walks up to the nearest .git, crossing the nested go.mod, so the
// nested module inherits the outer file unless it has its own. Both modules
// replace the gsx runtime to this checkout.
//
//	root/.git/
//	root/go.mod              module example.com/app
//	root/gsx.toml            class_merger = "example.com/app/merge.Merge"
//	root/merge/merge.go      the merger
//	root/views/hello.gsx     outer-module page (a composable class → merger call)
//	root/nested/go.mod       module example.com/nested
//	root/nested/views/hi.gsx nested-module page (body from nestedView)
//
// nestedConfig, when non-empty, is written as root/nested/gsx.toml.
func nestedConfigFixture(t *testing.T, root, outerConfig, nestedConfig, nestedView string) {
	t.Helper()
	gsxDir := repoRoot(t)
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	mkfile(t, filepath.Join(root, "go.mod"),
		"module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+gsxDir+"\n")
	mkfile(t, filepath.Join(root, "gsx.toml"), outerConfig)
	mkfile(t, filepath.Join(root, "merge", "merge.go"),
		"package merge\n\nimport \"strings\"\n\nfunc Merge(classes []string) string { return strings.Join(classes, \" \") }\n")
	mkfile(t, filepath.Join(root, "views", "hello.gsx"),
		"package views\n\ncomponent Hello(name string, on bool) {\n\t<p class={ \"text-sm\", \"on\": on }>Hello, { name }</p>\n}\n")
	mkfile(t, filepath.Join(root, "nested", "go.mod"),
		"module example.com/nested\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+gsxDir+"\n")
	if nestedConfig != "" {
		mkfile(t, filepath.Join(root, "nested", "gsx.toml"), nestedConfig)
	}
	mkfile(t, filepath.Join(root, "nested", "views", "hi.gsx"), nestedView)
}

const outerMergerConfig = "class_merger = \"example.com/app/merge.Merge\"\n"

// shoutFilterConfig aliases a filter from the gsx runtime's std package, which
// every module requiring gsx can resolve — the structpages shape, where one
// repo-root gsx.toml is shared by every example sub-module.
const shoutFilterConfig = "[filters]\nshout = \"github.com/gsxhq/gsx/std.Upper\"\n"

const nestedShoutView = "package views\n\ncomponent Hi() {\n\t<p class=\"text-sm\">{ \"hi\" |> shout }</p>\n}\n"

const nestedPlainView = "package views\n\ncomponent Hi() {\n\t<p class=\"text-sm\">hi</p>\n}\n"

// TestGenerateNestedModuleOwnConfig proves config resolves per module: a nested
// module with its own gsx.toml is generated against THAT file, not the outer
// module's. The outer file names a class_merger only the outer module can
// import; the nested file aliases a filter the nested view uses. Both must
// generate from one walk started at the outer root.
func TestGenerateNestedModuleOwnConfig(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	root := t.TempDir()
	nestedConfigFixture(t, root, outerMergerConfig, shoutFilterConfig, nestedShoutView)

	var out, errb bytes.Buffer
	if code := run([]string{"-C", root, "generate", "."}, &out, &errb); code != 0 {
		t.Fatalf("run generate exit=%d stderr=%q stdout=%q", code, errb.String(), out.String())
	}
	outer, err := os.ReadFile(filepath.Join(root, "views", "hello.x.go"))
	if err != nil {
		t.Fatalf("outer output: %v", err)
	}
	if !strings.Contains(string(outer), "_gsxcm.Merge") {
		t.Fatalf("outer module must use its configured class merger; got:\n%s", outer)
	}
	nested, err := os.ReadFile(filepath.Join(root, "nested", "views", "hi.x.go"))
	if err != nil {
		t.Fatalf("nested output: %v", err)
	}
	if !strings.Contains(string(nested), "std.Upper") {
		t.Fatalf("nested module must use its own gsx.toml filter alias; got:\n%s", nested)
	}
	if strings.Contains(string(nested), "_gsxcm.Merge") {
		t.Fatalf("nested module must not inherit the outer class_merger; got:\n%s", nested)
	}
}

// TestGenerateNestedModuleInheritsResolvableConfig pins the structpages shape:
// a nested module WITHOUT its own gsx.toml inherits the outer one, and that
// works when everything the outer file names is importable from the nested
// module.
func TestGenerateNestedModuleInheritsResolvableConfig(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	root := t.TempDir()
	nestedConfigFixture(t, root, shoutFilterConfig, "", nestedShoutView)

	var out, errb bytes.Buffer
	if code := run([]string{"-C", root, "generate", "."}, &out, &errb); code != 0 {
		t.Fatalf("run generate exit=%d stderr=%q stdout=%q", code, errb.String(), out.String())
	}
	nested, err := os.ReadFile(filepath.Join(root, "nested", "views", "hi.x.go"))
	if err != nil {
		t.Fatalf("nested output: %v", err)
	}
	if !strings.Contains(string(nested), "std.Upper") {
		t.Fatalf("nested module must inherit the outer filter alias; got:\n%s", nested)
	}
}

// TestGenerateNestedModuleInheritedConfigUnresolvable is issue #200 itself: the
// nested module has no gsx.toml, inherits the outer one, and the outer
// class_merger package is not importable from the nested module. The failure
// must name the inherited file, the module it was applied to, and the fix.
func TestGenerateNestedModuleInheritedConfigUnresolvable(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	root := t.TempDir()
	nestedConfigFixture(t, root, outerMergerConfig, "", nestedPlainView)

	var out, errb bytes.Buffer
	code := run([]string{"-C", root, "generate", "."}, &out, &errb)
	if code != 1 {
		t.Fatalf("run generate exit=%d, want 1; stderr=%q stdout=%q", code, errb.String(), out.String())
	}
	assertInheritedConfigError(t, errb.String(), root)
	// The outer module itself is fine and must still have been generated.
	if _, err := os.Stat(filepath.Join(root, "views", "hello.x.go")); err != nil {
		t.Fatalf("outer module output must be written despite the nested failure: %v", err)
	}
}

// TestGenerateNestedModuleOutsideRepoUsesDefaults is issue #200's literal
// reproduction: no .git anywhere, so discovery for the nested module stops at
// its own go.mod, finds no gsx.toml, and the nested module generates with
// defaults while the outer module keeps its merger.
func TestGenerateNestedModuleOutsideRepoUsesDefaults(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	root := t.TempDir()
	nestedConfigFixture(t, root, outerMergerConfig, "", nestedPlainView)
	if err := os.RemoveAll(filepath.Join(root, ".git")); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	if code := run([]string{"-C", root, "generate", "."}, &out, &errb); code != 0 {
		t.Fatalf("run generate exit=%d stderr=%q stdout=%q", code, errb.String(), out.String())
	}
	outer, err := os.ReadFile(filepath.Join(root, "views", "hello.x.go"))
	if err != nil {
		t.Fatalf("outer output: %v", err)
	}
	if !strings.Contains(string(outer), "_gsxcm.Merge") {
		t.Fatalf("outer module must use its configured class merger; got:\n%s", outer)
	}
	if _, err := os.Stat(filepath.Join(root, "nested", "views", "hi.x.go")); err != nil {
		t.Fatalf("nested output: %v", err)
	}
}

// assertInheritedConfigError checks the loud cross-module config error: the
// inherited gsx.toml, the module it was applied to, the underlying codegen
// failure, and the per-module gsx.toml the user should add.
func assertInheritedConfigError(t *testing.T, msg, root string) {
	t.Helper()
	for _, want := range []string{
		filepath.Join(root, "gsx.toml") + " applies to module example.com/nested",
		"class_merger package \"example.com/app/merge\"",
		"add " + filepath.Join(root, "nested", "gsx.toml"),
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error must mention %q; got:\n%s", want, msg)
		}
	}
}

// TestWatchSessionNestedModuleInheritedConfigUnresolvable is the watch/dev
// counterpart: the session resolves config per module, so the nested module's
// initial generate fails with the same annotated error instead of a bare
// codegen message.
func TestWatchSessionNestedModuleInheritedConfigUnresolvable(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	root := t.TempDir()
	nestedConfigFixture(t, root, outerMergerConfig, "", nestedPlainView)

	cfg := watchConfig{paths: []string{root}, moduleConfig: discoveredModuleConfig(config{}, false)}
	sess, _, err := startWatchSessionForTest(cfg)
	if err != nil {
		t.Fatalf("startWatchSessionForTest: %v", err)
	}
	results, err := sess.initialGenerate()
	if err != nil {
		t.Fatalf("initialGenerate: %v", err)
	}
	var msgs []string
	for _, r := range results {
		if r.Err != nil {
			msgs = append(msgs, r.Err.Error())
		}
	}
	if len(msgs) == 0 {
		t.Fatalf("expected the nested module to fail; results=%+v", results)
	}
	assertInheritedConfigError(t, strings.Join(msgs, "\n"), root)
}

// TestWatchSessionNestedModuleOwnConfig proves the watch session opens each
// module against its own gsx.toml.
func TestWatchSessionNestedModuleOwnConfig(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	root := t.TempDir()
	nestedConfigFixture(t, root, outerMergerConfig, shoutFilterConfig, nestedShoutView)

	cfg := watchConfig{paths: []string{root}, moduleConfig: discoveredModuleConfig(config{}, false)}
	sess, _, err := startWatchSessionForTest(cfg)
	if err != nil {
		t.Fatalf("startWatchSessionForTest: %v", err)
	}
	results, err := sess.initialGenerate()
	if err != nil {
		t.Fatalf("initialGenerate: %v", err)
	}
	for _, r := range results {
		if r.Err != nil {
			t.Fatalf("initial generate %s: %v", r.Dir, r.Err)
		}
	}
	nested, err := os.ReadFile(filepath.Join(root, "nested", "views", "hi.x.go"))
	if err != nil {
		t.Fatalf("nested output: %v", err)
	}
	if !strings.Contains(string(nested), "std.Upper") {
		t.Fatalf("nested module must use its own gsx.toml filter alias; got:\n%s", nested)
	}
}
