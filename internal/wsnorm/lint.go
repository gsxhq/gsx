package wsnorm

import (
	"go/token"
	"html"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gsxhq/gsx/ast"
)

// FindingKind names a whitespace lint.
type FindingKind uint8

const (
	// PunctuationSpace: whitespace renders a space before closing punctuation
	// or after opening punctuation, whether it is a line break or a same-line
	// space (often one an earlier gsx fmt joined onto the line).
	PunctuationSpace FindingKind = iota + 1
	// RedundantSpaceLiteral: a string literal with a space at its edge
	// ({ " " }, { " and " }) beside whitespace that renders another.
	RedundantSpaceLiteral
)

// Finding is one whitespace lint result on an unnormalized (freshly parsed)
// tree. Pos/End is the reported range; Edits, applied together, fix it.
type Finding struct {
	Kind     FindingKind
	Pos, End token.Pos
	Punct    rune // the punctuation, for PunctuationSpace
	Edits    []Edit
	// Migrate reports that the pre-v0.5 rule rendered nothing for this run
	// (every part was a line break at a Text edge), so Edits restore exactly
	// the output that rule produced. `gsx fix whitespace` applies only these
	// by default; the others (a same-line space, or a line break inside one
	// text, both of which always rendered) change existing output and are
	// applied by `gsx fix whitespace -all` or an editor quick fix.
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
		return "a second space renders beside this string's space"
	}
	return "a space renders " + punctSide(f.Punct) + " " + strconv.QuoteRune(f.Punct)
}

// Help says how to fix it.
func (f Finding) Help() string {
	if f.Kind == RedundantSpaceLiteral {
		return "remove the whitespace: the string already supplies the space"
	}
	return "remove the whitespace to keep the punctuation attached"
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
// Both lints concern a run that renders a space: facing a string literal that
// already has a space on that side it is redundant-space-literal; facing
// closing (or after opening) punctuation it is punctuation-space. Either way
// the fix deletes the run, which keeps an author's { " " } and never cascades.
func Lint(f *ast.File) []Finding {
	var out []Finding
	w := walker{region: func(nodes []ast.Markup, start, end int) {
		scanRegion(nodes, start, end, func(*ast.Text, string) {}, func(r *Run) {
			if !r.Renders {
				return
			}
			finding := Finding{Edits: deleteRun(r), Migrate: layoutRun(r) && oldRuleDropped(r) && gapAgrees(nodes, r)}
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
			case closesAfter(r):
				finding.Kind, finding.Punct = PunctuationSpace, firstRune(r.Next)
			case r.Prev != "" && strings.ContainsRune(openingPunct, lastRune(r.Prev)):
				finding.Kind, finding.Punct = PunctuationSpace, lastRune(r.Prev)
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

// gapAgrees reports that the rest of the gap around r, out to the content on
// each side, renders the same under the v0.4 rule as now, so deleting r (which
// v0.4 dropped) restores v0.4's output for the whole gap. The gap extends past
// whitespace-only literals and bare comments and into the facing edge of an
// element or of every body of a block.
func gapAgrees(nodes []ast.Markup, r *Run) bool {
	return (r.Prev != "" || edgeAgrees(nodes, r.PrevIndex, -1)) &&
		(r.Next != "" || edgeAgrees(nodes, r.NextIndex, 1))
}

// edgeAgrees walks nodes from i in direction dir, away from a run, up to the
// first content, and reports that the whitespace on the way renders the same
// under both rules. A children-list edge ends the walk.
func edgeAgrees(nodes []ast.Markup, i, dir int) bool {
	for i >= 0 && i < len(nodes) {
		switch v := nodes[i].(type) {
		case *ast.Comment:
			if v.Bare {
				i += dir
				continue
			}
		case *ast.Interp:
			if s, ok := literalValue(v); ok && strings.Trim(s, " \t\r\n") == "" {
				i += dir // data under both rules; the gap goes on
				continue
			}
			return true
		case *ast.Element:
			if IsPreserveTag(v.Tag) {
				return true
			}
			return listEdgeAgrees(v.Children, dir)
		case *ast.Fragment:
			return listEdgeAgrees(v.Children, dir)
		case *ast.MarkerRegion:
			return listEdgeAgrees(v.Children, dir)
		case *ast.IfMarkup:
			return listEdgeAgrees(v.Then, dir) && listEdgeAgrees(v.Else, dir)
		case *ast.ForMarkup:
			return listEdgeAgrees(v.Body, dir)
		case *ast.SwitchMarkup:
			for _, c := range v.Cases {
				if !listEdgeAgrees(c.Body, dir) {
					return false
				}
			}
			return true
		}
		if !isTextRegionNode(nodes[i]) {
			return true
		}
		start, end := i, i+1
		for start > 0 && isTextRegionNode(nodes[start-1]) {
			start--
		}
		for end < len(nodes) && isTextRegionNode(nodes[end]) {
			end++
		}
		agrees, content := regionEdgeAgrees(nodes, start, end, dir)
		if !agrees || content {
			return agrees
		}
		if dir < 0 {
			i = start - 1
		} else {
			i = end
		}
	}
	return true
}

func listEdgeAgrees(nodes []ast.Markup, dir int) bool {
	if dir < 0 {
		return edgeAgrees(nodes, len(nodes)-1, dir)
	}
	return edgeAgrees(nodes, 0, dir)
}

// regionEdgeAgrees compares the run at the edge of nodes[start:end] that faces
// the walk (its trailing run when dir < 0, its leading run otherwise): v0.4
// rendered one space per part without a line break (each part is the edge of
// its own Text), the current rule at most one. content reports a word in the
// region, which ends the walk.
func regionEdgeAgrees(nodes []ast.Markup, start, end, dir int) (agrees, content bool) {
	agrees = true
	scanRegion(nodes, start, end, func(*ast.Text, string) { content = true }, func(r *Run) {
		if dir < 0 && r.Next != "" || dir > 0 && r.Prev != "" {
			return
		}
		old := 0
		for _, p := range r.Parts {
			if !strings.ContainsAny(p.Text.Value[p.Start:p.End], "\n\r") {
				old++
			}
		}
		now := 0
		if r.Renders {
			now = 1
		}
		// A run v0.4 dropped that now renders and faces a spaced literal is
		// itself a finding whose fix deletes it, leaving v0.4's zero.
		fixedToo := old == 0 && now == 1 && layoutRun(r) && oldRuleDropped(r) &&
			(dir < 0 && r.Next == "" && facingLiteral(r.NextNode, true) != nil ||
				dir > 0 && r.Prev == "" && facingLiteral(r.PrevNode, false) != nil)
		agrees = old == now || fixedToo
	})
	return agrees, content
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
	if !ok {
		return false
	}
	s, ok := literalValue(v)
	if !ok || s == "" {
		return false
	}
	if leading {
		return isWS(s[0])
	}
	return isWS(s[len(s)-1])
}

// literalValue is the value of an interpolation that is a plain Go string
// literal.
func literalValue(v *ast.Interp) (string, bool) {
	if len(v.Stages) != 0 || v.Embedded != nil {
		return "", false
	}
	s, err := strconv.Unquote(strings.TrimSpace(v.Expr))
	return s, err == nil
}

// tokenStarts are closing marks that also begin a token when a letter or digit
// follows them: `.xlsx`, `.5`, `.env`, `:root`. (An apostrophe followed by a
// letter, as in `’s`, still attaches to what precedes it.)
const tokenStarts = ".:"

// closesAfter reports that the word after r starts with closing punctuation
// that attaches to what precedes r. A `%` attaches to a quantity: a number,
// or a sibling such as `{ v }`, not a word or comma (`below, %` labels a unit).
func closesAfter(r *Run) bool {
	if r.Next == "" {
		return false
	}
	p, rest := leadingRune(r.Next)
	if !strings.ContainsRune(closingPunct, p) {
		return false
	}
	if next, _ := utf8.DecodeRuneInString(rest); strings.ContainsRune(tokenStarts, p) && (unicode.IsLetter(next) || unicode.IsDigit(next)) {
		return false
	}
	if p == '%' {
		if r.Prev != "" {
			return unicode.IsDigit(lastRune(r.Prev))
		}
		return r.PrevNode != nil
	}
	return true
}

// leadingRune is firstRune with the rest of word after that character.
func leadingRune(word string) (rune, string) {
	if strings.HasPrefix(word, "&") {
		if k := strings.IndexByte(word, ';'); k > 1 {
			if dec := html.UnescapeString(word[:k+1]); dec != word[:k+1] {
				r, _ := utf8.DecodeRuneInString(dec)
				return r, word[k+1:]
			}
		}
	}
	r, size := utf8.DecodeRuneInString(word)
	return r, word[size:]
}

// firstRune and lastRune return the rendered edge character of a source word,
// decoding a character reference at that edge.
func firstRune(word string) rune {
	r, _ := leadingRune(word)
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
