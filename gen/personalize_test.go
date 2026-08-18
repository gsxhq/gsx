package gen

import (
	"bytes"
	"encoding/hex"
	"errors"
	"go/format"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// personalizeFixture is the fetched-template fixture used across the
// personalize tests: a go.mod-rooted "repo" whose .go/.gsx files import
// their own module, a package.json, and a gsx-template.json manifest that
// strips docs/* and a specific workflow file and asks for one generated
// secret.
func personalizeFixture() fstest.MapFS {
	return fstest.MapFS{
		"go.mod": &fstest.MapFile{Data: []byte("module github.com/gsxhq/template\n\ngo 1.26\n")},
		"main.go": &fstest.MapFile{Data: []byte(`package main

import (
	"fmt"

	"github.com/gsxhq/template/pages"
)

func main() {
	fmt.Println(pages.Index)
}
`)},
		"pages/x.gsx": &fstest.MapFile{Data: []byte(`package pages

import "github.com/gsxhq/template/pages"

templ Index() {
	<div>hi</div>
}
`)},
		"package.json": &fstest.MapFile{Data: []byte(`{
  "name": "template",
  "version": "0.0.0",
  "scripts": {
    "dev": "vite"
  }
}
`)},
		"gsx-template.json": &fstest.MapFile{Data: []byte(`{
  "strip": ["docs/*", ".github/workflows/deploy-demo.yml"],
  "env": {"SESSION_SECRET": "secret-hex-32"}
}
`)},
		"docs/README.md":                    &fstest.MapFile{Data: []byte("# template docs\n")},
		".github/workflows/deploy-demo.yml": &fstest.MapFile{Data: []byte("name: deploy-demo\n")},
	}
}

func TestPersonalizeRewritesModulePaths(t *testing.T) {
	t.Parallel()
	dest := t.TempDir()
	if err := personalize(personalizeFixture(), dest, "example.com/myapp"); err != nil {
		t.Fatal(err)
	}

	gomod, err := os.ReadFile(filepath.Join(dest, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gomod), "module example.com/myapp") {
		t.Errorf("go.mod not rewritten: %s", gomod)
	}

	mainGo, err := os.ReadFile(filepath.Join(dest, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mainGo), `"example.com/myapp/pages"`) {
		t.Errorf("main.go import not rewritten: %s", mainGo)
	}

	pagesX, err := os.ReadFile(filepath.Join(dest, "pages", "x.gsx"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pagesX), `"example.com/myapp/pages"`) {
		t.Errorf("pages/x.gsx import not rewritten: %s", pagesX)
	}

	pkg, err := os.ReadFile(filepath.Join(dest, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pkg), `"name": "myapp"`) {
		t.Errorf("package.json name not rewritten: %s", pkg)
	}

	// No trace of the old module path anywhere in the scaffolded output.
	_ = filepath.Walk(dest, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), "github.com/gsxhq/template") {
			t.Errorf("old module path leaked into %s: %s", p, b)
		}
		return nil
	})
}

func TestPersonalizeStripsManifestGlobs(t *testing.T) {
	t.Parallel()
	dest := t.TempDir()
	if err := personalize(personalizeFixture(), dest, "example.com/myapp"); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"docs/README.md", ".github/workflows/deploy-demo.yml", "gsx-template.json"} {
		if _, err := os.Stat(filepath.Join(dest, rel)); err == nil {
			t.Errorf("%s should have been stripped", rel)
		} else if !os.IsNotExist(err) {
			t.Errorf("%s: unexpected stat error: %v", rel, err)
		}
	}
	for _, rel := range []string{"go.mod", "main.go", filepath.Join("pages", "x.gsx"), "package.json"} {
		if _, err := os.Stat(filepath.Join(dest, rel)); err != nil {
			t.Errorf("%s should not have been stripped: %v", rel, err)
		}
	}
}

func dotEnvValue(t *testing.T, env, key string) string {
	t.Helper()
	for line := range strings.SplitSeq(env, "\n") {
		if k, v, ok := strings.Cut(line, "="); ok && k == key {
			return v
		}
	}
	t.Fatalf("%s not found in .env: %q", key, env)
	return ""
}

func TestPersonalizeGeneratesSecrets(t *testing.T) {
	t.Parallel()
	dest1 := t.TempDir()
	if err := personalize(personalizeFixture(), dest1, "example.com/myapp"); err != nil {
		t.Fatal(err)
	}
	env1, err := os.ReadFile(filepath.Join(dest1, ".env"))
	if err != nil {
		t.Fatalf("expected .env to be created: %v", err)
	}
	secret1 := dotEnvValue(t, string(env1), "SESSION_SECRET")
	if len(secret1) != 64 {
		t.Fatalf("SESSION_SECRET = %q (len %d), want 64 hex chars", secret1, len(secret1))
	}
	if _, err := hex.DecodeString(secret1); err != nil {
		t.Fatalf("SESSION_SECRET is not valid hex: %v", err)
	}

	dest2 := t.TempDir()
	if err := personalize(personalizeFixture(), dest2, "example.com/myapp"); err != nil {
		t.Fatal(err)
	}
	env2, err := os.ReadFile(filepath.Join(dest2, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	secret2 := dotEnvValue(t, string(env2), "SESSION_SECRET")
	if secret1 == secret2 {
		t.Fatalf("two personalize runs produced the same secret: %s", secret1)
	}
}

// TestPersonalizeInvalidModulePath pins personalize's rejection of genuinely
// malformed module input. The fixture string's spaces and "!" are outside
// module.CheckImportPath's allowed path-element characters (ASCII
// letters/digits and -._~ only — see `go doc golang.org/x/mod/module
// CheckImportPath`), so it stays invalid under the looser CheckImportPath
// validator personalize now uses (previously module.CheckPath, which also
// rejected it, but additionally rejected any bare, non-dotted module name —
// see TestPersonalizeAcceptsBareModuleName for the case that distinguishes
// the two).
func TestPersonalizeInvalidModulePath(t *testing.T) {
	t.Parallel()
	dest := t.TempDir()
	err := personalize(personalizeFixture(), dest, "not a valid module path!!")
	if err == nil {
		t.Fatal("expected an error for an invalid module path")
	}
	var ime *invalidModuleError
	if !errors.As(err, &ime) {
		t.Fatalf("expected an *invalidModuleError, got %T: %v", err, err)
	}
}

// TestPersonalizeAcceptsBareModuleName pins the CRITICAL fix: a bare,
// non-dotted module name like "myapp" — exactly what `gsx new myapp` derives
// as the default module with zero flags — must be accepted for a fetched or
// --from template, the same as it already is for the embedded templates.
// module.CheckPath (personalize's validator before this fix) rejected this;
// module.CheckImportPath (what `go mod init myapp` itself effectively
// accepts) does not.
func TestPersonalizeAcceptsBareModuleName(t *testing.T) {
	t.Parallel()
	dest := t.TempDir()
	if err := personalize(personalizeFixture(), dest, "myapp"); err != nil {
		t.Fatalf("personalize(%q) should succeed for a bare module name: %v", "myapp", err)
	}
	gomod, err := os.ReadFile(filepath.Join(dest, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gomod), "module myapp") {
		t.Fatalf("go.mod = %s", gomod)
	}
	mainGo, err := os.ReadFile(filepath.Join(dest, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mainGo), `"myapp/pages"`) {
		t.Fatalf("main.go import not rewritten to the bare module: %s", mainGo)
	}
}

// TestPersonalizePreservesExecBit pins mode preservation: a template's
// executable script keeps its exec bit through personalize, and a
// tightly-permissioned source file is still floored at 0o644 (so the
// scaffold is never left with an unreadable file). This only exercises
// fstest.MapFS's own Mode field — a real module zip fetched from the proxy
// carries no permission bits at all (golang.org/x/mod/zip's format docs:
// "File permissions and timestamps are ignored"), so this behavior is
// observable in practice only for a --from local checkout (os.DirFS, which
// does report real host permission bits).
func TestPersonalizePreservesExecBit(t *testing.T) {
	t.Parallel()
	src := fstest.MapFS{
		"go.mod":     &fstest.MapFile{Data: []byte("module example.com/scripts\n"), Mode: 0o644},
		"run.sh":     &fstest.MapFile{Data: []byte("#!/bin/sh\necho hi\n"), Mode: 0o755},
		"secret.txt": &fstest.MapFile{Data: []byte("shh\n"), Mode: 0o600},
	}
	dest := t.TempDir()
	if err := personalize(src, dest, "example.com/renamed"); err != nil {
		t.Fatal(err)
	}

	runInfo, err := os.Stat(filepath.Join(dest, "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if runInfo.Mode().Perm()&0o111 == 0 {
		t.Errorf("run.sh lost its exec bit: mode = %v", runInfo.Mode())
	}

	secretInfo, err := os.Stat(filepath.Join(dest, "secret.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if secretInfo.Mode().Perm()&0o644 != 0o644 {
		t.Errorf("secret.txt should be floored at 0o644, got %v", secretInfo.Mode())
	}
}

func TestPersonalizeNoManifest(t *testing.T) {
	t.Parallel()
	src := fstest.MapFS{
		"go.mod":  &fstest.MapFile{Data: []byte("module example.com/bare\n")},
		"main.go": &fstest.MapFile{Data: []byte("package main\n\nfunc main() {}\n")},
	}
	dest := t.TempDir()
	if err := personalize(src, dest, "example.com/renamed"); err != nil {
		t.Fatalf("personalize with no manifest: %v", err)
	}
	gomod, err := os.ReadFile(filepath.Join(dest, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gomod), "module example.com/renamed") {
		t.Errorf("go.mod = %s", gomod)
	}
	if _, err := os.Stat(filepath.Join(dest, ".env")); !os.IsNotExist(err) {
		t.Errorf("no .env should be created when there is no env manifest (stat err = %v)", err)
	}
}

// TestPersonalizeRewritesGsxToml pins the gsx.toml filter-reference rewrite
// mentioned in the plan's personalize spec, at both the src root AND nested
// under a subdirectory — personalize walks the whole tree for gsx.toml
// (path.Base(p) == "gsx.toml"), not just a root-only check, so a template
// with more than one gsx.toml (a subproject, an example app under examples/,
// etc.) gets every one rewritten. The shared fixture doesn't ship a
// gsx.toml, so this is a small dedicated fixture.
func TestPersonalizeRewritesGsxToml(t *testing.T) {
	t.Parallel()
	src := fstest.MapFS{
		"go.mod":                &fstest.MapFile{Data: []byte("module github.com/gsxhq/template\n")},
		"gsx.toml":              &fstest.MapFile{Data: []byte("[[filter]]\nname = \"markdown\"\nimport = \"github.com/gsxhq/template/filters\"\n")},
		"examples/app/gsx.toml": &fstest.MapFile{Data: []byte("[[filter]]\nname = \"markdown\"\nimport = \"github.com/gsxhq/template/filters\"\n")},
	}
	dest := t.TempDir()
	if err := personalize(src, dest, "example.com/myapp"); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"gsx.toml", filepath.Join("examples", "app", "gsx.toml")} {
		toml, err := os.ReadFile(filepath.Join(dest, rel))
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		if !strings.Contains(string(toml), `"example.com/myapp/filters"`) {
			t.Errorf("%s filter import not rewritten: %s", rel, toml)
		}
		if strings.Contains(string(toml), "github.com/gsxhq/template") {
			t.Errorf("%s still references the old module: %s", rel, toml)
		}
	}
}

// TestPersonalizeFormatsGoFiles pins the fix for a real, downstream-found
// defect: personalize's import rewrite is a byte-level `"<old` -> `"<new`
// replace (see the oldQuoted/newQuoted comment on personalize), which is
// correct for *finding* the import but has no notion of import ordering. The
// fixture below mirrors the failure as it was actually found — a flagship
// template's pages/projects.go, gofmt-clean under its own module path,
// scaffolded to a *.go file that failed the scaffold's own gofmt gate.
//
// The reproduction needs the own-module import to sort strictly between two
// unrelated imports in the same gofmt group under the OLD module, and to
// sort somewhere else under the NEW one:
//
//   - old module "github.com/gsxhq/template" sorts between
//     "github.com/aaa/pkg" and "github.com/zzz/other" — so the block below is
//     gofmt-clean as shipped in the template.
//   - new module "example.com/n" sorts before both (an 'e' beats a 'g'), so
//     a byte-level rewrite leaves "example.com/n/pages/nav" sitting in the
//     OLD (now wrong) position, second rather than first.
//
// The assertion — personalized output is byte-identical to format.Source of
// itself — is the general gofmt-clean check the fix promises for every *.go
// file, not just this one shape of reordering.
func TestPersonalizeFormatsGoFiles(t *testing.T) {
	t.Parallel()
	const oldModule = "github.com/gsxhq/template"
	const newModule = "example.com/n"
	src := fstest.MapFS{
		"go.mod": &fstest.MapFile{Data: []byte("module " + oldModule + "\n\ngo 1.26\n")},
		"pages/projects.go": &fstest.MapFile{Data: []byte(`package pages

import (
	"fmt"

	"github.com/aaa/pkg"
	"` + oldModule + `/pages/nav"
	"github.com/zzz/other"
)

func Index() string {
	return fmt.Sprint(pkg.X, nav.Y, other.Z)
}
`)},
	}
	dest := t.TempDir()
	if err := personalize(src, dest, newModule); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(filepath.Join(dest, "pages", "projects.go"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := format.Source(got)
	if err != nil {
		t.Fatalf("format.Source(got): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("personalized pages/projects.go is not gofmt-clean:\n--- got ---\n%s\n--- gofmt ---\n%s", got, want)
	}
}

func TestReplaceModuleRefsBoundary(t *testing.T) {
	t.Parallel()
	const old = "example.com/app"
	in := `import (
	"example.com/app"
	"example.com/app/pages"
	"example.com/apple/x"
	"example.com/app-extras/y"
	"example.com/apps/z"
	"other.com/example.com/app"
)
const s = "example.com/app is great"
const u = "https://example.com/app"
`
	got := string(replaceModuleRefs([]byte(in), old, "myapp"))
	want := `import (
	"myapp"
	"myapp/pages"
	"example.com/apple/x"
	"example.com/app-extras/y"
	"example.com/apps/z"
	"other.com/example.com/app"
)
const s = "example.com/app is great"
const u = "https://example.com/app"
`
	if got != want {
		t.Fatalf("replaceModuleRefs:\n got: %s\nwant: %s", got, want)
	}
	// Untouched input is returned as-is.
	if out := replaceModuleRefs([]byte("nothing here"), old, "x"); string(out) != "nothing here" {
		t.Fatalf("no-op rewrite changed bytes: %q", out)
	}
	// Prefix at the very end of the input (no terminator) is left alone.
	if out := replaceModuleRefs([]byte(`"example.com/app`), old, "x"); string(out) != `"example.com/app` {
		t.Fatalf("unterminated ref rewritten: %q", out)
	}
}

func TestPersonalizeOmitsVCSSubmodulesAndSymlinks(t *testing.T) {
	t.Parallel()
	src := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(src, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/tpl\n\ngo 1.26\n")
	write("main.go", "package main\n")
	write(".git/HEAD", "ref: refs/heads/main\n")
	write(".git/objects/ab/cd", "x")
	write("sub/go.mod", "module example.com/tpl/sub\n")
	write("sub/x.go", "package sub\n")
	write("vendor/modules.txt", "# x\n")
	write("vendor/example.com/dep/dep.go", "package dep\n")
	write("real/data.txt", "hello\n")
	if err := os.Symlink(filepath.Join(src, "real"), filepath.Join(src, "linkdir")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Join(src, "main.go"), filepath.Join(src, "linkfile.go")); err != nil {
		t.Fatal(err)
	}

	dest := t.TempDir()
	if err := personalize(os.DirFS(src), dest, "myapp"); err != nil {
		t.Fatalf("personalize: %v", err)
	}
	for _, rel := range []string{".git/HEAD", ".git", "sub/x.go", "sub/go.mod", "vendor/modules.txt", "vendor/example.com/dep/dep.go", "linkdir", "linkfile.go"} {
		if _, err := os.Lstat(filepath.Join(dest, filepath.FromSlash(rel))); err == nil {
			t.Errorf("%s should have been omitted from the scaffold", rel)
		}
	}
	for _, rel := range []string{"go.mod", "main.go", "real/data.txt"} {
		if _, err := os.Stat(filepath.Join(dest, filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s should have been copied: %v", rel, err)
		}
	}
}

func TestShouldStripSubtreeAndGlobs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		pattern string
		rel     string
		want    bool
	}{
		{"docs/*", "docs/README.md", true},
		{"docs/*", "docs/guide/intro.md", true},
		{"docs/", "docs/guide/intro.md", true},
		{"docs", "docs/guide/intro.md", true},
		{"docs", "docsx/a.md", false},
		{"*.md", "README.md", true},
		{"*.md", "docs/README.md", false},
		{"**/*.md", "docs/a/b.md", false}, // path.Match has no **: two stars are one segment
		{".github/", ".github/workflows/ci.yml", true},
		{"", "anything", false},
	}
	for _, c := range cases {
		got := shouldStrip(c.rel, templateManifest{Strip: []string{c.pattern}})
		if got != c.want {
			t.Errorf("shouldStrip(%q, pattern %q) = %v, want %v", c.rel, c.pattern, got, c.want)
		}
	}
	if !shouldStrip("gsx-template.json", templateManifest{}) {
		t.Error("manifest itself must always be stripped")
	}
}

func TestRewritePackageJSONPreservesDocument(t *testing.T) {
	t.Parallel()
	in := "{\n  \"private\": true,\n  \"name\":   \"old-name\",\n  \"scripts\": {\n    \"build\": \"tsc && vite build <x>\"\n  },\n  \"version\": \"1.0.0\"\n}\n"
	got, err := rewritePackageJSON([]byte(in), "github.com/me/myapp")
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"private\": true,\n  \"name\":   \"myapp\",\n  \"scripts\": {\n    \"build\": \"tsc && vite build <x>\"\n  },\n  \"version\": \"1.0.0\"\n}\n"
	if string(got) != want {
		t.Fatalf("rewritePackageJSON:\n got: %s\nwant: %s", got, want)
	}

	// No top-level "name": one is inserted; a nested "name" is not mistaken
	// for it.
	in2 := "{\n  \"scripts\": {\"name\": \"nested\"}\n}\n"
	got2, err := rewritePackageJSON([]byte(in2), "myapp")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(got2), "{\n  \"name\": \"myapp\",") || !strings.Contains(string(got2), `"name": "nested"`) {
		t.Fatalf("insert path:\n%s", got2)
	}
	if _, err := rewritePackageJSON([]byte("[]"), "myapp"); err == nil {
		t.Fatal("array document should be rejected")
	}
}
