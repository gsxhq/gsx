package gsx

import (
	"bytes"
	"context"
	"reflect"
	"testing"
)

func TestAttrsFromMapSorts(t *testing.T) {
	got := AttrsFromMap(map[string]any{"id": "x", "class": "c", "data-z": 1})
	want := Attrs{{Key: "class", Value: "c"}, {Key: "data-z", Value: 1}, {Key: "id", Value: "x"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AttrsFromMap = %v, want %v", got, want)
	}
	if AttrsFromMap(nil) != nil {
		t.Fatal("AttrsFromMap(nil) should be nil")
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
