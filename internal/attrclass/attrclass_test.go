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
		{"hx-get", CtxURL}, {"xlink:href", CtxURL},
		// CSS
		{"style", CtxCSS}, {"STYLE", CtxCSS},
		// plain
		{"id", CtxPlain}, {"data-x", CtxPlain}, {"class", CtxPlain},
	}
	for _, tc := range cases {
		if got := c.Context(tc.name); got != tc.want {
			t.Errorf("Context(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestUserRulesAdditive(t *testing.T) {
	c := New(Rules{
		JS:  []Rule{{Prefix: "wire:"}, {Prefix: "v-on:"}},
		URL: []Rule{{Name: "data-href"}},
		CSS: []Rule{{Name: "data-style"}},
	}, nil)
	checks := map[string]Context{
		"wire:click": CtxJS, "v-on:click": CtxJS,
		"data-href": CtxURL, "data-style": CtxCSS,
		// built-ins still win and are unchanged
		"onclick": CtxJS, "href": CtxURL, "style": CtxCSS,
		// unrelated still plain
		"data-x": CtxPlain,
	}
	for name, want := range checks {
		if got := c.Context(name); got != want {
			t.Errorf("Context(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestPredicateIsFallbackOnly(t *testing.T) {
	// predicate would say onclick is URL, but built-ins claim it first → stays JS.
	c := New(Rules{}, func(name string) (Context, bool) {
		if name == "onclick" {
			return CtxURL, true
		}
		if name == "fancy-go" {
			return CtxJS, true
		}
		return CtxPlain, false
	})
	if got := c.Context("onclick"); got != CtxJS {
		t.Errorf("predicate must not downgrade built-in: Context(onclick) = %v, want CtxJS", got)
	}
	if got := c.Context("fancy-go"); got != CtxJS {
		t.Errorf("predicate fallback: Context(fancy-go) = %v, want CtxJS", got)
	}
	if !c.HasPredicate() {
		t.Error("HasPredicate() = false, want true")
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
	a := New(Rules{JS: []Rule{{Prefix: "wire:"}}}, nil)
	b := New(Rules{JS: []Rule{{Prefix: "wire:"}}}, nil)
	if a.Fingerprint() != b.Fingerprint() {
		t.Error("same rules must produce same fingerprint")
	}
	c := New(Rules{JS: []Rule{{Prefix: "other:"}}}, nil)
	if a.Fingerprint() == c.Fingerprint() {
		t.Error("different rules must produce different fingerprint")
	}
	withPred := New(Rules{JS: []Rule{{Prefix: "wire:"}}}, func(string) (Context, bool) { return CtxPlain, false })
	if a.Fingerprint() == withPred.Fingerprint() {
		t.Error("presence of predicate must change fingerprint")
	}
}

func TestURLSink(t *testing.T) {
	image := []struct{ tag, name string }{
		{"img", "src"}, {"IMG", "SRC"},
		{"source", "src"},
		{"input", "src"},
		{"video", "poster"},
		{"body", "background"},
		{"table", "background"},
	}
	for _, c := range image {
		if got := URLSink(c.tag, c.name); got != SinkImage {
			t.Errorf("URLSink(%q,%q) = %v, want SinkImage", c.tag, c.name, got)
		}
	}
	strict := []struct{ tag, name string }{
		{"a", "href"},
		{"form", "action"},
		{"script", "src"}, // script src must stay strict
		{"iframe", "src"}, // iframe src must stay strict
		{"object", "data"},
		{"embed", "src"},
		{"video", "src"}, // media src, not an image sink
		{"img", "href"},  // href on img is not a resource sink
	}
	for _, c := range strict {
		if got := URLSink(c.tag, c.name); got != SinkStrict {
			t.Errorf("URLSink(%q,%q) = %v, want SinkStrict", c.tag, c.name, got)
		}
	}
}

func TestURLExactNames(t *testing.T) {
	// Builtin: the 16 built-in URL names, lowercased and sorted, no prefixes.
	wantBuiltin := []string{
		"action", "background", "cite", "data", "formaction", "href",
		"hx-delete", "hx-get", "hx-patch", "hx-post", "hx-put",
		"manifest", "ping", "poster", "src", "xlink:href",
	}
	if got := Builtin().URLExactNames(); !reflect.DeepEqual(got, wantBuiltin) {
		t.Errorf("Builtin().URLExactNames() = %v, want %v", got, wantBuiltin)
	}
	if got := Builtin().URLPrefixes(); len(got) != 0 {
		t.Errorf("Builtin().URLPrefixes() = %v, want empty", got)
	}
	// nil classifier is the built-in floor.
	if got := (*Classifier)(nil).URLExactNames(); !reflect.DeepEqual(got, wantBuiltin) {
		t.Errorf("nil.URLExactNames() = %v, want %v", got, wantBuiltin)
	}

	// New with exact + prefix URL rules: the exact name unions with the built-ins
	// (deduped, sorted); a duplicate/case-variant of a built-in does not double it.
	// Prefixes are lowercased, deduped and sorted; exact rules are excluded.
	c := New(Rules{URL: []Rule{
		{Name: "Data-Href"}, // case-variant user exact rule → data-href
		{Name: "HREF"},      // duplicate of a built-in → no double
		{Prefix: "Data-URL-"},
		{Prefix: "hx-"},
	}}, nil)
	wantExact := []string{
		"action", "background", "cite", "data", "data-href", "formaction", "href",
		"hx-delete", "hx-get", "hx-patch", "hx-post", "hx-put",
		"manifest", "ping", "poster", "src", "xlink:href",
	}
	if got := c.URLExactNames(); !reflect.DeepEqual(got, wantExact) {
		t.Errorf("New().URLExactNames() = %v, want %v", got, wantExact)
	}
	wantPrefixes := []string{"data-url-", "hx-"}
	if got := c.URLPrefixes(); !reflect.DeepEqual(got, wantPrefixes) {
		t.Errorf("New().URLPrefixes() = %v, want %v", got, wantPrefixes)
	}
}
