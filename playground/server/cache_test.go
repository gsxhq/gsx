package main

import "testing"

func TestCacheKeyDistinct(t *testing.T) {
	a := cacheKey(renderReq{GSX: "x", Invoke: "Y(YProps{})"})
	b := cacheKey(renderReq{GSX: "x", Invoke: "Z(ZProps{})"})
	c := cacheKey(renderReq{GSX: "x2", Invoke: "Y(YProps{})"})
	if a == b || a == c || b == c {
		t.Fatalf("keys collided: a=%s b=%s c=%s", a, b, c)
	}
	if a != cacheKey(renderReq{GSX: "x", Invoke: "Y(YProps{})"}) {
		t.Fatal("same input produced different keys")
	}
}

func TestCacheHit(t *testing.T) {
	in := renderReq{
		GSX:    "package views\n\ncomponent C(s string) {\n\t<p>{s}</p>\n}\n",
		Invoke: `C(CProps{S: "cached"})`,
	}
	first := testPool.render(in)
	if first.Cached {
		t.Fatal("first render should not be cached")
	}
	if first.HTML == "" {
		t.Fatalf("first render produced no html (err=%q diags=%+v)", first.Error, first.Diagnostics)
	}
	second := testPool.render(in)
	if !second.Cached {
		t.Fatal("second render of identical input should be a cache hit")
	}
	if second.HTML != first.HTML {
		t.Fatalf("cached html differs: %q vs %q", second.HTML, first.HTML)
	}
}
