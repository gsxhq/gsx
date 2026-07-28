package gsx

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
	for name := range want {
		if !trueFalseAttr(name) {
			t.Errorf("trueFalseAttr(%q) = false, but its value vocabulary is true/false; regenerate with go generate ./internal/htmldata", name)
		}
		if BoolRendersBare(name) {
			t.Errorf("BoolRendersBare(%q) = true; an attribute whose values ARE \"true\"/\"false\" must stringify", name)
		}
	}

	// The reverse direction: nothing may creep into the generated table that the
	// dataset does not justify. trueFalseExtras is the curated escape hatch and
	// is deliberately separate, so it must NOT appear in the generated switch.
	for _, name := range []string{"contenteditable", "writingsuggestions"} {
		if trueFalseAttr(name) {
			t.Errorf("trueFalseAttr(%q) = true; curated names belong in trueFalseExtras, not the generated table", name)
		}
	}
}

// The curated extras exist because the dataset records no value vocabulary for
// them, yet HTML's "false" is load-bearing: both inherit, so only the literal
// "false" opts a subtree out.
func TestTrueFalseExtras(t *testing.T) {
	for name := range trueFalseExtras {
		if BoolRendersBare(name) {
			t.Errorf("BoolRendersBare(%q) = true; want false (enumerated true/false, and it inherits)", name)
		}
	}
}
