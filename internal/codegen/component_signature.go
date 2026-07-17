package codegen

import (
	"fmt"
	goast "go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"go/types"
	"strconv"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
)

// paramSynthPrefix is the synthetic Go source before the authored parameter
// list. Subtracting its byte length maps parser positions back to Component.Params.
const paramSynthPrefix = "package p\nfunc _("

// parsedParamFieldList is the shared syntax parse of a component parameter
// list. src is trimmed exactly once; offsets into synth become offsets into src
// by subtracting len(paramSynthPrefix).
type parsedParamFieldList struct {
	src   string
	synth string
	fset  *token.FileSet
	list  *goast.FieldList
}

// parseParamFieldList parses a component's raw parameter-list source through
// the Go parser. It preserves field grouping while the declaration model expands
// every authored logical parameter in exact order.
func parseParamFieldList(src string) (parsedParamFieldList, error) {
	src = strings.TrimSpace(src)
	if src == "" {
		return parsedParamFieldList{src: src}, nil
	}
	fset := token.NewFileSet()
	synth := paramSynthPrefix + src + ") {}"
	f, err := parser.ParseFile(fset, "", synth, 0)
	if err != nil {
		return parsedParamFieldList{}, fmt.Errorf("codegen: parse params %q: %w", src, err)
	}
	fn, ok := f.Decls[0].(*goast.FuncDecl)
	if !ok || fn.Type.Params == nil {
		return parsedParamFieldList{}, fmt.Errorf("codegen: parse params %q: unexpected declaration shape", src)
	}
	return parsedParamFieldList{src: src, synth: synth, fset: fset, list: fn.Type.Params}, nil
}

type declarationParamRole uint8

const (
	declarationParamOrdinary declarationParamRole = iota
	declarationParamChildren
	declarationParamAttrs
)

// componentParamDecl is one logical parameter in the exact order authored.
// Grouped declarations produce one entry per name; unnamed declarations produce
// one entry whose nameOff is -1.
type componentParamDecl struct {
	name           string
	normalizedType string
	typeSrc        string
	nameOff        int
	typeOff        int
	typeLen        int
	variadic       bool
	role           declarationParamRole
}

func parseComponentParamDecls(src string) ([]componentParamDecl, error) {
	parsed, err := parseParamFieldList(src)
	if err != nil {
		return nil, err
	}
	if parsed.list == nil {
		return nil, nil
	}

	var out []componentParamDecl
	for _, field := range parsed.list.List {
		var tb strings.Builder
		if err := printer.Fprint(&tb, parsed.fset, field.Type); err != nil {
			return nil, err
		}
		tStart := parsed.fset.Position(field.Type.Pos()).Offset
		tEnd := parsed.fset.Position(field.Type.End()).Offset
		typeOff := tStart - len(paramSynthPrefix)
		base := componentParamDecl{
			normalizedType: tb.String(),
			typeSrc:        parsed.src[typeOff : typeOff+tEnd-tStart],
			nameOff:        -1,
			typeOff:        typeOff,
			typeLen:        tEnd - tStart,
		}
		_, base.variadic = field.Type.(*goast.Ellipsis)
		if len(field.Names) == 0 {
			out = append(out, base)
			continue
		}
		for _, nm := range field.Names {
			p := base
			p.name = nm.Name
			p.nameOff = parsed.fset.Position(nm.Pos()).Offset - len(paramSynthPrefix)
			switch p.name {
			case "children":
				p.role = declarationParamChildren
			case "attrs":
				p.role = declarationParamAttrs
			default:
				p.role = declarationParamOrdinary
			}
			out = append(out, p)
		}
	}
	return out, nil
}

// componentDeclaration is the syntax-only component contract used to compare
// mutually exclusive source variants before semantic type information exists.
type componentDeclaration struct {
	recvType   string
	typeParams string
	params     []componentParamDecl
}

func componentDeclarationFor(c *gsxast.Component) (componentDeclaration, error) {
	var d componentDeclaration
	if c.Recv != "" {
		_, recvType, _, err := parseRecv(c.Recv)
		if err != nil {
			return componentDeclaration{}, err
		}
		d.recvType = recvType
	}
	typeParams, err := normalizedTypeParams(c.TypeParams)
	if err != nil {
		return componentDeclaration{}, err
	}
	d.typeParams = typeParams
	d.params, err = parseComponentParamDecls(c.Params)
	if err != nil {
		return componentDeclaration{}, err
	}
	return d, nil
}

func normalizedTypeParams(src string) (string, error) {
	list, fset, err := parseTypeParamFieldList(src)
	if err != nil {
		return "", err
	}
	if list == nil {
		return "", nil
	}
	var params []string
	for _, field := range list.List {
		if len(field.Names) == 0 {
			return "", fmt.Errorf("codegen: parse type params %q: unnamed type parameter", strings.TrimSpace(src))
		}
		var constraint strings.Builder
		if err := printer.Fprint(&constraint, fset, field.Type); err != nil {
			return "", err
		}
		for _, name := range field.Names {
			params = append(params, name.Name+" "+constraint.String())
		}
	}
	return strings.Join(params, ", "), nil
}

func appendCanonicalField(b *strings.Builder, value string) {
	b.WriteString(strconv.Itoa(len(value)))
	b.WriteByte(':')
	b.WriteString(value)
}

func (d componentDeclaration) canonical() string {
	var b strings.Builder
	b.WriteString("component-declaration-v1")
	appendCanonicalField(&b, d.recvType)
	appendCanonicalField(&b, d.typeParams)
	for _, p := range d.params {
		b.WriteByte('p')
		appendCanonicalField(&b, p.name)
		appendCanonicalField(&b, p.normalizedType)
		if p.variadic {
			b.WriteByte(1)
		} else {
			b.WriteByte(0)
		}
		b.WriteByte(byte(p.role))
	}
	return b.String()
}

// runtimeContract carries the exact runtime type identities needed to classify
// a callable. attrs is explicit because the canonical gsx.Attrs type is passed
// directly, while another defined slice with the same underlying []gsx.Attr
// requires conversion at the eventual call site.
type runtimeContract struct {
	node  types.Type
	attr  types.Type
	attrs types.Type
}

// runtimeContractFromAnalysisPackage derives the runtime identities from the
// package that go/types checked for the analysis skeleton. The skeleton always
// imports the runtime directly under a reserved alias; consulting Imports keeps
// this independent of source aliases and reuses the exact type universe that
// produced the component and operand facts.
func runtimeContractFromAnalysisPackage(analysis *types.Package) (runtimeContract, error) {
	if analysis == nil {
		return runtimeContract{}, fmt.Errorf("component-signature-runtime: nil analysis package")
	}

	var runtimePkg *types.Package
	for _, imported := range analysis.Imports() {
		if imported == nil || imported.Path() != gsxRuntimePath {
			continue
		}
		if runtimePkg != nil && runtimePkg != imported {
			return runtimeContract{}, fmt.Errorf("component-signature-runtime: analysis package %q directly imports multiple semantic package identities for %q", analysis.Path(), gsxRuntimePath)
		}
		runtimePkg = imported
	}
	if runtimePkg == nil {
		return runtimeContract{}, fmt.Errorf("component-signature-runtime: analysis package %q does not directly import %q", analysis.Path(), gsxRuntimePath)
	}
	if !runtimePkg.Complete() {
		return runtimeContract{}, fmt.Errorf("component-signature-runtime: imported runtime package %q is incomplete", gsxRuntimePath)
	}

	lookup := func(name string) (types.Type, error) {
		obj := runtimePkg.Scope().Lookup(name)
		if obj == nil {
			return nil, fmt.Errorf("component-signature-runtime: imported runtime package %q is missing type %s", gsxRuntimePath, name)
		}
		typeName, ok := obj.(*types.TypeName)
		if !ok {
			return nil, fmt.Errorf("component-signature-runtime: imported runtime object %s is not a type name", name)
		}
		if typeName.Pkg() != runtimePkg {
			return nil, fmt.Errorf("component-signature-runtime: imported runtime type %s has a foreign semantic package identity", name)
		}
		typ := typeName.Type()
		if invalidSemanticTypeSeen(typ, make(map[types.Type]bool)) {
			return nil, fmt.Errorf("component-signature-runtime: imported runtime type %s has an incomplete or invalid type", name)
		}
		return typ, nil
	}

	node, err := lookup("Node")
	if err != nil {
		return runtimeContract{}, err
	}
	attr, err := lookup("Attr")
	if err != nil {
		return runtimeContract{}, err
	}
	attrs, err := lookup("Attrs")
	if err != nil {
		return runtimeContract{}, err
	}
	if !attrsSliceHasExactElement(attrs, attr) {
		return runtimeContract{}, fmt.Errorf("component-signature-runtime: imported runtime type Attrs does not have underlying []Attr with the imported Attr identity")
	}
	return runtimeContract{node: node, attr: attr, attrs: attrs}, nil
}

type paramRole uint8

const (
	roleProp paramRole = iota
	roleChildren
	roleAttrs
	roleGoOnlyVariadic
)

type attrsParamMode uint8

const (
	attrsDirect attrsParamMode = iota
	attrsDefinedSlice
	attrsVariadic
)

type componentParam struct {
	variable  *types.Var
	origin    *types.Var
	name      string
	typ       types.Type
	index     int
	role      paramRole
	attrsMode attrsParamMode
}

type componentSignatureModel struct {
	goSig  *types.Signature
	params []componentParam
	result types.Type
}

func analyzeComponentSignature(sig *types.Signature, runtime runtimeContract) (componentSignatureModel, error) {
	if sig == nil {
		return componentSignatureModel{}, fmt.Errorf("component-signature: nil callable signature")
	}
	checkedTypes := make(map[types.Type]bool)
	if invalidSemanticTypeSeen(runtime.node, checkedTypes) || invalidSemanticTypeSeen(runtime.attr, checkedTypes) || invalidSemanticTypeSeen(runtime.attrs, checkedTypes) {
		return componentSignatureModel{}, fmt.Errorf("component-signature-runtime: incomplete runtime type contract")
	}
	if !attrsSliceHasExactElement(runtime.attrs, runtime.attr) {
		return componentSignatureModel{}, fmt.Errorf("component-signature-runtime: canonical attrs type %s does not have underlying []%s", runtime.attrs, runtime.attr)
	}

	result, err := componentResultType(sig, runtime)
	if err != nil {
		return componentSignatureModel{}, err
	}

	model := componentSignatureModel{
		goSig:  sig,
		params: make([]componentParam, sig.Params().Len()),
		result: result,
	}
	for i := range sig.Params().Len() {
		variable := sig.Params().At(i)
		switch variable.Name() {
		case "":
			return componentSignatureModel{}, fmt.Errorf("component-parameter-name: function parameters must be named to be used as a component; parameter %d is unnamed", i)
		case "_":
			return componentSignatureModel{}, fmt.Errorf("component-parameter-name: function parameters must be named to be used as a component; parameter %d is blank", i)
		}
		if invalidSemanticTypeSeen(variable.Type(), checkedTypes) {
			return componentSignatureModel{}, fmt.Errorf("component-param-type: parameter %d %q contains an invalid semantic type", i, variable.Name())
		}
		param := componentParam{
			variable: variable,
			origin:   variable.Origin(),
			name:     variable.Name(),
			typ:      variable.Type(),
			index:    i,
		}
		variadic := sig.Variadic() && i == sig.Params().Len()-1

		switch {
		case param.name == "ctx" || strings.HasPrefix(param.name, "_gsx"):
			return componentSignatureModel{}, fmt.Errorf("component-reserved-param: parameter %d %q is reserved", i, param.name)
		case param.name == "children":
			if !validChildrenParam(param.typ, variadic, runtime.node) {
				return componentSignatureModel{}, fmt.Errorf("component-children-type: parameter %d children has type %s; want %s or ...%s", i, param.typ, runtime.node, runtime.node)
			}
			param.role = roleChildren
		case param.name == "attrs":
			mode, ok := classifyAttrsParam(param.typ, variadic, runtime)
			if !ok {
				return componentSignatureModel{}, fmt.Errorf("component-attrs-type: parameter %d attrs has type %s; want the exact attrs-bag family", i, param.typ)
			}
			param.role = roleAttrs
			param.attrsMode = mode
		case variadic:
			param.role = roleGoOnlyVariadic
		default:
			param.role = roleProp
		}
		model.params[i] = param
	}
	return model, nil
}

func invalidSemanticTypeSeen(t types.Type, seen map[types.Type]bool) bool {
	if t == nil {
		return true
	}
	t = types.Unalias(t)
	if t == nil {
		return true
	}
	if seen[t] {
		return false
	}
	seen[t] = true

	switch t := t.(type) {
	case *types.Basic:
		return t.Kind() == types.Invalid
	case *types.Array:
		return invalidSemanticTypeSeen(t.Elem(), seen)
	case *types.Slice:
		return invalidSemanticTypeSeen(t.Elem(), seen)
	case *types.Struct:
		for field := range t.Fields() {
			if invalidSemanticTypeSeen(field.Type(), seen) {
				return true
			}
		}
	case *types.Pointer:
		return invalidSemanticTypeSeen(t.Elem(), seen)
	case *types.Tuple:
		for variable := range t.Variables() {
			if invalidSemanticTypeSeen(variable.Type(), seen) {
				return true
			}
		}
	case *types.Signature:
		if t.Recv() != nil && invalidSemanticTypeSeen(t.Recv().Type(), seen) {
			return true
		}
		if invalidTypeParamList(t.RecvTypeParams(), seen) || invalidTypeParamList(t.TypeParams(), seen) {
			return true
		}
		if t.Params() != nil && invalidSemanticTypeSeen(t.Params(), seen) {
			return true
		}
		return t.Results() != nil && invalidSemanticTypeSeen(t.Results(), seen)
	case *types.Interface:
		for method := range t.Methods() {
			if invalidSemanticTypeSeen(method.Type(), seen) {
				return true
			}
		}
		for embedded := range t.EmbeddedTypes() {
			if invalidSemanticTypeSeen(embedded, seen) {
				return true
			}
		}
	case *types.Map:
		return invalidSemanticTypeSeen(t.Key(), seen) || invalidSemanticTypeSeen(t.Elem(), seen)
	case *types.Chan:
		return invalidSemanticTypeSeen(t.Elem(), seen)
	case *types.Named:
		for typeArg := range t.TypeArgs().Types() {
			if invalidSemanticTypeSeen(typeArg, seen) {
				return true
			}
		}
		return invalidSemanticTypeSeen(t.Underlying(), seen)
	case *types.TypeParam:
		return invalidSemanticTypeSeen(t.Constraint(), seen)
	case *types.Union:
		for term := range t.Terms() {
			if invalidSemanticTypeSeen(term.Type(), seen) {
				return true
			}
		}
	}
	return false
}

func invalidTypeParamList(list *types.TypeParamList, seen map[types.Type]bool) bool {
	if list == nil {
		return false
	}
	for typeParam := range list.TypeParams() {
		if invalidSemanticTypeSeen(typeParam, seen) {
			return true
		}
	}
	return false
}

func validChildrenParam(t types.Type, variadic bool, node types.Type) bool {
	if !variadic {
		return types.Identical(t, node)
	}
	slice, ok := types.Unalias(t).(*types.Slice)
	return ok && types.Identical(slice.Elem(), node)
}

func classifyAttrsParam(t types.Type, variadic bool, runtime runtimeContract) (attrsParamMode, bool) {
	if variadic {
		slice, ok := types.Unalias(t).(*types.Slice)
		if !ok || !types.Identical(slice.Elem(), runtime.attr) {
			return 0, false
		}
		return attrsVariadic, true
	}

	if types.Identical(t, runtime.attrs) {
		return attrsDirect, true
	}
	unaliased := types.Unalias(t)
	if slice, ok := unaliased.(*types.Slice); ok {
		if types.Identical(slice.Elem(), runtime.attr) {
			return attrsDirect, true
		}
		return 0, false
	}
	if named, ok := unaliased.(*types.Named); ok && attrsSliceHasExactElement(named, runtime.attr) {
		return attrsDefinedSlice, true
	}
	return 0, false
}

func attrsSliceHasExactElement(t, attr types.Type) bool {
	unaliased := types.Unalias(t)
	if named, ok := unaliased.(*types.Named); ok {
		unaliased = named.Underlying()
	}
	slice, ok := unaliased.(*types.Slice)
	return ok && types.Identical(slice.Elem(), attr)
}
