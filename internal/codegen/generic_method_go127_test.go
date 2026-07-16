package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenericMethodComponentGo127(t *testing.T) {
	if !toolchainHasGenericMethods() {
		if os.Getenv("GSX_REQUIRE_GENERIC_METHODS") == "1" {
			t.Fatal("GSX_REQUIRE_GENERIC_METHODS=1 but the active toolchain does not parse generic methods")
		}
		t.Skip("active Go toolchain does not parse generic methods yet")
	}
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module genericmethod\n\ngo 1.27\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(tmp, "views")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, pkgDir, "views.gsx", `package views

type Page struct{}

component (p Page) Box[T string | int](value T) {
	<span>box</span>
}

component (p Page) Render() {
	<p.Box[int] value={7} />
	<p.Box value={7} />
}
`)
	res, err := GenerateDirs(tmp, []string{pkgDir}, Options{FilterPkgs: []string{stdImportPath}, CSSMinify: true, JSMinify: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got string
	for _, src := range res[pkgDir].Files {
		got = string(src)
	}
	for _, want := range []string{
		// Verbatim generic method declaration — the authored signature emitted
		// directly, with no synthesized <Recv><Name>Props struct or _gsxp param.
		"func (p Page) Box[T string | int](value T) _gsxrt.Node",
		"p.Box[int](7)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("generated source missing %q:\n%s", want, got)
		}
	}
	// The verbatim ABI must NOT synthesize a Props struct or an _gsxp wrapper
	// parameter for a (generic) method component.
	if strings.Contains(got, "PageBoxProps") {
		t.Fatalf("verbatim ABI must not synthesize a Props struct, but found PageBoxProps:\n%s", got)
	}
	// Both the explicit-type-arg call site (<p.Box[int] .../>) and the
	// omitted-type-arg one (<p.Box .../>, inferred via the caller-side probe
	// — the method-component inference gate) must independently emit this
	// exact instantiated positional call; a plain Contains above would already
	// pass if only ONE of the two sites produced it, so count occurrences to
	// prove the inference probe path (not just the explicit-type-arg path) worked.
	const wantCall = "p.Box[int](7)"
	if n := strings.Count(got, wantCall); n != 2 {
		t.Fatalf("want 2 occurrences of %q (one per call site), got %d:\n%s", wantCall, n, got)
	}
}
