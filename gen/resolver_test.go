package gen

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/codegen"
	"github.com/gsxhq/gsx/internal/diag"
)

type sourceOnlyResolver interface {
	GenerateSource(string, []byte) (Result, error)
	GenerateSources(map[string][]byte) (Result, error)
}

type diskResolver interface {
	Generate(string, map[string][]byte) (Result, error)
}

var (
	_ sourceOnlyResolver = (*BundledResolver)(nil)
	_ diskResolver       = (*CachedResolver)(nil)
)

func TestResolverPublicSurfacesAreSeparated(t *testing.T) {
	bundledConstructor := reflect.TypeOf(NewBundledResolver)
	bundledType := bundledConstructor.Out(0)
	if got, want := bundledType.Elem().Name(), "BundledResolver"; got != want {
		t.Fatalf("NewBundledResolver result type = %s, want *gen.%s", bundledType, want)
	}
	if _, ok := bundledType.MethodByName("Generate"); ok {
		t.Fatal("BundledResolver exposes disk-backed Generate")
	}
	for _, method := range []string{"GenerateSource", "GenerateSources"} {
		if _, ok := bundledType.MethodByName(method); !ok {
			t.Errorf("BundledResolver does not expose %s", method)
		}
	}

	cachedType := reflect.TypeFor[*CachedResolver]()
	if _, ok := cachedType.MethodByName("Generate"); !ok {
		t.Fatal("CachedResolver does not expose Generate")
	}
	for _, method := range []string{"GenerateSource", "GenerateSources"} {
		if _, ok := cachedType.MethodByName(method); ok {
			t.Errorf("CachedResolver unexpectedly exposes source-only %s", method)
		}
	}
}

func TestCachedResolverRejectsUnboundZeroValue(t *testing.T) {
	_, err := new(CachedResolver).Generate(t.TempDir(), nil)
	if err == nil || !strings.Contains(err.Error(), "no bound module root") {
		t.Fatalf("Generate error = %v, want unbound-resolver rejection", err)
	}
}

func TestCachedResolverModuleBinding(t *testing.T) {
	bound := newModule(t, "example.com/app")
	boundViews := filepath.Join(bound, "views")
	if err := os.MkdirAll(boundViews, 0o755); err != nil {
		t.Fatal(err)
	}
	boundInfo, err := os.Stat(bound)
	if err != nil {
		t.Fatal(err)
	}
	resolver := &CachedResolver{moduleRoot: bound, modulePath: "example.com/app", moduleRootInfo: boundInfo}

	t.Run("same physical module", func(t *testing.T) {
		root, modulePath, err := resolver.moduleForDir(boundViews)
		if err != nil {
			t.Fatal(err)
		}
		if root != bound || modulePath != "example.com/app" {
			t.Fatalf("moduleForDir = (%q, %q), want (%q, %q)", root, modulePath, bound, "example.com/app")
		}
	})

	t.Run("different root with same module path", func(t *testing.T) {
		other := newModule(t, "example.com/app")
		_, _, err := resolver.moduleForDir(other)
		if err == nil || !strings.Contains(err.Error(), "bound to module root") {
			t.Fatalf("moduleForDir error = %v, want physical root-binding rejection", err)
		}
		if _, err := resolver.Generate(other, nil); err == nil || !strings.Contains(err.Error(), "bound to module root") {
			t.Fatalf("Generate error = %v, want root-binding rejection before bundle use", err)
		}
	})

	t.Run("nested module", func(t *testing.T) {
		nested := filepath.Join(bound, "nested")
		if err := os.MkdirAll(nested, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(nested, "go.mod"), []byte("module example.com/nested\n\ngo 1.26.1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, _, err := resolver.moduleForDir(nested)
		if err == nil || !strings.Contains(err.Error(), "bound to module root") {
			t.Fatalf("moduleForDir error = %v, want nested-module rejection", err)
		}
	})

	t.Run("physical symlink escape", func(t *testing.T) {
		outside := t.TempDir()
		link := filepath.Join(bound, "linked")
		if err := os.Symlink(outside, link); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		_, _, err := resolver.moduleForDir(link)
		if err == nil || !strings.Contains(err.Error(), "outside its physical module root") {
			t.Fatalf("moduleForDir error = %v, want physical-containment rejection", err)
		}
	})

	t.Run("module identity changed", func(t *testing.T) {
		root := newModule(t, "example.com/original")
		rootInfo, err := os.Stat(root)
		if err != nil {
			t.Fatal(err)
		}
		changed := &CachedResolver{moduleRoot: root, modulePath: "example.com/original", moduleRootInfo: rootInfo}
		if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/changed\n\ngo 1.26.1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, _, err = changed.moduleForDir(root)
		if err == nil || !strings.Contains(err.Error(), "module path changed") {
			t.Fatalf("moduleForDir error = %v, want module-path change rejection", err)
		}
	})

	t.Run("go.mod became malformed", func(t *testing.T) {
		root := newModule(t, "example.com/app")
		rootInfo, err := os.Stat(root)
		if err != nil {
			t.Fatal(err)
		}
		boundToValidMod := &CachedResolver{moduleRoot: root, modulePath: "example.com/app", moduleRootInfo: rootInfo}
		if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/app\n\nrequire (\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, _, err = boundToValidMod.moduleForDir(root)
		if err == nil || !strings.Contains(err.Error(), "parse") {
			t.Fatalf("moduleForDir error = %v, want malformed go.mod rejection", err)
		}
	})

	t.Run("root replaced at same path", func(t *testing.T) {
		parent := t.TempDir()
		root := filepath.Join(parent, "app")
		if err := os.Mkdir(root, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/app\n\ngo 1.26.1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		rootInfo, err := os.Stat(root)
		if err != nil {
			t.Fatal(err)
		}
		boundToOriginal := &CachedResolver{moduleRoot: root, modulePath: "example.com/app", moduleRootInfo: rootInfo}
		if err := os.Rename(root, filepath.Join(parent, "original")); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(root, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/app\n\ngo 1.26.1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, _, err = boundToOriginal.moduleForDir(root)
		if err == nil || !strings.Contains(err.Error(), "bound to module root") {
			t.Fatalf("moduleForDir error = %v, want replaced-root rejection", err)
		}
	})

	t.Run("symlink alias", func(t *testing.T) {
		alias := filepath.Join(t.TempDir(), "app")
		if err := os.Symlink(bound, alias); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		root, modulePath, err := resolver.moduleForDir(filepath.Join(alias, "views"))
		if err != nil {
			t.Fatal(err)
		}
		if root != alias || modulePath != "example.com/app" {
			t.Fatalf("moduleForDir = (%q, %q), want symlink root (%q, %q)", root, modulePath, alias, "example.com/app")
		}
	})
}

func TestCachedResolverGenerateRetainsModuleProvenance(t *testing.T) {
	root := newModule(t, "example.com/app")
	uiDir := filepath.Join(root, "ui")
	bridgeDir := filepath.Join(root, "bridge")
	pageDir := filepath.Join(root, "page")
	writeFile(t, uiDir, "card.gsx", "package ui\n\ncomponent Card(title string) { <p>{title}</p> }\n")
	writeFile(t, uiDir, "card.x.go", "// Code generated by gsx. DO NOT EDIT.\n\npackage ui\n\ntype CardProps struct { Title string }\n")
	writeFile(t, bridgeDir, "bridge.go", `package bridge

import "example.com/app/ui"

type OldCardProps = ui.CardProps

func Label() string { return "bridge" }
`)
	writeFile(t, pageDir, "page.gsx", `package page

import "example.com/app/bridge"

component Page() { <p>{bridge.Label()}</p> }
`)

	resolver, err := NewCachedResolver(root, []string{"example.com/app/bridge"})
	if err != nil {
		t.Fatalf("NewCachedResolver: %v", err)
	}
	result, err := resolver.Generate(pageDir, nil)
	if err != errInProcessDiagnostics {
		t.Fatalf("Generate error = %v, want %v (diagnostics=%+v)", err, errInProcessDiagnostics, result.Diags)
	}
	if len(result.Files) != 0 {
		t.Fatalf("Generate emitted stale-universe output: %v", result.Files)
	}
	if len(result.Diags) != 1 || result.Diags[0].Code != "bundle-project-gsx-transitive" {
		t.Fatalf("diagnostics = %+v, want one bundle-project-gsx-transitive diagnostic", result.Diags)
	}
	if got := result.Diags[0].Start.Filename; got != filepath.Join(pageDir, "page.gsx") {
		t.Fatalf("diagnostic file = %q, want page.gsx", got)
	}
}

// TestCachedResolverGenerate proves that Generate of a valid component yields
// the expected generated Go with no diagnostics.
func TestCachedResolverGenerate(t *testing.T) {
	t.Parallel()
	mod := newModule(t, "example.com/cached-generate")
	viewsDir := filepath.Join(mod, "views")
	if err := os.MkdirAll(viewsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	r, err := NewCachedResolver(mod, DefaultPlaygroundImports)
	if err != nil {
		t.Fatal(err)
	}
	src := map[string][]byte{
		"views/comp.gsx": []byte("package views\n\ncomponent G(s string){\n\t<p>{s}</p>\n}\n"),
	}
	res, err := r.Generate(viewsDir, src)
	if err != nil {
		t.Fatalf("Generate: %v (diags: %+v)", err, res.Diags)
	}
	if len(res.Diags) != 0 {
		t.Fatalf("expected no diagnostics, got: %+v", res.Diags)
	}
	got := res.Files["views/comp.x.go"]
	if !bytes.Contains(got, []byte("func G(")) {
		t.Fatalf("generated Go missing func G:\n%s", got)
	}
}

// TestCachedResolverTypeError proves that a component with a type error
// ({missng}) yields a positioned diagnostic in the .gsx source, and that the
// position matches the default path's diagnostic for the same input.
func TestCachedResolverTypeError(t *testing.T) {
	t.Parallel()
	const badSrc = "package views\n\ncomponent Bad() {\n\t<p>{missng}</p>\n}\n"

	// --- Cached path ---
	cachedMod := newModule(t, "example.com/cached-type-error")
	cachedViewsDir := filepath.Join(cachedMod, "views")
	if err := os.MkdirAll(cachedViewsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	r, err := NewCachedResolver(cachedMod, DefaultPlaygroundImports)
	if err != nil {
		t.Fatal(err)
	}
	src := map[string][]byte{
		"views/bad.gsx": []byte(badSrc),
	}
	cachedRes, _ := r.Generate(cachedViewsDir, src)
	var cachedDiag *diag.Diagnostic
	for i := range cachedRes.Diags {
		d := &cachedRes.Diags[i]
		if d.Severity == diag.Error {
			cachedDiag = d
			break
		}
	}
	if cachedDiag == nil {
		t.Fatalf("cached path: expected an error diagnostic for {missng}, got none (diags: %+v)", cachedRes.Diags)
	}
	if cachedDiag.Start.Line == 0 {
		t.Errorf("cached path: diagnostic has no line position: %+v", cachedDiag)
	}

	// --- Default path (GenerateDirs) ---
	// Build a temp module so packages.Load can resolve imports.
	mod := newModule(t, "gsxresolvererr")
	// Write the bad component into the temp module's views package.
	viewsDir := mod + "/views"
	writeFile(t, viewsDir, "bad.gsx", badSrc)
	defaultRes, err2 := codegen.GenerateDirs(mod, []string{viewsDir}, codegen.Options{}, nil)
	if err2 != nil {
		t.Fatalf("default path: GenerateDirs error: %v", err2)
	}
	defaultDR := defaultRes[viewsDir]
	var defaultDiag *diag.Diagnostic
	for i := range defaultDR.Diags {
		d := &defaultDR.Diags[i]
		if d.Severity == diag.Error {
			defaultDiag = d
			break
		}
	}
	if defaultDiag == nil {
		t.Fatalf("default path: expected an error diagnostic for {missng}, got none (diags: %+v)", defaultDR.Diags)
	}

	// Both paths must produce a diagnostic at the same line/column in the .gsx source.
	if cachedDiag.Start.Line != defaultDiag.Start.Line {
		t.Errorf("line mismatch: cached=%d default=%d",
			cachedDiag.Start.Line, defaultDiag.Start.Line)
	}
	if cachedDiag.Start.Column != defaultDiag.Start.Column {
		t.Errorf("column mismatch: cached=%d default=%d",
			cachedDiag.Start.Column, defaultDiag.Start.Column)
	}
	t.Logf("cached  diag: line=%d col=%d msg=%q file=%q",
		cachedDiag.Start.Line, cachedDiag.Start.Column, cachedDiag.Message, cachedDiag.Start.Filename)
	t.Logf("default diag: line=%d col=%d msg=%q file=%q",
		defaultDiag.Start.Line, defaultDiag.Start.Column, defaultDiag.Message, defaultDiag.Start.Filename)
}

func TestCachedResolverParseErrorDiagnostic(t *testing.T) {
	t.Parallel()
	const badSrc = `package views

component Meter(value int, color string) {
	<div
		class={ "meter", "meter-full": value >= 100 }
		style={ value |> printf("width: %d%%"); "color: " + color }
	/>
}
`
	mod := newModule(t, "example.com/cached-parse-error")
	viewsDir := filepath.Join(mod, "views")
	if err := os.MkdirAll(viewsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	r, err := NewCachedResolver(mod, DefaultPlaygroundImports)
	if err != nil {
		t.Fatal(err)
	}
	res, _ := r.Generate(viewsDir, map[string][]byte{"views/meter.gsx": []byte(badSrc)})
	if len(res.Files) != 0 {
		t.Fatalf("parse error should not produce generated files: %v", res.Files)
	}
	if len(res.Diags) == 0 {
		t.Fatal("expected parse error diagnostic, got none")
	}
	d := res.Diags[0]
	if d.Severity != diag.Error || d.Start.Line != 6 || d.Start.Column != 41 {
		t.Fatalf("diagnostic = %+v, want error at 6:41", d)
	}
	if d.End.Line != 6 || d.End.Column <= d.Start.Column {
		t.Fatalf("diagnostic range = %d:%d..%d:%d, want non-empty range on line 6", d.Start.Line, d.Start.Column, d.End.Line, d.End.Column)
	}
	if !strings.Contains(d.Message, "trailing text after `)`") {
		t.Fatalf("diagnostic message = %q", d.Message)
	}
}
