package jsmin

import "testing"

// FuzzMinifyJS asserts robustness: never panics, idempotent. minifyJS is a
// formatter (not a security boundary), so idempotence + no-panic is the property.
func FuzzMinifyJS(f *testing.F) {
	for _, s := range []string{
		"", "function f(){return 1}", "let x = `a ${b} c`", "a/* c */b", "/*! k */\nx",
		"let r=/a b/g", "return\nx", "a //x\nb", "`${`${x}`}`", "var x", "x=>x",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		once := minifyJS(s)
		if twice := minifyJS(once); twice != once {
			t.Fatalf("not idempotent:\n in=%q\n once=%q\n twice=%q", s, once, twice)
		}
	})
}
