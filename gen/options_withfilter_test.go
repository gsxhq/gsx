package gen

import (
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/codegen"
)

// SampleFilter is a plain top-level exported function used as a WithFilter
// target in tests. Its qualified name resolves to this (gen) package.
func SampleFilter(s string) string { return s }

type sampleRecv struct{}

// Method is a method on sampleRecv, used to exercise method-value rejection.
func (sampleRecv) Method(s string) string { return s }

// TestWithFilterTopLevelFunc proves a plain exported top-level function resolves
// to its package import path + exported func name and is recorded as an alias.
func TestWithFilterTopLevelFunc(t *testing.T) {
	cfg := applyOpts(WithFilter("sample", SampleFilter))
	if len(cfg.errs) != 0 {
		t.Fatalf("unexpected errs: %v", cfg.errs)
	}
	if len(cfg.aliases) != 1 {
		t.Fatalf("aliases len = %d, want 1", len(cfg.aliases))
	}
	got := cfg.aliases[0]
	want := codegen.FilterAlias{Name: "sample", PkgPath: genPath, FuncName: "SampleFilter"}
	if got != want {
		t.Fatalf("alias = %+v, want %+v", got, want)
	}
}

// TestWithFilterAliasOrder proves aliases accumulate in option order (last-wins
// is applied later at harvest, so order must be preserved here).
func TestWithFilterAliasOrder(t *testing.T) {
	cfg := applyOpts(WithFilter("a", SampleFilter), WithFilter("b", SampleFilter))
	if len(cfg.aliases) != 2 || cfg.aliases[0].Name != "a" || cfg.aliases[1].Name != "b" {
		t.Fatalf("alias order not preserved: %+v", cfg.aliases)
	}
}

// TestWithFilterRejectsMethodValue proves a bound/unbound method value is
// rejected with a clear config error and adds no alias.
func TestWithFilterRejectsMethodValue(t *testing.T) {
	var r sampleRecv
	cfg := applyOpts(WithFilter("m", r.Method))
	if len(cfg.errs) == 0 {
		t.Fatal("expected an error for a method value, got none")
	}
	if len(cfg.aliases) != 0 {
		t.Fatalf("expected no aliases from a method value, got %+v", cfg.aliases)
	}
}

// TestWithFilterRejectsClosure proves a closure (anonymous function value) is
// rejected: its runtime name carries a .funcN suffix, not an exported ident.
func TestWithFilterRejectsClosure(t *testing.T) {
	closure := func(s string) string { return s }
	cfg := applyOpts(WithFilter("c", closure))
	if len(cfg.errs) == 0 {
		t.Fatal("expected an error for a closure, got none")
	}
	if len(cfg.aliases) != 0 {
		t.Fatalf("expected no aliases from a closure, got %+v", cfg.aliases)
	}
}

// TestWithFilterRejectsNonFunc proves a non-function value is rejected.
func TestWithFilterRejectsNonFunc(t *testing.T) {
	cfg := applyOpts(WithFilter("x", 42))
	if len(cfg.errs) == 0 {
		t.Fatal("expected an error for a non-function, got none")
	}
}

// TestWithFilterRejectsNil proves a nil fn is rejected.
func TestWithFilterRejectsNil(t *testing.T) {
	cfg := applyOpts(WithFilter("x", nil))
	if len(cfg.errs) == 0 {
		t.Fatal("expected an error for nil fn, got none")
	}
}

// TestWithFilterRejectsUnexported proves an unexported top-level function is
// rejected (the template vocabulary must point at an exported func).
func TestWithFilterRejectsUnexported(t *testing.T) {
	cfg := applyOpts(WithFilter("u", unexportedFilter))
	if len(cfg.errs) == 0 {
		t.Fatal("expected an error for an unexported function, got none")
	}
	if len(cfg.aliases) != 0 {
		t.Fatalf("expected no aliases from an unexported function, got %+v", cfg.aliases)
	}
	// The error should name the offending function.
	if !strings.Contains(cfg.errs[0].Error(), "unexportedFilter") {
		t.Fatalf("error should name the function; got: %v", cfg.errs[0])
	}
}

func unexportedFilter(s string) string { return s }
