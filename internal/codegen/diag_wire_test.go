package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A file with TWO independent type errors must report BOTH (not just the first).
func TestAllTypeErrorsReported(t *testing.T) {
	t.Parallel()
	// Create a temp module rooted at a real Go module so packages.Load works.
	mod := tempModule(t, "gsxdiagwiretest")

	// Create a views subdirectory with a .gsx file containing TWO undefined idents.
	viewsDir := filepath.Join(mod, "views")
	if err := os.MkdirAll(viewsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := `package views

component X() {
	<div>{ undefinedA }</div>
	<span>{ undefinedB }</span>
}
`
	if err := os.WriteFile(filepath.Join(viewsDir, "views.gsx"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := GeneratePackages(mod, []string{viewsDir})
	if err != nil {
		t.Fatalf("GeneratePackages returned hard error: %v", err)
	}
	pr := out[mustAbs(t, viewsDir)]
	if pr == nil {
		t.Fatal("no PackageResult for dir")
	}
	msgs := diagMsgs(pr)
	if !strings.Contains(msgs, "undefinedA") || !strings.Contains(msgs, "undefinedB") {
		t.Errorf("expected BOTH type errors, got diagnostics:\n%s", msgs)
	}
}

func diagMsgs(pr *PackageResult) string {
	var s string
	for _, d := range pr.Diags {
		s += d.Message + "\n"
	}
	return s
}
