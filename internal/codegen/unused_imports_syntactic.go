package codegen

import (
	goast "go/ast"
	"go/token"
	"strings"
)

// skeletonUsedNames returns the set of identifiers used as the qualifier X in
// any selector expression X.Sel within f. An imported package name can only be
// referenced this way (or via a dot/blank import, handled separately), so this
// set is exactly "which import local-names are referenced" for a valid Go file.
func skeletonUsedNames(f *goast.File) map[string]bool {
	used := map[string]bool{}
	goast.Inspect(f, func(n goast.Node) bool {
		if sel, ok := n.(*goast.SelectorExpr); ok {
			if id, ok := sel.X.(*goast.Ident); ok {
				used[id.Name] = true
			}
		}
		return true
	})
	return used
}

// importBaseName is the last path segment — the CONVENTIONAL default local name,
// which for some packages (e.g. gopkg.in/yaml.v3 → "yaml") is NOT the real
// package name. It is used only as a fast "definitely used" check; a base that
// is not referenced makes the import a removal CANDIDATE whose real name must be
// resolved before removal.
func importBaseName(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}

// classifyUnusedImports splits a file's hoisted import specs into definitely-unused
// imports and removal candidates, given the set of referenced qualifier names.
//
//   - `_` / `.` imports are never removed (always "used").
//   - An import whose only skeleton reference was dropped by a requalification-
//     failed generic tag (sunk) is never removed — it IS used in the .gsx source.
//   - An aliased import's explicit name is authoritative: unused iff its alias is
//     not referenced.
//   - A default import is kept when its path base is referenced; otherwise it is a
//     CANDIDATE (its real package name may differ from the base and still be used).
func classifyUnusedImports(used map[string]bool, imps []importSpec, sunk map[sunkImportKey]bool, gsxFset *token.FileSet) (unused []UnusedImport, candidates []importSpec) {
	for _, imp := range imps {
		if imp.name == "_" || imp.name == "." {
			continue
		}
		if sunk != nil && imp.pos.IsValid() {
			k := sunkImportKey{line: gsxFset.Position(imp.pos).Line, path: imp.path}
			if sunk[k] {
				continue
			}
		}
		if imp.name != "" {
			if !used[imp.name] {
				unused = append(unused, UnusedImport{Name: imp.name, Path: imp.path})
			}
			continue
		}
		if used[importBaseName(imp.path)] {
			continue
		}
		candidates = append(candidates, imp)
	}
	return unused, candidates
}
