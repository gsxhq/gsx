package sourceintel

import (
	"crypto/sha256"
	"go/ast"
	"go/token"
	"go/types"
	"sort"
)

type OccurrenceKind uint8

const (
	IdentifierDefinition OccurrenceKind = iota
	IdentifierUse
	Expression
)

type Occurrence struct {
	Span         Span
	Kind         OccurrenceKind
	Object       types.Object
	TypeAndValue types.TypeAndValue
	HasTypeValue bool
}

type DeclarationKind uint8

const (
	DeclarationFunction DeclarationKind = iota
	DeclarationMethod
	DeclarationType
	DeclarationStruct
	DeclarationInterface
	DeclarationConstant
	DeclarationVariable
)

type Declaration struct {
	Name      string
	Kind      DeclarationKind
	Container string
	NameSpan  Span
	DeclSpan  Span
	Object    types.Object
}

type SourceVersion struct {
	Size   int
	SHA256 [sha256.Size]byte
}

type MappedFile struct {
	AST           *ast.File
	TokenFile     *token.File
	SourceMap     *SourceMap
	SourceVersion SourceVersion
}

type occurrenceIndex struct {
	items []Occurrence
	nodes []occurrenceNode
	root  int
}

// occurrenceNode is a centered interval-tree node. The two orders let a point
// query stop immediately after the center-crossing intervals that contain it.
type occurrenceNode struct {
	center  int
	left    int
	right   int
	byStart []int
	byEnd   []int
}

type Index struct {
	occurrences  map[string]occurrenceIndex
	definitions  map[types.Object]Span
	declarations map[string][]Declaration
	sources      map[string]SourceVersion
}

func BuildIndex(info *types.Info, files []MappedFile) *Index {
	index := &Index{
		occurrences:  make(map[string]occurrenceIndex),
		definitions:  make(map[types.Object]Span),
		declarations: make(map[string][]Declaration),
		sources:      make(map[string]SourceVersion),
	}
	for _, file := range files {
		if file.SourceMap == nil {
			continue
		}
		index.sources[file.SourceMap.sourcePath] = file.SourceVersion
		if info == nil || file.AST == nil || file.TokenFile == nil {
			continue
		}
		index.harvestOccurrences(info, file)
		index.harvestDeclarations(info, file)
	}
	for path, occurrences := range index.occurrences {
		index.occurrences[path] = newOccurrenceIndex(occurrences.items)
	}
	for path := range index.declarations {
		declarations := index.declarations[path]
		sort.SliceStable(declarations, func(a, b int) bool {
			left, right := declarations[a], declarations[b]
			if left.DeclSpan.Start != right.DeclSpan.Start {
				return left.DeclSpan.Start < right.DeclSpan.Start
			}
			if left.NameSpan.Start != right.NameSpan.Start {
				return left.NameSpan.Start < right.NameSpan.Start
			}
			if left.Kind != right.Kind {
				return left.Kind < right.Kind
			}
			return left.Name < right.Name
		})
		index.declarations[path] = declarations
	}
	return index
}

func (i *Index) harvestOccurrences(info *types.Info, file MappedFile) {
	ast.Inspect(file.AST, func(node ast.Node) bool {
		if node == nil {
			return true
		}
		if ident, ok := node.(*ast.Ident); ok {
			if object := info.Defs[ident]; object != nil {
				i.addIdentifier(file, ident, IdentifierDefinition, object)
			}
			if object := info.Uses[ident]; object != nil {
				i.addIdentifier(file, ident, IdentifierUse, object)
			}
			return true
		}
		expression, ok := node.(ast.Expr)
		if !ok {
			return true
		}
		typeAndValue, ok := info.Types[expression]
		if !ok {
			return true
		}
		span, ok := mappedNodeSpan(file, expression, Hover)
		if !ok {
			return true
		}
		i.addOccurrence(Occurrence{
			Span:         span,
			Kind:         Expression,
			TypeAndValue: typeAndValue,
			HasTypeValue: true,
		})
		return true
	})
}

func (i *Index) addIdentifier(file MappedFile, ident *ast.Ident, kind OccurrenceKind, object types.Object) {
	span, ok := mappedNodeSpan(file, ident, Definition|Hover)
	if !ok {
		return
	}
	i.addOccurrence(Occurrence{
		Span:   span,
		Kind:   kind,
		Object: object,
	})
	if kind == IdentifierDefinition {
		i.definitions[Origin(object)] = span
	}
}

func (i *Index) addOccurrence(occurrence Occurrence) {
	index := i.occurrences[occurrence.Span.Path]
	index.items = append(index.items, occurrence)
	i.occurrences[occurrence.Span.Path] = index
}

func newOccurrenceIndex(occurrences []Occurrence) occurrenceIndex {
	index := occurrenceIndex{root: -1}
	for _, occurrence := range occurrences {
		if occurrence.Span.End > occurrence.Span.Start {
			index.items = append(index.items, occurrence)
		}
	}
	sort.SliceStable(index.items, func(left, right int) bool {
		return occurrenceLess(index.items[left], index.items[right])
	})
	indices := make([]int, len(index.items))
	for item := range index.items {
		indices[item] = item
	}
	index.root = index.build(indices)
	return index
}

func occurrenceLess(left, right Occurrence) bool {
	if left.Span.Start != right.Span.Start {
		return left.Span.Start < right.Span.Start
	}
	leftIdentifier := left.Kind != Expression
	rightIdentifier := right.Kind != Expression
	if leftIdentifier != rightIdentifier {
		return leftIdentifier
	}
	leftLength := left.Span.End - left.Span.Start
	rightLength := right.Span.End - right.Span.Start
	if leftLength != rightLength {
		return leftLength < rightLength
	}
	return left.Kind < right.Kind
}

func (i *occurrenceIndex) build(indices []int) int {
	if len(indices) == 0 {
		return -1
	}
	center := i.items[indices[len(indices)/2]].Span.Start
	left := make([]int, 0, len(indices)/2)
	right := make([]int, 0, len(indices)/2)
	overlaps := make([]int, 0, len(indices))
	for _, item := range indices {
		span := i.items[item].Span
		switch {
		case span.End <= center:
			left = append(left, item)
		case span.Start > center:
			right = append(right, item)
		default:
			overlaps = append(overlaps, item)
		}
	}
	nodeIndex := len(i.nodes)
	i.nodes = append(i.nodes, occurrenceNode{
		center:  center,
		left:    -1,
		right:   -1,
		byStart: overlaps,
		byEnd:   append([]int(nil), overlaps...),
	})
	sort.SliceStable(i.nodes[nodeIndex].byEnd, func(left, right int) bool {
		leftOccurrence := i.items[i.nodes[nodeIndex].byEnd[left]]
		rightOccurrence := i.items[i.nodes[nodeIndex].byEnd[right]]
		if leftOccurrence.Span.End != rightOccurrence.Span.End {
			return leftOccurrence.Span.End > rightOccurrence.Span.End
		}
		return occurrenceLess(leftOccurrence, rightOccurrence)
	})
	i.nodes[nodeIndex].left = i.build(left)
	i.nodes[nodeIndex].right = i.build(right)
	return nodeIndex
}

func (i *Index) harvestDeclarations(info *types.Info, file MappedFile) {
	for _, declaration := range file.AST.Decls {
		declarationSpan, ok := mappedDeclarationSpan(file, declaration)
		if !ok {
			continue
		}
		switch declaration := declaration.(type) {
		case *ast.FuncDecl:
			i.addFunctionDeclaration(info, file, declaration, declarationSpan)
		case *ast.GenDecl:
			i.addGeneralDeclaration(info, file, declaration, declarationSpan)
		}
	}
}

func (i *Index) addFunctionDeclaration(info *types.Info, file MappedFile, declaration *ast.FuncDecl, declarationSpan Span) {
	object := info.Defs[declaration.Name]
	if object == nil {
		return
	}
	nameSpan, ok := mappedNodeSpan(file, declaration.Name, Symbol)
	if !ok {
		return
	}
	kind := DeclarationFunction
	container := ""
	if declaration.Recv != nil {
		kind = DeclarationMethod
		container = receiverContainer(object)
	}
	i.declarations[nameSpan.Path] = append(i.declarations[nameSpan.Path], Declaration{
		Name:      declaration.Name.Name,
		Kind:      kind,
		Container: container,
		NameSpan:  nameSpan,
		DeclSpan:  declarationSpan,
		Object:    object,
	})
}

func (i *Index) addGeneralDeclaration(info *types.Info, file MappedFile, declaration *ast.GenDecl, declarationSpan Span) {
	for _, specification := range declaration.Specs {
		switch specification := specification.(type) {
		case *ast.TypeSpec:
			kind := DeclarationType
			if !specification.Assign.IsValid() {
				switch specification.Type.(type) {
				case *ast.StructType:
					kind = DeclarationStruct
				case *ast.InterfaceType:
					kind = DeclarationInterface
				}
			}
			i.addNamedDeclaration(info, file, specification.Name, kind, declarationSpan)
		case *ast.ValueSpec:
			var kind DeclarationKind
			switch declaration.Tok {
			case token.CONST:
				kind = DeclarationConstant
			case token.VAR:
				kind = DeclarationVariable
			default:
				continue
			}
			for _, name := range specification.Names {
				i.addNamedDeclaration(info, file, name, kind, declarationSpan)
			}
		}
	}
}

func (i *Index) addNamedDeclaration(info *types.Info, file MappedFile, name *ast.Ident, kind DeclarationKind, declarationSpan Span) {
	object := info.Defs[name]
	if object == nil {
		return
	}
	nameSpan, ok := mappedNodeSpan(file, name, Symbol)
	if !ok {
		return
	}
	i.declarations[nameSpan.Path] = append(i.declarations[nameSpan.Path], Declaration{
		Name:     name.Name,
		Kind:     kind,
		NameSpan: nameSpan,
		DeclSpan: declarationSpan,
		Object:   object,
	})
}

func mappedDeclarationSpan(file MappedFile, declaration ast.Decl) (Span, bool) {
	generatedStart := file.TokenFile.Offset(declaration.Pos())
	generatedEnd := file.TokenFile.Offset(declaration.End())
	if span, ok := file.SourceMap.SourceSpan(generatedStart, generatedEnd, Symbol); ok {
		return span, true
	}
	return file.SourceMap.DeclarationSpan(generatedStart, generatedEnd)
}

func receiverContainer(object types.Object) string {
	function, ok := object.(*types.Func)
	if !ok {
		return ""
	}
	signature, ok := function.Type().(*types.Signature)
	if !ok || signature.Recv() == nil {
		return ""
	}
	receiverType := signature.Recv().Type()
	if pointer, ok := receiverType.(*types.Pointer); ok {
		receiverType = pointer.Elem()
	}
	named, ok := receiverType.(*types.Named)
	if !ok {
		return ""
	}
	return named.Obj().Name()
}

func mappedNodeSpan(file MappedFile, node ast.Node, capability Capability) (Span, bool) {
	if node.Pos() < file.AST.Pos() || node.End() > file.AST.End() {
		return Span{}, false
	}
	return file.SourceMap.SourceSpan(file.TokenFile.Offset(node.Pos()), file.TokenFile.Offset(node.End()), capability)
}

func Origin(object types.Object) types.Object {
	switch object := object.(type) {
	case *types.Func:
		return object.Origin()
	case *types.Var:
		return object.Origin()
	default:
		return object
	}
}

func (i *Index) At(path string, offset int) (Occurrence, bool) {
	occurrence, ok, _ := i.at(path, offset)
	return occurrence, ok
}

func (i *Index) at(path string, offset int) (Occurrence, bool, int) {
	index, ok := i.occurrences[path]
	if !ok {
		return Occurrence{}, false, 0
	}
	return index.at(offset)
}

func (i occurrenceIndex) at(offset int) (Occurrence, bool, int) {
	var best Occurrence
	found := false
	visits := 0
	for nodeIndex := i.root; nodeIndex >= 0; {
		node := i.nodes[nodeIndex]
		switch {
		case offset < node.center:
			for _, item := range node.byStart {
				visits++
				candidate := i.items[item]
				if candidate.Span.Start > offset {
					break
				}
				if !found || occurrencePreferred(candidate, best) {
					best = candidate
					found = true
				}
			}
			nodeIndex = node.left
		case offset > node.center:
			for _, item := range node.byEnd {
				visits++
				candidate := i.items[item]
				if candidate.Span.End <= offset {
					break
				}
				if !found || occurrencePreferred(candidate, best) {
					best = candidate
					found = true
				}
			}
			nodeIndex = node.right
		default:
			for _, item := range node.byStart {
				visits++
				candidate := i.items[item]
				if !found || occurrencePreferred(candidate, best) {
					best = candidate
					found = true
				}
			}
			nodeIndex = -1
		}
	}
	return best, found, visits
}

func occurrencePreferred(candidate, current Occurrence) bool {
	candidateIdentifier := candidate.Kind != Expression
	currentIdentifier := current.Kind != Expression
	if candidateIdentifier != currentIdentifier {
		return candidateIdentifier
	}
	candidateLength := candidate.Span.End - candidate.Span.Start
	currentLength := current.Span.End - current.Span.Start
	if candidateLength != currentLength {
		return candidateLength < currentLength
	}
	if candidate.Span.Start != current.Span.Start {
		return candidate.Span.Start > current.Span.Start
	}
	return candidate.Kind < current.Kind
}

func (i *Index) Definition(object types.Object) (Span, bool) {
	span, ok := i.definitions[Origin(object)]
	return span, ok
}

func (i *Index) Declarations(path string) []Declaration {
	return append([]Declaration(nil), i.declarations[path]...)
}

func (i *Index) MatchesSource(path string, source []byte) bool {
	version, ok := i.sources[path]
	return ok && version.Size == len(source) && version.SHA256 == sha256.Sum256(source)
}
