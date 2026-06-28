package codegen

import (
	"go/token"
	"go/types"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
)

// buildCrossNav builds a package's component cross-index (componentKey ->
// CrossRef with .gsx Decl + in-package Refs) and the navigable-reference index.
// gsxFset resolves .gsx declaration positions; skelFset (the skeleton fset)
// resolves use positions (//line-mapped back to .gsx for skeleton refs, real
// .go for hand-written refs). Shared by the Module core (both codegen.GenerateDirs
// and the LSP path) to produce identical indexes.
func buildCrossNav(
	compByKey map[string]*gsxast.Component,
	objKey map[types.Object]string,
	gsxFset, skelFset *token.FileSet,
	info *types.Info,
	pkgTypes *types.Package,
) (map[string]CrossRef, []NavRef) {
	index := map[string]CrossRef{}
	for key, c := range compByKey {
		index[key] = CrossRef{Name: c.Name, Decl: gsxFset.Position(c.NamePos)} // gsx fset → .gsx position
	}

	// Build maps for NavIndex: props-struct objects and field var objects → .gsx targets.
	// structObjToComp maps a props-struct types.Object → the component it belongs to.
	// fieldObjToPos maps a field *types.Var → the .gsx position of the corresponding param.
	structObjToComp := map[types.Object]*gsxast.Component{}
	fieldObjToPos := map[*types.Var]token.Position{}
	for _, c := range compByKey {
		// Derive propsName the same way emitComponentSkeleton does.
		propsName := c.Name + "Props"
		if c.Recv != "" {
			_, _, recvTypeName, rerr := parseRecv(c.Recv)
			if rerr == nil {
				propsName = recvTypeName + c.Name + "Props"
			}
		}
		structObj := pkgTypes.Scope().Lookup(propsName)
		if structObj == nil {
			continue
		}
		structObjToComp[structObj] = c

		// Map each field var → the .gsx position of its corresponding param.
		params, err := parseParams(c.Params)
		if err != nil {
			continue
		}
		st, ok := structObj.Type().Underlying().(*types.Struct)
		if !ok {
			continue
		}
		for _, p := range params {
			fname := fieldName(p.name)
			paramPos := gsxFset.Position(c.ParamsPos + token.Pos(p.nameOff))
			for i := 0; i < st.NumFields(); i++ {
				fv := st.Field(i)
				if fv.Name() == fname {
					fieldObjToPos[fv] = paramPos
					break
				}
			}
		}
	}

	var navIndex []NavRef
	for id, obj := range info.Uses {
		p := skelFset.Position(id.Pos())
		if strings.HasSuffix(p.Filename, ".x.go") {
			continue // synthetic skeleton position with no //line — skip
		}
		// Case 1: component func reference → .gsx component decl.
		if key, ok := objKey[obj]; ok {
			c := compByKey[key]
			cr := index[key]
			cr.Refs = append(cr.Refs, p)
			index[key] = cr
			navIndex = append(navIndex, NavRef{
				From: p,
				Name: id.Name,
				To:   gsxFset.Position(c.NamePos),
			})
			continue
		}
		// Case 2: props-struct type reference → start of the .gsx component
		// argument list (the props ARE the params, so CardProps lands on the
		// param list rather than the component name). Components with no params
		// have no ParamsPos; fall back to the component name there.
		if c, ok := structObjToComp[obj]; ok {
			to := c.ParamsPos
			if !to.IsValid() {
				to = c.NamePos
			}
			navIndex = append(navIndex, NavRef{
				From: p,
				Name: id.Name,
				To:   gsxFset.Position(to),
			})
			continue
		}
		// Case 3: props-struct field reference → .gsx param position.
		if fv, ok := obj.(*types.Var); ok {
			if paramPos, ok := fieldObjToPos[fv]; ok {
				navIndex = append(navIndex, NavRef{
					From: p,
					Name: id.Name,
					To:   paramPos,
				})
			}
		}
	}
	return index, navIndex
}
