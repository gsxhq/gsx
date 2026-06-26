package gen

import (
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
func buildManifest(modPath string, cls *attrclass.Classifier, predLabel string, hasFieldMatcher bool, filters []manifestFilter) manifest {
	return manifest{
		SchemaVersion:   manifestSchemaVersion,
		Module:          modPath,
		UserRules:       cls.Rules(),
		HasPredicate:    cls.HasPredicate(),
		PredicateLabel:  predLabel,
		HasFieldMatcher: hasFieldMatcher,
		Filters:         filters,
	}
}
