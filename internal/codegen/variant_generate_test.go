package codegen

import (
	"go/token"
	"go/types"
	"maps"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/diag"
)

// hasError reports whether diags contains an Error-severity diagnostic.
func hasError(diags []diag.Diagnostic) bool {
	for _, d := range diags {
		if d.Severity == diag.Error {
			return true
		}
	}
	return false
}

// keysOfGenerated returns the sorted-order-agnostic key list of a Generate
// output map, for readable failure messages.
func keysOfGenerated(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func requireDuplicateComponentError(t *testing.T, out map[string][]byte, diags []diag.Diagnostic) {
	t.Helper()
	for _, diagnostic := range diags {
		if diagnostic.Code == "duplicate-component" && diagnostic.Severity == diag.Error {
			if len(out) != 0 {
				t.Fatalf("duplicate component emitted files %v", keysOfGenerated(out))
			}
			return
		}
	}
	t.Fatalf("diagnostics = %v, want duplicate-component error", diags)
}

func TestComponentVariantsRequireConstraintsOnEveryMember(t *testing.T) {
	for _, test := range []struct {
		name  string
		files map[string]string
	}{
		{
			name: "unconstrained",
			files: map[string]string{
				"a.gsx": "package views\ncomponent Icon(value int) { <span/> }\n",
				"b.gsx": "package views\ncomponent Icon(value int) { <span/> }\n",
			},
		},
		{
			name: "mixed",
			files: map[string]string{
				"icon_linux.gsx": "package views\ncomponent Icon(value int) { <span/> }\n",
				"icon.gsx":       "package views\ncomponent Icon(value int) { <span/> }\n",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir, module := openTestModule(t, test.files)
			out, diags, err := module.Generate(dir)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			requireDuplicateComponentError(t, out, diags)
		})
	}
}

func TestComponentVariantsAcceptFilenameConstraints(t *testing.T) {
	dir, module := openTestModule(t, map[string]string{
		"icon_linux.gsx":   "package views\ncomponent Icon(value int) { <span>linux</span> }\n",
		"icon_windows.gsx": "package views\ncomponent Icon(value int) { <span>windows</span> }\n",
	})
	out, diags, err := module.Generate(dir)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if hasError(diags) {
		t.Fatalf("diagnostics = %v, want filename-constrained variant family", diags)
	}
	if len(out) != 2 {
		t.Fatalf("generated files = %v, want both variants", keysOfGenerated(out))
	}
}

func TestComponentVariantSignatureIdentityIsSemantic(t *testing.T) {
	for _, test := range []struct {
		name    string
		files   map[string]string
		wantErr bool
	}{
		{
			name: "same type through different aliases",
			files: map[string]string{
				"icon_a.gsx": "//go:build variantA\n\npackage views\nimport h \"net/http\"\ncomponent Icon(value h.Header) { <span/> }\n",
				"icon_b.gsx": "//go:build variantB\n\npackage views\nimport header \"net/http\"\ncomponent Icon(value header.Header) { <span/> }\n",
			},
		},
		{
			name: "alpha renamed type parameter",
			files: map[string]string{
				"icon_a.gsx": "//go:build variantA\n\npackage views\ncomponent Icon[T any](value T) { <span/> }\n",
				"icon_b.gsx": "//go:build variantB\n\npackage views\ncomponent Icon[U any](value U) { <span/> }\n",
			},
		},
		{
			name: "same spelling bound to different packages",
			files: map[string]string{
				"icon_a.gsx": "//go:build variantA\n\npackage views\nimport x \"bufio\"\ncomponent Icon(value x.Reader) { <span/> }\n",
				"icon_b.gsx": "//go:build variantB\n\npackage views\nimport x \"strings\"\ncomponent Icon(value x.Reader) { <span/> }\n",
			},
			wantErr: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir, module := openTestModule(t, test.files)
			out, diags, err := module.Generate(dir)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if test.wantErr {
				requireDuplicateComponentError(t, out, diags)
				return
			}
			if hasError(diags) {
				t.Fatalf("diagnostics = %v, want semantically identical variants", diags)
			}
			if len(out) != 2 {
				t.Fatalf("generated files = %v, want both variants", keysOfGenerated(out))
			}
		})
	}
}

func TestComponentVariantMethodReceiverAliasIdentityIsSemantic(t *testing.T) {
	dir, module := openTestModule(t, map[string]string{
		"types.gsx":  "package views\ntype Receiver struct{}\ntype Alias = Receiver\ncomponent (r Receiver) Page() { <r.Card value=\"x\"/> }\n",
		"card_a.gsx": "//go:build variantA\n\npackage views\ncomponent (r Receiver) Card(value string) { <span>a</span> }\n",
		"card_b.gsx": "//go:build variantB\n\npackage views\ncomponent (r Alias) Card(value string) { <span>b</span> }\n",
		"companion.go": `package views

func variantNavProbe(r Receiver) {
	_ = r.Card
	_ = r.Card("x")
}
`,
	})
	output, diagnostics, err := module.Generate(dir)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if hasError(diagnostics) {
		t.Fatalf("diagnostics = %v, want receiver aliases in one semantic family", diagnostics)
	}
	if len(output) != 3 {
		t.Fatalf("generated files = %v, want both variants and shared types", keysOfGenerated(output))
	}
	if got := module.externalLoads(); got != 1 {
		t.Fatalf("external loads after cold variant Generate = %d, want one", got)
	}

	module.SetOverride(filepath.Join(dir, "card_b.gsx"), []byte("//go:build variantB\n\npackage views\ncomponent (r Alias) Card(value string) { <span>warm</span> }\n"))
	output, diagnostics, err = module.Generate(dir)
	if err != nil {
		t.Fatalf("warm Generate: %v", err)
	}
	if hasError(diagnostics) || len(output) != 3 {
		t.Fatalf("warm output=%v diagnostics=%v, want all variant files", keysOfGenerated(output), diagnostics)
	}
	if got := module.externalLoads(); got != 1 {
		t.Fatalf("external loads after warm variant edit = %d, want frozen importer reused", got)
	}

	pkg, err := module.Package(dir)
	if err != nil {
		t.Fatalf("Package: %v", err)
	}
	signatureMembers := 0
	for component, refs := range pkg.SigTypes {
		if component.Name == "Card" && len(refs) != 0 {
			signatureMembers++
		}
	}
	if signatureMembers != 2 {
		t.Fatalf("Card members with signature type refs = %d, want both receiver spellings", signatureMembers)
	}
	cardEntries := 0
	for _, cross := range pkg.CrossIndex {
		if cross.Name != "Card" {
			continue
		}
		cardEntries++
		if len(cross.Decls) != 2 || len(cross.Refs) == 0 {
			t.Fatalf("Card cross-index = %+v, want both alias declarations and the method-value tag reference", cross)
		}
	}
	if cardEntries != 1 {
		t.Fatalf("Card cross-index entries = %d, want one semantic family", cardEntries)
	}
	navTargets := map[string]string{"Card": "card_a.gsx"}
	for _, nav := range pkg.NavIndex {
		if !strings.HasSuffix(nav.From.Filename, "companion.go") {
			continue
		}
		if wantFile, ok := navTargets[nav.Name]; ok {
			if !strings.HasSuffix(nav.To.Filename, wantFile) {
				t.Fatalf("%s nav target = %s, want %s", nav.Name, nav.To.Filename, wantFile)
			}
			delete(navTargets, nav.Name)
		}
	}
	if len(navTargets) != 0 {
		t.Fatalf("public component navigation targets missing: %v; all refs: %+v", navTargets, pkg.NavIndex)
	}
}

func TestComponentVariantReceiverFamilyRejectsValuePointerMismatch(t *testing.T) {
	dir, module := openTestModule(t, map[string]string{
		"types.gsx":  "package views\ntype Receiver struct{}\n",
		"card_a.gsx": "//go:build variantA\n\npackage views\ncomponent (r Receiver) Card() { <span>a</span> }\n",
		"card_b.gsx": "//go:build variantB\n\npackage views\ncomponent (r *Receiver) Card() { <span>b</span> }\n",
	})
	output, diagnostics, err := module.Generate(dir)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	requireDuplicateComponentError(t, output, diagnostics)
	for _, diagnostic := range diagnostics {
		if strings.Contains(diagnostic.Message, "already declared") {
			t.Fatalf("raw Go method redeclaration leaked: %v", diagnostics)
		}
	}
}

func TestComponentVariantGenericReceiverIdentityIsAlphaEquivalent(t *testing.T) {
	dir, module := openTestModule(t, map[string]string{
		"types.gsx":  "package views\ntype Receiver[T ~int] struct{}\n",
		"card_a.gsx": "//go:build variantA\n\npackage views\ncomponent (r Receiver[T]) Card() { <span>a</span> }\n",
		"card_b.gsx": "//go:build variantB\n\npackage views\ncomponent (r Receiver[U]) Card() { <span>b</span> }\n",
	})
	output, diagnostics, err := module.Generate(dir)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if hasError(diagnostics) {
		t.Fatalf("diagnostics = %v, want alpha-equivalent receiver parameters in one family", diagnostics)
	}
	if len(output) != 3 {
		t.Fatalf("generated files = %v, want both variants and shared receiver type", keysOfGenerated(output))
	}
}

func TestComponentVariantSignatureAlphaEquivalenceIncludesReceiverParameterUses(t *testing.T) {
	pkg := types.NewPackage("example.com/views", "views")
	constraint := types.NewInterfaceType(nil, []types.Type{
		types.NewUnion([]*types.Term{types.NewTerm(true, types.Typ[types.Int])}),
	}).Complete()
	originParameter := types.NewTypeParam(types.NewTypeName(token.NoPos, pkg, "Element", nil), constraint)
	origin := types.NewNamed(types.NewTypeName(token.NoPos, pkg, "Receiver", nil), types.NewStruct(nil, nil), nil)
	origin.SetTypeParams([]*types.TypeParam{originParameter})

	signature := func(name string, pointer, mismatched bool) *types.Signature {
		receiverParameter := types.NewTypeParam(types.NewTypeName(token.NoPos, pkg, name, nil), constraint)
		receiver, err := types.Instantiate(types.NewContext(), origin, []types.Type{receiverParameter}, false)
		if err != nil {
			t.Fatal(err)
		}
		if pointer {
			receiver = types.NewPointer(receiver)
		}
		element := types.Type(types.NewSlice(receiverParameter))
		if mismatched {
			element = receiverParameter
		}
		parameterType := types.NewMap(receiverParameter, element)
		return types.NewSignatureType(
			types.NewVar(token.NoPos, pkg, "r", receiver),
			[]*types.TypeParam{receiverParameter},
			nil,
			types.NewTuple(types.NewVar(token.NoPos, pkg, "value", parameterType)),
			types.NewTuple(),
			false,
		)
	}

	left := signature("T", false, false)
	if !componentVariantSignatureUsable(left) {
		t.Fatal("valid generic receiver signature was classified unusable")
	}
	if right := signature("U", false, false); !identicalComponentVariantSignatures(left, right) {
		t.Fatal("Receiver[T] and Receiver[U] signatures with alpha-renamed nested parameter uses are not identical")
	}
	if pointer := signature("U", true, false); identicalComponentVariantSignatures(left, pointer) {
		t.Fatal("value and pointer receiver signatures compared identical")
	}
	if mismatch := signature("U", false, true); identicalComponentVariantSignatures(left, mismatch) {
		t.Fatal("different parameter types compared identical after receiver alpha-renaming")
	}
}

func TestComponentVariantReceiverIdentityIgnoresInvalidNamedUnderlyingType(t *testing.T) {
	dir, module := openTestModule(t, map[string]string{
		"types.gsx":  "package views\ntype Receiver struct { Bad Missing }\ntype Alias = Receiver\n",
		"card_a.gsx": "//go:build variantA\n\npackage views\ncomponent (r Receiver) Card(value string) { <span>a</span> }\n",
		"card_b.gsx": "//go:build variantB\n\npackage views\ncomponent (r Alias) Card(value string) { <span>b</span> }\n",
	})
	output, diagnostics, err := module.Generate(dir)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(output) != 0 {
		t.Fatalf("output = %v, want the authored Missing type error to block emission", keysOfGenerated(output))
	}
	foundMissing := false
	for _, diagnostic := range diagnostics {
		if strings.Contains(diagnostic.Message, "undefined: Missing") {
			foundMissing = true
		}
		if strings.Contains(diagnostic.Message, "already declared") || diagnostic.Code == "duplicate-component" {
			t.Fatalf("diagnostics = %v, receiver identity must not add a synthetic redeclaration error", diagnostics)
		}
	}
	if !foundMissing {
		t.Fatalf("diagnostics = %v, want the authored undefined Missing error", diagnostics)
	}
}

func TestInvalidComponentVariantSignaturesNeverGraduate(t *testing.T) {
	for _, test := range []struct {
		name       string
		first      string
		last       string
		additional map[string]string
	}{
		{name: "invalid types in path order", first: "MissingA", last: "MissingB"},
		{name: "invalid types in reverse order", first: "MissingB", last: "MissingA"},
		{name: "valid but different types", first: "int", last: "string"},
		{name: "identical invalid map key", first: "map[[]int]string", last: "map[[]int]string"},
		{
			name:  "identical failed instantiation",
			first: "Box[int]",
			last:  "Box[int]",
			additional: map[string]string{
				"types.gsx": "package views\ntype Box[T ~string] struct{}\n",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			files := map[string]string{
				"icon_a.gsx": "//go:build variantA\n\npackage views\ncomponent Icon(value " + test.first + ") { <span>a</span> }\n",
				"icon_b.gsx": "//go:build variantB\n\npackage views\ncomponent Icon(value " + test.last + ") { <span>b</span> }\n",
			}
			maps.Copy(files, test.additional)
			dir, module := openTestModule(t, files)
			pkg, err := module.Package(dir)
			if err != nil {
				t.Fatalf("Package: %v", err)
			}
			foundDuplicate := false
			for _, diagnostic := range pkg.Diags {
				foundDuplicate = foundDuplicate || diagnostic.Code == "duplicate-component"
			}
			if !foundDuplicate {
				t.Fatalf("diagnostics = %v, want unprovable variant family rejected", pkg.Diags)
			}
			for key, cross := range pkg.CrossIndex {
				if cross.Name == "Icon" && len(cross.Decls) > 1 {
					t.Fatalf("invalid family graduated as shared identity %q: %+v", key, cross)
				}
			}
		})
	}
}

func TestNonLocalReceiverCannotJoinLocalVariantFamily(t *testing.T) {
	dir, module := openTestModule(t, map[string]string{
		"types.gsx":  "package views\ntype Client struct{}\n",
		"card_a.gsx": "//go:build variantA\n\npackage views\ncomponent (c Client) Card() { <span>a</span> }\n",
		"card_b.gsx": "//go:build variantB\n\npackage views\nimport \"net/http\"\ncomponent (c http.Client) Card() { <span>b</span> }\n",
	})
	pkg, err := module.Package(dir)
	if err != nil {
		t.Fatalf("Package: %v", err)
	}
	foundIllegalReceiver := false
	for _, diagnostic := range pkg.Diags {
		if strings.Contains(diagnostic.Message, "non-local type") || strings.Contains(diagnostic.Message, "cannot define new methods") {
			foundIllegalReceiver = true
		}
	}
	if !foundIllegalReceiver {
		t.Fatalf("diagnostics = %v, want authored non-local receiver error", pkg.Diags)
	}
	for key, cross := range pkg.CrossIndex {
		if cross.Name == "Card" && len(cross.Decls) > 1 {
			t.Fatalf("non-local receiver joined local identity %q: %+v", key, cross)
		}
	}
}

func TestReservedAnalysisNameInVariantReturnsPositionedDiagnostic(t *testing.T) {
	dir, module := openTestModule(t, map[string]string{
		"icon_a.gsx": `//go:build variantA

package views

var _gsxtargetbody1 = 1

component Icon(value string) { <span>a</span> }
`,
		"icon_b.gsx": "//go:build variantB\n\npackage views\ncomponent Icon(value string) { <span>b</span> }\n",
	})
	pkg, err := module.Package(dir)
	if err != nil {
		t.Fatalf("Package returned infrastructure error instead of source diagnostic: %v", err)
	}
	for _, diagnostic := range pkg.Diags {
		if strings.HasSuffix(diagnostic.Start.Filename, "icon_a.gsx") && diagnostic.Start.Line == 5 && strings.Contains(diagnostic.Message, "reserved _gsx prefix") {
			return
		}
	}
	t.Fatalf("diagnostics = %v, want positioned reserved-name error", pkg.Diags)
}

func TestUnresolvedSameSpellingMethodDeclarationsKeepDistinctLSPIdentities(t *testing.T) {
	dir, module := openTestModule(t, map[string]string{
		"card_a.gsx": "//go:build variantA\n\npackage views\ncomponent (r Missing) Card(value string) { <span/> }\n",
		"card_b.gsx": "//go:build variantB\n\npackage views\ncomponent (r Missing) Card(value string) { <span/> }\n",
	})
	pkg, err := module.Package(dir)
	if err != nil {
		t.Fatal(err)
	}
	cardEntries := 0
	for key, cross := range pkg.CrossIndex {
		if cross.Name != "Card" {
			continue
		}
		cardEntries++
		if !strings.HasPrefix(key, "!unresolved-receiver:") || len(cross.Decls) != 1 {
			t.Fatalf("Card identity %q = %+v, want one opaque declaration", key, cross)
		}
	}
	if cardEntries != 2 {
		t.Fatalf("Card CrossIndex entries = %d, want unresolved declarations isolated", cardEntries)
	}
	signatureMembers := 0
	for component, refs := range pkg.SigTypes {
		if component.Name == "Card" && len(refs) != 0 {
			signatureMembers++
		}
	}
	if signatureMembers != 2 {
		t.Fatalf("Card navigable signature members = %d, want both isolated declarations", signatureMembers)
	}
}

func TestComponentVariantAnalysisNamesAvoidCompanionDeclarations(t *testing.T) {
	dir, module := openTestModule(t, map[string]string{
		"a_icon.gsx": "//go:build variantA\n\npackage views\ncomponent Icon(value string) { <span>a</span> }\n",
		"b_icon.gsx": "//go:build variantB\n\npackage views\ncomponent Icon(value string) { <span>b</span> }\n",
		"c_card.gsx": "//go:build variantA\n\npackage views\ncomponent (r Receiver) Card(value string) { <span>a</span> }\n",
		"d_card.gsx": "//go:build variantB\n\npackage views\ncomponent (r Receiver) Card(value string) { <span>b</span> }\n",
		"companion.go": `package views

type Receiver struct { _gsxtargetbody6 int }

func _gsxtargetbody1() {}
type _gsxtargetprops2 struct{}
type Receiver_gsxtargetbody7Props struct{}
`,
	})
	output, diagnostics, err := module.Generate(dir)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if hasError(diagnostics) {
		t.Fatalf("diagnostics = %v, want collision-free analysis names", diagnostics)
	}
	if len(output) != 4 {
		t.Fatalf("generated files = %v, want both variants", keysOfGenerated(output))
	}
}

func TestComponentVariantAnalysisNamesCannotSatisfyAuthoredReferences(t *testing.T) {
	dir, module := openTestModule(t, map[string]string{
		"icon_a.gsx": "//go:build variantA\n\npackage views\ncomponent Icon(value string) { <span>a</span> }\n",
		"icon_b.gsx": "//go:build variantB\n\npackage views\ncomponent Icon(value string) { <span>b</span> }\n",
		"companion.go": `package views

var leaked _gsxtargetprops1
`,
	})
	pkg, err := module.Package(dir)
	if err != nil {
		t.Fatalf("Package: %v", err)
	}
	foundUndefined := false
	for _, diagnostic := range pkg.Diags {
		if strings.HasSuffix(diagnostic.Start.Filename, "companion.go") && strings.Contains(diagnostic.Message, "undefined: _gsxtargetprops1") {
			foundUndefined = true
		}
	}
	if !foundUndefined {
		t.Fatalf("diagnostics = %v, want authored private-name reference to remain undefined", pkg.Diags)
	}
	for _, nav := range pkg.NavIndex {
		if strings.HasSuffix(nav.From.Filename, "companion.go") && nav.Name == "_gsxtargetprops1" {
			t.Fatalf("analysis-only declaration became a navigation target: %+v", nav)
		}
	}
}

func TestComponentVariantAnalysisNamesAvoidDotImportScope(t *testing.T) {
	dir, module := openTestModule(t, map[string]string{
		"card_a.gsx": `//go:build variantA

package views
import . "testmod/dotdep"
var _ Receiver_gsxtargetbody1Props
type Receiver struct{}
component (r Receiver) Card(value string) { <span>a</span> }
`,
		"card_b.gsx": "//go:build variantB\n\npackage views\ncomponent (r Receiver) Card(value string) { <span>b</span> }\n",
	})
	writeFile(t, dir, "dotdep/dep.go", "package dotdep\ntype Receiver_gsxtargetbody1Props struct{}\n")
	output, diagnostics, err := module.Generate(dir)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if hasError(diagnostics) {
		t.Fatalf("diagnostics = %v, want dot-import-aware collision-free names", diagnostics)
	}
	if len(output) != 2 {
		t.Fatalf("generated files = %v, want both variants", keysOfGenerated(output))
	}
}

func TestComponentVariantBrokenDefaultImportRemainsPositionedDiagnostic(t *testing.T) {
	dir, module := openTestModule(t, map[string]string{
		"icon_a.gsx": "//go:build variantA\n\npackage views\nimport \"example.com/does-not-exist\"\ncomponent Icon(value string) { <span>a</span> }\n",
		"icon_b.gsx": "//go:build variantB\n\npackage views\ncomponent Icon(value string) { <span>b</span> }\n",
	})
	output, diagnostics, err := module.Generate(dir)
	if err != nil {
		t.Fatalf("Generate infrastructure error = %v, want authored import diagnostic", err)
	}
	if len(output) != 0 {
		t.Fatalf("broken import emitted files %v", keysOfGenerated(output))
	}
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == diag.Error && strings.HasSuffix(diagnostic.Start.Filename, "icon_a.gsx") && diagnostic.Start.Line == 4 {
			return
		}
	}
	t.Fatalf("diagnostics = %v, want positioned error on icon_a.gsx import", diagnostics)
}

func TestBundleVariantAnalysisNamesUseDefaultImportPackageIdentity(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module testmod\n\ngo 1.26.1\n")
	writeFile(t, root, "icon_a.gsx", `//go:build variantA

package views

import "example.com/helper"

component Icon(value _gsxtargetbody1.Value) { <span>a</span> }
`)
	writeFile(t, root, "icon_b.gsx", `//go:build variantB

package views

import helper "example.com/helper"

component Icon(value helper.Value) { <span>b</span> }
`)

	helper := types.NewPackage("example.com/helper", "_gsxtargetbody1")
	value := types.NewTypeName(token.NoPos, helper, "Value", nil)
	types.NewNamed(value, types.Typ[types.String], nil)
	helper.Scope().Insert(value)
	helper.MarkComplete()
	imports := targetTestImporter().(mapImporter)
	imports[helper.Path()] = helper
	module, err := Open(Options{
		ModuleRoot: root,
		ModulePath: "testmod",
		Bundle:     testBundle(imports, funcTables{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	output, diagnostics, err := module.Generate(root)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if hasError(diagnostics) || len(output) != 2 {
		t.Fatalf("Bundle output=%v diagnostics=%v, want collision-free default-import identity", keysOfGenerated(output), diagnostics)
	}
	if got := module.externalLoads(); got != 0 {
		t.Fatalf("Bundle external loads = %d, want zero", got)
	}
}

func TestRawGoCrossFileRedeclarationIsNeverAComponentVariant(t *testing.T) {
	dir, module := openTestModule(t, map[string]string{
		"a.gsx": "//go:build variantA\n\npackage views\nfunc helper() {}\ncomponent A() { <span/> }\n",
		"b.gsx": "//go:build variantB\n\npackage views\nfunc helper() {}\ncomponent B() { <span/> }\n",
	})
	out, diags, err := module.Generate(dir)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !hasError(diags) {
		t.Fatalf("diagnostics = %v output = %v, want raw Go redeclaration error", diags, keysOfGenerated(out))
	}
	if len(out) != 0 {
		t.Fatalf("raw Go redeclaration emitted files %v", keysOfGenerated(out))
	}
}

// TestSameSigVariantGeneratesAllFiles is the regression for the bug this
// subsystem fixes: two .gsx files under disjoint //go:build tags declaring a
// same-name/same-signature component (a legitimate build-tag variant) used to
// produce a cross-file "redeclared in this block" go/types error, which
// blocked emission for the WHOLE package — not just the redeclared component.
// The component-only target plan must fold the logical public declaration while
// keeping every variant body in the package-wide analysis universe.
func TestSameSigVariantGeneratesAllFiles(t *testing.T) {
	dir, m := openTestModule(t, map[string]string{
		"icon_linux.gsx":   "//go:build linux\n\npackage views\n\ncomponent Icon(name string) { <span>linux:{ name }</span> }\n",
		"icon_windows.gsx": "//go:build windows\n\npackage views\n\ncomponent Icon(name string) { <span>win:{ name }</span> }\n",
		"page.gsx":         "package views\n\ncomponent Page() { <Icon name=\"x\"/> }\n",
	})
	out, diags, err := m.Generate(dir)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if hasError(diags) {
		t.Fatalf("unexpected error diagnostics: %v", diags)
	}
	for _, want := range []string{"icon_linux.gsx", "icon_windows.gsx", "page.gsx"} {
		if _, ok := out[filepath.Join(dir, want)]; !ok {
			t.Fatalf("missing generated output for %s; got keys %v", want, keysOfGenerated(out))
		}
	}
	linuxOut := out[filepath.Join(dir, "icon_linux.gsx")]
	if !strings.Contains(string(linuxOut), "//go:build linux") {
		t.Fatalf("linux variant lost its build constraint:\n%s", linuxOut)
	}
}

// TestDiffSigVariantIsCleanError covers the genuine-conflict side: a same-name
// component declared with DIFFERENT signatures across build-tagged files is a
// real ambiguity (gsx does not parse build tags, so it cannot know which
// signature wins). This must surface as a single clean duplicate-component
// diagnostic — never a raw go/types "redeclared in this block" — and must
// block emission entirely (not just for the conflicting component).
func TestDiffSigVariantIsCleanError(t *testing.T) {
	dir, m := openTestModule(t, map[string]string{
		"icon_linux.gsx":   "//go:build linux\n\npackage views\n\ncomponent Icon(name string) { <span>{ name }</span> }\n",
		"icon_windows.gsx": "//go:build windows\n\npackage views\n\ncomponent Icon(name int) { <span>{ name }</span> }\n",
	})
	out, diags, err := m.Generate(dir)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !hasError(diags) {
		t.Fatalf("expected a duplicate-component error, got none: %v", diags)
	}
	foundClean := false
	for _, d := range diags {
		if d.Code == "duplicate-component" {
			foundClean = true
		}
		if strings.Contains(d.Message, "redeclared in this block") {
			t.Fatalf("raw go/types redeclared error leaked: %q", d.Message)
		}
	}
	if !foundClean {
		t.Fatalf("no duplicate-component diagnostic in %v", diags)
	}
	if len(out) != 0 {
		t.Fatalf("diff-sig conflict must block emission, got %v", keysOfGenerated(out))
	}
}

// TestMethodVariantSameSigGeneratesAllFiles is the method-component analogue of
// TestSameSigVariantGeneratesAllFiles. go/types reports a METHOD redeclaration
// with a different message ("method Form.Field already declared at FILE:L:C")
// than a func's ("redeclared in this block" + "other declaration" note). Before
// the fix, suppression did not recognize the method form, so a same-signature
// method variant under disjoint build tags blocked emission for the WHOLE
// package. All three files must now generate.
func TestMethodVariantSameSigGeneratesAllFiles(t *testing.T) {
	dir, m := openTestModule(t, map[string]string{
		"field_linux.gsx":   "//go:build linux\n\npackage views\n\ncomponent (f Form) Field(name string) { <span>linux:{ name }</span> }\n",
		"field_windows.gsx": "//go:build windows\n\npackage views\n\ncomponent (f Form) Field(name string) { <span>win:{ name }</span> }\n",
		"form.gsx":          "package views\n\ntype Form struct{}\n\ncomponent Page() { <div>page</div> }\n",
	})
	out, diags, err := m.Generate(dir)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if hasError(diags) {
		t.Fatalf("unexpected error diagnostics: %v", diags)
	}
	for _, want := range []string{"field_linux.gsx", "field_windows.gsx", "form.gsx"} {
		if _, ok := out[filepath.Join(dir, want)]; !ok {
			t.Fatalf("missing generated output for %s; got keys %v", want, keysOfGenerated(out))
		}
	}
	linuxOut := out[filepath.Join(dir, "field_linux.gsx")]
	if !strings.Contains(string(linuxOut), "//go:build linux") {
		t.Fatalf("linux variant lost its build constraint:\n%s", linuxOut)
	}
}

// TestMethodVariantDiffSigIsCleanError is the method-component analogue of
// TestDiffSigVariantIsCleanError: differing method signatures are a genuine
// ambiguity and must surface as a single clean duplicate-component diagnostic,
// never a raw go/types "already declared"/"redeclared" leak, with emission
// blocked entirely.
func TestMethodVariantDiffSigIsCleanError(t *testing.T) {
	dir, m := openTestModule(t, map[string]string{
		"field_linux.gsx":   "//go:build linux\n\npackage views\n\ncomponent (f Form) Field(name string) { <span>{ name }</span> }\n",
		"field_windows.gsx": "//go:build windows\n\npackage views\n\ncomponent (f Form) Field(name int) { <span>{ name }</span> }\n",
		"form.gsx":          "package views\n\ntype Form struct{}\n",
	})
	out, diags, err := m.Generate(dir)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !hasError(diags) {
		t.Fatalf("expected a duplicate-component error, got none: %v", diags)
	}
	foundClean := false
	for _, d := range diags {
		if d.Code == "duplicate-component" {
			foundClean = true
		}
		if strings.Contains(d.Message, "already declared") || strings.Contains(d.Message, "redeclared") {
			t.Fatalf("raw go/types method-redeclaration error leaked: %q", d.Message)
		}
	}
	if !foundClean {
		t.Fatalf("no duplicate-component diagnostic in %v", diags)
	}
	if len(out) != 0 {
		t.Fatalf("diff-sig conflict must block emission, got %v", keysOfGenerated(out))
	}
}

// TestWithinFileRedeclarationKeptDespiteVariant pins finding #3: a name
// redeclared BOTH within file A (a real mistake) AND across files A/B (a
// same-signature variant) must NOT be silently generated — the within-file
// redeclaration stays a hard error. The over-reaching name+file-count
// suppression used to drop the within-file error too (its name spanned ≥2
// files). Detection now comes from the skeleton ASTs (collectRedeclFacts), so
// it is order-independent and exact.
func TestWithinFileRedeclarationKeptDespiteVariant(t *testing.T) {
	dir, m := openTestModule(t, map[string]string{
		"icon_a.gsx": "package views\n\ncomponent Icon(name string) { <a>{ name }</a> }\ncomponent Icon(name string) { <b>{ name }</b> }\n",
		"icon_b.gsx": "//go:build windows\n\npackage views\n\ncomponent Icon(name string) { <c>{ name }</c> }\n",
	})
	out, diags, err := m.Generate(dir)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !hasError(diags) {
		t.Fatalf("within-file redeclaration must stay a hard error, got diags=%v out=%v", diags, keysOfGenerated(out))
	}
	if len(out) != 0 {
		t.Fatalf("within-file redeclaration must block emission, got %v", keysOfGenerated(out))
	}
}
