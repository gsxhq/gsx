package lsp

import (
	"go/types"
	"path/filepath"
	"strings"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/codegen"
)

// TestAttrsOnlyTagHover mirrors TestAttrsOnlyTagDeclAtSamePackage
// (definition_attrsonly_test.go) fixture-for-fixture: HomeIcon is a
// package-level var (initialized from a factory func) whose type is
// func(gsx.Attrs) gsx.Node, declared in a sibling .go file — never a
// `component` declaration, so componentAtTag (hover's existing tag path,
// backed by the same CrossIndex componentTagDeclAt uses for go-to-def) misses
// it entirely. A cursor on <HomeIcon/> must hover the var's own
// declaration/type, resolved through the SAME seam go-to-def uses
// (attrsOnlyTagObject + isAttrsOnlyValueType in definition_attrsonly.go), not
// a new lookup.
func TestAttrsOnlyTagHover(t *testing.T) {
	root := t.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	writeLSPTestFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	viewsDir := filepath.Join(root, "views")
	pageSrc := "package views\n\n" +
		"import \"github.com/gsxhq/gsx\"\n\n" +
		"type iconProps struct {\n\tName  string\n\tAttrs gsx.Attrs\n}\n\n" +
		"component renderIcon(p iconProps) {\n\t<svg { gsx.Attrs{{Key: \"class\", Value: \"w-5 h-5\"}}.Merge(p.Attrs)... }>{p.Name}</svg>\n}\n\n" +
		"component Page() {\n\t<div>\n\t\t<HomeIcon class=\"h-3 w-3\"/>\n\t</div>\n}\n"
	writeLSPTestFile(t, viewsDir, "page.gsx", pageSrc)
	iconsSrc := "package views\n\n" +
		"import \"github.com/gsxhq/gsx\"\n\n" +
		"func namedIcon(name string) func(gsx.Attrs) gsx.Node {\n" +
		"\treturn func(attrs gsx.Attrs) gsx.Node {\n" +
		"\t\treturn renderIcon(iconProps{Name: name, Attrs: attrs})\n\t}\n}\n\n" +
		"var HomeIcon = namedIcon(\"house\")\n"
	writeLSPTestFile(t, viewsDir, "icons.go", iconsSrc)

	m, err := codegen.Open(codegen.Options{ModuleRoot: root, ModulePath: "example.com/app", FilterPkgs: []string{codegen.StdImportPath}})
	if err != nil {
		t.Fatal(err)
	}
	pr, err := m.Package(viewsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(pr.Diags) > 0 {
		t.Fatalf("unexpected diagnostics: %+v", pr.Diags)
	}
	pkg := &Package{
		GSXFset: pr.GSXFset,
		Fset:    pr.Fset,
		Info:    pr.Info,
		Types:   pr.Types,
		Files:   pr.GSXFiles,
		ExprMap: pr.ExprMap,
	}
	gsxPath := filepath.Join(viewsDir, "page.gsx")
	tagStart := strings.Index(pageSrc, "<HomeIcon") + 1 // +1 to skip '<'
	if tagStart < 1 {
		t.Fatal("could not find <HomeIcon in src")
	}

	// Sanity: componentAtTag (hover's existing component-tag path) must miss
	// HomeIcon — it's a var, not a `component` decl — proving this really
	// needs the new attrs-only hover branch, not the existing one.
	if _, _, _, ok := componentAtTag(pkg, gsxPath, tagStart); ok {
		t.Fatal("componentAtTag unexpectedly resolved an attrs-only tag")
	}

	obj, nameStart, nameLen, ok := attrsOnlyTagAt(pkg, gsxPath, tagStart)
	if !ok {
		t.Fatalf("attrsOnlyTagAt returned false for cursor on 'H' of HomeIcon tag")
	}
	if obj.Name() != "HomeIcon" {
		t.Errorf("hover object name = %q, want HomeIcon", obj.Name())
	}
	if got := pageSrc[nameStart : nameStart+nameLen]; got != "HomeIcon" {
		t.Errorf("hover range = %q, want HomeIcon", got)
	}
	sig := types.ObjectString(obj, qualifierFor(pkg))
	if sig == "" {
		t.Fatal("hover signature is empty")
	}
	if !strings.Contains(sig, "HomeIcon") || !strings.Contains(sig, "gsx.Attrs") || !strings.Contains(sig, "gsx.Node") {
		t.Errorf("hover signature = %q, want it to name HomeIcon's func(gsx.Attrs) gsx.Node type", sig)
	}
}

func TestRenderComponentSig(t *testing.T) {
	fn := &gsxast.Component{Name: "Card", Params: "title string"}
	if got, want := renderComponentSig(fn), "component Card(title string)"; got != want {
		t.Errorf("func component: got %q, want %q", got, want)
	}
	method := &gsxast.Component{Recv: "(p UsersPage)", Name: "Row", Params: "u User"}
	if got, want := renderComponentSig(method), "component (p UsersPage) Row(u User)"; got != want {
		t.Errorf("method component: got %q, want %q", got, want)
	}
	nullary := &gsxast.Component{Name: "Page"}
	if got, want := renderComponentSig(nullary), "component Page()"; got != want {
		t.Errorf("nullary component: got %q, want %q", got, want)
	}
}
