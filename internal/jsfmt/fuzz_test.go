package jsfmt

import "testing"

// FuzzReindentJS asserts the re-indenter's invariants on arbitrary input:
//   - never panics;
//   - idempotent: Format(Format(x)) == Format(x);
//   - token-preserving: re-indenting changes ONLY whitespace, so the JS token
//     signature is invariant — TokenSignature(in) == TokenSignature(out). This
//     is the strong correctness property: any dropped/added/fused token (e.g.
//     the lone-\r line-terminator-drop bug, or a mis-lexed regex) changes the
//     signature and fails here.
//
// jsfmt.Format errors only on a lexer failure (→ the printer renders verbatim),
// so the success branch is what we check.
func FuzzReindentJS(f *testing.F) {
	for _, s := range []string{
		"",
		"const x = 1",
		"function f() {\n  return 1\n}",
		// the callback pattern that escaped to a user file
		"el.addEventListener('x', (e) => {\nf(e)\n})",
		"['a','b'].forEach(n => {\ng(n)\n})",
		"(function() {\nvar x = 1\n})()",
		"let r = /a\\/b/g; let q = a / b",
		"const t = `a ${b} c`",
		"x = `${`${y}`}`",
		"a //line\nb",
		"a /* block\n comment */ b",
		"return\nx",
		"a=1\rb=2",   // lone CR
		"a=1 b=2",    // U+2028 line separator
		"a=1\r\nb=2", // CRLF
		"if (x) {\n if (y) {\n  z\n }\n}",
		"const o = { a: 1, b() { return 2 } }",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out, err := Format([]byte(s), 80, 2)
		if err != nil {
			return // lex error → verbatim fallback; nothing to assert
		}
		out2, err2 := Format(out, 80, 2)
		if err2 != nil || string(out2) != string(out) {
			t.Fatalf("not idempotent:\n in=%q\n once=%q\n twice=%q err=%v", s, out, out2, err2)
		}
		if si, so := TokenSignature([]byte(s)), TokenSignature(out); si != so {
			t.Fatalf("re-indent changed the token signature (whitespace-only invariant violated):\n in=%q\n out=%q\n sigIn=%q\n sigOut=%q", s, out, si, so)
		}
	})
}
