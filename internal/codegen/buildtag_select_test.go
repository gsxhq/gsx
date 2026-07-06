package codegen

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuildTagSelectsVariant proves that the same-name/same-signature
// build-tag variant tolerance (see TestSameSigVariantGeneratesAllFiles)
// doesn't just generate cleanly — the generated .x.go files actually
// compile-select the right variant under `go build -tags`, and the SELECTED
// variant is the one that runs. gsx never parses build tags; it's the Go
// toolchain that must pick exactly one Icon.
//
// Two Icon variants are gated on custom tags (variantA / variantB) with a
// body-unique static string each, plus a Page that renders Icon. We generate,
// write to a probe module (package main, so the built binary is runnable),
// build under each tag, and execute the binary to confirm the rendered output
// carries only that tag's marker string.
func TestBuildTagSelectsVariant(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns the go toolchain")
	}
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module varx\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	writeFile(t, tmp, "icon_a.gsx", "//go:build variantA\n\npackage main\n\ncomponent Icon(name string) {\n\t<span>VARIANT_A:{ name }</span>\n}\n")
	writeFile(t, tmp, "icon_b.gsx", "//go:build variantB\n\npackage main\n\ncomponent Icon(name string) {\n\t<span>VARIANT_B:{ name }</span>\n}\n")
	writeFile(t, tmp, "page.gsx", "package main\n\ncomponent Page() {\n\t<Icon name=\"x\"/>\n}\n")
	writeFile(t, tmp, "main.go", `package main

import (
	"context"
	"fmt"
	"os"
)

func main() {
	if err := Page().Render(context.Background(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
`)

	out, err := GenerateDirs(tmp, []string{tmp}, Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, ok := out[tmp]
	if !ok {
		t.Fatalf("no result for %s", tmp)
	}
	if hasError(res.Diags) {
		t.Fatalf("unexpected error diagnostics generating variants: %v", res.Diags)
	}
	for gsxPath, src := range res.Files {
		base := strings.TrimSuffix(filepath.Base(gsxPath), ".gsx")
		writeFile(t, tmp, base+".x.go", string(src))
	}

	// Under the default tag set (no -tags), Go's implicit "!variantA &&
	// !variantB" constraint means NEITHER Icon variant is even a candidate —
	// only page.x.go and main.go would build, referencing an undefined Icon.
	// That's expected; this test only asserts selection once a variant tag
	// is set, exercising both tags so it can't be "B always wins" by luck.
	assertVariantSelected(t, tmp, "variantA", "icon_a.x.go", "icon_b.x.go", "VARIANT_A:x", "VARIANT_B")
	assertVariantSelected(t, tmp, "variantB", "icon_b.x.go", "icon_a.x.go", "VARIANT_B:x", "VARIANT_A")
}

// assertVariantSelected builds and runs the probe module under the given tag,
// asserting via `go list` that only wantFile (not excludeFile) is a candidate
// source, and via the running binary's stdout that it rendered wantMarker
// and not excludeMarker.
func assertVariantSelected(t *testing.T, dir, tag, wantFile, excludeFile, wantMarker, excludeMarker string) {
	t.Helper()

	list := exec.Command("go", "list", "-tags", tag, "-f", "{{.GoFiles}}")
	list.Dir = dir
	lout, lerr := list.CombinedOutput()
	if lerr != nil {
		t.Fatalf("go list -tags %s: %v\n%s", tag, lerr, lout)
	}
	if !strings.Contains(string(lout), wantFile) || strings.Contains(string(lout), excludeFile) {
		t.Fatalf("go list -tags %s = %s; want %s included, %s tag-excluded", tag, lout, wantFile, excludeFile)
	}

	binPath := filepath.Join(dir, "varx_"+tag+".bin")
	build := exec.Command("go", "build", "-tags", tag, "-o", binPath, ".")
	build.Dir = dir
	if bout, berr := build.CombinedOutput(); berr != nil {
		t.Fatalf("go build -tags %s: %v\n%s", tag, berr, bout)
	}

	run := exec.Command(binPath)
	rout, rerr := run.CombinedOutput()
	if rerr != nil {
		t.Fatalf("run %s: %v\n%s", binPath, rerr, rout)
	}
	if !strings.Contains(string(rout), wantMarker) {
		t.Fatalf("tag %s: output = %q; want it to contain %q", tag, rout, wantMarker)
	}
	if strings.Contains(string(rout), excludeMarker) {
		t.Fatalf("tag %s: output = %q; must NOT contain %q", tag, rout, excludeMarker)
	}
}
