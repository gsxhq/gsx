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

// ClassMerger is the installable class-merge strategy. It receives the flattened,
// non-empty class tokens in source order and returns the final class string. The
// default dedupes (first occurrence wins) and joins with single spaces. Apps
// replace it once at init to install e.g. a Tailwind-aware merger.
var ClassMerger func(tokens []string) string = defaultClassMerge

func defaultClassMerge(tokens []string) string {
	seen := make(map[string]struct{}, len(tokens))
	out := make([]string, 0, len(tokens))
	for _, t := range tokens {
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return strings.Join(out, " ")
}

// classTokens flattens the on parts into whitespace-split, non-empty tokens.
func classTokens(parts []ClassPart) []string {
	var toks []string
	for _, p := range parts {
		if !p.on {
			continue
		}
		toks = append(toks, strings.Fields(p.s)...)
	}
	return toks
}

// Class composes parts, runs them through ClassMerger, and writes the escaped
// class attribute value.
func (gw *Writer) Class(parts ...ClassPart) {
	if gw.err != nil {
		return
	}
	gw.AttrValue(ClassMerger(classTokens(parts)))
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
