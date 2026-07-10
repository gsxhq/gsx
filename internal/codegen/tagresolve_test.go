package codegen

import (
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
	"go/token"
)

// collectStamps walks the file and returns tag -> IsComponent for every element.
// Duplicate tags append "#2", "#3", ... in walk order so tests can address them.
func collectStamps(f *gsxast.File) map[string]bool {
	out := map[string]bool{}
	var walk func(nodes []gsxast.Markup)
	record := func(el *gsxast.Element) {
		key := el.Tag
		for i := 2; ; i++ {
			if _, dup := out[key]; !dup {
				break
			}
			key = el.Tag + "#" + string(rune('0'+i))
		}
		out[key] = el.IsComponent
	}
	walk = func(nodes []gsxast.Markup) {
		forEachElement(nodes, func(el *gsxast.Element) { record(el) })
	}
	for _, d := range f.Decls {
		switch t := d.(type) {
		case *gsxast.Component:
			walk(t.Body)
		case *gsxast.GoWithElements:
			for _, p := range t.Parts {
				switch pt := p.(type) {
				case *gsxast.Element:
					record(pt)
					walk(pt.Children)
				case *gsxast.Fragment:
					walk(pt.Children)
				}
			}
		}
	}
	return out
}

func TestResolveComponentTags(t *testing.T) {
	f := parseGSXForTest(t, `package views

component card() {
	<div class="c">{children}</div>
}

component div() {
	<div>{children}</div>
}

component Page() {
	<card/>
	<span/>
	<my-widget/>
	<ui.Button/>
	<div/>
}
`)
	declNames := map[string]bool{"card": true, "div": true, "Page": true}
	bag := diag.NewBag(token.NewFileSet())
	resolveComponentTags(f, declNames, bag)
	got := collectStamps(f)

	// Ordering note: card's body <div> walks first (leaf: no exclusion needed,
	// "div" IS declared → true!). Assert precisely instead of via a flat map.
	// Inside card's body, <div> must be TRUE (div is a declared component,
	// card ≠ div — no exclusion). Inside div's body, <div> must be FALSE
	// (self-exclusion). Inside Page, <div> must be TRUE.
	if !got["div"] { // card's body
		t.Error("inside card: <div> should resolve to the div component")
	}
	if got["div#2"] { // div's own body
		t.Error("inside component div: <div> must be the leaf (self-exclusion)")
	}
	if !got["div#3"] { // Page's body
		t.Error("inside Page: <div> should resolve to the div component")
	}
	if !got["card"] || got["span"] || got["my-widget"] || !got["ui.Button"] {
		t.Errorf("stamps wrong: %v", got)
	}
}

func TestResolveComponentTagsMethodExclusion(t *testing.T) {
	f := parseGSXForTest(t, `package views

type page struct{}

component (p page) item() {
	<item/>
}
`)
	bag := diag.NewBag(token.NewFileSet())
	resolveComponentTags(f, map[string]bool{"item": true, "page": true}, bag)
	got := collectStamps(f)
	if got["item"] {
		t.Error("inside method item: <item> must be leaf (exclusion keyed by name, methods included)")
	}
}

func TestResolveComponentTagsGoWithElements(t *testing.T) {
	f := parseGSXForTest(t, `package views

component chip() {
	<b>x</b>
}

var chip2 = <div><chip/></div>

var chip3 = <chip3/>
`)
	bag := diag.NewBag(token.NewFileSet())
	resolveComponentTags(f, map[string]bool{"chip": true, "chip2": true, "chip3": true}, bag)
	got := collectStamps(f)
	if !got["chip"] {
		t.Error("<chip/> inside var chip2 initializer should resolve")
	}
	if got["chip3"] {
		t.Error("<chip3/> inside var chip3 must be leaf (self-exclusion in var initializer)")
	}
}

func TestResolveComponentTagsGoWithElementsFragment(t *testing.T) {
	f := parseGSXForTest(t, `package views

component chip() {
	<b>x</b>
}

var pair = <><chip/><pair/></>
`)
	bag := diag.NewBag(token.NewFileSet())
	resolveComponentTags(f, map[string]bool{"chip": true, "pair": true}, bag)
	got := collectStamps(f)
	if !got["chip"] {
		t.Error("<chip/> inside var pair's fragment initializer should resolve")
	}
	if got["pair"] {
		t.Error("<pair/> inside var pair's fragment initializer must be leaf (self-exclusion)")
	}
}

func TestForEachElementCompleteness(t *testing.T) {
	f := parseGSXForTest(t, `package views

component Page(items []string, on bool) {
	<a1/>
	<div><a2/></div>
	<Card header={<a3/>}/>
	<>
		<a4/>
	</>
	{ if on {
		<a5/>
	} else {
		<a6/>
	} }
	{ for _, it := range items {
		<a7>{it}</a7>
	} }
	{ switch {
	case on:
		<a8/>
	} }
}
`)
	seen := map[string]bool{}
	for _, d := range f.Decls {
		if c, ok := d.(*gsxast.Component); ok {
			forEachElement(c.Body, func(el *gsxast.Element) { seen[el.Tag] = true })
		}
	}
	for _, tag := range []string{"a1", "a2", "a3", "a4", "a5", "a6", "a7", "a8", "div", "Card"} {
		if !seen[tag] {
			t.Errorf("forEachElement missed <%s>", tag)
		}
	}
}
