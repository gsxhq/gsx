package htmlattr

import (
	"html/template"
	"strings"
	"testing"
)

var eventHandlerCases = struct{ js, plain []string }{
	js: []string{
		"onclick", "ONCLICK", "onLoad", "ontoggle", "oncontentvisibilityautostatechange",
		"onbeforecopy", "onscrollsnapchange", "on", "online", "onlyinteger",
		"data-onclick", "DATA-ONLOAD", "svg:onload", "xlink:onclick", ":onclick",
	},
	plain: []string{
		"x-on:click", "hx-on:click", "on:click", "@click", "xmlns:onx",
		"data-x", "href", "title", "",
	},
}

// TestIsEventHandler pins html/template's attrType JS rule.
func TestIsEventHandler(t *testing.T) {
	for _, n := range eventHandlerCases.js {
		if !IsEventHandler(n) {
			t.Errorf("IsEventHandler(%q) = false; want true", n)
		}
	}
	for _, n := range eventHandlerCases.plain {
		if IsEventHandler(n) {
			t.Errorf("IsEventHandler(%q) = true; want false", n)
		}
	}
}

// TestIsEventHandlerMatchesHTMLTemplate checks the port against html/template
// itself: a string in a JS-context attribute renders as a quoted JS string.
func TestIsEventHandlerMatchesHTMLTemplate(t *testing.T) {
	for _, n := range append(append([]string{}, eventHandlerCases.js...), eventHandlerCases.plain...) {
		if n == "" {
			continue
		}
		tmpl, err := template.New("").Parse(`<div ` + n + `="{{.}}">`)
		if err != nil {
			t.Fatalf("%q: %v", n, err)
		}
		var b strings.Builder
		if err := tmpl.Execute(&b, "v"); err != nil {
			t.Fatalf("%q: %v", n, err)
		}
		if got, want := strings.Contains(b.String(), `&#34;v&#34;`), IsEventHandler(n); got != want {
			t.Errorf("%q: html/template JS context = %v, IsEventHandler = %v (%s)", n, got, want, b.String())
		}
	}
}
