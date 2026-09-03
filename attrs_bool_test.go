package gsx

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// stringerFlag is a bool with a String method: anyRenderVal checks Stringer
// FIRST, so it renders as the string "on"/"off" and never toggles.
type stringerFlag bool

func (f stringerFlag) String() string {
	if f {
		return "on"
	}
	return "off"
}

// boolValueTable is the value → state contract shared by TestAttrsBool and the
// Spread agreement test below. Every value here is one Spread can render.
// spreadFlag (toggle_test.go) is the package's named-bool fixture: Spread
// classifies it by underlying kind, so Bool must too.
var boolValueTable = []struct {
	name string
	val  any
	want bool
}{
	{"nil", nil, false},
	{"true", true, true},
	{"false", false, false},
	{"Toggle(true)", Toggle(true), true},
	{"Toggle(false)", Toggle(false), false},
	{"spreadFlag(true)", spreadFlag(true), true},
	{"spreadFlag(false)", spreadFlag(false), false},
	{"stringerFlag(false)", stringerFlag(false), true},
	{"empty string", "", true},
	{"string false", "false", true},
	{"string disabled", "disabled", true},
	{"RawJS", RawJS("x"), true},
	{"int 0", 0, true},
	{"[]byte empty", []byte{}, true},
}

func TestAttrsBool(t *testing.T) {
	for _, tc := range boolValueTable {
		a := Attrs{{Key: "disabled", Value: tc.val}}
		if got := a.Bool("disabled"); got != tc.want {
			t.Errorf("Bool(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
	if Attrs(nil).Bool("disabled") {
		t.Error("Bool on nil bag should be false")
	}
	if (Attrs{{Key: "open", Value: true}}).Bool("disabled") {
		t.Error("Bool on absent key should be false")
	}

	// Last valid pair wins, in both directions.
	if (Attrs{{Key: "disabled", Value: true}, {Key: "disabled", Value: false}}).Bool("disabled") {
		t.Error("true then false should resolve false")
	}
	if !(Attrs{{Key: "disabled", Value: false}, {Key: "disabled", Value: true}}).Bool("disabled") {
		t.Error("false then true should resolve true")
	}
	if (Attrs{{Key: "disabled", Value: Toggle(true)}, {Key: "disabled", Value: Toggle(false)}}).Bool("disabled") {
		t.Error("Toggle(true) then Toggle(false) should resolve false")
	}
	if (Attrs{{Key: "disabled", Value: "yes"}, {Key: "disabled", Value: nil}}).Bool("disabled") {
		t.Error("a trailing nil should resolve false")
	}

	// State, not output: a false bool on a value-vocabulary name renders
	// aria-pressed="false" yet is not on.
	if (Attrs{{Key: "aria-pressed", Value: false}}).Bool("aria-pressed") {
		t.Error("aria-pressed={false} should be false")
	}
	if !(Attrs{{Key: "aria-pressed", Value: "false"}}).Bool("aria-pressed") {
		t.Error(`aria-pressed="false" (a string) should be true`)
	}

	// Exact-match like Get: a case variant is a different key.
	if (Attrs{{Key: "Disabled", Value: true}}).Bool("disabled") {
		t.Error("Bool must not fold case")
	}

	// A structurally invalid name never renders, so it is never on.
	if (Attrs{{Key: "dis abled", Value: true}}).Bool("dis abled") {
		t.Error("invalid attribute name should be false")
	}

	// A value Spread cannot render (it fails the render) is never on either.
	if (Attrs{{Key: "disabled", Value: struct{}{}}}).Bool("disabled") {
		t.Error("unsupported value type should be false")
	}
	if (Attrs{{Key: "disabled", Value: (*int)(nil)}}).Bool("disabled") {
		t.Error("typed nil pointer should be false")
	}

	// class/style read their raw last pair, not Get's string aggregate: Spread
	// decides a Toggle's presence before aggregating.
	if (Attrs{{Key: "class", Value: "a"}, {Key: "class", Value: Toggle(false)}}).Bool("class") {
		t.Error("class ending in Toggle(false) should be false")
	}
	if !(Attrs{{Key: "class", Value: "a"}, {Key: "class", Value: "b"}}).Bool("class") {
		t.Error("class of strings should be true")
	}
}

// TestAttrsBoolAgreesWithSpread pins Bool to the renderer: on every name where a
// bool renders bare, Bool(key) is true exactly when Spread writes the attribute.
func TestAttrsBoolAgreesWithSpread(t *testing.T) {
	names := []string{"disabled", "hidden", "data-open", "x-cloak", "checked", "class", "style"}
	bags := map[string]Attrs{}
	for _, tc := range boolValueTable {
		bags[tc.name] = Attrs{{Key: "K", Value: tc.val}}
	}
	bags["true then false"] = Attrs{{Key: "K", Value: true}, {Key: "K", Value: false}}
	bags["false then true"] = Attrs{{Key: "K", Value: false}, {Key: "K", Value: true}}
	bags["string then Toggle(false)"] = Attrs{{Key: "K", Value: "x"}, {Key: "K", Value: Toggle(false)}}
	bags["nil then string"] = Attrs{{Key: "K", Value: nil}, {Key: "K", Value: ""}}
	bags["absent"] = Attrs{{Key: "other", Value: true}}

	for _, name := range names {
		for label, proto := range bags {
			bag := make(Attrs, len(proto))
			for i, kv := range proto {
				if kv.Key == "K" {
					kv.Key = name
				}
				bag[i] = kv
			}
			if (name == "class" || name == "style") && aggregateStringifies(bag, name) {
				// class/style are string aggregates: Spread stringifies a plain
				// bool or a nil INTO the aggregate (class="a false") before its
				// presence rule runs, so on those two keys only Toggle carries
				// presence. Bool reads the pair itself.
				continue
			}
			var buf bytes.Buffer
			w := W(&buf)
			w.Spread(context.Background(), "div", bag, AttrSinks{}, nil)
			if err := w.Err(); err != nil {
				t.Fatalf("%s %s: Spread error: %v", name, label, err)
			}
			// No value in the table contains whitespace, so Fields yields one
			// token per attribute: a bare `name` or `name="…"`. An exact token
			// match keeps a longer name sharing the prefix (data-open-x) from
			// counting as rendered.
			out := buf.String()
			rendered := false
			for tok := range strings.FieldsSeq(out) {
				if tok == name || strings.HasPrefix(tok, name+`="`) {
					rendered = true
				}
			}
			if got := bag.Bool(name); got != rendered {
				t.Errorf("%s %s: Bool = %v but Spread wrote %q", name, label, got, out)
			}
		}
	}
}

// aggregateStringifies reports whether the last pair for key is a value the
// class/style aggregate turns into text before Spread's presence rule can see
// it: nil, or a bool-kinded value that is not a Toggle.
func aggregateStringifies(bag Attrs, key string) bool {
	for i := len(bag) - 1; i >= 0; i-- {
		if bag[i].Key != key {
			continue
		}
		if _, isToggle := bag[i].Value.(Toggle); isToggle {
			return false
		}
		if bag[i].Value == nil {
			return true
		}
		_, k, ok := anyRenderVal(bag[i].Value)
		return ok && k == kindBool
	}
	return false
}
