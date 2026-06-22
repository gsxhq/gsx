package attrjs_test

import (
	"testing"

	"github.com/gsxhq/gsx/internal/attrjs"
)

func TestIsJSAttr(t *testing.T) {
	trueCases := []string{
		"onclick",
		"onmouseover",
		"@click",
		"@submit",
		"x-data",
		"x-init",
		"x-show",
		"x-if",
		"x-effect",
		"x-on:click",
		":class",
		":style",
		"hx-on:click",
		"hx-on:htmx:before-request",
	}
	for _, name := range trueCases {
		if !attrjs.IsJSAttr(name) {
			t.Errorf("IsJSAttr(%q) = false, want true", name)
		}
	}

	// False cases: names that must NOT be treated as JS context.
	// Note: the heuristic matches any "on"+"<letter>" prefix, so genuinely-false
	// names are those that don't start with @, hx-on, on[a-z], x-data/init/show/if/effect,
	// x-on:, or :<non-empty>.
	falseCases := []string{
		"class",
		"style",
		"href",
		"hx-get",
		"hx-post",
		"title",
		"data-x",
		":",      // bare colon — not a valid Alpine binding
		"id",
		"value",
	}
	for _, name := range falseCases {
		if attrjs.IsJSAttr(name) {
			t.Errorf("IsJSAttr(%q) = true, want false", name)
		}
	}
}
