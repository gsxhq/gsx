package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNavIndex: a component Card(title string) declared in card.gsx and called
// directly from main.go must navigate the function reference to the authored
// component declaration.
func TestNavIndex(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("../..")
	writeFile(t, dir, "go.mod",
		"module example.com/nav\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	writeFile(t, dir, "card.gsx",
		"package nav\n\ncomponent Card(title string) {\n\t<div>{ title }</div>\n}\n")
	writeFile(t, dir, "main.go",
		"package nav\n\nvar _ = Card(\"x\")\n")

	m, err := Open(Options{ModuleRoot: dir, ModulePath: "example.com/nav"})
	if err != nil {
		t.Fatal(err)
	}
	pr, err := m.Package(dir)
	if err != nil {
		t.Fatal(err)
	}
	if hasDiagErrors(pr.Diags) {
		t.Fatalf("unexpected package errors: %v", pr.Diags)
	}

	// Index NavRefs by Name, filtering to those From main.go.
	byName := map[string]NavRef{}
	for _, nr := range pr.NavIndex {
		if strings.HasSuffix(nr.From.Filename, "main.go") {
			byName[nr.Name] = nr
		}
	}

	// --- Card func reference → card.gsx ---
	cardRef, ok := byName["Card"]
	if !ok {
		t.Errorf("NavIndex missing NavRef for 'Card' from main.go; all refs: %+v", pr.NavIndex)
	} else {
		if !strings.HasSuffix(cardRef.To.Filename, "card.gsx") {
			t.Errorf("Card NavRef.To.Filename = %q, want card.gsx", cardRef.To.Filename)
		}
	}

	// exact: Card NavRef.To must land on the 'C' of `component Card`.
	if data, err := os.ReadFile(cardRef.To.Filename); err == nil && cardRef.To.Offset < len(data) {
		if data[cardRef.To.Offset] != 'C' {
			t.Errorf("Card NavRef.To should land on the component name 'C'; To=%v", cardRef.To)
		}
	}
}
