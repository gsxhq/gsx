package codegen

import (
	"bytes"
	"go/token"
	"go/types"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/sourceview"
	gsxparser "github.com/gsxhq/gsx/parser"
)

func directTestComponent(t *testing.T, declaration string) *gsxast.Component {
	t.Helper()
	fset := token.NewFileSet()
	file, err := gsxparser.ParseFile(fset, "component.gsx", []byte("package p\n"+declaration+" { <span/> }\n"), 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, declaration := range file.Decls {
		if component, ok := declaration.(*gsxast.Component); ok {
			return component
		}
	}
	t.Fatal("component declaration not found")
	return nil
}

func TestDirectComponentDeclarationEligibility(t *testing.T) {
	tests := []struct {
		name           string
		declaration    string
		wantEligible   bool
		wantTypeParams []string
		wantParams     []string
		wantVariadic   bool
	}{
		{name: "plain", declaration: "component Child(value string)", wantEligible: true, wantParams: []string{"value"}},
		{name: "generic", declaration: "component Child[T any](value T)", wantEligible: true, wantTypeParams: []string{"T"}, wantParams: []string{"value"}},
		{name: "grouped generic", declaration: "component Child[T, U any](left T, right U)", wantEligible: true, wantTypeParams: []string{"T", "U"}, wantParams: []string{"left", "right"}},
		{name: "constraint only generic", declaration: "component Child[T interface{ ~string }]()", wantEligible: true, wantTypeParams: []string{"T"}},
		{name: "attrs variadic", declaration: "component Child(attrs ...gsx.Attr)", wantEligible: true, wantParams: []string{"attrs"}, wantVariadic: true},
		{name: "ordinary variadic", declaration: "component Child(prefix string, values ...int)", wantEligible: true, wantParams: []string{"prefix", "values"}, wantVariadic: true},
		{name: "unnamed value", declaration: "component Child(string)", wantEligible: false},
		{name: "blank value", declaration: "component Child(_ string)", wantEligible: false},
		{name: "blank type", declaration: "component Child[_ any](value string)", wantEligible: false},
		{name: "ctx type", declaration: "component Child[ctx any](value string)", wantEligible: false},
		{name: "reserved prefix type", declaration: "component Child[_gsxT any](value string)", wantEligible: false},
		{name: "method", declaration: "component (v View) Child(value string)", wantEligible: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok, err := directComponentDeclarationFor(directTestComponent(t, tt.declaration))
			if err != nil {
				t.Fatal(err)
			}
			if ok != tt.wantEligible {
				t.Fatalf("eligible = %v, want %v; metadata = %+v", ok, tt.wantEligible, got)
			}
			if !ok {
				return
			}
			if !reflect.DeepEqual(got.typeParamNames, tt.wantTypeParams) || !reflect.DeepEqual(got.paramNames, tt.wantParams) || got.variadic != tt.wantVariadic {
				t.Fatalf("metadata = %+v, want type params %v, params %v, variadic %v", got, tt.wantTypeParams, tt.wantParams, tt.wantVariadic)
			}
		})
	}
}

func TestDirectComponentTargetRequiresLocalGSXPackageFunction(t *testing.T) {
	local := types.NewPackage("example.com/p", "p")
	foreign := types.NewPackage("example.com/q", "q")
	localFunc := types.NewFunc(token.NoPos, local, "Child", types.NewSignatureType(nil, nil, nil, nil, nil, false))
	foreignFunc := types.NewFunc(token.NoPos, foreign, "Child", types.NewSignatureType(nil, nil, nil, nil, nil, false))
	direct := &directComponentFamily{logicalKey: "Child", helperName: "_gsxrenderChild"}
	tests := []struct {
		name string
		fact componentTargetFact
		want bool
	}{
		{name: "local gsx function", fact: componentTargetFact{origin: localFunc, provenance: targetPackageFunc, declaration: componentTargetDeclarationProvenance{direct: direct}}, want: true},
		{name: "imported gsx function", fact: componentTargetFact{origin: foreignFunc, provenance: targetPackageFunc, declaration: componentTargetDeclarationProvenance{direct: direct}}},
		{name: "local plain go function", fact: componentTargetFact{origin: localFunc, provenance: targetPackageFunc}},
		{name: "package variable", fact: componentTargetFact{origin: types.NewVar(token.NoPos, local, "Child", localFunc.Type()), provenance: targetPackageVar, declaration: componentTargetDeclarationProvenance{direct: direct}}},
		{name: "bound method", fact: componentTargetFact{origin: localFunc, provenance: targetConcreteMethodValue, declaration: componentTargetDeclarationProvenance{direct: direct}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := directComponentTarget(tt.fact, local); (got != nil) != tt.want {
				t.Fatalf("direct target = %+v, want present %v", got, tt.want)
			}
		})
	}
}

func TestPlanComponentPositionalCallsPropagatesDirectDeclaration(t *testing.T) {
	fixture := newSignatureRuntimeFixture(t)
	element, fset := plannerElement(t, `<C/>`)
	pkg := types.NewPackage("example.com/p", "p")
	signature := types.NewSignatureType(nil, nil, nil, nil, types.NewTuple(types.NewVar(token.NoPos, pkg, "", fixture.runtime.node)), false)
	function := types.NewFunc(token.NoPos, pkg, "C", signature)
	direct := &directComponentFamily{logicalKey: "C", helperName: "_gsxrenderC"}
	plan, diagnostics := planComponentPositionalCalls(componentPositionalPlanningInput{
		callSites:       positionalTestRegistry(element),
		targets:         map[callSiteID]componentTargetFact{1: {site: 1, raw: signature, object: function, origin: function, provenance: targetPackageFunc, declaration: componentTargetDeclarationProvenance{direct: direct}}},
		expressionFacts: map[gsxast.Node]expressionFact{},
		runtime:         fixture.runtime,
		analysisPackage: pkg,
		fset:            fset,
	})
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	if got := plan.sites[1].directTarget; got == nil || got.helperName != "_gsxrenderC" {
		t.Fatalf("site direct target = %+v", got)
	}
}

func TestRequiredAliasTypeParameterHasPositionedDiagnostic(t *testing.T) {
	for _, alias := range []string{"_gsxrt", "_gsxctx", "_gsxio"} {
		t.Run(alias, func(t *testing.T) {
			dir, module := openTestModule(t, map[string]string{
				"page.gsx": "package p\ncomponent Child[" + alias + " any]() { <span/> }\n",
			})
			output, diagnostics, err := module.Generate(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(output) != 0 {
				t.Fatalf("generated output = %v, want none", output)
			}
			found := false
			for _, diagnostic := range diagnostics {
				if diagnostic.Start.Filename == filepath.Join(dir, "page.gsx") && diagnostic.Start.Line == 2 && diagnostic.Start.Column > 1 && strings.Contains(diagnostic.Message, alias) && strings.Contains(diagnostic.Message, "reserved") {
					found = true
				}
			}
			if !found {
				t.Fatalf("diagnostics = %+v, want positioned reserved alias diagnostic", diagnostics)
			}
		})
	}
}

func TestNonCollidingReservedPrefixTypeParameterIsACompileableFallback(t *testing.T) {
	dir, module := openTestModule(t, map[string]string{
		"page.gsx": "package p\ncomponent Child[_gsxT any]() { <span/> }\n",
	})
	output, diagnostics, err := module.Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if hasError(diagnostics) || len(output) != 1 {
		t.Fatalf("output = %v, diagnostics = %+v, want successful fallback generation", output, diagnostics)
	}
	module.mu.Lock()
	provenance := module.targetDeclProvenance[dir][".Child"]
	module.mu.Unlock()
	if provenance.direct != nil {
		t.Fatalf("direct provenance = %+v, want conservative fallback", provenance.direct)
	}
}

func TestDirectHelperOccupiedNamesIncludesEveryNonOwnedSamePackageGoFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name, source string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(source), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("hand.go", "package p\nfunc _gsxrenderHand() {}\n")
	write("orphan.x.go", "package p\nfunc _gsxrenderOther() {}\n")
	write("same_test.go", "package p\nvar _gsxrenderThird int\n")
	write("external_test.go", "package p_test\nfunc _gsxrenderExternal() {}\n")
	write("child.x.go", "package p\nfunc _gsxrenderChild() {}\n")
	files := map[string]*gsxast.File{filepath.Join(dir, "child.gsx"): parseGSXForTest(t, `package p
func _gsxrenderChunk() {}
component Child() { <span/> }
`)}
	view := map[string]sourceview.FileSnapshot{}
	for _, name := range []string{"hand.go", "orphan.x.go", "same_test.go", "external_test.go", "child.x.go"} {
		path := filepath.Join(dir, name)
		view[path] = sourceview.ReadFileSnapshot(path)
	}
	got, err := directHelperOccupiedNamesFromView("p", files, view)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"_gsxrenderHand", "_gsxrenderOther", "_gsxrenderThird", "_gsxrenderChunk"} {
		if !got[want] {
			t.Errorf("missing occupied name %q", want)
		}
	}
	for _, absent := range []string{"_gsxrenderExternal", "_gsxrenderChild"} {
		if got[absent] {
			t.Errorf("unexpected occupied name %q", absent)
		}
	}
}

func TestDirectHelperOccupiedNamesSurfacesParseErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "broken.go"), []byte("package p\nfunc broken("), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "broken.go")
	if _, err := directHelperOccupiedNamesFromView("p", nil, map[string]sourceview.FileSnapshot{path: sourceview.ReadFileSnapshot(path)}); err == nil {
		t.Fatal("missing parse error")
	}
}

func TestAssignDirectComponentDeclarationsUsesOneDeterministicFamilyName(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "collision_test.go"), []byte("package p\nfunc _gsxrenderChild() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := map[string]*gsxast.File{
		filepath.Join(dir, "child_a.gsx"): parseGSXForTest(t, "//go:build !alternate\n\npackage p\ncomponent Child[T any](value T) { <span/> }\n"),
		filepath.Join(dir, "child_b.gsx"): parseGSXForTest(t, "//go:build alternate\n\npackage p\ncomponent Child[U any](value U) { <span/> }\n"),
		filepath.Join(dir, "parent.gsx"):  parseGSXForTest(t, "package p\ncomponent Parent() { <Child[string] value=\"x\"/> }\n"),
		filepath.Join(dir, "other.gsx"):   parseGSXForTest(t, "package p\ncomponent Other() { <Child[string] value=\"y\"/> }\n"),
	}
	plan := syntacticComponentTargetPlan(files)
	var err error
	goFiles := map[string]sourceview.FileSnapshot{
		filepath.Join(dir, "collision_test.go"): sourceview.ReadFileSnapshot(filepath.Join(dir, "collision_test.go")),
	}
	plan, err = assignDirectComponentDeclarationsFromView("p", files, plan, goFiles)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string][]string{}
	for component, emission := range plan.emissions {
		if component.Name != "Child" {
			continue
		}
		if emission.direct == nil {
			t.Fatalf("Child[%s] has no direct metadata", component.TypeParams)
		}
		seen[emission.direct.family.helperName] = append(seen[emission.direct.family.helperName], emission.direct.typeParamNames...)
	}
	if got := seen["_gsxrenderChild1"]; len(got) != 2 || !((got[0] == "T" && got[1] == "U") || (got[0] == "U" && got[1] == "T")) {
		t.Fatalf("allocated metadata = %v, want one suffixed family carrying T and U declaration names", seen)
	}

	second, err := assignDirectComponentDeclarationsFromView("p", files, syntacticComponentTargetPlan(files), goFiles)
	if err != nil {
		t.Fatal(err)
	}
	for component, emission := range plan.emissions {
		if emission.direct != nil && second.emissions[component].direct.family.helperName != emission.direct.family.helperName {
			t.Fatalf("repeated allocation changed %s helper from %s to %s", component.Name, emission.direct.family.helperName, second.emissions[component].direct.family.helperName)
		}
	}
}

func TestDirectHelperCollisionFixturesCompile(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns the go toolchain")
	}
	for _, collisionFile := range []string{"orphan.x.go", "helper_test.go"} {
		t.Run(collisionFile, func(t *testing.T) {
			dir, module := openTestModule(t, map[string]string{
				"page.gsx": "package p\ncomponent Child(value string) { <span>child</span> }\ncomponent Parent() { <Child value=\"x\"/> }\n",
			})
			if err := os.WriteFile(filepath.Join(dir, collisionFile), []byte("package p\nfunc _gsxrenderChild() {}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			output, diagnostics, err := module.Generate(dir)
			if err != nil {
				t.Fatal(err)
			}
			if hasError(diagnostics) {
				t.Fatalf("generation diagnostics = %+v", diagnostics)
			}
			module.mu.Lock()
			provenance := module.targetDeclProvenance[dir][".Child"]
			module.mu.Unlock()
			if provenance.direct == nil || provenance.direct.helperName != "_gsxrenderChild1" {
				t.Fatalf("direct provenance = %+v, want suffixed collision-free helper", provenance.direct)
			}
			for sourcePath, generated := range output {
				if err := os.WriteFile(strings.TrimSuffix(sourcePath, ".gsx")+".x.go", generated, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			command := exec.Command("go", "test", "./...")
			command.Dir = dir
			if commandOutput, err := command.CombinedOutput(); err != nil {
				t.Fatalf("go test: %v\n%s", err, commandOutput)
			}
		})
	}
}

func TestDirectHelperOwnedOutputDoesNotRenameOnRegeneration(t *testing.T) {
	dir, firstModule := openTestModule(t, map[string]string{
		"page.gsx": "package p\ncomponent Child(value string) { <span>child</span> }\ncomponent Parent() { <Child value=\"x\"/> }\n",
	})
	first, diagnostics, err := firstModule.Generate(dir)
	if err != nil || hasError(diagnostics) {
		t.Fatalf("first generate: err=%v diagnostics=%+v", err, diagnostics)
	}
	firstModule.mu.Lock()
	firstProvenance := firstModule.targetDeclProvenance[dir][".Child"]
	firstModule.mu.Unlock()
	if firstProvenance.direct == nil || firstProvenance.direct.helperName != "_gsxrenderChild" {
		t.Fatalf("first direct provenance = %+v", firstProvenance.direct)
	}
	for sourcePath, generated := range first {
		if err := os.WriteFile(strings.TrimSuffix(sourcePath, ".gsx")+".x.go", generated, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	secondModule, err := Open(firstModule.opts)
	if err != nil {
		t.Fatal(err)
	}
	second, diagnostics, err := secondModule.Generate(dir)
	if err != nil || hasError(diagnostics) {
		t.Fatalf("second generate: err=%v diagnostics=%+v", err, diagnostics)
	}
	secondModule.mu.Lock()
	secondProvenance := secondModule.targetDeclProvenance[dir][".Child"]
	secondModule.mu.Unlock()
	if secondProvenance.direct == nil || secondProvenance.direct.helperName != "_gsxrenderChild" {
		t.Fatalf("second direct provenance = %+v, want owned output ignored", secondProvenance.direct)
	}
	if len(first) != len(second) {
		t.Fatalf("generated file counts differ: %d then %d", len(first), len(second))
	}
	for path, want := range first {
		if got := second[path]; !bytes.Equal(got, want) {
			t.Fatalf("regeneration changed %s\nfirst:\n%s\nsecond:\n%s", path, want, got)
		}
	}
}

func TestDirectHelperSourceOnlyIgnoresHostGoFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "host.go"), []byte("package p\nfunc _gsxrenderChild() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "page.gsx")
	module, err := Open(Options{
		ModuleRoot: dir,
		ModulePath: "example.com/virtual",
		SourceOnly: true,
		Bundle:     testBundle(targetTestImporter(), funcTables{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	module.SetOverride(path, []byte("package p\ncomponent Child() { <span/> }\ncomponent Parent() { <Child/> }\n"))
	_, diagnostics, err := module.Generate(dir)
	if err != nil || hasError(diagnostics) {
		t.Fatalf("Generate: err=%v diagnostics=%+v", err, diagnostics)
	}
	module.mu.Lock()
	provenance := module.targetDeclProvenance[dir][".Child"]
	module.mu.Unlock()
	if provenance.direct == nil || provenance.direct.helperName != "_gsxrenderChild" {
		t.Fatalf("SourceOnly direct provenance = %+v, want host-independent base helper", provenance.direct)
	}
}

func TestDirectHelperUsesFrozenManifestAndGoOverrides(t *testing.T) {
	root := t.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	dir := filepath.Join(root, "p")
	writeFile(t, dir, "page.gsx", "package p\ncomponent Child() { <span/> }\ncomponent Parent() { <Child/> }\n")
	helper := filepath.Join(dir, "helper_test.go")
	writeFile(t, dir, "helper_test.go", "package p\nfunc _gsxrenderChild() {}\n")
	manifest, err := sourceview.Build(sourceview.BuildOptions{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(helper, []byte("package p\nfunc diskChanged() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app", SourceManifest: manifest})
	if err != nil {
		t.Fatal(err)
	}
	_, diagnostics, err := module.Generate(dir)
	if err != nil || hasError(diagnostics) {
		t.Fatalf("Generate frozen manifest: err=%v diagnostics=%+v", err, diagnostics)
	}
	module.mu.Lock()
	provenance := module.targetDeclProvenance[dir][".Child"]
	module.mu.Unlock()
	if provenance.direct == nil || provenance.direct.helperName != "_gsxrenderChild1" {
		t.Fatalf("frozen direct provenance = %+v, want saved collision suffix", provenance.direct)
	}

	module.SetOverride(helper, []byte("package p\nfunc overrideName() {}\n"))
	_, diagnostics, err = module.Generate(dir)
	if err != nil || hasError(diagnostics) {
		t.Fatalf("Generate Go override: err=%v diagnostics=%+v", err, diagnostics)
	}
	module.mu.Lock()
	provenance = module.targetDeclProvenance[dir][".Child"]
	module.mu.Unlock()
	if provenance.direct == nil || provenance.direct.helperName != "_gsxrenderChild" {
		t.Fatalf("override direct provenance = %+v, want override-controlled base helper", provenance.direct)
	}
	module.ClearOverride(helper)
	_, diagnostics, err = module.Generate(dir)
	if err != nil || hasError(diagnostics) {
		t.Fatalf("Generate restored frozen helper: err=%v diagnostics=%+v", err, diagnostics)
	}
	module.mu.Lock()
	provenance = module.targetDeclProvenance[dir][".Child"]
	module.mu.Unlock()
	if provenance.direct == nil || provenance.direct.helperName != "_gsxrenderChild1" {
		t.Fatalf("restored frozen provenance = %+v, want saved collision suffix", provenance.direct)
	}
}

func TestDirectHelperRestoresCapturedSavedAbsence(t *testing.T) {
	root := t.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	dir := filepath.Join(root, "p")
	writeFile(t, dir, "page.gsx", "package p\ncomponent Child() { <span/> }\ncomponent Parent() { <Child/> }\n")
	helper := filepath.Join(dir, "helper_test.go")
	manifest, err := sourceview.Build(sourceview.BuildOptions{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app", SourceManifest: manifest})
	if err != nil {
		t.Fatal(err)
	}
	module.SetOverride(helper, []byte("package p\nfunc _gsxrenderChild() {}\n"))
	_, diagnostics, err := module.Generate(dir)
	if err != nil || hasError(diagnostics) {
		t.Fatalf("Generate present override: err=%v diagnostics=%+v", err, diagnostics)
	}
	module.mu.Lock()
	provenance := module.targetDeclProvenance[dir][".Child"]
	module.mu.Unlock()
	if provenance.direct == nil || provenance.direct.helperName != "_gsxrenderChild1" {
		t.Fatalf("present override provenance = %+v, want suffix", provenance.direct)
	}
	if err := os.WriteFile(helper, []byte("package p\nfunc _gsxrenderChild() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	module.ClearOverride(helper)
	_, diagnostics, err = module.Generate(dir)
	if err != nil || hasError(diagnostics) {
		t.Fatalf("Generate restored absence: err=%v diagnostics=%+v", err, diagnostics)
	}
	module.mu.Lock()
	provenance = module.targetDeclProvenance[dir][".Child"]
	module.mu.Unlock()
	if provenance.direct == nil || provenance.direct.helperName != "_gsxrenderChild" {
		t.Fatalf("restored absence provenance = %+v, want captured-absence base helper", provenance.direct)
	}
}

func TestDirectHelperRefreshPublishesNewInactiveGoMembership(t *testing.T) {
	dir, module := openTestModule(t, map[string]string{
		"page.gsx": "package p\ncomponent Child() { <span/> }\ncomponent Parent() { <Child/> }\n",
	})
	_, diagnostics, err := module.Generate(dir)
	if err != nil || hasError(diagnostics) {
		t.Fatalf("initial Generate: err=%v diagnostics=%+v", err, diagnostics)
	}
	module.mu.Lock()
	provenance := module.targetDeclProvenance[dir][".Child"]
	module.mu.Unlock()
	if provenance.direct == nil || provenance.direct.helperName != "_gsxrenderChild" {
		t.Fatalf("initial provenance = %+v", provenance.direct)
	}
	writeFile(t, dir, "inactive.go", "//go:build helpervariant\n\npackage p\nfunc _gsxrenderChild() {}\n")
	if _, err := module.RefreshDiskSourcesAndInvalidate(dir); err != nil {
		t.Fatal(err)
	}
	_, diagnostics, err = module.Generate(dir)
	if err != nil || hasError(diagnostics) {
		t.Fatalf("refreshed Generate: err=%v diagnostics=%+v", err, diagnostics)
	}
	module.mu.Lock()
	provenance = module.targetDeclProvenance[dir][".Child"]
	module.mu.Unlock()
	if provenance.direct == nil || provenance.direct.helperName != "_gsxrenderChild1" {
		t.Fatalf("refreshed provenance = %+v, want new inactive collision suffix", provenance.direct)
	}
}

func TestDirectComponentAlphaRenamedVariantsCompileBothSelections(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns the go toolchain")
	}
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module directvariants\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	writeFile(t, dir, "child_default.gsx", "//go:build !alternate\n\npackage p\ncomponent Child[T any](value T) { <span>default</span> }\n")
	writeFile(t, dir, "child_alternate.gsx", "//go:build alternate\n\npackage p\ncomponent Child[U any](value U) { <span>alternate</span> }\n")
	writeFile(t, dir, "parent.gsx", "package p\ncomponent Parent() { <Child[string] value=\"x\"/> }\n")
	result, err := GenerateDirs(dir, []string{dir}, Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	generated := result[dir]
	if hasError(generated.Diags) {
		t.Fatalf("generation diagnostics = %+v", generated.Diags)
	}
	for sourcePath, output := range generated.Files {
		outputPath := strings.TrimSuffix(sourcePath, ".gsx") + ".x.go"
		if err := os.WriteFile(outputPath, output, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "default", args: []string{"test", "./..."}},
		{name: "alternate", args: []string{"test", "-tags", "alternate", "./..."}},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := exec.Command("go", test.args...)
			command.Dir = dir
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("go %v: %v\n%s", test.args, err, output)
			}
		})
	}
}
