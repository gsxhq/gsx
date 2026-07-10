package gsx

import (
	"bytes"
	"context"
	"errors"
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

func TestAttrsGetLastWins(t *testing.T) {
	a := Attrs{{Key: "k", Value: "first"}, {Key: "k", Value: "second"}}
	v, ok := a.Get("k")
	if !ok || v != "second" {
		t.Fatalf("Get last-wins = %v,%v want second,true", v, ok)
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

func TestAttrsMergeIncomingWinsOverReceiverDuplicates(t *testing.T) {
	a := Attrs{{Key: "id", Value: "old-1"}, {Key: "data-x", Value: "keep"}, {Key: "id", Value: "old-2"}}
	got := a.Merge(Attrs{{Key: "id", Value: "new"}})
	want := Attrs{{Key: "data-x", Value: "keep"}, {Key: "id", Value: "new"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Merge with duplicate receiver key = %v, want %v", got, want)
	}
}

func TestAttrsCondThunks(t *testing.T) {
	if got, err := AttrsCond(true, func() (Attrs, error) { return Attrs{{Key: "a", Value: "1"}}, nil }, nil); err != nil || !reflect.DeepEqual(got, Attrs{{Key: "a", Value: "1"}}) {
		t.Fatalf("AttrsCond true = %v, %v", got, err)
	}
	if got, err := AttrsCond(false, func() (Attrs, error) { return Attrs{{Key: "a", Value: "1"}}, nil }, nil); got != nil || err != nil {
		t.Fatalf("AttrsCond false no-else = %v, %v, want nil, nil", got, err)
	}
}

func TestAttrsCondError(t *testing.T) {
	boom := errors.New("boom")
	ok := Attrs{{Key: "class", Value: "hot"}}

	a, err := AttrsCond(true, func() (Attrs, error) { return ok, nil }, nil)
	if err != nil || len(a) != 1 {
		t.Fatalf("taken then = %v, %v", a, err)
	}
	if _, err := AttrsCond(true, func() (Attrs, error) { return nil, boom }, nil); !errors.Is(err, boom) {
		t.Fatalf("then error not propagated: %v", err)
	}
	if _, err := AttrsCond(false, nil, func() (Attrs, error) { return nil, boom }); !errors.Is(err, boom) {
		t.Fatalf("els error not propagated: %v", err)
	}
	// Untaken branch must never run; nil els yields (nil, nil).
	a, err = AttrsCond(false, func() (Attrs, error) { panic("untaken branch ran") }, nil)
	if a != nil || err != nil {
		t.Fatalf("untaken = %v, %v", a, err)
	}
}

func TestSpreadOrderAndDrop(t *testing.T) {
	var buf bytes.Buffer
	gw := W(&buf)
	gw.Spread(context.Background(), Attrs{
		{Key: "data-b", Value: "2"},
		{Key: "data-a", Value: "1"},
		{Key: "data-b", Value: "last"},
		{Key: "checked", Value: true},
		{Key: "skip me", Value: "x"}, // invalid name → dropped
		{Key: "off", Value: false},   // false bool → omitted
	})
	if got := buf.String(); got != ` data-a="1" data-b="last" checked` {
		t.Fatalf("Spread = %q", got)
	}
}

func TestSpreadAggregatesDuplicateClassStyle(t *testing.T) {
	var buf bytes.Buffer
	gw := W(&buf)
	gw.Spread(context.Background(), Attrs{
		{Key: "data-a", Value: "1"},
		{Key: "class", Value: "first"},
		{Key: "data-b", Value: "2"},
		{Key: "class", Value: "second"},
		{Key: "style", Value: "color: red"},
		{Key: "style", Value: "display: block"},
	})
	if got := buf.String(); got != ` data-a="1" data-b="2" class="first second" style="color: red; display: block"` {
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
	recordThen := func() (Attrs, error) { thenRan = true; return Attrs{{Key: "a", Value: "1"}}, nil }
	recordEls := func() (Attrs, error) { elsRan = true; return Attrs{{Key: "b", Value: "2"}}, nil }

	// true branch: only then must run.
	thenRan, elsRan = false, false
	got, err := AttrsCond(true, recordThen, recordEls)
	if err != nil {
		t.Fatalf("AttrsCond(true) unexpected error: %v", err)
	}
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
	got, err = AttrsCond(false, recordThen, recordEls)
	if err != nil {
		t.Fatalf("AttrsCond(false) unexpected error: %v", err)
	}
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
	got, err = AttrsCond(false, recordThen, nil)
	if thenRan || elsRan {
		t.Error("AttrsCond(false, ..., nil): no thunk should run")
	}
	if got != nil || err != nil {
		t.Errorf("AttrsCond(false, ..., nil) = %v, %v, want nil, nil", got, err)
	}
}

func TestConcatAttrs(t *testing.T) {
	a := Attrs{{Key: "a", Value: "1"}}
	b := Attrs{{Key: "b", Value: "2"}, {Key: "a", Value: "3"}}
	got := ConcatAttrs(a, nil, b)
	want := Attrs{{Key: "a", Value: "1"}, {Key: "b", Value: "2"}, {Key: "a", Value: "3"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if ConcatAttrs() != nil || ConcatAttrs(nil, Attrs{}) != nil {
		t.Fatalf("empty concat must be nil")
	}
	// input bags must not be aliased: mutating the result must not touch a or b
	got[0].Value = "mut"
	if a[0].Value != "1" {
		t.Fatalf("ConcatAttrs aliased its input")
	}
}

func TestAttrsGetFold(t *testing.T) {
	a := Attrs{{Key: "HREF", Value: "x"}, {Key: "Href", Value: "y"}}
	// last-wins across case variants; lookup key is lowercase.
	if v, ok := a.GetFold("href"); !ok || v != "y" {
		t.Fatalf("GetFold(href) = %v,%v want y,true", v, ok)
	}
	if _, ok := a.GetFold("src"); ok {
		t.Fatalf("GetFold(src) should be absent")
	}
	if _, ok := (Attrs)(nil).GetFold("href"); ok {
		t.Fatalf("GetFold on nil should be absent")
	}
}

func TestAttrsWithoutFold(t *testing.T) {
	a := Attrs{
		{Key: "HREF", Value: "1"},
		{Key: "data-x", Value: "2"},
		{Key: "Src", Value: "3"},
		{Key: "id", Value: "4"},
	}
	got := a.WithoutFold("href", "src")
	want := Attrs{{Key: "data-x", Value: "2"}, {Key: "id", Value: "4"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("WithoutFold = %v want %v", got, want)
	}
	if a.WithoutFold("href", "data-x", "src", "id") != nil {
		t.Fatalf("WithoutFold dropping everything should be nil")
	}
	// input not mutated
	if a[0].Key != "HREF" {
		t.Fatalf("WithoutFold mutated its input")
	}
}

func TestAttrsWithoutFunc(t *testing.T) {
	a := Attrs{{Key: "keep", Value: "1"}, {Key: "drop", Value: "2"}, {Key: "keep2", Value: "3"}}
	got := a.WithoutFunc(func(k string) bool { return k == "drop" })
	want := Attrs{{Key: "keep", Value: "1"}, {Key: "keep2", Value: "3"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("WithoutFunc = %v want %v", got, want)
	}
	if a.WithoutFunc(func(string) bool { return true }) != nil {
		t.Fatalf("WithoutFunc dropping all should be nil")
	}
}

func TestURLPrefixMatch(t *testing.T) {
	prefixes := []string{"data-url-", "hx-"}
	for _, k := range []string{"data-url-next", "Data-URL-Prev", "hx-get", "HX-Boost"} {
		if !URLPrefixMatch(k, prefixes) {
			t.Errorf("URLPrefixMatch(%q) = false, want true", k)
		}
	}
	for _, k := range []string{"data-x", "href", "data-url", "onhx-"} {
		if URLPrefixMatch(k, prefixes) {
			t.Errorf("URLPrefixMatch(%q) = true, want false", k)
		}
	}
	if URLPrefixMatch("anything", nil) {
		t.Errorf("URLPrefixMatch with no prefixes must be false")
	}
}

func TestSpreadURLPrefixed(t *testing.T) {
	var buf bytes.Buffer
	gw := W(&buf)
	gw.SpreadURLPrefixed(context.Background(), Attrs{
		{Key: "data-url-next", Value: "javascript:alert(1)"}, // sanitized via strict sink
		{Key: "data-x", Value: "kept-elsewhere"},             // not a prefix match → skipped
		{Key: "Data-URL-Prev", Value: "/ok"},                 // case-variant key matches
		{Key: "data-url-next", Value: "/last"},               // last-wins duplicate
		{Key: "bad key", Value: "y"},                         // invalid name → dropped
	}, []string{"data-url-"}, nil)
	got := buf.String()
	if strings.Contains(got, "javascript:") {
		t.Fatalf("SpreadURLPrefixed leaked a javascript: URL: %q", got)
	}
	want := ` Data-URL-Prev="/ok" data-url-next="/last"`
	if got != want {
		t.Fatalf("SpreadURLPrefixed = %q want %q", got, want)
	}
}

func TestSpreadURLPrefixedExcluded(t *testing.T) {
	var buf bytes.Buffer
	gw := W(&buf)
	// A force-owned prefix key must be SKIPPED (its forced attr owns it), matched
	// case-insensitively; a non-excluded prefix key still writes (sanitized).
	gw.SpreadURLPrefixed(context.Background(), Attrs{
		{Key: "data-url-next", Value: "javascript:alert(1)"}, // force-owned → skipped
		{Key: "Data-URL-Keep", Value: "/ok"},                 // not excluded → written
	}, []string{"data-url-"}, []string{"data-url-next"})
	got := buf.String()
	if strings.Contains(got, "data-url-next") {
		t.Fatalf("SpreadURLPrefixed wrote a force-owned key: %q", got)
	}
	if want := ` Data-URL-Keep="/ok"`; got != want {
		t.Fatalf("SpreadURLPrefixed = %q want %q", got, want)
	}
}

func TestSpreadURLPrefixedRawURL(t *testing.T) {
	var buf bytes.Buffer
	gw := W(&buf)
	gw.SpreadURLPrefixed(context.Background(), Attrs{
		{Key: "data-url-x", Value: RawURL("app://z")}, // author vouch passes verbatim
	}, []string{"data-url-"}, nil)
	if got := buf.String(); got != ` data-url-x="app://z"` {
		t.Fatalf("SpreadURLPrefixed RawURL = %q", got)
	}
}
