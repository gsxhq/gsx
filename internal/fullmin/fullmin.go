// Package fullmin is gsx's aggressive ("full") minifier: a thin wrapper over
// github.com/tdewolff/minify/v2 for the [minify] level "full". Unlike the safe
// cssmin/jsmin passes (whitespace/comment reductions only, never value rewrites),
// it performs value rewrites — color/number shortening, local-variable mangling —
// via tdewolff's AST-based minifiers, which remain correctness-preserving
// (top-level names are kept, ASI is respected).
//
// It is invoked only on HOLELESS <style>/<script> blocks through gsx's ext-minifier
// hook; holey (@{ }) blocks use the built-in safe pass.
package fullmin

import (
	"github.com/tdewolff/minify/v2"
	"github.com/tdewolff/minify/v2/css"
	"github.com/tdewolff/minify/v2/js"
)

// m is the shared minifier registry. tdewolff's *minify.M is safe for concurrent
// use after setup (the registered funcs allocate fresh per-call state), so a
// single package-level instance serves all codegen goroutines.
var m = newMinifier()

func newMinifier() *minify.M {
	m := minify.New()
	m.AddFunc("text/css", css.Minify)
	m.AddFunc("application/javascript", js.Minify)
	return m
}

// CSS aggressively minifies a complete (holeless) CSS string.
func CSS(s string) (string, error) { return m.String("text/css", s) }

// JS aggressively minifies a complete (holeless) JS string.
func JS(s string) (string, error) { return m.String("application/javascript", s) }
