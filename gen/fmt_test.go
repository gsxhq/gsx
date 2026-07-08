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
		style={ value |> format("width: %d%%") "color: " + color }
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
