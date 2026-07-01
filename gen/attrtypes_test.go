package gen

import "testing"

func TestAttrTypesPublicFacadeCompiles(t *testing.T) {
	t.Parallel()
	// Construct rules using only gen.Rule — no attrclass import.
	var cfg config
	WithURLAttrs(Rule{Name: "data-href"})(&cfg)

	if len(cfg.errs) != 0 {
		t.Fatalf("unexpected errs: %v", cfg.errs)
	}

	cls := cfg.classifier()

	if got := len(cfg.urlRules); got != 1 {
		t.Fatalf("urlRules len = %d, want 1", got)
	}
	if cfg.urlRules[0].Name != "data-href" {
		t.Fatalf("urlRules[0] = %+v, want data-href name rule", cfg.urlRules[0])
	}
	if cls == nil {
		t.Fatal("classifier is nil")
	}
}
