package codegen

import (
	"errors"
	"fmt"
	goast "go/ast"
	goparser "go/parser"
	"go/scanner"
	"go/token"
	"go/types"
	"maps"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/attrclass"
	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/internal/jsx"
)

type componentTargetProvenance uint8

const (
	targetPackageFunc componentTargetProvenance = iota + 1
	targetPackageVar
	targetConcreteMethodValue
)

type componentTargetRejection uint8

const (
	componentTargetAccepted componentTargetRejection = iota
	componentTargetDefinitiveNonCallablePackageObject
	componentTargetUnresolved
	componentTargetDisallowedProvenance
)

type authoredTypeArgFact struct {
	expr goast.Expr
	typ  types.Type
}

// componentTargetFact is the immutable target-only semantic record for one
// planned component call. Nullable fields are intentional: discovery produces
// one fact for every planned call site even when lookup, provenance, or explicit
// type arguments fail, and Task 5 decides diagnostic precedence after authored
// operands have been checked.
type componentTargetFact struct {
	site callSiteID
	expr goast.Expr

	object types.Object
	origin types.Object
	raw    *types.Signature

	authoredTypeArgs []authoredTypeArgFact
	explicitInstance *types.Instance
	targetDiags      []diag.Diagnostic

	provenance componentTargetProvenance
	rejection  componentTargetRejection

	hasSelection  bool
	selectionKind types.SelectionKind
	selectionRecv types.Type

	usesImportedQualifier bool
}

func (f componentTargetFact) effectiveSignature() *types.Signature {
	if f.explicitInstance != nil {
		if sig, ok := f.explicitInstance.Type.(*types.Signature); ok {
			return sig
		}
	}
	return f.raw
}

type componentTargetSourceSegment struct {
	outputStart int
	outputEnd   int
	sourceStart token.Pos
}

type parsedComponentTargetExpression struct {
	source     string
	expr       goast.Expr
	diagnostic *diag.Diagnostic
	segments   []componentTargetSourceSegment
}

func (p parsedComponentTargetExpression) sourcePos(outputOffset int) token.Pos {
	for _, segment := range p.segments {
		if segment.outputStart <= outputOffset && outputOffset < segment.outputEnd {
			return segment.sourceStart + token.Pos(outputOffset-segment.outputStart)
		}
	}
	if len(p.segments) > 0 && outputOffset == len(p.source) {
		last := p.segments[len(p.segments)-1]
		return last.sourceStart + token.Pos(last.outputEnd-last.outputStart)
	}
	return token.NoPos
}

// parseComponentTargetExpression validates one target independently, before it
// can enter the package discovery skeleton. Its source map is built from parser-
// recorded positions rather than inferred from trimmed strings, so diagnostics
// inside multiline arguments and at a whitespace-separated closing bracket
// still resolve to the authored bytes.
func parseComponentTargetExpression(element *gsxast.Element, fset *token.FileSet) (parsedComponentTargetExpression, error) {
	if element == nil {
		return parsedComponentTargetExpression{}, fmt.Errorf("codegen: nil component target element")
	}
	if element.Tag == "" || !element.TagPos.IsValid() {
		return parsedComponentTargetExpression{}, fmt.Errorf("codegen: component target is missing parser-recorded tag identity")
	}

	var target strings.Builder
	parsed := parsedComponentTargetExpression{}
	appendSegment := func(src string, pos token.Pos) error {
		if src == "" {
			return nil
		}
		if !pos.IsValid() {
			return fmt.Errorf("codegen: component target segment %q has no parser-recorded position", src)
		}
		start := target.Len()
		target.WriteString(src)
		parsed.segments = append(parsed.segments, componentTargetSourceSegment{
			outputStart: start,
			outputEnd:   target.Len(),
			sourceStart: pos,
		})
		return nil
	}
	if err := appendSegment(element.Tag, element.TagPos); err != nil {
		return parsedComponentTargetExpression{}, err
	}
	if element.TypeArgs != "" {
		if err := appendSegment("[", element.TypeArgsOpenPos); err != nil {
			return parsedComponentTargetExpression{}, err
		}
		if err := appendSegment(element.TypeArgs, element.TypeArgsPos); err != nil {
			return parsedComponentTargetExpression{}, err
		}
		if err := appendSegment("]", element.TypeArgsClosePos); err != nil {
			return parsedComponentTargetExpression{}, err
		}
	} else if element.TypeArgsOpenPos.IsValid() || element.TypeArgsPos.IsValid() || element.TypeArgsClosePos.IsValid() {
		return parsedComponentTargetExpression{}, fmt.Errorf("codegen: component target <%s> records type-argument positions without type arguments", element.Tag)
	}
	parsed.source = target.String()

	parseFset := token.NewFileSet()
	expr, err := goparser.ParseExprFrom(parseFset, "component-target", parsed.source, goparser.SkipObjectResolution)
	if err == nil {
		if expr == nil {
			return parsedComponentTargetExpression{}, fmt.Errorf("codegen: component target parser returned no expression for %q", parsed.source)
		}
		parsed.expr = expr
		return parsed, nil
	}
	var list scanner.ErrorList
	if !errors.As(err, &list) || len(list) == 0 {
		return parsedComponentTargetExpression{}, fmt.Errorf("codegen: component target parser returned an unpositioned error: %w", err)
	}
	position := parsed.sourcePos(list[0].Pos.Offset)
	diagnostic := diag.Diagnostic{
		Severity: diag.Error,
		Code:     "parse-error",
		Message:  list[0].Msg,
		Source:   "parser",
	}
	if fset != nil && position.IsValid() {
		diagnostic.Start = fset.Position(position)
		diagnostic.End = diagnostic.Start
	}
	parsed.diagnostic = &diagnostic
	return parsed, nil
}

type componentTargetRawSpan struct {
	start int
	end   int
}

type componentTargetMarker struct {
	site       callSiteID
	element    *gsxast.Element
	identifier string
	source     string
	rawSpan    componentTargetRawSpan

	syntaxDiagnostic *diag.Diagnostic
	file             *goast.File
	valueSpec        *goast.ValueSpec
	expr             goast.Expr
}

// componentTargetMarkerRegistry owns only target-discovery bindings. Call-site
// identity remains in callSiteRegistry; keeping the marker table separate makes
// it impossible for syntax/type facts to mutate the preprocessing contract.
type componentTargetMarkerRegistry struct {
	callSites *callSiteRegistry
	bySite    map[callSiteID]*componentTargetMarker
	ordered   []*componentTargetMarker
}

func newComponentTargetMarkerRegistry(callSites *callSiteRegistry) (*componentTargetMarkerRegistry, error) {
	if callSites == nil {
		return nil, fmt.Errorf("codegen: target discovery requires a complete call-site registry")
	}
	return &componentTargetMarkerRegistry{
		callSites: callSites,
		bySite:    make(map[callSiteID]*componentTargetMarker),
	}, nil
}

func componentTargetMarkerName(site callSiteID) string {
	return fmt.Sprintf("_gsxtarget%d", site)
}

func (r *componentTargetMarkerRegistry) hasCandidate(element *gsxast.Element) bool {
	return r != nil && r.callSites != nil && r.callSites.hasCandidate(element)
}

func (r *componentTargetMarkerRegistry) emitBinding(sb skeletonWriter, element *gsxast.Element, fset *token.FileSet) error {
	if r == nil || r.callSites == nil {
		return fmt.Errorf("codegen: target binding emitted without a marker registry")
	}
	site, ok := r.callSites.byElement[element]
	if !ok {
		return fmt.Errorf("codegen: component element <%s> has no call-site identity", element.Tag)
	}
	record := r.callSites.records[site-1]
	if record.disposition != componentSiteCandidate {
		return fmt.Errorf("codegen: preserved call site %d entered target discovery", site)
	}
	if _, duplicate := r.bySite[site]; duplicate {
		return fmt.Errorf("codegen: target call site %d was emitted more than once", site)
	}

	parsed, err := parseComponentTargetExpression(element, fset)
	if err != nil {
		return err
	}
	marker := &componentTargetMarker{
		site:             site,
		element:          element,
		identifier:       componentTargetMarkerName(site),
		source:           parsed.source,
		syntaxDiagnostic: parsed.diagnostic,
	}
	r.bySite[site] = marker
	r.ordered = append(r.ordered, marker)

	if parsed.diagnostic != nil {
		fmt.Fprintf(sb, "var %s any\n_ = %s\n", marker.identifier, marker.identifier)
		return nil
	}

	fmt.Fprintf(sb, "var %s = ", marker.identifier)
	for i, segment := range parsed.segments {
		emitSkeletonBlockLine(sb, fset, segment.sourceStart)
		if i == 0 {
			marker.rawSpan.start = sb.Len()
		}
		sb.WriteString(parsed.source[segment.outputStart:segment.outputEnd])
	}
	marker.rawSpan.end = sb.Len()
	fmt.Fprintf(sb, "\n_ = %s\n", marker.identifier)
	return nil
}

func (r *componentTargetMarkerRegistry) adjustFrom(first, delta int) {
	for _, marker := range r.ordered[first:] {
		if marker.rawSpan.end > marker.rawSpan.start {
			marker.rawSpan.start += delta
			marker.rawSpan.end += delta
		}
	}
}

type componentTargetSeedBoundary struct {
	open  string
	close string
}

func markComponentTargetSeed(first callSiteID, seed string) (string, componentTargetSeedBoundary) {
	boundary := componentTargetSeedBoundary{
		open:  fmt.Sprintf("\x00gsx-target-seed-%d-open\x00", first),
		close: fmt.Sprintf("\x00gsx-target-seed-%d-close\x00", first),
	}
	return boundary.open + seed + boundary.close, boundary
}

// unmarkComponentTargetSeed recovers an exact source-map offset after a pure
// string rewrite such as pipeline lowering. NUL-delimited sentinels cannot
// occur in valid authored Go source, and the rewrite must retain each exactly
// once; failure is an internal error, never a guessed offset.
func unmarkComponentTargetSeed(rewritten string, boundary componentTargetSeedBoundary) (clean string, seedOffset int, err error) {
	if strings.Count(rewritten, boundary.open) != 1 || strings.Count(rewritten, boundary.close) != 1 {
		return "", 0, fmt.Errorf("codegen: target seed rewrite did not preserve exact source-map boundaries")
	}
	open := strings.Index(rewritten, boundary.open)
	close := strings.Index(rewritten, boundary.close)
	if close < open+len(boundary.open) {
		return "", 0, fmt.Errorf("codegen: target seed rewrite reordered source-map boundaries")
	}
	withoutOpen := rewritten[:open] + rewritten[open+len(boundary.open):]
	close -= len(boundary.open)
	clean = withoutOpen[:close] + withoutOpen[close+len(boundary.close):]
	return clean, open, nil
}

func (r *componentTargetMarkerRegistry) validateComplete() error {
	for _, record := range r.callSites.records {
		_, emitted := r.bySite[record.id]
		switch record.disposition {
		case componentSiteCandidate:
			if !emitted {
				return fmt.Errorf("codegen: planned call site %d <%s> has no target marker", record.id, record.element.Tag)
			}
		case componentSitePreservedInvalidRegion:
			if emitted {
				return fmt.Errorf("codegen: preserved call site %d <%s> has a target marker", record.id, record.element.Tag)
			}
		default:
			return fmt.Errorf("codegen: call site %d has unknown disposition %d", record.id, record.disposition)
		}
	}
	return nil
}

// bindComponentTargetMarkers assigns only the markers emitted while building
// one exact discovery skeleton to that skeleton's parsed AST. Companion source
// is never searched by identifier spelling: a user's ordinary
// `_gsxtargetN` declaration is unrelated even when it appears in the same
// package.
func bindComponentTargetMarkers(file *goast.File, first int, fset *token.FileSet, registry *componentTargetMarkerRegistry) error {
	if registry == nil {
		return fmt.Errorf("codegen: cannot bind a nil target marker registry")
	}
	if file == nil {
		return fmt.Errorf("codegen: cannot bind target markers to a nil discovery file")
	}
	if first < 0 || first > len(registry.ordered) {
		return fmt.Errorf("codegen: target marker bind range starts at %d with %d markers", first, len(registry.ordered))
	}
	wanted := make(map[string]*componentTargetMarker, len(registry.ordered)-first)
	for _, marker := range registry.ordered[first:] {
		wanted[marker.identifier] = marker
	}
	goast.Inspect(file, func(node goast.Node) bool {
		spec, ok := node.(*goast.ValueSpec)
		if !ok {
			return true
		}
		for _, name := range spec.Names {
			marker := wanted[name.Name]
			if marker == nil {
				continue
			}
			if marker.valueSpec != nil {
				// Preserve the duplicate and let validation below report it.
				marker.file = nil
				continue
			}
			marker.file = file
			marker.valueSpec = spec
		}
		return true
	})

	for _, marker := range registry.ordered[first:] {
		if marker.valueSpec == nil {
			return fmt.Errorf("codegen: target marker %s was not found in the parsed discovery skeleton", marker.identifier)
		}
		if marker.file == nil {
			return fmt.Errorf("codegen: target marker %s occurs more than once in the discovery skeleton", marker.identifier)
		}
		if len(marker.valueSpec.Names) != 1 || marker.valueSpec.Names[0].Name != marker.identifier {
			return fmt.Errorf("codegen: target marker %s has an unexpected declaration shape", marker.identifier)
		}
		if marker.syntaxDiagnostic != nil {
			if len(marker.valueSpec.Values) != 0 {
				return fmt.Errorf("codegen: syntax-invalid target marker %s unexpectedly has a value", marker.identifier)
			}
			continue
		}
		if len(marker.valueSpec.Values) != 1 {
			return fmt.Errorf("codegen: target marker %s has %d values; want one", marker.identifier, len(marker.valueSpec.Values))
		}
		marker.expr = marker.valueSpec.Values[0]
		start := fset.PositionFor(marker.expr.Pos(), false).Offset
		end := fset.PositionFor(marker.expr.End(), false).Offset
		if start != marker.rawSpan.start || end != marker.rawSpan.end {
			return fmt.Errorf("codegen: target marker %s raw expression span is [%d,%d), recorded [%d,%d)", marker.identifier, start, end, marker.rawSpan.start, marker.rawSpan.end)
		}
	}
	return nil
}

func validateComponentTargetMarkers(registry *componentTargetMarkerRegistry) error {
	if registry == nil {
		return fmt.Errorf("codegen: cannot validate a nil target marker registry")
	}
	for _, marker := range registry.ordered {
		if marker.file == nil || marker.valueSpec == nil {
			return fmt.Errorf("codegen: target marker %s has no exact discovery-file binding", marker.identifier)
		}
	}
	byFile := make(map[*goast.File][]*componentTargetMarker)
	for _, marker := range registry.ordered {
		if marker.expr != nil {
			byFile[marker.file] = append(byFile[marker.file], marker)
		}
	}
	for _, markers := range byFile {
		sort.Slice(markers, func(i, j int) bool { return markers[i].rawSpan.start < markers[j].rawSpan.start })
		for i := 1; i < len(markers); i++ {
			if markers[i].rawSpan.start < markers[i-1].rawSpan.end {
				return fmt.Errorf("codegen: target marker spans %s and %s overlap", markers[i-1].identifier, markers[i].identifier)
			}
		}
	}
	return nil
}

type componentTargetShape struct {
	supplier *goast.Ident
	selector *goast.SelectorExpr
	typeArgs []goast.Expr
}

func componentTargetShapeOf(expr goast.Expr) (componentTargetShape, bool) {
	shape := componentTargetShape{}
	base := expr
	switch indexed := expr.(type) {
	case *goast.IndexExpr:
		base = indexed.X
		shape.typeArgs = []goast.Expr{indexed.Index}
	case *goast.IndexListExpr:
		base = indexed.X
		shape.typeArgs = append([]goast.Expr(nil), indexed.Indices...)
	}
	switch target := base.(type) {
	case *goast.Ident:
		shape.supplier = target
	case *goast.SelectorExpr:
		shape.supplier = target.Sel
		shape.selector = target
	default:
		return componentTargetShape{}, false
	}
	return shape, true
}

func targetCallableSignature(typ types.Type) *types.Signature {
	if typ == nil {
		return nil
	}
	unaliased := types.Unalias(typ)
	if signature, ok := unaliased.(*types.Signature); ok {
		return signature
	}
	signature, _ := unaliased.Underlying().(*types.Signature)
	return signature
}

func targetDeclaredReceiverIsInterface(fn *types.Func) bool {
	if fn == nil {
		return false
	}
	signature, _ := fn.Type().(*types.Signature)
	if signature == nil || signature.Recv() == nil {
		return false
	}
	_, ok := types.Unalias(signature.Recv().Type()).Underlying().(*types.Interface)
	return ok
}

func definitiveNonCallablePackageObject(object types.Object) bool {
	if object == nil || object.Pkg() == nil || object.Parent() != object.Pkg().Scope() {
		return false
	}
	switch object := object.(type) {
	case *types.Const, *types.TypeName:
		return true
	case *types.Var:
		return targetCallableSignature(object.Type()) == nil
	default:
		return false
	}
}

func componentTargetDiagnostic(element *gsxast.Element, fset *token.FileSet, code, message string) diag.Diagnostic {
	diagnostic := diag.Diagnostic{Severity: diag.Error, Code: code, Message: message, Source: "codegen"}
	if fset == nil || element == nil {
		return diagnostic
	}
	start := element.TagPos
	end := start + token.Pos(len(element.Tag))
	if element.TypeArgsClosePos.IsValid() {
		end = element.TypeArgsClosePos + 1
	}
	diagnostic.Start = fset.Position(start)
	diagnostic.End = fset.Position(end)
	return diagnostic
}

func componentTargetTypeDiagnostic(typeErr types.Error) diag.Diagnostic {
	position := typeErr.Fset.Position(typeErr.Pos)
	return diag.Diagnostic{
		Start:    position,
		End:      position,
		Severity: diag.Error,
		Message:  typeErr.Msg,
		Source:   "types",
	}
}

func componentTargetImportObjects(files []*goast.File, info *types.Info) map[*goast.File]map[*types.PkgName]bool {
	imports := make(map[*goast.File]map[*types.PkgName]bool, len(files))
	for _, file := range files {
		set := make(map[*types.PkgName]bool)
		for _, spec := range file.Imports {
			var object types.Object
			if spec.Name != nil {
				object = info.Defs[spec.Name]
			} else {
				object = info.Implicits[spec]
			}
			if pkgName, ok := object.(*types.PkgName); ok {
				set[pkgName] = true
			}
		}
		imports[file] = set
	}
	return imports
}

func rawTargetErrorPosition(typeErr types.Error) (*token.File, int) {
	if typeErr.Fset == nil || !typeErr.Pos.IsValid() {
		return nil, -1
	}
	file := typeErr.Fset.File(typeErr.Pos)
	if file == nil {
		return nil, -1
	}
	return file, file.Offset(typeErr.Pos)
}

func markerRawTokenFile(marker *componentTargetMarker, fset *token.FileSet) *token.File {
	if marker == nil || marker.expr == nil || fset == nil {
		return nil
	}
	return fset.File(marker.expr.Pos())
}

func expectedIncompleteGenericTarget(marker *componentTargetMarker, fact componentTargetFact, siteErrors []types.Error, fset *token.FileSet) bool {
	if marker == nil || fact.raw == nil || len(siteErrors) != 1 {
		return false
	}
	typeParams := fact.raw.TypeParams()
	if typeParams == nil || typeParams.Len() == 0 || len(fact.authoredTypeArgs) >= typeParams.Len() {
		return false
	}
	file, offset := rawTargetErrorPosition(siteErrors[0])
	return file == markerRawTokenFile(marker, fset) && offset == marker.rawSpan.start
}

// harvestComponentTargetFacts turns the separately type-checked discovery
// skeleton into one total fact per planned site. Type errors are partitioned by
// exact raw marker spans; errors outside every marker remain unrelated and must
// be handled by the caller as package failures.
func harvestComponentTargetFacts(files []*goast.File, fset *token.FileSet, info *types.Info, typeErrs []types.Error, registry *componentTargetMarkerRegistry) (map[callSiteID]componentTargetFact, []types.Error, error) {
	if info == nil {
		return nil, nil, fmt.Errorf("codegen: target discovery has no types.Info")
	}
	if err := validateComponentTargetMarkers(registry); err != nil {
		return nil, nil, err
	}

	siteErrors := make(map[callSiteID][]types.Error)
	var unrelated []types.Error
	for _, typeErr := range typeErrs {
		if typeErr.Fset != fset {
			return nil, nil, fmt.Errorf("codegen: target checker error uses a different FileSet")
		}
		file, offset := rawTargetErrorPosition(typeErr)
		var owner *componentTargetMarker
		for _, marker := range registry.ordered {
			if marker.expr == nil || file != markerRawTokenFile(marker, fset) {
				continue
			}
			if marker.rawSpan.start <= offset && offset < marker.rawSpan.end {
				if owner != nil {
					return nil, nil, fmt.Errorf("codegen: type error at raw offset %d belongs to overlapping target markers", offset)
				}
				owner = marker
			}
		}
		if owner == nil {
			unrelated = append(unrelated, typeErr)
			continue
		}
		siteErrors[owner.site] = append(siteErrors[owner.site], typeErr)
	}

	importObjects := componentTargetImportObjects(files, info)
	facts := make(map[callSiteID]componentTargetFact, len(registry.ordered))
	for _, marker := range registry.ordered {
		fact := componentTargetFact{site: marker.site, expr: marker.expr}
		if marker.syntaxDiagnostic != nil {
			fact.rejection = componentTargetDisallowedProvenance
			fact.targetDiags = []diag.Diagnostic{*marker.syntaxDiagnostic}
			facts[marker.site] = fact
			continue
		}

		shape, ok := componentTargetShapeOf(marker.expr)
		if !ok {
			fact.rejection = componentTargetDisallowedProvenance
			fact.targetDiags = append(fact.targetDiags, componentTargetDiagnostic(marker.element, fset, "invalid-component-target", "component target must be an identifier, selector, or explicit generic instantiation"))
			facts[marker.site] = fact
			continue
		}
		for _, arg := range shape.typeArgs {
			fact.authoredTypeArgs = append(fact.authoredTypeArgs, authoredTypeArgFact{expr: arg, typ: info.TypeOf(arg)})
		}
		fact.object = info.Uses[shape.supplier]
		errs := siteErrors[marker.site]
		if shape.selector != nil {
			qualifier, isIdent := shape.selector.X.(*goast.Ident)
			pkgName, isPkgName := info.Uses[qualifier].(*types.PkgName)
			fact.usesImportedQualifier = isIdent && isPkgName && importObjects[marker.file][pkgName]
		}

		selection := info.Selections[shape.selector]
		if selection != nil {
			fact.hasSelection = true
			fact.selectionKind = selection.Kind()
			fact.selectionRecv = selection.Recv()
		}

		var provenanceMessage string
		switch {
		case selection != nil:
			fn, isMethod := selection.Obj().(*types.Func)
			switch {
			case selection.Kind() == types.MethodExpr:
				provenanceMessage = "component target is a method expression; bind it to a concrete receiver value"
			case selection.Kind() == types.FieldVal:
				provenanceMessage = "component target is a function-valued field; only package function variables and concrete bound methods are supported"
			case selection.Kind() != types.MethodVal || !isMethod:
				provenanceMessage = "component target is not a concrete bound method value"
			case targetDeclaredReceiverIsInterface(fn):
				provenanceMessage = "component target dispatches through an interface; a concrete bound method is required"
			default:
				fact.object = fn
				fact.origin = fn.Origin()
				fact.raw, _ = selection.Type().(*types.Signature)
				if fact.raw == nil {
					provenanceMessage = "component target method does not have a callable signature"
				} else {
					fact.provenance = targetConcreteMethodValue
				}
			}
		case fact.object == nil:
			fact.rejection = componentTargetUnresolved
			provenanceMessage = "component target could not be resolved"
		case invalidSemanticTypeSeen(fact.object.Type(), make(map[types.Type]bool)):
			fact.rejection = componentTargetUnresolved
			provenanceMessage = "component target has an incomplete semantic type"
		default:
			if shape.selector != nil {
				qualifier, isIdent := shape.selector.X.(*goast.Ident)
				pkgName, isPkgName := info.Uses[qualifier].(*types.PkgName)
				if !isIdent || !isPkgName || !importObjects[marker.file][pkgName] || fact.object.Pkg() != pkgName.Imported() {
					provenanceMessage = "component target selector is not qualified by this file's imported package object"
					break
				}
			}
			switch object := fact.object.(type) {
			case *types.Func:
				if object.Pkg() == nil || object.Parent() != object.Pkg().Scope() {
					provenanceMessage = "component target is a local or parameter function; a package function is required"
					break
				}
				fact.origin = object.Origin()
				fact.raw, _ = object.Type().(*types.Signature)
				if fact.raw == nil {
					provenanceMessage = "component target function does not have a callable signature"
				} else {
					fact.provenance = targetPackageFunc
				}
			case *types.Var:
				if object.Pkg() == nil || object.Parent() != object.Pkg().Scope() {
					provenanceMessage = "component target is a local or parameter variable; a package function variable is required"
					break
				}
				fact.origin = object.Origin()
				fact.raw = targetCallableSignature(object.Type())
				if fact.raw == nil {
					fact.rejection = componentTargetDefinitiveNonCallablePackageObject
					provenanceMessage = "component target package variable is not callable"
				} else {
					fact.provenance = targetPackageVar
				}
			default:
				if definitiveNonCallablePackageObject(object) {
					fact.rejection = componentTargetDefinitiveNonCallablePackageObject
				} else {
					fact.rejection = componentTargetDisallowedProvenance
				}
				provenanceMessage = "component target is not a package function, package function variable, or concrete bound method"
			}
		}
		provenanceRejected := provenanceMessage != ""
		lookupSucceeded := fact.object != nil
		omitIncompleteGeneric := expectedIncompleteGenericTarget(marker, fact, errs, fset)
		if provenanceRejected {
			if fact.rejection == componentTargetAccepted {
				fact.rejection = componentTargetDisallowedProvenance
			}
			fact.object = nil
			fact.raw = nil
			fact.origin = nil
			fact.provenance = 0
			// An unresolved object already has an exact checker diagnostic in this
			// marker span. Resolved-but-disallowed semantic shapes additionally get
			// positioned provenance guidance; their native checker diagnostics remain
			// intact below.
			if lookupSucceeded || len(errs) == 0 {
				fact.targetDiags = append(fact.targetDiags, componentTargetDiagnostic(marker.element, fset, "invalid-component-target", provenanceMessage))
			}
		}

		if !omitIncompleteGeneric {
			for _, typeErr := range errs {
				fact.targetDiags = append(fact.targetDiags, componentTargetTypeDiagnostic(typeErr))
			}
		}
		if fact.provenance != 0 && len(errs) == 0 {
			if instance, ok := info.Instances[shape.supplier]; ok {
				copy := instance
				if _, ok := copy.Type.(*types.Signature); ok {
					fact.explicitInstance = &copy
				}
			}
		}
		facts[marker.site] = fact
	}
	return facts, unrelated, nil
}

func incompleteComponentResult(sig *types.Signature) bool {
	if sig == nil || sig.Results().Len() != 1 {
		return false
	}
	return invalidSemanticTypeSeen(sig.Results().At(0).Type(), make(map[types.Type]bool))
}

func (r *callSiteRegistry) finalizeComponentIdentity(facts map[callSiteID]componentTargetFact, runtime runtimeContract, fset *token.FileSet, bag *diag.Bag) error {
	if r == nil {
		return fmt.Errorf("codegen: cannot finalize a nil call-site registry")
	}
	if r.finalized {
		return fmt.Errorf("codegen: component identity already finalized")
	}
	if bag == nil {
		return fmt.Errorf("codegen: component identity finalization requires a diagnostic bag")
	}
	for _, element := range r.leafTypeArgs {
		reportLeafTypeArgs(bag, element)
	}
	for i := range r.records {
		record := &r.records[i]
		if record.element == nil {
			return fmt.Errorf("codegen: call site %d has no element", record.id)
		}
		if record.element.IsComponent {
			return fmt.Errorf("codegen: call site %d <%s> was stamped before semantic finalization", record.id, record.element.Tag)
		}
		if record.disposition == componentSitePreservedInvalidRegion {
			continue
		}
		if record.disposition != componentSiteCandidate {
			return fmt.Errorf("codegen: call site %d <%s> has non-candidate disposition %d before finalization", record.id, record.element.Tag, record.disposition)
		}
		fact, ok := facts[record.id]
		if !ok {
			return fmt.Errorf("codegen: candidate call site %d <%s> has no semantic target fact", record.id, record.element.Tag)
		}

		if record.candidate == componentCandidateLowercasePackage && fact.rejection == componentTargetDefinitiveNonCallablePackageObject {
			record.disposition = componentSiteLeaf
			if record.element.TypeArgs != "" {
				reportLeafTypeArgs(bag, record.element)
			}
			continue
		}
		if fact.rejection != componentTargetAccepted || len(fact.targetDiags) != 0 || fact.raw == nil || fact.provenance == 0 {
			record.disposition = componentSiteRejected
			if len(fact.targetDiags) == 0 {
				bag.Add(componentTargetDiagnostic(record.element, fset, "invalid-component-target", "component target could not be resolved to an allowed callable"))
			} else {
				for _, diagnostic := range fact.targetDiags {
					bag.Add(diagnostic)
				}
			}
			continue
		}

		signature := fact.effectiveSignature()
		_, resultErr := componentResultType(signature, runtime)
		if resultErr != nil {
			if record.candidate == componentCandidateLowercasePackage && !incompleteComponentResult(signature) {
				record.disposition = componentSiteLeaf
				if record.element.TypeArgs != "" {
					reportLeafTypeArgs(bag, record.element)
				}
				continue
			}
			record.disposition = componentSiteRejected
			bag.Add(positionalSignatureDiagnostic(record.element, fset, resultErr))
			continue
		}
		record.element.IsComponent = true
		record.disposition = componentSitePlanned
	}
	r.finalized = true
	return nil
}

type componentTargetCheckConfig struct {
	ignoreFuncBodies         bool
	disableUnusedImportCheck bool
	typeEnvironment          typeCheckEnvironment
}

func checkComponentTargetPackage(pkgPath, pkgName string, files []*goast.File, fset *token.FileSet, importer types.Importer, checkConfig componentTargetCheckConfig) (*types.Package, *types.Info, []types.Error) {
	info := &types.Info{
		Types:      make(map[goast.Expr]types.TypeAndValue),
		Defs:       make(map[*goast.Ident]types.Object),
		Uses:       make(map[*goast.Ident]types.Object),
		Instances:  make(map[*goast.Ident]types.Instance),
		Selections: make(map[*goast.SelectorExpr]*types.Selection),
		Implicits:  make(map[goast.Node]types.Object),
	}
	var typeErrs []types.Error
	config := types.Config{
		Importer:                 importer,
		IgnoreFuncBodies:         checkConfig.ignoreFuncBodies,
		DisableUnusedImportCheck: checkConfig.disableUnusedImportCheck,
		Sizes:                    checkConfig.typeEnvironment.sizes,
		GoVersion:                checkConfig.typeEnvironment.goVersion,
		Error: func(err error) {
			if typeErr, ok := err.(types.Error); ok {
				typeErrs = append(typeErrs, typeErr)
			}
		},
	}
	pkg := types.NewPackage(pkgPath, pkgName)
	checker := types.NewChecker(&config, fset, pkg, info)
	_ = checker.Files(files)
	return pkg, info, typeErrs
}

// parsedGSXPackage owns one freshly parsed package AST through its codegen
// lifecycle. Its constructor copies the file map; production supplies only
// nodes created by that parse and never shares them with another owner. The AST
// is mutable during component-call preprocessing, so the transition is
// package-wide and one-shot rather than state stored on public ast.File nodes.
type parsedGSXPackage struct {
	name    string
	files   map[string]*gsxast.File
	sources map[string][]byte

	preprocessingClaimed atomic.Bool
}

func newParsedGSXPackage(name string, files map[string]*gsxast.File) *parsedGSXPackage {
	return newParsedGSXPackageWithSources(name, files, nil)
}

func newParsedGSXPackageWithSources(name string, files map[string]*gsxast.File, sources map[string][]byte) *parsedGSXPackage {
	owned := make(map[string]*gsxast.File, len(files))
	maps.Copy(owned, files)
	ownedSources := make(map[string][]byte, len(sources))
	for path, source := range sources {
		ownedSources[path] = append([]byte(nil), source...)
	}
	return &parsedGSXPackage{name: name, files: owned, sources: ownedSources}
}

func (p *parsedGSXPackage) preprocessComponentCallSites(declNames map[string]bool, fset *token.FileSet, classifier *attrclass.Classifier, bag *diag.Bag) (callSitePreprocessResult, error) {
	if p == nil {
		return callSitePreprocessResult{}, fmt.Errorf("codegen: nil parsed GSX package")
	}
	if !p.preprocessingClaimed.CompareAndSwap(false, true) {
		return callSitePreprocessResult{}, fmt.Errorf("codegen: component-call preprocessing already claimed package %q", p.name)
	}
	return preprocessClaimedComponentCallSites(p.files, declNames, fset, classifier, bag)
}

type callSiteID uint32

const invalidCallSiteID callSiteID = 0

type callSiteDisposition uint8

const (
	componentSiteCandidate callSiteDisposition = iota
	componentSitePlanned
	componentSiteLeaf
	componentSiteRejected
	componentSitePreservedInvalidRegion
)

type callSiteRecord struct {
	id          callSiteID
	path        string
	element     *gsxast.Element
	candidate   componentCandidateKind
	disposition callSiteDisposition
}

type callSiteRegistry struct {
	byElement    map[*gsxast.Element]callSiteID
	records      []callSiteRecord
	leafTypeArgs []*gsxast.Element
	finalized    bool
}

// componentTargetQualifiers returns the syntactic selector roots used by
// component targets in one source file. This is the same exact syntactic
// contract used by unused-import analysis for ordinary Go selectors: if the
// authored target is <ui.Card>, ui is referenced even though the operand
// skeleton deliberately leaves target validation to the separate target pass.
func componentTargetQualifiers(registry *callSiteRegistry, facts map[callSiteID]componentTargetFact, path string) map[string]bool {
	qualifiers := make(map[string]bool)
	if registry == nil {
		return qualifiers
	}
	cleanPath := filepath.Clean(path)
	for _, record := range registry.records {
		if filepath.Clean(record.path) != cleanPath || record.element == nil {
			continue
		}
		switch record.disposition {
		case componentSiteCandidate:
			// Importer-free preparation has no semantic facts and therefore keeps
			// syntactic selector roots conservatively.
		case componentSitePlanned, componentSiteRejected:
			fact, ok := facts[record.id]
			if !ok || !fact.usesImportedQualifier {
				continue
			}
		default:
			continue
		}
		qualifier, _, ok := strings.Cut(record.element.Tag, ".")
		if ok && token.IsIdentifier(qualifier) {
			qualifiers[qualifier] = true
		}
		// The tag qualifier alone misses two authored uses of an import that never
		// reach the operand skeleton: a markup type-argument (<comp.Check[cons.Foo]>
		// references cons) and a component-tag attribute-value Go expression
		// (attrs={{ "@x": gsx.RawJS(…) }} references gsx). Component-tag attributes
		// are not emitted into the fmt skeleton, so skeletonUsedNames cannot see
		// them; harvest their package-qualifier roots here so unused-import removal
		// keeps them.
		harvestElementQualifierRoots(record.element, qualifiers)
	}
	return qualifiers
}

// harvestElementQualifierRoots records, in qualifiers, every package-qualifier
// root (the X in an X.Sel selector) authored in element's own type-argument list
// and attribute-value Go expressions. It is a deliberately conservative
// token-scan: any IDENT immediately followed by a PERIOD is a qualifier
// reference, hence a use of that import. Over-keeping an import is safe;
// dropping a used one is the bug this closes. It walks the element's attrs (not
// its child elements — each nested component tag is its own registry record) via
// gsxast.Inspect so every Go-expression carrier (ExprAttr, OrderedAttrsAttr
// pairs, SpreadAttr, EmbeddedAttr holes, CondAttr branches, ClassAttr parts,
// value-form control flow, pipeline stage args) is covered uniformly.
func harvestElementQualifierRoots(element *gsxast.Element, qualifiers map[string]bool) {
	if element == nil {
		return
	}
	scanQualifierRoots(element.TypeArgs, qualifiers)
	for _, attr := range element.Attrs {
		gsxast.Inspect(attr, func(n gsxast.Node) bool {
			switch n := n.(type) {
			case *gsxast.ExprAttr:
				scanQualifierRoots(n.Expr, qualifiers)
				scanStageQualifierRoots(n.Stages, qualifiers)
			case *gsxast.SpreadAttr:
				scanQualifierRoots(n.Expr, qualifiers)
				scanStageQualifierRoots(n.Stages, qualifiers)
			case *gsxast.OrderedPair:
				scanQualifierRoots(n.Value, qualifiers)
			case *gsxast.Interp:
				scanQualifierRoots(n.Expr, qualifiers)
				scanStageQualifierRoots(n.Stages, qualifiers)
			case *gsxast.EmbeddedAttr:
				scanStageQualifierRoots(n.Stages, qualifiers)
			case *gsxast.EmbeddedInterp:
				scanStageQualifierRoots(n.Stages, qualifiers)
			case *gsxast.CondAttr:
				scanQualifierRoots(n.Cond, qualifiers)
			case *gsxast.ClassPart:
				scanQualifierRoots(n.Expr, qualifiers)
				scanQualifierRoots(n.Cond, qualifiers)
				scanStageQualifierRoots(n.Stages, qualifiers)
			case *gsxast.ValueArm:
				scanQualifierRoots(n.Expr, qualifiers)
				scanStageQualifierRoots(n.Stages, qualifiers)
			case *gsxast.ValueIf:
				scanQualifierRoots(n.Cond, qualifiers)
			case *gsxast.ValueSwitch:
				scanQualifierRoots(n.Tag, qualifiers)
			case *gsxast.ValueSwitchCase:
				scanQualifierRoots(n.List, qualifiers)
			case *gsxast.Element:
				// A component tag nested inside a markup attribute value is its own
				// registry record, so its attrs are harvested there; keep only its
				// type arguments conservatively here.
				scanQualifierRoots(n.TypeArgs, qualifiers)
			}
			return true
		})
	}
}

// scanStageQualifierRoots harvests qualifier roots from the argument source of
// each pipeline stage (`|> name(args)`); the stage name is a registered filter
// identifier, never a package qualifier.
func scanStageQualifierRoots(stages []gsxast.PipeStage, qualifiers map[string]bool) {
	for _, st := range stages {
		if st.HasArgs {
			scanQualifierRoots(st.Args, qualifiers)
		}
	}
}

// scanQualifierRoots tokenizes src with go/scanner and records, in qualifiers,
// every identifier immediately followed by a `.` — the qualifier X in a selector
// X.Sel, which is exactly how an imported package name is referenced. Lexical
// errors are ignored: a partial scan can only under-report, and any qualifier it
// does find is a genuine use.
func scanQualifierRoots(src string, qualifiers map[string]bool) {
	if strings.TrimSpace(src) == "" {
		return
	}
	var s scanner.Scanner
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	s.Init(file, []byte(src), func(token.Position, string) {}, 0)
	prevIdent := ""
	for {
		_, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		if tok == token.PERIOD && prevIdent != "" {
			qualifiers[prevIdent] = true
		}
		if tok == token.IDENT {
			prevIdent = lit
		} else {
			prevIdent = ""
		}
	}
}

func (r *callSiteRegistry) hasCandidates() bool {
	if r == nil {
		return false
	}
	for _, record := range r.records {
		if record.disposition == componentSiteCandidate {
			return true
		}
	}
	return false
}

func (r *callSiteRegistry) hasCandidate(element *gsxast.Element) bool {
	if r == nil || element == nil {
		return false
	}
	id, ok := r.byElement[element]
	return ok && id != invalidCallSiteID && int(id) <= len(r.records) && r.records[id-1].disposition == componentSiteCandidate
}

type callSitePreprocessResult struct {
	registry  *callSiteRegistry
	syntaxOK  bool
	scriptsOK bool
}

func (r callSitePreprocessResult) analysisReady() bool {
	return r.syntaxOK && r.scriptsOK
}

// preprocessClaimedComponentCallSites is the mutation body behind
// parsedGSXPackage.preprocessComponentCallSites, the only package-analysis
// transition allowed to
// materialize markup embedded in Go expressions. It completes that mutation
// for every file first, validates exact GoWithElements exclusion mappings,
// resolves JavaScript context on the expanded tree, records component candidates, and
// only then allocates stable one-based call-site IDs in path and authored source
// order.
func preprocessClaimedComponentCallSites(files map[string]*gsxast.File, declNames map[string]bool, fset *token.FileSet, classifier *attrclass.Classifier, bag *diag.Bag) (callSitePreprocessResult, error) {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	seenFiles := make(map[*gsxast.File]string, len(paths))
	for _, path := range paths {
		file := files[path]
		if file == nil {
			return callSitePreprocessResult{}, fmt.Errorf("codegen: nil gsx AST for %s", path)
		}
		if prior, exists := seenFiles[file]; exists {
			return callSitePreprocessResult{}, fmt.Errorf("codegen: the same gsx AST is registered as both %s and %s", prior, path)
		}
		seenFiles[file] = path
	}

	syntaxOK := true
	for _, path := range paths {
		if !materializeEmbeddedMarkup(files[path], classifier, fset, bag) {
			syntaxOK = false
		}
	}
	if !syntaxOK {
		return callSitePreprocessResult{syntaxOK: false, scriptsOK: true}, nil
	}
	goExclusions, syntaxOK, err := packageGoWithElementsExclusions(paths, files, bag)
	if err != nil {
		return callSitePreprocessResult{}, err
	}
	if !syntaxOK {
		return callSitePreprocessResult{syntaxOK: false, scriptsOK: true}, nil
	}
	scriptsOK := true
	for _, path := range paths {
		if !jsx.ResolveScripts(files[path], bag) {
			scriptsOK = false
		}
	}
	if !scriptsOK {
		return callSitePreprocessResult{syntaxOK: true, scriptsOK: false}, nil
	}
	candidates := make(map[*gsxast.Element]componentCandidateKind)
	for _, path := range paths {
		if err := collectMaterializedComponentCandidates(files[path], declNames, goExclusions, candidates, bag); err != nil {
			return callSitePreprocessResult{}, err
		}
	}

	registry := &callSiteRegistry{byElement: make(map[*gsxast.Element]callSiteID)}
	for _, path := range paths {
		if err := registry.collectFile(path, files[path], candidates, bag); err != nil {
			return callSitePreprocessResult{}, err
		}
	}
	return callSitePreprocessResult{registry: registry, syntaxOK: syntaxOK, scriptsOK: scriptsOK}, nil
}

// packageGoWithElementsExclusions computes every top-level self-exclusion fact
// before JavaScript analysis or component stamping begins. This is a
// package-wide syntax gate: a recovered Go parser AST is never allowed to feed
// semantic analysis for only part of the package.
func packageGoWithElementsExclusions(paths []string, files map[string]*gsxast.File, bag *diag.Bag) (map[*gsxast.GoWithElements]map[int]componentExclusions, bool, error) {
	out := make(map[*gsxast.GoWithElements]map[int]componentExclusions)
	syntaxOK := true
	for _, path := range paths {
		for _, decl := range files[path].Decls {
			withElements, ok := decl.(*gsxast.GoWithElements)
			if !ok {
				continue
			}
			exclusions, err := goWithElementsExcludes(withElements)
			if err != nil {
				var sourceDiagnostic *goWithElementsDiagnostic
				if !errors.As(err, &sourceDiagnostic) {
					return nil, false, err
				}
				syntaxOK = false
				bag.Report(sourceDiagnostic.pos, sourceDiagnostic.end, diag.Error, sourceDiagnostic.code, sourceDiagnostic.source, "%s", sourceDiagnostic.message)
				continue
			}
			out[withElements] = exclusions
		}
	}
	return out, syntaxOK, nil
}

// collectMaterializedComponentCandidates walks every markup-bearing field, including
// Interp.Embedded and GoBlock.Embedded, which gsxast.Inspect deliberately does
// not traverse. All elements therefore share one classification rule whether
// they came from the original parse or expression preprocessing.
func collectMaterializedComponentCandidates(file *gsxast.File, declNames map[string]bool, goExclusions map[*gsxast.GoWithElements]map[int]componentExclusions, candidates map[*gsxast.Element]componentCandidateKind, bag *diag.Bag) error {
	var walk func([]gsxast.Markup, componentExclusions, bool)
	var walkParts func([]gsxast.GoPart, componentExclusions, bool)
	walkParts = func(parts []gsxast.GoPart, exclusions componentExclusions, reportDiagnostics bool) {
		for _, part := range parts {
			if markup, ok := part.(gsxast.Markup); ok {
				walk([]gsxast.Markup{markup}, exclusions, reportDiagnostics)
			}
		}
	}
	walk = func(nodes []gsxast.Markup, exclusions componentExclusions, reportDiagnostics bool) {
		for _, node := range nodes {
			switch node := node.(type) {
			case *gsxast.Element:
				recordComponentCandidate(candidates, node, declNames, exclusions, bag, reportDiagnostics)
				walkMarkupAttrs(node.Attrs, func(value []gsxast.Markup) { walk(value, exclusions, reportDiagnostics) })
				walk(node.Children, exclusions, reportDiagnostics)
			case *gsxast.Fragment:
				walk(node.Children, exclusions, reportDiagnostics)
			case *gsxast.Interp:
				walkParts(node.Embedded, exclusions, reportDiagnostics)
			case *gsxast.EmbeddedInterp:
				walk(node.Segments, exclusions, reportDiagnostics)
			case *gsxast.ForMarkup:
				walk(node.Body, exclusions, reportDiagnostics)
			case *gsxast.IfMarkup:
				walk(node.Then, exclusions, reportDiagnostics)
				walk(node.Else, exclusions, reportDiagnostics)
			case *gsxast.SwitchMarkup:
				for _, clause := range node.Cases {
					walk(clause.Body, exclusions, reportDiagnostics)
				}
			case *gsxast.GoBlock:
				// Direct element/fragment parts make the entire block an
				// unsupported preserve region. Still record candidate classifications
				// while leaving every semantic stamp false, but suppress secondary
				// validation diagnostics; the registry collector owns the block's one
				// rejection.
				blockDiagnostics := reportDiagnostics && node.UnsupportedMarkup == nil
				walkParts(node.Embedded, exclusions, blockDiagnostics)
			}
		}
	}

	for _, decl := range file.Decls {
		switch decl := decl.(type) {
		case *gsxast.Component:
			walk(decl.Body, oneComponentExclusion(decl.Name), true)
		case *gsxast.GoWithElements:
			excludes, ok := goExclusions[decl]
			if !ok {
				return fmt.Errorf("codegen: missing GoWithElements exclusion facts for declaration at %s", file.Package)
			}
			for i, part := range decl.Parts {
				if markup, ok := part.(gsxast.Markup); ok {
					walk([]gsxast.Markup{markup}, excludes[i], true)
				}
			}
		}
	}
	return nil
}

func (r *callSiteRegistry) add(path string, element *gsxast.Element, candidate componentCandidateKind, disposition callSiteDisposition) error {
	if prior, exists := r.byElement[element]; exists {
		return fmt.Errorf("codegen: element <%s> in %s was visited twice while assigning call-site IDs (first ID %d)", element.Tag, path, prior)
	}
	id := callSiteID(len(r.records) + 1)
	if id == invalidCallSiteID {
		return fmt.Errorf("codegen: call-site ID overflow")
	}
	r.byElement[element] = id
	r.records = append(r.records, callSiteRecord{id: id, path: path, element: element, candidate: candidate, disposition: disposition})
	return nil
}

func (r *callSiteRegistry) collectFile(path string, file *gsxast.File, candidates map[*gsxast.Element]componentCandidateKind, bag *diag.Bag) error {
	var walk func([]gsxast.Markup) error
	var walkParts func([]gsxast.GoPart) error
	walkParts = func(parts []gsxast.GoPart) error {
		for _, part := range parts {
			if markup, ok := part.(gsxast.Markup); ok {
				if err := walk([]gsxast.Markup{markup}); err != nil {
					return err
				}
			}
		}
		return nil
	}
	walk = func(nodes []gsxast.Markup) error {
		for _, node := range nodes {
			switch node := node.(type) {
			case *gsxast.Element:
				if candidate := candidates[node]; candidate != componentCandidateNone {
					if err := r.add(path, node, candidate, componentSiteCandidate); err != nil {
						return err
					}
				} else if node.TypeArgs != "" {
					r.leafTypeArgs = append(r.leafTypeArgs, node)
				}
				var attrErr error
				walkMarkupAttrs(node.Attrs, func(value []gsxast.Markup) {
					if attrErr == nil {
						attrErr = walk(value)
					}
				})
				if attrErr != nil {
					return attrErr
				}
				if err := walk(node.Children); err != nil {
					return err
				}
			case *gsxast.Fragment:
				if err := walk(node.Children); err != nil {
					return err
				}
			case *gsxast.Interp:
				if err := walkParts(node.Embedded); err != nil {
					return err
				}
			case *gsxast.EmbeddedInterp:
				if err := walk(node.Segments); err != nil {
					return err
				}
			case *gsxast.ForMarkup:
				if err := walk(node.Body); err != nil {
					return err
				}
			case *gsxast.IfMarkup:
				if err := walk(node.Then); err != nil {
					return err
				}
				if err := walk(node.Else); err != nil {
					return err
				}
			case *gsxast.SwitchMarkup:
				for _, clause := range node.Cases {
					if err := walk(clause.Body); err != nil {
						return err
					}
				}
			case *gsxast.GoBlock:
				first := node.UnsupportedMarkup
				if first != nil {
					bag.Errorf(first.Pos(), first.End(), "unsupported-node", "element literals inside {{ }} blocks are not supported yet")
					for _, part := range node.Embedded {
						if element, ok := part.(*gsxast.Element); ok {
							if err := r.add(path, element, componentCandidateNone, componentSitePreservedInvalidRegion); err != nil {
								return err
							}
						}
					}
					continue
				}
				if err := walkParts(node.Embedded); err != nil {
					return err
				}
			}
		}
		return nil
	}

	for _, decl := range file.Decls {
		switch decl := decl.(type) {
		case *gsxast.Component:
			if err := walk(decl.Body); err != nil {
				return err
			}
		case *gsxast.GoWithElements:
			if err := walkParts(decl.Parts); err != nil {
				return err
			}
		}
	}
	return nil
}
