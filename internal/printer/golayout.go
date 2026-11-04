package printer

import (
	"fmt"
	goast "go/ast"
	"go/format"
	goparser "go/parser"
	"go/scanner"
	"go/token"
	"sort"
	"strings"
	"unicode"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/goexprshape"
	"github.com/gsxhq/gsx/internal/pretty"
)

// goIsland records a replaceable Go syntax region, not a copy of markup to
// render. Its document is assembled from current GoText and semantic nodes.
type goIsland struct {
	start, end int
	expr, stmt bool
}
type goLayoutPart struct {
	start, end int
	part       ast.GoPart
}

// fmtSourceGoParts keeps the smallest Go syntax regions needed to hold fixed
// literal newlines at their authored columns. The rest remains ordinary Go.
func (p *printer) fmtSourceGoParts(parts []ast.GoPart, source, prefix, suffix string, strip func([]byte) (string, bool), origin ...token.Pos) (string, map[string]goValue, bool, error) {
	if source == "" || len(parts) == 0 {
		return "", nil, false, nil
	}
	constrained := false
	for _, part := range parts {
		constrained = constrained || p.anchoredGoMarkup(part)
	}
	if !constrained {
		return "", nil, false, nil
	}
	base := parts[0].Pos()
	var skeleton strings.Builder
	skeleton.WriteString(prefix)
	spans := make([]goLayoutPart, 0, len(parts))
	anchors := map[int]int{}
	for _, part := range parts {
		start := skeleton.Len()
		if gt, ok := part.(ast.GoText); ok {
			skeleton.WriteString(gt.Src)
		} else {
			a, b := int(part.Pos()-base), int(part.End()-base)
			if a < 0 || b > len(source) || b-a < 2 {
				return "", nil, false, fmt.Errorf("printer: cannot preserve embedded Go source layout")
			}
			raw := []byte(source[a:b])
			raw[0] = '`'
			raw[len(raw)-1] = '`'
			for i := 1; i < len(raw)-1; i++ {
				if raw[i] != '\n' && raw[i] != '\r' {
					raw[i] = ' '
				}
			}
			if p.anchoredGoMarkup(part) {
				anchors[start] = skeleton.Len() + len(raw)
			}
			skeleton.Write(raw)
		}
		spans = append(spans, goLayoutPart{start, skeleton.Len(), part})
	}
	if len(anchors) == 0 {
		return "", nil, false, nil
	}
	skeleton.WriteString(suffix)
	src := skeleton.String()
	fs := token.NewFileSet()
	var holes []goexprshape.Hole
	for _, span := range spans {
		if _, ok := span.part.(ast.GoText); !ok {
			holes = append(holes, goexprshape.Hole{Start: span.start, End: span.end})
		}
	}
	parserView := goexprshape.SanitizeGeometry(src, holes)
	tree, err := goparser.ParseFile(fs, "", parserView, goparser.ParseComments|goparser.SkipObjectResolution)
	if err != nil {
		return "", nil, false, fmt.Errorf("printer: cannot parse source-aligned embedded Go: %w", err)
	}
	file := fs.File(tree.Pos())
	file.SetLinesForContent([]byte(src))
	// go/scanner removes carriage returns from raw string token values. Restore
	// the exact token spelling in this position-only AST so End() still denotes
	// original byte offsets, including through ValueSpec and other ancestors.
	goast.Inspect(tree, func(n goast.Node) bool {
		lit, ok := n.(*goast.BasicLit)
		if !ok || lit.Kind != token.STRING || !strings.HasPrefix(lit.Value, "`") {
			return true
		}
		a := file.Offset(lit.Pos())
		end := strings.IndexByte(src[a+1:], '`')
		if end >= 0 {
			lit.Value = src[a : a+end+2]
		}
		return true
	})
	// The first real Go token on each physical line determines which ancestors
	// can keep the opening line in place. Comments are retained with their region.
	first := make(map[int]int)
	attached := map[int]int{}
	type commentTrivia struct{ start, endLine int }
	var pending []commentTrivia
	var scan scanner.Scanner
	scan.Init(file, []byte(src), nil, scanner.ScanComments)
	for {
		pos, tok, _ := scan.Scan()
		if tok == token.EOF {
			break
		}
		off := file.Offset(pos)
		if off < len(prefix) || off >= len(src)-len(suffix) {
			pending = nil
			continue
		}
		if tok == token.COMMENT {
			if strings.HasPrefix(src[off:], "/*") {
				close := strings.Index(src[off+2:], "*/")
				if close >= 0 {
					end := off + 2 + close + 2
					pending = append(pending, commentTrivia{off, file.Line(file.Pos(end - 1))})
				}
			}
			continue
		}
		if tok == token.SEMICOLON {
			continue
		}
		line := file.Line(pos)
		for _, comment := range pending {
			if comment.endLine == line {
				attached[off] = comment.start
				break
			}
		}
		pending = nil

		if _, ok := first[line]; !ok {
			first[line] = off
		}
	}

	selectionFailed := false
	var regions []goIsland
	var stack []goast.Node
	var starts, nearest []int
	goast.Inspect(tree, func(n goast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			starts = starts[:len(starts)-1]
			nearest = nearest[:len(nearest)-1]
			return true
		}
		at := file.Offset(n.Pos())
		candidate := len(stack)
		_, valid := replaceableGoIsland(n)
		if !valid || at < len(prefix) || file.Offset(n.End()) > len(src)-len(suffix) {
			candidate = -1
			if len(nearest) > 0 {
				candidate = nearest[len(nearest)-1]
			}
		}
		stack = append(stack, n)
		starts = append(starts, at)
		nearest = append(nearest, candidate)
		lit, ok := n.(*goast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		operandEnd, anchored := anchors[at]
		if !anchored {
			return true
		}
		opening := max(first[file.Line(lit.Pos())], len(prefix))
		// A standalone parenthesized operand is flattened by gofmt. Include its
		// authored parens and the tokens on their opening line in the constraint.
		for j := len(stack) - 2; j >= 0; j-- {
			paren, ok := stack[j].(*goast.ParenExpr)
			if !ok {
				break
			}
			opening = min(opening, max(first[file.Line(paren.Pos())], len(prefix)))
		}
		index := sort.Search(len(starts), func(i int) bool { return starts[i] > opening }) - 1
		if index < 0 || nearest[index] < 0 {
			selectionFailed = true
			return true
		}
		node := stack[nearest[index]]
		r, _ := replaceableGoIsland(node)
		r.start = file.Offset(node.Pos())
		if leading, ok := attached[r.start]; ok {
			r.start = leading
		}
		r.end = max(file.Offset(node.End()), operandEnd)
		// AST ancestors form nested or disjoint intervals. Coalesce in one pass.
		for len(regions) > 0 && r.start <= regions[len(regions)-1].start && r.end >= regions[len(regions)-1].end {
			regions = regions[:len(regions)-1]
		}
		if len(regions) > 0 && regions[len(regions)-1].end >= r.end {
			return true
		}
		regions = append(regions, r)
		return true
	})
	if selectionFailed || len(regions) == 0 {
		return "", nil, false, fmt.Errorf("printer: cannot preserve embedded Go source layout")
	}
	merged := regions
	values := map[string]goValue{}
	used := map[rune]bool{}
	for _, r := range source {
		used[r] = true
	}
	for _, r := range src {
		used[r] = true
	}
	nextRune := rune(0x4e00)
	marker := func() (string, error) {
		for nextRune <= unicode.MaxRune && (!unicode.IsLetter(nextRune) || used[nextRune]) {
			nextRune++
		}
		if nextRune > unicode.MaxRune {
			return "", fmt.Errorf("printer: source-island marker alphabet exhausted")
		}
		m := string(nextRune)
		used[nextRune] = true
		nextRune++
		return m, nil
	}
	type edit struct {
		start, end  int
		replacement string
	}
	var edits []edit
	// Protected regions render from actual GoText and markup docs. The snapshot
	// supplied only their physical newline geometry to the parser above.
	spanIndex := 0
	for _, r := range merged {
		var docs []pretty.Doc
		pos := r.start
		for spanIndex < len(spans) && spans[spanIndex].end <= r.start {
			spanIndex++
		}
		for j := spanIndex; j < len(spans) && spans[j].start < r.end; j++ {
			s := spans[j]
			a, b := max(r.start, s.start), min(r.end, s.end)
			if text, ok := s.part.(ast.GoText); ok {
				docs = append(docs, pretty.Text(text.Src[a-s.start:b-s.start]))
			} else {
				if a != s.start || b != s.end {
					return "", nil, false, fmt.Errorf("printer: cannot preserve embedded Go source layout")
				}
				doc, ok := p.goExprValue(s.part)
				if !ok {
					return "", nil, false, fmt.Errorf("printer: cannot preserve embedded Go source layout")
				}
				line := src[:s.start]
				if i := strings.LastIndexByte(line, '\n'); i >= 0 {
					line = line[i+1:]
				}
				docs = append(docs, indentN(realTabDepth(line), doc))
			}
			pos = b
		}
		if pos != r.end {
			return "", nil, false, fmt.Errorf("printer: cannot preserve embedded Go source layout")
		}
		lineStart := strings.LastIndexByte(src[:r.start], '\n') + 1
		leading := src[lineStart:r.start]
		if lineStart <= len(prefix) && len(origin) > 0 {
			sourcePrefix := p.sourceIndent(origin[0])
			if p.originIndent != nil {
				sourcePrefix = *p.originIndent
			}
			leading = sourcePrefix + src[len(prefix):r.start]
		}
		if strings.TrimSpace(leading) != "" {
			leading = ""
		}
		replacement, err := marker()
		if err != nil {
			return "", nil, false, err
		}
		if r.stmt {
			replacement += "()"
		} else if !r.expr {
			replacement = "var " + replacement + " = 0"
		}
		values[replacement] = goValue{doc: pretty.Concat(docs...), sourceAligned: true, sourceIndent: leading}
		edits = append(edits, edit{r.start, r.end, replacement})
	}
	for _, s := range spans {
		if _, ok := s.part.(ast.GoText); ok {
			continue
		}
		i := sort.Search(len(merged), func(i int) bool { return merged[i].end > s.start })
		if i < len(merged) && merged[i].start <= s.start {
			continue
		}
		doc, ok := p.goExprValue(s.part)
		if !ok {
			return "", nil, false, fmt.Errorf("printer: cannot preserve embedded Go source layout")
		}
		flat, isFlat := goExprFlatText(doc)
		replacement, err := marker()
		if err != nil {
			return "", nil, false, err
		}
		if isFlat {
			replacement = strings.Repeat(replacement, max(1, len([]rune(flat))))
		}
		values[replacement] = goValue{doc: doc, flat: flat, isFlat: isFlat}
		edits = append(edits, edit{s.start, s.end, replacement})
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
	var replaced strings.Builder
	var replacementHoles []goexprshape.Hole
	cursor := 0
	for _, e := range edits {
		replaced.WriteString(src[cursor:e.start])
		start := replaced.Len()
		replaced.WriteString(e.replacement)
		replacementHoles = append(replacementHoles, goexprshape.Hole{Start: start, End: replaced.Len()})
		cursor = e.end
	}
	replaced.WriteString(src[cursor:])
	prepared, _ := goexprshape.Sanitize(replaced.String(), replacementHoles)
	out, err := format.Source([]byte(blockFormBraces(breakWideLiterals(prepared, p.width, p.tabWidth, ""))))
	if err != nil {
		return "", nil, false, fmt.Errorf("printer: cannot format source-aligned embedded Go: %w", err)
	}
	text, ok := strip(out)
	if !ok {
		return "", nil, false, fmt.Errorf("printer: cannot preserve embedded Go source layout")
	}
	return text, values, true, nil
}

// sourceIndent masks syntax before a field into equivalent source columns.
// Tabs remain tabs so the configured tab width has the same interpretation.
func (p *printer) sourceIndent(pos token.Pos) string {
	prefix, ok := p.source.LinePrefix(pos)
	if !ok {
		return ""
	}
	var b strings.Builder
	for _, r := range prefix {
		if r == '\t' {
			b.WriteByte('\t')
		} else {
			b.WriteByte(' ')
		}
	}
	return b.String()
}

// Embedded JS/CSS literals have separate formatting contracts. Only markup
// operands participate in fixed HTML source layout.
func (p *printer) anchoredGoMarkup(part ast.GoPart) bool {
	switch part.(type) {
	case *ast.Element, *ast.Fragment:
		return p.anchoredNode(part)
	case ast.GoText, *ast.EmbeddedInterp:
		return false
	}
	return false
}

// firstLineGoAnchor reports a fixed markup operand beginning on the field's
// original first line. Its GSX delimiter must stay attached to that line.
func (p *printer) firstLineGoAnchor(src string) (anchored bool) {
	if found, ok := p.firstLineAnchors[src]; ok {
		return found
	}
	if p.firstLineAnchors == nil {
		p.firstLineAnchors = map[string]bool{}
	}
	defer func() { p.firstLineAnchors[src] = anchored }()
	if !mayHoldGoValue(src) {
		return false
	}
	parts, ok := splitGoParts(src)
	if !ok || len(parts) == 0 {
		return false
	}
	firstEnd := strings.IndexByte(src, '\n')
	if firstEnd < 0 {
		firstEnd = len(src)
	}
	base := parts[0].Pos()
	for _, part := range parts {
		if int(part.Pos()-base) >= firstEnd {
			return false
		}
		if p.anchoredGoMarkup(part) {
			return true
		}
	}
	return false
}

func sourceLeading(src string) string { return src[:len(src)-len(strings.TrimLeft(src, " \t\r\n"))] }

func (p *printer) anchoredAttrOpening(e *ast.Element) bool {
	for _, a := range e.Attrs {
		if p.anchoredNode(a) {
			return true
		}
	}
	return false
}

// Case/communication clauses and function bodies require their enclosing
// construct: an expression statement cannot replace their grammar category.
func replaceableGoIsland(n goast.Node) (goIsland, bool) {
	switch n.(type) {
	case *goast.BlockStmt, *goast.CaseClause, *goast.CommClause:
		return goIsland{}, false
	case goast.Expr:
		return goIsland{expr: true}, true
	case goast.Stmt:
		return goIsland{stmt: true}, true
	case goast.Decl:
		return goIsland{}, true
	}
	return goIsland{}, false
}

func (p *printer) inlineGoAttr(v *ast.ExprAttr) bool {
	before, ok := p.source.Slice(v.Pos(), v.ExprPos)
	return ok && !strings.ContainsAny(before, "\r\n") && p.firstLineGoAnchor(v.Expr)
}
