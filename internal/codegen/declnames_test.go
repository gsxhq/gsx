package codegen

import (
	"go/token"
	"os"
	"path/filepath"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
	gsxparser "github.com/gsxhq/gsx/parser"
)

func parseGSXForTest(t *testing.T, src string) *gsxast.File {
	t.Helper()
	f, err := gsxparser.ParseFile(token.NewFileSet(), "test.gsx", []byte(src), 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return f
}

func TestPackageDeclNames(t *testing.T) {
	dir := t.TempDir()
	// Hand-written .go: func, method, var, type, const, import.
	if err := os.WriteFile(filepath.Join(dir, "helpers.go"), []byte(`package views

import "time"

func data() string { return time.Now().String() }
func (p page) method() {}
var count int
type page struct{}
const limit = 3
`), 0o644); err != nil {
		t.Fatal(err)
	}
	// _test.go and .x.go must be skipped.
	os.WriteFile(filepath.Join(dir, "x_test.go"), []byte("package views\nfunc testOnly() {}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "gen.x.go"), []byte("package views\nfunc generated() {}\n"), 0o644)

	// The element-free Go run (chunkFunc + chunkLimit) parses as a GoChunk;
	// the run holding help + card2 has embedded elements and parses as a
	// GoWithElements. The component between them keeps the runs separate —
	// contiguous top-level Go merges into a single decl.
	gsx := parseGSXForTest(t, `package views

component card() {
	<div>x</div>
}

func chunkFunc() string { return "" }

const chunkLimit = 9

component (p page) row() {
	<li>x</li>
}

var help = <a href="/help">?</a>

func card2() any { return <div>x</div> }
`)
	got := packageDeclNames(dir, map[string]*gsxast.File{"a.gsx": gsx})

	for _, want := range []string{"data", "count", "page", "limit", "card", "chunkFunc", "chunkLimit", "help", "card2"} {
		if !got[want] {
			t.Errorf("missing %q", want)
		}
	}
	for _, absent := range []string{"method", "row", "time", "testOnly", "generated"} {
		if got[absent] {
			t.Errorf("must not contain %q", absent)
		}
	}
}
