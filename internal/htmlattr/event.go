package htmlattr

import "strings"

// IsEventHandler reports whether a value of attribute name is JavaScript the
// browser may run, so a dynamic value must leave through the JS value sink,
// never plain attribute escaping. It is html/template's rule (attrType), ported
// faithfully: after ASCII-lowercasing and stripping a "data-" prefix or an XML
// namespace prefix ("svg:onload" → "onload"; an xmlns: name is a URL), any name
// starting with "on" is an event handler. A prefix rule rather than a table:
// browsers keep adding handlers, and a name missing from a table would run an
// attribute-escaped string as code.
func IsEventHandler(name string) bool {
	name = strings.ToLower(name)
	if rest, ok := strings.CutPrefix(name, "data-"); ok {
		name = rest
	} else if prefix, short, ok := strings.Cut(name, ":"); ok {
		if prefix == "xmlns" {
			return false
		}
		name = short
	}
	return strings.HasPrefix(name, "on")
}
