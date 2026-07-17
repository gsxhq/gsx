package gsx

import "testing"

func TestIsBooleanAttr(t *testing.T) {
	// A representative slice of the WHATWG "Boolean attribute" set. Presence
	// alone means true; the value is ignored, so only absence expresses false.
	present := []string{
		"checked", "disabled", "required", "readonly", "selected", "multiple",
		"autofocus", "async", "defer", "open", "hidden", "download",
		"controls", "loop", "muted", "reversed", "ismap", "novalidate",
	}
	for _, n := range present {
		if !IsBooleanAttr(n) {
			t.Errorf("IsBooleanAttr(%q) = false; want true", n)
		}
	}

	// ASCII-case-insensitive: HTML attribute names fold.
	for _, n := range []string{"Checked", "DISABLED", "ReadOnly"} {
		if !IsBooleanAttr(n) {
			t.Errorf("IsBooleanAttr(%q) = false; want true (names fold)", n)
		}
	}

	// The guard set: these LOOK boolean but are enumerated — "false" is a valid
	// keyword, so they must stringify, not toggle. Listing any of them would
	// re-introduce the inverted render this whole change removes.
	notPresence := []string{
		"contenteditable", "draggable", "spellcheck",
		// value attributes and arbitrary author names
		"class", "style", "id", "href", "value", "aria-hidden", "aria-expanded",
		"data-open", "translate", "autocapitalize", "role", "title",
	}
	for _, n := range notPresence {
		if IsBooleanAttr(n) {
			t.Errorf("IsBooleanAttr(%q) = true; want false", n)
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
