// Package cssfmt is a minimal CSS formatter built on the pretty Doc IR. It is
// the built-in rawfmt.Formatter for <style> bodies during gsx fmt: tokenize →
// parse rules/declarations/at-rules → build a pretty.Doc → Print. It is
// deliberately minimal — correct-or-error, never best-effort-mangle: any
// construct it cannot represent returns an error so the caller falls back to
// verbatim. It has no knowledge of HTML nesting; rawfmt owns the outer indent.
package cssfmt

import (
	"fmt"
	"strings"

	"github.com/gsxhq/gsx/internal/pretty"
)

// Format formats a self-contained CSS source string at the given print width.
func Format(src []byte, width int) ([]byte, error) {
	toks, err := tokenize(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	items, err := p.parseItems(false)
	if err != nil {
		return nil, err
	}
	if p.i != len(p.toks) {
		return nil, fmt.Errorf("unexpected %q", p.toks[p.i].text)
	}
	doc := layoutTopLevel(items)
	out := pretty.Print(doc, width)
	out = strings.TrimRight(out, "\n") + "\n"
	return []byte(out), nil
}

// --- parse tree -------------------------------------------------------------

// item is one top-level or in-block construct.
type item struct {
	comment string // non-empty → a standalone comment item (other fields unused)

	// rule / at-rule with a block:
	prelude []token // selector list or at-rule prelude (raw tokens, trimmed of edge WS)
	block   []item  // children when isBlock

	// declaration or at-rule statement (no block):
	decl []token // raw tokens of "prop: value" or "@import …" (trimmed of edge WS)

	isBlock bool // has { block }
	isDecl  bool // declaration or statement ending in ; (or block end)
}

type parser struct {
	toks []token
	i    int
}

func (p *parser) peek() (token, bool) {
	if p.i < len(p.toks) {
		return p.toks[p.i], true
	}
	return token{}, false
}

// parseItems parses items until EOF (inBlock=false) or a matching } (inBlock=true).
func (p *parser) parseItems(inBlock bool) ([]item, error) {
	var items []item
	for {
		t, ok := p.peek()
		if !ok {
			if inBlock {
				return nil, fmt.Errorf("unterminated block")
			}
			return items, nil
		}
		switch t.kind {
		case tWS:
			p.i++
		case tComment:
			items = append(items, item{comment: t.text})
			p.i++
		case tRBrace:
			if inBlock {
				p.i++ // consume }
				return items, nil
			}
			return nil, fmt.Errorf("unexpected }")
		default:
			it, err := p.parseStatement()
			if err != nil {
				return nil, err
			}
			items = append(items, it)
		}
	}
}

// parseStatement collects tokens until ';' (a declaration/at-statement), '{' (a
// rule/at-rule, then its block), or '}'/EOF. The prelude tokens are trimmed of
// edge whitespace.
func (p *parser) parseStatement() (item, error) {
	start := p.i
	for {
		t, ok := p.peek()
		if !ok {
			// Reached EOF without ; or } — a declaration with no terminator is
			// tolerated, but an unclosed block already errored above. Treat the
			// leftover as a malformed input.
			return item{}, fmt.Errorf("unterminated statement")
		}
		switch t.kind {
		case tSemi:
			toks := trimWS(p.toks[start:p.i])
			p.i++ // consume ;
			return item{decl: toks, isDecl: true}, nil
		case tLBrace:
			prelude := trimWS(p.toks[start:p.i])
			p.i++ // consume {
			block, err := p.parseItems(true)
			if err != nil {
				return item{}, err
			}
			return item{prelude: prelude, block: block, isBlock: true}, nil
		case tRBrace:
			// Declaration with no trailing ';' before the block closes.
			toks := trimWS(p.toks[start:p.i])
			if len(toks) == 0 {
				return item{}, fmt.Errorf("empty statement")
			}
			return item{decl: toks, isDecl: true}, nil
		default:
			p.i++
		}
	}
}

func trimWS(toks []token) []token {
	for len(toks) > 0 && toks[0].kind == tWS {
		toks = toks[1:]
	}
	for len(toks) > 0 && toks[len(toks)-1].kind == tWS {
		toks = toks[:len(toks)-1]
	}
	return toks
}

// --- layout -----------------------------------------------------------------

// layoutTopLevel renders the top-level item list with a BLANK line (two
// HardLines) between adjacent rules/at-rules, per the spec's readability rule.
// Nested block bodies keep layoutItems' single-HardLine separation.
func layoutTopLevel(items []item) pretty.Doc {
	var parts []pretty.Doc
	for i, it := range items {
		if i > 0 {
			parts = append(parts, pretty.HardLine, pretty.HardLine)
		}
		parts = append(parts, layoutItem(it))
	}
	return pretty.Concat(parts...)
}

func layoutItems(items []item) pretty.Doc {
	var parts []pretty.Doc
	for i, it := range items {
		if i > 0 {
			parts = append(parts, pretty.HardLine)
		}
		parts = append(parts, layoutItem(it))
	}
	return pretty.Concat(parts...)
}

func layoutItem(it item) pretty.Doc {
	switch {
	case it.comment != "":
		return pretty.Text(it.comment)
	case it.isBlock:
		head := layoutPrelude(it.prelude)
		body := layoutItems(it.block)
		return pretty.Concat(
			head, pretty.Text(" {"),
			pretty.Indent(pretty.Concat(pretty.HardLine, body)),
			pretty.HardLine, pretty.Text("}"),
		)
	case it.isDecl:
		return layoutDecl(it.decl)
	default:
		return pretty.Text("")
	}
}

// layoutPrelude renders a selector list / at-rule prelude: top-level
// comma-separated parts joined with ", ", wrapping via Fill when too wide.
func layoutPrelude(toks []token) pretty.Doc {
	groups := splitTopLevel(toks, tComma)
	if len(groups) <= 1 {
		return pretty.Text(renderInline(toks))
	}
	var fill []pretty.Doc
	for i, g := range groups {
		if i > 0 {
			fill = append(fill, pretty.Text(","), pretty.Line)
		}
		fill = append(fill, pretty.Text(renderInline(g)))
	}
	return pretty.Group(pretty.Fill(fill...))
}

// layoutDecl renders "prop: value;". The first top-level colon splits property
// from value; the value's top-level comma list wraps via Fill.
func layoutDecl(toks []token) pretty.Doc {
	prop, value, ok := splitFirst(toks, tColon)
	if !ok {
		// No colon (e.g. a bare at-statement like @import "x"): render inline.
		return pretty.Concat(pretty.Text(renderInline(toks)), pretty.Text(";"))
	}
	groups := splitTopLevel(value, tComma)
	if len(groups) <= 1 {
		return pretty.Concat(
			pretty.Text(renderInline(prop)), pretty.Text(": "),
			pretty.Text(renderInline(value)), pretty.Text(";"),
		)
	}
	var fill []pretty.Doc
	for i, g := range groups {
		if i > 0 {
			fill = append(fill, pretty.Text(","), pretty.Line)
		}
		fill = append(fill, pretty.Text(renderInline(g)))
	}
	return pretty.Concat(
		pretty.Text(renderInline(prop)), pretty.Text(": "),
		pretty.Group(pretty.Indent(pretty.Fill(fill...))), pretty.Text(";"),
	)
}

// renderInline collapses a token run to single-line text: each whitespace run
// becomes a single space (dropped at the edges), and every other token is
// emitted verbatim and adjacent. It injects NO spacing around ':' / ',' / '>'
// etc. — that is the SAFE minimal normalization: a pseudo-class like ".a:hover"
// or a functional ":is(a:hover)" must keep its colon attached, and there is no
// purely-structural way to tell a selector colon from a media-feature colon
// without parsing the prelude grammar. Declaration colon spacing ("prop: value")
// is added explicitly by layoutDecl, and selector / value comma-lists are joined
// by the Fill in layoutPrelude / layoutDecl — so renderInline never needs to
// inject a space itself. Comments are kept inline as ordinary tokens.
func renderInline(toks []token) string {
	var b strings.Builder
	for _, t := range toks {
		if t.kind == tWS {
			if b.Len() > 0 {
				b.WriteByte(' ')
			}
			continue
		}
		b.WriteString(t.text)
	}
	return strings.TrimRight(b.String(), " ")
}

// splitTopLevel splits toks on sep at paren depth 0, returning the groups
// (each trimmed of edge WS). A nested "(a, b)" is not split.
func splitTopLevel(toks []token, sep tokKind) [][]token {
	var groups [][]token
	depth := 0
	start := 0
	for i, t := range toks {
		switch t.kind {
		case tLParen:
			depth++
		case tRParen:
			if depth > 0 {
				depth--
			}
		case sep:
			if depth == 0 {
				groups = append(groups, trimWS(toks[start:i]))
				start = i + 1
			}
		}
	}
	groups = append(groups, trimWS(toks[start:]))
	return groups
}

// splitFirst splits toks at the first top-level occurrence of sep, returning
// (before, after, true). With no top-level sep it returns (toks, nil, false).
func splitFirst(toks []token, sep tokKind) (before, after []token, ok bool) {
	depth := 0
	for i, t := range toks {
		switch t.kind {
		case tLParen:
			depth++
		case tRParen:
			if depth > 0 {
				depth--
			}
		case sep:
			if depth == 0 {
				return trimWS(toks[:i]), trimWS(toks[i+1:]), true
			}
		}
	}
	return toks, nil, false
}
