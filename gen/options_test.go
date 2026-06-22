package gen

import (
	"reflect"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/cssmin"
	"github.com/gsxhq/gsx/std"
)

const stdPath = "github.com/gsxhq/gsx/std"

// applyOpts is a tiny internal seam: it builds a config from options so the
// option behavior can be inspected without running the CLI.
func applyOpts(opts ...Option) config {
	var cfg config
	for _, opt := range opts {
		opt(&cfg)
	}
	return cfg
}

// TestWithFiltersStdPath proves WithFilters(std.Pkg) records the std import
// path recovered via reflection.
func TestWithFiltersStdPath(t *testing.T) {
	cfg := applyOpts(WithFilters(std.Pkg))
	if len(cfg.errs) != 0 {
		t.Fatalf("unexpected errs: %v", cfg.errs)
	}
	want := []string{stdPath}
	if !reflect.DeepEqual(cfg.filterPkgs, want) {
		t.Fatalf("filterPkgs = %v, want %v", cfg.filterPkgs, want)
	}
}

// otherPkgMarker stands in for a second filter package's Pkg token: its type
// lives in this (gen) package, so its recovered path is the gen import path.
type otherPkgMarker struct{}

var otherPkg otherPkgMarker

const genPath = "github.com/gsxhq/gsx/gen"

// TestWithFiltersOrderAndDedup proves order is preserved (overrides last) and
// duplicate package paths are collapsed to a single first-seen entry.
func TestWithFiltersOrderAndDedup(t *testing.T) {
	cfg := applyOpts(WithFilters(std.Pkg, otherPkg, std.Pkg))
	if len(cfg.errs) != 0 {
		t.Fatalf("unexpected errs: %v", cfg.errs)
	}
	// std appears once (first-seen position), other follows; the trailing
	// duplicate std is dropped.
	want := []string{stdPath, genPath}
	if !reflect.DeepEqual(cfg.filterPkgs, want) {
		t.Fatalf("filterPkgs = %v, want %v", cfg.filterPkgs, want)
	}
}

// TestWithFiltersAcrossCalls proves dedup spans multiple WithFilters options
// applied to the same config.
func TestWithFiltersAcrossCalls(t *testing.T) {
	cfg := applyOpts(WithFilters(std.Pkg), WithFilters(otherPkg), WithFilters(std.Pkg))
	want := []string{stdPath, genPath}
	if !reflect.DeepEqual(cfg.filterPkgs, want) {
		t.Fatalf("filterPkgs = %v, want %v", cfg.filterPkgs, want)
	}
}

// TestWithFiltersNilMarkerRecorded proves a nil marker is recorded as an error
// rather than silently dropped, and does not add a filter package.
func TestWithFiltersNilMarkerRecorded(t *testing.T) {
	cfg := applyOpts(WithFilters(nil))
	if len(cfg.errs) == 0 {
		t.Fatal("expected an error for a nil marker, got none")
	}
	if len(cfg.filterPkgs) != 0 {
		t.Fatalf("expected no filter pkgs from a nil marker, got %v", cfg.filterPkgs)
	}
}

// TestWithFiltersBuiltinMarkerRecorded proves a marker whose type has no
// package path (a builtin/unnamed type) is recorded as an error, not dropped.
func TestWithFiltersBuiltinMarkerRecorded(t *testing.T) {
	cfg := applyOpts(WithFilters(42))
	if len(cfg.errs) == 0 {
		t.Fatal("expected an error for a builtin-typed marker, got none")
	}
	if len(cfg.filterPkgs) != 0 {
		t.Fatalf("expected no filter pkgs from a builtin marker, got %v", cfg.filterPkgs)
	}
}

// This is a focused unit test of the threading contract at the cssmin layer:
// the same boundary gen.WithCSSMinifier relies on. (An end-to-end gen test needs
// a temp module; the corpus covers built-in end-to-end. Here we assert the ext
// func reaches holeless blocks only.)
func TestWithCSSMinifierBoundary(t *testing.T) {
	called := false
	ext := func(css string) (string, error) { called = true; return "/*ext*/" + css, nil }

	holeless := &ast.File{Decls: []ast.Decl{&ast.Component{Name: "C", Body: []ast.Markup{
		&ast.Element{Tag: "style", Children: []ast.Markup{&ast.Text{Value: ".a{x:1}"}}},
	}}}}
	if err := cssmin.MinifyFile(holeless, ext); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("ext not called on holeless block")
	}
	got := holeless.Decls[0].(*ast.Component).Body[0].(*ast.Element).Children[0].(*ast.Text).Value
	if !strings.HasPrefix(got, "/*ext*/") {
		t.Fatalf("ext output not used: %q", got)
	}

	called = false
	holey := &ast.File{Decls: []ast.Decl{&ast.Component{Name: "C", Body: []ast.Markup{
		&ast.Element{Tag: "style", Children: []ast.Markup{
			&ast.Text{Value: ".a{x:"}, &ast.Interp{Expr: "v"}, &ast.Text{Value: "}"},
		}},
	}}}}
	if err := cssmin.MinifyFile(holey, ext); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("ext must NOT be called on a holey block")
	}
}

func TestWithCSSMinifierOption(t *testing.T) {
	min := func(css string) (string, error) { return css, nil }
	var cfg config
	WithCSSMinifier(min)(&cfg)
	if cfg.cssMin == nil {
		t.Fatal("WithCSSMinifier did not set cfg.cssMin")
	}
}

func TestWithJSMinifierOption(t *testing.T) {
	min := func(js string) (string, error) { return js, nil }
	var cfg config
	WithJSMinifier(min)(&cfg)
	if cfg.jsMin == nil {
		t.Fatal("WithJSMinifier did not set cfg.jsMin")
	}
}
