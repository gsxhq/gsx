package gen

import (
	"fmt"
	"os"
	"strings"
)

// envOverride is one curated environment variable that overrides a declarative
// (gsx.toml) config value. The mechanism is general (one table, one pass); the
// coverage is selective — only knobs that legitimately vary dev↔prod get a var.
// See docs/guide/config.md and the design spec for the three-layer model.
type envOverride struct {
	name  string // GSX_<THING>
	desc  string // one-line help (surfaced by `gsx info`)
	apply func(raw string, cfg *config) error
}

// envOverrides is the registry of user-facing env overrides. NOTE: GSXCACHE and
// GSX_PERF are internal/test knobs, NOT user config, and are deliberately absent.
var envOverrides = []envOverride{
	{
		name: "GSX_MINIFY",
		desc: `minify <style>/<script>: off|safe|full (overrides [minify])`,
		apply: func(raw string, cfg *config) error {
			var lvl MinifyLevel
			switch strings.ToLower(strings.TrimSpace(raw)) {
			case "off", "none":
				lvl = MinifyNone
			case "on", "safe":
				lvl = MinifySafe
			case "full":
				lvl = MinifyFull
			default:
				return fmt.Errorf("GSX_MINIFY=%q: want \"off\"/\"none\", \"safe\"/\"on\", or \"full\"", raw)
			}
			cfg.cssMinLevel, cfg.jsMinLevel = lvl, lvl
			return nil
		},
	},
}

// applyEnvOverrides returns cfg with every PRESENT registered env var applied.
// It takes cfg by value and returns a copy (no mutation of the caller's config),
// matching mergeConfig's style. An invalid value is a hard error naming the var.
func applyEnvOverrides(cfg config) (config, error) {
	for _, o := range envOverrides {
		if raw, ok := os.LookupEnv(o.name); ok {
			if err := o.apply(raw, &cfg); err != nil {
				return config{}, err
			}
		}
	}
	return cfg, nil
}
