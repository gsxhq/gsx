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
		JS:  RuleSet{Prefixes: []string{"wire:", "v-on:"}},
		URL: RuleSet{Names: []string{"data-href"}},
		CSS: RuleSet{Names: []string{"data-style"}},
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

func TestFingerprintStable(t *testing.T) {
	a := New(Rules{JS: RuleSet{Prefixes: []string{"wire:"}}})
	b := New(Rules{JS: RuleSet{Prefixes: []string{"wire:"}}})
	if a.Fingerprint() != b.Fingerprint() {
		t.Error("same rules must produce same fingerprint")
	}
	c := New(Rules{JS: RuleSet{Prefixes: []string{"other:"}}})
	if a.Fingerprint() == c.Fingerprint() {
		t.Error("different rules must produce different fingerprint")
	}

	// EVERY matcher must reach the hash — it feeds the codegen cache key, so a
	// field the fingerprint cannot see would serve stale generated code after a
	// config edit. This is the property the removed predicate escape hatch could
	// not provide (a closure body is not hashable), and the reason rules are
	// declarative data.
	base := Rules{URL: RuleSet{Names: []string{"data-href"}}}
	for name, changed := range map[string]Rules{
		"names":    {URL: RuleSet{Names: []string{"data-other"}}},
		"prefixes": {URL: RuleSet{Names: []string{"data-href"}, Prefixes: []string{"data-url-"}}},
		"suffixes": {URL: RuleSet{Names: []string{"data-href"}, Suffixes: []string{"-url"}}},
		"urlTags": {
			URL:     RuleSet{Names: []string{"data-href"}},
			URLTags: map[string]RuleSet{"img": {Names: []string{"data-src"}}},
		},
	} {
		if New(base).Fingerprint() == New(changed).Fingerprint() {
			t.Errorf("changing %s must change the fingerprint", name)
		}
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
	want := Rules{URL: RuleSet{Names: []string{
		"hx-get", "hx-post", "hx-put", "hx-delete", "hx-patch",
	}}}
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

func TestUserURLRules(t *testing.T) {
	// The built-in floor is NOT repeated here: it lives in the runtime
	// (gsx.URLAttrSink) and Spread applies it itself, so generated code carries
	// only the project's own delta.
	if got := Builtin().UserURLRules("div"); !got.Empty() {
		t.Errorf("Builtin().UserURLRules() = %+v, want empty", got)
	}
	if got := (*Classifier)(nil).UserURLRules("div"); !got.Empty() {
		t.Errorf("nil.UserURLRules() = %+v, want empty", got)
	}

	c := New(Rules{URL: RuleSet{
		Names: []string{
			"Data-Href", // case-variant user exact rule → data-href
			"HREF",      // already in the floor → dropped, never doubled
			"src",       // ditto: a rule cannot demote <img src> off the image sink
		},
		Prefixes: []string{"Data-URL-", "hx-"},
		Suffixes: []string{"-URL"},
	}})
	got := c.UserURLRules("div")
	if want := []string{"data-href"}; !reflect.DeepEqual(got.Names, want) {
		t.Errorf("UserURLRules().Names = %v, want %v", got.Names, want)
	}
	if want := []string{"data-url-", "hx-"}; !reflect.DeepEqual(got.Prefixes, want) {
		t.Errorf("UserURLRules().Prefixes = %v, want %v", got.Prefixes, want)
	}
	if want := []string{"-url"}; !reflect.DeepEqual(got.Suffixes, want) {
		t.Errorf("UserURLRules().Suffixes = %v, want %v", got.Suffixes, want)
	}
}

// A tag-scoped rule applies on its element and nowhere else, and merges with the
// global set rather than replacing it. Codegen resolves this at generate time,
// so the leaf never sees anything tag-dependent.
func TestUserURLRulesTagScope(t *testing.T) {
	c := New(Rules{
		URL:     RuleSet{Names: []string{"data-href"}},
		URLTags: map[string]RuleSet{"img": {Names: []string{"data-src"}}},
	})
	if got, want := c.UserURLRules("img").Names, []string{"data-href", "data-src"}; !reflect.DeepEqual(got, want) {
		t.Errorf("UserURLRules(img).Names = %v, want %v", got, want)
	}
	if got, want := c.UserURLRules("div").Names, []string{"data-href"}; !reflect.DeepEqual(got, want) {
		t.Errorf("UserURLRules(div).Names = %v, want %v", got, want)
	}
	// Context agrees with the delta.
	if got := c.Context("img", "data-src"); got != CtxURL {
		t.Errorf("Context(img, data-src) = %v, want CtxURL", got)
	}
	if got := c.Context("div", "data-src"); got != CtxPlain {
		t.Errorf("Context(div, data-src) = %v, want CtxPlain", got)
	}
}

// An empty matcher would classify every attribute, so it is a config error.
func TestRuleSetValidRejectsEmptyEntry(t *testing.T) {
	for _, set := range []RuleSet{{Names: []string{""}}, {Prefixes: []string{" "}}, {Suffixes: []string{""}}} {
		if err := set.Valid(); err == nil {
			t.Errorf("RuleSet%+v.Valid() = nil, want an error", set)
		}
	}
	if err := (RuleSet{Names: []string{"ok"}}).Valid(); err != nil {
		t.Errorf("valid set rejected: %v", err)
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
