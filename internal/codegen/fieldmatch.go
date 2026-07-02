package codegen

import (
	"fmt"
	"go/token"
	"slices"
	"strings"
	"unicode"
)

// FieldMatcher maps an HTML attribute name to a Go struct field name, given the
// set of exported field names on the target props struct. It returns the matched
// field name and true when a match is found, or ("", false) to signal that the
// attribute falls through to the Attrs bag.
//
// The default matcher (defaultFieldMatcher) handles:
//   - plain identifiers: capitalize first letter ("variant" → "Variant")
//   - kebab names: split on "-", Title-case each segment, join
//     ("full-width" → "FullWidth", "aria-label" → "AriaLabel")
//
// A custom matcher may be installed via gen.WithFieldMatcher.
type FieldMatcher func(attr string, fields []string) (field string, ok bool)

// defaultFieldMatcher is the built-in attr→field matcher. It handles two cases:
//
//  1. Plain identifiers (no "-"): capitalize the first letter.
//     "variant" → "Variant", "fullWidth" → "FullWidth" (already capitalized first letter).
//
//  2. Kebab names: split on "-", Title-case each segment, join them.
//     "full-width" → "FullWidth", "aria-label" → "AriaLabel".
//
// In both cases, the candidate field is returned only when it is present in the
// fields slice; otherwise ("", false) is returned so the attr falls through to
// Attrs.
func defaultFieldMatcher(attr string, fields []string) (string, bool) {
	candidate := attrToFieldCandidate(attr)
	if candidate == "" {
		return "", false
	}
	if slices.Contains(fields, candidate) {
		return candidate, true
	}
	return "", false
}

// attrToFieldCandidate converts an attr name to a candidate Go field name.
// For a plain identifier (no "-"), capitalize the first letter.
// For a kebab name (contains "-"), split on "-", Title-case each segment, join.
// Returns "" if any segment is empty (malformed kebab like "-foo" or "foo-").
func attrToFieldCandidate(attr string) string {
	if !strings.Contains(attr, "-") {
		// Plain identifier: capitalize first letter only.
		return capitalizeFirst(attr)
	}
	// Kebab: split on "-", Title-case each segment.
	parts := strings.Split(attr, "-")
	var b strings.Builder
	for _, seg := range parts {
		if seg == "" {
			// Leading/trailing/double dash — not a valid kebab attr name; bail.
			return ""
		}
		b.WriteString(titleCase(seg))
	}
	return b.String()
}

// capitalizeFirst returns s with its first Unicode letter upper-cased.
// Returns s unchanged if s is empty.
func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	// Fast path: first byte is ASCII letter.
	if s[0] >= 'a' && s[0] <= 'z' {
		return string(s[0]-32) + s[1:]
	}
	// Slow path: first rune may be multi-byte.
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// titleCase returns s with its first Unicode letter upper-cased and the rest
// lower-cased, matching standard Title-case for a single word segment.
func titleCase(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	for i := 1; i < len(r); i++ {
		r[i] = unicode.ToLower(r[i])
	}
	return string(r)
}

// fieldMatcherOrDefault returns fm if non-nil, else defaultFieldMatcher.
func fieldMatcherOrDefault(fm FieldMatcher) FieldMatcher {
	if fm != nil {
		return fm
	}
	return defaultFieldMatcher
}

// validateMatchedField checks that fn (returned by a custom FieldMatcher) is a
// valid Go identifier AND — when declared is non-nil — is present in the known
// field set. The default matcher only ever returns fields that already exist, so
// this only matters for buggy custom matchers. Returns a non-nil error with code
// "bad-field-match" when the check fails.
func validateMatchedField(fn, attr, propsType string, declared map[string]bool) error {
	if !token.IsIdentifier(fn) {
		return fmt.Errorf("field matcher returned %q for attribute %q, which is not a valid Go identifier", fn, attr)
	}
	if declared != nil && !declared[fn] {
		return fmt.Errorf("field matcher returned %q for attribute %q, which is not an exported field of %s", fn, attr, propsType)
	}
	return nil
}

// matchField resolves an attr name to a props struct field name using the given
// FieldMatcher and the declared field set. It mirrors the old isPropField logic
// but adds kebab→Camel support and returns the matched field name directly.
//
// Rules (applied in order):
//  1. A non-identifier attr (contains "-", "@", ":", etc.) is passed to the
//     matcher; if the matcher returns a hit, it is a declared prop field.
//  2. A pure identifier attr: capitalize first letter, look up in declared.
//     If present it's a prop field; if absent (declared != nil), fall through.
//  3. When declared is nil (cross-package / unknown), assume a pure-identifier
//     attr IS a prop — today's behavior for cross-package children.
//  4. "Attrs" and "Children" are never caller-set prop fields; they fall through.
//
// Returns (fieldName, true) when the attr maps to a declared prop field,
// or ("", false) to send the attr to the Attrs bag.
func matchField(declared map[string]bool, attr string, fm FieldMatcher) (string, bool) {
	// Build a slice of field names from declared (or empty for nil/cross-package).
	// We need this for the matcher call.
	var fieldNames []string
	if declared != nil {
		fieldNames = make([]string, 0, len(declared))
		for f := range declared {
			fieldNames = append(fieldNames, f)
		}
	}

	// Guard: "Attrs" and "Children" are special synthesized fields; never prop.
	// Quick check via the old capitalize-first rule before calling the matcher.
	simple := capitalizeFirst(attr)
	if simple == "Attrs" || simple == "Children" {
		return "", false
	}

	if declared == nil {
		// Cross-package / unknown: assume identifier attrs are props (legacy behavior).
		// Non-identifier names (contains "-" etc.) call the matcher and check; if
		// the matcher says "yes" (custom matcher) we trust it, else fall through.
		// For the default matcher, kebab → candidate; since declared is nil and we
		// have no field list, we cannot confirm the candidate exists → fall through.
		// Only identifier attrs assume-yes on nil declared.
		if !strings.Contains(attr, "-") && isIdentifierAttr(attr) {
			return simple, true
		}
		// Non-identifier on unknown child → fall through to bag (safe default).
		return "", false
	}

	// declared is non-nil: use the matcher to look up the field.
	field, ok := fm(attr, fieldNames)
	if !ok {
		return "", false
	}
	// Double-check: Attrs/Children must never be matched as a prop field.
	if field == "Attrs" || field == "Children" {
		return "", false
	}
	return field, true
}

// matchOrderedAttrsField resolves an ordered-attrs attribute name to a props
// struct field. Ordered-attrs literals are only valid for gsx.Attrs-like props,
// and unlike ordinary attrs they may explicitly target the synthesized Attrs
// field itself:
//
//	<Panel Attrs={{ "data-x": "1" }}>
//
// Ordinary attr matching still treats Attrs as special so scalar attrs named
// "Attrs" fall through instead of being caller-set as a prop.
func matchOrderedAttrsField(declared map[string]bool, attr string, fm FieldMatcher) (string, bool) {
	if field, ok := matchField(declared, attr, fm); ok {
		return field, true
	}
	if attrToFieldCandidate(attr) != "Attrs" {
		return "", false
	}
	if declared == nil {
		return "Attrs", true
	}
	if declared["Attrs"] {
		return "Attrs", true
	}
	return "", false
}

// isIdentifierAttr reports whether attr is a valid Go identifier (no hyphens,
// colons, at-signs etc.). This is used for the cross-package nil-declared path.
func isIdentifierAttr(attr string) bool {
	if attr == "" {
		return false
	}
	for i, r := range attr {
		if i == 0 {
			if !unicode.IsLetter(r) && r != '_' {
				return false
			}
		} else {
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' {
				return false
			}
		}
	}
	return true
}
