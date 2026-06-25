package gen

import (
	"io"

	"github.com/gsxhq/gsx/internal/attrclass"
	"github.com/gsxhq/gsx/internal/codegen"
)

// watchConfig carries everything runWatch needs, mirroring runGenerate's
// configured options so watch honors the same filters/classifier/minifiers.
type watchConfig struct {
	paths      []string
	format     string // "" (human) or "ndjson"
	stdout     io.Writer
	stderr     io.Writer
	quiet      bool
	verbose    bool
	filterPkgs []string
	aliases    []codegen.FilterAlias
	cls        *attrclass.Classifier
	predLabel  string
	fm         codegen.FieldMatcher
	cssMin     func(string) (string, error)
	jsMin      func(string) (string, error)
}

// runWatch starts the long-lived generate-on-change daemon. Stub: returns 0 when
// there are no dirs to watch. The watch loop is added in Task 5.
func runWatch(cfg watchConfig) int {
	dirs, err := discoverDirs(cfg.paths)
	if err != nil || len(dirs) == 0 {
		return 0
	}
	return 0
}
