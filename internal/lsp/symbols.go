package lsp

import (
	goast "go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/sourceintel"
	gsxparser "github.com/gsxhq/gsx/parser"
)

// LSP SymbolKind numeric constants (subset gsx emits).
const (
	symKindClass     = 5
	symKindMethod    = 6
	symKindInterface = 11
	symKindFunction  = 12
	symKindVariable  = 13
	symKindConstant  = 14
	symKindStruct    = 23
)

// Symbol is one navigable declaration in a .gsx file: a component or a
// top-level authored Go declaration. Positions are resolved against the file's
// FileSet (byte columns, 1-based line/column).
type Symbol struct {
	Name      string
	Kind      int            // LSP SymbolKind
	Container string         // package name, or receiver type name for methods
	NamePos   token.Position // start of the name (selectionRange / workspace Location)
	DeclStart token.Position // start of the whole declaration (documentSymbol range)
	DeclEnd   token.Position // end of the whole declaration
}

// FileSymbols extracts the symbols declared in one parsed .gsx file. Components
// are owned by the GSX AST. Top-level Go declarations come from the retained
// semantic index when it describes these exact source bytes; otherwise a
// focused partial-parser fallback recovers declarations from current authored
// Go regions.
func FileSymbols(path string, source []byte, file *gsxast.File, fset *token.FileSet, index *sourceintel.Index) []Symbol {
	semantic := file != nil && fset != nil && index != nil && index.MatchesSource(path, source)
	if !semantic {
		fset = token.NewFileSet()
		file, _ = gsxparser.ParseFileWithClassifier(fset, path, source, 0, nil)
		if file == nil {
			return nil
		}
	}
	var out []Symbol
	for _, d := range file.Decls {
		switch decl := d.(type) {
		case *gsxast.Component:
			out = append(out, componentSymbol(file, fset, decl))
		}
	}
	if semantic {
		for _, declaration := range index.Declarations(path) {
			if symbol, ok := semanticDeclarationSymbol(file, fset, declaration); ok {
				out = append(out, symbol)
			}
		}
	} else {
		for _, d := range file.Decls {
			switch declaration := d.(type) {
			case *gsxast.GoChunk:
				out = append(out, partialGoChunkSymbols(file, fset, source, declaration)...)
			case *gsxast.GoWithElements:
				out = append(out, partialGoWithElementsSymbols(file, fset, source, declaration)...)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		left, right := out[i], out[j]
		if left.DeclStart.Offset != right.DeclStart.Offset {
			return left.DeclStart.Offset < right.DeclStart.Offset
		}
		leftLength := left.DeclEnd.Offset - left.DeclStart.Offset
		rightLength := right.DeclEnd.Offset - right.DeclStart.Offset
		if leftLength != rightLength {
			return leftLength < rightLength
		}
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		return left.Name < right.Name
	})
	return out
}

func semanticDeclarationSymbol(file *gsxast.File, fset *token.FileSet, declaration sourceintel.Declaration) (Symbol, bool) {
	kind, ok := semanticSymbolKind(declaration.Kind)
	if !ok || declaration.NameSpan.Path != declaration.DeclSpan.Path {
		return Symbol{}, false
	}
	namePos, ok := sourceOffsetPosition(file, fset, declaration.NameSpan.Start)
	if !ok {
		return Symbol{}, false
	}
	declStart, ok := sourceOffsetPosition(file, fset, declaration.DeclSpan.Start)
	if !ok {
		return Symbol{}, false
	}
	declEnd, ok := sourceOffsetPosition(file, fset, declaration.DeclSpan.End)
	if !ok {
		return Symbol{}, false
	}
	container := declaration.Container
	if container == "" {
		container = file.Package
	}
	return Symbol{
		Name:      declaration.Name,
		Kind:      kind,
		Container: container,
		NamePos:   namePos,
		DeclStart: declStart,
		DeclEnd:   declEnd,
	}, true
}

func semanticSymbolKind(kind sourceintel.DeclarationKind) (int, bool) {
	switch kind {
	case sourceintel.DeclarationFunction:
		return symKindFunction, true
	case sourceintel.DeclarationMethod:
		return symKindMethod, true
	case sourceintel.DeclarationType:
		return symKindClass, true
	case sourceintel.DeclarationStruct:
		return symKindStruct, true
	case sourceintel.DeclarationInterface:
		return symKindInterface, true
	case sourceintel.DeclarationConstant:
		return symKindConstant, true
	case sourceintel.DeclarationVariable:
		return symKindVariable, true
	default:
		return 0, false
	}
}

func sourceOffsetPosition(file *gsxast.File, fset *token.FileSet, offset int) (token.Position, bool) {
	if file == nil || fset == nil {
		return token.Position{}, false
	}
	tokenFile := fset.File(file.Pos())
	if tokenFile == nil || offset < 0 || offset > tokenFile.Size() {
		return token.Position{}, false
	}
	return fset.Position(tokenFile.Pos(offset)), true
}

func componentSymbol(file *gsxast.File, fset *token.FileSet, c *gsxast.Component) Symbol {
	kind := symKindFunction
	container := file.Package
	if c.Recv != "" {
		kind = symKindMethod
		container = receiverTypeName(c.Recv)
	}
	return Symbol{
		Name:      c.Name,
		Kind:      kind,
		Container: container,
		NamePos:   fset.Position(c.NamePos),
		DeclStart: fset.Position(c.Pos()),
		DeclEnd:   fset.Position(c.End()),
	}
}

// partialGoWrapPrefix makes one reconstructed authored region a parseable file.
// It has no source mapping and is removed before translating parser offsets.
const partialGoWrapPrefix = "package p\n"

type partialGoSegment struct {
	parsedStart int
	parsedEnd   int
	sourceStart int
	sourceEnd   int
	linear      bool
}

type partialGoSource struct {
	text     strings.Builder
	segments []partialGoSegment
}

func (s *partialGoSource) appendAuthored(text string, sourceStart, sourceEnd int) bool {
	if sourceEnd-sourceStart != len(text) || !s.canAppend(sourceStart) {
		return false
	}
	parsedStart := s.text.Len()
	s.text.WriteString(text)
	s.segments = append(s.segments, partialGoSegment{
		parsedStart: parsedStart,
		parsedEnd:   s.text.Len(),
		sourceStart: sourceStart,
		sourceEnd:   sourceEnd,
		linear:      true,
	})
	return true
}

func (s *partialGoSource) appendExpressionPlaceholder(source []byte, sourceStart, sourceEnd int) bool {
	if sourceStart < 0 || sourceEnd-sourceStart < 2 || sourceEnd > len(source) || !s.canAppend(sourceStart) {
		return false
	}
	placeholder := make([]byte, sourceEnd-sourceStart)
	for i := range placeholder {
		placeholder[i] = ' '
	}
	placeholder[0] = '`'
	placeholder[len(placeholder)-1] = '`'
	for i, b := range source[sourceStart:sourceEnd] {
		// Preserve physical line breaks at their authored byte offsets, but leave
		// CR as a same-width space. Go removes CR from raw literal token values;
		// retaining it could make ast.BasicLit.End shorter than the physical
		// placeholder and destroy the exact endpoint proof below.
		if b == '\n' {
			if i == 0 || i == len(placeholder)-1 {
				return false
			}
			placeholder[i] = b
		}
	}
	parsedStart := s.text.Len()
	s.text.Write(placeholder)
	s.segments = append(s.segments, partialGoSegment{
		parsedStart: parsedStart,
		parsedEnd:   s.text.Len(),
		sourceStart: sourceStart,
		sourceEnd:   sourceEnd,
	})
	return true
}

func (s *partialGoSource) canAppend(sourceStart int) bool {
	if len(s.segments) == 0 {
		return true
	}
	last := s.segments[len(s.segments)-1]
	return last.parsedEnd == s.text.Len() && last.sourceEnd == sourceStart
}

func (s *partialGoSource) sourceOffset(parsedOffset int) (int, bool) {
	for _, segment := range s.segments {
		if parsedOffset < segment.parsedStart {
			return 0, false
		}
		if parsedOffset > segment.parsedEnd {
			continue
		}
		if segment.linear {
			return segment.sourceStart + parsedOffset - segment.parsedStart, true
		}
		switch parsedOffset {
		case segment.parsedStart:
			return segment.sourceStart, true
		case segment.parsedEnd:
			return segment.sourceEnd, true
		default:
			return 0, false
		}
	}
	return 0, false
}

func (s *partialGoSource) linearSourceSpan(parsedStart, parsedEnd int) (int, int, bool) {
	if parsedEnd <= parsedStart {
		return 0, 0, false
	}
	for _, segment := range s.segments {
		if parsedStart < segment.parsedStart {
			return 0, 0, false
		}
		if parsedStart > segment.parsedEnd || parsedEnd > segment.parsedEnd {
			continue
		}
		if !segment.linear {
			return 0, 0, false
		}
		sourceStart := segment.sourceStart + parsedStart - segment.parsedStart
		sourceEnd := segment.sourceStart + parsedEnd - segment.parsedStart
		return sourceStart, sourceEnd, true
	}
	return 0, 0, false
}

// partialGoChunkSymbols parses a GoChunk's verbatim source in recovery mode and
// emits only declarations represented by the returned partial Go AST.
func partialGoChunkSymbols(file *gsxast.File, fset *token.FileSet, source []byte, chunk *gsxast.GoChunk) []Symbol {
	tokenFile := fset.File(chunk.Pos())
	if tokenFile == nil || tokenFile.Size() != len(source) {
		return nil
	}
	start := tokenFile.Offset(chunk.Pos())
	end := tokenFile.Offset(chunk.End())
	if start < 0 || end < start || end > len(source) || chunk.Src != string(source[start:end]) {
		return nil
	}
	var reconstructed partialGoSource
	if !reconstructed.appendAuthored(chunk.Src, start, end) {
		return nil
	}
	return partialGoSymbols(file.Package, fset, tokenFile, source, reconstructed)
}

func partialGoWithElementsSymbols(file *gsxast.File, fset *token.FileSet, source []byte, declaration *gsxast.GoWithElements) []Symbol {
	tokenFile := fset.File(declaration.Pos())
	if tokenFile == nil || tokenFile.Size() != len(source) {
		return nil
	}
	declarationStart := tokenFile.Offset(declaration.Pos())
	declarationEnd := tokenFile.Offset(declaration.End())
	if declarationStart < 0 || declarationEnd < declarationStart || declarationEnd > len(source) {
		return nil
	}
	var reconstructed partialGoSource
	cursor := declarationStart
	for _, part := range declaration.Parts {
		if fset.File(part.Pos()) != tokenFile || fset.File(part.End()) != tokenFile {
			return nil
		}
		start := tokenFile.Offset(part.Pos())
		end := tokenFile.Offset(part.End())
		if start != cursor || end < start || end > declarationEnd {
			return nil
		}
		switch part := part.(type) {
		case gsxast.GoText:
			if part.Src != string(source[start:end]) || !reconstructed.appendAuthored(part.Src, start, end) {
				return nil
			}
		case *gsxast.Element, *gsxast.Fragment, *gsxast.EmbeddedInterp:
			if !reconstructed.appendExpressionPlaceholder(source, start, end) {
				return nil
			}
		default:
			return nil
		}
		cursor = end
	}
	if cursor != declarationEnd || reconstructed.text.Len() != declarationEnd-declarationStart {
		return nil
	}
	return partialGoSymbols(file.Package, fset, tokenFile, source, reconstructed)
}

func partialGoSymbols(packageName string, sourceFset *token.FileSet, sourceTokenFile *token.File, source []byte, reconstructed partialGoSource) []Symbol {
	gfset := token.NewFileSet()
	gf, _ := parser.ParseFile(gfset, "recovery.go", partialGoWrapPrefix+reconstructed.text.String(), parser.AllErrors|parser.SkipObjectResolution)
	if gf == nil {
		return nil
	}
	parsedTokenFile := gfset.File(gf.Pos())
	if parsedTokenFile == nil {
		return nil
	}

	mapPos := func(pos token.Pos) (token.Position, bool) {
		parsedOffset := parsedTokenFile.Offset(pos) - len(partialGoWrapPrefix)
		sourceOffset, ok := reconstructed.sourceOffset(parsedOffset)
		if !ok || sourceOffset < 0 || sourceOffset > sourceTokenFile.Size() {
			return token.Position{}, false
		}
		return sourceFset.Position(sourceTokenFile.Pos(sourceOffset)), true
	}
	// Parser recovery may synthesize identifiers. Prove the complete token came
	// from one verbatim authored segment before publishing it as a symbol.
	mapIdent := func(ident *goast.Ident) (token.Position, bool) {
		if ident == nil || gfset.File(ident.Pos()) != parsedTokenFile || gfset.File(ident.End()) != parsedTokenFile {
			return token.Position{}, false
		}
		parsedStart := parsedTokenFile.Offset(ident.Pos()) - len(partialGoWrapPrefix)
		parsedEnd := parsedTokenFile.Offset(ident.End()) - len(partialGoWrapPrefix)
		sourceStart, sourceEnd, ok := reconstructed.linearSourceSpan(parsedStart, parsedEnd)
		if !ok || sourceStart < 0 || sourceEnd > len(source) || string(source[sourceStart:sourceEnd]) != ident.Name {
			return token.Position{}, false
		}
		return sourceFset.Position(sourceTokenFile.Pos(sourceStart)), true
	}
	return partialGoASTSymbols(packageName, gf, mapPos, mapIdent)
}

func partialGoASTSymbols(packageName string, file *goast.File, mapPos func(token.Pos) (token.Position, bool), mapIdent func(*goast.Ident) (token.Position, bool)) []Symbol {
	var out []Symbol
	for _, d := range file.Decls {
		switch decl := d.(type) {
		case *goast.FuncDecl:
			if decl.Name == nil {
				continue
			}
			kind := symKindFunction
			container := packageName
			if decl.Recv != nil && len(decl.Recv.List) > 0 {
				kind = symKindMethod
				container = exprTypeName(decl.Recv.List[0].Type)
			}
			namePos, nameOK := mapIdent(decl.Name)
			declStart, startOK := mapPos(decl.Pos())
			declEnd, endOK := mapPos(decl.End())
			if !nameOK || !startOK || !endOK {
				continue
			}
			out = append(out, Symbol{
				Name: decl.Name.Name, Kind: kind, Container: container,
				NamePos: namePos, DeclStart: declStart, DeclEnd: declEnd,
			})
		case *goast.GenDecl:
			declStart, startOK := mapPos(decl.Pos())
			declEnd, endOK := mapPos(decl.End())
			if !startOK || !endOK {
				continue
			}
			switch decl.Tok {
			case token.TYPE:
				for _, sp := range decl.Specs {
					ts, ok := sp.(*goast.TypeSpec)
					if !ok || ts.Name == nil {
						continue
					}
					namePos, ok := mapIdent(ts.Name)
					if !ok {
						continue
					}
					out = append(out, Symbol{
						Name: ts.Name.Name, Kind: typeSpecKind(ts), Container: packageName,
						NamePos: namePos, DeclStart: declStart, DeclEnd: declEnd,
					})
				}
			case token.CONST, token.VAR:
				kind := symKindVariable
				if decl.Tok == token.CONST {
					kind = symKindConstant
				}
				for _, sp := range decl.Specs {
					vs, ok := sp.(*goast.ValueSpec)
					if !ok {
						continue
					}
					for _, n := range vs.Names {
						namePos, ok := mapIdent(n)
						if !ok || n.Name == "_" {
							continue
						}
						out = append(out, Symbol{
							Name: n.Name, Kind: kind, Container: packageName,
							NamePos: namePos, DeclStart: declStart, DeclEnd: declEnd,
						})
					}
				}
			}
		}
	}
	return out
}

func typeSpecKind(ts *goast.TypeSpec) int {
	switch ts.Type.(type) {
	case *goast.StructType:
		return symKindStruct
	case *goast.InterfaceType:
		return symKindInterface
	default:
		return symKindClass
	}
}

// receiverTypeName extracts the base type name from a component receiver
// source like "(f *Form)" or "(p UsersPage)" → "Form" / "UsersPage". It
// parses the receiver as real Go source (mirroring codegen.parseRecv) rather
// than string-splitting, so it handles pointer receivers, generic type args
// (e.g. "(f *Form[T, U])" → "Form"), and irregular spacing correctly. Falls
// back to the raw trimmed text if it cannot parse the shape.
func receiverTypeName(recv string) string {
	trimmed := strings.TrimSpace(recv)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", "package _\nfunc "+trimmed+" _m() {}", 0)
	if err != nil {
		return trimmed
	}
	fn, ok := f.Decls[0].(*goast.FuncDecl)
	if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 {
		return trimmed
	}
	if name := exprTypeName(fn.Recv.List[0].Type); name != "" {
		return name
	}
	return trimmed
}

// exprTypeName returns the base type name of a (possibly pointer or
// generic-instantiated) type expression, e.g. "*Form" → "Form",
// "Form[T]" → "Form", "*Form[T, U]" → "Form". Returns "" for shapes it
// doesn't recognize. Shared by receiver-type extraction for gsx component
// receivers and (future) Go method receivers.
func exprTypeName(e goast.Expr) string {
	switch t := e.(type) {
	case *goast.StarExpr:
		return exprTypeName(t.X)
	case *goast.IndexExpr:
		return exprTypeName(t.X)
	case *goast.IndexListExpr:
		return exprTypeName(t.X)
	case *goast.Ident:
		return t.Name
	default:
		return ""
	}
}
