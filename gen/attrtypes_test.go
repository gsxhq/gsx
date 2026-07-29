package gen

import "testing"

func TestAttrTypesPublicFacadeCompiles(t *testing.T) {
	t.Parallel()
	// Construct rules using only gen.RuleSet — no attrclass import.
	var cfg config
	WithURLAttrs(RuleSet{Names: []string{"data-href"}, Suffixes: []string{"-url"}})(&cfg)
	WithURLAttrsOn("img", RuleSet{Names: []string{"data-src"}})(&cfg)

	if len(cfg.errs) != 0 {
		t.Fatalf("unexpected errs: %v", cfg.errs)
	}
	if got, want := cfg.urlRules.Names, []string{"data-href"}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("urlRules.Names = %v, want %v", got, want)
	}
	if got := cfg.urlTagRules["img"].Names; len(got) != 1 || got[0] != "data-src" {
		t.Fatalf("urlTagRules[img].Names = %v, want [data-src]", got)
	}
	if cfg.classifier() == nil {
		t.Fatal("classifier is nil")
	}
}

// An empty matcher would classify every attribute; the option records it as a
// config error rather than silently installing it.
func TestWithURLAttrsRejectsEmptyEntry(t *testing.T) {
	t.Parallel()
	var cfg config
	WithURLAttrs(RuleSet{Names: []string{""}})(&cfg)
	if len(cfg.errs) == 0 {
		t.Fatal("empty name should be a config error")
	}
}
