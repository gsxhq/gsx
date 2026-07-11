package gen

import (
	"testing"

	"github.com/gsxhq/gsx/internal/codegen"
)

// TestWithRendererTopLevelFunc proves a plain exported top-level function
// resolves to its package import path + exported func name and is recorded as
// a renderer under the given (validated, canonicalized) TypeKey.
func TestWithRendererTopLevelFunc(t *testing.T) {
	t.Parallel()
	cfg := applyOpts(WithRenderer("example.com/app/pgtype.Text", SampleFilter))
	if len(cfg.errs) != 0 {
		t.Fatalf("unexpected errs: %v", cfg.errs)
	}
	if len(cfg.renderers) != 1 {
		t.Fatalf("renderers len = %d, want 1", len(cfg.renderers))
	}
	got := cfg.renderers[0]
	want := codegen.RendererAlias{TypeKey: "example.com/app/pgtype.Text", PkgPath: genPath, FuncName: "SampleFilter"}
	if got != want {
		t.Fatalf("renderer = %+v, want %+v", got, want)
	}
}

// TestWithRendererPointerTypeKey proves a *-prefixed TypeKey is preserved
// verbatim (it is the canonical form codegen.rendererKey produces for a
// pointer-registered type).
func TestWithRendererPointerTypeKey(t *testing.T) {
	t.Parallel()
	cfg := applyOpts(WithRenderer("*example.com/app/pgtype.Int4", SampleFilter))
	if len(cfg.errs) != 0 {
		t.Fatalf("unexpected errs: %v", cfg.errs)
	}
	if len(cfg.renderers) != 1 || cfg.renderers[0].TypeKey != "*example.com/app/pgtype.Int4" {
		t.Fatalf("renderers = %+v", cfg.renderers)
	}
}

// TestWithRendererOrder proves renderers accumulate in option order (last-wins
// per TypeKey is applied later at harvest, so order must be preserved here).
func TestWithRendererOrder(t *testing.T) {
	t.Parallel()
	cfg := applyOpts(
		WithRenderer("example.com/app/pgtype.A", SampleFilter),
		WithRenderer("example.com/app/pgtype.B", SampleFilter),
	)
	if len(cfg.renderers) != 2 || cfg.renderers[0].TypeKey != "example.com/app/pgtype.A" || cfg.renderers[1].TypeKey != "example.com/app/pgtype.B" {
		t.Fatalf("renderer order not preserved: %+v", cfg.renderers)
	}
}

// TestWithRendererRejectsBadTypeKey proves a malformed TypeKey (no dot) is
// rejected with a clear config error and adds no renderer.
func TestWithRendererRejectsBadTypeKey(t *testing.T) {
	t.Parallel()
	cfg := applyOpts(WithRenderer("nodothere", SampleFilter))
	if len(cfg.errs) == 0 {
		t.Fatal("expected an error for a malformed TypeKey, got none")
	}
	if len(cfg.renderers) != 0 {
		t.Fatalf("expected no renderers from a malformed TypeKey, got %+v", cfg.renderers)
	}
}

// TestWithRendererRejectsMethodValue proves a bound/unbound method value is
// rejected with a clear config error and adds no renderer.
func TestWithRendererRejectsMethodValue(t *testing.T) {
	t.Parallel()
	var r sampleRecv
	cfg := applyOpts(WithRenderer("example.com/app/pgtype.Text", r.Method))
	if len(cfg.errs) == 0 {
		t.Fatal("expected an error for a method value, got none")
	}
	if len(cfg.renderers) != 0 {
		t.Fatalf("expected no renderers from a method value, got %+v", cfg.renderers)
	}
}

// TestWithRendererRejectsClosure proves a closure is rejected: its runtime
// name carries a .funcN suffix, not an exported ident.
func TestWithRendererRejectsClosure(t *testing.T) {
	t.Parallel()
	closure := func(s string) string { return s }
	cfg := applyOpts(WithRenderer("example.com/app/pgtype.Text", closure))
	if len(cfg.errs) == 0 {
		t.Fatal("expected an error for a closure, got none")
	}
	if len(cfg.renderers) != 0 {
		t.Fatalf("expected no renderers from a closure, got %+v", cfg.renderers)
	}
}

// TestWithRendererRejectsNonFunc proves a non-function value is rejected.
func TestWithRendererRejectsNonFunc(t *testing.T) {
	t.Parallel()
	cfg := applyOpts(WithRenderer("example.com/app/pgtype.Text", 42))
	if len(cfg.errs) == 0 {
		t.Fatal("expected an error for a non-function, got none")
	}
}

// TestWithRendererRejectsNil proves a nil fn is rejected.
func TestWithRendererRejectsNil(t *testing.T) {
	t.Parallel()
	cfg := applyOpts(WithRenderer("example.com/app/pgtype.Text", nil))
	if len(cfg.errs) == 0 {
		t.Fatal("expected an error for nil fn, got none")
	}
}

// TestMergeConfigRenderersOptsAppendAfterBase proves opts renderers are
// appended after base renderers (last-wins-per-TypeKey resolves later, at
// harvest — merge itself is a plain concatenation, matching aliases).
func TestMergeConfigRenderersOptsAppendAfterBase(t *testing.T) {
	t.Parallel()
	base := applyOpts()
	base.renderers = []codegen.RendererAlias{{TypeKey: "p.T", PkgPath: "p", FuncName: "Base"}}
	opts := applyOpts()
	opts.renderers = []codegen.RendererAlias{{TypeKey: "p.T", PkgPath: "p", FuncName: "Override"}}

	merged := mergeConfig(base, opts)
	if len(merged.renderers) != 2 || merged.renderers[0].FuncName != "Base" || merged.renderers[1].FuncName != "Override" {
		t.Fatalf("renderers = %+v", merged.renderers)
	}
}
