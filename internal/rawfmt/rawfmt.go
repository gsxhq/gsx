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

// FormatString runs the placeholder → format → (escape) → restore pipeline and
// returns the formatted source with holes restored, WITHOUT re-indenting into a
// Doc (the caller shapes it). escape, if non-nil, is applied to the formatted
// text BEFORE hole restoration — used to re-escape an embedded literal's
// delimiter. Escaping the whole placeholdered string is safe: the hole sentinels
// are collision-free identifiers containing no escapable character, so escaping
// never corrupts one, and the restored holes are inserted afterward and stay
// unescaped. ok=false on arity mismatch, formatter error/panic, or a hole-restore
// mismatch (the caller falls back to verbatim).
func FormatString(segments, holes []string, f Formatter, escape func(string) string) (string, bool) {
	if len(segments) != len(holes)+1 {
		return "", false
	}
	placeholdered, prefix := buildPlaceholdered(segments, holes)
	formatted, err := safeFormat(f, placeholdered)
	if err != nil {
		return "", false
	}
	out := string(formatted)
	if escape != nil {
		out = escape(out)
	}
	restored, ok := restore(out, prefix, holes)
	if !ok {
		return "", false
	}
	return restored, true
}

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
	restored, ok := FormatString(segments, holes, f, nil)
	if !ok {
		return pretty.Doc{}, false
	}
	return reindent(restored), true
}

// LineFormatter formats a self-contained embedded-language source and returns the
// re-indented LOGICAL lines: an Opaque token (template literal, block comment,
// string) keeps its internal newlines WITHIN one line. ok=false on a lex error →
// caller renders verbatim. Unlike Formatter (flat bytes), it carries the
// logical-line structure the Doc builder needs to leave multi-line-token interiors
// verbatim instead of re-indenting them.
type LineFormatter func(src []byte) (lines []string, ok bool)

// FormatStringLines runs placeholder → line-format → (escape per line) → restore
// and returns the formatted LOGICAL lines with holes restored. escape (if
// non-nil) is applied to each line BEFORE restoration; safe because hole sentinels
// contain no escapable character and never span a line. ok=false on arity
// mismatch, format failure, or a hole-restore mismatch.
func FormatStringLines(segments, holes []string, lf LineFormatter, escape func(string) string) ([]string, bool) {
	if len(segments) != len(holes)+1 {
		return nil, false
	}
	placeholdered, prefix := buildPlaceholdered(segments, holes)
	lines, ok := lf([]byte(placeholdered))
	if !ok {
		return nil, false
	}
	if escape != nil {
		for i := range lines {
			lines[i] = escape(lines[i])
		}
	}
	return restoreLines(lines, prefix, holes)
}

// FormatLines renders a raw-text element body (<script>/<style>) from a
// LineFormatter. Each logical line becomes HardLine+Text; a line's internal
// newlines (Opaque content) are emitted verbatim by Text — no managed indent — so
// template-literal / block-comment interiors survive re-indentation. ok=false →
// caller renders verbatim.
func FormatLines(segments, holes []string, lf LineFormatter) (pretty.Doc, bool) {
	lines, ok := FormatStringLines(segments, holes, lf, nil)
	if !ok {
		return pretty.Doc{}, false
	}
	return reindentLines(lines), true
}

// reindentLines is reindent for LOGICAL lines. It trims leading/trailing blank
// logical lines, then emits one HardLine+Text per line (blank → bare "\n").
// TrimRight touches only each logical line's tail (its last physical line);
// Opaque interior newlines and their whitespace are preserved. Wrapped in Indent
// with a trailing HardLine, matching reindent's placement between the tags.
func reindentLines(lines []string) pretty.Doc {
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return pretty.Text("")
	}
	parts := make([]pretty.Doc, 0, len(lines)*2+1)
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			parts = append(parts, pretty.Text("\n"))
			continue
		}
		parts = append(parts, pretty.HardLine, pretty.Text(strings.TrimRight(ln, " \t")))
	}
	return pretty.Concat(pretty.Indent(pretty.Concat(parts...)), pretty.HardLine)
}

// restoreLines is restore for a slice of logical lines: each sentinel index must
// appear EXACTLY once across all lines (a sentinel never spans a line boundary).
// ok=false on any missing/duplicated sentinel or a stray prefix.
func restoreLines(lines []string, prefix string, holes []string) ([]string, bool) {
	for i := range holes {
		tok := sentinel(prefix, i)
		count := 0
		for _, ln := range lines {
			count += strings.Count(ln, tok)
		}
		if count != 1 {
			return nil, false
		}
		for j := range lines {
			if strings.Contains(lines[j], tok) {
				lines[j] = strings.Replace(lines[j], tok, holes[i], 1)
				break
			}
		}
	}
	for _, ln := range lines {
		if strings.Contains(ln, prefix) {
			return nil, false
		}
	}
	return lines, true
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
