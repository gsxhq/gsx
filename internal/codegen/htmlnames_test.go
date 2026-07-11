package codegen

import "testing"

func TestHTMLElementNames(t *testing.T) {
	for _, present := range []string{"div", "slot", "search", "template", "selectedcontent"} {
		if !htmlElementNames[present] {
			t.Errorf("htmlElementNames[%q] = false, want true", present)
		}
	}
	for _, absent := range []string{"item", "card", "param"} {
		if htmlElementNames[absent] {
			t.Errorf("htmlElementNames[%q] = true, want false", absent)
		}
	}
}
