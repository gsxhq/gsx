package lsp

import (
	"go/token"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
)

// completionContextKind names the cursor-context category the repaired parse
// places the cursor in. Handlers (Task 9-15) branch on it.
type completionContextKind int

const (
	ctxNone      completionContextKind = iota // markup text, js/css bodies, import strings — v1 offers nothing
	ctxGoExpr                                 // Interp/ExprAttr/SpreadAttr/ClassPart/OrderedPair/value-form/ctrl-clause/GoBlock/GoChunk/@{} holes
	ctxPipeStage                              // after |> inside a pipeline
	ctxTag                                    // tag-name region after <
	ctxAttrName                               // inside an open tag, attribute-name position
	ctxAttrValue                              // inside a StaticAttr string value
	ctxSigType                                // inside a component signature params region
)

// completionContext is the classification of the cursor over a repaired parse.
// Only the fields relevant to kind are populated; the rest are zero.
type completionContext struct {
	kind      completionContextKind
	node      gsxast.Node     // the matched node (Interp, Element, PipeStage owner, GoChunk, Component, ...)
	exprPos   token.Pos       // Go contexts: the matched fragment's start (skeleton bridge anchor)
	element   *gsxast.Element // tag/attr-name/attr-value contexts: the enclosing element
	attr      gsxast.Attr     // attr-value context: the StaticAttr under the cursor
	qualifier string          // tag context: "pkg" when completing <pkg.▮; "" otherwise
	phantom   bool            // a repair phantom sits at the cursor (`_` pipe stage, the injected `""` attr value, or the injected `_` tag/member name)
}

// spanContainsForCompletion reports whether off lies within [start, start+length]
// — INCLUSIVE of the end, because a completion cursor sits AFTER the last typed
// character. (Contrast exprNodeAtOffset's half-open [start, start+length) used
// for hover/definition, where the cursor sits ON a character.)
func spanContainsForCompletion(start, length, off int) bool {
	return off >= start && off <= start+length
}

// classifyCompletionContext locates the innermost construct containing off in
// r.parsed and maps it to a completionContext. off is in ORIGINAL buffer
// coordinates, which equal patched coordinates because every repair patch is
// inserted AT off (bytes before off are never moved). Positions in r.parsed
// resolve against r.fset. An unrepairable buffer (r.parsed == nil) is ctxNone.
//
// Rules are applied in priority order (first match wins): pipe stage → Go expr
// → tag → attr name → attr value → sig type → ctxNone. The order matters only
// where regions nest: an ExprAttr's Go expression sits inside the open-tag
// region (Go beats attr-name), and a StaticAttr value also sits there (so the
// attr-name rule explicitly excludes value spans, since attr-name outranks
// attr-value by number yet a cursor in a value must classify as attr-value).
func classifyCompletionContext(r repairResult, path string, off int) completionContext {
	if r.parsed == nil {
		return completionContext{kind: ctxNone}
	}
	fset := r.fset
	posOff := func(p token.Pos) int { return fset.Position(p).Offset }

	// phantomStage: the `_` patch healed an empty `|> ` stage — the `_` at the
	// cursor is a repair token, not authored. phantomValue: the `""/>` patch
	// healed a dangling `attr=` — the empty string value is injected, not
	// typed. phantomTag: the `_/>` patch healed a bare `<▮` or a qualified
	// `<pkg.▮` with nothing after the dot — the `_` standing in for the
	// tag/member name is injected, not typed.
	phantomStage := r.patch == "_"
	phantomValue := r.patch == "\"\"/>"
	phantomTag := r.patch == "_/>"

	var pipeCtx, goCtx, tagCtx, nameCtx, valueCtx, sigCtx *completionContext

	// innerEl is the innermost element (smallest span) whose [Pos, End] contains
	// off; it is the enclosing element for tag/attr-name/attr-value contexts and
	// the subject of the whitespace attr-name rule.
	var innerEl *gsxast.Element
	innerElSpan := 1 << 30

	inspectWithEmbedded(r.parsed, func(n gsxast.Node) bool {
		if n == nil {
			return false
		}

		// Track the enclosing element.
		if el, ok := n.(*gsxast.Element); ok {
			s, e := posOff(el.Pos()), posOff(el.End())
			if off >= s && off <= e && e-s < innerElSpan {
				innerEl = el
				innerElSpan = e - s
			}
		}

		// Rule 6: signature type — cursor inside a Component's params region.
		if c, ok := n.(*gsxast.Component); ok && c.ParamsPos.IsValid() && c.Params != "" {
			if spanContainsForCompletion(posOff(c.ParamsPos), len(c.Params), off) {
				sigCtx = &completionContext{kind: ctxSigType, node: n, exprPos: c.ParamsPos}
			}
		}

		// Rule 2 (GoChunk): a verbatim top-level Go span — its skeleton bridge is
		// the source index (Task 9). GoChunk is not in nodeNavSpans.
		if gc, ok := n.(*gsxast.GoChunk); ok {
			if spanContainsForCompletion(posOff(gc.Pos()), len(gc.Src), off) {
				goCtx = &completionContext{kind: ctxGoExpr, node: n, exprPos: gc.Pos()}
			}
		}

		// Rule 2 (empty GoBlock): a `{{ }}` whose Code is empty has a zero-width
		// nav span anchored at the closing brace (Code is "" and CodePos sits at
		// `}}`), so the nodeNavSpans loop below cannot place a cursor sitting in
		// the brace interior. Classify that interior as a Go statement context
		// anchored at CodePos. Guarded to genuinely-empty blocks (no embedded
		// literal, no unsupported markup) so a GoBlock carrying elements still
		// routes its inner tags/attrs through the normal rules.
		if gb, ok := n.(*gsxast.GoBlock); ok && gb.Code == "" && gb.Embedded == nil && gb.UnsupportedMarkup == nil && gb.CodePos.IsValid() {
			inner0 := posOff(gb.Pos()) + len("{{")
			inner1 := posOff(gb.End()) - len("}}")
			if off >= inner0 && off <= inner1 {
				goCtx = &completionContext{kind: ctxGoExpr, node: n, exprPos: gb.CodePos}
			}
		}

		// Rule 3 (tag) + Rule 4 (BoolAttr name): element-level.
		if el, ok := n.(*gsxast.Element); ok {
			if el.TagPos.IsValid() && spanContainsForCompletion(posOff(el.TagPos), len(el.Tag), off) {
				// phantom is recorded here for classification symmetry with the
				// other ctx*/phantom* pairs above (pipe stage, attr value); it is
				// currently unread by tagCompletion, which serves the same
				// candidate list whether or not the tag token is repair-injected.
				tagCtx = &completionContext{kind: ctxTag, node: el, element: el, phantom: phantomTag}
				if i := strings.Index(el.Tag, "."); i >= 0 && off > posOff(el.TagPos)+i {
					tagCtx.qualifier = el.Tag[:i]
				}
			}
		}
		if ba, ok := n.(*gsxast.BoolAttr); ok {
			if spanContainsForCompletion(posOff(ba.Pos()), len(ba.Name), off) {
				nameCtx = &completionContext{kind: ctxAttrName, node: ba}
			}
		}

		// Rule 5 (attr value): cursor inside a StaticAttr string value. The value
		// span is derived from the attr span: closing quote = End-1, opening quote
		// = valueEnd-len(Value)-1 (verified against parsed examples). For the
		// phantom `""/>` repair the cursor sits at the OPENING quote (one before
		// valueStart), so match that offset too and flag it phantom.
		if sa, ok := n.(*gsxast.StaticAttr); ok {
			valueEnd := posOff(sa.End()) - 1
			valueStart := valueEnd - len(sa.Value)
			switch {
			case off >= valueStart && off <= valueEnd:
				valueCtx = &completionContext{kind: ctxAttrValue, node: sa, attr: sa,
					phantom: phantomValue && sa.Value == ""}
			case phantomValue && sa.Value == "" && off == valueStart-1:
				valueCtx = &completionContext{kind: ctxAttrValue, node: sa, attr: sa, phantom: true}
			}
		}

		// Rules 1 (pipe stage) + 2 (Go expr): nodeNavSpans-carrying nodes.
		spans, stages := nodeNavSpans(n)
		if len(spans) > 0 {
			seed := spans[0].pos
			for _, st := range stages {
				if st.NamePos.IsValid() && spanContainsForCompletion(posOff(st.NamePos), len(st.Name), off) {
					pipeCtx = &completionContext{kind: ctxPipeStage, node: n, exprPos: seed, phantom: phantomStage}
				}
				if st.HasArgs && st.ArgsPos.IsValid() && spanContainsForCompletion(posOff(st.ArgsPos), len(st.Args), off) {
					// Cursor inside a stage's argument list is a Go-expression context.
					goCtx = &completionContext{kind: ctxGoExpr, node: n, exprPos: st.ArgsPos}
				}
			}
			for _, s := range spans {
				if s.pos.IsValid() && spanContainsForCompletion(posOff(s.pos), s.ln, off) {
					goCtx = &completionContext{kind: ctxGoExpr, node: n, exprPos: s.pos}
				}
			}
		}
		return true
	})

	// Rule 4 (whitespace attr-name): cursor inside the enclosing element's open
	// tag, after the tag name, before the tag's closing `>`/`/>`, and not inside
	// any StaticAttr value span. Only when no BoolAttr name already matched. A
	// cursor in an ExprAttr expression is caught by the higher-priority Go rule
	// below, so it need not be excluded here.
	//
	// openEnd is the byte offset of the open tag's own `>` (openTagEnd scans the
	// source), NOT innerEl.End(): an empty element `<div>▮</div>` has no children,
	// so an End()-based bound would stretch the attr-name region across the whole
	// (childless) body and misclassify a body cursor as an attribute name.
	//
	// The upper bound is INCLUSIVE (off <= openEnd): a completion cursor sits in
	// the gap BEFORE its byte offset, so a cursor tucked right against the closing
	// bracket (`<div ▮>`, `<div ▮></div>`, `<div ▮/>`) has off == openEnd yet still
	// wants attribute completion. A body cursor `<div>▮</div>` has off == openEnd+1
	// (one past the `>`) and is correctly excluded.
	if nameCtx == nil && innerEl != nil {
		tagEnd := posOff(innerEl.TagPos) + len(innerEl.Tag)
		openEnd := openTagEnd(r.src, tagEnd)
		if off > tagEnd && off <= openEnd && !offInStaticValue(innerEl, off, posOff) {
			nameCtx = &completionContext{kind: ctxAttrName, node: innerEl, element: innerEl}
		}
	}

	// Fill the enclosing element for element-anchored contexts.
	if nameCtx != nil && nameCtx.element == nil {
		nameCtx.element = innerEl
	}
	if valueCtx != nil {
		valueCtx.element = innerEl
	}

	// Resolve by priority; first non-nil wins.
	for _, c := range []*completionContext{pipeCtx, goCtx, tagCtx, nameCtx, valueCtx, sigCtx} {
		if c != nil {
			return *c
		}
	}
	return completionContext{kind: ctxNone}
}

// openTagEnd returns the byte offset, in src, of the `>` that closes an open
// tag whose name ends at from — the first '>' reached scanning forward while
// respecting the regions where a '>' is not a tag terminator: HTML single/
// double-quoted attribute values, and braced {expr} values (whose interior Go
// string/rune/raw-string literals are skipped so a '>' or '}' inside them does
// not end the tag or unbalance the brace count). Deterministic tokenization,
// consistent with the repo's blob-scan rules. When no closing '>' exists (an
// unclosed open tag mid-edit) it returns len(src), leaving the rule's upper
// bound open rather than fabricating one.
func openTagEnd(src []byte, from int) int {
	i := max(from, 0)
	for i < len(src) {
		switch src[i] {
		case '>':
			return i
		case '"', '\'':
			// HTML-quoted attribute value: terminated by the matching quote.
			q := src[i]
			i++
			for i < len(src) && src[i] != q {
				i++
			}
			if i < len(src) {
				i++ // consume the closing quote
			}
		case '{':
			i = skipBraced(src, i)
		default:
			i++
		}
	}
	return len(src)
}

// skipBraced returns the byte offset just past the balanced {expr} value that
// begins at src[i] (src[i] is the opening '{'). Nested braces are balanced and
// interior Go string/rune/raw-string literals and `//`/`/* */` comments are
// skipped so a '}' inside a literal or comment does not close the value early.
// Runs to len(src) on an unbalanced value.
func skipBraced(src []byte, i int) int {
	depth := 0
	for i < len(src) {
		switch src[i] {
		case '{':
			depth++
			i++
		case '}':
			depth--
			i++
			if depth == 0 {
				return i
			}
		case '"', '\'', '`':
			i = skipGoString(src, i)
		case '/':
			switch {
			case i+1 < len(src) && src[i+1] == '/':
				i = skipLineComment(src, i)
			case i+1 < len(src) && src[i+1] == '*':
				i = skipBlockComment(src, i)
			default:
				i++
			}
		default:
			i++
		}
	}
	return i
}

// skipLineComment returns the byte offset just past the `//` line comment
// that begins at src[i] (src[i] is the first '/') — the offset of the
// terminating newline, or len(src) if the comment runs to EOF.
func skipLineComment(src []byte, i int) int {
	for i < len(src) && src[i] != '\n' {
		i++
	}
	return i
}

// skipBlockComment returns the byte offset just past the `/* … */` comment
// that begins at src[i] (src[i] is the '/'). Runs to len(src) on an
// unterminated comment.
func skipBlockComment(src []byte, i int) int {
	i += 2 // consume "/*"
	for i < len(src) {
		if src[i] == '*' && i+1 < len(src) && src[i+1] == '/' {
			return i + 2
		}
		i++
	}
	return len(src)
}

// skipGoString returns the byte offset just past the Go string, rune, or raw
// string literal that begins at src[i] (src[i] is the opening quote). Raw
// strings (backtick) take no escapes; interpreted strings and runes honor a
// backslash escape. Runs to len(src) on an unterminated literal.
func skipGoString(src []byte, i int) int {
	q := src[i]
	i++
	for i < len(src) {
		if q != '`' && src[i] == '\\' {
			i += 2
			continue
		}
		if src[i] == q {
			return i + 1
		}
		i++
	}
	return i
}

// offInStaticValue reports whether off falls within any StaticAttr value span of
// el (inclusive), so the whitespace attr-name rule can decline a value position.
func offInStaticValue(el *gsxast.Element, off int, posOff func(token.Pos) int) bool {
	for _, a := range el.Attrs {
		sa, ok := a.(*gsxast.StaticAttr)
		if !ok {
			continue
		}
		valueEnd := posOff(sa.End()) - 1
		valueStart := valueEnd - len(sa.Value)
		if off >= valueStart-1 && off <= valueEnd {
			return true
		}
	}
	return false
}
