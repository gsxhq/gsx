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

// TestSpreadOrderAndDrop exercises SpreadForwarding with no forwarding args
// (navNames/imageNames/prefixes/excluded all nil) — the standalone-spread shape
// a hand-written call or a nested cond-attr spread uses now that the old
// non-sanitizing Spread method is gone. Order/last-wins/drop semantics are
// unchanged: SpreadForwarding is the sole spread implementation.
func TestSpreadOrderAndDrop(t *testing.T) {
	var buf bytes.Buffer
	gw := W(&buf)
	gw.SpreadForwarding(context.Background(), Attrs{
		{Key: "data-b", Value: "2"},
		{Key: "data-a", Value: "1"},
		{Key: "data-b", Value: "last"},
		{Key: "checked", Value: true},
		{Key: "skip me", Value: "x"}, // invalid name → dropped
		{Key: "off", Value: false},   // false bool → omitted
	}, nil, nil, nil, nil)
	if got := buf.String(); got != ` data-a="1" data-b="last" checked` {
		t.Fatalf("SpreadForwarding = %q", got)
	}
}

func TestSpreadAggregatesDuplicateClassStyle(t *testing.T) {
	var buf bytes.Buffer
	gw := W(&buf)
	gw.SpreadForwarding(context.Background(), Attrs{
		{Key: "data-a", Value: "1"},
		{Key: "class", Value: "first"},
		{Key: "data-b", Value: "2"},
		{Key: "class", Value: "second"},
		{Key: "style", Value: "color: red"},
		{Key: "style", Value: "display: block"},
	}, nil, nil, nil, nil)
	if got := buf.String(); got != ` data-a="1" data-b="2" class="first second" style="color: red; display: block"` {
		t.Fatalf("SpreadForwarding = %q", got)
	}
}

// TestSpreadSecurityDropsInvalidNames verifies that SpreadForwarding (with no
// forwarding args — the standalone-spread shape) drops structurally unsafe
// attribute names (tag-breakout, whitespace, prohibited chars) while keeping
// legitimate special names used by frameworks. This is a regression guard for
// the validAttrName contract (a port of html/template name-safety).
func TestSpreadSecurityDropsInvalidNames(t *testing.T) {
	unsafe := []string{
		"x onmouseover=y", // space → could inject a second attribute
		">",               // > breaks tag structure
		"a/b",             // / is a prohibited character
	}
	// Empty key is also invalid; check separately since strings.Contains("", "") is always true.
	emptyKeyBag := Attrs{{Key: "", Value: "evil"}}
	var emptyBuf bytes.Buffer
	W(&emptyBuf).SpreadForwarding(context.Background(), emptyKeyBag, nil, nil, nil, nil)
	if emptyBuf.Len() != 0 {
		t.Errorf("SpreadForwarding with empty key should emit nothing, got: %q", emptyBuf.String())
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
	gw.SpreadForwarding(context.Background(), bag, nil, nil, nil, nil)
	out := buf.String()

	for _, k := range unsafe {
		if strings.Contains(out, k) {
			t.Errorf("SpreadForwarding should have dropped unsafe key %q, but output contains it: %s", k, out)
		}
	}
	for _, k := range kept {
		if !strings.Contains(out, k) {
			t.Errorf("SpreadForwarding should have kept legitimate key %q, but output is: %s", k, out)
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

// TestSpreadForwarding drives the single-pass forwarding-element writer through
// every routing case at once: nav (href), image (src), prefix (data-url-x),
// excluded (class/forced), plain (data-n), a case-variant unsafe key (HREF), a
// RawURL vouch, and a scalar duplicate — asserting one ordered pass routes each
// to the right sink, sanitizes the smuggled HREF, resolves last-wins, and
// preserves the bag's authored order (URL keys render IN position).
func TestSpreadForwarding(t *testing.T) {
	var buf bytes.Buffer
	gw := W(&buf)
	navNames := []string{"action", "href", "src"} // "src" here would be nav…
	imageNames := []string{"src"}                 // …but image wins (checked first)
	prefixes := []string{"data-url-"}
	excluded := []string{"class", "style", "id"} // class/style merged elsewhere; id forced
	gw.SpreadForwarding(context.Background(), Attrs{
		{Key: "data-n", Value: "1"},                       // plain, first position
		{Key: "href", Value: "/nav"},                      // nav sink
		{Key: "src", Value: "data:image/png;base64,AAAA"}, // image sink (data:image ok)
		{Key: "data-url-x", Value: "javascript:alert(1)"}, // prefix → strict nav sink, sanitized
		{Key: "class", Value: "c"},                        // excluded → skipped (merged separately)
		{Key: "id", Value: "forced"},                      // excluded (forced) → skipped
		{Key: "HREF", Value: "javascript:alert(2)"},       // case-variant nav → sanitized, not smuggled
		{Key: "data-n", Value: "last"},                    // scalar duplicate → last-wins
		{Key: "aria-x", Value: RawURL("app://ok")},        // RawURL but not URL-classified → plain string, escaped verbatim
		{Key: "action", Value: RawURL("app://vouch")},     // RawURL through nav sink → verbatim
		{Key: "checked", Value: true},                     // bool → BoolAttr
		{Key: "bad key", Value: "x"},                      // invalid name → dropped
	}, navNames, imageNames, prefixes, excluded)
	got := buf.String()
	// Security: neither the case-variant HREF nor the prefix key smuggles a scheme.
	if strings.Contains(got, "javascript:") {
		t.Fatalf("SpreadForwarding leaked a javascript: URL: %q", got)
	}
	// data: image URL survives the image sink (would be blocked by the strict nav sink).
	if !strings.Contains(got, `src="data:image/png;base64,AAAA"`) {
		t.Fatalf("SpreadForwarding dropped a valid data:image URL: %q", got)
	}
	// One pass, bag order preserved: URL keys render in position; the duplicate
	// data-n is last-wins so it renders at its LAST slot (like Spread); class/style
	// /id excluded; the case-variant HREF sanitized in place.
	want := ` href="/nav" src="data:image/png;base64,AAAA"` +
		` data-url-x="about:invalid#gsx" HREF="about:invalid#gsx" data-n="last"` +
		` aria-x="app://ok" action="app://vouch" checked`
	if got != want {
		t.Fatalf("SpreadForwarding =\n  %q\nwant\n  %q", got, want)
	}
}

// TestSpreadForwardingImageBeatsNav pins the routing precedence: a name in BOTH
// navNames and imageNames takes the image sink (imageNames is checked first), so
// an <img src=data:image/*> forwarded through a bag keeps its data: URL.
func TestSpreadForwardingImageBeatsNav(t *testing.T) {
	var buf bytes.Buffer
	gw := W(&buf)
	gw.SpreadForwarding(context.Background(), Attrs{
		{Key: "src", Value: "data:image/gif;base64,R0lGOD"},
	}, []string{"src"}, []string{"src"}, nil, nil)
	if want := ` src="data:image/gif;base64,R0lGOD"`; buf.String() != want {
		t.Fatalf("SpreadForwarding image-beats-nav = %q want %q", buf.String(), want)
	}
}

// TestSpreadForwardingNavRejectsDataImage confirms a data:image URL on a
// NAV-classified key (not in imageNames) is rejected by the strict sink — the
// image allowance is name-scoped, never global.
func TestSpreadForwardingNavRejectsDataImage(t *testing.T) {
	var buf bytes.Buffer
	gw := W(&buf)
	gw.SpreadForwarding(context.Background(), Attrs{
		{Key: "href", Value: "data:image/png;base64,AAAA"},
	}, []string{"href"}, nil, nil, nil)
	if got := buf.String(); got != ` href="about:invalid#gsx"` {
		t.Fatalf("SpreadForwarding nav must reject data:image = %q", got)
	}
}

// TestSpreadForwardingExcludedCaseInsensitive verifies excluded matches fold
// (HTML attr names fold), so a case-variant of a force-owned name is skipped —
// the forced attr, emitted unguarded elsewhere, is the sole value.
func TestSpreadForwardingExcludedCaseInsensitive(t *testing.T) {
	var buf bytes.Buffer
	gw := W(&buf)
	gw.SpreadForwarding(context.Background(), Attrs{
		{Key: "HREF", Value: "javascript:alert(1)"}, // force-owned via excluded → skipped entirely
		{Key: "data-keep", Value: "ok"},
	}, []string{"href"}, nil, nil, []string{"href"})
	got := buf.String()
	if strings.Contains(got, "HREF") || strings.Contains(got, "javascript:") {
		t.Fatalf("SpreadForwarding wrote a force-owned case-variant key: %q", got)
	}
	if want := ` data-keep="ok"`; got != want {
		t.Fatalf("SpreadForwarding = %q want %q", got, want)
	}
}

// TestSpreadForwardingAggregatesClassStyle pins the standalone-spread shape: with
// excluded=nil (e.g. a spread nested in a cond-attr, where no forced site owns
// class/style), a non-excluded class/style key aggregates via a.Class()/a.Style()
// at its last source position rather than being rendered raw per-entry.
func TestSpreadForwardingAggregatesClassStyle(t *testing.T) {
	var buf bytes.Buffer
	gw := W(&buf)
	gw.SpreadForwarding(context.Background(), Attrs{
		{Key: "class", Value: "a"},
		{Key: "class", Value: "b"},
		{Key: "style", Value: "color:red"},
		{Key: "style", Value: "margin:0"},
	}, nil, nil, nil, nil)
	if got, want := buf.String(), ` class="a b" style="color:red; margin:0"`; got != want {
		t.Fatalf("SpreadForwarding class/style aggregation = %q, want %q", got, want)
	}
	if got := buf.String(); !strings.Contains(got, `class="a b"`) {
		t.Fatalf("SpreadForwarding did not aggregate class: %q", got)
	}
}
