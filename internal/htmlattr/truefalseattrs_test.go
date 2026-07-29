package htmlattr

import (
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/htmldata"
)

// trueFalseAttr is generated from the dataset internal/htmldata exposes. Rather
// than pin the bytes, re-derive the criterion here and compare: an attribute
// belongs iff its own value vocabulary lists both "true" and "false". A dataset
// refresh that adds or drops such an attribute fails this test until the table
// is regenerated — and regenerating one file without the other would silently
// change how every bool-valued attribute renders.
func TestTrueFalseAttrMatchesHTMLData(t *testing.T) {
	trueFalseSet := map[string]bool{}
	for name, values := range htmldata.ValueSets {
		var hasTrue, hasFalse bool
		for _, v := range values {
			switch v.Name {
			case "true":
				hasTrue = true
			case "false":
				hasFalse = true
			}
		}
		trueFalseSet[name] = hasTrue && hasFalse
	}

	want := map[string]bool{}
	collect := func(attrs []htmldata.Attribute) {
		for _, a := range attrs {
			if strings.ToLower(a.Name) != a.Name {
				t.Errorf("attribute name %q is not lowercase; the generated switch would never match it", a.Name)
			}
			if trueFalseSet[a.ValueSet] {
				want[a.Name] = true
			}
		}
	}
	collect(htmldata.GlobalAttributes)
	for _, tag := range htmldata.Tags {
		collect(tag.Attrs)
	}

	if len(want) == 0 {
		t.Fatal("derived no true/false attributes from htmldata; the dataset or its schema changed")
	}

	// Both directions, over every name the dataset knows. Checking only the
	// forward direction (or a couple of hand-picked names in reverse) lets an
	// extra `case "href",` sit in the generated switch undetected, which would
	// silently turn href={b} from presence into href="true".
	seen := map[string]bool{}
	check := func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		wantTrueFalse := want[name] && datasetTrueFalseErrors[name] == ""
		if got := trueFalseAttr(name); got != wantTrueFalse {
			t.Errorf("trueFalseAttr(%q) = %v, want %v; regenerate with go generate ./internal/htmldata", name, got, wantTrueFalse)
		}
		if wantTrueFalse && RendersBare(name) {
			t.Errorf("RendersBare(%q) = true; an attribute whose values ARE \"true\"/\"false\" must stringify", name)
		}
	}
	for _, a := range htmldata.GlobalAttributes {
		check(a.Name)
	}
	for _, tag := range htmldata.Tags {
		for _, a := range tag.Attrs {
			check(a.Name)
		}
	}
	// Sanity: the sweep above must have covered the names most likely to be
	// mis-added, so a future dataset reshuffle cannot quietly empty it out.
	for _, name := range []string{"href", "src", "sandbox", "class", "aria-hidden"} {
		if !seen[name] {
			t.Errorf("%q was not covered by the dataset sweep; the drift gate is weaker than it looks", name)
		}
	}

	// The curated escape hatches are deliberately separate from the generated
	// table, so neither may appear in the switch.
	for name := range trueFalseExtras {
		if trueFalseAttr(name) {
			t.Errorf("trueFalseAttr(%q) = true; curated names belong in trueFalseExtras, not the generated table", name)
		}
	}
	for name, why := range datasetTrueFalseErrors {
		if trueFalseAttr(name) {
			t.Errorf("trueFalseAttr(%q) = true; the generator must exclude it (%s)", name, why)
		}
		if !RendersBare(name) {
			t.Errorf("RendersBare(%q) = false; want true (%s)", name, why)
		}
	}
}

// The curated extras exist because the dataset records no value vocabulary for
// them, yet HTML's "false" is load-bearing: both inherit, so only the literal
// "false" opts a subtree out.
func TestTrueFalseExtras(t *testing.T) {
	for name := range trueFalseExtras {
		if RendersBare(name) {
			t.Errorf("RendersBare(%q) = true; want false (enumerated true/false, and it inherits)", name)
		}
	}
}
