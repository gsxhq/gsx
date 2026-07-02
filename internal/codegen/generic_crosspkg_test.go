package codegen

import (
	"os"
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
