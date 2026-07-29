package gsx

import "testing"

// URLAttrSink is the built-in floor: the table that decides, for an
// (element, attribute) pair, which sanitizer the value must leave through. It
// lives in the runtime so codegen and the Spread leaf cannot drift, and so a
// hand-written caller gets the safety default without opting in.
func TestURLAttrSink(t *testing.T) {
	cases := []struct {
		tag, name string
		want      URLSink
	}{
		// Image-rendering resource sinks: data:image/* is inert here.
		{"img", "src", URLSinkImage}, {"IMG", "SRC", URLSinkImage},
		{"source", "src", URLSinkImage},
		{"input", "src", URLSinkImage},
		{"video", "poster", URLSinkImage},
		{"body", "background", URLSinkImage},
		{"table", "background", URLSinkImage},
		// Strict navigational: a data: URL here is a live document or executable.
		{"a", "href", URLSinkNav},
		{"form", "action", URLSinkNav},
		{"script", "src", URLSinkNav},
		{"iframe", "src", URLSinkNav},
		{"object", "data", URLSinkNav},
		{"embed", "src", URLSinkNav},
		{"video", "src", URLSinkNav},
		{"img", "href", URLSinkNav},
		{"a", "xlink:href", URLSinkNav},
		// Compound-value URL sinks.
		{"img", "srcset", URLSinkSrcset},
		{"link", "imagesrcset", URLSinkSrcset},
		{"meta", "content", URLSinkRefresh},
		{"META", "Content", URLSinkRefresh},
		// Not URLs at all.
		{"div", "content", URLSinkNone},
		{"span", "content", URLSinkNone},
		{"", "content", URLSinkNone},
		{"div", "id", URLSinkNone},
		{"div", "class", URLSinkNone},
		{"meta", "name", URLSinkNone},
		{"a", "hx-get", URLSinkNone}, // opt-in preset, not the floor
	}
	for _, c := range cases {
		if got := URLAttrSink(c.tag, c.name); got != c.want {
			t.Errorf("URLAttrSink(%q, %q) = %v, want %v", c.tag, c.name, got, c.want)
		}
		// BuiltinURLAttr must agree with the table it gates.
		if got, want := BuiltinURLAttr(c.tag, c.name), c.want != URLSinkNone; got != want {
			t.Errorf("BuiltinURLAttr(%q, %q) = %v, want %v", c.tag, c.name, got, want)
		}
	}
}

// A project rule adds a name; it can never take one off the floor or move it to
// a laxer sink. sinkFor consults the floor first for exactly that reason.
func TestAttrSinksCannotDowngradeTheFloor(t *testing.T) {
	// Trying to move <img src> to the strict-nav set, or <a href> to the image
	// set (which would newly permit data:image), must have no effect.
	s := AttrSinks{Nav: []string{"src"}, Image: []string{"href"}}
	if got := s.sinkFor("img", "src"); got != URLSinkImage {
		t.Errorf("sinkFor(img, src) = %v, want URLSinkImage (floor wins)", got)
	}
	if got := s.sinkFor("a", "href"); got != URLSinkNav {
		t.Errorf("sinkFor(a, href) = %v, want URLSinkNav (floor wins)", got)
	}
	// A name outside the floor is what a rule can actually contribute.
	if got := s.sinkFor("div", "data-href"); got != URLSinkNone {
		t.Errorf("sinkFor(div, data-href) = %v, want URLSinkNone", got)
	}
	withRule := AttrSinks{Nav: []string{"data-href"}, Prefixes: []string{"data-url-"}}
	if got := withRule.sinkFor("div", "data-href"); got != URLSinkNav {
		t.Errorf("sinkFor(div, data-href) with rule = %v, want URLSinkNav", got)
	}
	if got := withRule.sinkFor("div", "data-url-x"); got != URLSinkNav {
		t.Errorf("sinkFor(div, data-url-x) via prefix = %v, want URLSinkNav", got)
	}
	// The zero value still gets the whole floor — the safety default is not
	// something a caller has to opt into.
	if got := (AttrSinks{}).sinkFor("a", "href"); got != URLSinkNav {
		t.Errorf("zero AttrSinks sinkFor(a, href) = %v, want URLSinkNav", got)
	}
	if got := (AttrSinks{}).sinkFor("meta", "content"); got != URLSinkRefresh {
		t.Errorf("zero AttrSinks sinkFor(meta, content) = %v, want URLSinkRefresh", got)
	}
}
