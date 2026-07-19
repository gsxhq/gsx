package gen

import "fmt"

// MinifyLevel is the minification level for one asset kind (<style> CSS or
// <script> JS). The zero value is MinifyNone — with no [minify] config and no
// GSX_MINIFY override, gsx emits assets verbatim. Minification is opt-in.
type MinifyLevel int

const (
	// MinifyNone (default) disables minification: the asset is emitted verbatim.
	MinifyNone MinifyLevel = iota
	// MinifyFull runs an aggressive AST-based minifier (tdewolff/minify) that
	// rewrites values. Its built-in implementation is cacheable; a custom
	// minifier installed via gen.WithCSSMinifier/WithJSMinifier takes precedence
	// and bypasses the cache.
	MinifyFull
)

// NOTE: gsx also has a conservative built-in "safe" pass (internal/cssmin,
// internal/jsmin) — whitespace/comment reductions only, never value rewrites,
// hole-aware. It is intentionally NOT exposed as a selectable level: it is our
// own less-battle-tested implementation, and its only niche (minified-but-cached)
// is moot in CI builds where the cache isn't reused. It is still REACHED in two
// ways: (1) it is the codegen engine default (minify=on, used by tests), and (2)
// it is full's fallback for holey (@{ }) blocks, which an external string->string
// minifier cannot process. To RE-EXPOSE a "safe" level later: add a MinifySafe
// constant here, a "safe" case in parseMinifyLevel, route it in
// config.effectiveCSSMin/effectiveJSMin (return nil so the built-in safe pass
// runs), and document it in docs/guide/config.md.

// String returns the TOML/CLI spelling of the level.
func (l MinifyLevel) String() string {
	if l == MinifyFull {
		return "full"
	}
	return "none"
}

// enabled reports whether a minify pass should run for this level.
func (l MinifyLevel) enabled() bool { return l != MinifyNone }

// parseMinifyLevel parses a level spelling for both [minify] config and the
// GSX_MINIFY env var (shared so the two vocabularies cannot drift). "safe" is
// intentionally unsupported — see the MinifyLevel doc note.
func parseMinifyLevel(s string) (MinifyLevel, error) {
	switch s {
	case "none":
		return MinifyNone, nil
	case "full":
		return MinifyFull, nil
	default:
		return 0, fmt.Errorf("invalid minify level %q (want \"none\" or \"full\")", s)
	}
}
