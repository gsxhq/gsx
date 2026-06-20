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

// TestMethodOnlyFileCleanError proves a .gsx whose only component is a method
// component yields the clean "method components not supported yet" error rather
// than an "imported and not used" error masking it (the skeleton imports _gsxrt
// but, with no rendered components, must still reference it via var _).
func TestMethodOnlyFileCleanError(t *testing.T) {
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
	if err == nil {
		t.Fatal("expected error for method component, got nil")
	}
	if strings.Contains(err.Error(), "imported") && strings.Contains(err.Error(), "not used") {
		t.Fatalf("import-unused error masked the real diagnostic: %v", err)
	}
	if !strings.Contains(err.Error(), "method components not supported") {
		t.Fatalf("expected method components not supported error, got: %v", err)
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
