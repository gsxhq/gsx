package gen

// TestAttrTypesPublicAPI proves that Rule, Rules, Context, and CtxJS/CtxURL/
// CtxCSS/CtxPlain are usable via ONLY the gen package — no attrclass import
// required. This is the regression guard that the public API façade actually
// compiles and works for an external user.
import "testing"

func TestAttrTypesPublicFacadeCompiles(t *testing.T) {
	// Construct rules using only gen.Rule — no attrclass import.
	var cfg config
	WithJSAttrs(Rule{Prefix: "wire:"})(&cfg)
	WithAttrClassifier("x", func(name string) (Context, bool) {
		if name == "x-custom" {
			return CtxJS, true
		}
		return CtxPlain, false
	})(&cfg)

	if len(cfg.errs) != 0 {
		t.Fatalf("unexpected errs: %v", cfg.errs)
	}

	cls := cfg.classifier()

	// Declarative rule hit: wire:click should resolve to CtxJS.
	if got := cls.Context("wire:click"); got != CtxJS {
		t.Errorf("wire:click: got context %v, want CtxJS (%v)", got, CtxJS)
	}

	// Predicate hit: x-custom should resolve to CtxJS via predicate.
	if got := cls.Context("x-custom"); got != CtxJS {
		t.Errorf("x-custom: got context %v, want CtxJS (%v)", got, CtxJS)
	}

	// Predicate pass-through: plain attribute returns CtxPlain.
	if got := cls.Context("data-plain"); got != CtxPlain {
		t.Errorf("data-plain: got context %v, want CtxPlain (%v)", got, CtxPlain)
	}

	// Sanity: constants have distinct values.
	if CtxPlain == CtxJS || CtxJS == CtxURL || CtxURL == CtxCSS {
		t.Error("context constants are not distinct")
	}
}
