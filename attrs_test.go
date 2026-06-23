package gsx

import (
	"context"
	"strings"
	"testing"
)

func TestAttrsHasGet(t *testing.T) {
	a := Attrs{"id": "x", "disabled": true}
	if !a.Has("id") || a.Has("nope") {
		t.Fatal("Has wrong")
	}
	if v, ok := a.Get("disabled"); !ok || v != true {
		t.Fatalf("Get = %v,%v", v, ok)
	}
}

func TestAttrsWithoutAndTakeAreImmutable(t *testing.T) {
	a := Attrs{"a": 1, "b": 2, "c": 3}
	w := a.Without("b")
	if w.Has("b") || !w.Has("a") || !a.Has("b") { // original keeps b
		t.Fatalf("Without mutated or wrong: w=%v a=%v", w, a)
	}
	v, rest := a.Take("a")
	if v != 1 || rest.Has("a") || !a.Has("a") {
		t.Fatalf("Take wrong: v=%v rest=%v a=%v", v, rest, a)
	}
}

func TestAttrsMergeConcatenatesClass(t *testing.T) {
	a := Attrs{"class": "btn", "id": "x"}
	b := Attrs{"class": "active", "id": "y"}
	m := a.Merge(b)
	if m["class"] != "btn active" { // concatenated
		t.Fatalf("class = %v", m["class"])
	}
	if m["id"] != "y" { // other wins
		t.Fatalf("id = %v", m["id"])
	}
}

func TestAttrsClassExtract(t *testing.T) {
	if got := (Attrs{"class": "btn btn px-4"}).Class(); got != "btn px-4" {
		t.Fatalf("got %q", got)
	}
	if got := (Attrs{}).Class(); got != "" {
		t.Fatalf("empty class = %q", got)
	}
}

func TestSpreadDeterministicAndTyped(t *testing.T) {
	var b strings.Builder
	W(&b).Spread(context.Background(), Attrs{
		"data-z":  "9",
		"id":      `a"b`,
		"checked": true,
		"hidden":  false, // omitted
		"count":   3,     // fmt-formatted
	})
	// keys sorted: checked, count, data-z, hidden(omitted), id
	want := ` checked count="3" data-z="9" id="a&#34;b"`
	if b.String() != want {
		t.Fatalf("got  %q\nwant %q", b.String(), want)
	}
}

func TestSpreadEmpty(t *testing.T) {
	var b strings.Builder
	W(&b).Spread(context.Background(), nil)
	if b.String() != "" {
		t.Fatalf("got %q", b.String())
	}
}

func TestAttrsMergeCallerWins(t *testing.T) {
	root := Attrs{"id": "root", "class": "base", "style": "color:red", "role": "x"}
	bag := Attrs{"id": "caller", "class": "extra", "style": "margin:0"}
	got := root.Merge(bag)
	if got["id"] != "caller" {
		t.Errorf("id = %v, want caller (other wins)", got["id"])
	}
	if got["role"] != "x" {
		t.Errorf("role dropped")
	}
	if got["class"] != "base extra" {
		t.Errorf("class = %v, want \"base extra\"", got["class"])
	}
	if got["style"] != "color:red; margin:0" {
		t.Errorf("style = %v, want \"color:red; margin:0\"", got["style"])
	}
}

func TestAttrsStyle(t *testing.T) {
	if got := (Attrs{"style": "color: red"}).Style(); got != "color: red" {
		t.Errorf("Style() = %q, want \"color: red\"", got)
	}
	if got := (Attrs{}).Style(); got != "" {
		t.Errorf("empty Style() = %q, want \"\"", got)
	}
}

func TestSpreadSkipsUnsafeKeysKeepsSpecialNames(t *testing.T) {
	var b strings.Builder
	W(&b).Spread(context.Background(), Attrs{
		// unsafe keys — must be dropped (tag/name breakout otherwise):
		`"><script>`:      "x",
		"x onmouseover=y": "z",
		"a/b":             "q",
		"":                "e",
		// legitimate special-char attribute names — must be kept:
		"hx-on::click": "go()",
		":class":       "c",
		"@click.away":  "d",
		"data-id":      "1",
	})
	// only the legitimate names survive, sorted: :class, @click.away, data-id, hx-on::click
	want := ` :class="c" @click.away="d" data-id="1" hx-on::click="go()"`
	if b.String() != want {
		t.Fatalf("got  %q\nwant %q", b.String(), want)
	}
}
