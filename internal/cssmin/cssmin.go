// Package cssmin is gsx's codegen-time CSS minifier: a robust, stable, safe pass
// over the static CSS of <style> blocks. It performs only whitespace/comment
// reductions that cannot change rendering — never value rewrites — and is
// hole-aware (an ${ } interpolation is opaque, its adjacent whitespace preserved).
package cssmin

import "strings"

// minifyCSS applies the safe-minification set to a complete CSS string: strips
// comments (keeping /*! … */), collapses each whitespace run to one space,
// removes whitespace adjacent to { } ; , drops a ; immediately before }, and
// trims the edges. String literals and url(…) interiors are preserved verbatim.
// It never rewrites values and never removes a space that could be significant.
func minifyCSS(s string) string {
	out := make([]byte, 0, len(s))
	pending := false      // an unemitted whitespace run
	afterBang := false    // whitespace after a kept /*! */ is still suppressed

	isDelim := func(c byte) bool { return c == '{' || c == '}' || c == ';' || c == ',' }
	flush := func(cur byte) {
		if !pending {
			return
		}
		pending = false
		if afterBang {
			afterBang = false
			return // whitespace immediately after a bang-comment is dropped
		}
		if len(out) == 0 {
			return // leading whitespace: drop
		}
		if isDelim(out[len(out)-1]) || isDelim(cur) {
			return // adjacent to a delimiter: drop
		}
		out = append(out, ' ')
	}

	i, n := 0, len(s)
	for i < n {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f':
			pending = true
			i++
		case c == '/' && i+1 < n && s[i+1] == '*':
			closeIdx := strings.Index(s[i+2:], "*/")
			end := n
			if closeIdx >= 0 {
				end = i + 2 + closeIdx + 2
			}
			if i+2 < n && s[i+2] == '!' {
				flush('/')
				out = append(out, s[i:end]...) // keep /*! … */ verbatim
				afterBang = true
			} else {
				pending = true // a removed comment still separates tokens
			}
			i = end
		case c == '"' || c == '\'':
			flush(c)
			out = append(out, c)
			i++
			for i < n {
				out = append(out, s[i])
				if s[i] == '\\' && i+1 < n {
					out = append(out, s[i+1])
					i += 2
					continue
				}
				closed := s[i] == c
				i++
				if closed {
					break
				}
			}
		case (c == 'u' || c == 'U') && isURLOpen(s, i):
			flush(c)
			out = append(out, s[i:i+4]...) // "url("
			i += 4
			j := i
			for j < n && isSpace(s[j]) {
				j++
			}
			if j < n && (s[j] == '"' || s[j] == '\'') {
				continue // quoted url: main loop handles the string + ')'
			}
			for i < n && s[i] != ')' { // unquoted: copy interior verbatim
				out = append(out, s[i])
				i++
			}
			if i < n {
				out = append(out, ')')
				i++
			}
		default:
			flush(c)
			if c == '}' && len(out) > 0 && out[len(out)-1] == ';' {
				out = out[:len(out)-1] // drop a ; immediately before }
			}
			out = append(out, c)
			i++
		}
	}
	return string(out)
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f'
}

// isURLOpen reports whether s[i:] begins a url( function token: "url(" (case-
// insensitive) whose "url" is not the tail of a longer identifier.
func isURLOpen(s string, i int) bool {
	if i+4 > len(s) || !strings.EqualFold(s[i:i+4], "url(") {
		return false
	}
	return i == 0 || !isNameByte(s[i-1])
}

// isNameByte reports whether c can be part of a CSS identifier.
func isNameByte(c byte) bool {
	return c == '-' || c == '_' ||
		'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' ||
		'0' <= c && c <= '9' || c >= 0x80
}
