package htmlattr

import "testing"

func TestIsBoolean(t *testing.T) {
	// A representative slice of the WHATWG "Boolean attribute" set. Presence
	// alone means true; the value is ignored, so only absence expresses false.
	present := []string{
		"checked", "disabled", "required", "readonly", "selected", "multiple",
		"autofocus", "async", "defer", "open", "hidden", "download",
		"controls", "loop", "muted", "reversed", "ismap", "novalidate",
	}
	for _, n := range present {
		if !IsBoolean(n) {
			t.Errorf("IsBoolean(%q) = false; want true", n)
		}
	}

	// ASCII-case-insensitive: HTML attribute names fold.
	for _, n := range []string{"Checked", "DISABLED", "ReadOnly"} {
		if !IsBoolean(n) {
			t.Errorf("IsBoolean(%q) = false; want true (names fold)", n)
		}
	}

	// The guard set: these LOOK boolean but are enumerated — "false" is a valid
	// keyword — plus ordinary value attributes. None is an HTML boolean
	// attribute. (Whether a bool RENDERS bare on them is RendersBare's
	// question, not this one: data-open is not an HTML boolean attribute yet
	// still toggles, because HTML defines no data-open at all.)
	notBoolean := []string{
		"contenteditable", "draggable", "spellcheck",
		// value attributes and arbitrary author names
		"class", "style", "id", "href", "value", "aria-hidden", "aria-expanded",
		"data-open", "translate", "autocapitalize", "role", "title",
	}
	for _, n := range notBoolean {
		if IsBoolean(n) {
			t.Errorf("IsBoolean(%q) = true; want false", n)
		}
	}
}

func TestRendersBare(t *testing.T) {
	// Everything renders presence — HTML boolean attributes, userland names,
	// library vocabularies, and ordinary HTML attributes a bool has no business
	// being on. Only the true/false vocabulary is special, so the rule stays one
	// sentence long.
	bare := []string{
		// HTML boolean attributes
		"required", "checked", "hidden", "download", "open", "novalidate",
		// userland: data-*, custom elements, an author's own name
		"data-open", "data-slot", "data-gsxui-dialog-close", "data-state",
		"active", "full-width", "my-custom-thing",
		// library vocabularies: written as static strings or js`` literals in
		// practice (hx-boost="false", x-show=js`open`), so a Go bool on one can
		// only have meant presence
		"x-cloak", "x-ignore", "x-show", "x-data", "hx-disable", "hx-preserve",
		"hx-boost", "hx-swap", "@click", ":class", "hx-on:click",
		// HTML names with a value vocabulary that is NOT true/false
		"title", "role", "id", "class", "value", "href", "dir", "translate",
		"part", "exportparts", "slot", "is", "popover", "itemprop", "onclick",
	}
	for _, n := range bare {
		if !RendersBare(n) {
			t.Errorf("RendersBare(%q) = false; want true", n)
		}
	}

	// The exception, and its whole extent: HTML's own values for these names ARE
	// "true"/"false". A screen reader announces aria-expanded="false" as
	// collapsed and says nothing when the attribute is absent; contenteditable
	// and spellcheck inherit, so only the literal "false" opts a subtree out;
	// draggable is on by default for images and links.
	stringified := []string{
		"aria-expanded", "aria-hidden", "aria-checked", "aria-pressed",
		"aria-selected", "aria-disabled", "aria-required", "aria-busy",
		"aria-current", "aria-invalid", "aria-haspopup",
		"contenteditable", "writingsuggestions", "draggable", "spellcheck",
	}
	for _, n := range stringified {
		if RendersBare(n) {
			t.Errorf("RendersBare(%q) = true; want false", n)
		}
	}

	// Names fold, in both directions.
	for _, n := range []string{"Data-Slot", "REQUIRED", "Hx-Boost"} {
		if !RendersBare(n) {
			t.Errorf("RendersBare(%q) = false; want true (names fold)", n)
		}
	}
	for _, n := range []string{"ARIA-EXPANDED", "ContentEditable", "Draggable"} {
		if RendersBare(n) {
			t.Errorf("RendersBare(%q) = true; want false (names fold)", n)
		}
	}
}

// The three lists must stay disjoint in the right directions: every curated
// extra must survive, and no guarded name may ever appear. This is the test that
// stops a future refresh of booleanAttrs (or a well-meaning addition) from
// dropping hidden/download or re-adding contenteditable.
func TestBooleanAttrListInvariants(t *testing.T) {
	for _, n := range presenceOnlyExtras {
		if !booleanAttrs[n] {
			t.Errorf("extra %q not in the effective list", n)
		}
	}
	for _, n := range []string{"contenteditable", "draggable", "spellcheck"} {
		if booleanAttrs[n] {
			t.Errorf("%q is enumerated (valid \"false\") and must NOT be a boolean attribute", n)
		}
	}
}
