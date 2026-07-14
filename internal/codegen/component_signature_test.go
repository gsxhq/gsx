package codegen

import (
	"strings"
	"testing"
)

func TestParseComponentParamDecls(t *testing.T) {
	assertDeclParams := func(src string, wantNames, wantTypes []string, wantVariadic []bool) []componentParamDecl {
		t.Helper()
		got, err := parseComponentParamDecls(src)
		if err != nil {
			t.Fatalf("parseComponentParamDecls(%q): %v", src, err)
		}
		if len(got) != len(wantNames) {
			t.Fatalf("parseComponentParamDecls(%q) = %d params, want %d", src, len(got), len(wantNames))
		}
		for i, p := range got {
			if p.name != wantNames[i] || p.normalizedType != wantTypes[i] || p.variadic != wantVariadic[i] {
				t.Errorf("param %d of %q = {name:%q normalizedType:%q variadic:%t}, want {%q %q %t}",
					i, src, p.name, p.normalizedType, p.variadic, wantNames[i], wantTypes[i], wantVariadic[i])
			}
			trimmed := strings.TrimSpace(src)
			if p.typeOff < 0 || p.typeLen != len(p.typeSrc) || p.typeOff+p.typeLen > len(trimmed) {
				t.Fatalf("param %d of %q has invalid type span [%d,%d)", i, src, p.typeOff, p.typeOff+p.typeLen)
			}
			if gotType := trimmed[p.typeOff : p.typeOff+p.typeLen]; gotType != p.typeSrc {
				t.Errorf("param %d of %q type span = %q, want typeSrc %q", i, src, gotType, p.typeSrc)
			}
			if p.name != "" {
				if p.nameOff < 0 || p.nameOff+len(p.name) > len(trimmed) || trimmed[p.nameOff:p.nameOff+len(p.name)] != p.name {
					t.Errorf("param %d of %q has invalid name span at %d", i, src, p.nameOff)
				}
			}
		}
		return got
	}

	assertDeclParams(
		"a, b string, _ bool, rest ...byte",
		[]string{"a", "b", "_", "rest"},
		[]string{"string", "string", "bool", "...byte"},
		[]bool{false, false, false, true},
	)
	unnamed := assertDeclParams(
		"string, bool, ...byte",
		[]string{"", "", ""},
		[]string{"string", "bool", "...byte"},
		[]bool{false, false, true},
	)
	for i, p := range unnamed {
		if p.nameOff != -1 {
			t.Fatalf("unnamed param %d nameOff=%d, want -1", i, p.nameOff)
		}
	}

	roles, err := parseComponentParamDecls("children gsx.Node, value string, attrs ...gsx.Attr")
	if err != nil {
		t.Fatal(err)
	}
	wantRoles := []declarationParamRole{declarationParamChildren, declarationParamOrdinary, declarationParamAttrs}
	for i, p := range roles {
		if p.role != wantRoles[i] {
			t.Errorf("param %q role=%d, want %d", p.name, p.role, wantRoles[i])
		}
	}
}

func TestComponentDeclarationCanonical(t *testing.T) {
	a := mustParseComponent(t, "package v\ncomponent C(a, b string, attrs ...gsx.Attr) { <i/> }\n")
	b := mustParseComponent(t, "package v\ncomponent C(a string, b string, attrs ...gsx.Attr) { <b/> }\n")
	c := mustParseComponent(t, "package v\ncomponent C(b string, a string, attrs ...gsx.Attr) { <i/> }\n")
	d := mustParseComponent(t, "package v\ncomponent C(a string, b string, attrs []gsx.Attr) { <i/> }\n")
	sa, err := componentDeclarationFor(a)
	if err != nil {
		t.Fatal(err)
	}
	sb, err := componentDeclarationFor(b)
	if err != nil {
		t.Fatal(err)
	}
	sc, err := componentDeclarationFor(c)
	if err != nil {
		t.Fatal(err)
	}
	sd, err := componentDeclarationFor(d)
	if err != nil {
		t.Fatal(err)
	}
	if sa.canonical() != sb.canonical() {
		t.Fatal("grouped and ungrouped logical parameters must match")
	}
	if sa.canonical() == sc.canonical() {
		t.Fatal("parameter reorder must change the contract")
	}
	if sa.canonical() == sd.canonical() {
		t.Fatal("variadic position must change the contract")
	}
}

func TestComponentDeclarationRenameChangesContract(t *testing.T) {
	a := mustParseComponent(t, "package v\ncomponent C(value string) { <i/> }\n")
	b := mustParseComponent(t, "package v\ncomponent C(label string) { <i/> }\n")
	sa, err := componentDeclarationFor(a)
	if err != nil {
		t.Fatal(err)
	}
	sb, err := componentDeclarationFor(b)
	if err != nil {
		t.Fatal(err)
	}
	if sa.canonical() == sb.canonical() {
		t.Fatal("parameter name is part of the markup contract")
	}
}

func TestComponentDeclarationCanonicalIsCollisionSafe(t *testing.T) {
	a := componentDeclaration{params: []componentParamDecl{{name: "a", normalizedType: "bc"}}}
	b := componentDeclaration{params: []componentParamDecl{{name: "ab", normalizedType: "c"}}}
	if a.canonical() == b.canonical() {
		t.Fatal("length-prefixed fields must distinguish ambiguous concatenations")
	}
	ordinary := componentDeclaration{params: []componentParamDecl{{name: "attrs", normalizedType: "gsx.Attrs", role: declarationParamOrdinary}}}
	attrs := componentDeclaration{params: []componentParamDecl{{name: "attrs", normalizedType: "gsx.Attrs", role: declarationParamAttrs}}}
	if ordinary.canonical() == attrs.canonical() {
		t.Fatal("reserved role must be encoded in the declaration contract")
	}
}
