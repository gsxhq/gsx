package codegen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/diag"
)

// Pins the dotted cross-package generic-tag paths:
//
//   - explicit type args: <components.Button[int]>
//   - inferred type args: <components.Button>
//
// The txtar corpus (internal/corpus) is single-package — every case is one
// input.gsx in one package — so this cross-package context cannot be expressed
// there and lives as a GenerateDirs unit test instead, mirroring
// writeCrossPkgModule in batch_crosspkg_test.go and the Options used by
// TestGenericMethodComponentGo127 in generic_method_go127_test.go. See
// CLAUDE.md's per-context corpus coverage rule.
func TestGenericCrossPackageTag(t *testing.T) {
	// Task 4 restores imported-component inference: an IMPORTED generic
	// component's genericSig (typeParams/params/imports, from its declaring
	// FILE) is requalified into the calling file's context (Task 3's engine)
	// before emitInferProbe builds the caller-side probe — see infer.go's
	// genericSig doc and analyze.go's emitProbes generic-tag branch. This
	// test pins the dotted cross-package generic-tag paths:
	//
	//   - explicit type args: <components.Button[int]>
	//   - inferred type args, no dep-qualified constraint: <components.Button>
	//   - inferred type args WITH a dep-qualified constraint (FlagBox's
	//     `T string | model.Flag`, requalified via a SECOND dep import)
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module example.com/xg\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	compDir := filepath.Join(tmp, "components")
	if err := os.MkdirAll(compDir, 0o755); err != nil {
		t.Fatal(err)
	}
	modelDir := filepath.Join(tmp, "model")
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, modelDir, "flag.go", "package model\n\ntype Flag string\n")
	writeFile(t, compDir, "button.gsx", "package components\n\nimport \"example.com/xg/model\"\n\ntype Flag = model.Flag\n\nfunc MakeFlag() model.Flag { return \"flag\" }\n\ncomponent Button[T string | int](label T) {\n\t<button>{label}</button>\n}\n\ncomponent FlagBox[T string | model.Flag](label T) {\n\t<span>{string(label)}</span>\n}\n")
	writeFile(t, tmp, "post.gsx", "package xg\n\nimport \"example.com/xg/components\"\n\ncomponent Post() {\n\t<components.Button[int] label={7} />\n\t<components.Button label=\"ok\" />\n\t<components.FlagBox label={components.MakeFlag()} />\n}\n")

	res, err := GenerateDirs(tmp, []string{tmp, compDir}, Options{FilterPkgs: []string{stdImportPath}, CSSMinify: true, JSMinify: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if diags := res[tmp].Diags; len(diags) != 0 {
		t.Fatalf("unexpected diagnostics generating root package: %+v", diags)
	}
	if diags := res[compDir].Diags; len(diags) != 0 {
		t.Fatalf("unexpected diagnostics generating components package: %+v", diags)
	}
	var root string
	for _, src := range res[tmp].Files {
		root = string(src)
	}
	for _, want := range []string{
		"components.Button[int](components.ButtonProps[int]{Label: 7})",
		"components.Button[string](components.ButtonProps[string]{Label: \"ok\"})",
		"components.FlagBox[model.Flag](components.FlagBoxProps[model.Flag]{Label: components.MakeFlag()})",
	} {
		if !strings.Contains(root, want) {
			t.Fatalf("generated root source missing %q:\n%s", want, root)
		}
	}
	if !strings.Contains(root, `"example.com/xg/model"`) {
		t.Fatalf("generated root source missing inferred type-arg import:\n%s", root)
	}
}

// TestGenericCrossPackageInference is the Task 4 brief's headline case: an
// IMPORTED generic component called with only SOME of its declared props
// (partial + imported) must still infer its type arguments — mirroring
// TestInferPartialProps's same-package finding-5 case, now for the
// cross-package caller-side probe path.
func TestGenericCrossPackageInference(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module example.com/gci\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	compDir := filepath.Join(tmp, "components")
	if err := os.MkdirAll(compDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, compDir, "button.gsx", "package components\n\ncomponent Button[T string | int](label T, size string) {\n\t<button class={size}>{label}</button>\n}\n")
	writeFile(t, tmp, "post.gsx", "package gci\n\nimport \"example.com/gci/components\"\n\ncomponent Post() {\n\t<components.Button label={7} />\n}\n")

	res, err := GenerateDirs(tmp, []string{tmp, compDir}, Options{FilterPkgs: []string{stdImportPath}, CSSMinify: true, JSMinify: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if diags := res[tmp].Diags; len(diags) != 0 {
		t.Fatalf("unexpected diagnostics generating root package: %+v", diags)
	}
	if diags := res[compDir].Diags; len(diags) != 0 {
		t.Fatalf("unexpected diagnostics generating components package: %+v", diags)
	}
	var root string
	for _, src := range res[tmp].Files {
		root = string(src)
	}
	if !strings.Contains(root, "components.Button[int](components.ButtonProps[int]{Label: 7})") {
		t.Fatalf("partial-props cross-package inference failed; generated:\n%s", root)
	}
}

// TestGenericCrossPackageInferenceUnexportedConstraint pins the Task 4
// fail-safe path: a dep constraint referencing an UNEXPORTED dep-local type
// cannot be requalified into the caller's context (it is unspeakable outside
// the dep package — see requalifyTypeExpr's doc), so the probe is skipped
// and exactly one positioned "inference-unavailable" diagnostic is recorded
// naming the offending type. Generation of the REST of the package (another,
// unrelated tag) is unaffected — the failure is scoped to the one bad tag.
func TestGenericCrossPackageInferenceUnexportedConstraint(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module example.com/gciu\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	compDir := filepath.Join(tmp, "components")
	if err := os.MkdirAll(compDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, compDir, "widget.gsx", "package components\n\ntype secret string\n\ncomponent Widget[T secret | string](label T) {\n\t<span>{string(label)}</span>\n}\n\ncomponent Button(label string) {\n\t<button>{label}</button>\n}\n")
	writeFile(t, tmp, "post.gsx", "package gciu\n\nimport \"example.com/gciu/components\"\n\ncomponent Post() {\n\t<components.Widget label=\"x\" />\n\t<components.Button label=\"ok\" />\n}\n")

	res, err := GenerateDirs(tmp, []string{tmp, compDir}, Options{FilterPkgs: []string{stdImportPath}, CSSMinify: true, JSMinify: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if diags := res[compDir].Diags; len(diags) != 0 {
		t.Fatalf("unexpected diagnostics generating components package: %+v", diags)
	}
	var found int
	for _, d := range res[tmp].Diags {
		if d.Code != "inference-unavailable" {
			continue
		}
		found++
		if d.Severity != diag.Warning {
			t.Errorf("inference-unavailable diagnostic severity = %v, want Warning: %+v", d.Severity, d)
		}
		if d.Start.Line == 0 {
			t.Errorf("inference-unavailable diagnostic is not positioned: %+v", d)
		}
		if !strings.Contains(d.Message, "components.Widget") || !strings.Contains(d.Message, "secret") {
			t.Errorf("inference-unavailable diagnostic does not name the tag/offending type: %+v", d)
		}
	}
	if found != 1 {
		t.Fatalf("want exactly 1 inference-unavailable diagnostic, got %d: %+v", found, res[tmp].Diags)
	}
	var root string
	for _, src := range res[tmp].Files {
		root = string(src)
	}
	if !strings.Contains(root, "Button(ButtonProps{Label: \"ok\"})") && !strings.Contains(root, "components.Button(components.ButtonProps{Label: \"ok\"})") {
		t.Fatalf("unaffected tag (components.Button) missing from generated root:\n%s", root)
	}
}

// TestGenericCrossPackageInferenceFailureOutputBuilds pins the emit-time half
// of the requalification fail-safe: when a param's ONLY use sits in the failed
// tag's prop values (`label={name}`), skipping the element entirely at emit
// time would leave the emitted `name := _gsxp.Name` local unused ("declared
// and not used" — the .x.go would not compile even though generate exited 0
// with only a warning). The emitted file must instead consume the skipped
// element's value expressions in a never-executed sink, so this test asserts
// the strongest possible property: `go build` of the WRITTEN output succeeds
// (the TestBuildTagExcludesGeneratedFile pattern).
func TestGenericCrossPackageInferenceFailureOutputBuilds(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns the go toolchain")
	}
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module example.com/gcib\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	compDir := filepath.Join(tmp, "components")
	if err := os.MkdirAll(compDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, compDir, "widget.gsx", "package components\n\ntype secret string\n\ncomponent Widget[T secret | string](label T) {\n\t<span>{string(label)}</span>\n}\n\ncomponent Button(label string) {\n\t<button>{label}</button>\n}\n")
	// name's ONLY use is the failed tag's prop value; the sibling Button tag
	// keeps the components import alive independently of the failed tag.
	writeFile(t, tmp, "post.gsx", "package gcib\n\nimport \"example.com/gcib/components\"\n\ncomponent Post(name string) {\n\t<components.Widget label={name} />\n\t<components.Button label=\"ok\" />\n}\n")

	res, err := GenerateDirs(tmp, []string{tmp, compDir}, Options{FilterPkgs: []string{stdImportPath}, CSSMinify: true, JSMinify: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertOnlyInferenceUnavailable(t, res[tmp].Diags)
	writeGeneratedAndBuild(t, tmp, res)
}

// TestGenericCrossPackageInferenceFailureSoleImportBuilds pins the sole-import
// half of the fail-safe: when the failed tag is the file's ONLY reference to
// the dep import, sinking it must not (a) fail the skeleton with a spurious
// hard `"…/components" imported and not used` error at the user's import line
// (which would exit 1 and bury the actionable inference-unavailable warning,
// losing the whole file), nor (b) leave the emitted .x.go with an unused
// import (which would not build). Asserts: warning-only diagnostics, a sibling
// file in the SAME package generates, and the written output `go build`s.
func TestGenericCrossPackageInferenceFailureSoleImportBuilds(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns the go toolchain")
	}
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module example.com/gcis\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	compDir := filepath.Join(tmp, "components")
	if err := os.MkdirAll(compDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, compDir, "widget.gsx", "package components\n\ntype secret string\n\ncomponent Widget[T secret | string](label T) {\n\t<span>{string(label)}</span>\n}\n")
	// The failed tag is the ONLY use of the components import in post.gsx.
	writeFile(t, tmp, "post.gsx", "package gcis\n\nimport \"example.com/gcis/components\"\n\ncomponent Post(name string) {\n\t<components.Widget label={name} />\n}\n")
	// A sibling file in the same package must be unaffected by post.gsx's sink.
	writeFile(t, tmp, "other.gsx", "package gcis\n\ncomponent Other() {\n\t<p>ok</p>\n}\n")

	res, err := GenerateDirs(tmp, []string{tmp, compDir}, Options{FilterPkgs: []string{stdImportPath}, CSSMinify: true, JSMinify: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertOnlyInferenceUnavailable(t, res[tmp].Diags)
	var otherGen string
	for p, src := range res[tmp].Files {
		if strings.HasSuffix(p, "other.gsx") {
			otherGen = string(src)
		}
	}
	if !strings.Contains(otherGen, "func Other() gsx.Node") {
		t.Fatalf("sibling file's generation affected by the failed tag's sink; other.gsx generated:\n%s", otherGen)
	}
	writeGeneratedAndBuild(t, tmp, res)
}

// TestGenericCrossPackageInferenceFailureAliasedSoleImportBuilds is the
// RENAMED-import variant of the sole-import fail-safe: go/types spells the
// unused-import error for `import comp "…"` as `"…" imported as comp and not
// used` (the SECOND errorUnusedPkg form), which the sunk-import filter must
// match just like the plain form — otherwise the spurious hard error
// survives, generation exits 1, and the whole file is lost (the original
// failure mode, spelled with an alias).
func TestGenericCrossPackageInferenceFailureAliasedSoleImportBuilds(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns the go toolchain")
	}
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module example.com/gcia\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	compDir := filepath.Join(tmp, "components")
	if err := os.MkdirAll(compDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, compDir, "widget.gsx", "package components\n\ntype secret string\n\ncomponent Widget[T secret | string](label T) {\n\t<span>{string(label)}</span>\n}\n")
	// Renamed import; the failed tag is its only use.
	writeFile(t, tmp, "post.gsx", "package gcia\n\nimport comp \"example.com/gcia/components\"\n\ncomponent Post(name string) {\n\t<comp.Widget label={name} />\n}\n")

	res, err := GenerateDirs(tmp, []string{tmp, compDir}, Options{FilterPkgs: []string{stdImportPath}, CSSMinify: true, JSMinify: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertOnlyInferenceUnavailable(t, res[tmp].Diags)
	var postGen string
	for p, src := range res[tmp].Files {
		if strings.HasSuffix(p, "post.gsx") {
			postGen = string(src)
		}
	}
	if !strings.Contains(postGen, `_ "example.com/gcia/components"`) {
		t.Fatalf("sole aliased import not rewritten to a blank import; post.gsx generated:\n%s", postGen)
	}
	writeGeneratedAndBuild(t, tmp, res)
}

// TestGenericCrossPackageInferenceFailureDotSiblingKeepsRealError pins the
// position-keying of the sunk-import filter: a file with TWO specs of the
// SAME dep path — a genuinely unused dot import AND a renamed alias whose
// only use is the failed tag — must have ONLY the alias spec's spurious
// error filtered. The dot spec's REAL `"…" imported and not used` error (a
// user mistake) must still surface as an Error diagnostic; a path-keyed
// filter would swallow it too.
func TestGenericCrossPackageInferenceFailureDotSiblingKeepsRealError(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module example.com/gcid\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	compDir := filepath.Join(tmp, "components")
	if err := os.MkdirAll(compDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, compDir, "widget.gsx", "package components\n\ntype secret string\n\ncomponent Widget[T secret | string](label T) {\n\t<span>{string(label)}</span>\n}\n")
	writeFile(t, tmp, "post.gsx", "package gcid\n\nimport (\n\t. \"example.com/gcid/components\"\n\tcomp \"example.com/gcid/components\"\n)\n\ncomponent Post(name string) {\n\t<comp.Widget label={name} />\n}\n")

	res, err := GenerateDirs(tmp, []string{tmp, compDir}, Options{FilterPkgs: []string{stdImportPath}, CSSMinify: true, JSMinify: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	foundReal := false
	for _, d := range res[tmp].Diags {
		if d.Severity == diag.Error && strings.Contains(d.Message, "imported and not used") {
			foundReal = true
			if d.Start.Line != 4 {
				t.Errorf("real unused-import error not positioned at the dot spec (line 4): %+v", d)
			}
		}
	}
	if !foundReal {
		t.Fatalf("the dot import's REAL unused-import error was swallowed by the sunk-import filter; diags = %+v", res[tmp].Diags)
	}
}

// assertOnlyInferenceUnavailable asserts diags contain at least one
// inference-unavailable warning and NO Error-severity diagnostic (the failure
// mode the fail-safe regression tests guard against is a hard error alongside
// — or instead of — the warning).
func assertOnlyInferenceUnavailable(t *testing.T, diags []diag.Diagnostic) {
	t.Helper()
	foundWarn := false
	for _, d := range diags {
		if d.Code == "inference-unavailable" && d.Severity == diag.Warning {
			foundWarn = true
			continue
		}
		if d.Severity == diag.Error {
			t.Errorf("unexpected error diagnostic alongside the fail-safe warning: %+v", d)
		}
	}
	if !foundWarn {
		t.Fatalf("expected an inference-unavailable warning; diags = %+v", diags)
	}
}

// writeGeneratedAndBuild writes every generated .x.go next to its .gsx (the
// TestBuildTagExcludesGeneratedFile pattern) and asserts `go build ./...`
// succeeds from moduleDir — the strongest check that the fail-safe emitted
// COMPILING Go, not just warning-only diagnostics.
func writeGeneratedAndBuild(t *testing.T, moduleDir string, results map[string]DirResult) {
	t.Helper()
	for _, r := range results {
		for gsxPath, src := range r.Files {
			base := strings.TrimSuffix(gsxPath, ".gsx")
			if werr := os.WriteFile(base+".x.go", src, 0o644); werr != nil {
				t.Fatal(werr)
			}
		}
	}
	build := exec.Command("go", "build", "./...")
	build.Dir = moduleDir
	if bout, berr := build.CombinedOutput(); berr != nil {
		t.Fatalf("go build of generated output: %v\n%s", berr, bout)
	}
}

func TestGenericInferredTagSkipsNonGenericTags(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module example.com/mixed\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	writeFile(t, tmp, "page.gsx", "package mixed\n\ncomponent Card(title string) {\n\t<div>{title}</div>\n}\n\ncomponent Button[T string | int](label T) {\n\t<button>{label}</button>\n}\n\ncomponent Page() {\n\t<Card title=\"x\" />\n\t<Button label={7} />\n}\n")

	res, err := GenerateDirs(tmp, []string{tmp}, Options{FilterPkgs: []string{stdImportPath}, CSSMinify: true, JSMinify: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if diags := res[tmp].Diags; len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %+v", diags)
	}
	var root string
	for _, src := range res[tmp].Files {
		root = string(src)
	}
	for _, want := range []string{
		"Card(CardProps{Title: \"x\"})",
		"Button[int](ButtonProps[int]{Label: 7})",
	} {
		if !strings.Contains(root, want) {
			t.Fatalf("generated source missing %q:\n%s", want, root)
		}
	}
	if strings.Contains(root, "Card[int]") {
		t.Fatalf("non-generic Card received inferred generic type args:\n%s", root)
	}
}
