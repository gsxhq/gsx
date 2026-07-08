package gen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gsx fmt runs the analyzer already (for unused-import removal), and the analyzer
// already positions skeleton parse errors back at the .gsx via //line directives.
// These tests pin that fmt surfaces them instead of discarding them — while still
// formatting, since unlike gofmt it did produce correct output.

// strayImportGsx is Go that gsx parses (it treats Go as an opaque blob) but Go
// rejects: an `import` after a declaration. It surfaces only at the skeleton parse.
const strayImportGsx = `package views

import "github.com/gsxhq/gsx"

func render() gsx.Node {
	return <div>inline</div>
}

import "strings"

component Index() {
	<section>{ render() }</section>
}
`

// writeGsxModule makes dir a loadable module (via the existing writeModule) and
// drops the given name→content .gsx files into it.
func writeGsxModule(t *testing.T, dir, modPath string, files map[string]string) {
	t.Helper()
	writeModule(t, dir, modPath)
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestFmtReportsGoParseErrorAndStillFormats is the headline behavior: the Go
// error is reported with its .gsx position, the exit code is nonzero, and the
// formatted output is still produced (gofmt refuses to write only because it
// produced nothing; gsx produced correct output).
func TestFmtReportsGoParseErrorAndStillFormats(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeGsxModule(t, dir, "demo/stray", map[string]string{"input.gsx": strayImportGsx})

	code, out, errb := fmtCapture(t, []string{filepath.Join(dir, "input.gsx")})
	if code == 0 {
		t.Fatalf("exit = 0, want nonzero; stderr=%q", errb)
	}
	if !strings.Contains(errb, "imports must appear before other declarations") {
		t.Errorf("stderr missing the Go diagnostic:\n%s", errb)
	}
	if !strings.Contains(errb, "input.gsx:9") {
		t.Errorf("diagnostic not positioned at the .gsx stray import (want input.gsx:9):\n%s", errb)
	}
	if !strings.Contains(out, "component Index()") {
		t.Errorf("file was not formatted despite the Go error:\n%s", out)
	}
}

// TestFmtWriteStillWritesOnGoParseError pins the deliberate divergence from
// gofmt: -w writes the formatted file even though the Go is broken. The broken Go
// relays verbatim, so nothing is lost.
func TestFmtWriteStillWritesOnGoParseError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Give the component body loose indentation so formatting has something to do.
	src := strings.Replace(strayImportGsx, "\t<section>{ render() }</section>", "        <section>{ render() }</section>", 1)
	writeGsxModule(t, dir, "demo/straywrite", map[string]string{"input.gsx": src})
	path := filepath.Join(dir, "input.gsx")

	code, _, errb := fmtCapture(t, []string{"-w", path})
	if code == 0 {
		t.Fatalf("exit = 0, want nonzero; stderr=%q", errb)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(after), "\t<section>{ render() }</section>") {
		t.Errorf("-w did not write the formatted file:\n%s", after)
	}
	// The broken Go must survive verbatim.
	if !strings.Contains(string(after), `import "strings"`) {
		t.Errorf("-w dropped the invalid Go:\n%s", after)
	}
}

// TestFmtUnloadableModuleReportsNoDiagnostic is the regression guard: a module
// that cannot load (no `require`/`replace` for the gsx runtime it imports) must
// not start failing gsx fmt. Only skeleton PARSE errors are diagnostics; load
// failures stay silent, as they are today.
func TestFmtUnloadableModuleReportsNoDiagnostic(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// go.mod with no require/replace for github.com/gsxhq/gsx → module won't load.
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module demo/unloadable\n\ngo 1.26.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(dir, "good.gsx")
	if err := os.WriteFile(good, []byte(unformattedGsx), 0o644); err != nil {
		t.Fatal(err)
	}

	code, out, errb := fmtCapture(t, []string{good})
	if errb != "" {
		t.Errorf("unloadable module produced stderr output: %q", errb)
	}
	if code != 0 {
		t.Errorf("exit = %d, want 0 for an unloadable module", code)
	}
	if !strings.Contains(out, "component Hi(name string)") {
		t.Errorf("file was not formatted:\n%s", out)
	}
}

// TestFmtNoImportsSkipsDiagnostics pins that -no-imports (which skips module
// analysis entirely) also skips diagnostics — there is no analyzer to ask.
func TestFmtNoImportsSkipsDiagnostics(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeGsxModule(t, dir, "demo/noimports", map[string]string{"input.gsx": strayImportGsx})

	code, _, errb := fmtCapture(t, []string{"-no-imports", filepath.Join(dir, "input.gsx")})
	if errb != "" {
		t.Errorf("-no-imports produced stderr output: %q", errb)
	}
	if code != 0 {
		t.Errorf("exit = %d, want 0 with -no-imports", code)
	}
}

// TestFmtCleanFileInModuleIsSilent pins no behavior change for the common case:
// valid Go in a loadable module reports nothing and exits 0.
func TestFmtCleanFileInModuleIsSilent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeGsxModule(t, dir, "demo/clean", map[string]string{"input.gsx": unformattedGsx})

	code, out, errb := fmtCapture(t, []string{filepath.Join(dir, "input.gsx")})
	if errb != "" {
		t.Errorf("clean file produced stderr output: %q", errb)
	}
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if !strings.Contains(out, "component Hi(name string)") {
		t.Errorf("file was not formatted:\n%s", out)
	}
}

// TestFmtDiagnosticsOutsideFormatSetAreDropped pins that formatting only the
// clean sibling is silent, even though the package's skeleton carries a
// diagnostic for the broken file. `gsx fmt other.gsx` must not report on files
// the user did not ask it to touch.
func TestFmtDiagnosticsOutsideFormatSetAreDropped(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeGsxModule(t, dir, "demo/outside", map[string]string{
		"input.gsx": strayImportGsx,
		"other.gsx": "package views\n\ncomponent   Other() {\n    <p>x</p>\n}\n",
	})

	code, out, errb := fmtCapture(t, []string{filepath.Join(dir, "other.gsx")})
	if errb != "" {
		t.Errorf("reported a diagnostic for a file outside the format set: %q", errb)
	}
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if !strings.Contains(out, "component Other()") {
		t.Errorf("sibling not formatted:\n%s", out)
	}
}

// TestFmtGoParseErrorDoesNotStopSiblingFormatting pins per-file attribution: the
// broken file reports, the clean sibling still formats.
func TestFmtGoParseErrorDoesNotStopSiblingFormatting(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeGsxModule(t, dir, "demo/sibling", map[string]string{
		"input.gsx": strayImportGsx,
		"other.gsx": "package views\n\ncomponent   Other() {\n    <p>x</p>\n}\n",
	})

	code, out, errb := fmtCapture(t, []string{dir})
	if code == 0 {
		t.Fatalf("exit = 0, want nonzero; stderr=%q", errb)
	}
	if strings.Count(errb, "imports must appear") != 1 {
		t.Errorf("want exactly one diagnostic, got:\n%s", errb)
	}
	if !strings.Contains(out, "component Other() {\n\t<p>x</p>\n}") {
		t.Errorf("clean sibling not formatted:\n%s", out)
	}
}
