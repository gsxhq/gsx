package lsp

import (
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"
)

// buildSyntheticPackage type-checks a plain Go source (no gsx involved) into an
// lsp.Package carrying Types, Info (with Scopes), and Fset — exactly the shape
// the scope-walk helpers consume. It returns the package and the *token.File so
// tests can turn byte offsets into token.Pos.
func buildSyntheticPackage(t *testing.T, src string) (*Package, *token.File) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	info := &types.Info{
		Types:  map[ast.Expr]types.TypeAndValue{},
		Defs:   map[*ast.Ident]types.Object{},
		Uses:   map[*ast.Ident]types.Object{},
		Scopes: map[ast.Node]*types.Scope{},
	}
	// Tolerate type errors (unresolved names in mid-edit fixtures) exactly as
	// the production skeleton typecheck does: go/types fills Info best-effort.
	conf := types.Config{Importer: importer.Default(), Error: func(error) {}}
	tpkg, _ := conf.Check("p", fset, []*ast.File{file}, info)
	pkg := &Package{Types: tpkg, Info: info, Fset: fset}
	return pkg, fset.File(file.Pos())
}

func TestScopeCandidates(t *testing.T) {
	src := `package p

import "strings"

var global = 1

func f(param int) {
	local := 2
	_ = local
	after := 3
	_ = after
}
`
	pkg, tf := buildSyntheticPackage(t, src)

	// POS: inside f's body after local and param are declared but strictly
	// before the `after := 3` declaration (Go's declaration-order rule excludes
	// only objects whose Pos is strictly greater than the cursor).
	markerOff := strings.Index(src, "_ = local") + len("_ = local")
	if markerOff < len("_ = local") {
		t.Fatal("marker not found")
	}
	pos := tf.Pos(markerOff)

	scope := innermostScopeAt(pkg, pos)
	if scope == nil {
		t.Fatal("innermostScopeAt returned nil")
	}
	cands := scopeCandidates(pkg, scope, pos)

	tier := map[string]int{}
	for _, c := range cands {
		tier[c.obj.Name()] = c.tier
	}

	for _, name := range []string{"local", "param", "global", "strings", "f"} {
		if _, ok := tier[name]; !ok {
			t.Errorf("candidate %q missing", name)
		}
	}
	if _, ok := tier["after"]; ok {
		t.Errorf("candidate %q present but is declared after the cursor", "after")
	}
	// Universe entries are visible everywhere.
	for _, name := range []string{"println", "error", "true"} {
		if _, ok := tier[name]; !ok {
			t.Errorf("universe candidate %q missing", name)
		}
	}

	wantTier := map[string]int{
		"strings": tierImported,
		"global":  tierPackage,
		"f":       tierPackage,
		"local":   tierLocal,
		"param":   tierLocal,
		"println": tierUniverse,
		"error":   tierUniverse,
		"true":    tierUniverse,
	}
	for name, want := range wantTier {
		if got := tier[name]; got != want {
			t.Errorf("tier[%q] = %d, want %d", name, got, want)
		}
	}
}

// TestScopeCandidatesShadowing verifies an inner declaration shadows an outer
// name of the same spelling: only the innermost binding is offered.
func TestScopeCandidatesShadowing(t *testing.T) {
	src := `package p

var x = 1

func g() {
	x := "inner"
	_ = x
	println(x)
}
`
	pkg, tf := buildSyntheticPackage(t, src)
	markerOff := strings.Index(src, "println(x)")
	pos := tf.Pos(markerOff)
	scope := innermostScopeAt(pkg, pos)
	cands := scopeCandidates(pkg, scope, pos)

	var xObjs []types.Object
	for _, c := range cands {
		if c.obj.Name() == "x" {
			xObjs = append(xObjs, c.obj)
		}
	}
	if len(xObjs) != 1 {
		t.Fatalf("expected exactly one x candidate, got %d", len(xObjs))
	}
	// The winner is the inner (local) x, not the package var.
	if _, isVar := xObjs[0].(*types.Var); !isVar {
		t.Fatalf("shadowed x is %T, want *types.Var (local)", xObjs[0])
	}
	if xObjs[0].Parent() == pkg.Types.Scope() {
		t.Fatal("shadowed x resolved to the package-scope var, want the local")
	}
}

// TestInnermostScopeAtAuthoredBetweenDecls pins that a cursor sitting BETWEEN
// top-level declarations (not inside any func body) resolves to the file scope,
// so imported package names, package-scope decls, and keywords all complete
// there. The //line directive reproduces the production geometry a real GoChunk
// has: the file scope's package clause stays on the skeleton path ("p.go")
// while the GoChunk-derived decl maps back to the authored .gsx — so the file
// scope's own span is filtered out of the direct match and only the
// fileScopeForAuthoredPath fallback can recover it.
func TestInnermostScopeAtAuthoredBetweenDecls(t *testing.T) {
	src := `package p

import "strings"

//line home.gsx:5:1
func helper() string { return "a" }
`
	pkg, _ := buildSyntheticPackage(t, src)

	// off is an authored-coordinate position that lies in no func/block span
	// (before helper's mapped body), forcing the between-decls fallback.
	scope := innermostScopeAtAuthored(pkg, "home.gsx", 1)
	if scope == nil {
		t.Fatal("innermostScopeAtAuthored returned nil")
	}
	// It must be the file scope, not the package scope: fileScopeSet recognizes
	// it, so imported names earn tierImported.
	if !fileScopeSet(pkg)[scope] {
		t.Fatalf("scope between decls is not the file scope (got package scope? %v)", scope == pkg.Types.Scope())
	}

	tier := map[string]int{}
	for _, c := range scopeCandidates(pkg, scope, token.NoPos) {
		tier[c.obj.Name()] = c.tier
	}
	if got, ok := tier["strings"]; !ok || got != tierImported {
		t.Errorf("imported name `strings` tier = %d (present=%v), want tierImported (%d)", got, ok, tierImported)
	}
	if got, ok := tier["helper"]; !ok || got != tierPackage {
		t.Errorf("package decl `helper` tier = %d (present=%v), want tierPackage (%d)", got, ok, tierPackage)
	}
	// Universe names remain visible (keywords are added by goCompletionItems, not
	// scopeCandidates, and are exercised in the e2e test).
	if _, ok := tier["error"]; !ok {
		t.Error("universe name `error` missing from bare GoChunk scope")
	}
}

// TestFileScopeForAuthoredPathUnknownPath pins the fallback boundary: a path
// that no skeleton file maps to yields nil, so innermostScopeAtAuthored can
// fall through to the package scope rather than mis-attributing a file scope.
func TestFileScopeForAuthoredPathUnknownPath(t *testing.T) {
	pkg, _ := buildSyntheticPackage(t, "package p\n\nimport \"strings\"\n\nvar _ = strings.Title\n")
	if fs := fileScopeForAuthoredPath(pkg, "nonexistent.gsx"); fs != nil {
		t.Errorf("fileScopeForAuthoredPath returned a scope for an unmapped path: %v", fs)
	}
	if got := innermostScopeAtAuthored(pkg, "nonexistent.gsx", 0); got != pkg.Types.Scope() {
		t.Errorf("innermostScopeAtAuthored for unmapped path = %v, want package scope", got)
	}
}

// buildSyntheticTwoFilePackage type-checks TWO plain Go sources together as one
// package (mirroring a real two-.gsx-file package: distinct skeleton files that
// share one types.Check call), each carrying its own //line directive so its
// decls report a distinct authored path via pkg.Fset.Position — exactly the
// production geometry buildMappedSkeleton/splitFileGoSource produce per .gsx
// file (see fileScopeForAuthoredPath's doc comment).
func buildSyntheticTwoFilePackage(t *testing.T, srcA, srcB string) *Package {
	t.Helper()
	fset := token.NewFileSet()
	fileA, err := parser.ParseFile(fset, "pA.go", srcA, 0)
	if err != nil {
		t.Fatalf("parse A: %v", err)
	}
	fileB, err := parser.ParseFile(fset, "pB.go", srcB, 0)
	if err != nil {
		t.Fatalf("parse B: %v", err)
	}
	info := &types.Info{
		Types:  map[ast.Expr]types.TypeAndValue{},
		Defs:   map[*ast.Ident]types.Object{},
		Uses:   map[*ast.Ident]types.Object{},
		Scopes: map[ast.Node]*types.Scope{},
	}
	conf := types.Config{Importer: importer.Default(), Error: func(error) {}}
	tpkg, _ := conf.Check("p", fset, []*ast.File{fileA, fileB}, info)
	return &Package{Types: tpkg, Info: info, Fset: fset}
}

// TestFileScopeForAuthoredPathTwoFiles pins the regression class the T4 review
// (.superpowers/sdd/batch2-t4-report.md, finding (e)) flagged as UNCOVERED:
// with two .gsx files in one package carrying DIFFERENT imports, the
// fileScopeForAuthoredPath fallback must return the file whose OWN decls map to
// the requested path — never the sibling file's scope, which would leak the
// wrong file's imported package names into completion. Before this test,
// nothing in the committed suite would fail if fileScopeForAuthoredPath ever
// returned the wrong file's scope in a multi-file package (buildSyntheticPackage
// only ever builds a single *ast.File); this promotes the reviewer's throwaway
// two-file probe into a permanent pin, covering both the direct helper and its
// innermostScopeAtAuthored caller (the actual completion entry point).
func TestFileScopeForAuthoredPathTwoFiles(t *testing.T) {
	srcA := `package p

import "strings"

//line a.gsx:1:1
func helperA() string { return "a" }
`
	srcB := `package p

import "os"

//line b.gsx:1:1
func helperB() string { return "b" }
`
	pkg := buildSyntheticTwoFilePackage(t, srcA, srcB)

	scopeA := fileScopeForAuthoredPath(pkg, "a.gsx")
	scopeB := fileScopeForAuthoredPath(pkg, "b.gsx")
	if scopeA == nil {
		t.Fatal("fileScopeForAuthoredPath(a.gsx) returned nil")
	}
	if scopeB == nil {
		t.Fatal("fileScopeForAuthoredPath(b.gsx) returned nil")
	}
	if scopeA == scopeB {
		t.Fatal("fileScopeForAuthoredPath returned the SAME scope for two different authored paths")
	}

	namesOf := func(scope *types.Scope) map[string]bool {
		names := map[string]bool{}
		for _, c := range scopeCandidates(pkg, scope, token.NoPos) {
			names[c.obj.Name()] = true
		}
		return names
	}
	namesA := namesOf(scopeA)
	namesB := namesOf(scopeB)

	if !namesA["strings"] {
		t.Errorf("a.gsx scope missing its own import `strings`; got %v", namesA)
	}
	if namesA["os"] {
		t.Errorf("a.gsx scope offers `os` — the WRONG file's scope leaked b.gsx's import; got %v", namesA)
	}
	if !namesB["os"] {
		t.Errorf("b.gsx scope missing its own import `os`; got %v", namesB)
	}
	if namesB["strings"] {
		t.Errorf("b.gsx scope offers `strings` — the WRONG file's scope leaked a.gsx's import; got %v", namesB)
	}

	// innermostScopeAtAuthored is the actual completion entry point: a
	// between-decls cursor (off=0, before either file's mapped func body) has no
	// enclosing func/block scope, so it must reach the same per-file scope
	// through the fallback — not the package scope, and not the other file's.
	authoredA := innermostScopeAtAuthored(pkg, "a.gsx", 0)
	authoredB := innermostScopeAtAuthored(pkg, "b.gsx", 0)
	if authoredA != scopeA {
		t.Errorf("innermostScopeAtAuthored(a.gsx, 0) = %v, want the direct fileScopeForAuthoredPath(a.gsx) result %v", authoredA, scopeA)
	}
	if authoredB != scopeB {
		t.Errorf("innermostScopeAtAuthored(b.gsx, 0) = %v, want the direct fileScopeForAuthoredPath(b.gsx) result %v", authoredB, scopeB)
	}
}

// TestMemberCandidates exercises the method-set + embedded-field BFS over a
// synthetic type, asserting promotion depth and the unexported-visibility gate.
func TestMemberCandidates(t *testing.T) {
	src := `package p

type Base struct{ Shared, base int }
type T struct {
	Base
	Name string
	priv int
}

func (T) M()  {}
func (*T) PM() {}
`
	pkg, _ := buildSyntheticPackage(t, src)
	tObj := pkg.Types.Scope().Lookup("T")
	if tObj == nil {
		t.Fatal("type T not found")
	}
	T := tObj.Type()

	collect := func(samePkg *types.Package) map[string]int {
		m := map[string]int{}
		for _, c := range memberCandidates(T, samePkg) {
			m[c.obj.Name()] = c.depth
		}
		return m
	}

	// Same package: every member is visible, including unexported ones.
	same := collect(pkg.Types)
	for _, name := range []string{"Name", "priv", "Base", "Shared", "base", "M", "PM"} {
		if _, ok := same[name]; !ok {
			t.Errorf("same-package member %q missing; got %v", name, same)
		}
	}
	if same["Shared"] != 1 {
		t.Errorf("Shared depth = %d, want 1", same["Shared"])
	}
	if same["base"] != 1 {
		t.Errorf("base depth = %d, want 1", same["base"])
	}
	if same["Name"] != 0 {
		t.Errorf("Name depth = %d, want 0", same["Name"])
	}
	if same["Base"] != 0 {
		t.Errorf("Base depth = %d, want 0", same["Base"])
	}

	// Other package (samePkg=nil): unexported members are hidden.
	other := collect(nil)
	for _, name := range []string{"Name", "Base", "Shared", "M", "PM"} {
		if _, ok := other[name]; !ok {
			t.Errorf("other-package member %q missing; got %v", name, other)
		}
	}
	for _, name := range []string{"priv", "base"} {
		if _, ok := other[name]; ok {
			t.Errorf("other-package member %q leaked (unexported); got %v", name, other)
		}
	}
}

// TestMemberCandidatesShadowing verifies a shallower field shadows a deeper
// promoted field of the same name (BFS dedup by name).
func TestMemberCandidatesShadowing(t *testing.T) {
	src := `package p

type Inner struct{ X int }
type Outer struct {
	Inner
	X string
}
`
	pkg, _ := buildSyntheticPackage(t, src)
	T := pkg.Types.Scope().Lookup("Outer").Type()
	var xDepth = -1
	var xCount int
	for _, c := range memberCandidates(T, pkg.Types) {
		if c.obj.Name() == "X" {
			xCount++
			xDepth = c.depth
		}
	}
	if xCount != 1 {
		t.Fatalf("expected exactly one X candidate (shallow shadows deep), got %d", xCount)
	}
	if xDepth != 0 {
		t.Errorf("winning X depth = %d, want 0 (Outer's own field shadows Inner's)", xDepth)
	}
}

// TestMemberCandidatesRecursiveEmbedding pins that a struct embedding a pointer
// to itself terminates (the visited-type guard) and still offers its named
// fields exactly once — before the guard this looped forever, hanging the
// dispatch goroutine.
func TestMemberCandidatesRecursiveEmbedding(t *testing.T) {
	src := `package p

type Rec struct {
	*Rec
	Label string
}
`
	pkg, _ := buildSyntheticPackage(t, src)
	T := pkg.Types.Scope().Lookup("Rec").Type()

	// A hard failure here (rather than a hang) means the guard regressed; the
	// test process would otherwise never return. count tracks duplicates.
	count := map[string]int{}
	for _, c := range memberCandidates(T, pkg.Types) {
		count[c.obj.Name()]++
	}
	if count["Label"] != 1 {
		t.Errorf("Label offered %d times, want exactly 1; got %v", count["Label"], count)
	}
	if count["Rec"] != 1 {
		t.Errorf("embedded field Rec offered %d times, want exactly 1; got %v", count["Rec"], count)
	}
	for name, n := range count {
		if n != 1 {
			t.Errorf("member %q duplicated (%d times)", name, n)
		}
	}
}

// TestMemberCandidatesMutualRecursion pins the same termination guard for a
// mutual-embedding cycle (A embeds *B, B embeds *A): the BFS visits each type
// once and returns instead of ping-ponging forever.
func TestMemberCandidatesMutualRecursion(t *testing.T) {
	src := `package p

type A struct {
	*B
	AName string
}

type B struct {
	*A
	BName string
}
`
	pkg, _ := buildSyntheticPackage(t, src)
	T := pkg.Types.Scope().Lookup("A").Type()

	count := map[string]int{}
	for _, c := range memberCandidates(T, pkg.Types) {
		count[c.obj.Name()]++
	}
	for _, name := range []string{"AName", "BName", "A", "B"} {
		if count[name] != 1 {
			t.Errorf("member %q offered %d times, want exactly 1; got %v", name, count[name], count)
		}
	}
}

// TestMemberCompletionItemsDepthClampDeepChain drives a deep linear embedding
// chain through memberCompletionItems and asserts the depth-driven sort tier is
// clamped below tierPackage: no member ever earns the "30" (tierPackage) sort
// prefix, and the deepest field clamps to "29".
func TestMemberCompletionItemsDepthClampDeepChain(t *testing.T) {
	var b strings.Builder
	b.WriteString("package p\n\n")
	const n = 25
	for i := range n {
		if i < n-1 {
			fmt.Fprintf(&b, "type Level%d struct {\n\tLevel%d\n\tF%d int\n}\n", i, i+1, i)
		} else {
			fmt.Fprintf(&b, "type Level%d struct {\n\tF%d int\n}\n", i, i)
		}
	}
	b.WriteString("\nvar v Level0\nvar _ = v.F0\n")
	src := b.String()
	pkg, _ := buildSyntheticPackage(t, src)

	// The `v.F0` selector recorded during type-checking has X of type Level0, so
	// memberCompletionItems walks the whole embedding chain and tiers every
	// promoted member by depth.
	var sel *ast.SelectorExpr
	for expr := range pkg.Info.Types {
		if se, ok := expr.(*ast.SelectorExpr); ok && se.Sel.Name == "F0" {
			sel = se
		}
	}
	if sel == nil {
		t.Fatal("v.F0 selector not found in Info.Types")
	}
	items, ok := memberCompletionItems(pkg, sel, sel.Sel.Pos(), src, 0, 0, encUTF8)
	if !ok {
		t.Fatal("memberCompletionItems did not take the member path")
	}
	byLabel := map[string]CompletionItem{}
	for _, it := range items {
		byLabel[it.Label] = it
		if strings.HasPrefix(it.SortText, "30") {
			t.Errorf("member %q reached tierPackage sort prefix %q; depth clamp failed", it.Label, it.SortText)
		}
	}
	// F20 is promoted from embedding depth 20, whose raw tier (tierMember+20 =
	// 30) equals tierPackage and must clamp to 29. (The BFS depth cap stops the
	// walk past depth 20, so F21..F24 are intentionally not offered.)
	deep := "F20"
	it, ok := byLabel[deep]
	if !ok {
		t.Fatalf("depth-20 field %q missing from members; got %v", deep, byLabel)
	}
	if !strings.HasPrefix(it.SortText, "29") {
		t.Errorf("field %q SortText = %q, want clamped tier prefix \"29\"", deep, it.SortText)
	}
}

// TestScopeCandidatesSkipsGsxInternals pins that scopeCandidates never offers a
// reserved `_gsx*` skeleton internal — accepting one would insert a generated
// identifier that poisons the file's analysis.
func TestScopeCandidatesSkipsGsxInternals(t *testing.T) {
	src := `package p

var _gsxuse = 1
var _gsxcompsig = 2
var _gsxbody = 3
var real = 4
`
	pkg, _ := buildSyntheticPackage(t, src)
	names := map[string]bool{}
	for _, c := range scopeCandidates(pkg, pkg.Types.Scope(), token.NoPos) {
		names[c.obj.Name()] = true
		if isReservedGsxInternal(c.obj.Name()) {
			t.Errorf("scopeCandidates leaked reserved internal %q", c.obj.Name())
		}
	}
	if !names["real"] {
		t.Errorf("scopeCandidates dropped the ordinary declaration `real`; got %v", names)
	}
}

// TestMemberDispatch asserts the dispatch decision: a cursor on the Sel of an
// enclosing selector takes the member path (enclosingSelector finds it), while a
// plain identifier takes the scope path (no enclosing selector).
func TestMemberDispatch(t *testing.T) {
	// Member position: `u.Na` — the cursor sits on `Na`, the Sel of the selector.
	selExpr, err := parser.ParseExpr("u.Na")
	if err != nil {
		t.Fatal(err)
	}
	sel := selExpr.(*ast.SelectorExpr)
	id := innermostIdent(sel, sel.Sel.Pos())
	if id != sel.Sel {
		t.Fatalf("innermostIdent on selector Sel = %v, want the Sel ident", id)
	}
	if got := enclosingSelector(sel, id); got != sel {
		t.Fatalf("enclosingSelector(sel, Sel) = %v, want the selector itself", got)
	}

	// Scope position: a bare identifier is not the Sel of any selector.
	plain, err := parser.ParseExpr("x")
	if err != nil {
		t.Fatal(err)
	}
	pid := plain.(*ast.Ident)
	if got := enclosingSelector(plain, pid); got != nil {
		t.Fatalf("enclosingSelector on a bare ident = %v, want nil (scope path)", got)
	}

	// The X of a selector is NOT its Sel: a cursor on `u` in `u.Na` completes as
	// a scope identifier, not a member, so enclosingSelector must not match it.
	xid := sel.X.(*ast.Ident)
	if got := enclosingSelector(sel, xid); got != nil {
		t.Fatalf("enclosingSelector on selector X = %v, want nil (X is a scope ident)", got)
	}
}

// isFileScope reports whether s is a file scope of the analyzed package. It
// has no production caller (production code goes through fileScopeSet
// directly, e.g. completion_gsx.go's importQualifierCandidates) — kept here,
// test-local, purely to give TestIsFileScope's fileScopeSet coverage a named
// single-scope assertion.
func isFileScope(pkg *Package, s *types.Scope) bool {
	return fileScopeSet(pkg)[s]
}

func TestIsFileScope(t *testing.T) {
	src := `package p

import "strings"

var global = 1
`
	pkg, _ := buildSyntheticPackage(t, src)
	var fileScopeCount int
	for node, s := range pkg.Info.Scopes {
		if _, ok := node.(*ast.File); ok {
			if !isFileScope(pkg, s) {
				t.Errorf("file scope not recognized by isFileScope")
			}
			fileScopeCount++
		}
	}
	if fileScopeCount == 0 {
		t.Fatal("no *ast.File scope found in Info.Scopes")
	}
	// The package scope is not a file scope.
	if isFileScope(pkg, pkg.Types.Scope()) {
		t.Error("package scope wrongly classified as a file scope")
	}
}
