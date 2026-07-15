// parser/boundary.go
package parser

import (
	goparser "go/parser"
	"go/scanner"
	"go/token"
	"strings"
)

// goExprEnd returns the index of the `}` that matches the `{` at src[open],
// scanning Go tokens from `open` so that (a) braces inside strings, runes, and
// comments do not count and (b) any markup prose BEFORE `open` is never
// tokenized. ok is false if no matching brace is found.
func goExprEnd(src string, open int) (int, bool) {
	return goDepth1End(src, open+1)
}

// goStagesEnd returns the index of the `}` that closes a whole-literal
// `|> stage |> stage` tail following a gsx embedded backtick literal. `from`
// is the position right after the literal's closing backtick (NOT an opening
// delimiter) — the enclosing `{` has already been consumed by the caller, so
// scanning starts at depth 1, as goExprEnd's does once past its own `{`.
//
// This must NOT scan the literal itself: the literal uses gsx's
// backslash-backtick escape convention (see embeddedBacktickEscaped), which
// the naive backtick handling in skipQuotedOrComment does not understand — an
// odd number of escaped backticks would desync it. By starting after the
// literal, the region this scans is plain pipe-stage/Go-argument syntax,
// where a Go-aware scan is safe.
func goStagesEnd(src string, from int) (int, bool) {
	return goDepth1End(src, from)
}

// goDepth1End scans src from `from` for the `}` that returns paren/bracket/
// brace depth to 0, treating `from` as already one level deep (i.e. as if the
// opening `{` immediately preceding `from` had already been consumed). Braces
// inside strings, runes, and comments do not count.
//
// Fast path: when the remaining source has no '<' (no gsx element/fragment
// can start) and no '`' (no bare/js/css backtick literal can start), the
// legacy byte loop below is exact and cheap — skip the go/scanner-based
// scanGoExpr entirely. Otherwise delegate to scanGoExpr, which is
// byte-identical to this loop on every region either scanner is asked to
// delimit in production (see TestScanGoExprCorpusDifferential) but is also
// aware of element and gsx-escaped-backtick spans that this byte loop is not.
func goDepth1End(src string, from int) (int, bool) {
	if scanRegionObserver != nil {
		scanRegionObserver(src, from)
	}
	if strings.IndexByte(src[from:], '<') < 0 && strings.IndexByte(src[from:], '`') < 0 {
		depth := 1
		for i := from; i < len(src); {
			if end, ok := skipGSXEmbeddedLiteral(src, i); ok {
				i = end
				continue
			}
			if end, ok := skipQuotedOrComment(src, i); ok {
				i = end
				continue
			}
			switch src[i] {
			case '{', '(', '[':
				depth++
			case '}', ')', ']':
				depth--
				if depth == 0 && src[i] == '}' {
					return i, true
				}
			}
			i++
		}
		return 0, false
	}
	res := scanGoExpr(src, from)
	if res.Close < 0 {
		return 0, false
	}
	return res.Close, true
}

// composedDelims splits the (already brace-stripped) inner class/style source
// src for top-level ',' and ':' delimiters.
//
// Fast path: no '<' and no '`' anywhere in src (same tag/backtick guard as
// goDepth1End) means the legacy byte loop's *span-skipping* is exact and
// cheap. But unlike goDepth1End, the byte loop is ALSO wrong on plain Go
// source containing a top-level ':=' — it can't distinguish that ':' (part of
// a short-var-decl DEFINE token, e.g. a value-form `if`/`switch`'s
// `;`-separated init: `if x := f(); x { … }`) from a real `: cond` guard
// colon, and spuriously reports it as one (see
// TestScanGoExprValueFormInitDivergence). So the fast path additionally
// requires no ":=" substring; scanGoExpr tokenizes ':=' as a single DEFINE
// and never confuses it for a Colon. Otherwise delegate to scanGoExpr(src,
// 0): scanning from offset 0 with scanGoExpr's depth starting at 1 mirrors
// this loop's depth starting at 0 (src here has no enclosing brace of its own
// to balance against), so the two report the same top-level delimiters — see
// scanGoExpr's doc comment.
func composedDelims(src string) (commas, colons []int) {
	if composedRegionObserver != nil {
		composedRegionObserver(src)
	}
	if strings.IndexByte(src, '<') < 0 && strings.IndexByte(src, '`') < 0 && !strings.Contains(src, ":=") {
		depth := 0
		for i := 0; i < len(src); {
			if end, ok := skipGSXEmbeddedLiteral(src, i); ok {
				i = end
				continue
			}
			if end, ok := skipQuotedOrComment(src, i); ok {
				i = end
				continue
			}
			switch src[i] {
			case '{', '(', '[':
				depth++
			case '}', ')', ']':
				depth--
			case ',':
				if depth == 0 {
					commas = append(commas, i)
				}
			case ':':
				if depth == 0 {
					colons = append(colons, i)
				}
			}
			i++
		}
		return commas, colons
	}
	res := scanGoExpr(src, 0)
	return res.Commas, res.Colons
}

// skipGSXEmbeddedLiteral recognizes a prefixed gsx embedded literal (either
// delimiter form: js`/css`/f` or their `"`-delimited escape-hatch
// counterparts js"/css"/f") starting at src[i] and, if found, skips past it
// with full hole awareness (embeddedLiteralEndHoleAware) — a nested prefixed
// literal or a plain Go raw string inside one of its `@{ }` holes cannot
// terminate this literal early. Returns (0, false) when src[i] does not begin
// one of the six prefixed forms (at an identifier boundary).
func skipGSXEmbeddedLiteral(src string, i int) (int, bool) {
	switch {
	case hasIdentBoundary(src, i) && strings.HasPrefix(src[i:], "js`"):
		return embeddedLiteralEndHoleAware(src, i+len("js`"), '`')
	case hasIdentBoundary(src, i) && strings.HasPrefix(src[i:], "css`"):
		return embeddedLiteralEndHoleAware(src, i+len("css`"), '`')
	case hasIdentBoundary(src, i) && strings.HasPrefix(src[i:], "f`"):
		return embeddedLiteralEndHoleAware(src, i+len("f`"), '`')
	case hasIdentBoundary(src, i) && strings.HasPrefix(src[i:], `js"`):
		return embeddedLiteralEndHoleAware(src, i+len(`js"`), '"')
	case hasIdentBoundary(src, i) && strings.HasPrefix(src[i:], `css"`):
		return embeddedLiteralEndHoleAware(src, i+len(`css"`), '"')
	case hasIdentBoundary(src, i) && strings.HasPrefix(src[i:], `f"`):
		return embeddedLiteralEndHoleAware(src, i+len(`f"`), '"')
	default:
		return 0, false
	}
}

func hasIdentBoundary(src string, i int) bool {
	return !goIdentifierContinueBefore(src, i)
}

// embeddedLiteralEndHoleAware returns the offset just past the closing delim
// of a gsx embedded literal whose body starts at src[i] (i.e. just after the
// opening delimiter), scanning with full `@{ }` hole awareness. It honours
// gsx's backslash escape convention — a delim preceded by an odd number of
// backslashes (a backslash-escaped backtick in a backtick literal, `\"` in a
// '"'-delimited one) is a literal char, not the terminator; an `@{` preceded
// by an odd number of backslashes is likewise a literal `\@{`, not a hole
// open (mirrors parseEmbeddedSegments's embeddedAtBraceEscaped). delim is a
// backtick for the f/js/css backtick forms and '"' for the '"'-delimited
// escape-hatch forms.
//
// On an unescaped `@{`, the hole's body is skipped with goExprEnd — the same
// Go-expression-aware brace/string/comment scanner the attribute-literal
// path uses to find a hole's end (parseInterp calls it directly;
// parseEmbeddedSegments reaches it via parseInterp) — so a delimiter INSIDE
// the hole's Go expression (a nested prefixed literal via
// skipGSXEmbeddedLiteral, a plain Go raw string, a further nested hole)
// cannot be mistaken for THIS literal's own closing delim. goExprEnd's own
// nested-literal handling calls back into skipGSXEmbeddedLiteral, which
// calls this function again — mutual recursion that gives arbitrary nesting
// depth. Termination: every recursive step strictly advances the scanned
// offset (a hole's close is always found, if at all, at some position > the
// hole's open), bounded above by len(src), so the recursion cannot cycle.
//
// A hole that fails to close (goExprEnd's ok is false) or a literal that
// never finds its own closing delim both fall back to consuming to len(src)
// — the same graceful "unterminated" convention the flat scan this replaces
// used, matching every existing caller's treatment of the returned bool
// (always true; ok=false is reserved for "not a gsx literal here" in
// skipGSXEmbeddedLiteral, one layer up).
func embeddedLiteralEndHoleAware(src string, i int, delim byte) (int, bool) {
	for i < len(src) {
		if src[i] == delim && !backtickEscapedIn(src, i) {
			return i + 1, true
		}
		if src[i] == '@' && i+1 < len(src) && src[i+1] == '{' && !backtickEscapedIn(src, i) {
			close, ok := goExprEnd(src, i+1)
			if !ok {
				return len(src), true
			}
			i = close + 1
			continue
		}
		i++
	}
	return len(src), true
}

func backtickEscapedIn(src string, backtick int) bool {
	n := 0
	for i := backtick - 1; i >= 0 && src[i] == '\\'; i-- {
		n++
	}
	return n%2 == 1
}

func skipQuotedOrComment(src string, i int) (int, bool) {
	switch src[i] {
	case '"', '\'':
		quote := src[i]
		i++
		for i < len(src) {
			if src[i] == '\\' {
				i += 2
				continue
			}
			if src[i] == quote {
				return i + 1, true
			}
			i++
		}
		return len(src), true
	case '`':
		i++
		for i < len(src) && src[i] != '`' {
			i++
		}
		if i < len(src) {
			return i + 1, true
		}
		return len(src), true
	case '/':
		if i+1 >= len(src) {
			return 0, false
		}
		switch src[i+1] {
		case '/':
			i += 2
			for i < len(src) && src[i] != '\n' {
				i++
			}
			return i, true
		case '*':
			i += 2
			for i+1 < len(src) && (src[i] != '*' || src[i+1] != '/') {
				i++
			}
			if i+1 < len(src) {
				return i + 2, true
			}
			return len(src), true
		}
	}
	return 0, false
}

// scanToBlockBrace finds the byte offset of the '{' that opens a control-flow
// body. `from` points just after the leading `keyword` ("if"/"for"/"switch").
// It enumerates each '{' at paren/bracket depth 0 and returns the first one for
// which `keyword <header> {}` parses as a valid Go statement — delegating
// composite-literal disambiguation to go/parser, so bare composite literals in a
// `for … range` clause are handled correctly. ok is false if none parse.
func scanToBlockBrace(src string, from int, keyword string) (int, bool) {
	sub := src[from:]
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(sub))
	var s scanner.Scanner
	s.Init(file, []byte(sub), func(token.Position, string) {}, scanner.ScanComments)

	depth := 0
	for {
		pos, tok, _ := s.Scan()
		if tok == token.EOF {
			return 0, false
		}
		switch tok {
		case token.LPAREN, token.LBRACK:
			depth++
		case token.RPAREN, token.RBRACK:
			depth--
		case token.LBRACE:
			if depth == 0 {
				b := from + fset.Position(pos).Offset
				if blockHeaderParses(keyword + " " + src[from:b]) {
					return b, true
				}
				depth++ // composite-literal brace; descend into it
			} else {
				depth++
			}
		case token.RBRACE:
			depth--
		}
	}
}

// blockHeaderParses reports whether `header {}` is a valid Go control-flow
// statement (header includes the leading keyword). Used to locate the body brace
// of a gsx control-flow construct with full Go fidelity.
func blockHeaderParses(header string) bool {
	_, err := goparser.ParseFile(token.NewFileSet(), "", "package p\nfunc _(){\n"+header+"{}\n}", 0)
	return err == nil
}

// scanToCaseColon returns the index of the ':' that ends a switch case list,
// scanning Go tokens from `from` (the start of the case list) and returning the
// first ':' at paren/bracket/brace depth 0. Scanning starts at `from` — not
// offset 0 — so markup prose before the case list (e.g. an earlier case body
// with an apostrophe) is never tokenized and cannot desync the scanner. ok is
// false if no such ':' is found.
func scanToCaseColon(src string, from int) (int, bool) {
	sub := src[from:]
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(sub))
	var s scanner.Scanner
	s.Init(file, []byte(sub), func(token.Position, string) {}, scanner.ScanComments)

	depth := 0
	for {
		pos, tok, _ := s.Scan()
		if tok == token.EOF {
			return 0, false
		}
		switch tok {
		case token.LPAREN, token.LBRACK, token.LBRACE:
			depth++
		case token.RPAREN, token.RBRACK, token.RBRACE:
			depth--
		case token.COLON:
			if depth == 0 {
				return from + fset.Position(pos).Offset, true
			}
		}
	}
}

// valueSwitchArmEnd returns the byte offset of the next top-level case,
// default, or switch-closing brace after a value-switch arm.
// Delimiters nested inside Go composite literals, function literals, or other
// bracketed expressions are ignored.
func valueSwitchArmEnd(src string, from int) (int, bool) {
	sub := src[from:]
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(sub))
	var s scanner.Scanner
	s.Init(file, []byte(sub), func(token.Position, string) {}, scanner.ScanComments)

	depth := 0
	for {
		pos, tok, _ := s.Scan()
		if tok == token.EOF {
			return 0, false
		}
		off := from + fset.Position(pos).Offset
		if depth == 0 {
			switch tok {
			case token.CASE, token.DEFAULT, token.RBRACE:
				return off, true
			}
		}
		switch tok {
		case token.LPAREN, token.LBRACK, token.LBRACE:
			depth++
		case token.RPAREN, token.RBRACK, token.RBRACE:
			depth--
		}
	}
}

// delimEnd returns the index of the `close` token matching the opener at
// src[open], scanning Go tokens from `open` so prose before `open` is never
// tokenized. `stop` (may be nil) is a set of tokens that terminate the scan
// as not-found; it applies only to tokens DIRECTLY inside the scanned list
// (depth 1) — nested brackets/braces may legally contain them (e.g. the `/`
// in a `[8/4]byte` array length). Callers use it to bound a scan to a
// syntactic context where those tokens are impossible, so a missing closer
// fails fast at the opener instead of matching an unrelated closer pages
// later.
func delimEnd(src string, open int, close token.Token, stop map[token.Token]bool) (int, bool) {
	sub := src[open:]
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(sub))
	var s scanner.Scanner
	s.Init(file, []byte(sub), nil, scanner.ScanComments)

	depth := 0
	for {
		pos, tok, _ := s.Scan()
		if tok == token.EOF {
			return 0, false
		}
		switch tok {
		case token.LPAREN, token.LBRACE, token.LBRACK:
			depth++
		case token.RPAREN, token.RBRACE, token.RBRACK:
			depth--
			if depth == 0 && tok == close {
				return open + fset.Position(pos).Offset, true
			}
		default:
			if depth == 1 && stop[tok] {
				return 0, false
			}
		}
	}
}

// typeListStop are tokens that can never occur inside a Go type-argument or
// type-parameter list: `<` alone (chan's `<-` is the distinct ARROW token),
// `>`, `/`, an inserted-or-real semicolon (Go itself rejects a bare newline
// before the `]`), and scanner ILLEGAL. Hitting one means the `[` list is
// unterminated — the caller anchors its error at the opener.
var typeListStop = map[token.Token]bool{
	token.SEMICOLON: true,
	token.LSS:       true,
	token.GTR:       true,
	token.QUO:       true,
	token.ILLEGAL:   true,
}

// parenEnd returns the index of the `)` matching the `(` at src[open].
//
// Not rerouted onto scanGoExpr (Task 2): scanGoExpr has no general
// matching-closer facility — only Close, which tracks '}', not ')'.
//
// parenEnd has two call sites, and they are NOT symmetric. A component's
// receiver/param type list (component.go) genuinely cannot contain a gsx
// element or backtick literal — Go type lists have no operand-position
// `<tag>` — so delimEnd's plain go/scanner-based matching is exact there,
// unconditionally.
//
// A pipe-filter's Go argument list (pipe.go's parsePipeStage), by contrast,
// CAN contain an element as an argument today: `{ x |> wrap(<b/>) }` parses
// (the element's own `<`/`>`/`/` bytes don't affect delimEnd's paren/bracket/
// brace depth, so it still finds the real closing `)` correctly). What
// delimEnd does NOT understand is an element's opaque text/attribute
// interior: a `)` inside a quoted attribute value or text content (e.g.
// `wrap(<b>closing)</b>)`) would be miscounted as the argument list's own
// close. parenEnd is left unrouted anyway, uninstrumented by a tag/backtick
// guard, because this fails SAFE rather than silently miscompiling: the
// premature "close" desyncs parsePipeStage's trailing-content check, which
// reports a "trailing text after `)`" parse error rather than accepting
// malformed input. Giving parenEnd real element-span awareness is deferred
// to whenever elements-as-filter-args are formally wired up and validated
// (Task 3+ of the embedded-literals work), not required for Task 2's
// byte-identical reroute.
func parenEnd(src string, open int) (int, bool) {
	return delimEnd(src, open, token.RPAREN, nil)
}

// bracketEnd returns the index of the `]` matching the `[` at src[open],
// bounded by typeListStop (it only ever scans type-arg/type-param lists).
func bracketEnd(src string, open int) (int, bool) {
	return delimEnd(src, open, token.RBRACK, typeListStop)
}
