package codegen

import (
	"flag"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/parser"
)

var update = flag.Bool("update", false, "update golden files")

const greetingSrc = `package examples

component Greeting(name string, count int) {
	<p>Hello, {name}! You have {count} messages.</p>
}
`

func generate(t *testing.T, src string) string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "greeting.gsx", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out, err := Generate(file)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	return string(out)
}

// TestGenerateSource checks the generated .x.go source (the lowering shape)
// against a golden file. Run with -update to regenerate.
func TestGenerateSource(t *testing.T) {
	got := generate(t, greetingSrc)
	const golden = "testdata/greeting.x.go.golden"
	if *update {
		if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	wantBytes, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden (run with -update first): %v", err)
	}
	if got != string(wantBytes) {
		t.Fatalf("generated source mismatch (run -update to accept):\n--- got ---\n%s", got)
	}
}

// TestRenderEndToEnd compiles and runs the generated code against the real
// runtime and asserts the rendered HTML — the seed of render.golden.
func TestRenderEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping go-run render test in -short mode")
	}
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}

	gen := generate(t, greetingSrc)
	// Reuse the generated decls in a package main harness.
	genMain := strings.Replace(gen, "package examples", "package main", 1)

	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module gsxrender\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	writeFile(t, dir, "greeting.go", genMain)
	writeFile(t, dir, "main.go", `package main

import (
	"context"
	"os"
)

func main() {
	_ = Greeting(GreetingProps{Name: "World", Count: 3}).Render(context.Background(), os.Stdout)
}
`)

	cmd := exec.Command("go", "run", ".")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run failed: %v\n%s", err, out)
	}
	want := "<p>Hello, World! You have 3 messages.</p>"
	if string(out) != want {
		t.Fatalf("rendered HTML mismatch:\n got %q\nwant %q", string(out), want)
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
