package lsp

import "testing"

func TestPartialGoSymbolsKeepRecoveredDeclarations(t *testing.T) {
	const source = `package page

func Before() {}

var Broken = []int{1 2}

func After() {}

var _ = "func Fabricated() {}"
`
	file, fset := parseGSX(t, "/m/recovery.gsx", source)
	syms := FileSymbols("/m/recovery.gsx", []byte(source), file, fset, nil)

	for _, name := range []string{"Before", "Broken", "After"} {
		if _, ok := symByName(syms, name); !ok {
			t.Errorf("recovered declarations missing %q: %+v", name, syms)
		}
	}
	if _, ok := symByName(syms, "Fabricated"); ok {
		t.Errorf("merely declaration-like text fabricated a symbol: %+v", syms)
	}
}
