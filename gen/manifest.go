package gen

import (
	"os"

	"github.com/gsxhq/gsx/internal/attrclass"
)

// manifestSchemaVersion is bumped on incompatible manifest layout changes so a
// reader can reject a manifest it does not understand.
const manifestSchemaVersion = 3

// manifest is the resolved, build-independent projection of a project's gsx
// configuration — the data `gsx info --json` emits. It is computed on demand
// (buildManifest) and never persisted: tools that want it run `gsx info --json`,
// which re-resolves live and so is never stale.
type manifest struct {
	SchemaVersion int              `json:"schemaVersion"`
	Module        string           `json:"module"`
	UserRules     manifestRules    `json:"userRules"`
	Filters       []manifestFilter `json:"filters,omitempty"`
	Minify        manifestMinify   `json:"minify"`
	Formatter     manifestFmt      `json:"formatter"`
	Env           []manifestEnv    `json:"env"`
}

type manifestRules struct {
	URL []attrclass.Rule `json:"url,omitempty"`
}

type manifestMinify struct {
	CSS string `json:"css"`
	JS  string `json:"js"`
}

// manifestFmt reports the resolved [formatter] table (effective values, so
// printWidth is 80 when the table or key is absent).
type manifestFmt struct {
	PrintWidth int `json:"printWidth"`
}

type manifestEnv struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Value       string `json:"value"` // "" when unset
	Active      bool   `json:"active"`
}

type manifestFilter struct {
	Name string `json:"name"`
	Pkg  string `json:"pkg"`
	Func string `json:"func"`
}

// buildManifest assembles a manifest from the resolved classifier and filters.
func buildManifest(modPath string, cls *attrclass.Classifier, filters []manifestFilter, cssMinLevel, jsMinLevel MinifyLevel, printWidth int) manifest {
	envs := make([]manifestEnv, 0, len(envOverrides))
	for _, o := range envOverrides {
		e := manifestEnv{
			Name:        o.name,
			Description: o.desc,
		}
		if val, ok := os.LookupEnv(o.name); ok {
			e.Value = val
			e.Active = true
		}
		envs = append(envs, e)
	}
	return manifest{
		SchemaVersion: manifestSchemaVersion,
		Module:        modPath,
		UserRules:     manifestRules{URL: cls.Rules().URL},
		Filters:       filters,
		Minify:        manifestMinify{CSS: cssMinLevel.String(), JS: jsMinLevel.String()},
		Formatter:     manifestFmt{PrintWidth: printWidth},
		Env:           envs,
	}
}
