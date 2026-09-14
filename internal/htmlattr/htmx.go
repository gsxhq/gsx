package htmlattr

import "strings"

// htmxURLAttrs are the htmx attributes whose value is the request URL: the
// five method attributes (htmx 2 and 4) plus htmx 4's hx-query (QUERY method)
// and hx-action (the URL paired with hx-method). hx-method names a verb, not a
// URL, and is deliberately absent.
var htmxURLAttrs = map[string]bool{
	"hx-get": true, "hx-post": true, "hx-put": true, "hx-delete": true, "hx-patch": true,
	"hx-query": true, "hx-action": true,
}

// HTMXURL reports whether name is an htmx request-URL attribute in any of the
// spellings htmx 4 reads for it. htmx 4 resolves such an attribute through one
// lookup: the plain name, then name:inherited on the element or an ancestor,
// then name:append and name:inherited:append (the appended value is used
// verbatim when no ancestor inherits). Every spelling therefore ends up as the
// fetch URL and every one is a URL sink. The suffixes are accepted only in
// that order; htmx does not read name:append:inherited.
//
// This is the "htmx" url preset's predicate, shared by static classification
// (attrclass) and the spread leaf (gsx.AttrSinks) so the two cannot drift.
// htmx's config.prefix and config.metaCharacter rename these attributes at
// runtime; a site using them declares the renamed forms under url_attrs.
func HTMXURL(name string) bool {
	ln := strings.ToLower(name)
	ln = strings.TrimSuffix(ln, ":append")
	ln = strings.TrimSuffix(ln, ":inherited")
	return htmxURLAttrs[ln]
}
