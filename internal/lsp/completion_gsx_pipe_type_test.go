package lsp

import (
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/codegen"
)

// analyzeForPipe builds a throwaway module (go.mod + optional sibling Go files +
// one page.gsx), analyzes the page package TOLERATING diagnostics (a mid-edit
// pipeline with an unknown trailing stage type-errors by design), and returns
// the resulting LSP Package, the page path, and the resolved filter candidates.
// It mirrors adaptPackageResult for the fields typed pipe-filter narrowing reads
// (Info, Types, ExprMap, GSXFiles, Filters).
func analyzeForPipe(t *testing.T, files map[string]string, filterPkgs []string, gsxSrc string) (*Package, string, []FilterCandidate) {
	t.Helper()
	root := t.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	for name, content := range files {
		write(name, content)
	}
	write("page/page.gsx", gsxSrc)

	if len(filterPkgs) == 0 {
		filterPkgs = []string{codegen.StdImportPath}
	}
	m, err := codegen.Open(codegen.Options{ModuleRoot: root, ModulePath: "example.com/app", FilterPkgs: filterPkgs})
	if err != nil {
		t.Fatal(err)
	}
	pr, err := m.Package(filepath.Join(root, "page"))
	if err != nil {
		t.Fatal(err)
	}
	filters := make([]FilterCandidate, len(pr.Filters))
	for i, f := range pr.Filters {
		filters[i] = FilterCandidate{Name: f.Name, Pkg: f.Pkg, Func: f.Func, WantsCtx: f.WantsCtx}
	}
	pkg := &Package{
		GSXFset: pr.GSXFset,
		Fset:    pr.Fset,
		Info:    pr.Info,
		Types:   pr.Types,
		Files:   pr.GSXFiles,
		ExprMap: pr.ExprMap,
	}
	return pkg, filepath.Join(root, "page", "page.gsx"), filters
}

// pipeCursor computes the (seedOff, off) pair compatiblePipeFilters needs for a
// cursor sitting immediately after stageMarker, whose pipeline seed is the
// unique substring seedExpr.
func pipeCursor(t *testing.T, src, seedExpr, stageMarker string) (seedOff, off int) {
	t.Helper()
	seedOff = strings.Index(src, seedExpr)
	if seedOff < 0 {
		t.Fatalf("seed %q not found", seedExpr)
	}
	m := strings.Index(src, stageMarker)
	if m < 0 {
		t.Fatalf("stage marker %q not found", stageMarker)
	}
	return seedOff, m + len(stageMarker)
}

func filterNameSet(filters []FilterCandidate) map[string]bool {
	m := make(map[string]bool, len(filters))
	for _, f := range filters {
		m[f.Name] = true
	}
	return m
}

const pipeUser = `package page

type User struct {
	Name  string
	Age   int
	Tags  []string
	Blob  []byte
}

`

// TestCompatiblePipeFiltersStringSeed: a string-typed seed offers string-subject
// filters (upper), the any-subject filter (printf) and the generic filter
// (default), and excludes filters whose subject is []string (join) or []byte
// (dataURL). A companion valid pipe (`join`) imports std into the skeleton
// universe so candidate signatures resolve.
func TestCompatiblePipeFiltersStringSeed(t *testing.T) {
	src := pipeUser + "component Home(user User) {\n\t<div>{ user.Tags |> join(\", \") }{ user.Name |> zz }</div>\n}\n"
	pkg, path, filters := analyzeForPipe(t, nil, nil, src)
	seedOff, off := pipeCursor(t, src, "user.Name", "|> zz")
	got, ok := compatiblePipeFilters(pkg, path, seedOff, off, filters)
	if !ok {
		t.Fatal("compatiblePipeFilters ok=false, want true (string seed resolves)")
	}
	labels := filterNameSet(got)
	for _, want := range []string{"upper", "printf", "default", "urlquery"} {
		if !labels[want] {
			t.Errorf("string seed: filter %q missing; got %v", want, labels)
		}
	}
	for _, excl := range []string{"join", "dataURL"} {
		if labels[excl] {
			t.Errorf("string seed: filter %q offered but subject is incompatible; got %v", excl, labels)
		}
	}
}

// TestCompatiblePipeFiltersIntSeed: an int-typed seed offers only the any-subject
// (printf) and generic (default) filters; every string-subject filter (upper,
// urlquery) and the []string/[]byte-subject filters (join, dataURL) are excluded.
func TestCompatiblePipeFiltersIntSeed(t *testing.T) {
	src := pipeUser + "component Home(user User) {\n\t<div>{ user.Tags |> join(\", \") }{ user.Age |> zz }</div>\n}\n"
	pkg, path, filters := analyzeForPipe(t, nil, nil, src)
	seedOff, off := pipeCursor(t, src, "user.Age", "|> zz")
	got, ok := compatiblePipeFilters(pkg, path, seedOff, off, filters)
	if !ok {
		t.Fatal("compatiblePipeFilters ok=false, want true (int seed resolves)")
	}
	labels := filterNameSet(got)
	for _, want := range []string{"printf", "default"} {
		if !labels[want] {
			t.Errorf("int seed: filter %q missing; got %v", want, labels)
		}
	}
	for _, excl := range []string{"upper", "urlquery", "join", "dataURL"} {
		if labels[excl] {
			t.Errorf("int seed: filter %q offered but subject is incompatible; got %v", excl, labels)
		}
	}
}

// TestCompatiblePipeFiltersSecondStage: a second-stage cursor resolves its
// incoming type from the PRECEDING filter's result (upper → string), not from
// the raw pipe node, and narrows accordingly — string-subject filters offered,
// []string/[]byte-subject filters excluded. The stronger "result type differs
// from the seed type" proof lives in TestCompatiblePipeFiltersErrFilterMidPipe
// (toStr: int → string). A no-args preceding stage is used deliberately: a
// preceding stage that carries ARGS combined with an unknown trailing stage
// currently yields an empty analysis (a codegen edge), which simply routes to
// the full-list fail-open.
func TestCompatiblePipeFiltersSecondStage(t *testing.T) {
	src := pipeUser + "component Home(user User) {\n\t<div>{ user.Tags |> join(\", \") }{ user.Name |> upper |> zz }</div>\n}\n"
	pkg, path, filters := analyzeForPipe(t, nil, nil, src)
	seedOff, off := pipeCursor(t, src, "user.Name", "|> zz")
	got, ok := compatiblePipeFilters(pkg, path, seedOff, off, filters)
	if !ok {
		t.Fatal("compatiblePipeFilters ok=false, want true (upper result resolves)")
	}
	labels := filterNameSet(got)
	if !labels["upper"] {
		t.Errorf("second stage: upper missing — incoming should be upper's string result; got %v", labels)
	}
	for _, excl := range []string{"join", "dataURL"} {
		if labels[excl] {
			t.Errorf("second stage: %q offered but string is not assignable to its subject; got %v", excl, labels)
		}
	}
}

// TestCompatiblePipeFiltersUnresolvableFailsOpen: an untyped/invalid seed (a
// non-existent field) cannot yield an incoming type, so narrowing fails OPEN —
// ok=false — and the caller offers the full list.
func TestCompatiblePipeFiltersUnresolvableFailsOpen(t *testing.T) {
	src := pipeUser + "component Home(user User) {\n\t<div>{ user.Bogus |> zz }</div>\n}\n"
	pkg, path, filters := analyzeForPipe(t, nil, nil, src)
	seedOff, off := pipeCursor(t, src, "user.Bogus", "|> zz")
	if _, ok := compatiblePipeFilters(pkg, path, seedOff, off, filters); ok {
		t.Fatal("compatiblePipeFilters ok=true, want false (broken seed must fail open)")
	}
}

const ctxErrFilters = `package myf

import "context"

// CtxUpper takes the ambient context first, then a string subject.
func CtxUpper(ctx context.Context, s string) string { return s }

// NeedInt has an int subject.
func NeedInt(n int) string { return "" }

// ToStr is an (R, error) filter: int subject, string result.
func ToStr(n int) (string, error) { return "", nil }
`

// TestCompatiblePipeFiltersCtxSubjectIndex: a ctx-taking filter's SUBJECT is
// parameter 1 (after context.Context), so a string seed offers ctxUpper (string
// subject) and excludes needInt (int subject) and toStr (int subject). Exercises
// rule 3 end to end. A companion valid pipe imports the custom filter package.
func TestCompatiblePipeFiltersCtxSubjectIndex(t *testing.T) {
	files := map[string]string{"myf/myf.go": ctxErrFilters}
	pkgs := []string{codegen.StdImportPath, "example.com/app/myf"}
	src := pipeUser + "component Home(user User) {\n\t<div>{ user.Age |> needInt }{ user.Name |> zz }</div>\n}\n"
	pkg, path, filters := analyzeForPipe(t, files, pkgs, src)
	seedOff, off := pipeCursor(t, src, "user.Name", "|> zz")
	got, ok := compatiblePipeFilters(pkg, path, seedOff, off, filters)
	if !ok {
		t.Fatal("compatiblePipeFilters ok=false, want true")
	}
	labels := filterNameSet(got)
	if !labels["ctxUpper"] {
		t.Errorf("ctx filter: ctxUpper missing — its subject (param 1) is string; got %v", labels)
	}
	for _, excl := range []string{"needInt", "toStr"} {
		if labels[excl] {
			t.Errorf("ctx filter: %q offered but its int subject rejects a string seed; got %v", excl, labels)
		}
	}
}

// TestCompatiblePipeFiltersErrFilterMidPipe: an (R, error) filter chains its R
// into the next stage. `int |> toStr |> ▮` has string incoming (toStr's string
// result), so upper/ctxUpper are offered and the int-subject needInt is excluded.
// Exercises rule 4 end to end.
func TestCompatiblePipeFiltersErrFilterMidPipe(t *testing.T) {
	files := map[string]string{"myf/myf.go": ctxErrFilters}
	pkgs := []string{codegen.StdImportPath, "example.com/app/myf"}
	src := pipeUser + "component Home(user User) {\n\t<div>{ user.Age |> needInt }{ user.Age |> toStr |> zz }</div>\n}\n"
	pkg, path, filters := analyzeForPipe(t, files, pkgs, src)
	// Second occurrence of user.Age is the cursor pipe's seed.
	first := strings.Index(src, "user.Age")
	seedOff := strings.Index(src[first+1:], "user.Age") + first + 1
	off := strings.Index(src, "|> zz") + len("|> zz")
	got, ok := compatiblePipeFilters(pkg, path, seedOff, off, filters)
	if !ok {
		t.Fatal("compatiblePipeFilters ok=false, want true (toStr string result resolves)")
	}
	labels := filterNameSet(got)
	for _, want := range []string{"upper", "ctxUpper"} {
		if !labels[want] {
			t.Errorf("err filter mid-pipe: %q missing — incoming should be toStr's string result; got %v", want, labels)
		}
	}
	if labels["needInt"] {
		t.Errorf("err filter mid-pipe: needInt offered but string is not assignable to its int subject; got %v", labels)
	}
}

// TestFilterSubjectType pins the subject-parameter index rules: parameter 0 for
// a ctx-less filter, parameter 1 for a ctx-taking one, and the ELEMENT type when
// the subject is itself the trailing variadic parameter.
func TestFilterSubjectType(t *testing.T) {
	// filterSubjectType keys off the wantsCtx flag, not the leading parameter's
	// concrete type, so a stand-in stands in for context.Context here.
	ctxType := types.Typ[types.Bool]
	str := types.Typ[types.String]
	newParam := func(ts ...types.Type) *types.Tuple {
		vars := make([]*types.Var, len(ts))
		for i, ty := range ts {
			vars[i] = types.NewVar(token.NoPos, nil, "", ty)
		}
		return types.NewTuple(vars...)
	}
	res := newParam(str)

	// ctx-less: subject is param 0.
	sig := types.NewSignatureType(nil, nil, nil, newParam(str), res, false)
	if got, ok := filterSubjectType(sig, false); !ok || got != str {
		t.Errorf("ctx-less subject = %v, %v; want string", got, ok)
	}
	// ctx-taking: subject is param 1.
	sig = types.NewSignatureType(nil, nil, nil, newParam(ctxType, str), res, false)
	if got, ok := filterSubjectType(sig, true); !ok || got != str {
		t.Errorf("ctx subject = %v, %v; want string (param 1)", got, ok)
	}
	// variadic subject: element type.
	sig = types.NewSignatureType(nil, nil, nil, newParam(types.NewSlice(str)), res, true)
	if got, ok := filterSubjectType(sig, false); !ok || got != str {
		t.Errorf("variadic subject = %v, %v; want string element", got, ok)
	}
}

// TestSubjectAccepts pins the compatibility rule: a type-parameter subject always
// accepts, an any/interface subject accepts anything, an exact match accepts, and
// a concrete mismatch is rejected.
func TestSubjectAccepts(t *testing.T) {
	str := types.Typ[types.String]
	i := types.Typ[types.Int]
	anyIface := types.NewInterfaceType(nil, nil) // empty interface == any

	tp := types.NewTypeParam(types.NewTypeName(token.NoPos, nil, "T", nil), types.NewInterfaceType(nil, nil))
	if !subjectAccepts(tp, i) {
		t.Error("type-parameter subject must accept any incoming type")
	}
	if !subjectAccepts(anyIface, i) {
		t.Error("any subject must accept int")
	}
	if !subjectAccepts(str, str) {
		t.Error("string subject must accept string")
	}
	if subjectAccepts(str, i) {
		t.Error("string subject must reject int")
	}
}
