// Package htmldata is the HTML tag/attribute/value completion dataset,
// generated from the vendored @vscode/web-custom-data browsers.html-data.json
// (MIT, LICENSE.vendored). Regenerate after replacing the JSON:
//
// HTMXAttributes is transcribed from the htmx documented attribute reference
// at https://htmx.org/reference/ (the "Core Attribute Reference" and
// "Additional Attribute Reference" tables), stored in htmx-data.json using
// the same custom-data schema.
//
//go:generate go run ./gen
package htmldata

// Value is one member of a ValueSets entry, e.g. an <input type> option.
type Value struct {
	Name string
	Doc  string
}

// Attribute is an HTML (or htmx) attribute: a tag attribute, a global
// attribute, or an HTMXAttributes entry.
type Attribute struct {
	Name string
	// Doc is markdown; it includes an MDN (or htmx) reference link when
	// the source data carries one.
	Doc string
	// ValueSet is a key into ValueSets ("" means freeform; "v" is the
	// vscode convention for a boolean-ish, presence-only attribute).
	ValueSet string
}

// Boolean reports whether a follows the vscode "v" valueSet convention:
// a presence-only attribute (e.g. hidden, disabled) rather than one that
// takes a value.
func (a Attribute) Boolean() bool { return a.ValueSet == "v" }

// Tag is one HTML element and its allowed attributes.
type Tag struct {
	Name  string
	Doc   string
	Attrs []Attribute
}
