package codegen

import "testing"

// TestCrossIndexMultiValuedVariants covers Task 6: the component cross-index
// must retain EVERY build-tag variant's declaration position (not collapse to
// one), so the LSP (Tasks 7-8) can show all variants on go-to-definition. Two
// same-signature Icon components under disjoint //go:build tags must produce
// a single CrossIndex entry whose Decls holds both positions, with Decl kept
// as the primary (first, sorted) for back-compat.
func TestCrossIndexMultiValuedVariants(t *testing.T) {
	dir, m := openTestModule(t, map[string]string{
		"icon_a.gsx": "//go:build !never\n\npackage views\n\ncomponent Icon(name string) { <a>{ name }</a> }\n",
		"icon_b.gsx": "//go:build never\n\npackage views\n\ncomponent Icon(name string) { <b>{ name }</b> }\n",
	})
	pkg, err := m.Package(dir) // retained analysis path used by the LSP
	if err != nil {
		t.Fatalf("Package: %v", err)
	}
	cr, ok := pkg.CrossIndex[".Icon"]
	if !ok {
		t.Fatal("no CrossIndex entry for .Icon")
	}
	if len(cr.Decls) != 2 {
		t.Fatalf("want 2 variant decls, got %d (%v)", len(cr.Decls), cr.Decls)
	}
	if !cr.Decl.IsValid() {
		t.Fatal("primary Decl must stay valid for back-compat")
	}
}
