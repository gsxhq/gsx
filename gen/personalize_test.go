package gen

import (
	"encoding/hex"
	"errors"
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
	for _, line := range strings.Split(env, "\n") {
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
// mentioned in the plan's personalize spec; the shared fixture doesn't ship
// a gsx.toml, so this is a small dedicated fixture.
func TestPersonalizeRewritesGsxToml(t *testing.T) {
	t.Parallel()
	src := fstest.MapFS{
		"go.mod":   &fstest.MapFile{Data: []byte("module github.com/gsxhq/template\n")},
		"gsx.toml": &fstest.MapFile{Data: []byte("[[filter]]\nname = \"markdown\"\nimport = \"github.com/gsxhq/template/filters\"\n")},
	}
	dest := t.TempDir()
	if err := personalize(src, dest, "example.com/myapp"); err != nil {
		t.Fatal(err)
	}
	toml, err := os.ReadFile(filepath.Join(dest, "gsx.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(toml), `"example.com/myapp/filters"`) {
		t.Errorf("gsx.toml filter import not rewritten: %s", toml)
	}
	if strings.Contains(string(toml), "github.com/gsxhq/template") {
		t.Errorf("gsx.toml still references the old module: %s", toml)
	}
}
