package codegen

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// assertHTMLEqual compares got and want as HTML by structure, ignoring
// insignificant inter-element whitespace and formatting differences. On
// mismatch it fails with both rendered strings.
func assertHTMLEqual(t *testing.T, got, want string) {
	t.Helper()
	gotTree, err := html.Parse(strings.NewReader(got))
	if err != nil {
		t.Fatalf("parse got HTML: %v\n%s", err, got)
	}
	wantTree, err := html.Parse(strings.NewReader(want))
	if err != nil {
		t.Fatalf("parse want HTML: %v\n%s", err, want)
	}
	if diff := compareNodes(gotTree, wantTree); diff != "" {
		t.Fatalf("HTML mismatch (%s):\n--- got ---\n%s\n--- want ---\n%s", diff, got, want)
	}
}

// wsRun collapses runs of whitespace into single spaces.
var wsRun = regexp.MustCompile(`\s+`)

// compareNodes structurally compares two parsed HTML nodes. It returns "" when
// equal, or a human-readable description of the first divergence.
func compareNodes(a, b *html.Node) string {
	if a.Type != b.Type {
		return fmt.Sprintf("node type %v != %v", a.Type, b.Type)
	}
	switch a.Type {
	case html.ElementNode:
		if a.Data != b.Data {
			return fmt.Sprintf("tag <%s> != <%s>", a.Data, b.Data)
		}
		if as, bs := attrSet(a), attrSet(b); as != bs {
			return fmt.Sprintf("<%s> attrs %q != %q", a.Data, as, bs)
		}
	case html.TextNode:
		at, bt := strings.TrimSpace(collapseWS(a.Data)), strings.TrimSpace(collapseWS(b.Data))
		if at != bt {
			return fmt.Sprintf("text %q != %q", at, bt)
		}
	case html.CommentNode, html.DoctypeNode:
		if a.Data != b.Data {
			return fmt.Sprintf("%v data %q != %q", a.Type, a.Data, b.Data)
		}
	}

	ac, bc := significantChildren(a), significantChildren(b)
	if len(ac) != len(bc) {
		return fmt.Sprintf("<%s> child count %d != %d", nodeLabel(a), len(ac), len(bc))
	}
	for i := range ac {
		if diff := compareNodes(ac[i], bc[i]); diff != "" {
			return diff
		}
	}
	return ""
}

// significantChildren returns a node's children, dropping whitespace-only text
// nodes that sit between elements (insignificant formatting whitespace).
func significantChildren(n *html.Node) []*html.Node {
	var out []*html.Node
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode && strings.TrimSpace(c.Data) == "" {
			continue
		}
		out = append(out, c)
	}
	return out
}

// attrSet renders a node's attributes as a sorted, comparable key=value set.
func attrSet(n *html.Node) string {
	parts := make([]string, 0, len(n.Attr))
	for _, a := range n.Attr {
		key := a.Key
		if a.Namespace != "" {
			key = a.Namespace + ":" + a.Key
		}
		parts = append(parts, key+"="+a.Val)
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}

func collapseWS(s string) string {
	return wsRun.ReplaceAllString(s, " ")
}

func nodeLabel(n *html.Node) string {
	if n.Type == html.ElementNode {
		return n.Data
	}
	return fmt.Sprintf("node(type=%d)", n.Type)
}

// TestRenderFieldAccess proves go/types resolves a struct field-access
// interpolation (user.Name string, user.Age int) end-to-end.
func TestRenderFieldAccess(t *testing.T) {
	files := map[string]string{
		"model.go": `package views

type User struct {
	Name string
	Age  int
}
`,
		"views.gsx": `package views

component Profile(user User) {
	<p>{user.Name} is {user.Age}</p>
}
`,
	}
	got := renderPackage(t, files, `p.Profile(p.ProfileProps{User: p.User{Name: "Alice", Age: 30}})`)
	assertHTMLEqual(t, got, "<p>Alice is 30</p>")
}

// renderPackage writes files (e.g. a sibling .go and one or more .gsx) into a
// package inside a temp module, runs GeneratePackage (go/packages + Overlay
// resolution), writes the generated .x.go, compiles a harness that renders
// `invocation` (qualified with package alias `p`), and returns the HTML.
func renderPackage(t *testing.T, files map[string]string, invocation string) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping go-run render test in -short mode")
	}
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gsxrender\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(tmp, "genpkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		writeFile(t, pkgDir, name, content)
	}

	gen, err := GeneratePackage(pkgDir)
	if err != nil {
		t.Fatalf("GeneratePackage: %v", err)
	}
	for gsxPath, src := range gen {
		base := strings.TrimSuffix(filepath.Base(gsxPath), ".gsx")
		writeFile(t, pkgDir, base+".x.go", string(src))
	}

	writeFile(t, tmp, "main.go", `package main

import (
	"context"
	"os"

	"github.com/gsxhq/gsx"
	p "gsxrender/genpkg"
)

var _ = gsx.Raw

func main() {
	_ = `+invocation+`.Render(context.Background(), os.Stdout)
}
`)
	cmd := exec.Command("go", "run", ".")
	cmd.Dir = tmp
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run failed: %v\n%s", err, out)
	}
	return string(out)
}

// generatePackageErr writes files into a temp-module package and runs
// GeneratePackage, returning its error (or nil). It is for tests that assert a
// clean CODEGEN error (no compile/render), so it does not need -short skipping.
func generatePackageErr(t *testing.T, files map[string]string) error {
	t.Helper()
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gsxgenerr\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(tmp, "genpkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for n, c := range files {
		writeFile(t, pkgDir, n, c)
	}
	_, err := GeneratePackage(pkgDir)
	return err
}

// TestRenderParamNameCollision proves params named after the generator's
// internal machinery (gw/w/p) no longer collide with it: the machinery moved to
// the _gsx* namespace, so these params bind and render verbatim.
func TestRenderParamNameCollision(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

component Collide(gw string, w string, p string) {
	<p>{gw}|{w}|{p}</p>
}
`,
	}
	got := renderPackage(t, files, `p.Collide(p.CollideProps{Gw: "G", W: "W", P: "P"})`)
	assertHTMLEqual(t, got, "<p>G|W|P</p>")
}

// TestReservedParamCtx proves a param named exactly `ctx` (ambient context) is
// rejected with a clean codegen error.
func TestReservedParamCtx(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

component Bad(ctx string) {
	<p>{ctx}</p>
}
`,
	}
	err := generatePackageErr(t, files)
	if err == nil {
		t.Fatal("expected error for param named ctx, got nil")
	}
	if !strings.Contains(err.Error(), "reserved") || !strings.Contains(err.Error(), "ctx") {
		t.Fatalf("expected clean reserved-name error, got: %v", err)
	}
}

// TestRenderCtxInInterp proves an interpolation referencing the ambient `ctx`
// (the closure's context.Context param) type-checks and renders: the skeleton
// component func binds a real `ctx` so `{ fromCtx(ctx) }` resolves instead of
// failing with `undefined: ctx`.
func TestRenderCtxInInterp(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

import "context"

func fromCtx(ctx context.Context) string {
	if ctx == nil {
		return "nil-ctx"
	}
	return "ok"
}

component C() {
	<p>{ fromCtx(ctx) }</p>
}
`,
	}
	got := renderPackage(t, files, `p.C(p.CProps{})`)
	assertHTMLEqual(t, got, "<p>ok</p>")
}

// TestReservedParamGsxPrefix proves a param using the reserved _gsx prefix is
// rejected with a clean codegen error.
func TestReservedParamGsxPrefix(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

component Bad(_gsxfoo string) {
	<p>{_gsxfoo}</p>
}
`,
	}
	err := generatePackageErr(t, files)
	if err == nil {
		t.Fatal("expected error for param using _gsx prefix, got nil")
	}
	if !strings.Contains(err.Error(), "_gsx") || !strings.Contains(err.Error(), "prefix") {
		t.Fatalf("expected clean reserved-prefix error, got: %v", err)
	}
}

// TestReservedParamEmittedImport proves a param named after a package the
// emitter references in the closure body (gsx, strconv) is rejected cleanly,
// rather than producing non-compiling generated code via local-binding shadowing.
func TestReservedParamEmittedImport(t *testing.T) {
	for _, name := range []string{"gsx", "strconv"} {
		files := map[string]string{
			"views.gsx": "package views\n\ncomponent Bad(" + name + " string, n int) {\n\t<p>{" + name + "}{n}</p>\n}\n",
		}
		err := generatePackageErr(t, files)
		if err == nil {
			t.Fatalf("param %q: expected reserved-name error, got nil", name)
		}
		if !strings.Contains(err.Error(), "reserved") {
			t.Fatalf("param %q: expected clean reserved-name error, got: %v", name, err)
		}
	}
}

// TestRenderPointerNode proves a *Widget param whose POINTER implements Render
// (pointer-receiver) classifies as catNode and renders via gw.Node(ctx, ptr),
// since *Widget's value method set has Render.
func TestRenderPointerNode(t *testing.T) {
	files := map[string]string{
		"widget.go": `package views

import (
	"context"
	"io"
)

type Widget struct{ Label string }

func (w *Widget) Render(ctx context.Context, out io.Writer) error {
	_, err := io.WriteString(out, "<b>"+w.Label+"</b>")
	return err
}
`,
		"views.gsx": `package views

component Show(widget *Widget) {
	<div>{widget}</div>
}
`,
	}
	got := renderPackage(t, files, `p.Show(p.ShowProps{Widget: &p.Widget{Label: "hi"}})`)
	assertHTMLEqual(t, got, "<div><b>hi</b></div>")
}

// TestValueNodePointerReceiverCleanError proves a Widget VALUE param whose only
// Render is pointer-receiver is NOT mis-classified as catNode (its value method
// set lacks Render): it falls through to a clean "not a renderable type" codegen
// error rather than producing non-compiling generated code.
func TestValueNodePointerReceiverCleanError(t *testing.T) {
	files := map[string]string{
		"widget.go": `package views

import (
	"context"
	"io"
)

type Widget struct{ Label string }

func (w *Widget) Render(ctx context.Context, out io.Writer) error {
	_, err := io.WriteString(out, "<b>"+w.Label+"</b>")
	return err
}
`,
		"views.gsx": `package views

component Show(widget Widget) {
	<div>{widget}</div>
}
`,
	}
	err := generatePackageErr(t, files)
	if err == nil {
		t.Fatal("expected clean error for value param with pointer-receiver Render, got nil")
	}
	if !strings.Contains(err.Error(), "not a renderable type") {
		t.Fatalf("expected clean 'not a renderable type' error, got: %v", err)
	}
}

// TestRenderRealGsxsharedFile proves a real gsxshared.x.go on disk is not
// clobbered by the _gsxuse probe overlay: the generator picks a free overlay
// filename, so UserHelper (declared in the real file) resolves and renders.
func TestRenderRealGsxsharedFile(t *testing.T) {
	files := map[string]string{
		"gsxshared.x.go": `package views

func UserHelper() string { return "real" }
`,
		"views.gsx": `package views

component Greet() {
	<p>{UserHelper()}</p>
}
`,
	}
	got := renderPackage(t, files, `p.Greet(p.GreetProps{})`)
	assertHTMLEqual(t, got, "<p>real</p>")
}

// TestProbeAcceptsMultiValueExpr is a forward check that the probe type-checks a
// (T, error) interpolation expression (multi-value), which the old `_ = (expr)`
// probe could not. Full unwrap rendering lands in Task 3; here we only assert
// the package RESOLVES + GENERATES without a type error.
func TestProbeAcceptsMultiValueExpr(t *testing.T) {
	files := map[string]string{
		"helpers.go": `package views

func lookup(k string) (string, error) { return k, nil }
`,
		"views.gsx": `package views

component Label(key string) {
	<span>{lookup(key)}</span>
}
`,
	}
	tmp := t.TempDir()
	repoRoot, _ := filepath.Abs("../..")
	writeFile(t, tmp, "go.mod", "module gsxr\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(tmp, "genpkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for n, c := range files {
		writeFile(t, pkgDir, n, c)
	}
	// Resolution must succeed (the multi-value expr type-checks under _gsxuse);
	// EMISSION may still error "not supported yet" until Task 3 — that is fine.
	_, err := GeneratePackage(pkgDir)
	if err != nil && strings.Contains(err.Error(), "type resolution failed") {
		t.Fatalf("probe failed to type-check a (T,error) expr: %v", err)
	}
}

// TestRenderTryUnwrap exercises the (T, error) auto-unwrap: an interpolation of
// a func returning (string, error) is lowered to a temp + error-propagate, then
// the value is rendered by its category.
func TestRenderTryUnwrap(t *testing.T) {
	files := map[string]string{
		"helpers.go": `package views

func greet(name string) (string, error) { return "Hi " + name, nil }
`,
		"views.gsx": `package views

component Card(name string) {
	<p>{greet(name)}</p>
}
`,
	}
	got := renderPackage(t, files, `p.Card(p.CardProps{Name: "Al"})`)
	assertHTMLEqual(t, got, "<p>Hi Al</p>")
}

func TestRenderForLoop(t *testing.T) {
	files := map[string]string{
		"model.go": `package views

type Item struct {
	Name  string
	Count int
}
`,
		"views.gsx": `package views

component List(items []Item) {
	<ul>{ for _, it := range items { <li>{it.Name}: {it.Count}</li> } }</ul>
}
`,
	}
	got := renderPackage(t, files,
		`p.List(p.ListProps{Items: []p.Item{{Name: "a", Count: 1}, {Name: "b", Count: 2}}})`)
	assertHTMLEqual(t, got, "<ul><li>a: 1</li><li>b: 2</li></ul>")
}

func TestRenderStaticAndBoolAttrs(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

component Field(on bool) {
	<input type="text" class="form-control" required disabled={on}/>
}
`,
	}
	got := renderPackage(t, files, `p.Field(p.FieldProps{On: true})`)
	assertHTMLEqual(t, got, `<input type="text" class="form-control" required disabled/>`)
	got = renderPackage(t, files, `p.Field(p.FieldProps{On: false})`)
	assertHTMLEqual(t, got, `<input type="text" class="form-control" required/>`)
}

func TestRenderFragment(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

component Pair(a string, b string) {
	<><span>{a}</span><span>{b}</span></>
}
`,
	}
	got := renderPackage(t, files, `p.Pair(p.PairProps{A: "x", B: "y"})`)
	assertHTMLEqual(t, got, "<span>x</span><span>y</span>")
}

func TestRenderInterpTypes(t *testing.T) {
	files := map[string]string{
		"model.go": `package views

import "fmt"

type Money int

func (m Money) String() string { return fmt.Sprintf("$%d", int(m)) }
`,
		"views.gsx": `package views

import "github.com/gsxhq/gsx"

component Demo(s string, n int, f float64, ok bool, node gsx.Node, price Money) {
	<div>{s}|{n}|{f}|{ok}|{node}|{price}</div>
}
`,
	}
	got := renderPackage(t, files,
		`p.Demo(p.DemoProps{S: "hi", N: 7, F: 3.5, Ok: true, Node: gsx.Raw("<b>x</b>"), Price: p.Money(9)})`)
	// gsx.Raw renders verbatim; Money is a fmt.Stringer -> "$9"; bool -> "true".
	assertHTMLEqual(t, got, `<div>hi|7|3.5|true|<b>x</b>|$9</div>`)
}

// TestRenderMixedChunk proves a single GoChunk that mixes an import with
// trailing type/func declarations is NOT misclassified as a pure-import chunk:
// the import is hoisted, AND the trailing decls survive into the body and are
// usable by the component. Regression for the ImportsOnly classification bug.
func TestRenderMixedChunk(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

import "fmt"

type Money int

func (m Money) String() string { return fmt.Sprintf("$%d", int(m)) }

func label() string { return "price:" }

component Receipt(price Money) {
	<p>{label()}{price}</p>
}
`,
	}
	// label() returns string; Money is a fmt.Stringer -> "$9". Both the helper
	// func and the Money type live in the same chunk as the `import "fmt"`.
	got := renderPackage(t, files, `p.Receipt(p.ReceiptProps{Price: p.Money(9)})`)
	assertHTMLEqual(t, got, `<p>price:$9</p>`)
}

// TestRenderNodeSlice exercises catNodeSlice: a []gsx.Node param interpolated as
// {items} must emit a for-loop that renders each node in order.
func TestRenderNodeSlice(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

import "github.com/gsxhq/gsx"

component List(items []gsx.Node) {
	<ul>{items}</ul>
}
`,
	}
	got := renderPackage(t, files,
		`p.List(p.ListProps{Items: []gsx.Node{gsx.Raw("<li>a</li>"), gsx.Raw("<li>b</li>")}})`)
	assertHTMLEqual(t, got, `<ul><li>a</li><li>b</li></ul>`)
}

// TestRenderMultiGsxPackage proves two .gsx files in one package resolve and
// render together: a cross-file component call (<Footer/> defined in a.gsx,
// called from b.gsx). Regression for the "_gsxuse redeclared" bug, where each
// per-file skeleton declared _gsxuse, breaking type resolution for the package.
func TestRenderMultiGsxPackage(t *testing.T) {
	files := map[string]string{
		"a.gsx": `package views

component Footer() {
	<footer>FOOT</footer>
}
`,
		"b.gsx": `package views

component Page() {
	<div><Footer/></div>
}
`,
	}
	got := renderPackage(t, files, `p.Page(p.PageProps{})`)
	assertHTMLEqual(t, got, `<div><footer>FOOT</footer></div>`)
}

// TestRenderNamedScalarTypes proves a named string type (type ID string) and a
// named bool type (type Flag bool) interpolate and compile: emitRender must
// convert to the underlying type (string(id), bool(flag)) or `go build` fails.
func TestRenderNamedScalarTypes(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

type ID string

type Flag bool

component Tag(id ID, flag Flag) {
	<p>{id}|{flag}</p>
}
`,
	}
	got := renderPackage(t, files, `p.Tag(p.TagProps{Id: p.ID("abc"), Flag: p.Flag(true)})`)
	assertHTMLEqual(t, got, `<p>abc|true</p>`)
}

// TestInterleavedImportsCleanError proves that a pass-through Go block with an
// import appearing after a func (invalid Go: imports must be first) yields a
// clean "pass-through"/"invalid Go" diagnostic rather than a cryptic leaked
// "imports must appear before other declarations" resolution error or a panic.
func TestInterleavedImportsCleanError(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

func helper() string { return "x" }

import "fmt"

var _ = fmt.Sprintf

component Bad() {
	<p>{helper()}</p>
}
`,
	}
	tmp := t.TempDir()
	repoRoot, _ := filepath.Abs("../..")
	writeFile(t, tmp, "go.mod", "module gsxbad\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(tmp, "genpkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for n, c := range files {
		writeFile(t, pkgDir, n, c)
	}
	_, err := GeneratePackage(pkgDir)
	if err == nil {
		t.Fatal("expected error for interleaved imports, got nil")
	}
	if !strings.Contains(err.Error(), "pass-through") && !strings.Contains(err.Error(), "invalid Go") {
		t.Fatalf("expected clean pass-through/invalid Go error, got: %v", err)
	}
}

// TestMethodOnlyFileGenerates proves a .gsx whose only component is a method
// component generates without an "imported and not used" error masking the
// skeleton (the skeleton imports _gsxrt but, with no other components, must
// still reference it via var _). Method components are now supported (Task 2).
func TestMethodOnlyFileGenerates(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

type P struct{}

component (p P) View() {
	<p>x</p>
}
`,
	}
	tmp := t.TempDir()
	repoRoot, _ := filepath.Abs("../..")
	writeFile(t, tmp, "go.mod", "module gsxmeth\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(tmp, "genpkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for n, c := range files {
		writeFile(t, pkgDir, n, c)
	}
	_, err := GeneratePackage(pkgDir)
	if err != nil {
		t.Fatalf("expected method-only file to generate, got error: %v", err)
	}
}

// TestRenderMethodNullary proves a nullary method component
// `(p UsersPage) Page()` emits a Go method with no props struct and no _gsxp
// param; the receiver var is in scope so {p.Title} references the receiver field.
func TestRenderMethodNullary(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

type UsersPage struct {
	Title string
	Sort  string
}

component (p UsersPage) Page() {
	<h1>{p.Title}</h1>
}
`,
	}
	got := renderPackage(t, files, `(p.UsersPage{Title: "Hi"}).Page()`)
	assertHTMLEqual(t, got, `<h1>Hi</h1>`)
}

// TestRenderMethodWithParam proves a method component with a param emits a
// <RecvType><Method>Props struct (UsersPageGridProps{Sort string}); the body
// references both the param (sort) and the receiver field (p.Title).
func TestRenderMethodWithParam(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

type UsersPage struct {
	Title string
	Sort  string
}

component (p UsersPage) Grid(sort string) {
	<div>{sort}-{p.Title}</div>
}
`,
	}
	got := renderPackage(t, files,
		`(p.UsersPage{Title: "T"}).Grid(p.UsersPageGridProps{Sort: "name"})`)
	assertHTMLEqual(t, got, `<div>name-T</div>`)
}

// TestRenderMethodPointerReceiver proves a pointer-receiver method component
// `(f *Form) Render2()` compiles and renders; the emitted receiver keeps the
// `*Form` while the props-struct name (if any) strips the `*` (FormRender2Props).
func TestRenderMethodPointerReceiver(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

type Form struct {
	Action string
}

component (f *Form) Render2(label string) {
	<form action={f.Action}>{label}</form>
}
`,
	}
	got := renderPackage(t, files,
		`(&p.Form{Action: "/submit"}).Render2(p.FormRender2Props{Label: "Go"})`)
	assertHTMLEqual(t, got, `<form action="/submit">Go</form>`)
}

// TestMethodUnnamedReceiverError proves an unnamed receiver
// `component (UsersPage) X()` is rejected with a clear error.
func TestMethodUnnamedReceiverError(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

type UsersPage struct{ Title string }

component (UsersPage) X() {
	<p>x</p>
}
`,
	}
	err := generatePackageErr(t, files)
	if err == nil {
		t.Fatal("expected error for unnamed receiver, got nil")
	}
	if !strings.Contains(err.Error(), "named") {
		t.Fatalf("expected receiver-must-be-named error, got: %v", err)
	}
}

// TestMethodReservedRecvVarCtx proves a receiver var named `ctx` is rejected:
// it would shadow the ambient context in the emitted closure body.
func TestMethodReservedRecvVarCtx(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

type UsersPage struct{ Title string }

component (ctx UsersPage) X() {
	<p>x</p>
}
`,
	}
	err := generatePackageErr(t, files)
	if err == nil {
		t.Fatal("expected error for receiver var named ctx, got nil")
	}
	if !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("expected reserved receiver-name error, got: %v", err)
	}
}

// TestRenderCrossFileAndComponent proves go/packages + Overlay resolves a type
// defined in a sibling .go file (User) AND a cross-component call (<Footer/>).
func TestRenderCrossFileAndComponent(t *testing.T) {
	files := map[string]string{
		"model.go": `package views

type User struct {
	Name string
	Age  int
}
`,
		"views.gsx": `package views

component Footer() {
	<footer>(c) gsx</footer>
}

component Profile(user User) {
	<div>{user.Name} ({user.Age}) <Footer/></div>
}
`,
	}
	got := renderPackage(t, files, `p.Profile(p.ProfileProps{User: p.User{Name: "Alice", Age: 30}})`)
	assertHTMLEqual(t, got, `<div>Alice (30) <footer>(c) gsx</footer></div>`)
}

func TestRenderPipelineBare(t *testing.T) {
	files := map[string]string{"views.gsx": `package views

component Hi(name string) { <p>{ name |> upper }</p> }
`}
	got := renderPackage(t, files, `p.Hi(p.HiProps{Name: "ada"})`)
	assertHTMLEqual(t, got, `<p>ADA</p>`)
}

func TestRenderPipelineChain(t *testing.T) {
	files := map[string]string{"views.gsx": `package views

component Hi(name string) { <p>{ name |> trim |> upper }</p> }
`}
	got := renderPackage(t, files, `p.Hi(p.HiProps{Name: "  ada  "})`)
	assertHTMLEqual(t, got, `<p>ADA</p>`)
}

func TestRenderPipelineParam(t *testing.T) {
	files := map[string]string{"views.gsx": `package views

component Hi(s string) { <p>{ s |> truncate(3) }</p> }
`}
	got := renderPackage(t, files, `p.Hi(p.HiProps{S: "abcdef"})`)
	assertHTMLEqual(t, got, `<p>abc</p>`)
}

func TestRenderPipelineJoin(t *testing.T) {
	files := map[string]string{"views.gsx": `package views

component Tags(tags []string) { <p>{ tags |> join(", ") }</p> }
`}
	got := renderPackage(t, files, `p.Tags(p.TagsProps{Tags: []string{"a","b","c"}})`)
	assertHTMLEqual(t, got, `<p>a, b, c</p>`)
}

func TestRenderPipelineParamArg(t *testing.T) {
	// A component param referenced ONLY inside a filter argument must be bound as
	// a local (the lowered _gsxstd.Join(sep)(...) references it verbatim).
	files := map[string]string{"views.gsx": `package views

component Tags(tags []string, sep string) { <p>{ tags |> join(sep) }</p> }
`}
	got := renderPackage(t, files, `p.Tags(p.TagsProps{Tags: []string{"a","b"}, Sep: " / "})`)
	assertHTMLEqual(t, got, `<p>a / b</p>`)
}

func TestRenderPipelineLoopVar(t *testing.T) {
	files := map[string]string{"views.gsx": `package views

component L(xs []string) { <ul>{ for _, x := range xs { <li>{ x |> upper }</li> } }</ul> }
`}
	got := renderPackage(t, files, `p.L(p.LProps{Xs: []string{"a","b"}})`)
	assertHTMLEqual(t, got, `<ul><li>A</li><li>B</li></ul>`)
}

func TestPipelineUnknownFilter(t *testing.T) {
	files := map[string]string{"views.gsx": `package views

component Hi(name string) { <p>{ name |> bogus }</p> }
`}
	if err := generatePackageErr(t, files); err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("expected unknown-filter error naming bogus, got: %v", err)
	}
}

func TestPipelineArityMismatch(t *testing.T) {
	// bare filter given args
	files := map[string]string{"views.gsx": `package views

component Hi(name string) { <p>{ name |> upper(2) }</p> }
`}
	if err := generatePackageErr(t, files); err == nil || !strings.Contains(err.Error(), "upper") {
		t.Fatalf("expected arity error naming upper, got: %v", err)
	}
}

func TestPipelineTryRejected(t *testing.T) {
	files := map[string]string{"views.gsx": `package views

component Hi(name string) { <p>{ name |> upper? }</p> }
`}
	if err := generatePackageErr(t, files); err == nil || !strings.Contains(err.Error(), "?") {
		t.Fatalf("expected ?-deferred error, got: %v", err)
	}
}

func TestRenderIf(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

component Status(n int) {
	<p>{ if n > 0 { <span>pos</span> } else if n < 0 { <span>neg</span> } else { <span>zero</span> } }</p>
}
`,
	}
	for _, tc := range []struct {
		n    int
		want string
	}{{1, "<p><span>pos</span></p>"}, {-1, "<p><span>neg</span></p>"}, {0, "<p><span>zero</span></p>"}} {
		got := renderPackage(t, files, fmt.Sprintf(`p.Status(p.StatusProps{N: %d})`, tc.n))
		assertHTMLEqual(t, got, tc.want)
	}
}

func TestRenderSwitch(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

component Badge(kind string) {
	<span>{ switch kind {
	case "warn":
		<b>warning</b>
	case "err":
		<b>error</b>
	default:
		<b>info</b>
	} }</span>
}
`,
	}
	for _, tc := range []struct{ kind, want string }{
		{"warn", "<span><b>warning</b></span>"},
		{"err", "<span><b>error</b></span>"},
		{"other", "<span><b>info</b></span>"},
	} {
		got := renderPackage(t, files, fmt.Sprintf(`p.Badge(p.BadgeProps{Kind: %q})`, tc.kind))
		assertHTMLEqual(t, got, tc.want)
	}
}

func TestRenderGoBlock(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

component Chip(first string, last string) {
	<div>{{ full := first + " " + last }}<span>{full}</span></div>
}
`,
	}
	got := renderPackage(t, files, `p.Chip(p.ChipProps{First: "Ada", Last: "Lovelace"})`)
	assertHTMLEqual(t, got, "<div><span>Ada Lovelace</span></div>")
}

func TestRenderExprAttrs(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

component Link(url string, label string, n int, checked bool) {
	<a href={url} data-label={label} data-n={n} aria-hidden={checked}>{label}</a>
}
`,
	}
	// URL sanitized + attr-escaped; string attr-escaped; int formatted; bool -> boolean attr.
	got := renderPackage(t, files,
		`p.Link(p.LinkProps{Url: "/p?q=a&b", Label: "a\"b", N: 5, Checked: true})`)
	assertHTMLEqual(t, got, `<a href="/p?q=a&b" data-label="a&#34;b" data-n="5" aria-hidden>a"b</a>`)
}

func TestRenderExprAttrURLBlocked(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

component Bad(u string) { <a href={u}>x</a> }
`,
	}
	got := renderPackage(t, files, `p.Bad(p.BadProps{U: "javascript:alert(1)"})`)
	// urlSanitize replaces a dangerous scheme with the sentinel.
	assertHTMLEqual(t, got, `<a href="about:invalid#gsx">x</a>`)
}

func TestRenderExprAttrJSRejected(t *testing.T) {
	for _, name := range []string{"onclick", "style"} {
		files := map[string]string{
			"views.gsx": "package views\n\ncomponent C(x string) {\n\t<div " + name + "={x}>y</div>\n}\n",
		}
		err := generatePackageErr(t, files)
		if err == nil {
			t.Fatalf("%s: expected fail-closed error for expr in JS/CSS context, got nil", name)
		}
		if !strings.Contains(err.Error(), "context") {
			t.Fatalf("%s: expected context-rejection error, got: %v", name, err)
		}
	}
}

func TestRenderExprAttrXSS(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

component C(v string) { <div data-x={v}>y</div> }
`,
	}
	got := renderPackage(t, files, `p.C(p.CProps{V: "\"><script>alert(1)</script>"})`)
	// the quote and angle brackets must be entity-escaped — no tag breakout.
	assertHTMLEqual(t, got, `<div data-x="&#34;&gt;&lt;script&gt;alert(1)&lt;/script&gt;">y</div>`)
}

func TestRenderAttrPipelinePlain(t *testing.T) {
	files := map[string]string{"views.gsx": `package views

component C(name string) { <div data-x={ name |> upper }>y</div> }
`}
	got := renderPackage(t, files, `p.C(p.CProps{Name: "ada"})`)
	assertHTMLEqual(t, got, `<div data-x="ADA">y</div>`)
}

func TestRenderAttrPipelineEscaped(t *testing.T) {
	// the pipeline result is still attr-escaped (no breakout)
	files := map[string]string{"views.gsx": `package views

component C(name string) { <div data-x={ name |> trim }>y</div> }
`}
	got := renderPackage(t, files, `p.C(p.CProps{Name: "  a\"b  "})`)
	assertHTMLEqual(t, got, `<div data-x="a&#34;b">y</div>`)
}

func TestRenderAttrPipelineURL(t *testing.T) {
	// URL context: result routed through gw.URL (scheme allow-list + escape)
	files := map[string]string{"views.gsx": `package views

component C(u string) { <a href={ u |> trim }>x</a> }
`}
	got := renderPackage(t, files, `p.C(p.CProps{U: "  javascript:alert(1)  "})`)
	assertHTMLEqual(t, got, `<a href="about:invalid#gsx">x</a>`)
}

func TestRenderAttrPipelineURLOK(t *testing.T) {
	files := map[string]string{"views.gsx": `package views

component C(u string) { <a href={ u |> trim }>x</a> }
`}
	got := renderPackage(t, files, `p.C(p.CProps{U: "  /p?q=a&b  "})`)
	assertHTMLEqual(t, got, `<a href="/p?q=a&b">x</a>`)
}

func TestAttrPipelineJSRejected(t *testing.T) {
	// JS context rejects even with a pipeline (pipeline does not unlock it)
	files := map[string]string{"views.gsx": `package views

component C(x string) { <div onclick={ x |> upper }>y</div> }
`}
	if err := generatePackageErr(t, files); err == nil || !strings.Contains(err.Error(), "context") {
		t.Fatalf("expected JS-context rejection, got: %v", err)
	}
}

func TestAttrPipelineUnknownFilter(t *testing.T) {
	files := map[string]string{"views.gsx": `package views

component C(x string) { <div data-x={ x |> bogus }>y</div> }
`}
	if err := generatePackageErr(t, files); err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("expected unknown-filter error, got: %v", err)
	}
}

func TestAttrPipelineTryStageRejected(t *testing.T) {
	files := map[string]string{"views.gsx": `package views

component C(x string) { <div data-x={ x |> upper? }>y</div> }
`}
	if err := generatePackageErr(t, files); err == nil {
		t.Fatalf("expected ?-stage deferred error, got nil")
	}
}

// TestRenderComposableClass proves `class={ "a", "b": cond, extra }` lowers to
// gsx.Class/gsx.ClassIf parts, merges (default merger dedupes + space-joins) and
// drops the conditional token when its cond is false. The `extra` param is used
// ONLY in a class part, so it also proves the usedParams binding reaches there.
func TestRenderComposableClass(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

component C(on bool, extra string) {
	<div class={ "btn", "btn-on": on, extra }>y</div>
}
`,
	}
	gotOn := renderPackage(t, files, `p.C(p.CProps{On: true, Extra: "x"})`)
	assertHTMLEqual(t, gotOn, `<div class="btn btn-on x">y</div>`)

	gotOff := renderPackage(t, files, `p.C(p.CProps{On: false, Extra: "x"})`)
	assertHTMLEqual(t, gotOff, `<div class="btn x">y</div>`)
}

// TestRenderComposableClassEscaping proves a class token / override containing a
// `"` is attr-escaped — no breakout.
func TestRenderComposableClassEscaping(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

component C(extra string) {
	<div class={ "btn", extra }>y</div>
}
`,
	}
	got := renderPackage(t, files, `p.C(p.CProps{Extra: "\"><script>"})`)
	assertHTMLEqual(t, got, `<div class="btn &#34;&gt;&lt;script&gt;">y</div>`)
}

// TestRenderElementSpread proves `<div {...attrs}>` emits gw.Spread: keys sorted,
// bool true -> boolean attr, others key="value" attr-escaped. The `attrs` param
// is used ONLY in the spread, so it also proves usedParams binds it.
func TestRenderElementSpread(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

import "github.com/gsxhq/gsx"

component C(attrs gsx.Attrs) {
	<div {...attrs}>y</div>
}
`,
	}
	got := renderPackage(t, files,
		`p.C(p.CProps{Attrs: gsx.Attrs{"data-x": "1", "hidden": true, "class": "c"}})`)
	// Spread sorts keys: class, data-x, hidden. hidden is a bool boolean-attr.
	assertHTMLEqual(t, got, `<div class="c" data-x="1" hidden>y</div>`)
}

// TestRenderComposableStyleRejected proves `style={ … }` composition still fails
// closed (CSS context cannot be entity-secured).
func TestRenderComposableStyleRejected(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

component C(on bool) { <div style={ "color: red": on }>y</div> }
`,
	}
	err := generatePackageErr(t, files)
	if err == nil {
		t.Fatalf("expected fail-closed error for style composition, got nil")
	}
	if !strings.Contains(err.Error(), "context") {
		t.Fatalf("expected context-rejection error, got: %v", err)
	}
}

func TestRenderCondAttrBool(t *testing.T) {
	files := map[string]string{"views.gsx": `package views

component C(featured bool) { <span { if featured { class="badge" } }>y</span> }
`}
	got := renderPackage(t, files, `p.C(p.CProps{Featured: true})`)
	assertHTMLEqual(t, got, `<span class="badge">y</span>`)
	got = renderPackage(t, files, `p.C(p.CProps{Featured: false})`)
	assertHTMLEqual(t, got, `<span>y</span>`)
}

func TestRenderCondAttrElseTypedExprs(t *testing.T) {
	// BOTH branches carry a typed expr value — the order-invariant check: each
	// must resolve+escape with its OWN type, not the other's.
	files := map[string]string{"views.gsx": `package views

component C(a bool, x string, n int) { <div { if a { data-x={x} } else { data-n={n} } }>y</div> }
`}
	got := renderPackage(t, files, `p.C(p.CProps{A: true, X: "a\"b", N: 5})`)
	assertHTMLEqual(t, got, `<div data-x="a&#34;b">y</div>`)
	got = renderPackage(t, files, `p.C(p.CProps{A: false, X: "a\"b", N: 5})`)
	assertHTMLEqual(t, got, `<div data-n="5">y</div>`)
}

func TestRenderCondAttrElseIf(t *testing.T) {
	files := map[string]string{"views.gsx": `package views

component C(n int) { <div { if n == 1 { data-one="y" } else if n == 2 { data-two="y" } else { data-other="y" } }>z</div> }
`}
	got := renderPackage(t, files, `p.C(p.CProps{N: 2})`)
	assertHTMLEqual(t, got, `<div data-two="y">z</div>`)
	got = renderPackage(t, files, `p.C(p.CProps{N: 9})`)
	assertHTMLEqual(t, got, `<div data-other="y">z</div>`)
}

func TestRenderCondAttrInterleaved(t *testing.T) {
	// a conditional attr BETWEEN two plain typed attrs + a typed child interp —
	// strongest order-invariant probe (misalignment renders one value's type for
	// another).
	files := map[string]string{"views.gsx": `package views

component C(a bool, s string, n int, f float64) { <div data-s={s} { if a { data-x={n} } } data-f={f}>{ n }</div> }
`}
	got := renderPackage(t, files, `p.C(p.CProps{A: true, S: "hi", N: 7, F: 2.5})`)
	assertHTMLEqual(t, got, `<div data-s="hi" data-x="7" data-f="2.5">7</div>`)
}

func TestRenderCondAttrParamOnlyInBranch(t *testing.T) {
	// param used ONLY inside a conditional branch's attr expr must be bound
	// (usedParams must recurse CondAttr — else `undefined: msg`).
	files := map[string]string{"views.gsx": `package views

component C(show bool, msg string) { <div { if show { title={msg} } }>y</div> }
`}
	got := renderPackage(t, files, `p.C(p.CProps{Show: true, Msg: "hello"})`)
	assertHTMLEqual(t, got, `<div title="hello">y</div>`)
}

// TestRenderChildComponentProps proves a parent passes props (from attributes)
// to a child component: expr attr, bool attr, and a param used ONLY in a child
// prop expr is bound. The child renders different output for featured true/false.
func TestRenderChildComponentProps(t *testing.T) {
	files := map[string]string{"views.gsx": `package views

component Card(title string, featured bool, count int) {
	<div class={ "card", "card-featured": featured }><h2>{title}</h2><span>{count}</span></div>
}

component Page(t string, n int) {
	<Card title={t} featured count={n}/>
}
`}
	got := renderPackage(t, files, `p.Page(p.PageProps{T: "Hi", N: 3})`)
	assertHTMLEqual(t, got, `<div class="card card-featured"><h2>Hi</h2><span>3</span></div>`)
}

// TestRenderChildComponentPropsFeaturedFalse proves the featured-false branch
// (bool prop omitted ⇒ false ⇒ no card-featured class).
func TestRenderChildComponentPropsFeaturedFalse(t *testing.T) {
	files := map[string]string{"views.gsx": `package views

component Card(title string, featured bool, count int) {
	<div class={ "card", "card-featured": featured }><h2>{title}</h2><span>{count}</span></div>
}

component Page(t string, n int) {
	<Card title={t} count={n}/>
}
`}
	got := renderPackage(t, files, `p.Page(p.PageProps{T: "Yo", N: 9})`)
	assertHTMLEqual(t, got, `<div class="card"><h2>Yo</h2><span>9</span></div>`)
}

// TestRenderChildComponentStaticProp proves a static-attr child prop is quoted
// and passed as a Go string literal.
func TestRenderChildComponentStaticProp(t *testing.T) {
	files := map[string]string{"views.gsx": `package views

component Card(title string, featured bool, count int) {
	<div><h2>{title}</h2><span>{count}</span></div>
}

component Page(n int) {
	<Card title="Hello" count={n}/>
}
`}
	got := renderPackage(t, files, `p.Page(p.PageProps{N: 5})`)
	assertHTMLEqual(t, got, `<div><h2>Hello</h2><span>5</span></div>`)
}

// TestRenderChildComponentNoProps proves the empty-attrs case still works
// (regression guard for Card(CardProps{})).
func TestRenderChildComponentNoProps(t *testing.T) {
	files := map[string]string{"views.gsx": `package views

component Footer() { <footer>x</footer> }

component Page() { <Footer/> }
`}
	got := renderPackage(t, files, `p.Page(p.PageProps{})`)
	assertHTMLEqual(t, got, `<footer>x</footer>`)
}

// TestChildComponentPropPipelineErrors proves a pipeline on a child prop is a
// clean codegen error (deferred).
func TestChildComponentPropPipelineErrors(t *testing.T) {
	files := map[string]string{"views.gsx": `package views

component Card(title string) { <div>{title}</div> }

component Page(t string) { <Card title={t |> upper}/> }
`}
	err := generatePackageErr(t, files)
	if err == nil {
		t.Fatal("expected error for pipeline on child prop, got nil")
	}
	if !strings.Contains(err.Error(), "pipeline") {
		t.Fatalf("expected pipeline error, got: %v", err)
	}
}

// TestChildComponentPropTryErrors proves a `?` try-marker on a child prop is a
// clean codegen error (deferred).
func TestChildComponentPropTryErrors(t *testing.T) {
	files := map[string]string{"views.gsx": `package views

func lookup(s string) (string, error) { return s, nil }

component Card(title string) { <div>{title}</div> }

component Page(t string) { <Card title={lookup(t)?}/> }
`}
	err := generatePackageErr(t, files)
	if err == nil {
		t.Fatal("expected error for try-marker on child prop, got nil")
	}
}

// TestChildComponentHyphenAttrErrors proves a non-identifier (hyphenated) attr
// on a component is a clean codegen error (fallthrough not supported).
func TestChildComponentHyphenAttrErrors(t *testing.T) {
	files := map[string]string{"views.gsx": `package views

component Card(title string) { <div>{title}</div> }

component Page() { <Card data-x="1"/> }
`}
	err := generatePackageErr(t, files)
	if err == nil {
		t.Fatal("expected error for hyphenated attr on component, got nil")
	}
	if !strings.Contains(err.Error(), "non-identifier") {
		t.Fatalf("expected non-identifier error, got: %v", err)
	}
}

// TestChildComponentSpreadErrors proves a spread on a component is a clean
// codegen error (not supported yet).
func TestChildComponentSpreadErrors(t *testing.T) {
	files := map[string]string{"views.gsx": `package views

component Card(title string) { <div>{title}</div> }

component Page(attrs gsx.Attrs) { <Card {...attrs}/> }
`}
	err := generatePackageErr(t, files)
	if err == nil {
		t.Fatal("expected error for spread on component, got nil")
	}
}

// TestChildComponentClassAttrErrors proves a composable class attr on a
// component is a clean codegen error (not supported yet).
func TestChildComponentClassAttrErrors(t *testing.T) {
	files := map[string]string{"views.gsx": `package views

component Card(title string) { <div>{title}</div> }

component Page(on bool) { <Card class={ "a", "b": on }/> }
`}
	err := generatePackageErr(t, files)
	if err == nil {
		t.Fatal("expected error for class attr on component, got nil")
	}
}

// TestRenderChildrenSlot proves a component referencing {children} places the
// parent-supplied slot markup at the {children} position, composed with a prop.
func TestRenderChildrenSlot(t *testing.T) {
	files := map[string]string{"views.gsx": `package views

component Card(title string) {
	<section><h2>{title}</h2>{children}</section>
}

component Page() {
	<Card title="Hi"><p>body</p></Card>
}
`}
	got := renderPackage(t, files, `p.Page(p.PageProps{})`)
	assertHTMLEqual(t, got, `<section><h2>Hi</h2><p>body</p></section>`)
}

// TestRenderChildrenSlotEmpty proves a {children}-referencing component rendered
// with NO children passed renders nothing for the slot (nil gsx.Node is a
// render no-op — gw.Node tolerates nil).
func TestRenderChildrenSlotEmpty(t *testing.T) {
	files := map[string]string{"views.gsx": `package views

component Card(title string) {
	<section><h2>{title}</h2>{children}</section>
}

component Page() {
	<Card title="Hi"/>
}
`}
	got := renderPackage(t, files, `p.Page(p.PageProps{})`)
	assertHTMLEqual(t, got, `<section><h2>Hi</h2></section>`)
}

// TestRenderChildrenSlotBinding proves slot content renders in the PARENT scope:
// it references a parent param and a parent loop var, both bound in the parent's
// render closure (not the child's).
func TestRenderChildrenSlotBinding(t *testing.T) {
	files := map[string]string{"views.gsx": `package views

component Wrap() {
	<div>{children}</div>
}

component Page(name string, items []string) {
	<Wrap>
		<span>{name}</span>
		{ for _, it := range items { <li>{it}</li> } }
	</Wrap>
}
`}
	got := renderPackage(t, files, `p.Page(p.PageProps{Name: "Jo", Items: []string{"a", "b"}})`)
	assertHTMLEqual(t, got, `<div><span>Jo</span><li>a</li><li>b</li></div>`)
}

// TestRenderChildrenSlotInterleaved is the load-bearing order-invariant test: a
// typed interp BEFORE a child component, the child's slot containing a typed
// interp of a DIFFERENT type, and a typed interp AFTER. Each must render with its
// own type (k-th _gsxuse maps to k-th collectExprs node).
func TestRenderChildrenSlotInterleaved(t *testing.T) {
	files := map[string]string{"views.gsx": `package views

component Wrap() {
	<div>{children}</div>
}

component Page(before string, mid int, after bool) {
	<span>{before}</span>
	<Wrap><em>{mid}</em></Wrap>
	<span>{after}</span>
}
`}
	got := renderPackage(t, files, `p.Page(p.PageProps{Before: "B", Mid: 7, After: true})`)
	assertHTMLEqual(t, got, `<span>B</span><div><em>7</em></div><span>true</span>`)
}

// TestReservedParamChildren proves a param named `children` is rejected: Task 2
// synthesizes a Children field + children local, so a user param would collide.
func TestReservedParamChildren(t *testing.T) {
	files := map[string]string{"views.gsx": `package views

import "github.com/gsxhq/gsx"

component X(children gsx.Node) { <div>{children}</div> }
`}
	err := generatePackageErr(t, files)
	if err == nil {
		t.Fatal("expected error for param named children, got nil")
	}
	if !strings.Contains(err.Error(), "children") || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("expected reserved-children error, got: %v", err)
	}
}

// TestRenderMethodInvocationChain is the example-11-style chain: a method
// component invokes another method via the enclosing receiver var (`p`), through
// several levels (Page → Content → Grid → Row, with a {for} loop in Grid). Each
// `<p.X.../>` lowers to a method call `p.X(...)`, NOT a package-function call.
func TestRenderMethodInvocationChain(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

type User struct {
	Email string
}

type UsersPage struct {
	Title string
	Sort  string
	Users []User
}

component (p UsersPage) Page() {
	<div><p.Content/></div>
}

component (p UsersPage) Content() {
	<h1>{p.Title}</h1><p.Grid sort={p.Sort}/>
}

component (p UsersPage) Grid(sort string) {
	<ul>{ for _, u := range p.Users { <p.Row user={u} sort={sort}/> } }</ul>
}

component (p UsersPage) Row(user User, sort string) {
	<li>{user.Email}-{sort}</li>
}
`,
	}
	got := renderPackage(t, files,
		`(p.UsersPage{Title: "T", Sort: "s", Users: []p.User{{Email: "a@b"}, {Email: "c@d"}}}).Page()`)
	assertHTMLEqual(t, got,
		`<div><h1>T</h1><ul><li>a@b-s</li><li>c@d-s</li></ul></div>`)
}

// TestRenderMethodInvocationNullary proves a nullary method invocation
// `<p.Content/>` (no attrs, no children) lowers to `p.Content()` with NO props
// literal (the nullary method has no props struct).
func TestRenderMethodInvocationNullary(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

type UsersPage struct {
	Title string
}

component (p UsersPage) Page() {
	<div><p.Content/></div>
}

component (p UsersPage) Content() {
	<h1>{p.Title}</h1>
}
`,
	}
	got := renderPackage(t, files, `(p.UsersPage{Title: "Hi"}).Page()`)
	assertHTMLEqual(t, got, `<div><h1>Hi</h1></div>`)
}

// TestRenderMethodInvocationParam proves a parameterized method invocation
// `<p.Grid sort={x}/>` lowers to `p.Grid(UsersPageGridProps{Sort: x})` — props
// type named after the ENCLOSING receiver's TYPE, not `p.GridProps`.
func TestRenderMethodInvocationParam(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

type UsersPage struct {
	Sort string
}

component (p UsersPage) Page() {
	<div><p.Grid sort={p.Sort}/></div>
}

component (p UsersPage) Grid(sort string) {
	<span>{sort}</span>
}
`,
	}
	got := renderPackage(t, files, `(p.UsersPage{Sort: "name"}).Page()`)
	assertHTMLEqual(t, got, `<div><span>name</span></div>`)
}

// TestRenderMethodInvocationLoopVarBinding proves a loop var used ONLY in a
// method-invocation prop (`<p.Row user={u}/>` inside `{ for _, u := range ... }`)
// is bound — collectChildPropExprSrc walks the method-tag attrs too.
func TestRenderMethodInvocationLoopVarBinding(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

type User struct {
	Email string
}

type UsersPage struct {
	Users []User
}

component (p UsersPage) Page() {
	<ul>{ for _, u := range p.Users { <p.Row user={u}/> } }</ul>
}

component (p UsersPage) Row(user User) {
	<li>{user.Email}</li>
}
`,
	}
	got := renderPackage(t, files,
		`(p.UsersPage{Users: []p.User{{Email: "x@y"}}}).Page()`)
	assertHTMLEqual(t, got, `<ul><li>x@y</li></ul>`)
}

// TestRenderMethodAndFunctionMixed proves the disambiguation rule: in a method
// component body, a dotted tag whose left == receiver var (`<p.Content/>`) lowers
// to a METHOD call, while an uppercase non-dotted same-file FUNCTION component
// (`<Card.../>`) lowers to a package-function call `Card(CardProps{...})`.
func TestRenderMethodAndFunctionMixed(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

type UsersPage struct {
	Title string
}

component Card(label string) {
	<div class="card">{label}</div>
}

component (p UsersPage) Page() {
	<section><p.Content/><Card label="hello"/></section>
}

component (p UsersPage) Content() {
	<h1>{p.Title}</h1>
}
`,
	}
	got := renderPackage(t, files, `(p.UsersPage{Title: "T"}).Page()`)
	assertHTMLEqual(t, got,
		`<section><h1>T</h1><div class="card">hello</div></section>`)
}

// TestRenderMethodInvocationSlotInterleaved is the order-invariant test for
// method invocations with slot content: a method component (`Wrap`) that places
// `{children}` is invoked via the receiver (`<p.Wrap>{mid}</p.Wrap>`), interleaved
// with sibling typed interps. Each renders with its own type (the k-th _gsxuse
// maps to the k-th collectExprs node, even through the method-tag slot recursion).
func TestRenderMethodInvocationSlotInterleaved(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

type UsersPage struct {
	Before string
	Mid    int
	After  bool
}

component (p UsersPage) Wrap() {
	<div>{children}</div>
}

component (p UsersPage) Page() {
	<span>{p.Before}</span>
	<p.Wrap><em>{p.Mid}</em></p.Wrap>
	<span>{p.After}</span>
}
`,
	}
	got := renderPackage(t, files,
		`(p.UsersPage{Before: "B", Mid: 7, After: true}).Page()`)
	assertHTMLEqual(t, got,
		`<span>B</span><div><em>7</em></div><span>true</span>`)
}
