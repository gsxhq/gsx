package cssfmt

import "testing"

// FuzzReindentCSS asserts the CSS re-indenter's invariants on arbitrary input:
//   - never panics;
//   - idempotent: Format(Format(x)) == Format(x);
//   - token-preserving: re-indenting changes ONLY whitespace, so the CSS token
//     signature is invariant — TokenSignature(in) == TokenSignature(out). Any
//     dropped/added/fused token (e.g. a mishandled \r fusing two value tokens)
//     changes the signature and fails here.
//
// cssfmt.Format errors only on a tokenizer failure (unterminated string/comment
// → the printer renders verbatim), so the success branch is what we check.
func FuzzReindentCSS(f *testing.F) {
	for _, s := range []string{
		"",
		".a{color:red}",
		".a {\n  color: red;\n  background: blue;\n}",
		"h1, h2, h3 { margin: 0 }",
		"@media (min-width: 600px) {\n .a { color: red }\n}",
		".a { width: calc(100% - 10px) }",
		".a { background: url(x.png) }",
		".a { content: \"a{b}c\" }",
		".a {\n\t/* multi\n line */\n\tcolor: red;\n}",
		".a { color: __gsxhole_0_ }",
		".x{margin:0;padding:0}.y{top:0}",
		".a{}\r.b{}", // lone CR between rules
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out, err := Format([]byte(s), 80, 2)
		if err != nil {
			return // tokenizer error → verbatim fallback; nothing to assert
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
