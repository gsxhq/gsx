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
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
)

// Printf formats the piped value v with a fmt format spec, returning the
// resulting string. The piped value is the FIRST verb argument; any extra
// args follow, so a multi-verb spec works too:
//
//	{ price |> printf("$%.2f") }        → fmt.Sprintf("$%.2f", price)
//	{ count |> printf("%d items") }     → fmt.Sprintf("%d items", count)
//	{ x |> printf("%d/%d", total) }     → fmt.Sprintf("%d/%d", x, total)
//
// The name matches html/template's printf builtin. It exists because
// fmt.Sprintf takes the spec FIRST and so cannot be a seed-first filter
// directly; Printf flips the argument order. The result is a plain string and
// is escaped for its rendering context like any other value.
func Printf(v any, spec string, rest ...any) string {
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

// Urlquery percent-encodes s so it can be safely placed inside a URL query
// component, exactly like html/template's urlquery builtin (both delegate to
// url.QueryEscape):
//
//	<a href=f`/search?q=@{ q |> urlquery }`>
//
// gsx's URL sinks sanitize the WHOLE assembled value (scheme allow-list +
// attribute escaping) but never rewrite the bytes inside a hole, so a query
// value containing '&', '=', '#', '%' or spaces would otherwise change the
// URL's meaning. Encode the component with urlquery; the sink still sanitizes
// the assembled whole.
//
// The spelling Urlquery (not URLQuery) is load-bearing: the pipe name is the
// func name with its first rune lowered, and it must come out as html/template's
// exact builtin name, urlquery.
func Urlquery(s string) string {
	return url.QueryEscape(s)
}

// DataURL assembles a base64 data: URL from raw bytes and a MIME type:
//
//	{ imageBytes |> dataURL("image/png") }  →  data:image/png;base64,<base64(imageBytes)>
//
// It is a plain assembly helper and grants NO privilege by itself: the value it
// returns is re-validated by the sink's URL sanitizer, so a non-image MIME (or an
// image data URL in a navigational sink) is still blocked. Use it in an image
// resource sink (<img src>, <video poster>, …).
func DataURL(subject []byte, mime string) string {
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(subject)
}
