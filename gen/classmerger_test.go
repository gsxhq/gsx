package gen

import "testing"

func TestWithClassMergerResolvesTopLevelFunc(t *testing.T) {
	var cfg config
	WithClassMerger(SampleMerge)(&cfg)
	if len(cfg.errs) != 0 {
		t.Fatalf("unexpected errs: %v", cfg.errs)
	}
	if cfg.classMerger == nil || cfg.classMerger.FuncName != "SampleMerge" {
		t.Fatalf("got %+v", cfg.classMerger)
	}
}

func TestWithClassMergerRejectsClosure(t *testing.T) {
	var cfg config
	WithClassMerger(func(t []string) string { return "" })(&cfg)
	if len(cfg.errs) == 0 {
		t.Fatalf("want error for closure, got none")
	}
}

// SampleMerge is an exported top-level function used as a test fixture for
// WithClassMerger. It must be exported so runtime.FuncForPC yields an exported
// name that passes the isExportedIdent check (matching the WithFilter convention).
func SampleMerge(tokens []string) string { return "" }
