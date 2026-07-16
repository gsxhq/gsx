package codegen

import (
	goast "go/ast"
	goparser "go/parser"
	"go/token"
	"go/types"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
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
// called with only SOME of its declared parameters must still infer its type
// arguments, exactly like a plain Go generic function call would from the
// arguments it's given. Omitted parameters are filled with their exact
// semantic zero after inference has fixed the type arguments.
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
	if !strings.Contains(src, `Button[int](7, "")`) {
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
	if !strings.Contains(src, "Button[int](7)") {
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
	genericSigs := genericSigsFor(files, byo)
	// Run the same one-pass preprocessing as production: without the component
	// stamp, <Box .../> would be misclassified as a plain HTML element and never
	// reach the inference-probe path this test is pinning.
	declNames := packageDeclNames(dir, files)
	bag := diag.NewBag(fset)
	if _, err := preprocessComponentCallSites(files, declNames, fset, nil, bag); err != nil {
		t.Fatalf("preprocess: %v", err)
	}
	skel, _, _, _, registry, _, err := buildSkeleton(file, funcTables{filters: table}, propFields, nodeProps, attrsProps, genericSigs, nil, byo, nil, fset, bag, nil, nil, skeletonFull)
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
	_, _, typeErrs := checkSkeletonPackage(dir, "views", []*goast.File{gf, shim}, fset, bundle.importer(), testTypeCheckEnvironment())

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

	// declSpan / siteAt spot-check (accumulated Task-8 input #1): the hoisted
	// helper decl carries no //line reset, so an error positioned inside its
	// OWN body (not the inline call above) would still need to resolve back
	// to this site. Assert the recorded declSpan actually covers the emitted
	// "func _gsxinfer1[...]" decl text in the final skeleton, and that siteAt
	// resolves an offset inside it (which falls OUTSIDE span, the call's own
	// range) to the SAME site.
	if declText := skel[site.declSpan.start:site.declSpan.end]; !strings.HasPrefix(declText, "func _gsxinfer1[") {
		t.Fatalf("declSpan [%d,%d) does not cover the hoisted helper decl; got %q", site.declSpan.start, site.declSpan.end, declText)
	}
	if site.declSpan.start >= site.span.start && site.declSpan.start < site.span.end {
		t.Fatalf("declSpan and span unexpectedly overlap; declSpan=%+v span=%+v", site.declSpan, site.span)
	}
	if got, ok := registry.siteAt(site.declSpan.start); !ok || got != site {
		t.Fatalf("siteAt(declSpan.start) = %+v, %v; want the same site %+v", got, ok, site)
	}
}

// TestUserCannotInferErrorNotRewritten is the finding-6/7 headline hijack
// case: Card is a plain, NON-generic component, but the user's OWN Go code —
// a real generic func called from inside Card's children — fails to infer
// its own type parameter. The OLD position-guessing rewrite
// (componentTagAtTypeError) matched ANY "cannot infer" error whose position
// fell anywhere within a component tag's overall .gsx line/column span,
// including its children content, so First(nil)'s failure (nested inside
// `<Card>...</Card>`) got hijacked and blamed on Card — a diagnostic naming
// the wrong symbol with a fabricated (and wrong-arity) instantiation hint.
// The registry-driven rewrite must never fire here: Card has no inference
// probe recorded at all (it isn't generic), so First's own error must pass
// through untouched, still naming First and T.
func TestUserCannotInferErrorNotRewritten(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module ucine\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	writeFile(t, tmp, "views.gsx", `package views

func First[T any](v []T) T { return v[0] }

component Card(title string) {
	<div>
		<h1>{title}</h1>
		{children}
	</div>
}

component Page() {
	<Card title="x">{First(nil)}</Card>
}
`)
	out, err := GenerateDirs(tmp, []string{tmp}, Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, d := range out[tmp].Diags {
		if !strings.Contains(d.Message, "cannot infer") {
			continue
		}
		found = true
		if !strings.Contains(d.Message, "First") || !strings.Contains(d.Message, "T") {
			t.Errorf("diagnostic does not name the user's own First[T] call: %+v", d)
		}
		if strings.Contains(d.Message, "<Card") {
			t.Errorf("diagnostic hijacked onto <Card> (finding-6/7 regression): %+v", d)
		}
	}
	if !found {
		t.Fatalf("no 'cannot infer' diagnostic surfaced; diags=%+v", out[tmp].Diags)
	}
}

// TestCrossPackageInferenceHintArity pins Task 8's arity fix for an IMPORTED
// generic component: components.Grid[K comparable, V any](rows map[K]V),
// called with a value go/types cannot infer K/V from (rows={nil}), must
// report a TWO-placeholder hint (`<components.Grid[type, type] ...>`) —
// the real arity — not the old code's single "[type]" (explicitTypeArgHint
// only ever counted a SAME-PACKAGE component's own TypeParams text; for a
// cross-package tag it silently fell back to its "type" default regardless
// of the real arity).
func TestCrossPackageInferenceHintArity(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module example.com/cpiha\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	compDir := filepath.Join(tmp, "components")
	if err := os.MkdirAll(compDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, compDir, "grid.gsx", "package components\n\ncomponent Grid[K comparable, V any](rows map[K]V) {\n\t<table></table>\n}\n")
	writeFile(t, tmp, "post.gsx", "package cpiha\n\nimport \"example.com/cpiha/components\"\n\ncomponent Post() {\n\t<components.Grid rows={nil} />\n}\n")

	res, err := GenerateDirs(tmp, []string{tmp, compDir}, Options{FilterPkgs: []string{stdImportPath}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if diags := res[compDir].Diags; len(diags) != 0 {
		t.Fatalf("unexpected diagnostics generating components package: %+v", diags)
	}
	var found bool
	for _, d := range res[tmp].Diags {
		if !strings.Contains(d.Message, "cannot infer") && !strings.Contains(d.Message, "type inference failed") {
			continue
		}
		found = true
		if !strings.Contains(d.Message, "<components.Grid[type, type] ...>") {
			t.Errorf("diagnostic does not carry the real 2-arity hint: %+v", d)
		}
	}
	if !found {
		t.Fatalf("no inference-failure diagnostic surfaced; diags=%+v", res[tmp].Diags)
	}
}

// TestConstraintViolationNamesTagNotProbe is the finding-2 probe-name-leak
// case: Ticker's type param U is successfully INFERRED (as time.Duration,
// from the supplied value), but then fails ITS OWN constraint (`~string |
// ~int`; time.Duration's underlying type is int64, not int, so `~int`
// rejects it) — a different go/types error shape than "cannot infer"
// ("... does not satisfy ..."), which can carry an "in call to _gsxinferN, "
// prefix naming the internal caller-side probe helper. Once the error's
// position matches this tag's recorded probe span, the rewrite must name the
// TAG and never leak the internal probe name, while preserving the
// constraint-violation substance (the offending type + constraint).
func TestConstraintViolationNamesTagNotProbe(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module cvnt\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	writeFile(t, tmp, "views.gsx", `package views

import "time"

component Ticker[U ~string | ~int](value U) {
	<p>{ value }</p>
}

component Page() {
	<Ticker value={time.Second} />
}
`)
	out, err := GenerateDirs(tmp, []string{tmp}, Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, d := range out[tmp].Diags {
		if !strings.Contains(d.Message, "does not satisfy") {
			continue
		}
		found = true
		if !strings.Contains(d.Message, "Ticker") {
			t.Errorf("diagnostic does not name the tag: %+v", d)
		}
		if strings.Contains(d.Message, "_gsxinfer") {
			t.Errorf("diagnostic leaks the internal probe name: %+v", d)
		}
		if !strings.Contains(d.Message, "time.Duration") {
			t.Errorf("diagnostic lost the constraint-violation substance: %+v", d)
		}
	}
	if !found {
		t.Fatalf("no 'does not satisfy' diagnostic surfaced; diags=%+v", out[tmp].Diags)
	}
}

// TestUserCannotInferInAttrNotRewritten is the ATTR-position variant of the
// finding-6/7 hijack (TestUserCannotInferErrorNotRewritten covers children):
// a supplied attr expression is inlined INTO the probe call's arguments, so
// the user's own failing generic call there (`value={First(nil)}`) positions
// its error INSIDE the probe span — span matching alone cannot tell it from
// the probe's own failure. The discriminator: the probe's own cannot-infer
// messages always carry the "in call to _gsxinferN, " prefix, the user's
// nested call carries ITS name ("in call to First, ..."). The message must
// pass through untouched — naming First, never blaming <Wrap> with a hint
// that cannot fix First.
func TestUserCannotInferInAttrNotRewritten(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module ucia\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	writeFile(t, tmp, "views.gsx", `package views

func First[T any](v []T) T { return v[0] }

component Wrap[T string | int](value T) {
	<p>{value}</p>
}

component Page() {
	<Wrap value={First(nil)} />
}
`)
	out, err := GenerateDirs(tmp, []string{tmp}, Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, d := range out[tmp].Diags {
		if !strings.Contains(d.Message, "cannot infer") {
			continue
		}
		found = true
		if !strings.Contains(d.Message, "First") {
			t.Errorf("diagnostic does not name the user's own First[T] call: %+v", d)
		}
		if strings.Contains(d.Message, "<Wrap") {
			t.Errorf("diagnostic hijacked onto <Wrap> (attr-position finding-6/7 regression): %+v", d)
		}
	}
	if !found {
		t.Fatalf("no 'cannot infer' diagnostic surfaced; diags=%+v", out[tmp].Diags)
	}
}

// TestUserConstraintViolationInAttrNotRewritten is the does-not-satisfy
// counterpart of TestUserCannotInferInAttrNotRewritten: the user's own
// generic call in an attr value fails ITS OWN constraint (`Only(1)` with
// `Only[T ~string]`), positioned inside the probe span at the ARGUMENT. The
// discriminator (the reviewer-verified position contract): the probe's OWN
// constraint violations position at the CALLEE while a nested user call's
// positions inside an argument expression — argAt hit → pass through
// untouched, never blaming <Wrap>.
// TestConstraintViolationNamesTagNotProbe still pins the probe's own
// (callee-positioned) violation getting the tag-naming rewrite.
func TestUserConstraintViolationInAttrNotRewritten(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module ucva\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	writeFile(t, tmp, "views.gsx", `package views

func Only[T ~string](v T) T { return v }

component Wrap[T any](value T) {
	<p>ok</p>
}

component Page() {
	<Wrap value={Only(1)} />
}
`)
	out, err := GenerateDirs(tmp, []string{tmp}, Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, d := range out[tmp].Diags {
		if !strings.Contains(d.Message, "does not satisfy") {
			continue
		}
		found = true
		if !strings.Contains(d.Message, "~string") {
			t.Errorf("diagnostic lost the user's constraint substance: %+v", d)
		}
		if strings.Contains(d.Message, "<Wrap") {
			t.Errorf("diagnostic blames <Wrap> for the user's own constraint violation: %+v", d)
		}
	}
	if !found {
		t.Fatalf("no 'does not satisfy' diagnostic surfaced; diags=%+v", out[tmp].Diags)
	}
}

// TestWrongPropTypeAtInferredTagNamesProp pins rewriteProbeDiag's
// argument-positioned leak arm (shape 5): a generic tag whose T infers fine
// from one prop but supplies a WRONG type for a NON-generic prop. go/types
// reports the assignability error AT the offending argument — inside the
// probe span, with the internal helper name in the message text ("cannot use
// 2 (untyped int constant) as string value in argument to _gsxinfer1"), and
// with NEITHER gate substring of the original two-arm rewrite ("cannot
// infer"/"does not satisfy"), so it used to reach users verbatim. The
// rewrite must scrub the probe reference and name the exact prop (via
// inferSite.argAt on the error's raw offset) + the tag, preserving the
// cannot-use substance.
func TestWrongPropTypeAtInferredTagNamesProp(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module wptn\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	writeFile(t, tmp, "views.gsx", `package views

component Two[T string | int](value T, name string) {
	<p>{value}{name}</p>
}

component Page() {
	<Two value={1} name={2} />
}
`)
	out, err := GenerateDirs(tmp, []string{tmp}, Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, d := range out[tmp].Diags {
		if !strings.Contains(d.Message, "cannot use 2") {
			continue
		}
		found = true
		if !strings.Contains(d.Message, `for prop "name" of <Two>`) {
			t.Errorf("diagnostic does not name the offending prop + tag: %+v", d)
		}
		if !strings.Contains(d.Message, "as string value") {
			t.Errorf("diagnostic lost the cannot-use substance: %+v", d)
		}
		if strings.Contains(d.Message, "_gsxinfer") {
			t.Errorf("diagnostic leaks the internal probe name: %+v", d)
		}
	}
	if !found {
		t.Fatalf("no wrong-prop-type diagnostic surfaced; diags=%+v", out[tmp].Diags)
	}
}

// TestMismatchedInferenceKeepsSubstance pins rewriteProbeDiag's
// substance-carrying cannot-infer arm (shape 2): two props constraining the
// SAME type param to conflicting types ("in call to _gsxinferN, mismatched
// types untyped int and untyped string (cannot infer T)"). The original
// rewrite matched the "cannot infer" substring and replaced the WHOLE
// message with the bare instantiate hint, dropping the mismatched-types
// substance that tells the user WHICH two values conflict. Now: tag +
// preserved substance + hint.
func TestMismatchedInferenceKeepsSubstance(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module miks\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	writeFile(t, tmp, "views.gsx", `package views

component Pair[T string | int](a T, b T) {
	<p>{a}{b}</p>
}

component Page() {
	<Pair a={1} b={"x"} />
}
`)
	out, err := GenerateDirs(tmp, []string{tmp}, Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, d := range out[tmp].Diags {
		if !strings.Contains(d.Message, "cannot infer") {
			continue
		}
		found = true
		if !strings.Contains(d.Message, "type inference failed for <Pair>") {
			t.Errorf("diagnostic does not name the tag: %+v", d)
		}
		if !strings.Contains(d.Message, "mismatched types untyped int and untyped string") {
			t.Errorf("diagnostic dropped the mismatched-types substance: %+v", d)
		}
		if !strings.Contains(d.Message, "please instantiate with <Pair[type] ...>") {
			t.Errorf("diagnostic lost the instantiate hint: %+v", d)
		}
		if strings.Contains(d.Message, "_gsxinfer") {
			t.Errorf("diagnostic leaks the internal probe name: %+v", d)
		}
	}
	if !found {
		t.Fatalf("no mismatched-inference diagnostic surfaced; diags=%+v", out[tmp].Diags)
	}
}

// TestNoSuppliedPropsGenericTagFriendlyHint pins rewriteProbeDiag's
// uninstantiated composite-literal arm (shape 4): a generic tag with params
// DECLARED but NONE supplied. emitInferProbe declines (nothing to infer
// from), the tag falls through to the plain `_ = Holder(HolderProps{})`
// probe, and go/types reports "cannot use generic type HolderProps[T any]
// without instantiation" — which used to pass through verbatim (the
// default-branch recordProbeSpan was recorded but the rewrite had no arm
// consuming it). There is genuinely nothing to infer FROM here, so the
// friendly instantiate hint is exactly the right advice.
func TestNoSuppliedPropsGenericTagFriendlyHint(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module nspg\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	writeFile(t, tmp, "views.gsx", `package views

component Holder[T any](value any) {
	<p>{ value }</p>
}

component Page() {
	<Holder />
}
`)
	out, err := GenerateDirs(tmp, []string{tmp}, Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, d := range out[tmp].Diags {
		if !strings.Contains(d.Message, "type inference failed for <Holder>") {
			continue
		}
		found = true
		if !strings.Contains(d.Message, "please instantiate with <Holder[type] ...>") {
			t.Errorf("diagnostic lost the instantiate hint: %+v", d)
		}
	}
	if !found {
		t.Fatalf("no friendly diagnostic surfaced; diags=%+v", out[tmp].Diags)
	}
	for _, d := range out[tmp].Diags {
		if strings.Contains(d.Message, "without instantiation") || strings.Contains(d.Message, "HolderProps") {
			t.Errorf("raw without-instantiation skeleton error leaked: %+v", d)
		}
	}
}

// TestAliasedImportNoSuppliedPropsFriendlyHint is the ALIAS-imported variant
// of TestNoSuppliedPropsGenericTagFriendlyHint: the recorded propsType is
// the CALLER's spelling ("comp.HolderProps") while go/types prints the dep
// package's own NAME ("cannot use generic type components.HolderProps[T
// any] without instantiation") — a gate matching the full qualified
// propsType text therefore missed, and aliased callers got the raw skeleton
// wording instead of the friendly hint. The gate must match the props type's
// BASE identifier ("HolderProps"), invariant across both spellings.
func TestAliasedImportNoSuppliedPropsFriendlyHint(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module example.com/ainsp\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	compDir := filepath.Join(tmp, "components")
	if err := os.MkdirAll(compDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, compDir, "holder.gsx", "package components\n\ncomponent Holder[T any](value any) {\n\t<p>h</p>\n}\n")
	writeFile(t, tmp, "post.gsx", "package ainsp\n\nimport comp \"example.com/ainsp/components\"\n\ncomponent Post() {\n\t<comp.Holder />\n}\n")

	res, err := GenerateDirs(tmp, []string{tmp, compDir}, Options{FilterPkgs: []string{stdImportPath}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if diags := res[compDir].Diags; len(diags) != 0 {
		t.Fatalf("unexpected diagnostics generating components package: %+v", diags)
	}
	var found bool
	for _, d := range res[tmp].Diags {
		if !strings.Contains(d.Message, "type inference failed for <comp.Holder>") {
			continue
		}
		found = true
		if !strings.Contains(d.Message, "please instantiate with <comp.Holder[type] ...>") {
			t.Errorf("diagnostic lost the instantiate hint: %+v", d)
		}
	}
	if !found {
		t.Fatalf("no friendly diagnostic surfaced; diags=%+v", res[tmp].Diags)
	}
	for _, d := range res[tmp].Diags {
		if strings.Contains(d.Message, "without instantiation") || strings.Contains(d.Message, "HolderProps") {
			t.Errorf("raw without-instantiation skeleton error leaked: %+v", d)
		}
	}
}

// importCall records one addImport(path, alias) invocation, in call order.
type importCall struct{ path, alias string }

// TestRequalifyTypeExpr is the brief's table test for requalifyTypeExpr: a
// single type expression written in a dep package's file context, rewritten
// for the calling file's skeleton. Covers every case in the task-3 brief plus
// edge cases beyond it (pointer/func recursion, explicit import alias, alias
// reuse across repeated qualifiers within one call, unresolvable and
// AMBIGUOUS qualifiers, bare predeclared name, and the declared-set contract:
// a component's own type-param name stays bare when supplied, and the
// shadowing trap when the caller fails to supply it is pinned explicitly).
func TestRequalifyTypeExpr(t *testing.T) {
	fmtImport := []importSpec{{name: "", path: "fmt"}}
	pqImport := []importSpec{{name: "pq", path: "example.com/mod/other"}}

	tests := []struct {
		name        string
		src         string
		depAlias    string
		depImports  []importSpec
		declared    map[string]bool
		want        string
		wantErr     string // substring; "" means no error expected
		wantImports []importCall
	}{
		{
			name:     "union of predeclared names is unchanged",
			src:      "string | int",
			depAlias: "components",
			want:     "string | int",
		},
		{
			name:     "tilde of predeclared name is unchanged",
			src:      "~string",
			depAlias: "components",
			want:     "~string",
		},
		{
			name:     "union with a dep-local exported type qualifies only that arm",
			src:      "MyInt | string",
			depAlias: "components",
			want:     "components.MyInt | string",
		},
		{
			name:        "qualified dep-file reference is re-imported under a fresh alias",
			src:         "fmt.Stringer",
			depAlias:    "components",
			depImports:  fmtImport,
			want:        "_gsxti1.Stringer",
			wantImports: []importCall{{"fmt", "_gsxti1"}},
		},
		{
			name:     "unexported bare ident is rejected",
			src:      "secret | int",
			depAlias: "components",
			wantErr:  "unexported type secret",
		},
		{
			name:     "slice element type is qualified",
			src:      "[]Row",
			depAlias: "components",
			want:     "[]components.Row",
		},
		{
			name:     "map value type is qualified, predeclared key untouched",
			src:      "map[string]Cfg",
			depAlias: "components",
			want:     "map[string]components.Cfg",
		},
		{
			name:     "bare predeclared name is unchanged",
			src:      "any",
			depAlias: "components",
			want:     "any",
		},
		{
			name:     "pointer recurses into its base type",
			src:      "*Row",
			depAlias: "components",
			want:     "*components.Row",
		},
		{
			name:     "func type recurses into params and results",
			src:      "func(Row) Cfg",
			depAlias: "components",
			want:     "func(components.Row) components.Cfg",
		},
		{
			name:        "explicit dep-file import alias resolves to its path",
			src:         "pq.T",
			depAlias:    "components",
			depImports:  pqImport,
			want:        "_gsxti1.T",
			wantImports: []importCall{{"example.com/mod/other", "_gsxti1"}},
		},
		{
			name:        "repeated qualifier for the same path reuses one alias",
			src:         "fmt.Stringer | fmt.GoStringer",
			depAlias:    "components",
			depImports:  fmtImport,
			want:        "_gsxti1.Stringer | _gsxti1.GoStringer",
			wantImports: []importCall{{"fmt", "_gsxti1"}},
		},
		{
			name:     "unresolvable qualifier is an error",
			src:      "pq.T",
			depAlias: "components",
			wantErr:  "cannot resolve import",
		},
		{
			name:       "unexported selector member is rejected",
			src:        "fmt.stringer",
			depAlias:   "components",
			depImports: fmtImport,
			wantErr:    "unexported type stringer",
		},
		{
			name:       "ambiguous last-segment match across two unaliased imports is an error",
			src:        "util.Thing",
			depAlias:   "components",
			depImports: []importSpec{{name: "", path: "example.com/a/util"}, {name: "", path: "example.com/b/util"}},
			wantErr:    "ambiguous import for type qualifier util",
		},
		{
			name:     "declared type-param name stays bare",
			src:      "[]T",
			depAlias: "components",
			declared: map[string]bool{"T": true},
			want:     "[]T",
		},
		{
			name:     "declared type-param name stays bare while dep-locals still qualify",
			src:      "map[T]Row",
			depAlias: "components",
			declared: map[string]bool{"T": true},
			want:     "map[T]components.Row",
		},
		{
			// The SHADOWING TRAP, pinned deliberately: without the declared set
			// the engine cannot tell a component's own type-param name from a
			// dep-local exported type, so `[]T` misqualifies to
			// `[]components.T` — which COMPILES (pointing at the wrong type) if
			// the dep exports a T. The caller MUST supply the component's
			// type-param name set (parseTypeParamNames) for param typeSrc.
			name:     "shadowing trap: empty declared set misqualifies a type-param name",
			src:      "[]T",
			depAlias: "components",
			declared: map[string]bool{},
			want:     "[]components.T",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls []importCall
			addImport := func(path, alias string) { calls = append(calls, importCall{path, alias}) }
			got, err := requalifyTypeExpr(tt.src, tt.depAlias, tt.depImports, tt.declared, addImport)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("requalifyTypeExpr(%q) = %q, nil; want error containing %q", tt.src, got, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("requalifyTypeExpr(%q) error = %q, want substring %q", tt.src, err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("requalifyTypeExpr(%q) unexpected error: %v", tt.src, err)
			}
			if got != tt.want {
				t.Errorf("requalifyTypeExpr(%q) = %q, want %q", tt.src, got, tt.want)
			}
			if tt.wantImports == nil {
				if len(calls) != 0 {
					t.Errorf("requalifyTypeExpr(%q) addImport calls = %+v, want none", tt.src, calls)
				}
			} else if !reflect.DeepEqual(calls, tt.wantImports) {
				t.Errorf("requalifyTypeExpr(%q) addImport calls = %+v, want %+v", tt.src, calls, tt.wantImports)
			}
		})
	}
}

// TestRequalifyTypeParams is the brief's table test for requalifyTypeParams:
// a whole bracketed type-param declaration list, rewritten field by field
// with every declared name collected up front so a later field's constraint
// can reference an earlier (or later) sibling type-param name without it
// being mistaken for a dep-local reference.
func TestRequalifyTypeParams(t *testing.T) {
	tests := []struct {
		name       string
		decl       string
		depAlias   string
		depImports []importSpec
		want       string
		wantErr    string
	}{
		{
			name:     "two params, one dep-local constraint",
			decl:     "K comparable, V Renderer",
			depAlias: "components",
			want:     "K comparable, V components.Renderer",
		},
		{
			name:     "constraint referencing a sibling type param stays bare",
			decl:     "K any, V interface{ ~[]K }",
			depAlias: "components",
			want:     "K any, V interface{ ~[]K }",
		},
		{
			name:     "single param with a dep-local union arm",
			decl:     "T MyInt | string",
			depAlias: "components",
			want:     "T components.MyInt | string",
		},
		{
			name:     "unexported constraint arm is rejected",
			decl:     "T secret | int",
			depAlias: "components",
			wantErr:  "unexported type secret",
		},
		{
			name:     "empty decl is a no-op",
			decl:     "",
			depAlias: "components",
			want:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addImport := func(path, alias string) {}
			got, err := requalifyTypeParams(tt.decl, tt.depAlias, tt.depImports, addImport)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("requalifyTypeParams(%q) = %q, nil; want error containing %q", tt.decl, got, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("requalifyTypeParams(%q) error = %q, want substring %q", tt.decl, err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("requalifyTypeParams(%q) unexpected error: %v", tt.decl, err)
			}
			if got != tt.want {
				t.Errorf("requalifyTypeParams(%q) = %q, want %q", tt.decl, got, tt.want)
			}
		})
	}
}

// writeUnexportedTypeArgModule scaffolds the Task 6 fixture: a models
// package exporting a constructor that returns an UNEXPORTED type
// (models.secret), a generic components.Box[T any](value T) whose
// constraint is `any` (so nothing about ITS declaration needs
// requalification — this is a DIFFERENT path from the Task 4
// requalification-failed fail-safe, which fires when the DECLARED
// constraint itself is dep-local-unexported; here the constraint is fine and
// inference succeeds, but the INFERRED type argument is unspeakable outside
// models), and a root package with two files: post.gsx (the offending tag)
// and other.gsx (an unrelated sibling that must be unaffected). valueExpr is
// the m.New... call whose inferred type argument names (possibly nests) the
// unexported models.secret. modName must be a valid bare Go package/module
// name unique to the calling test (keeps each test's temp module import
// path distinct).
//
// models is a plain .go file, not .gsx: it declares no components (only
// bare type/func decls), and generateFile unconditionally emits the
// boilerplate context/io/gsx runtime imports for every .x.go regardless of
// whether any component actually used them — a componentless .gsx would
// generate a .x.go with those three imports unused, failing `go build` for a
// reason unrelated to this test. This mirrors modelDir/flag.go in
// TestGenericCrossPackageTag (generic_crosspkg_test.go), which sidesteps the
// same issue the same way; models is never passed to GenerateDirs (it has
// nothing for gsx to generate).
func writeUnexportedTypeArgModule(t *testing.T, modName, valueExpr string) (tmp, compDir string) {
	t.Helper()
	repoRootAbs, _ := filepath.Abs("../..")
	tmp = t.TempDir()
	writeFile(t, tmp, "go.mod", "module example.com/"+modName+"\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRootAbs+"\n")
	compDir = filepath.Join(tmp, "components")
	if err := os.MkdirAll(compDir, 0o755); err != nil {
		t.Fatal(err)
	}
	modelsDir := filepath.Join(tmp, "models")
	if err := os.MkdirAll(modelsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// value is never rendered inside Box: T's constraint is `any`, which
	// classify's typeparam handling treats as catUnsupported (an empty/`any`
	// term set), so interpolating {value} directly would fail Box's OWN
	// generation with an unrelated "unrenderable" diagnostic. The prop only
	// needs to exist for inference to run over it.
	writeFile(t, compDir, "box.gsx", "package components\n\ncomponent Box[T any](value T) {\n\t<span>box</span>\n}\n")
	writeFile(t, modelsDir, "models.go", `package models

type secret string

// secretID is an unexported ALIAS (gotypesalias=1 is unconditional on this
// repo's Go version, so go/types materializes it as *types.Alias and
// types.TypeString prints the ALIAS's own name, not its RHS) — the walker
// must treat it exactly like an unexported Named.
type secretID = string

func NewSecret() secret { return "shh" }

func NewSecrets() []secret { return []secret{"shh"} }

func NewSecretID() secretID { return "id" }

type getter struct{}

func (getter) Get() secret { return "shh" }

// NewGetter returns an ANONYMOUS interface type: types.TypeString prints its
// explicit method signatures inline (interface{Get() models.secret}), so the
// walker must descend into explicit methods, not just embedded types.
func NewGetter() interface{ Get() secret } { return getter{} }
`)
	writeFile(t, tmp, "post.gsx", "package "+modName+"\n\nimport (\n\t\"example.com/"+modName+"/components\"\n\tm \"example.com/"+modName+"/models\"\n)\n\ncomponent Post() {\n\t<components.Box value={"+valueExpr+"} />\n}\n")
	writeFile(t, tmp, "other.gsx", "package "+modName+"\n\ncomponent Other() {\n\t<p>ok</p>\n}\n")
	return tmp, compDir
}

// assertUnrenderableTypeArg asserts diags contains EXACTLY one
// component-type-args diagnostic, that it is Error-severity and positioned,
// and that its message names the offending qualified type (wantType, e.g.
// "models.secret") — mirrors assertOnlyInferenceUnavailable's shape in
// generic_crosspkg_test.go for the sibling (Task 4) fail-safe, adapted to
// this Error-severity, hard-failure diagnostic.
func assertUnrenderableTypeArg(t *testing.T, diags []diag.Diagnostic, wantType string) {
	t.Helper()
	var found int
	for _, d := range diags {
		if d.Code != "component-type-args" {
			if d.Severity == diag.Error {
				t.Errorf("unexpected error diagnostic alongside unrenderable-type-arg: %+v", d)
			}
			continue
		}
		found++
		if d.Severity != diag.Error {
			t.Errorf("unrenderable-type-arg diagnostic severity = %v, want Error: %+v", d.Severity, d)
		}
		if d.Start.Line == 0 {
			t.Errorf("unrenderable-type-arg diagnostic is not positioned: %+v", d)
		}
		if !strings.Contains(d.Message, wantType) {
			t.Errorf("unrenderable-type-arg diagnostic does not name %s: %+v", wantType, d)
		}
	}
	if found != 1 {
		t.Fatalf("want exactly 1 unrenderable-type-arg diagnostic, got %d: %+v", found, diags)
	}
}

// TestInferredUnexportedTypeArgRejected pins the Task 6 emit-time backstop:
// an inferred type argument that names a type UNEXPORTED outside its
// declaring package cannot be printed at all (`models.secret` is not valid
// Go syntax from outside package models), so childTypeArgUse must refuse to
// print it and record a positioned diagnostic instead of emitting
// non-compiling Go. This is the emit-time counterpart to the Task 4
// requalification-failed fail-safe (generic_crosspkg_test.go) — that one
// fires when the DECLARED constraint can't be requalified into the caller's
// context; this one fires when inference SUCCEEDS but the winning type
// argument is unspeakable at the call site. The exact package plan fails
// closed, so the caller package emits no partial output; the components
// package (which has no bad tags itself) still generates cleanly.
func TestInferredUnexportedTypeArgRejected(t *testing.T) {
	tmp, compDir := writeUnexportedTypeArgModule(t, "utar", "m.NewSecret()")

	res, err := GenerateDirs(tmp, []string{tmp, compDir}, Options{FilterPkgs: []string{stdImportPath}, CSSMinify: true, JSMinify: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if diags := res[compDir].Diags; len(diags) != 0 {
		t.Fatalf("unexpected diagnostics generating components package: %+v", diags)
	}
	assertUnrenderableTypeArg(t, res[tmp].Diags, "models.secret")

	if len(res[tmp].Files) != 0 {
		t.Fatalf("caller package must not have partial generated output; files = %+v", res[tmp].Files)
	}
}

// TestInferredUnexportedTypeArgRejectedNested is the nested variant: the
// offending unexported type is buried inside a slice ([]models.secret, from
// NewSecrets), not the bare top-level type argument. unspeakableTypeArg must
// recurse into the slice element to catch it — a check that only looked at
// the top-level *types.Named would miss this.
func TestInferredUnexportedTypeArgRejectedNested(t *testing.T) {
	tmp, compDir := writeUnexportedTypeArgModule(t, "utarn", "m.NewSecrets()")

	res, err := GenerateDirs(tmp, []string{tmp, compDir}, Options{FilterPkgs: []string{stdImportPath}, CSSMinify: true, JSMinify: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if diags := res[compDir].Diags; len(diags) != 0 {
		t.Fatalf("unexpected diagnostics generating components package: %+v", diags)
	}
	assertUnrenderableTypeArg(t, res[tmp].Diags, "models.secret")

	for p := range res[tmp].Files {
		if strings.HasSuffix(p, "post.gsx") {
			t.Fatalf("post.gsx must not have generated output; files = %+v", res[tmp].Files)
		}
	}
}

// TestInferredUnexportedTypeArgRejectedAlias is the ALIAS variant. Unlike an
// unexported defined type, an unexported alias to string contributes its RHS
// to inference, so the exact call can legally instantiate Box[string].
func TestInferredUnexportedTypeArgRejectedAlias(t *testing.T) {
	tmp, compDir := writeUnexportedTypeArgModule(t, "utara", "m.NewSecretID()")

	res, err := GenerateDirs(tmp, []string{tmp, compDir}, Options{FilterPkgs: []string{stdImportPath}, CSSMinify: true, JSMinify: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if diags := res[compDir].Diags; len(diags) != 0 {
		t.Fatalf("unexpected diagnostics generating components package: %+v", diags)
	}
	if diags := res[tmp].Diags; len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %+v", diags)
	}
	for p, src := range res[tmp].Files {
		if strings.HasSuffix(p, "post.gsx") && !strings.Contains(string(src), "components.Box[string](m.NewSecretID())") {
			t.Fatalf("post.gsx missing inferred alias RHS call:\n%s", src)
		}
	}
}

// TestInferredUnexportedTypeArgRejectedInterfaceMethod is the anonymous-
// interface variant: the constructor returns an ANONYMOUS `interface{ Get()
// secret }`, so the inferred type argument prints its explicit method
// signatures inline (`interface{Get() models.secret}` — typestring.go writes
// each explicit method's signature for an unnamed interface). The offender
// hides in a METHOD RESULT, not in an embedded type, so unspeakableTypeArg's
// Interface case must walk ExplicitMethods() (each method's *types.Signature
// recursing through the existing Signature case), not just EmbeddedTypes().
func TestInferredUnexportedTypeArgRejectedInterfaceMethod(t *testing.T) {
	tmp, compDir := writeUnexportedTypeArgModule(t, "utari", "m.NewGetter()")

	res, err := GenerateDirs(tmp, []string{tmp, compDir}, Options{FilterPkgs: []string{stdImportPath}, CSSMinify: true, JSMinify: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if diags := res[compDir].Diags; len(diags) != 0 {
		t.Fatalf("unexpected diagnostics generating components package: %+v", diags)
	}
	assertUnrenderableTypeArg(t, res[tmp].Diags, "models.secret")

	for p := range res[tmp].Files {
		if strings.HasSuffix(p, "post.gsx") {
			t.Fatalf("post.gsx must not have generated output; files = %+v", res[tmp].Files)
		}
	}
}

// TestInferredTypeArgImportCollision reproduces the independent-review probe
// (finding 1): main.gsx plain-imports "example.com/tic/other/ids" (package
// ids) and uses it directly in markup, while ALSO calling a generic child
// component whose type argument is inferred as example.com/tic/ids.ID — a
// DIFFERENT package that happens to declare the same package name "ids".
// childTypeArgUse's qf used to print every inferred cross-package type
// argument's qualifier as plain pkg.Name(), plain-importing the path into
// `imports` unconditionally — with two DIFFERENT paths both named "ids" that
// emits two `import "..."` lines both binding the name `ids`, i.e.
// `ids redeclared in this block` at `go build`, even though generate exited 0
// — a hard-invariant violation (generate must never emit non-compiling
// output). qf must now detect the name collision against this file's already
// -bound import names and allocate a fresh `_gsxti<N>` alias for the
// inferred type argument's import instead of colliding.
func TestInferredTypeArgImportCollision(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns the go toolchain")
	}
	repoRootAbs, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module example.com/tic\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRootAbs+"\n")
	compDir := filepath.Join(tmp, "components")
	if err := os.MkdirAll(compDir, 0o755); err != nil {
		t.Fatal(err)
	}
	idsDir := filepath.Join(tmp, "ids")
	if err := os.MkdirAll(idsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	otherIDsDir := filepath.Join(tmp, "other", "ids")
	if err := os.MkdirAll(otherIDsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, idsDir, "id.go", "package ids\n\ntype ID string\n")
	writeFile(t, otherIDsDir, "id.go", "package ids\n\nfunc Label() string { return \"other\" }\n")
	writeFile(t, compDir, "button.gsx", "package components\n\nimport \"example.com/tic/ids\"\n\ncomponent Button[T ~string](label T) {\n\t<button>{label}</button>\n}\n\nfunc NewID() ids.ID { return ids.ID(\"abc\") }\n")
	writeFile(t, tmp, "post.gsx", "package tic\n\nimport (\n\t\"example.com/tic/components\"\n\t\"example.com/tic/other/ids\"\n)\n\ncomponent Post() {\n\t<div>{ids.Label()}</div>\n\t<components.Button label={components.NewID()} />\n}\n")

	res, err := GenerateDirs(tmp, []string{tmp, compDir}, Options{FilterPkgs: []string{stdImportPath}, CSSMinify: true, JSMinify: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if diags := res[compDir].Diags; len(diags) != 0 {
		t.Fatalf("unexpected diagnostics generating components package: %+v", diags)
	}
	if diags := res[tmp].Diags; len(diags) != 0 {
		t.Fatalf("unexpected diagnostics generating root package: %+v", diags)
	}
	var root string
	for p, src := range res[tmp].Files {
		if strings.HasSuffix(p, "post.gsx") {
			root = string(src)
		}
	}
	if root == "" {
		t.Fatalf("post.gsx did not generate; files = %+v", res[tmp].Files)
	}
	if !strings.Contains(root, "_gsxty1.ID") {
		t.Fatalf("generated source does not alias the colliding inferred type-arg import; generated:\n%s", root)
	}

	for _, r := range res {
		for gsxPath, src := range r.Files {
			base := strings.TrimSuffix(gsxPath, ".gsx")
			if werr := os.WriteFile(base+".x.go", src, 0o644); werr != nil {
				t.Fatal(werr)
			}
		}
	}
	build := exec.Command("go", "build", "./...")
	build.Dir = tmp
	if bout, berr := build.CombinedOutput(); berr != nil {
		t.Fatalf("go build of generated output: %v\n%s", berr, bout)
	}
}

// TestInferredTypeArgImportNoCollision pins the generated-import invariant:
// even without a user-import collision, a type argument discovered during
// planning uses the reserved _gsxty namespace rather than introducing a
// caller-visible package binding.
func TestInferredTypeArgImportNoCollision(t *testing.T) {
	repoRootAbs, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module example.com/tinc\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRootAbs+"\n")
	compDir := filepath.Join(tmp, "components")
	if err := os.MkdirAll(compDir, 0o755); err != nil {
		t.Fatal(err)
	}
	idsDir := filepath.Join(tmp, "ids")
	if err := os.MkdirAll(idsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, idsDir, "id.go", "package ids\n\ntype ID string\n")
	writeFile(t, compDir, "button.gsx", "package components\n\nimport \"example.com/tinc/ids\"\n\ncomponent Button[T ~string](label T) {\n\t<button>{label}</button>\n}\n\nfunc NewID() ids.ID { return ids.ID(\"abc\") }\n")
	writeFile(t, tmp, "post.gsx", "package tinc\n\nimport \"example.com/tinc/components\"\n\ncomponent Post() {\n\t<components.Button label={components.NewID()} />\n}\n")

	res, err := GenerateDirs(tmp, []string{tmp, compDir}, Options{FilterPkgs: []string{stdImportPath}, CSSMinify: true, JSMinify: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if diags := res[tmp].Diags; len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %+v", diags)
	}
	var root string
	for _, src := range res[tmp].Files {
		root = string(src)
	}
	if !strings.Contains(root, `_gsxty1 "example.com/tinc/ids"`) {
		t.Fatalf("generated source missing reserved inferred type-arg import:\n%s", root)
	}
	if !strings.Contains(root, "components.Button[_gsxty1.ID]") {
		t.Fatalf("generated source missing reserved _gsxty type qualifier:\n%s", root)
	}
}

// TestPackageWideInferHelperNamesAcrossFiles reproduces the final whole-branch
// review's Critical-1 finding: buildSkeleton used to construct a FRESH
// inferRegistry per file (one call per .gsx file), and inferRegistry.nextName
// counted "_gsxinfer1", "_gsxinfer2", ... PER REGISTRY. Two sibling .gsx files
// in the SAME package (a.gsx and b.gsx below) each caller-side-infer against
// box.gsx's generic Box WITHOUT type args, so each file's skeleton hoists its
// own package-level `func _gsxinfer1[...]` — but every file's skeleton is
// type-checked TOGETHER as one package, so the two identically-named
// "_gsxinfer1" package-level funcs collide: `_gsxinfer1 redeclared in this
// block`, failing the WHOLE package even though neither .gsx file individually
// did anything wrong. The fix makes probe helper names unique PACKAGE-WIDE
// (module_importer.go's analyze shares one inferNameAllocator across every
// file's buildSkeleton call for the package) while keeping the registry
// itself — and its harvest/diagnostic lookups — per file.
func TestPackageWideInferHelperNamesAcrossFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns the go toolchain")
	}
	repoRootAbs, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module example.com/twofiles\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRootAbs+"\n")
	writeFile(t, tmp, "box.gsx", "package views\n\ncomponent Box[T string | int](value T) {\n\t<span>{value}</span>\n}\n")
	writeFile(t, tmp, "a.gsx", "package views\n\ncomponent A() {\n\t<Box value={1} />\n}\n")
	writeFile(t, tmp, "b.gsx", "package views\n\ncomponent B() {\n\t<Box value={\"hi\"} />\n}\n")

	res, err := GenerateDirs(tmp, []string{tmp}, Options{FilterPkgs: []string{stdImportPath}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if diags := res[tmp].Diags; len(diags) != 0 {
		t.Fatalf("unexpected diagnostics generating package with two sibling inferring files: %+v", diags)
	}
	if len(res[tmp].Files) != 3 {
		t.Fatalf("expected 3 generated files (box.gsx, a.gsx, b.gsx), got %d: %+v", len(res[tmp].Files), res[tmp].Files)
	}

	for gsxPath, src := range res[tmp].Files {
		base := strings.TrimSuffix(gsxPath, ".gsx")
		if werr := os.WriteFile(base+".x.go", src, 0o644); werr != nil {
			t.Fatal(werr)
		}
	}
	build := exec.Command("go", "build", "./...")
	build.Dir = tmp
	if bout, berr := build.CombinedOutput(); berr != nil {
		t.Fatalf("go build of generated output (two sibling files each inferring): %v\n%s", berr, bout)
	}
}

// TestInferredFilterPackageTypeArgUsesFilterAlias pins independent reserved
// namespaces when one package is both a filter source and the owner of an
// inferred type argument. Filter calls use _gsxfN; exact type rendering uses
// _gsxtyN. Both imports must remain bound and the result must build.
func TestInferredFilterPackageTypeArgUsesFilterAlias(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns the go toolchain")
	}
	repoRootAbs, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module example.com/filtertypearg\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRootAbs+"\n")

	tfDir := filepath.Join(tmp, "tfilters")
	if err := os.MkdirAll(tfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, tfDir, "tfilters.go", "package tfilters\n\ntype Kind string\n\nfunc Shout(s string) string { return s + \"!\" }\n\nfunc MakeKind(s string) Kind { return Kind(s) }\n")

	viewsDir := filepath.Join(tmp, "views")
	if err := os.MkdirAll(viewsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// value is never rendered inside Box: T's constraint is `any`, which
	// classify's typeparam handling treats as catUnsupported (an empty/`any`
	// term set), so interpolating {value} directly would fail Box's OWN
	// generation with an unrelated "unrenderable" diagnostic (see
	// writeUnexportedTypeArgModule's doc for the same fixture shape). The prop
	// only needs to exist for inference to run over it.
	writeFile(t, viewsDir, "page.gsx", "package views\n\ncomponent Box[T any](value T) {\n\t<span>box</span>\n}\n\ncomponent Page() {\n\t<div>{\"hi\" |> shout}</div>\n\t<Box value={\"hi\" |> makeKind} />\n}\n")

	res, err := GenerateDirs(tmp, []string{viewsDir}, Options{FilterPkgs: []string{stdImportPath, "example.com/filtertypearg/tfilters"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if diags := res[viewsDir].Diags; len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %+v", diags)
	}
	var page string
	for gsxPath, src := range res[viewsDir].Files {
		if strings.HasSuffix(gsxPath, "page.gsx") {
			page = string(src)
		}
		base := strings.TrimSuffix(gsxPath, ".gsx")
		if werr := os.WriteFile(base+".x.go", src, 0o644); werr != nil {
			t.Fatal(werr)
		}
	}
	if page == "" {
		t.Fatalf("page.gsx did not generate; files = %+v", res[viewsDir].Files)
	}
	if !strings.Contains(page, "Box[_gsxty1.Kind]") {
		t.Fatalf("generated source does not qualify the inferred filter-package type arg with the reserved type alias; generated:\n%s", page)
	}

	build := exec.Command("go", "build", "./...")
	build.Dir = tmp
	if bout, berr := build.CombinedOutput(); berr != nil {
		t.Fatalf("go build of generated output (inferred filter-package type arg): %v\n%s", berr, bout)
	}
}

func TestGeneratedImportAllocatorTransactions(t *testing.T) {
	caller := types.NewPackage("example.com/caller", "caller")
	depA := types.NewPackage("example.com/a/util", "util")
	depB := types.NewPackage("example.com/b/util", "util")

	t.Run("prefix drives alias naming", func(t *testing.T) {
		a := newGeneratedImportAllocator("_gsxty")
		if got := a.alloc("example.com/dep"); got != "_gsxty1" {
			t.Fatalf("alias = %q, want _gsxty1", got)
		}
		if got := a.alloc("example.com/dep"); got != "_gsxty1" {
			t.Fatalf("repeated path must reuse alias, got %q", got)
		}
		// The stable skeleton-requalification prefix is unchanged.
		ti := newGeneratedImportAllocator("_gsxti")
		if got := ti.alloc("example.com/x"); got != "_gsxti1" {
			t.Fatalf("requalification alias = %q, want _gsxti1", got)
		}
	})

	t.Run("rejected candidate leaks no import", func(t *testing.T) {
		a := newGeneratedImportAllocator("_gsxty")
		txn := a.begin()
		q := txn.qualifier(caller)
		_ = q(depA) // speculative allocation on the working copy
		if len(a.specs()) != 0 {
			t.Fatalf("uncommitted transaction leaked imports: %+v", a.specs())
		}
	})

	t.Run("qualifier leaves the current package unqualified and aliases foreign", func(t *testing.T) {
		a := newGeneratedImportAllocator("_gsxty")
		txn := a.begin()
		q := txn.qualifier(caller)
		if got := q(caller); got != "" {
			t.Fatalf("current package must be unqualified, got %q", got)
		}
		aliasA := q(depA)
		aliasB := q(depB)
		if aliasA == "" || aliasB == "" || aliasA == aliasB {
			t.Fatalf("two same-named packages must get distinct reserved aliases, got %q/%q", aliasA, aliasB)
		}
		txn.commit()
		if len(a.specs()) != 2 {
			t.Fatalf("commit must publish both imports, got %+v", a.specs())
		}
	})

	t.Run("commit publishes the winner", func(t *testing.T) {
		a := newGeneratedImportAllocator("_gsxty")
		txn := a.begin()
		txn.qualifier(caller)(depA)
		txn.commit()
		specs := a.specs()
		if len(specs) != 1 || specs[0].name != "_gsxty1" || specs[0].path != "example.com/a/util" {
			t.Fatalf("committed specs = %+v", specs)
		}
	})
}
