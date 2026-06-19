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
