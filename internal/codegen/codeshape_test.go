package codegen

import (
	"bytes"
	goparser "go/parser"
	"go/printer"
	"go/token"
	"testing"
)

// codeShape reprints generated Go source without its comments, for tests that
// assert the shape of emitted code. Shipped output carries a /*line*/ anchor
// before authored tokens (`string( /*line a.gsx:3:9*/ u)`); positions are
// pinned by TestShippedHoleLineAnchors and the corpus goldens, so shape tests
// compare the code alone.
func codeShape(t *testing.T, src string) string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := goparser.ParseFile(fset, "gen.x.go", src, 0)
	if err != nil {
		t.Fatalf("codeShape: parse generated source: %v\n%s", err, src)
	}
	var buf bytes.Buffer
	if err := (&printer.Config{Mode: printer.UseSpaces | printer.TabIndent, Tabwidth: 8}).Fprint(&buf, fset, f); err != nil {
		t.Fatalf("codeShape: print: %v", err)
	}
	return buf.String()
}
