package codegen

import "testing"

func TestHTMLElementNames(t *testing.T) {
	for _, present := range []string{"div", "slot", "search", "template"} {
		if !htmlElementNames[present] {
			t.Errorf("htmlElementNames[%q] = false, want true", present)
		}
	}
	for _, absent := range []string{"item", "card"} {
		if htmlElementNames[absent] {
			t.Errorf("htmlElementNames[%q] = true, want false", absent)
		}
	}
}
