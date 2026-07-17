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
		// plain bool on a NON-boolean name → stringify (the reversal this fixes)
		{"unlisted true", "aria-hidden", true, ` aria-hidden="true"`},
		{"unlisted false", "data-open", false, ` data-open="false"`},
		// named bool honors the same name rule (dispatch unification made this work)
		{"named bool listed", "required", spreadFlag(false), ""},
		{"named bool unlisted", "aria-expanded", spreadFlag(true), ` aria-expanded="true"`},
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

type spreadFlag bool
