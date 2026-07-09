package codegen

import (
	"go/types"
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

// TestMissingImportsScriptInterpolation: an undefined qualifier used ONLY
// inside a <script> `@{ ... }` interpolation is reported, same as any other
// position. The `@{ }` script-interpolation syntax is confirmed against the
// real corpus case internal/corpus/testdata/cases/script/interp_value.txtar.
func TestMissingImportsScriptInterpolation(t *testing.T) {
	src := "package u\n\ncomponent C() {\n\t<script>\n\t\tconst x = @{ fmt.Sprint(1) };\n\t</script>\n}\n"
	m, dir := newMissingModule(t, src)
	got := missingNames(t, m, dir)
	if len(got) != 1 || got[0] != "fmt.Sprint" {
		t.Fatalf("missing = %v, want [fmt.Sprint]", got)
	}
}

// TestMissingImportsChildPropNoDuplicate is the adversarial-review regression:
// analyze emits a SECOND copy of a component child-prop expression, under its
// own //line stamp, as an inference-harvest probe (see infer.go's
// inferRegistry doc), so the single source-level `fmt.Sprint` qualifier used
// to be walked twice by missingFromSkeletons — once at its real site, once
// inside the probe copy — and reported twice, at two different columns. This
// must collapse to exactly one entry. Covers both a plain component and a
// generic one, since the probe is emitted for both (see
// components/generic_inferred_tag.txtar for the generic skeleton shape).
func TestMissingImportsChildPropNoDuplicate(t *testing.T) {
	for name, src := range map[string]string{
		"plain": "package u\n\ncomponent Show(v string) {\n\t<p>{ v }</p>\n}\n\n" +
			"component Wrap() {\n\t<Show v={ fmt.Sprint(1) } />\n}\n",
		"generic": "package u\n\ncomponent Show[T any](v T) {\n\t<p>{ v }</p>\n}\n\n" +
			"component Wrap() {\n\t<Show value={ fmt.Sprint(1) } />\n}\n",
	} {
		t.Run(name, func(t *testing.T) {
			m, dir := newMissingModule(t, src)
			pr, err := m.Package(dir)
			if err != nil {
				t.Fatal(err)
			}
			mis := pr.MissingImports[filepath.Join(dir, "a.gsx")]
			if len(mis) != 1 {
				t.Fatalf("MissingImports = %+v, want exactly 1 entry (was reported twice before the fix)", mis)
			}
			if mis[0].Name != "fmt" || mis[0].Symbol != "Sprint" {
				t.Fatalf("got %s.%s, want fmt.Sprint", mis[0].Name, mis[0].Symbol)
			}
		})
	}
}

// TestMissingImportsRepeatedGenuineUsesCollapse: fmt.Sprint used TWICE, on two
// different lines, both in plain interpolation (no child-prop probe
// involved), still collapses to ONE entry. This is intended, not a
// regression: organizeImports/the quickfix adds one `fmt` import either way,
// so a second entry for the same (Name, Symbol) in the same file carries no
// additional information for the caller.
func TestMissingImportsRepeatedGenuineUsesCollapse(t *testing.T) {
	src := "package u\n\ncomponent Wrap() {\n\t<p>{ fmt.Sprint(1) }</p>\n\t<p>{ fmt.Sprint(2) }</p>\n}\n"
	m, dir := newMissingModule(t, src)
	got := missingNames(t, m, dir)
	if len(got) != 1 || got[0] != "fmt.Sprint" {
		t.Fatalf("missing = %v, want [fmt.Sprint] (repeated genuine uses collapse to one)", got)
	}
}

// TestMissingImportsDifferentQualifiersBothReported: two DIFFERENT undefined
// qualifiers must both survive the (Name, Symbol) dedupe.
func TestMissingImportsDifferentQualifiersBothReported(t *testing.T) {
	src := "package u\n\ncomponent Wrap() {\n\t<p>{ fmt.Sprint(1) }</p>\n\t<p>{ rand.IntN(3) }</p>\n}\n"
	m, dir := newMissingModule(t, src)
	got := missingNames(t, m, dir)
	want := []string{"fmt.Sprint", "rand.IntN"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("missing = %v, want %v", got, want)
	}
}

// TestMissingImportsPosMatchesDiagnostic: MissingImport.Pos is documented to
// mirror the diagnostic the user actually sees for the same undefined
// qualifier, so a future quickfix can associate with the client's
// context.diagnostics. Checked against the REAL shipped diagnostic (pr.Diags),
// not a hardcoded column, for both a plain and a generic child-prop tag.
//
// The generic case is the reviewer-found regression: analyze emits the
// child-prop expression twice (props literal + _gsxuseq harvest probe); for a
// GENERIC tag, probeSiteForError's inferRegistry span covers the PROPS
// LITERAL occurrence (it is the one participating in type inference), so
// filtering on it dropped the wrong copy and kept the _gsxuseq one instead —
// MissingImport.Pos pointed at a position with NO diagnostic at all.
func TestMissingImportsPosMatchesDiagnostic(t *testing.T) {
	for name, src := range map[string]string{
		"plain": "package u\n\ncomponent Show(v string) {\n\t<p>{ v }</p>\n}\n\n" +
			"component Wrap() {\n\t<Show v={ fmt.Sprint(1) } />\n}\n",
		"generic": "package u\n\ncomponent Show[T any](v T) {\n\t<p>{ v }</p>\n}\n\n" +
			"component Wrap() {\n\t<Show value={ fmt.Sprint(1) } />\n}\n",
	} {
		t.Run(name, func(t *testing.T) {
			m, dir := newMissingModule(t, src)
			pr, err := m.Package(dir)
			if err != nil {
				t.Fatal(err)
			}
			mis := pr.MissingImports[filepath.Join(dir, "a.gsx")]
			if len(mis) != 1 {
				t.Fatalf("MissingImports = %+v, want exactly 1 entry", mis)
			}
			var diag *diagPos
			for i := range pr.Diags {
				d := &pr.Diags[i]
				if strings.Contains(d.Message, "undefined: fmt") {
					diag = &diagPos{line: d.Start.Line, col: d.Start.Column}
					break
				}
			}
			if diag == nil {
				t.Fatalf("no shipped diagnostic contains %q; Diags = %+v", "undefined: fmt", pr.Diags)
			}
			if mis[0].Pos.Line != diag.line || mis[0].Pos.Column != diag.col {
				t.Fatalf("MissingImport.Pos = %d:%d, want it to match the shipped diagnostic at %d:%d",
					mis[0].Pos.Line, mis[0].Pos.Column, diag.line, diag.col)
			}
		})
	}
}

// TestResolveUnambiguousStdlib: `fmt` resolves to exactly one path.
func TestResolveUnambiguousStdlib(t *testing.T) {
	m, _ := newMissingModule(t, "package u\n\nvar xx = <p>hi</p>\n")
	got := m.ResolveImportCandidates("fmt", "Sprintf")
	if len(got) != 1 || got[0] != "fmt" {
		t.Fatalf("resolve(fmt, Sprintf) = %v, want [fmt]", got)
	}
}

// TestResolveDisambiguatesBySymbol: only math/rand/v2 exports IntN.
func TestResolveDisambiguatesBySymbol(t *testing.T) {
	m, _ := newMissingModule(t, "package u\n\nvar xx = <p>hi</p>\n")
	got := m.ResolveImportCandidates("rand", "IntN")
	if len(got) != 1 || got[0] != "math/rand/v2" {
		t.Fatalf("resolve(rand, IntN) = %v, want [math/rand/v2]", got)
	}
	// html/template exports HTML; text/template does not.
	got = m.ResolveImportCandidates("template", "HTML")
	if len(got) != 1 || got[0] != "html/template" {
		t.Fatalf("resolve(template, HTML) = %v, want [html/template]", got)
	}
}

// TestResolveAmbiguousKeepsAll: when no candidate can be eliminated by symbol,
// every candidate survives so the caller can offer one quickfix each (and
// organizeImports adds nothing).
func TestResolveAmbiguousKeepsAll(t *testing.T) {
	m, _ := newMissingModule(t, "package u\n\nvar xx = <p>hi</p>\n")
	got := m.ResolveImportCandidates("rand", "NoSuchSymbolAnywhere")
	if len(got) < 2 {
		t.Fatalf("resolve(rand, <none>) = %v, want all candidates kept", got)
	}
}

// TestResolveExcludesUnimportableInternal: `internal` collides across four std
// packages (encoding/json/internal, log/internal, log/slog/internal,
// net/http/internal) whose LAST path component is `internal`, but none of
// them is importable from user code — that is a Go visibility rule on the
// path COMPONENT, not on the declared package NAME. A prior substring filter
// on "internal/" missed exactly this shape (nothing follows the last
// "internal/"), so stdlibIndex["internal"] leaked all four and the LSP would
// have offered a quickfix the compiler rejects. This must resolve to nothing.
func TestResolveExcludesUnimportableInternal(t *testing.T) {
	m, _ := newMissingModule(t, "package u\n\nvar xx = <p>hi</p>\n")
	if got := m.ResolveImportCandidates("internal", "Anything"); len(got) != 0 {
		t.Fatalf("resolve(internal, Anything) = %v, want [] — no std `internal` package is importable", got)
	}
}

// TestResolveUnknownNameYieldsNothing: no scan, no guess, no candidates.
func TestResolveUnknownNameYieldsNothing(t *testing.T) {
	m, _ := newMissingModule(t, "package u\n\nvar xx = <p>hi</p>\n")
	if got := m.ResolveImportCandidates("zzznotapkg", "Thing"); len(got) != 0 {
		t.Fatalf("resolve(zzznotapkg) = %v, want []", got)
	}
}

// TestResolveFindsDepGraphPackage: a package already in the module's dep graph
// resolves from types alone (no stdlib table entry exists for it).
func TestResolveFindsDepGraphPackage(t *testing.T) {
	m, _ := newMissingModule(t, "package u\n\nvar xx = <p>hi</p>\n")
	got := m.ResolveImportCandidates("gsx", "Node")
	if len(got) != 1 || got[0] != "github.com/gsxhq/gsx" {
		t.Fatalf("resolve(gsx, Node) = %v, want [github.com/gsxhq/gsx]", got)
	}
}

// TestResolveNeverUsesFabricatedName: `v2` is the placeholder name go/types
// fabricates for math/rand/v2 when its importer never loaded it. It must resolve
// to nothing.
//
// HONEST SCOPE: this is an ABSENCE guard — `v2` is in neither stdlibIndex (whose
// key is the real name `rand`) nor the dep graph — so it does not by itself
// exercise depGraphPackages' Complete() gate. That gate is still required:
// packages.Load hands back PARTIAL Types for a package with errors (see the note
// at module.go:505), and reading Name() off such a package is the PR #64 Critical.
// The implementer MUST additionally assert, in a direct unit test of
// depGraphPackages, that an incomplete *types.Package in the mapImporter is
// skipped. Construct one with types.NewPackage (never marked complete).
func TestResolveNeverUsesFabricatedName(t *testing.T) {
	m, _ := newMissingModule(t, "package u\n\nvar xx = <p>hi</p>\n")
	if got := m.ResolveImportCandidates("v2", "IntN"); len(got) != 0 {
		t.Fatalf("resolve(v2) = %v, want [] — a fabricated path-base name must not resolve", got)
	}
}

// TestDepGraphPackagesSkipsIncomplete: the Complete() gate, tested directly
// against completeDepGraphPackages (the function depGraphPackages delegates
// the actual filtering to) rather than re-implementing the check inline —
// re-implementing it would pass unconditionally regardless of whether the
// production gate exists at all. types.NewPackage returns a package that is
// NOT complete; its Name() is whatever we pass, standing in for go/types'
// fabricated path-base name. A sibling COMPLETE package is included too, so
// the assertion cannot pass simply because the output happens to be empty.
func TestDepGraphPackagesSkipsIncomplete(t *testing.T) {
	incomplete := types.NewPackage("math/rand/v2", "v2") // never MarkComplete()d
	if incomplete.Complete() {
		t.Fatal("precondition: types.NewPackage must not be complete")
	}
	complete := types.NewPackage("fmt", "fmt")
	complete.MarkComplete()

	mi := mapImporter{"math/rand/v2": incomplete, "fmt": complete}
	got := completeDepGraphPackages(mi)
	if _, ok := got["math/rand/v2"]; ok {
		t.Fatal("an incomplete package must never contribute its (fabricated) name")
	}
	if _, ok := got["fmt"]; !ok {
		t.Fatal("a complete package must still be returned")
	}
}

// TestPackageDoesNotResolveImports: the hot path never resolves. The second half
// proves the counter can move, so the zero assertion is not vacuous.
func TestPackageDoesNotResolveImports(t *testing.T) {
	m, dir := newMissingModule(t, "package u\n\nvar xx = <p>{ fmt.Sprintf(\"x\") }</p>\n")
	before := resolveImportCandidatesCalls.Load()
	if _, err := m.Package(dir); err != nil {
		t.Fatal(err)
	}
	if got := resolveImportCandidatesCalls.Load(); got != before {
		t.Errorf("Package() resolved %d time(s); want 0 — resolution must stay off the hot path", got-before)
	}
	m.ResolveImportCandidates("fmt", "Sprintf")
	if got := resolveImportCandidatesCalls.Load(); got == before {
		t.Error("counter never moved; the zero assertion above is vacuous")
	}
}

// diagPos is a minimal line/column pair used only to compare MissingImport.Pos
// against a diag.Diagnostic's Start without importing the diag package twice.
type diagPos struct {
	line, col int
}

// TestMissingImportsSpreadPosMatchesDiagnostic pins the inHarvestProbe filter
// for an ELEMENT SPREAD, `{ expr... }` (syntax confirmed against the real
// corpus, e.g. internal/corpus/testdata/cases/fallthrough/caller_wins.txtar's
// `{ attrs... }`). Unlike the child-prop case (TestMissingImportsPosMatchesDiagnostic),
// analyze.go emits the spread's _gsxuseq(...) harvest probe BEFORE the native
// `var _ _gsxrt.Attrs = (...)` recheck (see analyze.go's walkSpreadAttrs
// emission, ~1649), so ast.Inspect visits the probe copy FIRST. The
// (Name, Symbol) dedupe alone would therefore keep the probe copy — the
// inHarvestProbe filter is what makes it skip ahead to the native copy
// instead, which is where the shipped "undefined: fmt" diagnostic anchors.
// This test FAILS if the inHarvestProbe skip in missingFromSkeletons is
// removed (verified by temporarily deleting it): MissingImport.Pos then
// points at the probe copy, which has no diagnostic of its own.
func TestMissingImportsSpreadPosMatchesDiagnostic(t *testing.T) {
	src := "package u\n\ncomponent Wrap() {\n\t<div { fmt.Sprint(1)... }>x</div>\n}\n"
	m, dir := newMissingModule(t, src)
	pr, err := m.Package(dir)
	if err != nil {
		t.Fatal(err)
	}
	mis := pr.MissingImports[filepath.Join(dir, "a.gsx")]
	if len(mis) != 1 {
		t.Fatalf("MissingImports = %+v, want exactly 1 entry", mis)
	}
	var diag *diagPos
	for i := range pr.Diags {
		d := &pr.Diags[i]
		if strings.Contains(d.Message, "undefined: fmt") {
			diag = &diagPos{line: d.Start.Line, col: d.Start.Column}
			break
		}
	}
	if diag == nil {
		t.Fatalf("no shipped diagnostic contains %q; Diags = %+v", "undefined: fmt", pr.Diags)
	}
	if mis[0].Pos.Line != diag.line || mis[0].Pos.Column != diag.col {
		t.Fatalf("MissingImport.Pos = %d:%d, want it to match the shipped diagnostic at %d:%d",
			mis[0].Pos.Line, mis[0].Pos.Column, diag.line, diag.col)
	}
}

// TestMissingImportsDifferentSymbolsSamePackage: fmt.Sprint and fmt.Errorf
// are DISTINCT (Name, Symbol) pairs, so both are reported — even though they
// share a qualifier name, they could in principle disambiguate to different
// packages, and the caller (organizeImports) is the one that resolves a
// qualifier to a single import path and dedupes further from there.
func TestMissingImportsDifferentSymbolsSamePackage(t *testing.T) {
	src := "package u\n\ncomponent Wrap() {\n\t<p>{ fmt.Sprint(1) }</p>\n\t<p>{ fmt.Errorf(\"x\") }</p>\n}\n"
	m, dir := newMissingModule(t, src)
	got := missingNames(t, m, dir)
	want := []string{"fmt.Errorf", "fmt.Sprint"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("missing = %v, want %v", got, want)
	}
}
