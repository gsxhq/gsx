package codegen

import (
	"fmt"
	goast "go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/packages"

	gsxast "github.com/gsxhq/gsx/ast"
	gsxparser "github.com/gsxhq/gsx/parser"
)

// GeneratePackage generates a .x.go for every .gsx file in dir, resolving
// interpolation types with go/types over the WHOLE package — the package's
// hand-written .go files plus synthesized skeletons of the gsx components,
// injected via go/packages Overlay. This resolves cross-file type references
// and cross-component calls. dir must be inside a Go module. The result maps
// each .gsx path to its generated source.
func GeneratePackage(dir string) (map[string][]byte, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.gsx"))
	if err != nil {
		return nil, err
	}
	files := map[string]*gsxast.File{}
	for _, m := range matches {
		src, err := os.ReadFile(m)
		if err != nil {
			return nil, err
		}
		f, err := gsxparser.ParseFile(token.NewFileSet(), m, src, 0)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", m, err)
		}
		files[m] = f
	}

	resolved, err := resolveTypesPkg(dir, files)
	if err != nil {
		return nil, err
	}

	out := map[string][]byte{}
	for path, file := range files {
		gen, err := generateFile(file, resolved)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		out[path] = gen
	}
	return out, nil
}

// resolveTypesPkg type-checks the package (real .go files + synthesized gsx
// component skeletons via Overlay) and returns each interpolation's type.
func resolveTypesPkg(dir string, files map[string]*gsxast.File) (map[*gsxast.Interp]types.Type, error) {
	overlay := map[string][]byte{}
	skelComps := map[string][]*gsxast.Component{}
	for path, file := range files {
		skel, comps, err := buildSkeleton(file)
		if err != nil {
			return nil, err
		}
		base := strings.TrimSuffix(filepath.Base(path), ".gsx")
		xpath := filepath.Join(dir, base+".x.go")
		overlay[xpath] = []byte(skel)
		skelComps[xpath] = comps
	}

	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedDeps | packages.NeedTypes |
			packages.NeedSyntax | packages.NeedTypesInfo,
		Dir:     dir,
		Overlay: overlay,
	}
	pkgs, err := packages.Load(cfg, ".")
	if err != nil {
		return nil, fmt.Errorf("codegen: load package: %w", err)
	}
	if len(pkgs) == 0 {
		return nil, fmt.Errorf("codegen: no package found in %s", dir)
	}
	pkg := pkgs[0]
	if len(pkg.Errors) > 0 {
		return nil, fmt.Errorf("codegen: type resolution failed: %s", pkg.Errors[0])
	}

	out := map[*gsxast.Interp]types.Type{}
	for _, f := range pkg.Syntax {
		fname := pkg.Fset.Position(f.Pos()).Filename
		comps, ok := skelComps[fname]
		if !ok {
			continue // a real .go file, not one of our overlays
		}
		harvest(f, comps, pkg.TypesInfo, out)
	}
	return out, nil
}

// buildSkeleton synthesizes a Go file standing in for the gsx file during type
// resolution: the file's GoChunks, plus each component's real props struct and
// func signature, with a probe body (used-param locals, each interpolation as
// `_ = (expr)`, each child component as `_ = Child(ChildProps{})`).
func buildSkeleton(file *gsxast.File) (string, []*gsxast.Component, error) {
	var comps []*gsxast.Component
	for _, d := range file.Decls {
		if c, ok := d.(*gsxast.Component); ok {
			comps = append(comps, c)
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "package %s\n", file.Package)
	sb.WriteString("import _gsxrt \"github.com/gsxhq/gsx\"\n")
	for _, d := range file.Decls {
		if gc, ok := d.(*gsxast.GoChunk); ok {
			sb.WriteString(gc.Src)
			sb.WriteByte('\n')
		}
	}
	for _, c := range comps {
		if c.Recv != "" {
			continue // SPIKE: method components handled later
		}
		params, err := parseParams(c.Params)
		if err != nil {
			return "", nil, err
		}
		fmt.Fprintf(&sb, "type %sProps struct {\n", c.Name)
		for _, p := range params {
			fmt.Fprintf(&sb, "\t%s %s\n", fieldName(p.name), p.typ)
		}
		sb.WriteString("}\n")
		fmt.Fprintf(&sb, "func %s(p %sProps) _gsxrt.Node {\n", c.Name, c.Name)
		used := usedParams(c, params)
		for _, p := range params {
			if used[p.name] {
				fmt.Fprintf(&sb, "\t%s := p.%s\n\t_ = %s\n", p.name, fieldName(p.name), p.name)
			}
		}
		emitProbes(&sb, c.Body)
		sb.WriteString("\treturn nil\n}\n")
	}
	return sb.String(), comps, nil
}

// emitProbes writes the type-resolution probe statements for a component body:
// `_ = (expr)` per interpolation (a ParenExpr RHS marks an interpolation probe),
// `_ = Child(ChildProps{})` per child component.
func emitProbes(sb *strings.Builder, nodes []gsxast.Markup) {
	for _, n := range nodes {
		switch t := n.(type) {
		case *gsxast.Interp:
			fmt.Fprintf(sb, "\t_ = (%s)\n", strings.TrimSpace(t.Expr))
		case *gsxast.Element:
			if isComponentTag(t.Tag) {
				fmt.Fprintf(sb, "\t_ = %s(%sProps{})\n", t.Tag, t.Tag)
			} else {
				emitProbes(sb, t.Children)
			}
		}
	}
}

// harvest reads each interpolation's resolved type from a type-checked skeleton
// file. Interpolation probes are AssignStmts whose RHS is a ParenExpr (child
// calls are plain CallExprs and are skipped).
func harvest(f *goast.File, comps []*gsxast.Component, info *types.Info, out map[*gsxast.Interp]types.Type) {
	byName := map[string]*gsxast.Component{}
	for _, c := range comps {
		byName[c.Name] = c
	}
	for _, decl := range f.Decls {
		fd, ok := decl.(*goast.FuncDecl)
		if !ok {
			continue
		}
		c, ok := byName[fd.Name.Name]
		if !ok || fd.Body == nil {
			continue
		}
		interps := componentInterps(c)
		k := 0
		for _, stmt := range fd.Body.List {
			as, ok := stmt.(*goast.AssignStmt)
			if !ok || len(as.Rhs) != 1 {
				continue
			}
			pe, ok := as.Rhs[0].(*goast.ParenExpr)
			if !ok {
				continue // child-component call probe, not an interpolation
			}
			if k >= len(interps) {
				break
			}
			out[interps[k]] = info.Types[pe.X].Type
			k++
		}
	}
}
