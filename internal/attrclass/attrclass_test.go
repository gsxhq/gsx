package attrclass

import (
	"reflect"
	"testing"
)

func TestBuiltinParity(t *testing.T) {
	c := Builtin()
	cases := []struct {
		name string
		want Context
	}{
		// JS (ported from attrjs): on*, @*, hx-on*, x-on:, x-data/init/show/if/effect, : bind
		{"onclick", CtxJS}, {"onChange", CtxJS}, {"@click", CtxJS},
		{"hx-on:click", CtxJS}, {"hx-on", CtxJS}, {"x-on:click", CtxJS},
		{"x-data", CtxJS}, {"x-init", CtxJS}, {"x-show", CtxJS},
		{"x-if", CtxJS}, {"x-effect", CtxJS}, {":class", CtxJS},
		// NOT JS — the precise on[a-z] rule must not over-match
		{"on", CtxPlain}, {"on-thing", CtxPlain}, {":", CtxPlain},
		{"online", CtxJS}, // "on"+lowercase letter — matches today's IsJSAttr exactly
		// URL (ported from urlAttrs)
		{"href", CtxURL}, {"src", CtxURL}, {"HREF", CtxURL},
		{"xlink:href", CtxURL},
		// htmx method attrs are NO LONGER built-in URLs — they moved to the opt-in
		// "htmx" preset (see TestPreset). The default classifies them plain.
		{"hx-get", CtxPlain}, {"hx-post", CtxPlain}, {"hx-put", CtxPlain},
		{"hx-delete", CtxPlain}, {"hx-patch", CtxPlain},
		// CSS
		{"style", CtxCSS}, {"STYLE", CtxCSS},
		// plain
		{"id", CtxPlain}, {"data-x", CtxPlain}, {"class", CtxPlain},
	}
	for _, tc := range cases {
		if got := c.Context("div", tc.name); got != tc.want {
			t.Errorf("Context(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestUserRulesAdditive(t *testing.T) {
	c := New(Rules{
		JS:  []Rule{{Prefix: "wire:"}, {Prefix: "v-on:"}},
		URL: []Rule{{Name: "data-href"}},
		CSS: []Rule{{Name: "data-style"}},
	})
	checks := map[string]Context{
		"wire:click": CtxJS, "v-on:click": CtxJS,
		"data-href": CtxURL, "data-style": CtxCSS,
		// built-ins still win and are unchanged
		"onclick": CtxJS, "href": CtxURL, "style": CtxCSS,
		// unrelated still plain
		"data-x": CtxPlain,
	}
	for name, want := range checks {
		if got := c.Context("div", name); got != want {
			t.Errorf("Context(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestRuleValid(t *testing.T) {
	if err := (Rule{Name: "x"}).Valid(); err != nil {
		t.Errorf("name-only rule should be valid: %v", err)
	}
	if err := (Rule{Prefix: "x:"}).Valid(); err != nil {
		t.Errorf("prefix-only rule should be valid: %v", err)
	}
	if (Rule{Name: "x", Prefix: "y"}).Valid() == nil {
		t.Error("both Name and Prefix set should be invalid")
	}
	if (Rule{}).Valid() == nil {
		t.Error("empty rule should be invalid")
	}
}

func TestFingerprintStable(t *testing.T) {
	a := New(Rules{JS: []Rule{{Prefix: "wire:"}}})
	b := New(Rules{JS: []Rule{{Prefix: "wire:"}}})
	if a.Fingerprint() != b.Fingerprint() {
		t.Error("same rules must produce same fingerprint")
	}
	c := New(Rules{JS: []Rule{{Prefix: "other:"}}})
	if a.Fingerprint() == c.Fingerprint() {
		t.Error("different rules must produce different fingerprint")
	}
}

func TestPreset(t *testing.T) {
	// The "htmx" preset re-enables the five htmx method attrs as URL rules —
	// the five EXACT names, never a "hx-" prefix (which would wrongly capture
	// hx-swap/hx-target/hx-trigger, none of which are URLs).
	rules, ok := Preset("htmx")
	if !ok {
		t.Fatal(`Preset("htmx") not found`)
	}
	want := Rules{URL: []Rule{
		{Name: "hx-get"}, {Name: "hx-post"}, {Name: "hx-put"},
		{Name: "hx-delete"}, {Name: "hx-patch"},
	}}
	if !reflect.DeepEqual(rules, want) {
		t.Errorf(`Preset("htmx") = %+v, want %+v`, rules, want)
	}

	// A classifier built from the preset's rules classifies the method attrs as
	// URL again, but leaves the non-URL hx-* attrs plain.
	c := New(rules)
	for _, n := range []string{"hx-get", "hx-post", "hx-put", "hx-delete", "hx-patch"} {
		if got := c.Context("div", n); got != CtxURL {
			t.Errorf("with htmx preset: Context(%q) = %v, want CtxURL", n, got)
		}
	}
	for _, n := range []string{"hx-swap", "hx-target", "hx-trigger"} {
		if got := c.Context("div", n); got != CtxPlain {
			t.Errorf("with htmx preset: Context(%q) = %v, want CtxPlain (not a URL attr)", n, got)
		}
	}

	// Unknown preset → (zero, false).
	if got, ok := Preset("nope"); ok || !reflect.DeepEqual(got, Rules{}) {
		t.Errorf(`Preset("nope") = (%+v, %v), want (Rules{}, false)`, got, ok)
	}
}

func TestUserURLExactNames(t *testing.T) {
	// The built-in floor is NOT repeated here: it lives in the runtime
	// (gsx.URLAttrSink) and Spread applies it itself, so generated code carries
	// only the project's own delta.
	if got := Builtin().UserURLExactNames(); len(got) != 0 {
		t.Errorf("Builtin().UserURLExactNames() = %v, want empty", got)
	}
	if got := (*Classifier)(nil).UserURLExactNames(); len(got) != 0 {
		t.Errorf("nil.UserURLExactNames() = %v, want empty", got)
	}
	if got := Builtin().URLPrefixes(); len(got) != 0 {
		t.Errorf("Builtin().URLPrefixes() = %v, want empty", got)
	}

	c := New(Rules{URL: []Rule{
		{Name: "Data-Href"}, // case-variant user exact rule → data-href
		{Name: "HREF"},      // already in the floor → dropped, never doubled
		{Name: "src"},       // ditto: a rule cannot demote <img src> off the image sink
		{Prefix: "Data-URL-"},
		{Prefix: "hx-"},
	}})
	if got, want := c.UserURLExactNames(), []string{"data-href"}; !reflect.DeepEqual(got, want) {
		t.Errorf("UserURLExactNames() = %v, want %v", got, want)
	}
	if got, want := c.URLPrefixes(), []string{"data-url-", "hx-"}; !reflect.DeepEqual(got, want) {
		t.Errorf("URLPrefixes() = %v, want %v", got, want)
	}
}

// `content` is a URL context on <meta> and nowhere else, and membership does NOT
// depend on a sibling http-equiv — html/template's rule. Keying it on the
// element alone is what makes the sink unevadable; the refresh sanitizer
// returns a non-directive value unchanged, so classifying every meta content
// costs nothing. The sink table itself is pinned in the runtime
// (TestURLAttrSink); what is checked here is that Context agrees with it.
func TestMetaContentIsTagScopedURL(t *testing.T) {
	c := Builtin()
	for _, tag := range []string{"meta", "META", "Meta"} {
		for _, name := range []string{"content", "CONTENT"} {
			if got := c.Context(tag, name); got != CtxURL {
				t.Errorf("Context(%q, %q) = %v, want CtxURL", tag, name, got)
			}
		}
	}
	for _, tag := range []string{"div", "span", "object", ""} {
		if got := c.Context(tag, "content"); got != CtxPlain {
			t.Errorf("Context(%q, content) = %v, want CtxPlain", tag, got)
		}
	}
	if got := c.Context("meta", "name"); got != CtxPlain {
		t.Errorf("Context(meta, name) = %v, want CtxPlain", got)
	}
}

func TestSrcsetClassifiedURL(t *testing.T) {
	c := Builtin()
	for _, name := range []string{"srcset", "imagesrcset", "SrcSet"} {
		if got := c.Context("div", name); got != CtxURL {
			t.Errorf("Context(%q) = %v, want CtxURL", name, got)
		}
	}
}
