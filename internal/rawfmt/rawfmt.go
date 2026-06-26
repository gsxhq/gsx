// Package rawfmt is the language-agnostic embedding layer that formats the body
// of a raw-text element (today <style>) during gsx fmt. It substitutes each
// @{ } interpolation hole with a collision-free sentinel token, runs a
// Formatter on the resulting self-contained source, restores the holes, and
// re-indents the result into a pretty.Doc. On any failure it reports ok=false
// so the caller falls back to verbatim rendering.
package rawfmt

import (
	"strconv"
	"strings"
)

// sentinel returns the placeholder token for hole index i under prefix. The
// trailing "_" disambiguates indices (sentinel(1) is not a substring of
// sentinel(10)). The token is a valid CSS identifier so it survives tokenizing.
func sentinel(prefix string, i int) string {
	return prefix + strconv.Itoa(i) + "_"
}

// buildPlaceholdered returns the placeholdered source — segments interleaved
// with one sentinel per hole — and the collision-free prefix it used. segments
// and holes interleave: segments[0] sentinel0 segments[1] sentinel1 … and
// len(segments) == len(holes)+1 (the caller guarantees this). The prefix is
// chosen so neither it nor any sentinel occurs in the segments OR the holes:
// start from a base and append "x_" until absent (deterministic → idempotent).
func buildPlaceholdered(segments, holes []string) (text, prefix string) {
	var scan strings.Builder
	for _, s := range segments {
		scan.WriteString(s)
	}
	for _, h := range holes {
		scan.WriteString(h)
	}
	haystack := scan.String()
	prefix = "__gsxhole_"
	for strings.Contains(haystack, prefix) {
		prefix += "x_"
	}
	var b strings.Builder
	for i, seg := range segments {
		b.WriteString(seg)
		if i < len(holes) {
			b.WriteString(sentinel(prefix, i))
		}
	}
	return b.String(), prefix
}

// restore replaces each sentinel in formatted with its hole, verifying that
// every sentinel index appears EXACTLY once. Any missing or duplicated sentinel
// → (zero, false). Because the prefix is absent from every hole, replacing one
// sentinel never creates or destroys another, so check-and-replace in a loop is
// safe and order-independent.
func restore(formatted, prefix string, holes []string) (string, bool) {
	out := formatted
	for i := range holes {
		tok := sentinel(prefix, i)
		if strings.Count(out, tok) != 1 {
			return "", false
		}
		out = strings.Replace(out, tok, holes[i], 1)
	}
	// No sentinel for any index may remain (a stray prefix means corruption).
	if strings.Contains(out, prefix) {
		return "", false
	}
	return out, true
}
