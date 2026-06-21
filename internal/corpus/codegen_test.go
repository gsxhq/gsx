package corpus

import (
	"os"
	"path/filepath"
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

func TestGenerateSingleClean(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	c, _ := loadCase("testdata/loadertest/single.txtar")
	tmp := mustTempModule(repoRoot)
	defer os.RemoveAll(tmp)
	gen, diag := c.generate(caseModuleDir(tmp, c), caseImportRoot(c))
	if len(diag) != 0 {
		t.Errorf("unexpected codegen diag: %s", diag)
	}
	if len(gen) == 0 {
		t.Errorf("expected non-empty generated output")
	}
}
