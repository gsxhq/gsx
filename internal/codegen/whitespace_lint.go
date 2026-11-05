package codegen

import (
	"go/token"
	"sort"

	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/internal/wsnorm"
)

// reportWhitespaceLint adds the parse-time whitespace findings to bag as
// punctuation-space / redundant-space-literal warnings; the lint policy
// applied to the bag sets their final level.
func reportWhitespaceLint(byPath map[string][]wsnorm.Finding, fset *token.FileSet, bag *diag.Bag) {
	paths := make([]string, 0, len(byPath))
	for path := range byPath {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		for _, f := range byPath[path] {
			code := diag.CodePunctuationSpace
			if f.Kind == wsnorm.RedundantSpaceLiteral {
				code = diag.CodeRedundantSpaceLiteral
			}
			help := f.Help()
			if f.Migrate {
				help += "; `gsx fix whitespace` applies this fix"
			}
			bag.Add(diag.Diagnostic{
				Start:    fset.Position(f.Pos),
				End:      fset.Position(f.End),
				Severity: diag.Warning,
				Code:     code,
				Message:  f.Message(),
				Help:     help,
				Source:   "codegen",
			})
		}
	}
}
