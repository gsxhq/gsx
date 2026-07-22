package htmldata

import "testing"

func TestGeneratedTable(t *testing.T) {
	if len(Tags) < 100 {
		t.Fatalf("Tags = %d, want the full HTML element set (>100)", len(Tags))
	}
	var div *Tag
	for i := range Tags {
		if Tags[i].Name == "div" {
			div = &Tags[i]
		}
	}
	if div == nil || div.Doc == "" {
		t.Fatal("div missing or undocumented")
	}
	var hasClass bool
	for _, a := range GlobalAttributes {
		if a.Name == "class" {
			hasClass = true
		}
	}
	if !hasClass {
		t.Fatal("global attribute class missing")
	}
	// input[type] must carry a value set with submit/button members.
	var input *Tag
	for i := range Tags {
		if Tags[i].Name == "input" {
			input = &Tags[i]
		}
	}
	if input == nil {
		t.Fatal("input missing")
	}
	var typeAttr *Attribute
	for i := range input.Attrs {
		if input.Attrs[i].Name == "type" {
			typeAttr = &input.Attrs[i]
		}
	}
	if typeAttr == nil || typeAttr.ValueSet == "" {
		t.Fatal("input[type] missing or without a value set")
	}
	found := false
	for _, v := range ValueSets[typeAttr.ValueSet] {
		if v.Name == "submit" {
			found = true
		}
	}
	if !found {
		t.Fatal("input[type] value set missing submit")
	}
	// hidden is boolean via the "v" set.
	var hidden bool
	for _, a := range GlobalAttributes {
		if a.Name == "hidden" && a.Boolean() {
			hidden = true
		}
	}
	if !hidden {
		t.Fatal("hidden not classified boolean (valueSet v)")
	}
}

func TestHTMXAttributes(t *testing.T) {
	const wantCount = 35
	if len(HTMXAttributes) != wantCount {
		t.Fatalf("HTMXAttributes = %d, want %d (transcribed from htmx.org/reference/ core + additional attribute tables)", len(HTMXAttributes), wantCount)
	}

	byName := make(map[string]Attribute, len(HTMXAttributes))
	for _, a := range HTMXAttributes {
		if a.Doc == "" {
			t.Errorf("HTMXAttributes[%q] has empty Doc", a.Name)
		}
		byName[a.Name] = a
	}

	get, ok := byName["hx-get"]
	if !ok {
		t.Fatal("hx-get missing from HTMXAttributes")
	}
	if get.Doc == "" {
		t.Fatal("hx-get has no doc")
	}

	swapOOB, ok := byName["hx-swap-oob"]
	if !ok {
		t.Fatal("hx-swap-oob missing from HTMXAttributes")
	}
	if swapOOB.Doc == "" {
		t.Fatal("hx-swap-oob has no doc")
	}
}
