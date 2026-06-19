package codegen

import (
	"flag"
	"go/token"
	"os"
	"path/filepath"
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
	got := renderGSX(t, greetingSrc, `Greeting(GreetingProps{Name: "World", Count: 3})`)
	assertHTMLEqual(t, got, "<p>Hello, World! You have 3 messages.</p>")
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
