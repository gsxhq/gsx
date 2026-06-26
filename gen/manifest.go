package gen

import (
	"os"

	"github.com/gsxhq/gsx/internal/attrclass"
)

// manifestSchemaVersion is bumped on incompatible manifest layout changes so a
// reader can reject a manifest it does not understand.
const manifestSchemaVersion = 1

// manifest is the resolved, build-independent projection of a project's gsx
// configuration — the data `gsx info --json` emits. It is computed on demand
// (buildManifest) and never persisted: tools that want it run `gsx info --json`,
// which re-resolves live and so is never stale.
type manifest struct {
	SchemaVersion   int              `json:"schemaVersion"`
	Module          string           `json:"module"`
	UserRules       attrclass.Rules  `json:"userRules"`
	HasPredicate    bool             `json:"hasPredicate"`
	PredicateLabel  string           `json:"predicateLabel,omitempty"`
	HasFieldMatcher bool             `json:"hasFieldMatcher,omitempty"`
	Filters         []manifestFilter `json:"filters,omitempty"`
	Minify          manifestMinify   `json:"minify"`
	Env             []manifestEnv    `json:"env"`
}

type manifestMinify struct {
	CSS string `json:"css"`
	JS  string `json:"js"`
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
// hasFieldMatcher should be true when a custom FieldMatcher is installed (nil
// means the default matcher is in effect; a non-nil custom matcher changes how
// attr→field resolution works and therefore changes the generated output for
// projects with kebab or custom-matched attrs).
func buildManifest(modPath string, cls *attrclass.Classifier, predLabel string, hasFieldMatcher bool, filters []manifestFilter, cssMinLevel, jsMinLevel MinifyLevel) manifest {
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
		SchemaVersion:   manifestSchemaVersion,
		Module:          modPath,
		UserRules:       cls.Rules(),
		HasPredicate:    cls.HasPredicate(),
		PredicateLabel:  predLabel,
		HasFieldMatcher: hasFieldMatcher,
		Filters:         filters,
		Minify:          manifestMinify{CSS: cssMinLevel.String(), JS: jsMinLevel.String()},
		Env:             envs,
	}
}
