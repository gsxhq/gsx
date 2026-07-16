package codegen

import (
	"go/types"

	gsxast "github.com/gsxhq/gsx/ast"
)

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
