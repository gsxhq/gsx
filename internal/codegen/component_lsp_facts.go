package codegen

import (
	"fmt"
	goast "go/ast"
	"go/token"
	"go/types"
	"sort"

	gsxast "github.com/gsxhq/gsx/ast"
)

func componentParamDeclarationFacts(compByKey map[string][]*gsxast.Component, objKey map[types.Object]string, fset *token.FileSet, packagePath string) ([]ComponentParamDeclFact, error) {
	keys := make([]string, 0, len(compByKey))
	for key := range compByKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var facts []ComponentParamDeclFact
	for _, key := range keys {
		components := compByKey[key]
		if len(components) == 0 {
			continue
		}
		var function *types.Func
		for object, objectKey := range objKey {
			candidate, ok := object.(*types.Func)
			if ok && objectKey == key && candidate.Name() == components[0].Name {
				function = candidate
				break
			}
		}
		if function == nil {
			return nil, fmt.Errorf("codegen: component parameter facts: logical component %s has no public semantic function", key)
		}
		signature, ok := function.Type().(*types.Signature)
		if !ok {
			return nil, fmt.Errorf("codegen: component parameter facts: logical component %s is not callable", key)
		}

		parsed := make([][]componentParamDecl, len(components))
		for index, component := range components {
			params, err := parseComponentParamDecls(component.Params)
			if err != nil {
				return nil, err
			}
			if len(params) != signature.Params().Len() {
				return nil, fmt.Errorf("codegen: component parameter facts: %s declaration has %d parameters; semantic signature has %d", key, len(params), signature.Params().Len())
			}
			parsed[index] = params
		}

		for ordinal := range signature.Params().Len() {
			variable := signature.Params().At(ordinal)
			name := variable.Name()
			if name == "" {
				continue
			}
			fact := ComponentParamDeclFact{
				PackagePath:  packagePath,
				ComponentKey: key,
				Ordinal:      ordinal,
				Name:         name,
				Origin:       variable.Origin(),
			}
			for index, component := range components {
				parameter := parsed[index][ordinal]
				if parameter.name != name || parameter.nameOff < 0 || !component.ParamsPos.IsValid() {
					return nil, fmt.Errorf("codegen: component parameter facts: %s parameter %d does not match its validated semantic identity", key, ordinal)
				}
				role := publishedDeclarationParamRole(parameter)
				if index == 0 {
					fact.Role = role
				} else if fact.Role != role {
					return nil, fmt.Errorf("codegen: component parameter facts: %s parameter %d variants have different roles", key, ordinal)
				}
				fact.Decls = append(fact.Decls, fset.Position(component.ParamsPos+token.Pos(parameter.nameOff)))
			}
			facts = append(facts, fact)
		}
	}
	return facts, nil
}

func componentParamReferenceFacts(calls map[*gsxast.Element]ComponentCallFact, declarations []ComponentParamDeclFact, fset *token.FileSet) []ComponentParamRefFact {
	type familyKey struct {
		packagePath  string
		componentKey string
		ordinal      int
	}
	origins := make(map[familyKey]*types.Var, len(declarations))
	for _, declaration := range declarations {
		origins[familyKey{declaration.PackagePath, declaration.ComponentKey, declaration.Ordinal}] = declaration.Origin
	}
	var facts []ComponentParamRefFact
	for _, call := range calls {
		for attr, parameter := range call.Params {
			name, ok := componentInputAttrName(attr)
			if !ok || !attr.Pos().IsValid() || name != parameter.Name {
				continue
			}
			origin := parameter.Origin
			if familyOrigin := origins[familyKey{call.TargetPackage, call.TargetKey, parameter.Ordinal}]; familyOrigin != nil {
				origin = familyOrigin
			}
			if origin == nil && parameter.Var != nil {
				origin = parameter.Var.Origin()
			}
			facts = append(facts, ComponentParamRefFact{
				PackagePath:  call.TargetPackage,
				ComponentKey: call.TargetKey,
				Ordinal:      parameter.Ordinal,
				Name:         parameter.Name,
				Role:         parameter.Role,
				Origin:       origin,
				Ref:          fset.Position(attr.Pos()),
			})
		}
	}
	sort.Slice(facts, func(i, j int) bool {
		if facts[i].PackagePath != facts[j].PackagePath {
			return facts[i].PackagePath < facts[j].PackagePath
		}
		if facts[i].ComponentKey != facts[j].ComponentKey {
			return facts[i].ComponentKey < facts[j].ComponentKey
		}
		if facts[i].Ordinal != facts[j].Ordinal {
			return facts[i].Ordinal < facts[j].Ordinal
		}
		if facts[i].Ref.Filename != facts[j].Ref.Filename {
			return facts[i].Ref.Filename < facts[j].Ref.Filename
		}
		return facts[i].Ref.Offset < facts[j].Ref.Offset
	})
	return facts
}

// componentParamBodyReferenceFacts publishes exact authored references inside
// component bodies. Identity comes exclusively from go/types objects in the
// type-checked skeleton. Positions come exclusively from the byte-identical
// source bridges already retained for definition/hover (ExprMap and CtrlMap).
// No source text or identifier spelling participates in discovery.
func componentParamBodyReferenceFacts(
	declarations []ComponentParamDeclFact,
	objKey map[types.Object]string,
	expressions map[gsxast.Node]goast.Expr,
	controls map[gsxast.Node]ctrlRef,
	info *types.Info,
	fset *token.FileSet,
) []ComponentParamRefFact {
	type familyKey struct {
		component string
		ordinal   int
	}
	byFamily := make(map[familyKey]ComponentParamDeclFact, len(declarations))
	for _, declaration := range declarations {
		byFamily[familyKey{declaration.ComponentKey, declaration.Ordinal}] = declaration
	}
	byOrigin := make(map[*types.Var]ComponentParamDeclFact)
	for object, componentKey := range objKey {
		function, ok := object.(*types.Func)
		if !ok {
			continue
		}
		signature, ok := function.Type().(*types.Signature)
		if !ok {
			continue
		}
		for ordinal := range signature.Params().Len() {
			declaration, ok := byFamily[familyKey{componentKey, ordinal}]
			if !ok {
				continue
			}
			byOrigin[signature.Params().At(ordinal).Origin()] = declaration
		}
	}
	if len(byOrigin) == 0 || info == nil || fset == nil {
		return nil
	}

	var facts []ComponentParamRefFact
	appendRefs := func(node goast.Node, skeletonStart, sourceStart token.Pos, sourceLen int) {
		if node == nil || !skeletonStart.IsValid() || !sourceStart.IsValid() || sourceLen < 0 {
			return
		}
		goast.Inspect(node, func(candidate goast.Node) bool {
			identifier, ok := candidate.(*goast.Ident)
			if !ok {
				return true
			}
			relative := int(identifier.Pos() - skeletonStart)
			if relative < 0 || relative+len(identifier.Name) > sourceLen {
				return true
			}
			variable, ok := info.Uses[identifier].(*types.Var)
			if !ok {
				return true
			}
			declaration, ok := byOrigin[variable.Origin()]
			if !ok {
				return true
			}
			facts = append(facts, ComponentParamRefFact{
				PackagePath:  declaration.PackagePath,
				ComponentKey: declaration.ComponentKey,
				Ordinal:      declaration.Ordinal,
				Name:         declaration.Name,
				Role:         declaration.Role,
				Origin:       declaration.Origin,
				Ref:          fset.Position(sourceStart + token.Pos(relative)),
			})
			return true
		})
	}

	for node, expression := range expressions {
		sourceStart, sourceText, stages, ok := componentExpressionSource(node)
		if !ok || expression == nil {
			continue
		}
		if len(stages) == 0 {
			appendRefs(expression, expression.Pos(), sourceStart, len(sourceText))
			continue
		}
		stageArgs, seed, ok := componentPipeSourceExpressions(expression, len(stages))
		if !ok || seed == nil {
			continue
		}
		appendRefs(seed, seed.Pos(), sourceStart, len(sourceText))
		for index, stage := range stages {
			if !stage.HasArgs || !stage.ArgsPos.IsValid() || len(stageArgs[index]) == 0 {
				continue
			}
			base := stageArgs[index][0].Pos()
			for _, argument := range stageArgs[index] {
				appendRefs(argument, base, stage.ArgsPos, len(stage.Args))
			}
		}
	}
	for node, control := range controls {
		sourceStart, ok := componentControlSourceStart(node)
		if !ok {
			continue
		}
		appendRefs(control.Node, control.ClauseStart, sourceStart, len(ctrlClauseText(node)))
	}

	sort.Slice(facts, func(i, j int) bool {
		if facts[i].PackagePath != facts[j].PackagePath {
			return facts[i].PackagePath < facts[j].PackagePath
		}
		if facts[i].ComponentKey != facts[j].ComponentKey {
			return facts[i].ComponentKey < facts[j].ComponentKey
		}
		if facts[i].Ordinal != facts[j].Ordinal {
			return facts[i].Ordinal < facts[j].Ordinal
		}
		if facts[i].Ref.Filename != facts[j].Ref.Filename {
			return facts[i].Ref.Filename < facts[j].Ref.Filename
		}
		return facts[i].Ref.Offset < facts[j].Ref.Offset
	})
	return facts
}

func componentExpressionSource(node gsxast.Node) (token.Pos, string, []gsxast.PipeStage, bool) {
	switch expression := node.(type) {
	case *gsxast.Interp:
		return expression.ExprPos, expression.Expr, expression.Stages, true
	case *gsxast.ExprAttr:
		return expression.ExprPos, expression.Expr, expression.Stages, true
	case *gsxast.SpreadAttr:
		return expression.ExprPos, expression.Expr, expression.Stages, true
	case *gsxast.OrderedPair:
		return expression.Pos(), expression.Value, nil, true
	case *gsxast.ClassPart:
		if expression.CF == nil && expression.CSSSegments == nil {
			return expression.ExprPos, expression.Expr, expression.Stages, true
		}
	case *gsxast.ValueArm:
		return expression.ExprPos, expression.Expr, expression.Stages, true
	}
	return token.NoPos, "", nil, false
}

func componentPipeSourceExpressions(expression goast.Expr, stageCount int) ([][]goast.Expr, goast.Expr, bool) {
	if stageCount <= 0 {
		return nil, nil, false
	}
	args := make([][]goast.Expr, stageCount)
	current := expression
	for index := stageCount - 1; index >= 0; index-- {
		call, ok := current.(*goast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return nil, nil, false
		}
		if _, ok := call.Fun.(*goast.SelectorExpr); !ok {
			return nil, nil, false
		}
		subject := 0
		if identifier, ok := call.Args[0].(*goast.Ident); ok && identifier.Name == pipeCtxIdent {
			subject = 1
		}
		if subject >= len(call.Args) {
			return nil, nil, false
		}
		args[index] = call.Args[subject+1:]
		current = call.Args[subject]
	}
	for {
		paren, ok := current.(*goast.ParenExpr)
		if !ok {
			break
		}
		current = paren.X
	}
	return args, current, true
}

func componentControlSourceStart(node gsxast.Node) (token.Pos, bool) {
	switch control := node.(type) {
	case *gsxast.ForMarkup:
		return control.ClausePos, control.ClausePos.IsValid()
	case *gsxast.IfMarkup:
		return control.CondPos, control.CondPos.IsValid()
	case *gsxast.GoBlock:
		return control.CodePos, control.CodePos.IsValid()
	case *gsxast.ValueIf:
		return control.CondPos, control.CondPos.IsValid()
	case *gsxast.ValueSwitch:
		return control.TagPos, control.TagPos.IsValid()
	case *gsxast.ValueSwitchCase:
		return control.ListPos, control.ListPos.IsValid()
	case *gsxast.CondAttr:
		return control.CondPos, control.CondPos.IsValid()
	case *gsxast.SwitchMarkup:
		return control.TagPos, control.TagPos.IsValid()
	case *gsxast.CaseClause:
		return control.ListPos, control.ListPos.IsValid()
	case *gsxast.ClassPart:
		return control.CondPos, control.CondPos.IsValid()
	}
	return token.NoPos, false
}

func publishedDeclarationParamRole(parameter componentParamDecl) ComponentParamRole {
	switch parameter.role {
	case declarationParamAttrs:
		return ComponentParamAttrs
	case declarationParamChildren:
		return ComponentParamChildren
	default:
		if parameter.variadic {
			return ComponentParamGoOnlyVariadic
		}
		return ComponentParamOrdinary
	}
}

func componentCallFacts(plan componentPositionalPackagePlan) map[*gsxast.Element]ComponentCallFact {
	if len(plan.byElement) == 0 {
		return nil
	}
	facts := make(map[*gsxast.Element]ComponentCallFact, len(plan.byElement))
	for element, siteID := range plan.byElement {
		site, ok := plan.sites[siteID]
		if !ok {
			continue
		}
		call := ComponentCallFact{
			Target:       site.target.object,
			TargetOrigin: site.target.origin,
			Signature:    site.signature.goSig,
			Params:       make(map[gsxast.Attr]ComponentParamFact),
		}
		identity := call.TargetOrigin
		if identity == nil {
			identity = call.Target
		}
		if identity != nil && identity.Pkg() != nil {
			call.TargetPackage = identity.Pkg().Path()
		}
		call.TargetKey = componentCallTargetKey(identity)
		bind := func(attr gsxast.Attr, paramIndex int) {
			if attr == nil || paramIndex < 0 || paramIndex >= len(site.call.args) {
				return
			}
			param := site.call.args[paramIndex].param
			call.Params[attr] = ComponentParamFact{
				Var:     param.variable,
				Origin:  param.origin,
				Name:    param.name,
				Ordinal: param.index,
				Role:    publishedComponentParamRole(param.role),
			}
		}
		var bindAttrsContributors func(componentAttrsStreamNode, int)
		bindAttrsContributors = func(node componentAttrsStreamNode, paramIndex int) {
			switch node.kind {
			case componentAttrsStreamContributor:
				bind(node.attr, paramIndex)
			case componentAttrsStreamConditional:
				for _, child := range node.then {
					bindAttrsContributors(child, paramIndex)
				}
				for _, child := range node.otherwise {
					bindAttrsContributors(child, paramIndex)
				}
			}
		}
		for _, value := range site.call.values {
			switch value.kind {
			case componentInputProp:
				if attr, ok := value.node.(gsxast.Attr); ok {
					bind(attr, value.paramIndex)
				}
			case componentInputAttrsContributor:
				if value.attrsNode != nil {
					bindAttrsContributors(*value.attrsNode, value.paramIndex)
				}
			case componentInputAttrsSegment:
				if value.attrsNode != nil && value.attrsNode.kind == componentAttrsStreamConditional {
					bindAttrsContributors(*value.attrsNode, value.paramIndex)
				}
			}
		}
		facts[element] = call
	}
	return facts
}

func componentCallTargetKey(object types.Object) string {
	fn, ok := object.(*types.Func)
	if !ok {
		return ""
	}
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return "." + fn.Name()
	}
	recv := types.Unalias(sig.Recv().Type())
	if pointer, ok := recv.(*types.Pointer); ok {
		recv = types.Unalias(pointer.Elem())
	}
	if named, ok := recv.(*types.Named); ok {
		return named.Obj().Name() + "." + fn.Name()
	}
	return ""
}

func publishedComponentParamRole(role paramRole) ComponentParamRole {
	switch role {
	case roleAttrs:
		return ComponentParamAttrs
	case roleChildren:
		return ComponentParamChildren
	case roleGoOnlyVariadic:
		return ComponentParamGoOnlyVariadic
	default:
		return ComponentParamOrdinary
	}
}
