package codegen

import (
	"fmt"
	"go/token"
	"runtime"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/internal/goexprshape"
)

// componentTargetSkeleton is the exact-signature source for one GSX file.
// Discovery bodies retain the shipping probe walk solely for lexical scope and
// liveness, but component call targets are emitted through the separate target
// marker registry. Declaration mode emits the same signatures with inert
// bodies and never consults the shipping Props ABI.
type componentTargetSkeleton struct {
	source          string
	imports         []importSpec
	embeddedMarkups [][]gsxast.Markup
}

// componentTargetEmission is the package-wide declaration plan for one GSX
// component. A cross-file logical variant set emits one public declaration and
// gives every variant body a unique analysis-only name. This keeps all lexical
// bodies type-checked without asking go/types to construct a package from
// redeclared public objects.
type componentTargetEmission struct {
	public            bool
	splitBody         bool
	bodyName          string
	analysisPropsName string
}

type componentTargetPlan struct {
	emissions         map[*gsxast.Component]componentTargetEmission
	logicalKeys       map[*gsxast.Component]string
	families          []componentVariantFamily
	invalidMembership bool
}

func (p componentTargetPlan) emission(component *gsxast.Component) (componentTargetEmission, bool) {
	emission, ok := p.emissions[component]
	return emission, ok
}

// logicalKey returns the public component identity shared by every member of a
// semantically validated build-variant family. Analysis-only private declaration
// names and alternate receiver spellings never become separate LSP identities.
func (p componentTargetPlan) logicalKey(component *gsxast.Component) string {
	if key := p.logicalKeys[component]; key != "" {
		return key
	}
	return componentKey(component)
}

// analysisPreludeSource is the one package-level probe-helper surface shared
// by shipping and exact-target analysis. Keeping it centralized prevents the
// phases from drifting and keeps generator-created declarations out of every
// per-GSX-file skeleton.
func analysisPreludeSource(pkgName string) string {
	return "package " + pkgName + "\n" +
		"func _gsxuse(...any) {}\n" +
		"func _gsxuseq(...any) {}\n" +
		"func _gsxusen(...any) {}\n" +
		"func _gsxcompsig(any) {}\n" +
		"func _gsxunwrap[T any](v T, _ ...any) T { return v }\n" +
		"func _gsxstr(any, ...any) string { return \"\" }\n" +
		"func _gsxelem(int) {}\n"
}

func targetGoWithElementsText(decl *gsxast.GoWithElements, shapes []goexprshape.Result, index int, text gsxast.GoText) string {
	source := text.Src
	if index > 0 && parenWrappable(decl.Parts[index-1], shapes, index-1) {
		source = goexprshape.StripLeadingParen(source)
	}
	if index < len(decl.Parts)-1 && parenWrappable(decl.Parts[index+1], shapes, index+1) {
		source = goexprshape.StripTrailingParen(source)
	}
	return source
}

type componentTargetDeclarationError struct {
	component *gsxast.Component
	code      string
	message   string
}

func (e *componentTargetDeclarationError) Error() string {
	return "codegen: " + e.message
}

func emitExactTargetComponent(
	builder *strings.Builder,
	component *gsxast.Component,
	table funcTables,
	usedFilters map[string]string,
	fset *token.FileSet,
	targets *componentTargetMarkerRegistry,
	goWithElements *[][]gsxast.Markup,
	bag *diag.Bag,
	mode skeletonMode,
	emission componentTargetEmission,
) error {
	if component.Recv != "" {
		if _, _, _, err := parseRecv(component.Recv); err != nil {
			return &componentTargetDeclarationError{component: component, code: "invalid-recv", message: strings.TrimPrefix(err.Error(), "codegen: ")}
		}
		if strings.TrimSpace(component.TypeParams) != "" && !toolchainHasGenericMethods() {
			return &componentTargetDeclarationError{
				component: component,
				code:      "unsupported-toolchain",
				message:   fmt.Sprintf("generic method components require a Go toolchain with generic methods (go1.27+); active toolchain: %s", runtime.Version()),
			}
		}
	}
	declaration, err := componentDeclarationFor(component)
	if err != nil {
		return &componentTargetDeclarationError{component: component, code: "invalid-syntax", message: strings.TrimPrefix(err.Error(), "codegen: ")}
	}
	hasAttrs := false
	for _, param := range declaration.params {
		if param.role == declarationParamAttrs {
			hasAttrs = true
			break
		}
	}

	emit := func(name string, probeBody bool) error {
		emitSkeletonComponentNameLine(builder, fset, component)
		builder.WriteString("func ")
		if component.Recv != "" {
			builder.WriteString(component.Recv)
			builder.WriteByte(' ')
		}
		builder.WriteString(name)
		if component.TypeParams != "" {
			builder.WriteByte('[')
			builder.WriteString(component.TypeParams)
			builder.WriteByte(']')
		}
		builder.WriteByte('(')
		builder.WriteString(component.Params)
		builder.WriteString(") _gsxrt.Node {\n")
		if !probeBody {
			builder.WriteString("return nil\n}\n")
			return nil
		}
		builder.WriteString("var ctx _gsxctx.Context\n_ = ctx\n")
		controlOffsets := make(map[gsxast.Node]int)
		controlTemp := 0
		if err := emitProbes(builder, component.Body, table, "", "", usedFilters, fset, controlOffsets, targets, goWithElements, bag, &controlTemp, hasAttrs); err != nil {
			return err
		}
		builder.WriteString("return nil\n}\n")
		return nil
	}

	switch mode {
	case skeletonTargetDeclarations:
		if !emission.splitBody {
			if !emission.public {
				return fmt.Errorf("codegen: unsplit target component %s has no public declaration", component.Name)
			}
			return emit(component.Name, false)
		}
		if emission.public {
			if err := emit(component.Name, false); err != nil {
				return err
			}
		}
		if emission.bodyName == "" {
			return fmt.Errorf("codegen: split target component %s has no analysis name", component.Name)
		}
		return emit(emission.bodyName, false)
	case skeletonTargetDiscovery:
		if targets == nil {
			return fmt.Errorf("codegen: target discovery component requires a marker registry")
		}
		if !emission.splitBody {
			if !emission.public {
				return fmt.Errorf("codegen: unsplit target component %s has no public declaration", component.Name)
			}
			return emit(component.Name, true)
		}
		if emission.bodyName == "" {
			return fmt.Errorf("codegen: split target component %s has no body name", component.Name)
		}
		if emission.public {
			if err := emit(component.Name, false); err != nil {
				return err
			}
		}
		return emit(emission.bodyName, true)
	default:
		return fmt.Errorf("codegen: exact target component emitted in invalid skeleton mode %d", mode)
	}
}

func emitTargetGoWithElements(
	builder *strings.Builder,
	decl *gsxast.GoWithElements,
	table funcTables,
	usedFilters map[string]string,
	fset *token.FileSet,
	targets *componentTargetMarkerRegistry,
	goWithElements *[][]gsxast.Markup,
	bag *diag.Bag,
	mode skeletonMode,
) error {
	shapes := goWithElementsParenShapes(decl)
	for index, part := range decl.Parts {
		switch part := part.(type) {
		case gsxast.GoText:
			emitSkeletonBlockLine(builder, fset, part.Pos())
			builder.WriteString(targetGoWithElementsText(decl, shapes, index, part))
		case *gsxast.Element:
			builder.WriteString("func() _gsxrt.Node {\n")
			if mode == skeletonTargetDiscovery {
				markup := []gsxast.Markup{part}
				index := len(*goWithElements)
				*goWithElements = append(*goWithElements, markup)
				fmt.Fprintf(builder, "_gsxelem(%d)\n", index)
				builder.WriteString("var ctx _gsxctx.Context\n_ = ctx\n")
				controlTemp := 0
				if err := emitProbes(builder, markup, table, "", "", usedFilters, fset, map[gsxast.Node]int{}, targets, goWithElements, bag, &controlTemp, false); err != nil {
					return err
				}
			}
			builder.WriteString("return nil\n}()")
		case *gsxast.Fragment:
			builder.WriteString("func() _gsxrt.Node {\n")
			if mode == skeletonTargetDiscovery {
				index := len(*goWithElements)
				*goWithElements = append(*goWithElements, part.Children)
				fmt.Fprintf(builder, "_gsxelem(%d)\n", index)
				builder.WriteString("var ctx _gsxctx.Context\n_ = ctx\n")
				controlTemp := 0
				if err := emitProbes(builder, part.Children, table, "", "", usedFilters, fset, map[gsxast.Node]int{}, targets, goWithElements, bag, &controlTemp, false); err != nil {
					return err
				}
			}
			builder.WriteString("return nil\n}()")
		case *gsxast.EmbeddedInterp:
			if mode == skeletonTargetDeclarations {
				switch part.Lang {
				case gsxast.EmbeddedJS:
					builder.WriteString("_gsxrt.RawJS(\"\")")
				case gsxast.EmbeddedCSS:
					builder.WriteString("_gsxrt.RawCSS(\"\")")
				default:
					builder.WriteString("\"\"")
				}
				continue
			}
			if len(part.Stages) > 0 {
				return fmt.Errorf("codegen: whole-literal pipelines on a Go-expression backtick literal are not supported")
			}
			controlTemp := 0
			if err := probeEmbeddedInterpIIFE(builder, part.Segments, part.Lang, table, "", "", usedFilters, fset, map[gsxast.Node]int{}, targets, goWithElements, bag, &controlTemp); err != nil {
				return err
			}
		default:
			return fmt.Errorf("codegen: unsupported target Go-expression part %T", part)
		}
	}
	builder.WriteByte('\n')
	return nil
}

func buildComponentTargetSkeleton(
	file *gsxast.File,
	table funcTables,
	fset *token.FileSet,
	bag *diag.Bag,
	targets *componentTargetMarkerRegistry,
	plan componentTargetPlan,
	mode skeletonMode,
) (componentTargetSkeleton, error) {
	if mode != skeletonTargetDiscovery && mode != skeletonTargetDeclarations {
		return componentTargetSkeleton{}, fmt.Errorf("codegen: invalid target skeleton mode %d", mode)
	}
	if mode == skeletonTargetDiscovery && targets == nil {
		return componentTargetSkeleton{}, fmt.Errorf("codegen: target discovery skeleton requires a marker registry")
	}
	if plan.emissions == nil {
		return componentTargetSkeleton{}, fmt.Errorf("codegen: target skeleton requires a package component plan")
	}

	imports, bodies, err := splitFileGoSource(file, fset)
	if err != nil {
		return componentTargetSkeleton{}, err
	}
	usedFilters := make(map[string]string)
	var embedded [][]gsxast.Markup
	var body strings.Builder
	markerStart := 0
	if targets != nil {
		markerStart = len(targets.ordered)
	}
	for _, declaration := range file.Decls {
		component, ok := declaration.(*gsxast.Component)
		if !ok {
			continue
		}
		emission, ok := plan.emission(component)
		if !ok {
			return componentTargetSkeleton{}, fmt.Errorf("codegen: component %s is absent from the package target plan", component.Name)
		}
		if err := emitExactTargetComponent(&body, component, table, usedFilters, fset, targets, &embedded, bag, mode, emission); err != nil {
			if declarationErr, ok := err.(*componentTargetDeclarationError); ok {
				bag.Errorf(declarationErr.component.Pos(), declarationErr.component.End(), declarationErr.code, "%s", declarationErr.message)
				continue
			}
			return componentTargetSkeleton{}, err
		}
	}
	for _, declaration := range file.Decls {
		withElements, ok := declaration.(*gsxast.GoWithElements)
		if !ok {
			continue
		}
		if err := emitTargetGoWithElements(&body, withElements, table, usedFilters, fset, targets, &embedded, bag, mode); err != nil {
			return componentTargetSkeleton{}, err
		}
	}

	var source strings.Builder
	fmt.Fprintf(&source, "package %s\n", file.Package)
	source.WriteString("import _gsxrt \"github.com/gsxhq/gsx\"\n")
	source.WriteString("import _gsxctx \"context\"\n")
	for _, alias := range sortedFilterAliases(usedFilters) {
		fmt.Fprintf(&source, "import %s %q\n", alias, usedFilters[alias])
	}
	for _, spec := range imports {
		emitSkeletonLineImport(&source, fset, spec.pos)
		if spec.name != "" {
			fmt.Fprintf(&source, "import %s %q\n", spec.name, spec.path)
		} else {
			fmt.Fprintf(&source, "import %q\n", spec.path)
		}
	}
	source.WriteString("var _ _gsxrt.Node\nvar _ _gsxctx.Context\n")
	bodyStart := source.Len()
	source.WriteString(body.String())
	if targets != nil {
		targets.adjustFrom(markerStart, bodyStart)
	}
	for _, goBody := range bodies {
		emitSkeletonLine(&source, fset, goBody.pos)
		source.WriteString(goBody.src)
		source.WriteByte('\n')
	}
	return componentTargetSkeleton{
		source:          source.String(),
		imports:         imports,
		embeddedMarkups: embedded,
	}, nil
}
