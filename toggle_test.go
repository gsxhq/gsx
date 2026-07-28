package gsx

import (
	"context"
	"strings"
	"testing"
)

func spreadOne(t *testing.T, key string, val any, navNames []string) string {
	t.Helper()
	var b strings.Builder
	gw := W(&b)
	gw.Spread(context.Background(), Attrs{{Key: key, Value: val}}, navNames, nil, nil, nil, nil)
	if gw.Err() != nil {
		t.Fatalf("Spread(%q=%#v) errored: %v", key, val, gw.Err())
	}
	return b.String()
}

func TestSpreadBoolByName(t *testing.T) {
	cases := []struct {
		name string
		key  string
		val  any
		want string
	}{
		// plain bool on a boolean attribute → presence toggle
		{"listed true", "required", true, " required"},
		{"listed false", "required", false, ""},
		// plain bool on a platform-valued name → stringify: the string IS the
		// state there (aria-* needs the literal "false")
		{"valued true", "aria-hidden", true, ` aria-hidden="true"`},
		{"valued false", "aria-expanded", false, ` aria-expanded="false"`},
		{"valued contenteditable", "contenteditable", false, ` contenteditable="false"`},
		// plain bool on a name the platform never defined → presence, because
		// HTML gives it no value vocabulary and [data-open] matches "false" too
		{"unknown true", "data-open", true, " data-open"},
		{"unknown false", "data-open", false, ""},
		{"unknown custom-element name", "active", true, " active"},
		// named bool honors the same name rule (dispatch unification made this work)
		{"named bool listed", "required", spreadFlag(false), ""},
		{"named bool valued", "aria-expanded", spreadFlag(true), ` aria-expanded="true"`},
		{"named bool unknown", "data-open", spreadFlag(true), " data-open"},
		// Toggle forces presence on ANY name, ignoring the list
		{"toggle unlisted true", "active", Toggle(true), " active"},
		{"toggle unlisted false", "active", Toggle(false), ""},
		{"toggle listed true", "required", Toggle(true), " required"},
		// a string is never affected by the list — required="foo" stays (CSS selector)
		{"string on listed name", "required", "foo", ` required="foo"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := spreadOne(t, c.key, c.val, nil); got != c.want {
				t.Errorf("Spread(%q=%#v) = %q; want %q", c.key, c.val, got, c.want)
			}
		})
	}
}

// A Toggle on a URL-classified name renders bare — it declares no value, so the
// URL sink is inapplicable, not merely skipped. It must NOT become href="true".
func TestSpreadToggleShortCircuitsURLSink(t *testing.T) {
	if got := spreadOne(t, "href", Toggle(true), []string{"href"}); got != " href" {
		t.Errorf("Spread(href=Toggle(true)) = %q; want %q (bare, no value sink)", got, " href")
	}
	if got := spreadOne(t, "href", Toggle(false), []string{"href"}); got != "" {
		t.Errorf("Spread(href=Toggle(false)) = %q; want %q (omitted)", got, "")
	}
}

// A plain bool on a URL-classified name reaches the same verdict as Toggle, and
// for the same reason: presence declares no value, so the sink is inapplicable.
// The bag leaf used to answer AFTER the URL sinks and write href="true" while
// codegen wrote a bare href for the identical source — a divergence between the
// two places BoolRendersBare is consulted.
func TestSpreadBoolOnURLNameMatchesCodegen(t *testing.T) {
	for _, c := range []struct {
		key       string
		val       any
		navNames  []string
		imageName []string
		want      string
	}{
		{"href", true, []string{"href"}, nil, " href"},
		{"href", false, []string{"href"}, nil, ""},
		{"src", spreadFlag(true), []string{"src"}, nil, " src"},
		{"background", true, nil, []string{"background"}, " background"},
		// a string on the same name still sanitizes — the bool short-circuit
		// must not have moved the sink itself
		{"href", "javascript:alert(1)", []string{"href"}, nil, ` href="about:invalid#gsx"`},
	} {
		got := spreadOne(t, c.key, c.val, c.navNames)
		if c.imageName != nil {
			var b strings.Builder
			gw := W(&b)
			gw.Spread(context.Background(), Attrs{{Key: c.key, Value: c.val}}, nil, c.imageName, nil, nil, nil)
			got = b.String()
		}
		if got != c.want {
			t.Errorf("Spread(%q=%#v) = %q; want %q", c.key, c.val, got, c.want)
		}
	}
}

type spreadFlag bool
