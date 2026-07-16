package codegen

import (
	"path/filepath"
	"testing"
)

func TestPackagePublishesExactComponentCallFacts(t *testing.T) {
	dir, module := openTestModule(t, map[string]string{
		"page.gsx": `package views

import "github.com/gsxhq/gsx"

component Card(title string, someAttrs gsx.Attrs, attrs gsx.Attrs) {
	<div/>
}

component Page() {
	<Card title="ok" someAttrs={{"id": "ordinary"}} attrs={{"class": "reserved"}}/>
}
`,
	})

	result, err := module.Package(dir)
	if err != nil {
		t.Fatal(err)
	}
	if hasDiagErrors(result.Diags) {
		t.Fatalf("unexpected diagnostics: %v", result.Diags)
	}
	if len(result.ComponentCalls) != 1 {
		t.Fatalf("component call facts = %d, want 1", len(result.ComponentCalls))
	}

	var call ComponentCallFact
	for _, candidate := range result.ComponentCalls {
		call = candidate
	}
	if call.Target == nil || call.Target.Name() != "Card" {
		t.Fatalf("target = %v, want Card object", call.Target)
	}
	if call.TargetPackage != "testmod" || call.TargetKey != ".Card" {
		t.Fatalf("target identity = (%q, %q), want (testmod, .Card)", call.TargetPackage, call.TargetKey)
	}
	if call.Signature == nil || call.Signature.Params().Len() != 3 {
		t.Fatalf("signature = %v, want three params", call.Signature)
	}
	if len(call.Params) != 3 {
		t.Fatalf("bound param facts = %d, want 3", len(call.Params))
	}

	want := map[string]struct {
		param string
		role  ComponentParamRole
	}{
		"title":     {param: "title", role: ComponentParamOrdinary},
		"someAttrs": {param: "someAttrs", role: ComponentParamOrdinary},
		"attrs":     {param: "attrs", role: ComponentParamAttrs},
	}
	for attr, param := range call.Params {
		name, ok := componentInputAttrName(attr)
		if !ok {
			t.Fatalf("published bound param for unnamed attr %T", attr)
		}
		expect, ok := want[name]
		if !ok {
			t.Fatalf("unexpected bound attr %q", name)
		}
		if param.Name != expect.param || param.Role != expect.role {
			t.Errorf("%s fact = {Name:%q Role:%v}, want {%q %v}", name, param.Name, param.Role, expect.param, expect.role)
		}
		if param.Var == nil || param.Origin == nil || param.Ordinal < 0 {
			t.Errorf("%s fact lacks semantic identity: %+v", name, param)
		}
		pos := result.Fset.Position(param.Origin.Pos())
		if filepath.Base(pos.Filename) != "page.gsx" {
			t.Errorf("%s origin position = %v, want page.gsx", name, pos)
		}
	}
}

func TestPackagePublishesExactComponentParameterFamilies(t *testing.T) {
	dir, module := openTestModule(t, map[string]string{
		"icon_a.gsx": `//go:build !never

package views

component Icon[T ~string](value T) { <span>{value}</span> }
`,
		"icon_b.gsx": `//go:build never

package views

component Icon[U ~string](value U) { <strong>{value}</strong> }
`,
		"page.gsx": `package views

component Page() { <Icon value="ok"/> }
`,
	})

	result, err := module.Package(dir)
	if err != nil {
		t.Fatal(err)
	}
	if hasDiagErrors(result.Diags) {
		t.Fatalf("unexpected diagnostics: %v", result.Diags)
	}
	if len(result.ComponentParamDecls) != 1 {
		t.Fatalf("component parameter declarations = %+v, want one logical parameter", result.ComponentParamDecls)
	}
	decl := result.ComponentParamDecls[0]
	if decl.PackagePath != "testmod" || decl.ComponentKey != ".Icon" || decl.Ordinal != 0 {
		t.Fatalf("declaration key = (%q, %q, %d), want (testmod, .Icon, 0)", decl.PackagePath, decl.ComponentKey, decl.Ordinal)
	}
	if decl.Name != "value" || decl.Role != ComponentParamOrdinary || decl.Origin == nil {
		t.Fatalf("declaration identity = %+v, want ordinary value with origin", decl)
	}
	if len(decl.Decls) != 2 {
		t.Fatalf("variant declaration positions = %+v, want both variants", decl.Decls)
	}
	for _, pos := range decl.Decls {
		if filepath.Base(pos.Filename) != "icon_a.gsx" && filepath.Base(pos.Filename) != "icon_b.gsx" {
			t.Fatalf("unexpected declaration position: %+v", pos)
		}
	}

	if len(result.ComponentParamRefs) != 3 {
		t.Fatalf("component parameter refs = %+v, want both variant body uses and the invocation attr", result.ComponentParamRefs)
	}
	refFiles := map[string]int{}
	for _, ref := range result.ComponentParamRefs {
		if ref.PackagePath != decl.PackagePath || ref.ComponentKey != decl.ComponentKey || ref.Ordinal != decl.Ordinal || ref.Name != decl.Name {
			t.Fatalf("reference key = %+v, want declaration key %+v", ref, decl)
		}
		if ref.Origin == nil || ref.Origin != decl.Origin {
			t.Fatalf("reference origin = %p, declaration origin = %p; want generic origin normalization", ref.Origin, decl.Origin)
		}
		refFiles[filepath.Base(ref.Ref.Filename)]++
	}
	for _, filename := range []string{"icon_a.gsx", "icon_b.gsx", "page.gsx"} {
		if refFiles[filename] != 1 {
			t.Fatalf("reference files = %v, want one exact ref in %s", refFiles, filename)
		}
	}
}

func TestPackagePublishesSemanticComponentParameterBodyRefs(t *testing.T) {
	dir, module := openTestModule(t, map[string]string{
		"card.gsx": `package views

component Card(title string, items []string, limit int) {
	<div data-title={title}>
		{{ copied := title }}
		{ if title != "" { <p>{copied}</p> } }
		<ul>{ for _, title := range items { <li>{title}</li> } }</ul>
		<p>{ title |> truncate(limit) }</p>
	</div>
}
`,
	})
	result, err := module.Package(dir)
	if err != nil {
		t.Fatal(err)
	}
	if hasDiagErrors(result.Diags) {
		t.Fatalf("unexpected diagnostics: %v", result.Diags)
	}
	counts := map[string]int{}
	for _, ref := range result.ComponentParamRefs {
		counts[ref.Name]++
		if filepath.Base(ref.Ref.Filename) != "card.gsx" {
			t.Fatalf("body ref position = %+v, want card.gsx", ref.Ref)
		}
	}
	if counts["title"] != 4 || counts["items"] != 1 || counts["limit"] != 1 {
		t.Fatalf("semantic body refs = %v, want title=4, items=1, limit=1; loop-local title must be excluded", counts)
	}
}
