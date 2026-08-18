package sourceintel

import (
	"crypto/sha256"
	"go/ast"
	"go/token"
	"go/types"
	"sort"
	"unsafe"
)

type OccurrenceKind uint8

const (
	IdentifierDefinition OccurrenceKind = iota
	IdentifierUse
	Expression
)

type Occurrence struct {
	Span          Span
	Kind          OccurrenceKind
	Object        types.Object
	TypeAndValue  types.TypeAndValue
	HasTypeValue  bool
	subtreeMaxEnd int // maximum Span.End in this implicit tree node's subtree
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

type VersionedSpan struct {
	Span          Span
	SourceVersion SourceVersion
}

func (v SourceVersion) Matches(source []byte) bool {
	return v.Size == len(source) && v.SHA256 == sha256.Sum256(source)
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
	canonical    func(types.Object) types.Object // nil = identity
}

// BuildOptions extends BuildIndex with facts the type checker cannot see.
type BuildOptions struct {
	// Extra occurrences (component tag sites, attribute→parameter bindings,
	// pipe stage names, build-tag variant declarations). An occurrence whose
	// path is not one of files' source paths is dropped: version gating
	// (MatchesSource) must hold for every indexed path.
	Extra []Occurrence
	// Canonical maps analysis-only helper objects (a split component's body
	// func and its parameters) onto the authored object they stand for. It is
	// applied to every harvested and extra object and inside Definition, so
	// callers may pass either object.
	Canonical func(types.Object) types.Object
}

// IndexStats describes the logical entries retained directly by an Index.
// ShallowBytesLowerBound counts the Index value, logical map key/value storage,
// and slice backing arrays. It deliberately excludes map bucket/control
// overhead, string backing storage, allocator overhead, and the go/types object
// graphs referenced by occurrences and declarations. It is therefore a stable
// shallow lower bound, not a retained-heap measurement.
type IndexStats struct {
	Files                  int
	Occurrences            int
	Definitions            int
	Declarations           int
	ShallowBytesLowerBound int64
}

func (i *Index) Stats() IndexStats {
	if i == nil {
		return IndexStats{}
	}
	stats := IndexStats{
		Files:                  len(i.sources),
		Definitions:            len(i.definitions),
		ShallowBytesLowerBound: int64(unsafe.Sizeof(*i)),
	}
	for path, occurrences := range i.occurrences {
		stats.Occurrences += len(occurrences)
		stats.ShallowBytesLowerBound += int64(unsafe.Sizeof(path))
		stats.ShallowBytesLowerBound += int64(unsafe.Sizeof(occurrences))
		stats.ShallowBytesLowerBound += int64(len(occurrences)) * int64(unsafe.Sizeof(Occurrence{}))
	}
	var object types.Object
	stats.ShallowBytesLowerBound += int64(len(i.definitions)) * (int64(unsafe.Sizeof(object)) + int64(unsafe.Sizeof(Span{})))
	for path, declarations := range i.declarations {
		stats.Declarations += len(declarations)
		stats.ShallowBytesLowerBound += int64(unsafe.Sizeof(path))
		stats.ShallowBytesLowerBound += int64(unsafe.Sizeof(declarations))
		stats.ShallowBytesLowerBound += int64(len(declarations)) * int64(unsafe.Sizeof(Declaration{}))
	}
	for path := range i.sources {
		stats.ShallowBytesLowerBound += int64(unsafe.Sizeof(path)) + int64(unsafe.Sizeof(SourceVersion{}))
	}
	return stats
}

func BuildIndex(info *types.Info, files []MappedFile) *Index {
	return BuildIndexWith(info, files, BuildOptions{})
}

func BuildIndexWith(info *types.Info, files []MappedFile, opts BuildOptions) *Index {
	index := &Index{
		occurrences:  make(map[string][]Occurrence),
		definitions:  make(map[types.Object]Span),
		declarations: make(map[string][]Declaration),
		sources:      make(map[string]SourceVersion),
		canonical:    opts.Canonical,
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
	for _, o := range opts.Extra {
		if o.Object == nil || o.Kind == Expression {
			continue
		}
		if _, ok := index.sources[o.Span.Path]; !ok {
			continue
		}
		o.Object = index.canon(o.Object)
		index.addOccurrence(o)
		if o.Kind == IdentifierDefinition {
			if _, exists := index.definitions[Origin(o.Object)]; !exists {
				index.definitions[Origin(o.Object)] = o.Span
			}
		}
	}
	for path, occurrences := range index.occurrences {
		index.occurrences[path] = indexOccurrences(occurrences)
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
	object = i.canon(object)
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
	i.occurrences[occurrence.Span.Path] = append(i.occurrences[occurrence.Span.Path], occurrence)
}

// canon applies the Index's Canonical mapping (nil = identity).
func (i *Index) canon(object types.Object) types.Object {
	if i.canonical == nil || object == nil {
		return object
	}
	return i.canonical(object)
}

// forEachOccurrence visits every indexed occurrence (identifier and expression),
// per path in map order. Used by SymbolGraph.
func (i *Index) forEachOccurrence(fn func(path string, o Occurrence)) {
	for path, occurrences := range i.occurrences {
		for _, o := range occurrences {
			fn(path, o)
		}
	}
}

func indexOccurrences(occurrences []Occurrence) []Occurrence {
	indexed := append([]Occurrence(nil), occurrences...)
	for occurrence := range indexed {
		indexed[occurrence].subtreeMaxEnd = 0
	}
	sort.SliceStable(indexed, func(left, right int) bool {
		return occurrenceLess(indexed[left], indexed[right])
	})
	augmentOccurrenceEnds(indexed, 0, len(indexed))
	return indexed
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

func augmentOccurrenceEnds(occurrences []Occurrence, lo, hi int) int {
	if lo >= hi {
		return -1
	}
	mid := lo + (hi-lo)/2
	maxEnd := occurrences[mid].Span.End
	if leftMaxEnd := augmentOccurrenceEnds(occurrences, lo, mid); leftMaxEnd > maxEnd {
		maxEnd = leftMaxEnd
	}
	if rightMaxEnd := augmentOccurrenceEnds(occurrences, mid+1, hi); rightMaxEnd > maxEnd {
		maxEnd = rightMaxEnd
	}
	occurrences[mid].subtreeMaxEnd = maxEnd
	return maxEnd
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
	object = i.canon(object)
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
	object = i.canon(object)
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
	occurrences, ok := i.occurrences[path]
	if !ok {
		return Occurrence{}, false, 0
	}
	lookup := occurrenceLookup{offset: offset}
	lookupOccurrenceTree(occurrences, 0, len(occurrences), &lookup)
	lookup.best.subtreeMaxEnd = 0
	return lookup.best, lookup.found, lookup.visits
}

type occurrenceLookup struct {
	offset int
	best   Occurrence
	found  bool
	visits int
}

func lookupOccurrenceTree(occurrences []Occurrence, lo, hi int, lookup *occurrenceLookup) {
	if lo >= hi {
		return
	}
	mid := lo + (hi-lo)/2
	candidate := occurrences[mid]
	lookup.visits++
	if candidate.subtreeMaxEnd <= lookup.offset {
		return
	}

	lookupOccurrenceTree(occurrences, lo, mid, lookup)
	if candidate.Span.Start > lookup.offset {
		return
	}
	if lookup.offset < candidate.Span.End && (!lookup.found || occurrencePreferred(candidate, lookup.best)) {
		lookup.best = candidate
		lookup.found = true
	}
	lookupOccurrenceTree(occurrences, mid+1, hi, lookup)
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
	span, ok := i.definitions[Origin(i.canon(object))]
	return span, ok
}

func (i *Index) Declarations(path string) []Declaration {
	return append([]Declaration(nil), i.declarations[path]...)
}

func (i *Index) MatchesSource(path string, source []byte) bool {
	version, ok := i.sources[path]
	return ok && version.Matches(source)
}
