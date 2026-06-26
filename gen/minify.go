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
)

// String returns the TOML/CLI spelling of the level.
func (l MinifyLevel) String() string {
	if l == MinifyNone {
		return "none"
	}
	return "safe"
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
	default:
		return 0, fmt.Errorf("invalid minify level %q (want \"safe\" or \"none\")", s)
	}
}
