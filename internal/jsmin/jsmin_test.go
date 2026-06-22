package jsmin

import "testing"

func TestMinifyJS(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"drop indentation, keep newline", "function f() {\n\treturn 1;\n}", "function f() {\nreturn 1;\n}"},
		{"collapse intra-line spaces", "let   x   =   1", "let x = 1"},
		{"strip line comment, keep one ASI newline", "a()\n// note\nb()", "a()\nb()"},
		{"strip block comment", "a/* note */b", "a b"},
		{"keep bang comment", "/*! keep */\nx", "/*! keep */\nx"},
		{"string interior verbatim", `let s = "a  b\t c"`, `let s = "a  b\t c"`},
		{"template interior verbatim", "let s = `a  ${ x }  b`", "let s = `a  ${ x }  b`"},
		{"regex interior verbatim", "let r = /a  b/g", "let r = /a  b/g"},
		{"ASI newline preserved", "return\nx", "return\nx"},
		{"no token fusion", "return  x", "return x"},
		{"collapse blank lines", "a()\n\n\n\nb()", "a()\nb()"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		if got := minifyJS(tt.in); got != tt.want {
			t.Errorf("%s: minifyJS(%q) = %q, want %q", tt.name, tt.in, got, tt.want)
		}
	}
}

func TestMinifyJSIdempotent(t *testing.T) {
	for _, in := range []string{
		"function f(){\n\treturn 1\n}", "let x = `a ${y} b`", "a/* c */b", "/*! k */\nx",
		"let r = /a b/; let q = a / b", "return\nx",
	} {
		once := minifyJS(in)
		if twice := minifyJS(once); twice != once {
			t.Errorf("not idempotent: minifyJS(%q)=%q, again=%q", in, once, twice)
		}
	}
}
