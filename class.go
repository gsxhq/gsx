package gsx

import "strings"

// ClassPart is one contribution to a class or style attribute: a string included
// only when on. Generated code builds these from the `"str": cond` source sugar.
type ClassPart struct {
	s  string
	on bool
}

// Class is an unconditional class/style contribution.
func Class(s string) ClassPart { return ClassPart{s: s, on: true} }

// ClassIf includes s only when on.
func ClassIf(s string, on bool) ClassPart { return ClassPart{s: s, on: on} }

// DefaultClassMerge is the built-in class-merge strategy. It receives the raw,
// un-split class strings of each on source (static parts, toggles, and the
// caller's fallthrough class) in source order, and resolves cross-source
// conflicts by keeping the LAST occurrence of each token (caller/last-wins),
// preserving surviving tokens in source order, e.g. ["a b", "a"] -> "b a".
//
// A single source is returned verbatim — there is nothing to merge across, so the
// author's (or caller's) class string is preserved exactly. This makes the common
// component-root case (one static class, no fallthrough) allocation-free.
// Generated code passes this as the merge function when no class_merger is
// configured.
func DefaultClassMerge(classes []string) string {
	switch len(classes) {
	case 0:
		return ""
	case 1:
		return classes[0]
	}
	// Multiple sources: split into tokens, dedupe last-wins, join. The dedupe is
	// in-place over the flattened token slice (no map), so a conflict-free merge
	// allocates only the token slice and the joined result.
	var toks []string
	for _, c := range classes {
		toks = append(toks, strings.Fields(c)...)
	}
	return strings.Join(dedupeLastWins(toks), " ")
}

// dedupeLastWins removes duplicate tokens keeping each token's LAST occurrence,
// otherwise preserving order. It compacts in place over toks' backing array, so
// it allocates nothing.
func dedupeLastWins(toks []string) []string {
	out := toks[:0]
	for i, t := range toks {
		dup := false
		for j := i + 1; j < len(toks); j++ {
			if toks[j] == t {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, t)
		}
	}
	return out
}

// onClasses collects the on, non-empty class strings from parts (raw, un-split),
// in source order — the input a ClassMerger receives.
func onClasses(parts []ClassPart) []string {
	var classes []string
	for _, p := range parts {
		if p.on && p.s != "" {
			classes = append(classes, p.s)
		}
	}
	return classes
}

// loneToken returns the lone on, non-empty part's string when exactly one part is
// on and it is a single token (no ASCII whitespace), else ("", false). A single
// class token cannot conflict with anything, so the merge step is a guaranteed
// no-op for any merger — letting the hot single-class case skip the slice and the
// merger call entirely.
func loneToken(parts []ClassPart) (string, bool) {
	s := ""
	for _, p := range parts {
		if !p.on || p.s == "" {
			continue
		}
		if s != "" || hasClassSpace(p.s) {
			return "", false
		}
		s = p.s
	}
	return s, s != ""
}

// hasClassSpace reports whether s contains ASCII whitespace (what class tokens are
// split on), i.e. whether s holds more than one token.
func hasClassSpace(s string) bool {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ' ', '\t', '\n', '\r', '\f', '\v':
			return true
		}
	}
	return false
}

// StyleString returns the merged style declaration string for parts (the value
// form of gw.Style), so generated code can pass a composed root style to
// StyleMerged. Like gw.Style it includes only on-parts and joins with "; ", but
// does NOT attr-escape (the caller escapes).
func StyleString(parts ...ClassPart) string {
	decls := make([]string, 0, len(parts))
	for _, p := range parts {
		if !p.on {
			continue
		}
		if d := strings.TrimSpace(p.s); d != "" {
			decls = append(decls, d)
		}
	}
	return strings.Join(decls, "; ")
}

// ClassString returns the merged class string for parts, run through merge (the
// value form of gw.Class), so generated code can place a composed class into an
// Attrs map. merge receives the raw, un-split on-class strings.
func ClassString(merge func(classes []string) string, parts ...ClassPart) string {
	if s, ok := loneToken(parts); ok {
		return s
	}
	return merge(onClasses(parts))
}

// ClassJoin flattens the on, non-empty parts into a single class string, applying
// only the built-in structural last-wins dedup (NOT the configured ClassMerger).
// It is for placing a composable class into an Attrs bag (the "class" entry a
// child component receives as fallthrough): the consuming root applies the
// configured merger exactly once over the root's own classes plus this bag string
// (Attrs.Class()), so running the configured merger here too would be redundant —
// and, with an expensive Tailwind-style merger, double the per-element cost.
//
// The structural dedup is kept (cheap, no merger call) so a composable class with
// repeated tokens resolves to the same string whether it sits on a root or is
// forwarded to a child — matching DefaultClassMerge's "keep last occurrence" rule
// for the common (default-merger) case.
func ClassJoin(parts ...ClassPart) string {
	var toks []string
	for _, p := range parts {
		if !p.on || p.s == "" {
			continue
		}
		toks = append(toks, strings.Fields(p.s)...)
	}
	return strings.Join(dedupeLastWins(toks), " ")
}

// Class composes parts, runs them through merge, and writes the escaped class
// attribute value. merge receives the raw, un-split on-class strings.
func (gw *Writer) Class(merge func(classes []string) string, parts ...ClassPart) {
	if gw.err != nil {
		return
	}
	if s, ok := loneToken(parts); ok {
		gw.AttrValue(s)
		return
	}
	gw.AttrValue(merge(onClasses(parts)))
}

// ClassMerged writes a class attribute composed of parts plus the extra string
// (e.g. a fallthrough Attrs.Class()), running everything through merge. It writes
// nothing when there is no class to emit — so a root element with no class and no
// fallthrough class stays attribute-free. merge receives the raw, un-split
// on-class strings.
func (gw *Writer) ClassMerged(merge func(classes []string) string, extra string, parts ...ClassPart) {
	if gw.err != nil {
		return
	}
	classes := onClasses(parts)
	if e := strings.TrimSpace(extra); e != "" {
		classes = append(classes, e)
	}
	var merged string
	switch {
	case len(classes) == 0:
		return
	case len(classes) == 1 && !hasClassSpace(classes[0]):
		// Lone token: no merge needed for any merger.
		merged = classes[0]
	default:
		merged = merge(classes)
	}
	if merged == "" {
		return
	}
	gw.writeStr(` class="`)
	gw.AttrValue(merged)
	gw.writeStr(`"`)
}

// Style composes the on parts as '; '-joined declarations (no merge) and writes
// the escaped style attribute value.
func (gw *Writer) Style(parts ...ClassPart) {
	if gw.err != nil {
		return
	}
	var decls []string
	for _, p := range parts {
		if !p.on {
			continue
		}
		if d := strings.TrimSpace(p.s); d != "" {
			decls = append(decls, d)
		}
	}
	gw.AttrValue(strings.Join(decls, "; "))
}
