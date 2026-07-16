package codegen

import gsxast "github.com/gsxhq/gsx/ast"

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
