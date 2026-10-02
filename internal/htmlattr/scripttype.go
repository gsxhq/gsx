package htmlattr

import "strings"

// ScriptTypeIsJS reports whether a <script> whose type attribute is typ runs
// as JavaScript: an empty type, "module", or a WHATWG JavaScript MIME type
// essence, legacy spellings included. Matching follows html/template's
// isJSType: case-insensitive, surrounding whitespace and any ;parameters
// ignored. Any other type marks a data block (application/json, importmap,
// text/template, …), which the browser never executes.
//
// It is the one predicate for every <script> classification — hole contexts
// (internal/jsx), minification (internal/jsmin), formatting (internal/printer)
// and generated-code rebasing (internal/codegen) — so they cannot disagree.
func ScriptTypeIsJS(typ string) bool {
	typ, _, _ = strings.Cut(typ, ";")
	switch strings.ToLower(strings.TrimSpace(typ)) {
	case "",
		"module",
		"application/ecmascript",
		"application/javascript",
		"application/x-ecmascript",
		"application/x-javascript",
		"text/ecmascript",
		"text/javascript",
		"text/javascript1.0",
		"text/javascript1.1",
		"text/javascript1.2",
		"text/javascript1.3",
		"text/javascript1.4",
		"text/javascript1.5",
		"text/jscript",
		"text/livescript",
		"text/x-ecmascript",
		"text/x-javascript":
		return true
	}
	return false
}
