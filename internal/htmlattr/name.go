package htmlattr

import "unicode/utf8"

// NameRune reports whether r may appear anywhere in an attribute name. It is
// the HTML authoring rule — "one or more characters other than controls,
// U+0020 SPACE, U+0022, U+0027, U+003E, U+002F, U+003D, and noncharacters" —
// minus four runes gsx reserves: `{` and `}` are tag structure (spread,
// `{ if }`, `{ switch }`, value forms), a backtick opens the f/js/css value
// literals (so `style`color:red“ stays the missing-`=` error it always was),
// and `<` is an HTML tokenizer parse error inside a name and in practice
// always a typo. Every position uses the same rule; there is no distinct
// first-rune class, so `.prop`, `?attr`, `#ref`, `[prop]`, `(event)` and
// `on:click|mod` are ordinary names.
//
// The parser scans names with it and the runtime spread keeps or drops bag keys
// with ValidName, so a name the compiler accepts is a name the leaf emits.
func NameRune(r rune) bool {
	switch {
	case r <= 0x20, 0x7f <= r && r <= 0x9f: // C0 controls, space, DEL, C1 controls
		return false
	case 0xfdd0 <= r && r <= 0xfdef, r&0xfffe == 0xfffe: // Unicode noncharacters
		return false
	}
	switch r {
	case '"', '\'', '>', '/', '=', '<', '{', '}', '`':
		return false
	}
	return true
}

// ValidName reports whether k is a non-empty, well-formed UTF-8 attribute name
// made entirely of NameRune runes. Invalid UTF-8 is never read as U+FFFD.
func ValidName(k string) bool {
	if k == "" {
		return false
	}
	for at := 0; at < len(k); {
		r, size := utf8.DecodeRuneInString(k[at:])
		if (r == utf8.RuneError && size == 1) || !NameRune(r) {
			return false
		}
		at += size
	}
	return true
}

// SameName reports whether a and b name the same HTML attribute. The HTML
// tokenizer lowercases ASCII letters in attribute names — and only those, even
// in foreign content, before SVG/MathML names like viewBox are adjusted — so
// two names are one attribute exactly when they are equal after ASCII case
// folding. Non-ASCII bytes compare exactly: unlike strings.EqualFold, the
// Kelvin sign is not "k". Every place gsx decides that one attribute
// overrides, suppresses or merges with another compares names with it.
func SameName(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		if lowerASCII(a[i]) != lowerASCII(b[i]) {
			return false
		}
	}
	return true
}

// FoldName returns name's canonical spelling under SameName (ASCII letters
// lowercased, every other byte unchanged), for use as a map key:
// SameName(a, b) == (FoldName(a) == FoldName(b)). A name with no ASCII
// uppercase letter is returned as is, without allocating.
func FoldName(name string) string {
	for i := 0; i < len(name); i++ {
		if 'A' <= name[i] && name[i] <= 'Z' {
			b := []byte(name)
			for j := i; j < len(b); j++ {
				b[j] = lowerASCII(b[j])
			}
			return string(b)
		}
	}
	return name
}

func lowerASCII(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}
