package cssmin

import "testing"

// FuzzMinifyCSS asserts the scanner is robust: it never panics, and minification
// is idempotent (a second pass is a no-op). minifyCSS is a formatter, not a
// security boundary, so idempotence + no-panic is the right invariant.
func FuzzMinifyCSS(f *testing.F) {
	for _, s := range []string{
		"", ".a{color:red}", "  .a , .b { x:1 ; }", "/* c */.a{x:1}", "/*! k */.a{x:1}",
		".a{width:calc(100% - 8px)}", ".a{content:\"  }  ;  \"}", "@media (a) and (b){.x{y:1}}",
		".a{background:url(data:image/svg+xml,<svg viewBox=\"0 0 8 8\">)}", "a/**/b", "/*! */x y{a:1 2}",
		"url(", "/*", "\"", "'", "}", ";;}", "{{{", "url(\x00)", "a:nth-child(2n + 1)",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		once := minifyCSS(s) // must not panic
		if twice := minifyCSS(once); twice != once {
			t.Fatalf("minifyCSS not idempotent:\n in=%q\n once=%q\n twice=%q", s, once, twice)
		}
	})
}
