package jsx

import "github.com/tdewolff/parse/v2/js"

// regexPosition reports whether a DivToken following the given previous
// significant token should be re-lexed as a regex literal rather than division.
//
// Copied verbatim from internal/jsmin/jsmin.go (same tdewolff-lexer regex-vs-
// division disambiguation); jsx needs it to drive l.RegExp() so a hole inside a
// regex literal classifies correctly. Kept local rather than factored shared to
// avoid jsx depending on jsmin (jsmin is the minifier, jsx the context engine).
func regexPosition(prev js.TokenType) bool {
	switch {
	case prev == js.ErrorToken: // start of input
		return true
	case js.IsIdentifier(prev): // plain identifier → division (a / b)
		return false
	case js.IsNumeric(prev): // number → division (1 / 2)
		return false
	}
	switch prev {
	// Tokens that end an expression → division.
	case js.CloseParenToken,
		js.CloseBracketToken,
		js.CloseBraceToken,
		js.IncrToken,
		js.DecrToken,
		js.RegExpToken,
		js.StringToken,
		js.TemplateToken,
		js.TemplateEndToken,
		js.TrueToken,
		js.FalseToken,
		js.NullToken,
		js.ThisToken,
		js.SuperToken:
		return false
	default:
		return true
	}
}

// isSignificant reports whether a token type participates in expression-context
// tracking. Whitespace, line terminators and comments are skipped so prevSig
// always names the previous code-bearing token.
func isSignificant(tt js.TokenType) bool {
	switch tt {
	case js.WhitespaceToken, js.LineTerminatorToken,
		js.CommentToken, js.CommentLineTerminatorToken:
		return false
	}
	return true
}

// isValuePosition reports whether a hole that lexes as its OWN identifier token,
// preceded by significant token prevSig, sits in a JavaScript value/expression
// position (→ JSCtxValue). It is the security-critical allow-list: only
// unambiguous expression-introducing tokens qualify; everything else fails
// closed (binding/name/statement/value-ending position).
func isValuePosition(prevSig js.TokenType) bool {
	// Operators introduce a value to their right — except ++ / -- (which attach
	// to an lvalue) and ?. (optional chaining, a member-NAME position like `.`).
	if js.IsOperator(prevSig) && prevSig != js.IncrToken && prevSig != js.DecrToken && prevSig != js.OptChainToken {
		return true
	}
	switch prevSig {
	// Grouping / call / array open, computed-member open.
	case js.OpenParenToken, js.OpenBracketToken:
		return true
	// Start of a template substitution `${ … }` — the hole is the expression.
	case js.TemplateStartToken, js.TemplateMiddleToken:
		return true
	// Separators / ternary / arrow / spread.
	case js.CommaToken, js.ColonToken, js.QuestionToken, js.ArrowToken, js.EllipsisToken:
		return true
	// Expression-introducing reserved words.
	case js.ReturnToken, js.TypeofToken, js.DeleteToken, js.VoidToken,
		js.NewToken, js.InToken, js.InstanceofToken, js.AwaitToken,
		js.YieldToken, js.CaseToken, js.ThrowToken, js.DoToken, js.ElseToken:
		return true
	// Identifier-keyword that introduces a value (for…of).
	case js.OfToken:
		return true
	}
	return false
}
