package codegen

import (
	goast "go/ast"
	goparser "go/parser"
	"go/token"
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
	genericSigs := genericSigsFor(files, byo)
	skel, _, _, _, registry, err := buildSkeleton(file, table, propFields, nodeProps, attrsProps, genericSigs, nil, byo, nil, fset, nil)
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
	writeFile(t, modelsDir, "models.go", "package models\n\ntype secret string\n\nfunc NewSecret() secret { return \"shh\" }\n\nfunc NewSecrets() []secret { return []secret{\"shh\"} }\n")
	writeFile(t, tmp, "post.gsx", "package "+modName+"\n\nimport (\n\t\"example.com/"+modName+"/components\"\n\tm \"example.com/"+modName+"/models\"\n)\n\ncomponent Post() {\n\t<components.Box value={"+valueExpr+"} />\n}\n")
	writeFile(t, tmp, "other.gsx", "package "+modName+"\n\ncomponent Other() {\n\t<p>ok</p>\n}\n")
	return tmp, compDir
}

// assertUnrenderableTypeArg asserts diags contains EXACTLY one
// unrenderable-type-arg diagnostic, that it is Error-severity and positioned,
// and that its message names the offending qualified type
// ("models.secret") — mirrors assertOnlyInferenceUnavailable's shape in
// generic_crosspkg_test.go for the sibling (Task 4) fail-safe, adapted to
// this Error-severity, hard-failure diagnostic.
func assertUnrenderableTypeArg(t *testing.T, diags []diag.Diagnostic) {
	t.Helper()
	var found int
	for _, d := range diags {
		if d.Code != "unrenderable-type-arg" {
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
		if !strings.Contains(d.Message, "models.secret") {
			t.Errorf("unrenderable-type-arg diagnostic does not name models.secret: %+v", d)
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
// argument is unspeakable at the call site. post.gsx's sole component (Post)
// fails outright (generateFile aborts the WHOLE FILE when any component
// fails — see emit.go's file-level ok tracking), so post.gsx has no
// generated output at all; the sibling other.gsx file in the same package
// must be unaffected, and the components package (which has no bad tags
// itself) must generate clean.
func TestInferredUnexportedTypeArgRejected(t *testing.T) {
	tmp, compDir := writeUnexportedTypeArgModule(t, "utar", "m.NewSecret()")

	res, err := GenerateDirs(tmp, []string{tmp, compDir}, Options{FilterPkgs: []string{stdImportPath}, CSSMinify: true, JSMinify: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if diags := res[compDir].Diags; len(diags) != 0 {
		t.Fatalf("unexpected diagnostics generating components package: %+v", diags)
	}
	assertUnrenderableTypeArg(t, res[tmp].Diags)

	for p := range res[tmp].Files {
		if strings.HasSuffix(p, "post.gsx") {
			t.Fatalf("post.gsx must not have generated output; files = %+v", res[tmp].Files)
		}
	}
	var otherGen string
	for p, src := range res[tmp].Files {
		if strings.HasSuffix(p, "other.gsx") {
			otherGen = string(src)
		}
	}
	if !strings.Contains(otherGen, "func Other() gsx.Node") {
		t.Fatalf("sibling file's generation affected by the rejected tag; other.gsx generated:\n%s", otherGen)
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
	assertUnrenderableTypeArg(t, res[tmp].Diags)

	for p := range res[tmp].Files {
		if strings.HasSuffix(p, "post.gsx") {
			t.Fatalf("post.gsx must not have generated output; files = %+v", res[tmp].Files)
		}
	}
}

// TestInferredUnexportedTypeArgRejectedOutputBuilds is the build-proof half:
// generation SUCCEEDS for every file except the rejected post.gsx (err ==
// nil, only the positioned diagnostic — never a hard error), and the WRITTEN
// output for everything that did generate must actually `go build` — the
// strongest possible property, mirroring
// TestGenericCrossPackageInferenceFailureOutputBuilds's shape. Unlike that
// Task 4 fail-safe (which sinks the failed tag's OWN file so it still
// builds), this Task 6 rejection drops the offending .gsx's output entirely;
// this test proves that omission alone is enough — the rest of the module
// builds fine without it.
func TestInferredUnexportedTypeArgRejectedOutputBuilds(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns the go toolchain")
	}
	tmp, compDir := writeUnexportedTypeArgModule(t, "utarb", "m.NewSecret()")

	res, err := GenerateDirs(tmp, []string{tmp, compDir}, Options{FilterPkgs: []string{stdImportPath}, CSSMinify: true, JSMinify: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertUnrenderableTypeArg(t, res[tmp].Diags)

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
