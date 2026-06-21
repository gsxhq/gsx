package corpus

import (
	"testing"
)

func TestAstAndParserDiagClean(t *testing.T) {
	c, _ := loadCase("testdata/loadertest/single.txtar")
	dump, diag, single := c.astAndParserDiag()
	if !single {
		t.Fatal("single = false, want true")
	}
	if len(diag) != 0 {
		t.Errorf("unexpected parser diag: %s", diag)
	}
	if len(dump) == 0 {
		t.Errorf("expected non-empty AST dump")
	}
}
