package gsx

import (
	"testing"

	"github.com/gsxhq/gsx/internal/htmlattr"
)

// A project rule adds a name; it can never take one off the floor or move it to
// a laxer sink. sinkFor consults htmlattr.Sink first for exactly that reason.
// The floor table itself is pinned by htmlattr's own TestSink.
func TestAttrSinksCannotDowngradeTheFloor(t *testing.T) {
	// Trying to move <img src> to the strict-nav set, or <a href> to the image
	// set (which would newly permit data:image), must have no effect.
	s := AttrSinks{Nav: []string{"src"}, Image: []string{"href"}}
	if got := s.sinkFor("img", "src"); got != htmlattr.SinkImage {
		t.Errorf("sinkFor(img, src) = %v, want htmlattr.SinkImage (floor wins)", got)
	}
	if got := s.sinkFor("a", "href"); got != htmlattr.SinkNav {
		t.Errorf("sinkFor(a, href) = %v, want htmlattr.SinkNav (floor wins)", got)
	}
	// A name outside the floor is what a rule can actually contribute.
	if got := s.sinkFor("div", "data-href"); got != htmlattr.SinkNone {
		t.Errorf("sinkFor(div, data-href) = %v, want htmlattr.SinkNone", got)
	}
	withRule := AttrSinks{Nav: []string{"data-href"}, Prefixes: []string{"data-url-"}}
	if got := withRule.sinkFor("div", "data-href"); got != htmlattr.SinkNav {
		t.Errorf("sinkFor(div, data-href) with rule = %v, want htmlattr.SinkNav", got)
	}
	if got := withRule.sinkFor("div", "data-url-x"); got != htmlattr.SinkNav {
		t.Errorf("sinkFor(div, data-url-x) via prefix = %v, want htmlattr.SinkNav", got)
	}
	// The zero value still gets the whole floor — the safety default is not
	// something a caller has to opt into.
	if got := (AttrSinks{}).sinkFor("a", "href"); got != htmlattr.SinkNav {
		t.Errorf("zero AttrSinks sinkFor(a, href) = %v, want htmlattr.SinkNav", got)
	}
	if got := (AttrSinks{}).sinkFor("meta", "content"); got != htmlattr.SinkRefresh {
		t.Errorf("zero AttrSinks sinkFor(meta, content) = %v, want htmlattr.SinkRefresh", got)
	}
}

// TestAttrSinksPreset checks that an enabled preset routes its attributes
// through the predicate rather than through a name list: every spelling
// htmx 4 reads for a request URL takes the strict navigational sink, other
// hx-* names stay unclassified, and the zero value enables no preset.
func TestAttrSinksPreset(t *testing.T) {
	on := AttrSinks{Presets: PresetHTMX}
	for _, key := range []string{"hx-get", "hx-action", "hx-query", "hx-post:inherited", "hx-get:append", "hx-action:inherited:append", "HX-DELETE"} {
		if got := on.sinkFor("button", key); got != htmlattr.SinkNav {
			t.Errorf("with PresetHTMX: sinkFor(button, %q) = %v, want htmlattr.SinkNav", key, got)
		}
	}
	for _, key := range []string{"hx-method", "hx-target", "hx-target:inherited", "hx-swap"} {
		if got := on.sinkFor("button", key); got != htmlattr.SinkNone {
			t.Errorf("with PresetHTMX: sinkFor(button, %q) = %v, want htmlattr.SinkNone", key, got)
		}
	}
	var off AttrSinks
	if got := off.sinkFor("button", "hx-get"); got != htmlattr.SinkNone {
		t.Errorf("zero AttrSinks: sinkFor(button, hx-get) = %v, want htmlattr.SinkNone (preset off)", got)
	}
}
