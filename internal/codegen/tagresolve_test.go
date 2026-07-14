package codegen

import (
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
	"go/token"
)

func preprocessTagsForTest(t testing.TB, fset *token.FileSet, file *gsxast.File, declNames map[string]bool, bag *diag.Bag) *callSiteRegistry {
	t.Helper()
	preprocessed, err := preprocessComponentCallSites(map[string]*gsxast.File{"test.gsx": file}, declNames, fset, nil, bag)
	if err != nil {
		t.Fatalf("preprocess component tags: %v", err)
	}
	if !preprocessed.analysisReady() {
		t.Fatalf("preprocess component tags was not analysis-ready: %+v", bag.Sorted())
	}
	return preprocessed.registry
}

// collectStamps returns tag -> IsComponent for every parsed-tree element.
// Duplicate tags append "#2", "#3", ... in walk order so tests can address them.
func collectStamps(f *gsxast.File) map[string]bool {
	out := map[string]bool{}
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
	gsxast.Inspect(f, func(node gsxast.Node) bool {
		if el, ok := node.(*gsxast.Element); ok {
			record(el)
		}
		return true
	})
	return out
}

func TestResolveComponentTags(t *testing.T) {
	f, fset := parseGSXForTestWithFset(t, `package views

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
	bag := diag.NewBag(fset)
	preprocessTagsForTest(t, fset, f, declNames, bag)
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
	f, fset := parseGSXForTestWithFset(t, `package views

type page struct{}

component (p page) item() {
	<item/>
}
`)
	bag := diag.NewBag(fset)
	preprocessTagsForTest(t, fset, f, map[string]bool{"item": true, "page": true}, bag)
	got := collectStamps(f)
	if got["item"] {
		t.Error("inside method item: <item> must be leaf (exclusion keyed by name, methods included)")
	}
}

func TestResolveComponentTagsGoWithElements(t *testing.T) {
	f, fset := parseGSXForTestWithFset(t, `package views

component chip() {
	<b>x</b>
}

var chip2 = <div><chip/></div>

var chip3 = <chip3/>
`)
	bag := diag.NewBag(fset)
	preprocessTagsForTest(t, fset, f, map[string]bool{"chip": true, "chip2": true, "chip3": true}, bag)
	got := collectStamps(f)
	if !got["chip"] {
		t.Error("<chip/> inside var chip2 initializer should resolve")
	}
	if got["chip3"] {
		t.Error("<chip3/> inside var chip3 must be leaf (self-exclusion in var initializer)")
	}
}

func TestResolveComponentTagsGoWithElementsFragment(t *testing.T) {
	f, fset := parseGSXForTestWithFset(t, `package views

component chip() {
	<b>x</b>
}

var pair = <><chip/><pair/></>
`)
	bag := diag.NewBag(fset)
	preprocessTagsForTest(t, fset, f, map[string]bool{"chip": true, "pair": true}, bag)
	got := collectStamps(f)
	if !got["chip"] {
		t.Error("<chip/> inside var pair's fragment initializer should resolve")
	}
	if got["pair"] {
		t.Error("<pair/> inside var pair's fragment initializer must be leaf (self-exclusion)")
	}
}

func TestResolveComponentTagsGroupedVarSpec(t *testing.T) {
	// A grouped var (...) block: the element lives in the SECOND ValueSpec, so
	// the exclusion must be keyed by THAT spec's name (mywidget), not the
	// group's first spec (unrelated). Pins both directions: <mywidget> under
	// var mywidget self-excludes (leaf), while a sibling tag naming the OTHER
	// spec's var (<unrelated/>) still resolves to a component.
	f, fset := parseGSXForTestWithFset(t, `package views

var (
	unrelated = 1
	mywidget  = <mywidget><unrelated/></mywidget>
)
`)
	bag := diag.NewBag(fset)
	preprocessTagsForTest(t, fset, f, map[string]bool{"unrelated": true, "mywidget": true}, bag)
	got := collectStamps(f)
	if got["mywidget"] {
		t.Error("inside var mywidget: <mywidget> must be leaf (self-exclusion keyed by the containing spec, not the group's first spec)")
	}
	if !got["unrelated"] {
		t.Error("inside var mywidget: <unrelated/> should resolve to the unrelated declaration")
	}
}

func TestResolveComponentTagsSelfReferenceWarning(t *testing.T) {
	f, fset := parseGSXForTestWithFset(t, `package views

component item() {
	<item>x</item>
}
`)
	bag := diag.NewBag(fset)
	preprocessTagsForTest(t, fset, f, map[string]bool{"item": true}, bag)
	diags := bag.Sorted()
	if len(diags) != 1 {
		t.Fatalf("got %d diagnostics, want 1: %+v", len(diags), diags)
	}
	d := diags[0]
	if d.Severity != diag.Warning || d.Code != "self-reference-leaf" {
		t.Errorf("diag = %+v, want severity=Warning code=self-reference-leaf", d)
	}
	got := collectStamps(f)
	if got["item"] {
		t.Error("<item> inside component item must be the leaf (self-exclusion)")
	}
}

func TestResolveComponentTagsWrapperNoWarning(t *testing.T) {
	// div/span ARE real HTML elements — the wrapper pattern (Task 5's
	// wrapper_self_exclusion.txtar) must stay silent.
	f, fset := parseGSXForTestWithFset(t, `package views

component div() {
	<div class="wrapped">{children}</div>
}
`)
	bag := diag.NewBag(fset)
	preprocessTagsForTest(t, fset, f, map[string]bool{"div": true}, bag)
	if diags := bag.Sorted(); len(diags) != 0 {
		t.Errorf("got %d diagnostics, want 0 (div is a real HTML element): %+v", len(diags), diags)
	}
}

func TestPreprocessComponentTagTraversalCompleteness(t *testing.T) {
	f, fset := parseGSXForTestWithFset(t, `package views

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
	preprocessTagsForTest(t, fset, f, map[string]bool{"Page": true}, diag.NewBag(fset))
	seen := map[string]bool{}
	gsxast.Inspect(f, func(node gsxast.Node) bool {
		if el, ok := node.(*gsxast.Element); ok {
			seen[el.Tag] = true
		}
		return true
	})
	for _, tag := range []string{"a1", "a2", "a3", "a4", "a5", "a6", "a7", "a8", "div", "Card"} {
		if !seen[tag] {
			t.Errorf("canonical preprocessor missed <%s>", tag)
		}
	}
}
