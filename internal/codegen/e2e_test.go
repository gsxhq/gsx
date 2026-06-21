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
