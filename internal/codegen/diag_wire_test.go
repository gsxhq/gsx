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

	out, err := GenerateDirs(mod, []string{viewsDir}, Options{}, nil)
	if err != nil {
		t.Fatalf("GenerateDirs returned hard error: %v", err)
	}
	dr := out[viewsDir]
	msgs := diagMsgs(dr)
	if !strings.Contains(msgs, "undefinedA") || !strings.Contains(msgs, "undefinedB") {
		t.Errorf("expected BOTH type errors, got diagnostics:\n%s", msgs)
	}
}

func diagMsgs(dr DirResult) string {
	var s strings.Builder
	for _, d := range dr.Diags {
		s.WriteString(d.Message)
		s.WriteString("\n")
	}
	return s.String()
}
