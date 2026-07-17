package gen

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/attrclass"
	"github.com/gsxhq/gsx/internal/codegen"
)

// fmtCapture drives runFmt with captured stdout/stderr and returns code+output.
func fmtCapture(t *testing.T, args []string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	wd, _ := os.Getwd()
	code := runFmt(&out, &errb, args, nil, nil, codegen.Options{Classifier: attrclass.Builtin()}, wd)
	return code, out.String(), errb.String()
}

// unformattedGsx is a parseable but non-canonical source (extra blank lines and
// loose indentation) so fmt has something to change.
const unformattedGsx = `package views



component   Hi(name string) {
    <p>{name}</p>
}
`

// TestFmtDefaultStdout proves the default mode writes formatted output to stdout
// and does not touch the file on disk.
func TestFmtDefaultStdout(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "hi.gsx")
	if err := os.WriteFile(p, []byte(unformattedGsx), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errb := fmtCapture(t, []string{p})
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%q)", code, errb)
	}
	if !strings.Contains(out, "component Hi(name string)") {
		t.Errorf("stdout missing formatted component:\n%s", out)
	}
	// The file on disk is untouched.
	onDisk, _ := os.ReadFile(p)
	if string(onDisk) != unformattedGsx {
		t.Errorf("default mode modified the file on disk")
	}
}

func TestFormatRejectsMalformedComposedAttributeMissingComma(t *testing.T) {
	t.Parallel()
	const src = `package views

component Meter(value int, color string) {
	<div
		class={ "meter", "meter-full": value >= 100 }
		style={ value |> printf("width: %d%%") "color: " + color }
	/>
}
`
	formatted, err := Format("playground.gsx", []byte(src))
	if err == nil {
		t.Fatalf("Format succeeded for malformed composed attrs; output:\n%s", formatted)
	}
}

// TestFmtListUnformatted proves -l lists an unformatted file and exits 1.
func TestFmtListUnformatted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "hi.gsx")
	if err := os.WriteFile(p, []byte(unformattedGsx), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errb := fmtCapture(t, []string{"-l", p})
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (stderr=%q)", code, errb)
	}
	if strings.TrimSpace(out) != p {
		t.Errorf("-l stdout = %q, want %q", out, p)
	}
}

// TestFmtListFormatted proves -l on an already-canonical file exits 0 with no
// output. The canonical form is obtained by running fmt's default mode first.
func TestFmtListFormatted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "hi.gsx")
	if err := os.WriteFile(p, []byte(unformattedGsx), 0o644); err != nil {
		t.Fatal(err)
	}
	// Capture the canonical form via default mode, write it back.
	_, canonical, _ := fmtCapture(t, []string{p})
	if err := os.WriteFile(p, []byte(canonical), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errb := fmtCapture(t, []string{"-l", p})
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%q)", code, errb)
	}
	if out != "" {
		t.Errorf("-l on formatted file printed %q, want empty", out)
	}
}

// TestFmtWriteIdempotent proves -w rewrites a changed file and is a no-op on the
// second run.
func TestFmtWriteIdempotent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "hi.gsx")
	if err := os.WriteFile(p, []byte(unformattedGsx), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errb := fmtCapture(t, []string{"-w", p})
	if code != 0 {
		t.Fatalf("first -w exit = %d, want 0 (stderr=%q)", code, errb)
	}
	if out != "" {
		t.Errorf("-w wrote to stdout: %q", out)
	}
	after1, _ := os.ReadFile(p)
	if string(after1) == unformattedGsx {
		t.Errorf("-w did not change the unformatted file")
	}
	if !strings.Contains(string(after1), "component Hi(name string)") {
		t.Errorf("-w produced unexpected content:\n%s", after1)
	}
	// Second run is a no-op: content identical.
	code2, _, errb2 := fmtCapture(t, []string{"-w", p})
	if code2 != 0 {
		t.Fatalf("second -w exit = %d, want 0 (stderr=%q)", code2, errb2)
	}
	after2, _ := os.ReadFile(p)
	if !bytes.Equal(after1, after2) {
		t.Errorf("-w is not idempotent:\nfirst:\n%s\nsecond:\n%s", after1, after2)
	}
}

// TestFmtParseError proves a parse-error file is reported to stderr and exits 1,
// while other files in the same invocation still format.
func TestFmtParseError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.gsx")
	if err := os.WriteFile(bad, []byte("package views\n\ncomponent Broken( {\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(dir, "good.gsx")
	if err := os.WriteFile(good, []byte(unformattedGsx), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errb := fmtCapture(t, []string{bad, good})
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (stderr=%q)", code, errb)
	}
	if !strings.Contains(errb, "bad.gsx") {
		t.Errorf("stderr missing bad file: %q", errb)
	}
	// The good file still produced formatted output on stdout.
	if !strings.Contains(out, "component Hi(name string)") {
		t.Errorf("good file was not formatted despite sibling parse error:\n%s", out)
	}
}

// TestFmtDirRecursive proves a directory arg recurses for .gsx files and skips
// junk dirs (here a hidden dir).
func TestFmtDirRecursive(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.gsx"), []byte(unformattedGsx), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "b.gsx"), []byte(unformattedGsx), 0o644); err != nil {
		t.Fatal(err)
	}
	hidden := filepath.Join(dir, ".hidden")
	if err := os.MkdirAll(hidden, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hidden, "skip.gsx"), []byte(unformattedGsx), 0o644); err != nil {
		t.Fatal(err)
	}

	code, out, errb := fmtCapture(t, []string{"-l", dir})
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (stderr=%q)", code, errb)
	}
	if !strings.Contains(out, filepath.Join(dir, "a.gsx")) {
		t.Errorf("-l missing a.gsx:\n%s", out)
	}
	if !strings.Contains(out, filepath.Join(sub, "b.gsx")) {
		t.Errorf("-l missing sub/b.gsx:\n%s", out)
	}
	if strings.Contains(out, "skip.gsx") {
		t.Errorf("-l descended into hidden dir:\n%s", out)
	}
}

// TestFmtDiff proves -d emits a unified-diff-style block for a changed file and
// exits 1.
func TestFmtDiff(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "hi.gsx")
	if err := os.WriteFile(p, []byte(unformattedGsx), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errb := fmtCapture(t, []string{"-d", p})
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (stderr=%q)", code, errb)
	}
	if !strings.Contains(out, "--- "+p+".orig") || !strings.Contains(out, "+++ "+p) {
		t.Errorf("-d output missing diff headers:\n%s", out)
	}
	if !strings.Contains(out, "@@") {
		t.Errorf("-d output missing hunk header:\n%s", out)
	}
}

// TestFmtRemovesUnusedImport: `gsx fmt -w` drops an unused import by default.
func TestFmtRemovesUnusedImport(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	w := func(p, c string) {
		if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w("go.mod", "module example.com/u\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	p := filepath.Join(dir, "c.gsx")
	w("c.gsx", "package u\n\nimport \"strings\"\n\ncomponent C() {\n\t<p>hi</p>\n}\n")

	if code, _, errb := fmtCapture(t, []string{"-w", p}); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errb)
	}
	after, _ := os.ReadFile(p)
	if strings.Contains(string(after), "strings") {
		t.Fatalf("unused import not removed by default:\n%s", after)
	}
}

// TestFmtNoImportsKeepsUnused: `-no-imports` skips removal (syntactic only).
func TestFmtNoImportsKeepsUnused(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	w := func(p, c string) {
		if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w("go.mod", "module example.com/u\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	p := filepath.Join(dir, "c.gsx")
	w("c.gsx", "package u\n\nimport \"strings\"\n\ncomponent C() {\n\t<p>hi</p>\n}\n")

	if code, _, errb := fmtCapture(t, []string{"-no-imports", "-w", p}); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errb)
	}
	after, _ := os.ReadFile(p)
	if !strings.Contains(string(after), "strings") {
		t.Fatalf("-no-imports should keep the import:\n%s", after)
	}
}

// TestFmtOutsideModuleFallsBack: a .gsx not in any module is still formatted
// (syntactically); the unused import is kept and the exit code is success.
func TestFmtOutsideModuleFallsBack(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := t.TempDir() // no go.mod
	p := filepath.Join(dir, "c.gsx")
	if err := os.WriteFile(p, []byte("package u\n\nimport   \"strings\"\n\ncomponent C() {\n\t<p>hi</p>\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errb := fmtCapture(t, []string{"-w", p})
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s (formatting must not fail outside a module)", code, errb)
	}
	after, _ := os.ReadFile(p)
	if !strings.Contains(string(after), "strings") {
		t.Fatalf("outside a module the import must be kept (no analysis):\n%s", after)
	}
	if strings.Contains(string(after), "import   \"strings\"") {
		t.Fatalf("file was not syntactically formatted:\n%s", after)
	}
}

// TestFmtRemovesUnusedKeepsUsed proves the rewired analyzeUnusedImports (one
// codegen.Module per module, via groupByModule) still removes an unused import
// while keeping a used one, for a module containing a single directory.
func TestFmtRemovesUnusedKeepsUsed(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := newModule(t, "fmtmod")
	src := "package fmtmod\n\nimport (\n\t\"strings\"\n\t\"bytes\"\n)\n\ncomponent Page() {\n\t<div>{strings.ToUpper(\"x\")}</div>\n}\n"
	page := writeFile(t, dir, "page.gsx", src)

	code, _, errb := fmtCapture(t, []string{"-w", page})
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errb)
	}
	got, err := os.ReadFile(page)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), `"bytes"`) {
		t.Errorf("unused bytes import should be removed:\n%s", got)
	}
	if !strings.Contains(string(got), `"strings"`) {
		t.Errorf("used strings import must be kept:\n%s", got)
	}
}

// TestFmtTwoDirsOneModule proves analyzeUnusedImports (the grouped
// one-codegen.Module-per-module path, via groupByModule) returns correct,
// independent results for two DIFFERENT directories of the SAME module in a
// single call: a's unused bytes import is reported, b's used strings import is
// not — confirming the shared Module correctly resolves each directory's own
// package rather than conflating them.
func TestFmtTwoDirsOneModule(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := newModule(t, "fmtmod")
	aSrc := "package a\n\nimport \"bytes\"\n\ncomponent A() {\n\t<p>hi</p>\n}\n"
	bSrc := "package b\n\nimport \"strings\"\n\ncomponent B() {\n\t<p>{strings.ToUpper(\"x\")}</p>\n}\n"
	aPath := writeFile(t, filepath.Join(dir, "a"), "a.gsx", aSrc)
	bPath := writeFile(t, filepath.Join(dir, "b"), "b.gsx", bSrc)

	refs, _ := analyzeUnusedImports(
		[]string{aPath, bPath},
		codegen.Options{Classifier: attrclass.Builtin()},
	)

	aAbs, _ := filepath.Abs(aPath)
	bAbs, _ := filepath.Abs(bPath)
	if len(refs[aAbs]) != 1 || refs[aAbs][0].Path != "bytes" {
		t.Errorf("a: want bytes unused, got %+v", refs[aAbs])
	}
	if len(refs[bAbs]) != 0 {
		t.Errorf("b: strings is used, want none unused, got %+v", refs[bAbs])
	}
}

// TestFmtKeepsTypeArgAndAttrExprImports pins the detector fix for imports
// referenced ONLY in positions the syntactic unused-import scan used to miss: a
// markup type-argument (<comp.Check[cons.Foo]> uses cons) and a component-tag
// attribute-value Go expression (attrs={{ "@x": gsx.RawJS(...) }} uses gsx).
// componentTargetQualifiers formerly harvested only the tag qualifier, so fmt
// stripped both imports and broke the file's compilation. Detection is
// syntactic, so the referenced comp/cons packages need not exist; a genuinely
// unused import (bytes) in the same file must still be the only one reported, to
// prove the fix keeps used imports without disabling removal.
func TestFmtKeepsTypeArgAndAttrExprImports(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := newModule(t, "fmtmod")
	src := "package views\n\nimport (\n\t\"bytes\"\n\n\t\"github.com/gsxhq/gsx\"\n\n\t\"fmtmod/comp\"\n\t\"fmtmod/cons\"\n)\n\n" +
		"component Page(d bool) {\n\t<comp.Check[cons.Foo] checked={d} attrs={{ \"@x\": gsx.RawJS(\"f()\") }} />\n}\n"
	page := writeFile(t, filepath.Join(dir, "views"), "views.gsx", src)

	refs, _ := analyzeUnusedImports(
		[]string{page},
		codegen.Options{Classifier: attrclass.Builtin()},
	)
	abs, _ := filepath.Abs(page)
	if len(refs[abs]) != 1 || refs[abs][0].Path != "bytes" {
		t.Fatalf("want only bytes unused (cons used in type-arg, gsx in attr expr, comp in tag), got %+v", refs[abs])
	}
}

// TestFmtToleratesMalformedConfig proves that with a builtin-only
// codegen.Options (the fallback gen/main.go's `fmt` case uses when
// resolveConfig fails on a malformed gsx.toml), formatting still succeeds.
func TestFmtToleratesMalformedConfig(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := newModule(t, "fmtmod")
	page := writeFile(t, dir, "page.gsx", "package fmtmod\n\ncomponent Page() {\n\t<div>hi</div>\n}\n")

	code, _, errb := fmtCapture(t, []string{"-l", page})
	if code != 0 { // already canonical → no diff → 0
		t.Fatalf("exit=%d stderr=%s", code, errb)
	}
}

// newFmtModule creates a temp dir containing a go.mod that replaces gsx with the
// repo under test, ready for module-resolving fmt runs.
func newFmtModule(t *testing.T) string {
	t.Helper()
	return newModule(t, "example.com/u")
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestFmtGoimportsMergesAndDedups: the default mode merges a single-line import
// with a grouped one and drops the duplicate.
func TestFmtGoimportsMergesAndDedups(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := newFmtModule(t)
	src := "package u\n\nimport \"strings\"\n\nimport (\n\t\"fmt\"\n\n\t\"strings\"\n)\n\n" +
		"component C() {\n\t<p>{ fmt.Sprint(strings.ToUpper(\"x\")) }</p>\n}\n"
	p := filepath.Join(dir, "c.gsx")
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := runFmt(&out, &errb, []string{"-w", p}, nil, nil, codegen.Options{}, dir); code != 0 {
		t.Fatalf("runFmt=%d stderr=%s", code, errb.String())
	}
	got := readFile(t, p)
	if n := strings.Count(got, "\"strings\""); n != 1 {
		t.Fatalf("duplicate not deduped (%d):\n%s", n, got)
	}
	if n := strings.Count(got, "import"); n != 1 {
		t.Fatalf("declarations not merged (%d import keywords):\n%s", n, got)
	}
}

// TestFmtImportsGofmtLeavesImportsAlone: -imports gofmt keeps the duplicate, the
// two declarations, AND an unused import (gofmt never removes).
func TestFmtImportsGofmtLeavesImportsAlone(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := newFmtModule(t)
	src := "package u\n\nimport \"bytes\"\n\nimport (\n\t\"fmt\"\n\n\t\"bytes\"\n)\n\n" +
		"component C() {\n\t<p>{ fmt.Sprint(1) }</p>\n}\n"
	p := filepath.Join(dir, "c.gsx")
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := runFmt(&out, &errb, []string{"-w", "-imports", "gofmt", p}, nil, nil, codegen.Options{}, dir); code != 0 {
		t.Fatalf("runFmt=%d stderr=%s", code, errb.String())
	}
	got := readFile(t, p)
	if n := strings.Count(got, "\"bytes\""); n != 2 {
		t.Fatalf("gofmt mode must keep the unused duplicate (%d):\n%s", n, got)
	}
	if n := strings.Count(got, "import"); n != 2 {
		t.Fatalf("gofmt mode must keep both declarations (%d):\n%s", n, got)
	}
}

// TestFmtNoImportsIsGofmtAlias: -no-imports behaves exactly like -imports gofmt.
func TestFmtNoImportsIsGofmtAlias(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	src := "package u\n\nimport \"bytes\"\n\nimport (\n\t\"fmt\"\n\n\t\"bytes\"\n)\n\n" +
		"component C() {\n\t<p>{ fmt.Sprint(1) }</p>\n}\n"
	run := func(args ...string) string {
		dir := newFmtModule(t)
		p := filepath.Join(dir, "c.gsx")
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		var out, errb bytes.Buffer
		if code := runFmt(&out, &errb, append(args, p), nil, nil, codegen.Options{}, dir); code != 0 {
			t.Fatalf("runFmt=%d stderr=%s", code, errb.String())
		}
		return readFile(t, p)
	}
	if a, b := run("-w", "-no-imports"), run("-w", "-imports", "gofmt"); a != b {
		t.Fatalf("-no-imports != -imports gofmt:\n%s\n---\n%s", a, b)
	}
}

// TestFmtImportsFlagConflict: -imports goimports with -no-imports is a usage
// error (exit 2), not a silent winner.
func TestFmtImportsFlagConflict(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	var out, errb bytes.Buffer
	code := runFmt(&out, &errb, []string{"-imports", "goimports", "-no-imports", dir}, nil, nil, codegen.Options{}, dir)
	if code != 2 {
		t.Fatalf("exit = %d, want 2; stderr=%s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "-no-imports") {
		t.Fatalf("stderr must explain the conflict: %s", errb.String())
	}
}

// TestFmtImportsFlagInvalid: an unknown -imports value is exit 2 naming both
// valid spellings.
func TestFmtImportsFlagInvalid(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	var out, errb bytes.Buffer
	code := runFmt(&out, &errb, []string{"-imports", "gofumpt", dir}, nil, nil, codegen.Options{}, dir)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	for _, want := range []string{"gofmt", "goimports"} {
		if !strings.Contains(errb.String(), want) {
			t.Fatalf("stderr %q must name %q", errb.String(), want)
		}
	}
}

// TestFmtConfigGofmtModeHonored: [formatter] imports = "gofmt" in gsx.toml is
// honored with no CLI flag.
func TestFmtConfigGofmtModeHonored(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := newFmtModule(t)
	if err := os.WriteFile(filepath.Join(dir, "gsx.toml"), []byte("[formatter]\nimports = \"gofmt\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := "package u\n\nimport \"bytes\"\n\ncomponent C() {\n\t<p>hi</p>\n}\n"
	p := filepath.Join(dir, "c.gsx")
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := runFmt(&out, &errb, []string{"-w", p}, nil, nil, codegen.Options{}, dir); code != 0 {
		t.Fatalf("runFmt=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(readFile(t, p), "\"bytes\"") {
		t.Fatal("gofmt mode from gsx.toml must keep the unused import")
	}
}
