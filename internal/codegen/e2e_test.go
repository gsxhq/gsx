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

// --- fallthrough (Task 15 migrated to corpus: testdata/cases/fallthrough/) ---
// The following two tests were not included in the Task 15 migration because they
// cover conditional-attr root eligibility, which is a separate concern.

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
