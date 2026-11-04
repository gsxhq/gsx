// Package htmltag defines HTML lexical contexts shared by parsing tools.
package htmltag

import "strings"

// SuppressBareComments reports whether a tag's body treats line-start slashes
// as content rather than GSX source comments. This is a lexical rule; all HTML
// tags preserve literal whitespace.
func SuppressBareComments(tag string) bool {
	switch strings.ToLower(tag) {
	case "pre", "textarea", "script", "style":
		return true
	}
	return false
}
