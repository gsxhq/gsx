package codegen

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestModuleIgnoresStaleOnDiskXGo proves the core on-disk-.x.go-independence
// invariant: if a stale generated .x.go exists on disk, Module.Generate must
// produce the correct output from the in-memory skeleton (derived from the .gsx
// source) and must NOT be influenced by the stale disk content.
//
// Non-vacuity is guaranteed by two complementary checks:
//
//  1. Scope check (direct, mechanical): the stale file declares a unique
//     exported variable StaleMarker. If the skip guard (compsByXGo exclusion
//     in analyze) is removed, the stale file IS fed to the type-checker and
//     StaleMarker lands in the package scope. With the guard intact, StaleMarker
//     is absent — proving the stale file was never seen by the type-checker.
//
//  2. Generate output check: Module.Generate must produce func Home( in the
//     output and must not contain StaleMarker (belt-and-suspenders check that
//     the emitted code is derived from the .gsx, not the stale disk file).
func TestModuleIgnoresStaleOnDiskXGo(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repoRoot, _ := filepath.Abs("../..")
	writeFile(t, root, "go.mod",
		"module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")

	pageDir := filepath.Join(root, "page")
	if err := os.MkdirAll(pageDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Source: a simple component.
	gsxPath := filepath.Join(pageDir, "page.gsx")
	writeFile(t, pageDir, "page.gsx",
		"package page\n\ncomponent Home() {\n\t<h1>hello</h1>\n}\n")

	// Stale on-disk .x.go — simulates a previously-generated file left on disk.
	// The unique declaration (var StaleMarker) acts as a canary: if this file is
	// fed to the type-checker its symbol appears in the package scope, making the
	// scope check below fail. A plain func Wrong() {} would not reliably surface
	// because it doesn't conflict with the skeleton and wouldn't affect resolved
	// types or emitted output — this variable is the actual detection mechanism.
	staleXGoPath := filepath.Join(pageDir, "page.x.go")
	staleContent := "package page\n\n// StaleMarker is a canary: its presence in the type-checker scope\n// proves the stale file was wrongly included.\nvar StaleMarker = \"STALE\"\n"
	if err := os.WriteFile(staleXGoPath, []byte(staleContent), 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := Open(Options{
		ModuleRoot: root,
		ModulePath: "example.com/app",
		FilterPkgs: []string{StdImportPath},
	})
	if err != nil {
		t.Fatal(err)
	}

	// --- Check 1: scope-level invariant ---
	// Call analyze directly (same package) to inspect the type-checker's
	// package scope. StaleMarker must NOT be there — if it is, the stale file
	// was included in the type-check (skip guard broken).
	ext, err := m.externalImporter()
	if err != nil {
		t.Fatalf("externalImporter: %v", err)
	}
	mi := &moduleImporter{m: m, external: ext, seen: map[string]bool{}}
	a, err := m.analyze(pageDir, mi, analysisTypeOnly)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if a.pkg != nil {
		if obj := a.pkg.Scope().Lookup("StaleMarker"); obj != nil {
			t.Errorf("stale page.x.go was fed to the type-checker: StaleMarker found in package scope (%v) — skip guard broken", obj)
		}
	}

	// --- Check 2: Generate output ---
	out, diags, err := m.Generate(pageDir)
	if err != nil {
		t.Fatalf("Generate: %v (diags=%v)", err, diags)
	}

	got := string(out[gsxPath])
	if got == "" {
		t.Fatalf("Generate produced no output for page.gsx; out=%v", out)
	}

	// Must contain the correct component function.
	if !strings.Contains(got, "func Home(") {
		t.Errorf("generated output missing func Home(; got:\n%s", got)
	}

	// Must NOT contain StaleMarker from the stale on-disk .x.go.
	if strings.Contains(got, "StaleMarker") {
		t.Errorf("generated output contains StaleMarker from stale disk .x.go — Module read stale file:\n%s", got)
	}
}

func TestModuleSourceIndexIgnoresOnDiskXGo(t *testing.T) {
	if testing.Short() {
		t.Skip("real module analysis")
	}
	root := t.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "go.mod", "module example.com/index\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	const source = `package page

type Label string

func helper(value Label) Label { return value }

component Card[T ~string](value T) {
	<strong>{value}</strong>
}

component Page(label Label) {
	<Card[string] value={string(helper(label))}/>
}
`
	pageDir := filepath.Join(root, "page")
	writeFile(t, pageDir, "page.gsx", source)
	gsxPath := filepath.Join(pageDir, "page.gsx")
	xgoPath := filepath.Join(pageDir, "page.x.go")

	type snapshot struct {
		occurrences  []string
		declarations []string
		scope        []string
	}
	analyze := func(t *testing.T) snapshot {
		t.Helper()
		module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/index", FilterPkgs: []string{StdImportPath}})
		if err != nil {
			t.Fatal(err)
		}
		result, err := module.Package(pageDir)
		if err != nil {
			t.Fatal(err)
		}
		if result.SourceIndex == nil || !result.SourceIndex.MatchesSource(gsxPath, []byte(source)) {
			t.Fatal("fresh package has no matching authored source index")
		}
		var got snapshot
		for _, target := range []struct {
			needle string
			delta  int
		}{
			{needle: "helper(value"},
			{needle: "return value", delta: len("return ")},
			{needle: "Card[T"},
			{needle: "value T"},
			{needle: "Card[string]", delta: len("Card[")},
			{needle: "helper(label)"},
			{needle: "label))"},
		} {
			offset := strings.Index(source, target.needle)
			if offset < 0 {
				t.Fatalf("fixture missing %q", target.needle)
			}
			offset += target.delta
			occurrence, ok := result.SourceIndex.At(gsxPath, offset)
			if !ok {
				t.Fatalf("no indexed occurrence at %q", target.needle)
			}
			object := ""
			if occurrence.Object != nil {
				object = occurrence.Object.String()
			}
			got.occurrences = append(got.occurrences, fmt.Sprintf("%d:%d:%d:%s:%v", occurrence.Span.Start, occurrence.Span.End, occurrence.Kind, object, occurrence.HasTypeValue))
		}
		for _, declaration := range result.SourceIndex.Declarations(gsxPath) {
			got.declarations = append(got.declarations, fmt.Sprintf("%s:%d:%s:%d:%d:%d:%d", declaration.Name, declaration.Kind, declaration.Container, declaration.NameSpan.Start, declaration.NameSpan.End, declaration.DeclSpan.Start, declaration.DeclSpan.End))
		}
		if result.Types == nil {
			t.Fatal("fresh package has no type package")
		}
		got.scope = result.Types.Scope().Names()
		return got
	}

	want := analyze(t)
	cases := []struct {
		name     string
		contents string
	}{
		{name: "invalid", contents: "this is not Go\x00\xff"},
		{name: "stale", contents: "package page\n\nvar StaleOnly = 1\n"},
		{name: "conflicting", contents: "package page\n\ntype Label int\nfunc helper() {}\nfunc Card() {}\n"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(xgoPath, []byte(test.contents), 0o644); err != nil {
				t.Fatal(err)
			}
			stamp := time.Unix(1_700_000_000, 123_000_000)
			if err := os.Chtimes(xgoPath, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(xgoPath)
			if err != nil {
				t.Fatal(err)
			}
			got := analyze(t)
			if !slices.Equal(got.occurrences, want.occurrences) || !slices.Equal(got.declarations, want.declarations) || !slices.Equal(got.scope, want.scope) {
				t.Fatalf("source index changed with %s paired output:\ngot=%+v\nwant=%+v", test.name, got, want)
			}
			contents, err := os.ReadFile(xgoPath)
			if err != nil {
				t.Fatal(err)
			}
			after, err := os.Stat(xgoPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(contents) != test.contents || !after.ModTime().Equal(before.ModTime()) {
				t.Fatalf("paired output mutated: contents=%q modtime=%v, want %q %v", contents, after.ModTime(), test.contents, before.ModTime())
			}
		})
	}
}
