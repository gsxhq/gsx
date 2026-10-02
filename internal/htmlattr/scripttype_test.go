package htmlattr

import "testing"

func TestScriptTypeIsJS(t *testing.T) {
	js := []string{
		"", "  ", "module", "MODULE",
		"text/javascript", "Text/JavaScript", " text/javascript ",
		"text/javascript; charset=utf-8", "application/javascript;x=y",
		"application/ecmascript", "application/x-ecmascript", "application/x-javascript",
		"text/ecmascript", "text/javascript1.0", "text/javascript1.1", "text/javascript1.2",
		"text/javascript1.3", "text/javascript1.4", "text/javascript1.5",
		"text/jscript", "text/livescript", "text/x-ecmascript", "text/x-javascript",
	}
	for _, typ := range js {
		if !ScriptTypeIsJS(typ) {
			t.Errorf("ScriptTypeIsJS(%q) = false; want true", typ)
		}
	}
	data := []string{
		"application/json", "application/ld+json", "importmap", "speculationrules",
		"text/template", "text/x-template", "text/plain", "text/javascript1.6",
		"javascript", "text/babel",
	}
	for _, typ := range data {
		if ScriptTypeIsJS(typ) {
			t.Errorf("ScriptTypeIsJS(%q) = true; want false", typ)
		}
	}
}
