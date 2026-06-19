package codegen

import (
	"fmt"
	"go/token"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/gsxhq/gsx/parser"
)

// packageClause matches the `package <name>` clause of the generated source so
// we can rewrite it to `package main` for the temp render module.
var packageClause = regexp.MustCompile(`(?m)^package\s+\w+`)

// renderGSX parses gsxSrc, runs Generate, assembles a throwaway Go module that
// renders the component, runs it, and returns the rendered HTML.
//
// invocation is the Go expression that builds and is rendered, e.g.
// `Greeting(GreetingProps{Name: "World", Count: 3})`. The harness wraps it as
// `_ = <invocation>.Render(context.Background(), os.Stdout)`.
//
// Any failure (parse, generate, go run) fails the test, including the generated
// source and the go tool output for debuggability.
func renderGSX(t *testing.T, gsxSrc, invocation string) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping go-run render test in -short mode")
	}

	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}

	file, err := parser.ParseFile(token.NewFileSet(), "component.gsx", gsxSrc, 0)
	if err != nil {
		t.Fatalf("parse:\n%s\nerror: %v", gsxSrc, err)
	}
	out, err := Generate(file)
	if err != nil {
		t.Fatalf("generate:\n%s\nerror: %v", gsxSrc, err)
	}
	gen := string(out)
	genMain := packageClause.ReplaceAllString(gen, "package main")

	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module gsxrender\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	writeFile(t, dir, "component.go", genMain)
	writeFile(t, dir, "main.go", fmt.Sprintf(`package main

import (
	"context"
	"os"
)

func main() {
	_ = %s.Render(context.Background(), os.Stdout)
}
`, invocation))

	cmd := exec.Command("go", "run", ".")
	cmd.Dir = dir
	rendered, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run failed: %v\n--- generated ---\n%s\n--- go output ---\n%s", err, genMain, rendered)
	}
	return string(rendered)
}

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
	src := `package examples

type User struct {
	Name string
	Age  int
}

component Profile(user User) {
	<p>{user.Name} is {user.Age}</p>
}
`
	got := renderGSX(t, src, `Profile(ProfileProps{User: User{Name: "Alice", Age: 30}})`)
	assertHTMLEqual(t, got, "<p>Alice is 30</p>")
}
