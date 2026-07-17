package parser

import (
	"unicode"
	"unicode/utf8"
)

// goIdentifierStart reports the exact first-rune rule from the Go spec:
// identifiers begin with a Unicode letter or underscore.
func goIdentifierStart(r rune) bool {
	return r == '_' || unicode.IsLetter(r)
}

// goIdentifierContinue reports the exact continuation-rune rule from the Go
// spec: Unicode letters, Unicode decimal digits, and underscores.
func goIdentifierContinue(r rune) bool {
	return goIdentifierStart(r) || unicode.IsDigit(r)
}

// validRuneAt decodes the rune beginning at byte offset at. Invalid UTF-8 is
// never treated as U+FFFD: DecodeRuneInString's size-one RuneError is rejected.
func validRuneAt(src string, at int) (rune, int, bool) {
	if at < 0 || at >= len(src) {
		return 0, 0, false
	}
	r, size := utf8.DecodeRuneInString(src[at:])
	if r == utf8.RuneError && size == 1 {
		return 0, 0, false
	}
	return r, size, true
}

func goIdentifierContinueAt(src string, at int) bool {
	r, _, ok := validRuneAt(src, at)
	return ok && goIdentifierContinue(r)
}

func goIdentifierContinueBefore(src string, at int) bool {
	if at <= 0 || at > len(src) {
		return false
	}
	r, size := utf8.DecodeLastRuneInString(src[:at])
	return (r != utf8.RuneError || size != 1) && goIdentifierContinue(r)
}

// scanGoIdentifier returns the first byte after the Go identifier beginning at
// start. It returns start when the first rune is not a valid identifier start.
func scanGoIdentifier(src string, start int) int {
	r, size, ok := validRuneAt(src, start)
	if !ok || !goIdentifierStart(r) {
		return start
	}
	for at := start + size; ; {
		r, size, ok = validRuneAt(src, at)
		if !ok || !goIdentifierContinue(r) {
			return at
		}
		at += size
	}
}

func attrNameRune(r rune) bool {
	return goIdentifierContinue(r) || r == ':' || r == '@' || r == '.' || r == '-'
}

func tagNameRune(r rune) bool {
	return goIdentifierContinue(r) || r == '-' || r == '.'
}

func scanName(src string, start int, accept func(rune) bool) int {
	at := start
	for {
		r, size, ok := validRuneAt(src, at)
		if !ok || !accept(r) {
			return at
		}
		at += size
	}
}

func scanAttrName(src string, start int) int {
	return scanName(src, start, attrNameRune)
}

func scanTagName(src string, start int) int {
	return scanName(src, start, tagNameRune)
}

func tagNameRuneAt(src string, at int) bool {
	r, _, ok := validRuneAt(src, at)
	return ok && tagNameRune(r)
}

// startsTagAt reports whether src[at:] begins a Go-style identifier tag, a
// fragment close, or an element close. The tag scanner retains GSX's existing
// '-' and '.' continuation extensions after this initial rune.
func startsTagAt(src string, at int) bool {
	if at < 0 || at >= len(src) {
		return false
	}
	if src[at] == '>' || src[at] == '/' {
		return true
	}
	r, _, ok := validRuneAt(src, at)
	return ok && goIdentifierStart(r)
}

func firstInvalidUTF8(src []byte) int {
	for at := 0; at < len(src); {
		_, size := utf8.DecodeRune(src[at:])
		if size == 1 && src[at] >= utf8.RuneSelf {
			return at
		}
		at += size
	}
	return -1
}
