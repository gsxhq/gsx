package codegen

import (
	goast "go/ast"
	goparser "go/parser"
	"go/token"
	"path/filepath"
	"strings"

	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/internal/jsx"
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

// fileSkeleton is one .gsx file's lowered skeleton AST plus the import
// metadata buildPackageSkeletons harvests alongside it: the file's hoisted
// import specs (imps) and the set of specs sunk by a requalification-failed
// generic tag (sunk) — see analyze's sunkImports doc for why a sunk import is
// never a removal candidate even when the skeleton drops its only reference.
type fileSkeleton struct {
	skel *goast.File
	imps []importSpec
	sunk map[sunkImportKey]bool
}

// packageSkeletons is the per-package result of buildPackageSkeletons: every
// buildable .gsx file's skeleton, keyed by its absolute .gsx path, plus the
// FileSet those skeletons (and the .gsx positions in their import specs)
// resolve against.
type packageSkeletons struct {
	gsxFset *token.FileSet
	byGsx   map[string]fileSkeleton // .gsx abs path -> skeleton + import specs + sunk set
}

// buildPackageSkeletons lowers every .gsx file in dir to its skeleton AST WITHOUT
// type-checking (no importer, no dependency resolution) and returns, per file,
// the parsed skeleton, its hoisted import specs, and its sunk-import set. It
// mirrors analyze's per-file loop (module_importer.go:769-819) using the same
// buildSkeleton lowering, but keeps only what unused-import detection needs. A
// file whose skeleton fails to build (parse/attr error) is simply omitted, so
// the caller keeps all of that file's imports.
func (m *Module) buildPackageSkeletons(dir string) (*packageSkeletons, error) {
	m.analysisMu.Lock()
	defer m.analysisMu.Unlock()
	m.maybeRebuildFset()
	m.applyDirty()
	fset := m.fset
	bag := diag.NewBag(fset)
	gsxFiles, _, err := m.parsePackageWithFset(dir, fset)
	if err != nil {
		return nil, err
	}
	for _, f := range gsxFiles {
		jsx.ResolveScripts(f, bag) // best-effort; failure just means we may skip that file below
	}
	table, err := m.cachedFilterTable()
	if err != nil {
		return nil, err
	}
	propFields, nodeProps, attrsProps, byo, err := componentPropFieldsFor(dir, gsxFiles)
	if err != nil {
		return nil, err
	}
	genericSigs := genericSigsFor(gsxFiles, byo)
	inferNames := newInferNameAllocator()
	out := &packageSkeletons{gsxFset: fset, byGsx: map[string]fileSkeleton{}}
	for path, f := range gsxFiles {
		ff := m.fileScopedFacts(dir, f, propFields, nodeProps, attrsProps, byo, bag, fset)
		skel, _, imps, _, infReg, berr := buildSkeleton(f, table, ff.propFields, ff.nodeProps, ff.attrsProps,
			genericSigs, ff.genericSigs, ff.byo, m.opts.FieldMatcher, fset, bag, inferNames)
		if berr != nil {
			continue // unbuildable → keep all imports (no entry)
		}
		base := strings.TrimSuffix(filepath.Base(path), ".gsx")
		absXpath := filepath.Join(dir, base+".x.go")
		gf, perr := goparser.ParseFile(fset, absXpath, skel, goparser.SkipObjectResolution)
		if perr != nil {
			continue
		}
		sunk := map[sunkImportKey]bool{}
		if len(infReg.failedAliases) > 0 && ff.depAliasSpecs != nil {
			for alias := range infReg.failedAliases {
				if spec, ok := ff.depAliasSpecs[alias]; ok && spec.pos.IsValid() {
					sunk[sunkImportKey{line: fset.Position(spec.pos).Line, path: spec.path}] = true
				}
			}
		}
		out.byGsx[path] = fileSkeleton{skel: gf, imps: imps, sunk: sunk}
	}
	return out, nil
}
