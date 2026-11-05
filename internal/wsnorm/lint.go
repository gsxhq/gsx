package wsnorm

import (
	"go/token"
	"html"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gsxhq/gsx/ast"
)

// FindingKind names a whitespace lint.
type FindingKind uint8

const (
	// PunctuationLineBreak: a line break renders a space before closing
	// punctuation or after opening punctuation. Usually layout the author (or
	// an earlier gsx fmt) never meant as a space.
	PunctuationLineBreak FindingKind = iota + 1
	// RedundantSpaceLiteral: a string literal with a space at its edge
	// ({ " " }, { " and " }) beside a line break that renders another.
	RedundantSpaceLiteral
)

// Finding is one whitespace lint result on an unnormalized (freshly parsed)
// tree. Pos/End is the reported range; Edits, applied together, fix it.
type Finding struct {
	Kind     FindingKind
	Pos, End token.Pos
	Punct    rune // the punctuation, for PunctuationLineBreak
	Edits    []Edit
	// Migrate reports that the pre-v0.5 rule rendered nothing for this run
	// (every part was a line break at a Text edge), so Edits restore exactly
	// the output that rule produced. `gsx fix whitespace` applies only these;
	// the others (a line break inside one text, which always rendered a space)
	// are still worth a look in the editor but would change existing output.
	Migrate bool
}

// Edit replaces the source bytes [Pos, End) with New.
type Edit struct {
	Pos, End token.Pos
	New      string
}

// Message describes the finding.
func (f Finding) Message() string {
	if f.Kind == RedundantSpaceLiteral {
		return "line break renders a second space beside this string's space"
	}
	return "line break renders a space " + punctSide(f.Punct) + " " + strconv.QuoteRune(f.Punct)
}

// Help says how to fix it.
func (f Finding) Help() string {
	if f.Kind == RedundantSpaceLiteral {
		return "join the line break: the string already supplies the space"
	}
	return "join the lines to keep the punctuation attached; write the space on the same line if it is intended"
}

// closingPunct takes no space before it; openingPunct takes none after it.
const (
	closingPunct = ".,;:!?)]’”%"
	openingPunct = "([“‘"
)

func punctSide(r rune) string {
	if strings.ContainsRune(openingPunct, r) {
		return "after"
	}
	return "before"
}

// Lint reports the whitespace findings of f, which must be unnormalized: Text
// values must still equal their source spans. It walks exactly the lists
// Normalize processes and judges each run with the same scanner, so a finding
// is always about whitespace Normalize renders.
//
// Both lints concern a layout run: one that renders a space although every
// part of it is a line break plus indentation. A part without a line break is
// a space the author wrote on the line, which is never flagged. A layout run
// facing { " " } is redundant-space-literal; one facing punctuation is
// punctuation-line-break. Either way the fix deletes the run, which keeps an
// author's { " " } and never cascades.
func Lint(f *ast.File) []Finding {
	var out []Finding
	w := walker{region: func(nodes []ast.Markup, start, end int) {
		scanRegion(nodes, start, end, func(*ast.Text, string) {}, func(r *Run) {
			if !r.Renders || !layoutRun(r) {
				return
			}
			finding := Finding{Edits: deleteRun(r), Migrate: oldRuleDropped(r)}
			finding.Pos, finding.End = runRange(r)
			var lit ast.Markup
			if r.Prev == "" {
				lit = facingLiteral(r.PrevNode, false)
			}
			if lit == nil && r.Next == "" {
				lit = facingLiteral(r.NextNode, true)
			}
			switch {
			case lit != nil:
				finding.Kind, finding.Pos, finding.End = RedundantSpaceLiteral, lit.Pos(), lit.End()
			case r.Next != "" && strings.ContainsRune(closingPunct, firstRune(r.Next)):
				finding.Kind, finding.Punct = PunctuationLineBreak, firstRune(r.Next)
			case r.Prev != "" && strings.ContainsRune(openingPunct, lastRune(r.Prev)):
				finding.Kind, finding.Punct = PunctuationLineBreak, lastRune(r.Prev)
			default:
				return
			}
			out = append(out, finding)
		})
	}}
	w.file(f)
	return out
}

// layoutRun reports that every part of r contains a line break.
func layoutRun(r *Run) bool {
	for _, p := range r.Parts {
		if !strings.ContainsAny(p.Text.Value[p.Start:p.End], "\n\r") {
			return false
		}
	}
	return true
}

// oldRuleDropped reports that the pre-v0.5 rule rendered nothing for the
// layout run r: it dropped a line-break run at a Text's edge (or a whole
// whitespace-only Text) and kept one inside a Text as a space.
func oldRuleDropped(r *Run) bool {
	for _, p := range r.Parts {
		if p.Start != 0 && p.End != len(p.Text.Value) {
			return false
		}
	}
	return true
}

func runRange(r *Run) (token.Pos, token.Pos) {
	first, last := r.Parts[0], r.Parts[len(r.Parts)-1]
	return first.Text.Pos() + token.Pos(first.Start), last.Text.Pos() + token.Pos(last.End)
}

func deleteRun(r *Run) []Edit {
	edits := make([]Edit, 0, len(r.Parts))
	for _, p := range r.Parts {
		edits = append(edits, Edit{Pos: p.Text.Pos() + token.Pos(p.Start), End: p.Text.Pos() + token.Pos(p.End)})
	}
	return edits
}

// facingLiteral returns the spaced literal that renders right at the boundary n
// presents to a run: n itself, or, when n is an if/for/switch block, a literal
// that is the first (leading) or last node of one of its bodies — the block's
// braces are syntax, so that literal touches the run whenever its branch
// renders. Branches are not evaluated: any body with such a literal counts.
func facingLiteral(n ast.Markup, leading bool) ast.Markup {
	if spacedLiteral(n, leading) {
		return n
	}
	var bodies [][]ast.Markup
	switch v := n.(type) {
	case *ast.IfMarkup:
		bodies = [][]ast.Markup{v.Then, v.Else}
	case *ast.ForMarkup:
		bodies = [][]ast.Markup{v.Body}
	case *ast.SwitchMarkup:
		for _, c := range v.Cases {
			bodies = append(bodies, c.Body)
		}
	}
	for _, body := range bodies {
		if lit := facingLiteral(bodyEdge(body, leading), leading); lit != nil {
			return lit
		}
	}
	return nil
}

// bodyEdge is the first (leading) or last node of body that is not
// source-transparent, or nil.
func bodyEdge(body []ast.Markup, leading bool) ast.Markup {
	for i := range body {
		j := i
		if !leading {
			j = len(body) - 1 - i
		}
		if !IsSourceTransparent(body[j]) {
			return body[j]
		}
	}
	return nil
}

// spacedLiteral reports an interpolation of a Go string literal whose value
// starts (leading) or ends with ASCII whitespace: { " " }, { " and " }. That
// space is data, so a line break beside it renders a second one.
func spacedLiteral(n ast.Markup, leading bool) bool {
	v, ok := n.(*ast.Interp)
	if !ok || len(v.Stages) != 0 || v.Embedded != nil {
		return false
	}
	s, err := strconv.Unquote(strings.TrimSpace(v.Expr))
	if err != nil || s == "" {
		return false
	}
	if leading {
		return isWS(s[0])
	}
	return isWS(s[len(s)-1])
}

// firstRune and lastRune return the rendered edge character of a source word,
// decoding a character reference at that edge.
func firstRune(word string) rune {
	if strings.HasPrefix(word, "&") {
		if k := strings.IndexByte(word, ';'); k > 1 {
			if dec := html.UnescapeString(word[:k+1]); dec != word[:k+1] {
				r, _ := utf8.DecodeRuneInString(dec)
				return r
			}
		}
	}
	r, _ := utf8.DecodeRuneInString(word)
	return r
}

func lastRune(word string) rune {
	if strings.HasSuffix(word, ";") {
		if k := strings.LastIndexByte(word, '&'); k >= 0 && k < len(word)-2 {
			if dec := html.UnescapeString(word[k:]); dec != word[k:] {
				r, _ := utf8.DecodeLastRuneInString(dec)
				return r
			}
		}
	}
	r, _ := utf8.DecodeLastRuneInString(word)
	return r
}
