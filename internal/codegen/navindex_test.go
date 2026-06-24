package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNavIndex: a component Card(title string) declared in card.gsx, called from
// main.go as Card(CardProps{Title: "x"}), must produce NavRefs for:
//   - "Card"      (func ref)    → card.gsx component decl
//   - "CardProps" (struct ref)  → card.gsx component decl
//   - "Title"     (field ref)   → card.gsx param position for "title"
func TestNavIndex(t *testing.T) {
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("../..")
	writeFile(t, dir, "go.mod",
		"module example.com/nav\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	writeFile(t, dir, "card.gsx",
		"package nav\n\ncomponent Card(title string) {\n\t<div>{ title }</div>\n}\n")
	writeFile(t, dir, "main.go",
		"package nav\n\nvar _ = Card(CardProps{Title: \"x\"})\n")

	out, err := GeneratePackagesWithFilters(dir, []string{dir}, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	pr := out[dir]
	if pr == nil {
		t.Fatalf("no result for %s", dir)
	}
	if pr.Err != nil {
		t.Fatalf("unexpected package error: %v", pr.Err)
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

	// --- CardProps struct reference → card.gsx ---
	propsRef, ok := byName["CardProps"]
	if !ok {
		t.Errorf("NavIndex missing NavRef for 'CardProps' from main.go; all refs: %+v", pr.NavIndex)
	} else {
		if !strings.HasSuffix(propsRef.To.Filename, "card.gsx") {
			t.Errorf("CardProps NavRef.To.Filename = %q, want card.gsx", propsRef.To.Filename)
		}
	}

	// --- Title field reference → card.gsx (the 'title' param position) ---
	titleRef, ok := byName["Title"]
	if !ok {
		t.Errorf("NavIndex missing NavRef for 'Title' from main.go; all refs: %+v", pr.NavIndex)
	} else {
		if !strings.HasSuffix(titleRef.To.Filename, "card.gsx") {
			t.Errorf("Title NavRef.To.Filename = %q, want card.gsx", titleRef.To.Filename)
		}
		// exact: To must point at the 'title' param in the component signature.
		if data, err := os.ReadFile(titleRef.To.Filename); err == nil {
			if titleRef.To.Offset >= len(data) || data[titleRef.To.Offset] != 't' {
				t.Errorf("Title NavRef.To should land on the 'title' param (byte 't'); To=%v", titleRef.To)
			}
		}
	}
	// exact: Card NavRef.To must land on the 'C' of `component Card`.
	if data, err := os.ReadFile(cardRef.To.Filename); err == nil && cardRef.To.Offset < len(data) {
		if data[cardRef.To.Offset] != 'C' {
			t.Errorf("Card NavRef.To should land on the component name 'C'; To=%v", cardRef.To)
		}
	}
}
