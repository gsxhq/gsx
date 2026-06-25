package gsx

import "strings"

import "testing"

func styleMerged(root, bag string) string {
	var b strings.Builder
	W(&b).StyleMerged(root, bag)
	return b.String()
}

func TestStyleMerged(t *testing.T) {
	for _, tt := range []struct{ root, bag, want string }{
		{"color: red; margin: 0", "color: blue", ` style="margin: 0; color: blue"`}, // dedupe, caller last
		{"a: 1; a: 2", "", ` style="a: 2"`},                                         // within-string last-wins
		{"color: red", "", ` style="color: red"`},
		{"", "", ""}, // empty -> no attr
		{"", "color: blue", ` style="color: blue"`},
		// robust splitter: ; and : inside url()/quotes are NOT boundaries
		{"background: url(data:image/png;base64,AA;BB)", "", ` style="background: url(data:image/png;base64,AA;BB)"`},
		{`content: "a; b"; color: red`, "color: blue", ` style="content: &#34;a; b&#34;; color: blue"`},
	} {
		if got := styleMerged(tt.root, tt.bag); got != tt.want {
			t.Errorf("StyleMerged(%q,%q) = %q, want %q", tt.root, tt.bag, got, tt.want)
		}
	}
}

func FuzzStyleMerged(f *testing.F) {
	f.Add("color:red; margin:0", "color:blue")
	f.Add("background:url(data:x;base64,AA;BB)", "")
	f.Add(`content:"a;b"`, "color:red")
	f.Fuzz(func(t *testing.T, root, bag string) {
		once := styleMerged(root, bag)
		// idempotence: re-merging the already-merged value (sans the ` style="`/`"` wrapper) is stable.
		inner := strings.TrimSuffix(strings.TrimPrefix(once, ` style="`), `"`)
		// Skip inputs containing characters that AttrValue HTML-escapes to entities
		// (all entities end with ';'). Re-feeding the HTML-escaped output back as CSS
		// is not a valid round-trip: the entity suffix ';' would be seen as a
		// declaration separator by the splitter, which is correct CSS parsing but not
		// a round-trip identity. This is a test-design boundary, not a splitter bug.
		if strings.ContainsAny(root+bag, "&<>\"'\x00") {
			return
		}
		twice := styleMerged(inner, "")
		// twice's inner must equal once's inner (no further change); compare unwrapped.
		t2 := strings.TrimSuffix(strings.TrimPrefix(twice, ` style="`), `"`)
		if inner != "" && t2 != inner {
			t.Fatalf("not idempotent: %q -> %q -> %q", root+"|"+bag, inner, t2)
		}
	})
}
