package gen

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGenerateWithUserFilter drives the unexported generate core with a user
// filter package registered (alongside std) and proves the rendered output
// flows through the user filter. This is the end-to-end equivalent of
// gen.Main(gen.WithFilters(std.Pkg, myfilters.Pkg)).
func TestGenerateWithUserFilter(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping go-build/run test in -short mode")
	}
	const mod = "gsxuf"
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module "+mod+"\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot(t)+"\n")

	mfDir := filepath.Join(tmp, "myfilters")
	writeFile(t, mfDir, "myfilters.go", "package myfilters\n\nfunc Shout(s string) string { return s + \"!\" }\n")

	viewsDir := filepath.Join(tmp, "views")
	writeFile(t, viewsDir, "hi.gsx", "package views\n\ncomponent Hi(n string) {\n\t<p>{ n |> shout }</p>\n}\n")

	filterPkgs := []string{stdPath, mod + "/myfilters"}
	res, err := generate([]string{viewsDir}, filterPkgs, nil, nil)
	if err != nil {
		t.Fatalf("generate: %v (errs=%v)", err, res.Errs)
	}
	if len(res.Written) != 1 {
		t.Fatalf("expected 1 written, got %d: %v", len(res.Written), res.Written)
	}

	// Drive a render harness that invokes the generated component.
	writeFile(t, tmp, "main.go", `package main

import (
	"context"
	"os"

	"github.com/gsxhq/gsx"
	p "gsxuf/views"
)

var _ = gsx.Raw

func main() {
	_ = p.Hi("hi").Render(context.Background(), os.Stdout)
}
`)
	cmd := exec.Command("go", "run", ".")
	cmd.Dir = tmp
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "hi!") {
		t.Fatalf("expected user filter shout to render \"hi!\"; got:\n%s", out)
	}
}
