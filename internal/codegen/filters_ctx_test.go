package codegen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// goRun runs `go run .` in dir and returns combined output, failing on error.
func goRun(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("go", "run", ".")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run failed: %v\n%s", err, out)
	}
	return string(out)
}

// TestWithFilterCtxInjectionAndErrorUnwrap is the end-to-end proof of the
// seed-first ctx-injecting alias path. It lays out a temp module with a filter
// package whose func is
//
//	func F(ctx context.Context, v string, k string) (string, error)
//
// registered under the template name "f" via a codegen.FilterAlias, then asserts:
//
//   - the generated .x.go lowers `s |> f("k")` to `…F(ctx, (s), "k")` — ctx is
//     injected as the first arg, the subject is parenthesized in second position,
//     and the explicit stage arg follows;
//   - the (string, error) result is unwrapped by gsx's implicit error handling
//     (the render produces the value, no second return leaks);
//   - emit ≡ probe: the SAME lowered call type-checks (probe) and renders (emit),
//     which is guaranteed because generation would fail if they diverged.
func TestWithFilterCtxInjectionAndErrorUnwrap(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping go-run render test in -short mode")
	}
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	writeMultiFile(t, tmp, "go.mod", "module gsxmf\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")

	mfDir := filepath.Join(tmp, "myfilters")
	if err := os.MkdirAll(mfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// F takes the ambient ctx first, the subject second, and an explicit arg
	// third, and returns (string, error). The body reads a value off ctx so the
	// injected ctx is observably used (not a placeholder).
	writeMultiFile(t, mfDir, "myfilters.go", `package myfilters

import "context"

type ctxKey struct{}

func WithGreeting(ctx context.Context, g string) context.Context {
	return context.WithValue(ctx, ctxKey{}, g)
}

// F is a seed-first, ctx-injecting, error-returning filter.
func F(ctx context.Context, v string, k string) (string, error) {
	g, _ := ctx.Value(ctxKey{}).(string)
	return g + ":" + v + ":" + k, nil
}
`)

	viewsDir := filepath.Join(tmp, "views")
	if err := os.MkdirAll(viewsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMultiFile(t, viewsDir, "views.gsx", `package views

component C(s string) { <p>{ s |> f("k") }</p> }
`)

	aliases := []FilterAlias{{Name: "f", PkgPath: "gsxmf/myfilters", FuncName: "F"}}
	genRes, err := GenerateDirs(tmp, []string{viewsDir}, GenOptions{FilterPkgs: []string{stdImportPath}, Aliases: aliases, CSSMinify: true, JSMinify: true}, nil)
	if err != nil {
		t.Fatalf("GenerateDirs: %v", err)
	}
	dr := genRes[viewsDir]
	if hasDiagErrors(dr.Diags) {
		t.Fatalf("GenerateDirs: unexpected errors: %v", dr.Diags)
	}
	var genSrc string
	for gsxPath, src := range dr.Files {
		base := strings.TrimSuffix(filepath.Base(gsxPath), ".gsx")
		writeMultiFile(t, viewsDir, base+".x.go", string(src))
		genSrc += string(src)
	}

	// Assert the lowered call shape: F(ctx, (s), "k"). The alias's package gets a
	// reserved _gsxf<i> import alias; assert on the call tail to stay alias-stable.
	if !strings.Contains(genSrc, ".F(ctx, (s), \"k\")") {
		t.Fatalf("generated .x.go missing seed-first ctx-injected call F(ctx, (s), \"k\"); got:\n%s", genSrc)
	}

	// Render through a ctx carrying a greeting, proving (a) ctx injection passes
	// the ambient render ctx, and (b) the (string, error) is unwrapped.
	writeMultiFile(t, tmp, "main.go", `package main

import (
	"context"
	"os"

	"github.com/gsxhq/gsx"
	mf "gsxmf/myfilters"
	p "gsxmf/views"
)

var _ = gsx.Raw

func main() {
	ctx := mf.WithGreeting(context.Background(), "hi")
	_ = p.C(p.CProps{S: "v"}).Render(ctx, os.Stdout)
}
`)
	out := goRun(t, tmp)
	if !strings.Contains(out, "hi:v:k") {
		t.Fatalf("expected rendered \"hi:v:k\" (ctx-injected + error-unwrapped); got:\n%s", out)
	}
}

// TestWithFilterPackageAlsoImportedDirectly proves the import-collision fix: a
// filter package that the .gsx ALSO plain-imports and references by its own name
// (e.g. structpages.RenderComponent alongside `|> url`) generates a file that
// imports the package BOTH under its reserved filter alias (for the lowered
// filter calls) AND plainly (for the user's direct `<pkg>.X` references). Before
// the fix, only the reserved-alias import was emitted and the direct reference
// was `undefined: <pkg>`.
func TestWithFilterPackageAlsoImportedDirectly(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping go-run render test in -short mode")
	}
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	writeMultiFile(t, tmp, "go.mod", "module gsxmf\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")

	mfDir := filepath.Join(tmp, "myfilters")
	if err := os.MkdirAll(mfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Upper2 is a seed-first filter; Helper is a NON-filter export the view calls
	// directly by name — the dual role that triggered the collision.
	writeMultiFile(t, mfDir, "myfilters.go", `package myfilters

import "strings"

func Upper2(s string) string { return strings.ToUpper(s) }

func Helper() string { return "H!" }
`)

	viewsDir := filepath.Join(tmp, "views")
	if err := os.MkdirAll(viewsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The view plain-imports the filter package AND uses both a filter from it
	// (`|> up`) and a non-filter export by name (`myfilters.Helper()`).
	writeMultiFile(t, viewsDir, "views.gsx", `package views

import "gsxmf/myfilters"

component C(s string) { <p>{ s |> up }{ myfilters.Helper() }</p> }
`)

	aliases := []FilterAlias{{Name: "up", PkgPath: "gsxmf/myfilters", FuncName: "Upper2"}}
	genRes2, err := GenerateDirs(tmp, []string{viewsDir}, GenOptions{FilterPkgs: []string{stdImportPath}, Aliases: aliases, CSSMinify: true, JSMinify: true}, nil)
	if err != nil {
		t.Fatalf("GenerateDirs: %v", err)
	}
	dr2 := genRes2[viewsDir]
	if hasDiagErrors(dr2.Diags) {
		t.Fatalf("GenerateDirs: unexpected errors: %v", dr2.Diags)
	}
	for gsxPath, src := range dr2.Files {
		base := strings.TrimSuffix(filepath.Base(gsxPath), ".gsx")
		writeMultiFile(t, viewsDir, base+".x.go", string(src))
	}

	writeMultiFile(t, tmp, "main.go", `package main

import (
	"context"
	"os"

	"github.com/gsxhq/gsx"
	p "gsxmf/views"
)

var _ = gsx.Raw

func main() {
	_ = p.C(p.CProps{S: "v"}).Render(context.Background(), os.Stdout)
}
`)
	// go run compiles the generated .x.go; before the fix this failed with
	// "undefined: myfilters" because only the reserved-alias import was emitted.
	out := goRun(t, tmp)
	if !strings.Contains(out, "VH!") {
		t.Fatalf("expected \"VH!\" (filter + direct package call both resolve); got:\n%s", out)
	}
}

// TestWithFilterCurriedMigrationDiagnostic proves a still-curried function
// registered via a FilterAlias surfaces the migration diagnostic rather than
// silently miscompiling.
func TestWithFilterCurriedMigrationDiagnostic(t *testing.T) {
	t.Parallel()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	writeMultiFile(t, tmp, "go.mod", "module gsxmf\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	mfDir := filepath.Join(tmp, "myfilters")
	if err := os.MkdirAll(mfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMultiFile(t, mfDir, "myfilters.go", "package myfilters\n\nfunc Old(n int) func(string) string { return func(s string) string { return s } }\n")
	viewsDir := filepath.Join(tmp, "views")
	if err := os.MkdirAll(viewsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMultiFile(t, viewsDir, "views.gsx", "package views\n\ncomponent C(s string) { <p>{ s |> old(2) }</p> }\n")

	aliases := []FilterAlias{{Name: "old", PkgPath: "gsxmf/myfilters", FuncName: "Old"}}
	_, curriedErr := GenerateDirs(tmp, []string{viewsDir}, GenOptions{FilterPkgs: []string{stdImportPath}, Aliases: aliases, CSSMinify: true, JSMinify: true}, nil)
	if curriedErr == nil {
		t.Fatal("expected error for curried filter, got nil")
	}
	if !strings.Contains(curriedErr.Error(), "removed curried shape") {
		t.Fatalf("expected curried-shape error; got: %v", curriedErr)
	}
}
