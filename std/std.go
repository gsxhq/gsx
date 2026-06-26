// Package std provides the gsx public filter standard library.
//
// A filter is an exported Go function in the seed-first shape:
//
//	func([ctx context.Context,] subject T, args...) R          // or (R, error)
//
// Codegen consumes these filters by contract (harvest-by-contract): it inspects
// the exported functions in this package and lowers `subject |> name(args…)` to
// `name(subject, args…)`, injecting the ambient render ctx as the first argument
// when the filter's first parameter is context.Context. All implementations
// here are stdlib-only and ctx-less.
package std

import (
	"fmt"
	"strings"
)

// Format formats the piped value v with a fmt format spec, returning the
// resulting string. The piped value is the FIRST verb argument; any extra
// args follow, so a multi-verb spec works too:
//
//	{ price |> format("$%.2f") }        → fmt.Sprintf("$%.2f", price)
//	{ count |> format("%d items") }     → fmt.Sprintf("%d items", count)
//	{ x |> format("%d/%d", total) }     → fmt.Sprintf("%d/%d", x, total)
//
// It exists because fmt.Sprintf takes the spec FIRST and so cannot be a
// seed-first filter directly; Format flips the argument order. The result is a
// plain string and is escaped for its rendering context like any other value.
func Format(v any, spec string, rest ...any) string {
	return fmt.Sprintf(spec, append([]any{v}, rest...)...)
}

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

// Truncate cuts s to at most n runes.
//
// The cut is rune-safe: it never splits a multi-byte UTF-8 sequence. No
// ellipsis is appended in v1. If n <= 0 the result is the empty string. If s is
// already n runes or fewer it is returned unchanged.
func Truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

// Join concatenates the elements of s, placing sep between consecutive elements.
func Join(s []string, sep string) string {
	return strings.Join(s, sep)
}

// Default yields fallback when s is the empty string, and otherwise returns s
// unchanged.
func Default(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
