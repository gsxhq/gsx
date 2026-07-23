package codegen

import (
	"slices"
	"strings"
	"testing"
)

// symbolNames extracts the Name field of every ExportedSymbol.
func symbolNames(syms []ExportedSymbol) []string {
	out := make([]string, len(syms))
	for i, s := range syms {
		out[i] = s.Name
	}
	return out
}

// TestPackageExportedSymbolsStdlib enumerates a std package's exported scope
// from the loaded dep graph (strings is reachable transitively), with kinds and
// details, even though the asking .gsx does not import it.
func TestPackageExportedSymbolsStdlib(t *testing.T) {
	m, _ := newMissingModule(t, "package u\n\nvar xx = <p>hi</p>\n")
	syms := m.PackageExportedSymbols("strings")
	if len(syms) == 0 {
		t.Fatal("strings exported symbols = none")
	}
	names := symbolNames(syms)
	for _, want := range []string{"ToUpper", "Contains", "NewReplacer", "Builder", "Replacer"} {
		if !slices.Contains(names, want) {
			t.Errorf("strings missing exported symbol %q; got %v", want, names[:min(len(names), 12)])
		}
	}
	// Sorted by name.
	if !slices.IsSortedFunc(syms, func(a, b ExportedSymbol) int { return strings.Compare(a.Name, b.Name) }) {
		t.Errorf("exported symbols not sorted by name")
	}
	// No unexported names leak.
	for _, s := range syms {
		if s.Name == "" || (s.Name[0] >= 'a' && s.Name[0] <= 'z') {
			t.Errorf("leaked unexported/empty symbol %q", s.Name)
		}
	}
	// Kinds and details are populated for representative symbols.
	by := map[string]ExportedSymbol{}
	for _, s := range syms {
		by[s.Name] = s
	}
	// Detail qualifies every package by its name (the file does not import
	// strings), matching how packageMemberItems renders an imported package's
	// members: "func strings.ToUpper(s string) string".
	if s := by["ToUpper"]; s.Kind != SymbolFunc || !strings.HasPrefix(s.Detail, "func strings.ToUpper(") {
		t.Errorf("ToUpper = {kind %d, detail %q}, want func kind + qualified signature", s.Kind, s.Detail)
	}
	if s := by["Builder"]; s.Kind != SymbolTypeStruct {
		t.Errorf("Builder kind = %d, want SymbolTypeStruct", s.Kind)
	}
}

// TestPackageExportedSymbolsGraphPackage enumerates a non-std dep-graph package
// (the gsx runtime) present in the module's build graph.
func TestPackageExportedSymbolsGraphPackage(t *testing.T) {
	m, _ := newMissingModule(t, "package u\n\nvar xx = <p>hi</p>\n")
	syms := m.PackageExportedSymbols("github.com/gsxhq/gsx")
	if len(syms) == 0 {
		t.Fatal("gsx runtime exported symbols = none")
	}
	if !slices.Contains(symbolNames(syms), "Node") {
		t.Errorf("gsx runtime missing exported `Node`; got %v", symbolNames(syms))
	}
}

// TestPackageExportedSymbolsUnknown: an unresolvable path yields nil, never a
// panic.
func TestPackageExportedSymbolsUnknown(t *testing.T) {
	m, _ := newMissingModule(t, "package u\n\nvar xx = <p>hi</p>\n")
	if got := m.PackageExportedSymbols("no/such/pkg/anywhere"); got != nil {
		t.Fatalf("unknown path = %v, want nil", got)
	}
	if got := m.PackageExportedSymbols(""); got != nil {
		t.Fatalf("empty path = %v, want nil", got)
	}
}

// TestImportablePackageNames returns name+path pairs from the graph and stdlib
// table, internal-visibility filtered, sorted, without the asking package
// itself.
func TestImportablePackageNames(t *testing.T) {
	m, dir := newMissingModule(t, "package u\n\nvar xx = <p>hi</p>\n")
	pkgs := m.ImportablePackageNames(dir)
	if len(pkgs) == 0 {
		t.Fatal("importable packages = none")
	}
	byPath := map[string]string{}
	for _, p := range pkgs {
		byPath[p.Path] = p.Name
	}
	for path, wantName := range map[string]string{
		"fmt":           "fmt",
		"strings":       "strings",
		"net/http":      "http",
		"math/rand/v2":  "rand",
		"encoding/json": "json",
		"html/template": "template",
	} {
		if got, ok := byPath[path]; !ok {
			t.Errorf("importable packages missing %q", path)
		} else if got != wantName {
			t.Errorf("%q name = %q, want %q", path, got, wantName)
		}
	}
	// Sorted by (name, path).
	if !slices.IsSortedFunc(pkgs, func(a, b PackageName) int {
		if a.Name != b.Name {
			return strings.Compare(a.Name, b.Name)
		}
		return strings.Compare(a.Path, b.Path)
	}) {
		t.Error("importable packages not sorted")
	}
	// The asking package itself is never offered.
	for _, p := range pkgs {
		if p.Path == "example.com/u" {
			t.Errorf("self-package offered as importable: %+v", p)
		}
	}
}
