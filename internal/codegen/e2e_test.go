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

// TestRenderMethodSameNameDifferentReceivers proves type resolution keys on
// receiver-type + method name, not name alone: two method components named `Row`
// on different receivers (with differently-typed interps) each resolve correctly.
// Regression for the harvest byName collision (found by independent review).
func TestRenderMethodSameNameDifferentReceivers(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

type A struct{ S string }
type B struct{ N int }

component (a A) Row() { <i>{a.S}</i> }
component (b B) Row() { <b>{b.N}</b> }
`,
	}
	got := renderPackage(t, files, `(p.A{S: "hi"}).Row()`)
	assertHTMLEqual(t, got, `<i>hi</i>`)
	got = renderPackage(t, files, `(p.B{N: 7}).Row()`)
	assertHTMLEqual(t, got, `<b>7</b>`)
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

// --- Task 1: child auto-fallthrough (Attrs field + root class-merge/spread) ---

// TestFallthroughComposedClassMerge: a single-root component whose root has a
// composed class merges the bag's class and spreads the bag's other attrs.
func TestFallthroughComposedClassMerge(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

component Box(variant string) {
	<div class={ "box", variant }>{children}</div>
}
`,
	}
	got := renderPackage(t, files,
		`p.Box(p.BoxProps{Variant: "big", Attrs: gsx.Attrs{"class": "w-full", "data-test": "x"}})`)
	assertHTMLEqual(t, got, `<div class="box big w-full" data-test="x"></div>`)
}

// TestFallthroughStaticClassMerge: a static class root merges the bag class and
// spreads the rest.
func TestFallthroughStaticClassMerge(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

component Card() {
	<section class="card">{children}</section>
}
`,
	}
	got := renderPackage(t, files,
		`p.Card(p.CardProps{Attrs: gsx.Attrs{"class": "hl", "id": "a"}})`)
	assertHTMLEqual(t, got, `<section class="card hl" id="a"></section>`)
}

// TestFallthroughNoClassRootWithBag: a no-class root gains a class from the bag.
func TestFallthroughNoClassRootWithBag(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

component P() {
	<p>{children}</p>
}
`,
	}
	got := renderPackage(t, files, `p.P(p.PProps{Attrs: gsx.Attrs{"class": "x"}})`)
	assertHTMLEqual(t, got, `<p class="x"></p>`)
}

// TestFallthroughNoClassEmptyBag: a no-class root with an EMPTY bag emits no
// class attribute (the ClassMerged empty-class guard).
func TestFallthroughNoClassEmptyBag(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

component P() {
	<p>{children}</p>
}
`,
	}
	got := renderPackage(t, files, `p.P(p.PProps{})`)
	if strings.Contains(got, "class=") {
		t.Fatalf("empty bag must not emit a class attribute, got: %q", got)
	}
	assertHTMLEqual(t, got, `<p></p>`)
}

// TestFallthroughRootWins: the root's own attr (href) wins; the bag's same-named
// entry is dropped (via Without), while a new bag attr spreads.
func TestFallthroughRootWins(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

component Link() {
	<a href="/x">{children}</a>
}
`,
	}
	got := renderPackage(t, files,
		`p.Link(p.LinkProps{Attrs: gsx.Attrs{"href": "/evil", "data-y": "1"}})`)
	assertHTMLEqual(t, got, `<a href="/x" data-y="1"></a>`)
}

// TestFallthroughEmptyBagNoop: a nil Attrs bag on a single-root component renders
// identically to having no bag — every merge/spread is a no-op.
func TestFallthroughEmptyBagNoop(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

component Box(variant string) {
	<div class={ "box", variant }>{children}</div>
}
`,
	}
	got := renderPackage(t, files, `p.Box(p.BoxProps{Variant: "big"})`)
	assertHTMLEqual(t, got, `<div class="box big"></div>`)
}

// TestFallthroughNotEligibleNoField: a fragment-root (multi-root) component is
// NOT single-root, so it has no synthesized Attrs field — assigning Attrs in the
// props literal is an unknown-field compile error surfaced by the generated code.
func TestFallthroughNotEligibleNoField(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

component Multi() {
	<p>a</p>
	<p>b</p>
}
`,
	}
	// GeneratePackage itself succeeds (no Attrs field is fine); the error only
	// surfaces when a caller tries to set Attrs. Use renderPackage so the harness
	// compiles the invocation against the generated props struct.
	if testing.Short() {
		t.Skip("skipping go-run render test in -short mode")
	}
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gsxnf\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(tmp, "genpkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for n, c := range files {
		writeFile(t, pkgDir, n, c)
	}
	gen, err := GeneratePackage(pkgDir)
	if err != nil {
		t.Fatalf("GeneratePackage: %v", err)
	}
	for gsxPath, src := range gen {
		base := strings.TrimSuffix(filepath.Base(gsxPath), ".gsx")
		s := string(src)
		if strings.Contains(s, "Attrs gsx.Attrs") {
			t.Fatalf("multi-root component must NOT synthesize an Attrs field, got:\n%s", s)
		}
		writeFile(t, pkgDir, base+".x.go", s)
	}
	writeFile(t, tmp, "main.go", `package main

import (
	"context"
	"os"

	"github.com/gsxhq/gsx"
	p "gsxnf/genpkg"
)

var _ = gsx.Raw

func main() {
	_ = p.Multi(p.MultiProps{Attrs: gsx.Attrs{"data-x": "1"}}).Render(context.Background(), os.Stdout)
}
`)
	cmd := exec.Command("go", "build", ".")
	cmd.Dir = tmp
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected compile error assigning Attrs to a non-eligible component, got success")
	}
	if !strings.Contains(string(out), "Attrs") {
		t.Fatalf("expected unknown-field Attrs error, got:\n%s", out)
	}
}

// TestReservedParamAttrs: a param named `attrs` is rejected (it is now
// synthesized), like `children`.
func TestReservedParamAttrs(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

import "github.com/gsxhq/gsx"

component X(attrs gsx.Attrs) {
	<p>hi</p>
}
`,
	}
	err := generatePackageErr(t, files)
	if err == nil {
		t.Fatal("expected error for param named attrs, got nil")
	}
	if !strings.Contains(err.Error(), "reserved") || !strings.Contains(err.Error(), "attrs") {
		t.Fatalf("expected clean reserved-name error mentioning attrs, got: %v", err)
	}
}

// example-12: call-site attribute split. A child invocation's attrs are split:
// declared props become props-struct fields; everything else (non-identifier
// names, undeclared identifiers) falls through into an Attrs gsx.Attrs{} bag,
// which the child's single-root element merges (class) / spreads.

// TestCallSiteFallthroughButton is the headline example-12: a Button child with a
// declared `variant` prop, a static class on its root, and data-test/hx-post
// fallthrough attrs. The variant drives the root class; the bag's class merges; the
// fallthrough attrs spread onto the root.
func TestCallSiteFallthroughButton(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

component Button(variant string) {
	<button class="btn" data-variant={variant}>{children}</button>
}

component Page() {
	<Button variant="primary" class="w-full" data-test="x" hx-post="/go">Save</Button>
}
`,
	}
	got := renderPackage(t, files, `p.Page(p.PageProps{})`)
	assertHTMLEqual(t, got,
		`<button class="btn w-full" data-variant="primary" data-test="x" hx-post="/go">Save</button>`)
}

// TestCallSiteDeclaredVsUndeclaredIdentifier: a declared identifier attr (`variant`)
// becomes a prop field; an UNDECLARED identifier attr (`role`) on a same-package
// child whose prop fields are known falls through into the bag (spread onto root).
func TestCallSiteDeclaredVsUndeclaredIdentifier(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

component Button(variant string) {
	<button data-variant={variant}>{children}</button>
}

component Page() {
	<Button variant="primary" role="button">Go</Button>
}
`,
	}
	got := renderPackage(t, files, `p.Page(p.PageProps{})`)
	assertHTMLEqual(t, got, `<button data-variant="primary" role="button">Go</button>`)
}

// TestCallSiteNonIdentifierFallthrough: a non-identifier attr name (data-test, @click,
// hx-post) can never be a prop field — it always falls through into the bag.
func TestCallSiteNonIdentifierFallthrough(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

component Button() {
	<button>{children}</button>
}

component Page() {
	<Button data-test="t" hx-post="/x">Go</Button>
}
`,
	}
	got := renderPackage(t, files, `p.Page(p.PageProps{})`)
	assertHTMLEqual(t, got, `<button data-test="t" hx-post="/x">Go</button>`)
}

// TestCallSiteClassMerge: a declared prop drives a composed root class, and the
// caller's fallthrough class MERGES into it: btn + primary (from variant) + w-full.
func TestCallSiteClassMerge(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

component Button(variant string) {
	<button class={ "btn", variant }>{children}</button>
}

component Page() {
	<Button variant="primary" class="w-full">Go</Button>
}
`,
	}
	got := renderPackage(t, files, `p.Page(p.PageProps{})`)
	assertHTMLEqual(t, got, `<button class="btn primary w-full">Go</button>`)
}

// TestCallSiteRootWins: when both the child's root and the caller's fallthrough set
// the same attr (`type`), the ROOT wins — the bag's `type="reset"` is dropped in
// favor of the root's `type="button"`.
func TestCallSiteRootWins(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

component Button() {
	<button type="button">{children}</button>
}

component Page() {
	<Button type="reset">Go</Button>
}
`,
	}
	got := renderPackage(t, files, `p.Page(p.PageProps{})`)
	assertHTMLEqual(t, got, `<button type="button">Go</button>`)
}

// TestCallSiteFallthroughNotEligible: a fallthrough attr on a MULTI-ROOT (not
// single-root) child has no Attrs field to receive it → the generated props literal
// assigns an unknown field → a `go build` compile error on the .x.go.
func TestCallSiteFallthroughNotEligible(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping go-run render test in -short mode")
	}
	files := map[string]string{
		"views.gsx": `package views

component Multi() {
	<p>a</p>
	<p>b</p>
}

component Page() {
	<Multi data-x="1"/>
}
`,
	}
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gsxcs\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(tmp, "genpkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for n, c := range files {
		writeFile(t, pkgDir, n, c)
	}
	gen, err := GeneratePackage(pkgDir)
	// The split happens at generate time; the probe also splits, so the probe's
	// `Multi(MultiProps{Attrs: ...})` references a non-existent field → resolution
	// (type-check) fails inside GeneratePackage.
	if err != nil {
		if !strings.Contains(err.Error(), "Attrs") {
			t.Fatalf("expected an Attrs-field resolution error, got: %v", err)
		}
		return
	}
	// If resolution somehow passed, the emitted .x.go must still fail to build.
	for gsxPath, src := range gen {
		base := strings.TrimSuffix(filepath.Base(gsxPath), ".gsx")
		writeFile(t, pkgDir, base+".x.go", string(src))
	}
	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = tmp
	out, berr := cmd.CombinedOutput()
	if berr == nil {
		t.Fatalf("expected compile error for fallthrough onto a non-eligible child, got success")
	}
	if !strings.Contains(string(out), "Attrs") {
		t.Fatalf("expected unknown-field Attrs error, got:\n%s", out)
	}
}

// TestFallthroughCondAttrRootNotEligible: a single-root component whose root sets an
// attribute CONDITIONALLY (`{ if … { id=… } }`) is NOT fallthrough-eligible — its
// runtime-named attrs can't be statically de-duped, so a colliding fallthrough would
// emit a duplicate attribute. It must fail closed (no Attrs field → unknown-field
// error), not silently emit `id="real" id="caller"`. (Independent-review finding.)
func TestFallthroughCondAttrRootNotEligible(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

component Box(active bool) {
	<button { if active { id="real" } }>x</button>
}

component Page() {
	<Box active={true} id="caller"/>
}
`,
	}
	if err := generatePackageErr(t, files); err == nil || !strings.Contains(err.Error(), "Attrs") {
		t.Fatalf("expected an Attrs unknown-field error for fallthrough onto a CondAttr-root child, got: %v", err)
	}
}

// TestFallthroughCondAttrRootStandalone: a CondAttr-root component still renders fine
// on its own (it is simply not fallthrough-eligible — no regression to its rendering).
func TestFallthroughCondAttrRootStandalone(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

component Box(active bool) {
	<button { if active { id="real" } }>x</button>
}
`,
	}
	got := renderPackage(t, files, `p.Box(p.BoxProps{Active: true})`)
	assertHTMLEqual(t, got, `<button id="real">x</button>`)
}

// TestCallSiteNoFallthroughUnchanged: a child invocation with ONLY declared props and
// no fallthrough produces a props literal with NO Attrs field (unchanged behavior).
func TestCallSiteNoFallthroughUnchanged(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

component Card(title string) {
	<div>{title}</div>
}

component Page() {
	<Card title="Hi"/>
}
`,
	}
	got := renderPackage(t, files, `p.Page(p.PageProps{})`)
	assertHTMLEqual(t, got, `<div>Hi</div>`)

	// Assert the generated props literal carries no Attrs field.
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gsxnofall\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(tmp, "genpkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for n, c := range files {
		writeFile(t, pkgDir, n, c)
	}
	gen, err := GeneratePackage(pkgDir)
	if err != nil {
		t.Fatalf("GeneratePackage: %v", err)
	}
	for _, src := range gen {
		if strings.Contains(string(src), "Card(CardProps{Title:") && strings.Contains(string(src), "Attrs:") {
			t.Fatalf("no-fallthrough Card invocation must not carry an Attrs field:\n%s", src)
		}
	}
}

// TestCallSiteMethodFallthrough: a method invocation `<p.Btn .../>` with a fallthrough
// attr splits identically — the declared prop is a field, the data-* attr falls into
// the method's props Attrs bag.
func TestCallSiteMethodFallthrough(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

type Pg struct{}

component (p Pg) Btn(variant string) {
	<button data-variant={variant}>{children}</button>
}

component (p Pg) Page() {
	<p.Btn variant="primary" data-test="x">Go</p.Btn>
}
`,
	}
	got := renderPackage(t, files, `(p.Pg{}).Page()`)
	assertHTMLEqual(t, got, `<button data-variant="primary" data-test="x">Go</button>`)
}

// --- Task 4: manual-mode fallthrough ({...attrs}) ---

// TestManualFallthroughPlacement is the headline manual-mode case: a component whose
// body references `attrs` (via a `{...attrs}` spread) takes over placement. The
// fallthrough attrs land on the INNER `<input>` (where {...attrs} sits), NOT on the
// multi-level `<div class="wrap">` root — auto root-injection is disabled. The
// declared `id` stays a prop; data-test/placeholder fall through into the bag.
func TestManualFallthroughPlacement(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

component Field(id string) {
	<div class="wrap"><input id={id} {...attrs}/></div>
}

component Page() {
	<Field id="email" data-test="x" placeholder="you@co"/>
}
`,
	}
	got := renderPackage(t, files, `p.Page(p.PageProps{})`)
	assertHTMLEqual(t, got,
		`<div class="wrap"><input id="email" data-test="x" placeholder="you@co"/></div>`)
}

// TestManualFallthroughDisablesAuto proves manual mode disables auto root injection:
// the fallthrough attrs appear ONCE, on the inner <input> (where {...attrs} is), and
// are NOT also auto-applied to the <div class="wrap"> root. Were auto still active,
// the root <div> would carry data-test/placeholder too.
func TestManualFallthroughDisablesAuto(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

component Field(id string) {
	<div class="wrap"><input id={id} {...attrs}/></div>
}

component Page() {
	<Field id="email" data-test="x" placeholder="you@co"/>
}
`,
	}
	got := renderPackage(t, files, `p.Page(p.PageProps{})`)
	// The root <div> must carry only its own class="wrap" — no fallthrough attrs.
	if strings.Contains(got, `<div class="wrap" data-test`) ||
		strings.Contains(got, `<div class="wrap" placeholder`) ||
		strings.Contains(got, `data-test="x"></div>`) {
		t.Fatalf("manual mode must not auto-apply fallthrough attrs to the root <div>:\n%s", got)
	}
	// Exactly one occurrence each (on the <input>).
	if n := strings.Count(got, `data-test="x"`); n != 1 {
		t.Fatalf("expected data-test once, got %d:\n%s", n, got)
	}
	if n := strings.Count(got, `placeholder="you@co"`); n != 1 {
		t.Fatalf("expected placeholder once, got %d:\n%s", n, got)
	}
}

// TestManualFallthroughWithout proves the bound `attrs` local is a real gsx.Attrs:
// `{...attrs.Without("id")}` compiles and the runtime Without call drops `id` from
// the spread (here the caller passes no id, so it is a no-op on output, but the
// method call must type-check and run). placeholder still falls through.
func TestManualFallthroughWithout(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

component Panel(title string) {
	<section><h2>{title}</h2><aside {...attrs.Without("title")}>body</aside></section>
}

component Page() {
	<Panel title="Hi" data-test="x" placeholder="p"/>
}
`,
	}
	got := renderPackage(t, files, `p.Page(p.PageProps{})`)
	assertHTMLEqual(t, got,
		`<section><h2>Hi</h2><aside data-test="x" placeholder="p">body</aside></section>`)
}

// TestManualFallthroughNullaryMethod proves manual mode forces the Attrs field even
// on a nullary method component (which normally stays props-less). Referencing
// `attrs` makes it fallthrough-eligible, so it gets a props struct + Attrs field and
// is invoked as a function-style child with the bag.
func TestManualFallthroughNullaryMethod(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

type Pg struct{}

component (p Pg) Wrap() {
	<div class="outer"><span {...attrs}/></div>
}

component (p Pg) Page() {
	<p.Wrap data-test="x"/>
}
`,
	}
	got := renderPackage(t, files, `(p.Pg{}).Page()`)
	assertHTMLEqual(t, got, `<div class="outer"><span data-test="x"></span></div>`)
}

// TestManualAutoStillWorks (regression): a single-root component with NO `attrs`
// reference still auto-applies the fallthrough bag at its root (manual must not
// regress auto). Mirrors the Task-1 auto path.
func TestManualAutoStillWorks(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

component Box(variant string) {
	<div class={ "box", variant }>{children}</div>
}
`,
	}
	got := renderPackage(t, files,
		`p.Box(p.BoxProps{Variant: "big", Attrs: gsx.Attrs{"class": "w-full", "data-test": "x"}})`)
	assertHTMLEqual(t, got, `<div class="box big w-full" data-test="x"></div>`)
}

// TestExample12EndToEnd is the end-to-end golden for examples/12_children_attrs.gsx:
// it reproduces, in same-package renders through the harness, the example's
// load-bearing patterns combined:
//   - AUTO fallthrough + class-merge + {children} (Toolbar -> Button): the caller's
//     class merges into the composed root class, data-*/hx-*/@click fall through onto
//     the single root <button>, and {children} places the slot content.
//   - MANUAL fallthrough placement (LoginForm -> Field): the multi-level Field roots
//     at <div class="field"> but places the caller's name/required/hx-get on the inner
//     <input> via {...attrs} — auto root injection disabled.
//
// Example 12 itself stays PARSE-ONLY (its LabeledInput uses a `{{ rest :=
// attrs.Without("class") }}` GoBlock whose declared var the emitter does not yet
// track as used — a pre-existing GoBlock-local limitation, orthogonal to fallthrough);
// this test graduates its auto+manual core to a render golden.
func TestExample12EndToEnd(t *testing.T) {
	files := map[string]string{
		"views.gsx": `package views

component Button(variant string) {
	<button type="button" class={ "btn", variantClass(variant) }>{children}</button>
}

component Toolbar() {
	<div><Button variant="primary" class="w-full" data-test="save" hx-post="/save" @click="go()">Save</Button></div>
}

component Field(label string) {
	<div class="field"><label>{label}</label><input class="control" {...attrs}/></div>
}

component LoginForm() {
	<form><Field label="Email" name="email" required hx-get="/check-email"/></form>
}

func variantClass(v string) string { return "btn-" + v }
`,
	}
	// Toolbar: auto path. class merges (btn + btn-primary + w-full); data-test/hx-post/
	// @click spread onto the single root <button>; root's own type wins; children place.
	gotToolbar := renderPackage(t, files, `p.Toolbar(p.ToolbarProps{})`)
	assertHTMLEqual(t, gotToolbar,
		`<div><button type="button" class="btn btn-primary w-full" data-test="save" hx-post="/save" @click="go()">Save</button></div>`)

	// LoginForm: manual path. name/required/hx-get land on the inner <input> (where
	// {...attrs} sits), NOT on the <div class="field"> root.
	gotForm := renderPackage(t, files, `p.LoginForm(p.LoginFormProps{})`)
	assertHTMLEqual(t, gotForm,
		`<form><div class="field"><label>Email</label><input class="control" name="email" required hx-get="/check-email"/></div></form>`)
}
