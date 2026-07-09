package codegen

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// newMissingModule writes a temp module whose single .gsx package is `src`.
func newMissingModule(t *testing.T, src string) (*Module, string) {
	t.Helper()
	dir := t.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	mod := "module example.com/u\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => " + repoRoot + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(mod), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.gsx"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Open(Options{ModuleRoot: dir, ModulePath: "example.com/u"})
	if err != nil {
		t.Skipf("Open: %v", err)
	}
	return m, dir
}

// missingNames returns the sorted "name.Symbol" pairs Package() reports for a.gsx.
func missingNames(t *testing.T, m *Module, dir string) []string {
	t.Helper()
	pr, err := m.Package(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, mi := range pr.MissingImports[filepath.Join(dir, "a.gsx")] {
		out = append(out, mi.Name+"."+mi.Symbol)
	}
	sort.Strings(out)
	return out
}

// TestMissingImportsDetectsUndefinedQualifier: the motivating case.
func TestMissingImportsDetectsUndefinedQualifier(t *testing.T) {
	m, dir := newMissingModule(t, "package u\n\nvar hello = \"hi\"\n\nvar xx = <p>{ fmt.Sprintf(hello) }</p>\n")
	got := missingNames(t, m, dir)
	if len(got) != 1 || got[0] != "fmt.Sprintf" {
		t.Fatalf("missing = %v, want [fmt.Sprintf]", got)
	}
}

// TestMissingImportsIgnoresLocalsAndImports: a selector on a LOCAL variable and a
// selector on an IMPORTED package must never be reported missing.
func TestMissingImportsIgnoresLocalsAndImports(t *testing.T) {
	src := "package u\n\nimport \"strings\"\n\n" +
		"type T struct{ Field int }\n\n" +
		"component C(v T) {\n\t<p>{ strings.ToUpper(\"x\") }{ v.Field }</p>\n}\n"
	m, dir := newMissingModule(t, src)
	if got := missingNames(t, m, dir); len(got) != 0 {
		t.Fatalf("missing = %v, want none (locals + imported pkgs are not missing)", got)
	}
}

// TestMissingImportsCapturesSymbol: the selector name is what disambiguates `rand`.
func TestMissingImportsCapturesSymbol(t *testing.T) {
	m, dir := newMissingModule(t, "package u\n\nvar xx = <p>{ rand.IntN(3) }</p>\n")
	got := missingNames(t, m, dir)
	if len(got) != 1 || got[0] != "rand.IntN" {
		t.Fatalf("missing = %v, want [rand.IntN]", got)
	}
}

// TestMissingImportsPositionIsGsx: Pos must point into the .gsx file, not the skeleton.
func TestMissingImportsPositionIsGsx(t *testing.T) {
	m, dir := newMissingModule(t, "package u\n\nvar xx = <p>{ fmt.Sprintf(\"x\") }</p>\n")
	pr, err := m.Package(dir)
	if err != nil {
		t.Fatal(err)
	}
	mis := pr.MissingImports[filepath.Join(dir, "a.gsx")]
	if len(mis) != 1 {
		t.Fatalf("want 1 missing, got %d", len(mis))
	}
	if !strings.HasSuffix(mis[0].Pos.Filename, "a.gsx") {
		t.Fatalf("Pos.Filename = %q, want .gsx path", mis[0].Pos.Filename)
	}
	if mis[0].Pos.Line != 3 {
		t.Fatalf("Pos.Line = %d, want 3", mis[0].Pos.Line)
	}
}

// TestMissingImportsAcrossPositions: a qualifier undefined in an attribute, a
// text interpolation, and an { if } condition is reported in every case.
//
// The "ifcond" case's control-flow spelling is taken from the real corpus
// syntax (internal/corpus/testdata/cases/control_flow/if_pos.txtar and
// parser/10_if.txtar): `{ if COND { <element> } }` — gsx has no `{ end }`
// terminator, the brace pair closes the clause.
func TestMissingImportsAcrossPositions(t *testing.T) {
	for name, src := range map[string]string{
		"interp": "package u\n\nvar xx = <p>{ fmt.Sprint(1) }</p>\n",
		"attr":   "package u\n\nvar xx = <p id={ fmt.Sprint(1) }>hi</p>\n",
		"ifcond": "package u\n\ncomponent C() {\n\t<div>{ if fmt.Sprint(1) == \"1\" { <p>y</p> } }</div>\n}\n",
	} {
		t.Run(name, func(t *testing.T) {
			m, dir := newMissingModule(t, src)
			got := missingNames(t, m, dir)
			if len(got) != 1 || got[0] != "fmt.Sprint" {
				t.Fatalf("missing = %v, want [fmt.Sprint]", got)
			}
		})
	}
}
