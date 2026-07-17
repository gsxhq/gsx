package main

import "testing"

func TestCacheKeyDistinct(t *testing.T) {
	a := cacheKey(renderReq{GSX: "x", Invoke: "Y()"})
	b := cacheKey(renderReq{GSX: "x", Invoke: "Z()"})
	c := cacheKey(renderReq{GSX: "x2", Invoke: "Y()"})
	if a == b || a == c || b == c {
		t.Fatalf("keys collided: a=%s b=%s c=%s", a, b, c)
	}
	if a != cacheKey(renderReq{GSX: "x", Invoke: "Y()"}) {
		t.Fatal("same input produced different keys")
	}
}

func TestCacheable(t *testing.T) {
	cases := []struct {
		name string
		r    renderResp
		want bool
	}{
		{"success", renderResp{HTML: "<p>x</p>"}, true},
		{"diagnostic", renderResp{Diagnostics: []diagnostic{{Severity: "error", Message: "boom"}}}, true},
		{"operational error", renderResp{Error: "render: timeout"}, false},
		{"empty timeout (poison case)", renderResp{}, false},
		{"error wins over html", renderResp{HTML: "<p>x</p>", Error: "oops"}, false},
	}
	for _, c := range cases {
		if got := cacheable(c.r); got != c.want {
			t.Errorf("%s: cacheable=%v want %v", c.name, got, c.want)
		}
	}
}

func TestCacheHit(t *testing.T) {
	in := renderReq{
		GSX:    "package views\n\ncomponent C(s string) {\n\t<p>{s}</p>\n}\n",
		Invoke: `C("cached")`,
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
