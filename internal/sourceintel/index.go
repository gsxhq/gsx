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

type Index struct {
	occurrences  map[string][]Occurrence
	definitions  map[types.Object]Span
	declarations map[string][]Declaration
	sources      map[string]SourceVersion
}

func BuildIndex(info *types.Info, files []MappedFile) *Index {
	index := &Index{
		occurrences:  make(map[string][]Occurrence),
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
	for path := range index.occurrences {
		occurrences := index.occurrences[path]
		sort.SliceStable(occurrences, func(a, b int) bool {
			left, right := occurrences[a], occurrences[b]
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
		})
		index.occurrences[path] = occurrences
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
		i.occurrences[span.Path] = append(i.occurrences[span.Path], Occurrence{
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
	i.occurrences[span.Path] = append(i.occurrences[span.Path], Occurrence{
		Span:   span,
		Kind:   kind,
		Object: object,
	})
	if kind == IdentifierDefinition {
		i.definitions[Origin(object)] = span
	}
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
	occurrences := i.occurrences[path]
	end := sort.Search(len(occurrences), func(index int) bool {
		return occurrences[index].Span.Start > offset
	})
	var best Occurrence
	found := false
	for index := end - 1; index >= 0; index-- {
		candidate := occurrences[index]
		if offset < candidate.Span.Start || offset >= candidate.Span.End {
			continue
		}
		if !found || occurrencePreferred(candidate, best) {
			best = candidate
			found = true
		}
	}
	return best, found
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
