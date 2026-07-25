package ast_test

import (
	goast "go/ast"
	goparser "go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// This file is the CHILDREN-BEARING-NODE exhaustiveness gate: a repo-wide
// static check that no markup walk can silently forget a container node type.
//
// It exists because *MarkerRegion (the `<?start …> … <?end>` region) was added
// as a third children-bearing markup container alongside *Element and
// *Fragment, and was then wired into only about a third of the walks that
// recurse into a children list. Nine walks silently skipped a region's
// children, producing bugs ranging from "a component inside a region is emitted
// as raw HTML" to "gsx fmt changes rendered output". Every one of them was a
// type switch that had a `case *ast.Fragment:` and no `case *ast.MarkerRegion:`.
// A missing case in a Go type switch is invisible — no compiler error, no
// panic, just a node that falls through untouched. This gate makes it visible.
//
// THE RULE
//
//	A type switch over gsx markup that names ANY anonymous children container
//	must name EVERY anonymous children container.
//
// Both halves of the rule are DISCOVERED from ast.go's own source, so adding a
// fourth container type (or a new sealed-family member) updates this gate with
// no list to maintain:
//
//   - an ANONYMOUS CHILDREN CONTAINER is a markup-family member (a type
//     declaring markupNode(), the exact mechanism ast.go itself uses to seal the
//     family — see clone_exhaustive_test.go's discoverFamilyMembers) that
//     declares a `Children []Markup` field and NO `Tag` field: a pure wrapper
//     whose only structural content is its children. Today that is *Fragment
//     and *MarkerRegion. *Element is a children container too, but it is NOT
//     anonymous — it is a tag-addressable, first-class node that appears in
//     switches for many reasons other than reaching its children (it is the
//     component-call carrier, a Go-expression value, the import-qualifier
//     harvest subject). A `case *ast.Fragment:` in a markup switch, by
//     contrast, exists for exactly ONE reason: to reach the children of a
//     wrapper that has no other meaning. So Fragment's presence is what makes
//     the other anonymous containers mandatory, and Element's is not.
//
//   - "over gsx markup" excludes the Go-expression-value switches
//     (`switch part := part.(type)` over a GoWithElements/Interp/GoBlock part
//     list), which legitimately handle *Element and *Fragment without
//     *MarkerRegion because a MarkerRegion is not a GoPart at all — a PI in
//     Go-expression position is rejected outright (corpus
//     pi/e_element_literal_rejected). A switch is excluded when it names a
//     GoPart-ONLY type (today `GoText`, membership discovered the same way), or
//     when its enclosing function is listed in goPartSwitchFunctions below —
//     the escape hatch for switches whose every case type is a member of BOTH
//     families, where nothing in the source settles which list is walked. A
//     switch that names no anonymous container at all (e.g. one dispatching
//     Text/Interp segments of an embedded body) is out of scope and skipped.
//
// Verified against the bug it exists to prevent: reverting all nine walk fixes
// makes this test report all ELEVEN gap sites, with no false positives.
//
// The gsx-vs-go/ast qualifier is resolved per file from the import list, so a
// `case *ast.Comment:` over go/ast in some other package is never mistaken for
// the gsx node of the same name.

// gsxASTImportPath is the gsx AST package this gate reasons about.
const gsxASTImportPath = "github.com/gsxhq/gsx/ast"

// discoverAnonymousContainers returns the markup-family type names that carry a
// `Children []Markup` field and NO `Tag` field — the anonymous children
// containers every markup walk must handle together. See this file's
// top-of-file doc for why the `Tag`-bearing container (*Element) is excluded.
// Discovered from ast.go, so a new container needs no list edit.
func discoverAnonymousContainers(t *testing.T, astGo *goast.File, markupMembers []familyMember) []string {
	t.Helper()
	isMarkup := map[string]bool{}
	for _, m := range markupMembers {
		isMarkup[m.typeName] = true
	}
	var out []string
	for _, decl := range astGo.Decls {
		gd, ok := decl.(*goast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*goast.TypeSpec)
			if !ok || !isMarkup[ts.Name.Name] {
				continue
			}
			st, ok := ts.Type.(*goast.StructType)
			if !ok || st.Fields == nil {
				continue
			}
			hasChildren, hasTag := false, false
			for _, field := range st.Fields.List {
				named := func(want string) bool {
					return slices.ContainsFunc(field.Names, func(n *goast.Ident) bool { return n.Name == want })
				}
				if named("Tag") {
					hasTag = true
				}
				arr, ok := field.Type.(*goast.ArrayType)
				if !ok || arr.Len != nil {
					continue
				}
				if elem, ok := arr.Elt.(*goast.Ident); ok && elem.Name == "Markup" && named("Children") {
					hasChildren = true
				}
			}
			if hasChildren && !hasTag {
				out = append(out, ts.Name.Name)
			}
		}
	}
	sort.Strings(out)
	return out
}

// gsxASTQualifiers returns the identifiers under which f refers to the gsx AST
// package: the import alias (or "ast", its default package name), plus "" for a
// file inside package ast itself, where the types are unqualified. Returning an
// empty set means f cannot name a gsx AST type, so it is skipped entirely.
func gsxASTQualifiers(f *goast.File, inASTPackage bool) map[string]bool {
	out := map[string]bool{}
	if inASTPackage {
		out[""] = true
	}
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != gsxASTImportPath {
			continue
		}
		if imp.Name != nil {
			if imp.Name.Name != "_" && imp.Name.Name != "." {
				out[imp.Name.Name] = true
			}
			continue
		}
		out["ast"] = true
	}
	return out
}

// switchGSXTypes returns the gsx AST type names named by the case clauses of
// one type switch, resolving each case expression's package qualifier against
// quals. A case naming something that is not a gsx AST type (`nil`, a go/ast
// type, an interface) contributes nothing.
func switchGSXTypes(sw *goast.TypeSwitchStmt, quals map[string]bool) []string {
	seen := map[string]bool{}
	for _, stmt := range sw.Body.List {
		cc, ok := stmt.(*goast.CaseClause)
		if !ok {
			continue
		}
		for _, expr := range cc.List {
			// A pointer-receiver node is named as *T; a value-kind one (GoText)
			// bare. Both forms reduce to the same type name.
			if star, ok := expr.(*goast.StarExpr); ok {
				expr = star.X
			}
			switch e := expr.(type) {
			case *goast.Ident:
				if quals[""] {
					seen[e.Name] = true
				}
			case *goast.SelectorExpr:
				pkg, ok := e.X.(*goast.Ident)
				if ok && quals[pkg.Name] {
					seen[e.Sel.Name] = true
				}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// goPartSwitchFunctions registers the Go-expression-part switches this gate
// cannot classify automatically: those whose EVERY case type is a member of
// both the Markup and GoPart families (*Element, *Fragment, *EmbeddedInterp),
// so neither an anonymous-container case nor a GoPart-only `GoText` case
// settles which list is being walked. A MarkerRegion is not a GoPart — a
// processing instruction in Go-expression position is rejected outright (corpus
// pi/e_element_literal_rejected) — so these switches are correct as written.
//
// This is the one hand-maintained list in this file, the same sanctioned
// fallback clone_exhaustive_test.go's astZeroValueTypes uses, and it fails
// loudly both ways: a NEW ambiguous switch is reported as a missing case until
// it is either fixed or consciously registered here, and a stale entry is
// reported as stale. Keys are "<module-relative file>:<enclosing func name>".
var goPartSwitchFunctions = map[string]string{
	"internal/codegen/analyze.go:firstDirectGoBlockMarkup":     "classifies a {{ }} block's GoPart split for the unsupported-markup annotation",
	"internal/codegen/parenstrip.go:parenWrappable":            "asks whether a GoWithElements part is a paren-wrappable gsx value",
	"internal/codegen/rebase.go:rebaseGoParts":                 "re-bases the embedded literals of a GoBlock/Interp GoPart split",
	"internal/codegen/tagresolve.go:reconstructGoWithElements": "validates the GoPart kinds of a GoWithElements reconstruction",
	"internal/cssmin/file.go:minifyGoParts":                    "minifies the css` literals of a GoBlock/Interp GoPart split",
	"internal/jsmin/file.go:minifyGoParts":                     "minifies the js` literals of a GoBlock/Interp GoPart split",
	"internal/jsx/jsx.go:resolveGoParts":                       "classifies the js` literals of a GoBlock/Interp GoPart split",
	"internal/printer/printer.go:goExprValue":                  "builds the doc for one non-GoText GoWithElements part",
	"internal/printer/printer.go:goWithElements":               "decides which GoWithElements parts are paren-strip eligible",
	"internal/wsnorm/wsnorm.go:Normalize":                      "normalizes the element/fragment parts of a GoWithElements decl",
	"parser/goexpr.go:markupKind":                              "names a parsed markup for the not-a-Go-expression-value error",
	"parser/goexpr.go:splitGoElements":                         "admits *Element/*Fragment as top-level Go-expression values, rejecting all other markup",
	"parser/goexpr.go:SplitGoExprElements":                     "admits *Element/*Fragment as embedded Go-expression values, rejecting all other markup",
}

// enclosingFuncName returns the name of the FuncDecl whose body contains pos
// ("" when pos is not inside one). Used to key goPartSwitchFunctions by
// function name rather than line number, so ordinary edits above a registered
// switch do not invalidate its entry.
func enclosingFuncName(f *goast.File, pos token.Pos) string {
	for _, decl := range f.Decls {
		fd, ok := decl.(*goast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		if pos >= fd.Body.Pos() && pos < fd.Body.End() {
			return fd.Name.Name
		}
	}
	return ""
}

// TestEveryMarkupWalkHandlesEveryContainer is the gate. See this file's
// top-of-file doc for the rule and why its discriminator is sound.
func TestEveryMarkupWalkHandlesEveryContainer(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed; cannot locate the module root")
	}
	astDir := filepath.Dir(thisFile)
	moduleRoot := filepath.Dir(astDir)

	fset := token.NewFileSet()
	astGo, err := goparser.ParseFile(fset, filepath.Join(astDir, "ast.go"), nil, 0)
	if err != nil {
		t.Fatalf("parsing ast.go: %v", err)
	}
	families := discoverFamilyMembers(t)
	containers := discoverAnonymousContainers(t, astGo, families["Markup"])
	if len(containers) < 2 {
		t.Fatalf("discovered %d anonymous children containers (%v); the ast.go scan is broken", len(containers), containers)
	}
	isGoPart := map[string]bool{}
	for _, m := range families["GoPart"] {
		isGoPart[m.typeName] = true
	}
	isMarkup := map[string]bool{}
	for _, m := range families["Markup"] {
		isMarkup[m.typeName] = true
	}
	exercised := map[string]bool{}
	isContainer := map[string]bool{}
	for _, c := range containers {
		isContainer[c] = true
	}
	t.Logf("anonymous children containers discovered from ast.go: %v", containers)

	files := goSourceFiles(t, moduleRoot)
	if len(files) < 50 {
		t.Fatalf("walked only %d Go files under %s; the tree walk is broken", len(files), moduleRoot)
	}
	checked := 0
	for _, path := range files {
		f, err := goparser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		rel, relErr := filepath.Rel(moduleRoot, path)
		if relErr != nil {
			rel = path
		}
		quals := gsxASTQualifiers(f, filepath.Dir(path) == astDir)
		if len(quals) == 0 {
			continue
		}
		goast.Inspect(f, func(n goast.Node) bool {
			sw, ok := n.(*goast.TypeSwitchStmt)
			if !ok {
				return true
			}
			named := switchGSXTypes(sw, quals)
			// Out of scope: names no anonymous children container at all.
			var namedContainers []string
			for _, name := range named {
				if isContainer[name] {
					namedContainers = append(namedContainers, name)
				}
			}
			if len(namedContainers) == 0 {
				return true
			}
			// Out of scope: a Go-expression-value switch, where a MarkerRegion
			// cannot appear (see the top-of-file doc). Recognised either by an
			// unambiguous GoPart-only case (GoText), or by an explicit registry
			// entry for switches whose every case type belongs to BOTH families.
			if slices.ContainsFunc(named, func(n string) bool { return isGoPart[n] && !isMarkup[n] }) {
				return true
			}
			if fn := enclosingFuncName(f, sw.Pos()); fn != "" {
				if _, exempt := goPartSwitchFunctions[rel+":"+fn]; exempt {
					exercised[rel+":"+fn] = true
					return true
				}
			}
			checked++
			var missing []string
			for _, c := range containers {
				if !slices.Contains(named, c) {
					missing = append(missing, c)
				}
			}
			if len(missing) > 0 {
				t.Errorf("%s:%d: this markup type switch handles %v but has no case for %v.\n"+
					"Anonymous children containers (%v) must travel together: they are pure wrappers, so a walk that reaches "+
					"one wrapper's children must reach them all — and a missing case in a Go type switch makes the node fall "+
					"through UNTOUCHED, with no compiler error and no panic.\n"+
					"Add the case (recursing into .Children like the *%s case beside it), or, if this switch really is over "+
					"Go-expression parts rather than markup, it must name only GoPart-family types.",
					rel, fset.Position(sw.Pos()).Line, namedContainers, missing, containers, namedContainers[0])
			}
			return true
		})
	}
	if checked == 0 {
		t.Fatal("classified zero markup type switches; the discriminator is broken")
	}
	// A stale entry means the switch it described moved or was deleted; the
	// registry must not accumulate exemptions for code that no longer exists.
	for key := range goPartSwitchFunctions {
		if !exercised[key] {
			t.Errorf("goPartSwitchFunctions has a stale entry %q: no ambiguous type switch was found in that function. Remove it (or fix the key) — an exemption for code that no longer exists silently widens this gate.", key)
		}
	}
	t.Logf("checked %d markup type switches across %d Go files", checked, len(files))
}

// goSourceFiles returns every non-test .go file under root that belongs to THIS
// module: nested modules (playground/server, examples/*) and testdata trees are
// skipped, as are generated .x.go files (they contain no markup walks).
func goSourceFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "testdata", ".claude", ".superpowers":
				return fs.SkipDir
			}
			if path != root {
				if _, statErr := os.Stat(filepath.Join(path, "go.mod")); statErr == nil {
					return fs.SkipDir // a nested module
				}
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, ".x.go") {
			return nil
		}
		out = append(out, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	sort.Strings(out)
	return out
}
