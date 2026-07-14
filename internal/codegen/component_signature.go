package codegen

import (
	"fmt"
	goast "go/ast"
	"go/parser"
	"go/printer"
	"go/token"
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
// the Go parser. It preserves the parsed field grouping so each consumer can
// project the exact model it owns: the shipping Props model remains named-only,
// while the verbatim declaration model retains every logical parameter.
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
