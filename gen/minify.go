package gen

import "fmt"

// MinifyLevel is the minification level for one asset kind (<style> CSS or
// <script> JS). The zero value is MinifySafe, so an absent [minify] table — and
// the unset config — means today's always-on safe minification.
type MinifyLevel int

const (
	// MinifySafe runs gsx's built-in safe minifier (or a custom one installed via
	// gen.WithCSSMinifier / gen.WithJSMinifier). It is the default.
	MinifySafe MinifyLevel = iota
	// MinifyNone disables minification: the asset is emitted verbatim.
	MinifyNone
	// MinifyFull runs an aggressive AST-based minifier (tdewolff/minify) that
	// rewrites values; it bypasses the incremental cache. A custom minifier, if
	// installed, takes precedence over the built-in full minifier.
	MinifyFull
)

// String returns the TOML/CLI spelling of the level.
func (l MinifyLevel) String() string {
	switch l {
	case MinifyNone:
		return "none"
	case MinifyFull:
		return "full"
	default:
		return "safe"
	}
}

// enabled reports whether the minify pass should run for this level.
func (l MinifyLevel) enabled() bool { return l != MinifyNone }

// parseMinifyLevel parses a TOML/CLI level spelling, rejecting anything else.
func parseMinifyLevel(s string) (MinifyLevel, error) {
	switch s {
	case "safe":
		return MinifySafe, nil
	case "none":
		return MinifyNone, nil
	case "full":
		return MinifyFull, nil
	default:
		return 0, fmt.Errorf("invalid minify level %q (want \"safe\", \"none\", or \"full\")", s)
	}
}
