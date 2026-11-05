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
	// RedundantSpaceLiteral: { " " } beside a line break that already renders
	// a space, so the two render two spaces.
	RedundantSpaceLiteral
)

// Finding is one whitespace lint result on an unnormalized (freshly parsed)
// tree. Pos/End is the reported range; Edits, applied together, fix it.
type Finding struct {
	Kind     FindingKind
	Pos, End token.Pos
	Punct    rune // the punctuation, for PunctuationLineBreak
	Edits    []Edit
}

// Edit replaces the source bytes [Pos, End) with New.
type Edit struct {
	Pos, End token.Pos
	New      string
}

// Message describes the finding.
func (f Finding) Message() string {
	if f.Kind == RedundantSpaceLiteral {
		return `{ " " } beside a line break renders two spaces`
	}
	return "line break renders a space " + punctSide(f.Punct) + " " + strconv.QuoteRune(f.Punct)
}

// Help says how to fix it.
func (f Finding) Help() string {
	if f.Kind == RedundantSpaceLiteral {
		return `remove { " " }: the line break already renders a space`
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
func Lint(f *ast.File) []Finding {
	var out []Finding
	w := walker{region: func(nodes []ast.Markup, start, end int) {
		scanRegion(nodes, start, end, func(*ast.Text, string) {}, func(r *Run) {
			if !r.Renders || !r.Newline {
				return
			}
			prevLit := r.Prev == "" && isSpaceLiteral(r.PrevNode)
			nextLit := r.Next == "" && isSpaceLiteral(r.NextNode)
			punct, isPunct := rune(0), false
			if p := firstRune(r.Next); r.Next != "" && strings.ContainsRune(closingPunct, p) {
				punct, isPunct = p, true
			} else if p := lastRune(r.Prev); r.Prev != "" && strings.ContainsRune(openingPunct, p) {
				punct, isPunct = p, true
			}
			switch {
			case isPunct && (prevLit || nextLit):
				// The literal is the author's explicit space beside the
				// punctuation; only the line break is extra.
				lit := r.PrevIndex
				if nextLit {
					lit = r.NextIndex
				}
				out = append(out, Finding{Kind: RedundantSpaceLiteral, Pos: nodes[lit].Pos(), End: nodes[lit].End(), Edits: deleteRun(r)})
			case isPunct:
				out = append(out, punctFinding(r, punct))
			case prevLit:
				out = append(out, spaceFinding(nodes, r, r.PrevIndex, -1))
			case nextLit:
				out = append(out, spaceFinding(nodes, r, r.NextIndex, 1))
			}
		})
	}}
	w.file(f)
	return out
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

func punctFinding(r *Run, p rune) Finding {
	pos, end := runRange(r)
	return Finding{Kind: PunctuationLineBreak, Pos: pos, End: end, Punct: p, Edits: deleteRun(r)}
}

// spaceFinding reports the { " " } at nodes[i] beside run r (dir is the
// literal's side of the run). Deleting the literal keeps the run's own space
// when another source sibling sits beyond the literal; at a list edge the
// run would then render nothing, so the run is deleted instead.
func spaceFinding(nodes []ast.Markup, r *Run, i, dir int) Finding {
	lit := nodes[i]
	f := Finding{Kind: RedundantSpaceLiteral, Pos: lit.Pos(), End: lit.End()}
	if hasSiblingBeyond(nodes, i, dir) {
		f.Edits = []Edit{{Pos: lit.Pos(), End: lit.End()}}
	} else {
		f.Edits = deleteRun(r)
	}
	return f
}

func hasSiblingBeyond(nodes []ast.Markup, i, dir int) bool {
	for j := i + dir; j >= 0 && j < len(nodes); j += dir {
		if IsSourceTransparent(nodes[j]) {
			continue
		}
		if t, ok := nodes[j].(*ast.Text); ok && strings.Trim(t.Value, " \t\r\n") == "" {
			continue
		}
		return sourceSibling(nodes, j)
	}
	return false
}

// isSpaceLiteral reports the { " " } idiom: an interpolation of a Go string
// literal whose value is exactly one ASCII space.
func isSpaceLiteral(n ast.Markup) bool {
	v, ok := n.(*ast.Interp)
	if !ok || len(v.Stages) != 0 || v.Embedded != nil {
		return false
	}
	s, err := strconv.Unquote(strings.TrimSpace(v.Expr))
	return err == nil && s == " "
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
