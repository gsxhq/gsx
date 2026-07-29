package htmlattr

import "testing"

func TestSink(t *testing.T) {
	cases := []struct {
		tag, name string
		want      URLSink
	}{
		// Image-rendering resource sinks: data:image/* is inert here.
		{"img", "src", SinkImage}, {"IMG", "SRC", SinkImage},
		{"source", "src", SinkImage},
		{"input", "src", SinkImage},
		{"video", "poster", SinkImage},
		{"body", "background", SinkImage},
		{"table", "background", SinkImage},
		// Strict navigational: a data: URL here is a live document or executable.
		{"a", "href", SinkNav},
		{"form", "action", SinkNav},
		{"script", "src", SinkNav},
		{"iframe", "src", SinkNav},
		{"object", "data", SinkNav},
		{"embed", "src", SinkNav},
		{"video", "src", SinkNav},
		{"img", "href", SinkNav},
		{"a", "xlink:href", SinkNav},
		// Compound-value URL sinks.
		{"img", "srcset", SinkSrcset},
		{"link", "imagesrcset", SinkSrcset},
		{"meta", "content", SinkRefresh},
		{"META", "Content", SinkRefresh},
		// Not URLs at all.
		{"div", "content", SinkNone},
		{"span", "content", SinkNone},
		{"", "content", SinkNone},
		{"div", "id", SinkNone},
		{"div", "class", SinkNone},
		{"meta", "name", SinkNone},
		{"a", "hx-get", SinkNone}, // opt-in preset, not the floor
	}
	for _, c := range cases {
		if got := Sink(c.tag, c.name); got != c.want {
			t.Errorf("Sink(%q, %q) = %v, want %v", c.tag, c.name, got, c.want)
		}
		// BuiltinURLAttr must agree with the table it gates.
		if got, want := BuiltinURL(c.tag, c.name), c.want != SinkNone; got != want {
			t.Errorf("BuiltinURL(%q, %q) = %v, want %v", c.tag, c.name, got, want)
		}
	}
}
