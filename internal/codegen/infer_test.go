package codegen

import (
	goast "go/ast"
	goparser "go/parser"
	"go/token"
	"path/filepath"
	"reflect"
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
	genericComps := genericCompsFor(componentsInFiles(files), byo)
	skel, _, _, _, registry, err := buildSkeleton(file, table, propFields, nodeProps, attrsProps, genericComps, nil, byo, nil, fset)
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
// a few edge cases (pointer/func recursion, explicit import alias, alias
// reuse across repeated qualifiers within one call, unresolvable qualifier,
// bare predeclared name) exercised beyond the brief's own table.
func TestRequalifyTypeExpr(t *testing.T) {
	fmtImport := []importSpec{{name: "", path: "fmt"}}
	pqImport := []importSpec{{name: "pq", path: "example.com/mod/other"}}

	tests := []struct {
		name        string
		src         string
		depAlias    string
		depImports  []importSpec
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls []importCall
			addImport := func(path, alias string) { calls = append(calls, importCall{path, alias}) }
			got, err := requalifyTypeExpr(tt.src, tt.depAlias, tt.depImports, addImport)
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
