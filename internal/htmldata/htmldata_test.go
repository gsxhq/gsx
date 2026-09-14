package htmldata

import (
	"strings"
	"testing"
)

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
	// Union of the htmx 2 reference (35 attributes) and the ten attributes
	// htmx 4 added, so completions serve either major version.
	const wantCount = 45
	if len(HTMXAttributes) != wantCount {
		t.Fatalf("HTMXAttributes = %d, want %d (union of htmx.org/reference/ and four.htmx.org/reference/ attribute tables)", len(HTMXAttributes), wantCount)
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

	// htmx 4 additions are present and marked as htmx 4 only, linking to the
	// four.htmx.org reference.
	for _, n := range []string{
		"hx-action", "hx-method", "hx-config", "hx-ignore", "hx-status", "hx-query",
		"hx-morph-skip", "hx-morph-skip-children", "hx-preload", "hx-pending",
	} {
		a, ok := byName[n]
		if !ok {
			t.Errorf("%s missing from HTMXAttributes (htmx 4 attribute)", n)
			continue
		}
		if !strings.HasPrefix(a.Doc, "htmx 4 only.") {
			t.Errorf("%s Doc = %q, want an \"htmx 4 only.\" lead", n, a.Doc)
		}
		if !strings.Contains(a.Doc, "https://four.htmx.org/attributes/"+n+"/") {
			t.Errorf("%s Doc = %q, want a four.htmx.org reference link", n, a.Doc)
		}
	}

	// Attributes htmx 4 removed stay offered (htmx 2 sites still use them) but
	// say so up front, naming the htmx 4 replacement where one exists.
	removed := map[string]string{
		"hx-disinherit":   "",
		"hx-inherit":      "",
		"hx-vars":         "hx-vals",
		"hx-params":       "",
		"hx-request":      "hx-config",
		"hx-history":      "",
		"hx-ext":          "",
		"hx-prompt":       "",
		"hx-disabled-elt": "hx-disable",
	}
	for n, repl := range removed {
		a, ok := byName[n]
		if !ok {
			t.Errorf("%s missing from HTMXAttributes (htmx 2 attribute)", n)
			continue
		}
		if !strings.HasPrefix(a.Doc, "htmx 2 only. Removed in htmx 4") {
			t.Errorf("%s Doc = %q, want an \"htmx 2 only. Removed in htmx 4\" lead", n, a.Doc)
		}
		if repl != "" && !strings.Contains(a.Doc, "`"+repl+"`") {
			t.Errorf("%s Doc = %q, want it to name the htmx 4 replacement %s", n, a.Doc, repl)
		}
	}

	// hx-disable changed meaning between versions; its doc states both.
	disable := byName["hx-disable"]
	for _, want := range []string{"htmx 2:", "htmx 4:", "`hx-ignore`", "`hx-disabled-elt`"} {
		if !strings.Contains(disable.Doc, want) {
			t.Errorf("hx-disable Doc = %q, want it to contain %q", disable.Doc, want)
		}
	}
}
