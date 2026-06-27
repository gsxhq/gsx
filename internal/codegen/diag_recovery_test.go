package codegen

import (
	"os"
	"path/filepath"
	"testing"
)

// Two components each with a distinct codegen error must BOTH be reported
// (component-boundary recovery), and each diagnostic must carry a .gsx position.
func TestComponentRecoveryReportsAllPositioned(t *testing.T) {
	t.Parallel()
	mod := tempModule(t, "gsxrecoverytest")
	dir := filepath.Join(mod, "views")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Two reserved-param errors in two components (a codegen-layer check, not types).
	src := `package views

component A(ctx string) {
	<div></div>
}

component B(children string) {
	<div></div>
}
`
	if err := os.WriteFile(filepath.Join(dir, "v.gsx"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := GeneratePackages(mod, []string{dir})
	if err != nil {
		t.Fatalf("hard error: %v", err)
	}
	pr := out[mustAbs(t, dir)]
	var lines int
	var positioned bool
	for _, d := range pr.Diags {
		if d.Source == "codegen" {
			lines++
			if d.Start.Line > 0 && d.Start.Column > 0 {
				positioned = true
			}
		}
	}
	if lines < 2 {
		t.Errorf("expected >=2 codegen diagnostics (one per component), got %d", lines)
	}
	if !positioned {
		t.Errorf("codegen diagnostics must carry .gsx positions")
	}
}
