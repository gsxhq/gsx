// Package std provides the gsx public filter standard library.
//
// A filter is an exported Go function in one of two shapes:
//
//   - bare:          func(T) R
//   - parameterized: func(Args...) func(T) R   (returns a unary func)
//
// Codegen consumes these filters by contract (harvest-by-contract): it
// inspects the exported functions in this package and wires bare filters
// directly, or applies the parameterized filters' arguments to obtain the
// unary func. All implementations here are stdlib-only.
package std

import "strings"

// Upper returns s with all Unicode letters mapped to their upper case.
func Upper(s string) string {
	return strings.ToUpper(s)
}

// Lower returns s with all Unicode letters mapped to their lower case.
func Lower(s string) string {
	return strings.ToLower(s)
}

// Trim returns s with leading and trailing white space removed, as defined
// by Unicode.
func Trim(s string) string {
	return strings.TrimSpace(s)
}

// Truncate returns a filter that cuts its input to at most n runes.
//
// The cut is rune-safe: it never splits a multi-byte UTF-8 sequence. No
// ellipsis is appended in v1. If n <= 0 the result is the empty string. If
// the input is already n runes or fewer it is returned unchanged.
func Truncate(n int) func(string) string {
	return func(s string) string {
		if n <= 0 {
			return ""
		}
		runes := []rune(s)
		if len(runes) <= n {
			return s
		}
		return string(runes[:n])
	}
}

// Join returns a filter that concatenates its input elements, placing sep
// between consecutive elements.
func Join(sep string) func([]string) string {
	return func(xs []string) string {
		return strings.Join(xs, sep)
	}
}

// Default returns a filter that yields fallback when its input is the empty
// string, and otherwise returns the input unchanged.
func Default(fallback string) func(string) string {
	return func(s string) string {
		if s == "" {
			return fallback
		}
		return s
	}
}
