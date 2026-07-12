package gsx

import "strings"

// splitDecls splits a CSS declaration list into trimmed declarations, treating
// ';' as a separator only when not nested in () and not inside a quote — so a ';'
// inside url(data:…;base64,…) or a quoted string is NOT a boundary.
func splitDecls(s string) []string {
	var decls []string
	depth := 0
	var quote byte // 0, '\'' or '"'
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '(':
			depth++
		case c == ')':
			if depth > 0 {
				depth--
			}
		case c == ';' && depth == 0:
			if d := strings.TrimSpace(s[start:i]); d != "" {
				decls = append(decls, d)
			}
			start = i + 1
		}
	}
	if d := strings.TrimSpace(s[start:]); d != "" {
		decls = append(decls, d)
	}
	return decls
}

// declProp returns the lower-cased property name of a declaration (text before
// the first ':' that is not nested in () nor inside a quote), or "" if there is
// no such ':' (a malformed fragment).
func declProp(decl string) string {
	depth := 0
	var quote byte
	for i := 0; i < len(decl); i++ {
		c := decl[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '(':
			depth++
		case c == ')':
			if depth > 0 {
				depth--
			}
		case c == ':' && depth == 0:
			return strings.ToLower(strings.TrimSpace(decl[:i]))
		}
	}
	return ""
}

// StyleMerged emits a merged ` style="…"` attribute combining rootStyle then
// bagStyle (caller last), deduping by property keeping the LAST occurrence,
// survivors in source order. A malformed fragment (no ':') is dropped. When the
// merged result is empty it emits nothing (matching the empty-bag no-op).
func (gw *Writer) StyleMerged(rootStyle, bagStyle string) {
	if gw.err != nil {
		return
	}
	// Fast path: no style on either side — the common component root. Skip the
	// split/dedup machinery entirely (this runs on every component root render).
	if rootStyle == "" && bagStyle == "" {
		return
	}
	rootDecls := splitDecls(rootStyle)
	bagDecls := splitDecls(bagStyle)
	decls := append(rootDecls, bagDecls...)
	// When every declaration is valid and unique there is nothing to merge or
	// discard. Preserve the authored bytes within each contributor; rebuilding
	// with strings.Join would normalize semicolon adjacency even though merging
	// had made no semantic change. The contributor boundary still uses the same
	// explicit separator as Attrs.Style.
	unique := len(decls) > 0
	for i, d := range decls {
		p := declProp(d)
		if p == "" {
			unique = false
			break
		}
		for j := range i {
			if declProp(decls[j]) == p {
				unique = false
				break
			}
		}
		if !unique {
			break
		}
	}
	if unique {
		rootRaw, bagRaw := rootStyle, bagStyle
		if len(rootDecls) == 0 {
			rootRaw = ""
		}
		if len(bagDecls) == 0 {
			bagRaw = ""
		}
		value := joinAttrStrings("style", rootRaw, bagRaw)
		if value == "" {
			return
		}
		gw.writeStr(` style="`)
		gw.AttrValue(value)
		gw.writeStr(`"`)
		return
	}
	out := decls[:0]
	for i, d := range decls {
		p := declProp(d)
		if p == "" {
			continue
		}
		// Keep d only if its property does not recur later (last-wins). Linear
		// over the (typically few) decls — no map allocation.
		dup := false
		for j := i + 1; j < len(decls); j++ {
			if declProp(decls[j]) == p {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, d)
		}
	}
	if len(out) == 0 {
		return
	}
	gw.writeStr(` style="`)
	gw.AttrValue(strings.Join(out, "; "))
	gw.writeStr(`"`)
}
