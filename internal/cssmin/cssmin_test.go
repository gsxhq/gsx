package cssmin

import "testing"

func TestMinifyCSS(t *testing.T) {
	tests := []struct{ name, in, want string }{
		// --- the safe transforms ---
		{"collapse+trim", "  .a  {\n\tcolor:  red;\n}  ", ".a{color: red}"},
		{"strip comment", "a/* x */b", "a b"},
		{"keep bang comment", "/*! keep */\n.a{color:red}", "/*! keep */.a{color:red}"},
		{"drop semi before brace", ".a{color:red;}", ".a{color:red}"},
		{"ws around delimiters", ".a , .b { x:1 ; y:2 }", ".a,.b{x:1;y:2}"},
		{"comment between idents keeps separation", "a/**/b", "a b"},
		// --- must NOT break (historical naive-minifier breakages) ---
		{"calc spacing", ".a{width:calc(100% - 8px)}", ".a{width:calc(100% - 8px)}"},
		{"descendant combinator", ".a   .b{x:1}", ".a .b{x:1}"},
		{"value separators", ".a{margin:1px   2px   3px}", ".a{margin:1px 2px 3px}"},
		{"string interior", ".a{content:\"  a  b  \"}", ".a{content:\"  a  b  \"}"},
		{"string with brace/semicolon", ".a{content:\"x}y;z\"}", ".a{content:\"x}y;z\"}"},
		{"url unquoted spaces", ".a{background:url(data:image/svg+xml,<svg viewBox=\"0 0 8 8\">)}", ".a{background:url(data:image/svg+xml,<svg viewBox=\"0 0 8 8\">)}"},
		{"url quoted + format", "@font-face{src:url(f.woff2) format(\"woff2\")}", "@font-face{src:url(f.woff2) format(\"woff2\")}"},
		{"media and", "@media (min-width:30px) and (max-width:50px){.a{x:1}}", "@media (min-width:30px) and (max-width:50px){.a{x:1}}"},
		{"grid-template-areas", ".g{grid-template-areas:\"a a\" \"b c\"}", ".g{grid-template-areas:\"a a\" \"b c\"}"},
		{"ie star hack", ".a{*zoom:1;_height:1px}", ".a{*zoom:1;_height:1px}"},
		{"An+B", ".a:nth-child(2n + 1){x:1}", ".a:nth-child(2n + 1){x:1}"},
		{"unicode-range", "@font-face{unicode-range:U+0000-00FF, U+0131}", "@font-face{unicode-range:U+0000-00FF,U+0131}"},
		{"empty", "", ""},
		{"only comment", "/* gone */", ""},
		// --- afterBang leak regression ---
		{"bang then token then significant space", "/*! */x y{a:b}", "/*! */x y{a:b}"},
		{"bang then token then value separator", "/*! */x{a:1 2}", "/*! */x{a:1 2}"},
		{"bang then immediate space suppressed", "/*! k */ .a{x:1}", "/*! k */.a{x:1}"},
	}
	for _, tt := range tests {
		if got := minifyCSS(tt.in); got != tt.want {
			t.Errorf("%s: minifyCSS(%q) = %q, want %q", tt.name, tt.in, got, tt.want)
		}
	}
}

func TestMinifyCSSIdempotent(t *testing.T) {
	for _, in := range []string{
		".a{color:red}", ".a , .b { x:1 ; }", "@media (a) and (b){.x{y:1}}",
		".a{width:calc(100% - 8px)}", ".a{content:\"  \"}", "/*! k */.a{x:1}",
	} {
		once := minifyCSS(in)
		if twice := minifyCSS(once); twice != once {
			t.Errorf("not idempotent: minifyCSS(%q)=%q, again=%q", in, once, twice)
		}
	}
}
