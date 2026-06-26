// Package rawfmt is the language-agnostic embedding layer that formats the body
// of a raw-text element (today <style>) during gsx fmt. It substitutes each
// @{ } interpolation hole with a collision-free sentinel token, runs a
// Formatter on the resulting self-contained source, restores the holes, and
// re-indents the result into a pretty.Doc. On any failure it reports ok=false
// so the caller falls back to verbatim rendering.
package rawfmt

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gsxhq/gsx/internal/pretty"
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

// Formatter formats a self-contained source string of an embedded language,
// returning the formatted bytes or an error. An error is not fatal: the caller
// falls back to verbatim rendering. The signature matches the cssMin/jsMin
// minifier options so wrappers (e.g. a prettier shell-out) drop in directly.
type Formatter func(src []byte) ([]byte, error)

// Format renders a raw-text element body. segments and holes interleave with
// len(segments) == len(holes)+1; each holes[i] is the already-rendered gsx
// source of an interpolation (e.g. "@{ fg }"). It returns (doc, true) on
// success, where doc is the body content to place between the open and close
// tags: an indented block of the formatted lines plus a trailing HardLine that
// returns to the tag's own depth for the close tag.
//
// It returns (zero, false) — caller renders the body verbatim instead — on any
// arity mismatch, Formatter error, recovered panic, or hole-restoration
// mismatch. Format never itself fails fmt on parseable gsx.
func Format(segments, holes []string, f Formatter) (pretty.Doc, bool) {
	if len(segments) != len(holes)+1 {
		return pretty.Doc{}, false
	}
	placeholdered, prefix := buildPlaceholdered(segments, holes)
	formatted, err := safeFormat(f, placeholdered)
	if err != nil {
		return pretty.Doc{}, false
	}
	restored, ok := restore(string(formatted), prefix, holes)
	if !ok {
		return pretty.Doc{}, false
	}
	return reindent(restored), true
}

// safeFormat calls f, converting a panic into an error so a buggy Formatter
// (including a third-party plugin) degrades to verbatim instead of crashing fmt.
func safeFormat(f Formatter, src string) (out []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("formatter panicked: %v", r)
		}
	}()
	return f([]byte(src))
}

// reindent converts the formatter's multi-line output into a Doc placed between
// the open and close tags. Non-blank lines become HardLine+Text (the engine
// indents them to the body's depth). A blank line becomes a bare Text("\n"):
// the engine writes it with NO indent, so blank lines never carry trailing
// tabs (which would break idempotence). Trailing whitespace on each line is
// trimmed. The final HardLine returns to the tag's depth for the close tag.
func reindent(s string) pretty.Doc {
	s = strings.Trim(s, "\n")
	if strings.TrimSpace(s) == "" {
		// An empty or whitespace-only body stays inline: nothing renders between
		// the open and close tags (e.g. <script src=...></script>).
		return pretty.Text("")
	}
	lines := strings.Split(s, "\n")
	parts := make([]pretty.Doc, 0, len(lines)*2+1)
	for _, ln := range lines {
		ln = strings.TrimRight(ln, " \t")
		if ln == "" {
			parts = append(parts, pretty.Text("\n"))
			continue
		}
		parts = append(parts, pretty.HardLine, pretty.Text(ln))
	}
	return pretty.Concat(pretty.Indent(pretty.Concat(parts...)), pretty.HardLine)
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
