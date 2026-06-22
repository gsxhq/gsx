// Package attrjs identifies HTML attributes whose value is JavaScript, so both
// the parser (which splits @{ } holes in their quoted values) and codegen (which
// escapes them as JS) agree on one set.
package attrjs

import "strings"

// IsJSAttr reports whether an attribute name carries a JavaScript value:
// inline event handlers (onclick…), Alpine directives, and HTMX hx-on.
func IsJSAttr(name string) bool {
	n := strings.ToLower(name)
	switch {
	case strings.HasPrefix(n, "@"): // Alpine @click shorthand for x-on:
		return true
	case strings.HasPrefix(n, "hx-on"): // HTMX hx-on:*
		return true
	case strings.HasPrefix(n, "on") && len(n) > 2 && n[2] >= 'a' && n[2] <= 'z': // onclick…
		return true
	case n == "x-data" || n == "x-init" || n == "x-show" || n == "x-if" || n == "x-effect":
		return true
	case strings.HasPrefix(n, "x-on:"): // Alpine x-on:click
		return true
	case strings.HasPrefix(n, ":") && n != ":": // Alpine :class / x-bind shorthand
		return true
	default:
		return false
	}
}
