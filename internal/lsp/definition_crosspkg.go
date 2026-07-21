package lsp

import (
	"go/token"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
)

// splitDottedTag splits a dotted component tag "qualifier.Name" into its parts,
// requiring a single dot and an upper-initial Name (a component, not a field
// access). "components.Input" → ("components","Input",true);
// "p.Content" → ("p","Content",true) — the qualifier just won't match an import.
func splitDottedTag(tag string) (qualifier, name string, ok bool) {
	i := strings.LastIndex(tag, ".")
	if i <= 0 || i == len(tag)-1 {
		return "", "", false
	}
	qualifier, name = tag[:i], tag[i+1:]
	if strings.Contains(qualifier, ".") || name[0] < 'A' || name[0] > 'Z' {
		return "", "", false
	}
	return qualifier, name, true
}

// resolveTagComponent resolves only same-package declarations retained in the
// package snapshot. Cross-package hover/definition uses ComponentCallFact and
// must never reconstruct dependency ASTs from package names or disk files.
func resolveTagComponent(pkg *Package, tag string) (*gsxast.Component, *token.FileSet, bool) {
	if _, _, dotted := splitDottedTag(tag); dotted {
		return nil, nil, false
	}
	c := findComponentDecl(pkg, tag)
	if c == nil {
		return nil, nil, false
	}
	return c, pkg.GSXFset, true
}
