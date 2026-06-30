package gsx

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestAttrMapToAttrsSorts(t *testing.T) {
	got := AttrMap{"id": "x", "class": "c", "data-z": 1}.ToAttrs()
	want := Attrs{{Key: "class", Value: "c"}, {Key: "data-z", Value: 1}, {Key: "id", Value: "x"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ToAttrs = %v, want %v", got, want)
	}
	if (AttrMap)(nil).ToAttrs() != nil {
		t.Fatal("AttrMap(nil).ToAttrs() should be nil")
	}
	if (AttrMap{}).ToAttrs() != nil {
		t.Fatal("empty AttrMap.ToAttrs() should be nil")
	}
}

func TestAttrsClassStyleAggregate(t *testing.T) {
	a := Attrs{{Key: "class", Value: "a"}, {Key: "x", Value: "1"}, {Key: "class", Value: "b"}}
	if got := a.Class(); got != "a b" {
		t.Fatalf("Class aggregate = %q, want %q", got, "a b")
	}
	s := Attrs{{Key: "style", Value: "color:red"}, {Key: "style", Value: "margin:0"}}
	if got := s.Style(); got != "color:red; margin:0" {
		t.Fatalf("Style aggregate = %q, want %q", got, "color:red; margin:0")
	}
}

func TestAttrsGetFirstWins(t *testing.T) {
	a := Attrs{{Key: "k", Value: "first"}, {Key: "k", Value: "second"}}
	v, ok := a.Get("k")
	if !ok || v != "first" {
		t.Fatalf("Get first-wins = %v,%v want first,true", v, ok)
	}
	if !a.Has("k") || a.Has("nope") {
		t.Fatal("Has wrong")
	}
}

func TestAttrsWithoutRemovesAll(t *testing.T) {
	a := Attrs{{Key: "class", Value: "a"}, {Key: "x", Value: "1"}, {Key: "class", Value: "b"}}
	got := a.Without("class")
	want := Attrs{{Key: "x", Value: "1"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Without = %v, want %v", got, want)
	}
	if Attrs(nil).Without("x") != nil {
		t.Fatal("Without on nil should be nil")
	}
}

func TestAttrsMergeOverwriteInPlace(t *testing.T) {
	a := Attrs{{Key: "id", Value: "old"}, {Key: "class", Value: "base"}}
	got := a.Merge(Attrs{{Key: "id", Value: "new"}, {Key: "class", Value: "extra"}, {Key: "data-x", Value: "1"}})
	want := Attrs{{Key: "id", Value: "new"}, {Key: "class", Value: "base extra"}, {Key: "data-x", Value: "1"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Merge = %v, want %v", got, want)
	}
}

func TestAttrsCondThunks(t *testing.T) {
	if got := AttrsCond(true, func() Attrs { return Attrs{{Key: "a", Value: "1"}} }, nil); !reflect.DeepEqual(got, Attrs{{Key: "a", Value: "1"}}) {
		t.Fatalf("AttrsCond true = %v", got)
	}
	if got := AttrsCond(false, func() Attrs { return Attrs{{Key: "a", Value: "1"}} }, nil); got != nil {
		t.Fatalf("AttrsCond false no-else = %v, want nil", got)
	}
}

func TestSpreadOrderAndDrop(t *testing.T) {
	var buf bytes.Buffer
	gw := W(&buf)
	gw.Spread(context.Background(), Attrs{
		{Key: "data-b", Value: "2"},
		{Key: "data-a", Value: "1"},
		{Key: "checked", Value: true},
		{Key: "skip me", Value: "x"}, // invalid name → dropped
		{Key: "off", Value: false},   // false bool → omitted
	})
	if got := buf.String(); got != ` data-b="2" data-a="1" checked` {
		t.Fatalf("Spread = %q", got)
	}
}

// TestSpreadSecurityDropsInvalidNames verifies that Spread drops structurally
// unsafe attribute names (tag-breakout, whitespace, prohibited chars) while
// keeping legitimate special names used by frameworks.  This is a regression
// guard for the validAttrName contract (a port of html/template name-safety).
func TestSpreadSecurityDropsInvalidNames(t *testing.T) {
	unsafe := []string{
		"x onmouseover=y", // space → could inject a second attribute
		">",               // > breaks tag structure
		"a/b",             // / is a prohibited character
	}
	// Empty key is also invalid; check separately since strings.Contains("", "") is always true.
	emptyKeyBag := Attrs{{Key: "", Value: "evil"}}
	var emptyBuf bytes.Buffer
	W(&emptyBuf).Spread(context.Background(), emptyKeyBag)
	if emptyBuf.Len() != 0 {
		t.Errorf("Spread with empty key should emit nothing, got: %q", emptyBuf.String())
	}
	kept := []string{
		"hx-on::click",
		":class",
		"@click.away",
		"data-x",
		"_",
	}

	bag := make(Attrs, 0, len(unsafe)+len(kept))
	for _, k := range unsafe {
		bag = append(bag, Attr{Key: k, Value: "evil"})
	}
	for _, k := range kept {
		bag = append(bag, Attr{Key: k, Value: "ok"})
	}

	var buf bytes.Buffer
	gw := W(&buf)
	gw.Spread(context.Background(), bag)
	out := buf.String()

	for _, k := range unsafe {
		if strings.Contains(out, k) {
			t.Errorf("Spread should have dropped unsafe key %q, but output contains it: %s", k, out)
		}
	}
	for _, k := range kept {
		if !strings.Contains(out, k) {
			t.Errorf("Spread should have kept legitimate key %q, but output is: %s", k, out)
		}
	}
}

// TestAttrsCondLazyEval verifies that AttrsCond never invokes the untaken
// branch.  This is a regression guard: if the untaken thunk were eagerly
// called, code like `AttrsCond(u != nil, func() Attrs{...u.Name...}, nil)`
// would panic on the nil path.
func TestAttrsCondLazyEval(t *testing.T) {
	thenRan, elsRan := false, false
	recordThen := func() Attrs { thenRan = true; return Attrs{{Key: "a", Value: "1"}} }
	recordEls := func() Attrs { elsRan = true; return Attrs{{Key: "b", Value: "2"}} }

	// true branch: only then must run.
	thenRan, elsRan = false, false
	got := AttrsCond(true, recordThen, recordEls)
	if !thenRan {
		t.Error("AttrsCond(true): then thunk was not called")
	}
	if elsRan {
		t.Error("AttrsCond(true): els thunk must not be called")
	}
	if len(got) == 0 || got[0].Key != "a" {
		t.Errorf("AttrsCond(true) = %v, want [{a 1}]", got)
	}

	// false branch: only els must run.
	thenRan, elsRan = false, false
	got = AttrsCond(false, recordThen, recordEls)
	if thenRan {
		t.Error("AttrsCond(false): then thunk must not be called")
	}
	if !elsRan {
		t.Error("AttrsCond(false): els thunk was not called")
	}
	if len(got) == 0 || got[0].Key != "b" {
		t.Errorf("AttrsCond(false) = %v, want [{b 2}]", got)
	}

	// false + nil els: neither thunk runs, result is nil.
	thenRan, elsRan = false, false
	got = AttrsCond(false, recordThen, nil)
	if thenRan || elsRan {
		t.Error("AttrsCond(false, ..., nil): no thunk should run")
	}
	if got != nil {
		t.Errorf("AttrsCond(false, ..., nil) = %v, want nil", got)
	}
}
