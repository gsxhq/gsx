package htmlattr

import "testing"

func TestHTMXURL(t *testing.T) {
	// The seven htmx request-URL attributes (htmx 2's five method attributes,
	// htmx 4's hx-query and hx-action) in each spelling htmx 4's attribute
	// lookup reads: plain, :inherited, :append, :inherited:append.
	for _, base := range []string{"hx-get", "hx-post", "hx-put", "hx-delete", "hx-patch", "hx-query", "hx-action"} {
		for _, n := range []string{base, base + ":inherited", base + ":append", base + ":inherited:append"} {
			if !HTMXURL(n) {
				t.Errorf("HTMXURL(%q) = false, want true", n)
			}
		}
	}
	// Names fold case, like every HTML attribute name.
	if !HTMXURL("HX-GET") || !HTMXURL("Hx-Action:Inherited") {
		t.Error("HTMXURL must fold case")
	}
	for _, n := range []string{
		"hx-method", // names the verb, not a URL
		"hx-swap", "hx-target", "hx-trigger", "hx-target:inherited", "hx-confirm:inherited:append",
		"hx-get:append:inherited", // suffixes in the wrong order are not read by htmx
		"hx-get:inherit", "hx-get:", "hx-get:inherited:inherited", "hx-get:append:append",
		"hx-gets", "hx-get-", "xhx-get", "hx-on:click", "href", "",
	} {
		if HTMXURL(n) {
			t.Errorf("HTMXURL(%q) = true, want false", n)
		}
	}
}
