package codegen

import (
	goast "go/ast"
	goparser "go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
	gsxparser "github.com/gsxhq/gsx/parser"
)

// TestInferProbeNameExactMatch pins isInferProbeName's exact-match contract:
// only the registry's own synthesized `_gsxinferN` names match. A user func
// merely PREFIXED with "GsxInfer" (finding 3's attack: a same-package
// GsxInferStuff helper corrupting the old positional harvest) must NOT match,
// since harvest now keys off this exact pattern to find probe calls.
func TestInferProbeNameExactMatch(t *testing.T) {
	for name, want := range map[string]bool{
		"_gsxinfer1":    true,
		"_gsxinfer42":   true,
		"_gsxinfer":     false,
		"_gsxinfer1x":   false,
		"GsxInferStuff": false, // the finding-3 attack: must NOT match
		"gsxinfer1":     false,
	} {
		if got := isInferProbeName(name); got != want {
			t.Errorf("isInferProbeName(%q) = %v, want %v", name, got, want)
		}
	}
}

// TestInferPartialProps is the finding-5 headline case: a generic component
// called with only SOME of its declared props must still infer its type
// arguments, exactly like a plain Go generic function call would from the
// arguments it's given. The old declaring-side GsxInfer helper required ALL
// declared fields to be supplied (inferHelperArgs bailed on any miss),
// silently falling through to a raw, uninstantiated composite-literal probe
// that always failed to compile.
func TestInferPartialProps(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module ipp\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	writeFile(t, tmp, "views.gsx", `package ipp

component Button[T string | int](label T, size string) {
	<button class={size}>{label}</button>
}

component Page() {
	<Button label={7} />
}
`)
	out, err := GenerateDirs(tmp, []string{tmp}, Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out[tmp].Diags) != 0 {
		t.Fatalf("diags: %+v", out[tmp].Diags)
	}
	var src string
	for _, b := range out[tmp].Files {
		src = string(b)
	}
	if !strings.Contains(src, "Button[int](ButtonProps[int]{Label: 7})") {
		t.Fatalf("partial-props inference failed; generated:\n%s", src)
	}
}

// TestUserGsxInferFuncDoesNotCorruptHarvest is the finding-3 case: a
// same-package user function whose name merely STARTS WITH "GsxInfer" (the
// old convention's exact prefix) must not be mistaken for an inference probe
// by harvest, and must not corrupt the k-th-probe alignment or otherwise
// affect a real generic tag's inference.
func TestUserGsxInferFuncDoesNotCorruptHarvest(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module ugif\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	writeFile(t, tmp, "views.gsx", `package ugif

func GsxInferStuff() float64 { return 1.5 }

component Button[T string | int](label T) {
	<b>{label}</b>
}

component Page() {
	<p>{ GsxInferStuff() }</p>
	<Button label={7} />
}
`)
	out, err := GenerateDirs(tmp, []string{tmp}, Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out[tmp].Diags) != 0 {
		t.Fatalf("diags: %+v", out[tmp].Diags)
	}
	var src string
	for _, b := range out[tmp].Files {
		src = string(b)
	}
	if !strings.Contains(src, "Button[int](ButtonProps[int]{Label: 7})") {
		t.Fatalf("inference corrupted by user GsxInferStuff func; generated:\n%s", src)
	}
}

// TestInferProbeRawSpanRecovery is the viability spike for the diagnostics
// task's span-matching design, pinning both sides of a deliberate tension:
//
// Probe statements are emitted UNDER the enclosing tag's //line mapping (NOT
// //line-free as the original task brief sketched) — module_importer.go's
// diagnostic loop drops any type error whose adjusted position still names
// the synthetic .x.go overlay, so a //line-free probe would make inference-
// failure diagnostics vanish entirely. That makes the ADJUSTED position of a
// probe's "cannot infer" error a .gsx position (survival), while the RAW
// skeleton offset — what inferSite.span is expressed in — must be recovered
// with fset.PositionFor(pos, false) (adjusted=false ignores //line).
//
// This test drives a real inference failure (`value={nil}` gives Go nothing
// to infer T from), reaches into the package-internal pipeline (buildSkeleton
// + checkSkeletonPackage, the same seam resolver_test.go uses), and asserts:
//
//  1. survival: the error's adjusted position maps to the .gsx (so the
//     module_importer filter keeps it), and
//  2. recovery: PositionFor(pos, false) yields a byte offset that falls
//     INSIDE the recorded inferSite.span for the probe.
//
// If (2) ever breaks, the diagnostics task's raw-position span matching is
// not viable as designed and needs a rethink before building on it.
func TestInferProbeRawSpanRecovery(t *testing.T) {
	t.Parallel()
	const src = `package views

component Box[T any](value T) {
	<span>{value}</span>
}

component Page() {
	<Box value={nil} />
}
`
	repoRootAbs, _ := filepath.Abs("../..")
	dir := t.TempDir()
	// loadFilterTable resolves the std filter package via `go list` from dir,
	// so the temp dir needs a real module context (same scaffold as
	// TestChildPropPipelineSkeletonImportsStd).
	writeFile(t, dir, "go.mod", "module ipsr\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRootAbs+"\n")
	gsxPath := filepath.Join(dir, "views.gsx")
	writeFile(t, dir, "views.gsx", src)
	fset := token.NewFileSet()
	file, err := gsxparser.ParseFile(fset, gsxPath, []byte(src), 0)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]*gsxast.File{gsxPath: file}
	propFields, nodeProps, attrsProps, byo, err := componentPropFieldsFor(dir, files)
	if err != nil {
		t.Fatalf("propFields: %v", err)
	}
	table, err := loadFilterTable(dir)
	if err != nil {
		t.Fatalf("loadFilterTable: %v", err)
	}
	skel, _, _, _, registry, err := buildSkeleton(file, table, propFields, nodeProps, attrsProps, nil, byo, nil, fset)
	if err != nil {
		t.Fatalf("buildSkeleton: %v", err)
	}
	site, ok := registry.lookup("_gsxinfer1")
	if !ok {
		t.Fatalf("no _gsxinfer1 probe recorded; skeleton:\n%s", skel)
	}
	// The recorded span must cover exactly the probe CALL statement in the
	// final skeleton string (the helper func decl is hoisted elsewhere).
	if spanText := skel[site.span.start:site.span.end]; !strings.HasPrefix(spanText, "_ = _gsxinfer1(") {
		t.Fatalf("span [%d,%d) does not cover the probe call; got %q", site.span.start, site.span.end, spanText)
	}

	// Type-check the skeleton through the same seam resolver_test.go uses.
	xgoPath := filepath.Join(dir, "views.x.go")
	gf, err := goparser.ParseFile(fset, xgoPath, skel, goparser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse skeleton: %v\n%s", err, skel)
	}
	shim, err := goparser.ParseFile(fset, filepath.Join(dir, "_gsxshared.x.go"),
		"package views\n\nfunc _gsxuse(...any) {}\nfunc _gsxuseq(...any) {}\nfunc _gsxcompsig(any) {}\nfunc _gsxunwrap[T any](v T, _ ...any) T { return v }\n", goparser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := newCachedResolver(repoRoot(t), []string{stdImportPath}, nil, allowImportsFixture)
	if err != nil {
		t.Fatal(err)
	}
	_, _, typeErrs := checkSkeletonPackage(dir, "views", []*goast.File{gf, shim}, fset, bundle.importer())

	var found bool
	for _, e := range typeErrs {
		if !strings.Contains(e.Msg, "cannot infer") {
			continue
		}
		found = true
		// Survival: the ADJUSTED position (honoring //line) must map to the
		// .gsx — this is what keeps the diagnostic alive through
		// module_importer's synthetic-position filter.
		if adj := fset.Position(e.Pos); !strings.HasSuffix(adj.Filename, ".gsx") {
			t.Errorf("adjusted position %v does not map to the .gsx; the diagnostic would be dropped", adj)
		}
		// Recovery: the RAW position (adjusted=false, ignoring //line) must
		// name the skeleton overlay and fall inside the recorded probe span.
		raw := fset.PositionFor(e.Pos, false)
		if raw.Filename != xgoPath {
			t.Errorf("raw position filename = %q, want the skeleton overlay %q", raw.Filename, xgoPath)
		}
		if raw.Offset < site.span.start || raw.Offset >= site.span.end {
			t.Errorf("raw offset %d outside probe span [%d,%d); span text %q, error %q",
				raw.Offset, site.span.start, site.span.end, skel[site.span.start:site.span.end], e.Msg)
		}
	}
	if !found {
		t.Fatalf("no 'cannot infer' type error surfaced; typeErrs=%v\nskeleton:\n%s", typeErrs, skel)
	}
}
