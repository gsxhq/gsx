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
	if call.TargetPackage != "example.com/test" || call.TargetKey != ".Card" {
		t.Fatalf("target identity = (%q, %q), want (example.com/test, .Card)", call.TargetPackage, call.TargetKey)
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
