package lsp

import (
	"encoding/json"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/gsxfmt"
	"github.com/gsxhq/gsx/internal/pretty"
	"github.com/gsxhq/gsx/internal/sourceintel"
)

func TestSourceTextPrefersOpenDocument(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "page.gsx")
	if err := os.WriteFile(path, []byte("package page\nvar Target = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	open := "package page\nvar \U0001f600 Target = 1\n"
	start := len("package page\nvar \U0001f600 ")

	s := &Server{docs: newDocStore(), enc: encUTF16}
	s.docs.open(pathToURI(path), open, 1)

	got, ok := s.sourceText(path)
	if !ok {
		t.Fatal("sourceText returned no source")
	}
	if string(got) != open {
		t.Fatalf("sourceText = %q, want open buffer %q", got, open)
	}
	gotRange, ok := s.rangeForSpan(sourceintel.Span{Path: path, Start: start, End: start + len("Target")})
	if !ok {
		t.Fatal("rangeForSpan returned no range")
	}
	want := Range{Start: Position{Line: 1, Character: 7}, End: Position{Line: 1, Character: 13}}
	if gotRange != want {
		t.Fatalf("rangeForSpan = %+v, want %+v", gotRange, want)
	}
}

func TestSourceTextDiskFallbackAndFailClosed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "with space.gsx")
	source := "package page\nvar \U0001f600 Target = 1\n"
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Server{docs: newDocStore(), enc: encUTF16}

	for _, input := range []string{path, pathToURI(path)} {
		got, ok := s.sourceText(input)
		if !ok || string(got) != source {
			t.Errorf("sourceText(%q) = (%q, %t), want disk source", input, got, ok)
		}
	}
	start := strings.Index(source, "Target")
	if got, ok := s.position(pathToURI(path), start); !ok || got != (Position{Line: 1, Character: 7}) {
		t.Fatalf("position = (%+v, %t), want UTF-16 disk position", got, ok)
	}
	if got, ok := s.locationForSpan(sourceintel.Span{Path: path, Start: start, End: start + len("Target")}); !ok || got.URI != pathToURI(path) {
		t.Fatalf("locationForSpan = (%+v, %t), want normalized file URI", got, ok)
	}

	invalid := []sourceintel.Span{
		{Path: path, Start: -1, End: 0},
		{Path: path, Start: start + 1, End: start},
		{Path: path, Start: 0, End: len(source) + 1},
		{Path: filepath.Join(dir, "missing.gsx"), Start: 0, End: 0},
	}
	for _, span := range invalid {
		if got, ok := s.rangeForSpan(span); ok {
			t.Errorf("rangeForSpan(%+v) = %+v, want fail closed", span, got)
		}
		if got, ok := s.locationForSpan(span); ok {
			t.Errorf("locationForSpan(%+v) = %+v, want fail closed", span, got)
		}
	}
	for _, offset := range []int{-1, len(source) + 1} {
		if got, ok := s.position(path, offset); ok {
			t.Errorf("position(%d) = %+v, want fail closed", offset, got)
		}
	}
}

func TestSourceTextNormalizesOpenDocumentPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "page.gsx")
	if err := os.WriteFile(path, []byte("saved"), 0o600); err != nil {
		t.Fatal(err)
	}
	dirtyURI := pathToURI(dir) + "/nested/../page.gsx"
	s := &Server{docs: newDocStore(), enc: encUTF16}
	s.docs.open(dirtyURI, "open", 1)

	got, ok := s.sourceText(path)
	if !ok || string(got) != "open" {
		t.Fatalf("sourceText(%q) = (%q, %t), want normalized open document", path, got, ok)
	}
}

func TestSourceTextExcludesPhysicalGeneratedGo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "page.x.go")
	if err := os.WriteFile(path, []byte("package page\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Server{docs: newDocStore()}
	if got, ok := s.sourceText(path); ok {
		t.Fatalf("sourceText generated file = %q, want unavailable", got)
	}
}

func TestTokenPositionAdapterUsesAuthoritativeGoSource(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dependency.go")
	saved := "package dep\nvar _ = \"x\"; var Target = 1\n"
	open := "package dep\nvar _ = \"\U0001f600\"; var Target = 1\n"
	if err := os.WriteFile(path, []byte(saved), 0o600); err != nil {
		t.Fatal(err)
	}
	start := strings.Index(open, "Target")
	s := &Server{docs: newDocStore(), enc: encUTF16}
	s.docs.open(pathToURI(path), open, 1)

	got, ok := s.locationForGoPosition(authoredTokenPosition(path, open, start), len("Target"))
	want := rangeForSpan(open, start, start+len("Target"), encUTF16)
	if !ok || got.Range != want {
		t.Fatalf("locationForTokenPosition = (%+v, %t), want open-buffer range %+v", got, ok, want)
	}

	missingGo := token.Position{Filename: filepath.Join(dir, "missing.go"), Line: 3, Column: 5}
	got, ok = s.locationForGoPosition(missingGo, 6)
	if !ok || got.Range != (Range{Start: Position{Line: 2, Character: 4}, End: Position{Line: 2, Character: 10}}) {
		t.Fatalf("missing Go fallback = (%+v, %t), want byte-column range", got, ok)
	}
	missingGSX := token.Position{Filename: filepath.Join(dir, "missing.gsx"), Line: 3, Column: 5}
	if got, ok := s.locationForGoPosition(missingGSX, 6); ok {
		t.Fatalf("missing GSX location = %+v, want fail closed", got)
	}
}

func TestAuthoredPositionUsesExactOffsetAndGoPositionUsesLineColumn(t *testing.T) {
	dir := t.TempDir()
	gsxPath := filepath.Join(dir, "page.gsx")
	goPath := filepath.Join(dir, "dependency.go")
	source := "package page\nvar _ = \"\U0001f600\"; var Target = 1\n"
	start := strings.Index(source, "Target")
	s := &Server{docs: newDocStore(), enc: encUTF16}
	s.docs.open(pathToURI(gsxPath), source, 1)
	s.docs.open(pathToURI(goPath), source, 1)

	authored := authoredTokenPosition(gsxPath, source, start)
	authored.Line = 1
	authored.Column = 1
	got, ok := s.locationForAuthoredPosition(authored, len("Target"))
	want := rangeForSpan(source, start, start+len("Target"), encUTF16)
	if !ok || got.Range != want {
		t.Fatalf("authored location with conflicting line/column = (%+v, %t), want exact-offset range %+v", got, ok, want)
	}

	dependency := authoredTokenPosition(goPath, source, start)
	dependency.Offset = 0
	got, ok = s.locationForGoPosition(dependency, len("Target"))
	if !ok || got.Range != want {
		t.Fatalf("Go location with conflicting offset = (%+v, %t), want line/column range %+v", got, ok, want)
	}
}

type authoritativeLocationAnalyzer struct {
	pkg  *Package
	refs []CrossRef
	syms []Symbol
}

func (a *authoritativeLocationAnalyzer) SetOverride(string, []byte) ([]string, error) {
	return nil, nil
}
func (a *authoritativeLocationAnalyzer) ClearOverride(string) ([]string, error) { return nil, nil }
func (a *authoritativeLocationAnalyzer) Analyze(string, map[string][]byte) (*Package, error) {
	if a.pkg != nil {
		return a.pkg, nil
	}
	return &Package{}, nil
}
func (a *authoritativeLocationAnalyzer) AnalyzeModule(string, map[string][]byte) ([]CrossRef, error) {
	return a.refs, nil
}
func (a *authoritativeLocationAnalyzer) AnalyzeModuleParams(string, map[string][]byte) ([]ComponentParamRenameFact, error) {
	return nil, nil
}
func (a *authoritativeLocationAnalyzer) ModuleSymbols(string, map[string][]byte) ([]Symbol, error) {
	return a.syms, nil
}
func (*authoritativeLocationAnalyzer) FormatSettings(string) gsxfmt.FormatSettings {
	return gsxfmt.FormatSettings{Width: 80, TabWidth: pretty.DefaultTabWidth}
}
func (*authoritativeLocationAnalyzer) ImportsMode(string) gsxfmt.ImportsMode {
	return gsxfmt.ImportsGoimports
}
func (*authoritativeLocationAnalyzer) ResolveImport(string, string, string) []string { return nil }

func TestDefinitionUsesUnsavedUTF16Target(t *testing.T) {
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "target.gsx")
	saved := "package page\nvar _ = \"x\"; var Target = 1\n"
	open := "package page\nvar _ = \"\U0001f600\"; var Target = 1\n"
	if err := os.WriteFile(targetPath, []byte(saved), 0o600); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(dir, "source.go")
	source := "package page\nvar Target int\n"
	targetStart := strings.Index(open, "Target")
	sourceStart := strings.Index(source, "Target")
	a := &authoritativeLocationAnalyzer{pkg: &Package{NavIndex: []NavRef{{
		From: authoredTokenPosition(sourcePath, source, sourceStart),
		Name: "Target",
		To:   authoredTokenPosition(targetPath, open, targetStart),
	}}}}
	targetURI, sourceURI := pathToURI(targetPath), pathToURI(sourcePath)
	out := drive(t, a, initFrame()+didOpenFrame(targetURI, open)+didOpenFrame(sourceURI, source)+
		definitionFrame(2, sourceURI, positionForByteOffset(source, sourceStart, encUTF16))+exitFrame())
	got := definitionLocation(t, out, 2)
	want := rangeForSpan(open, targetStart, targetStart+len("Target"), encUTF16)
	if got == nil || got.URI != targetURI || got.Range != want {
		t.Fatalf("definition = %+v, want open-buffer UTF-16 location %s %+v", got, targetURI, want)
	}
}

func TestDefinitionResolvesCrossPackageComponentThroughExactASTOffset(t *testing.T) {
	const page = `package page

import "example.com/app/widgets"

component Page() {
	<widgets.Card title="hello"/>
}
`
	const card = `package widgets

component Card(title string) {
	<p>{title}</p>
}
`
	pkg, path := analyzedLSPModule(t, map[string]string{
		"page/page.gsx":    page,
		"widgets/card.gsx": card,
	}, "page/page.gsx")
	cursor, ok := componentTargetAtOffset(pkg, path, strings.Index(page, "widgets.Card")+len("widgets."))
	if !ok {
		t.Fatal("componentTargetAtOffset returned no cross-package call")
	}
	component, fset, ok := componentForObject(pkg, componentTargetObject(cursor.fact))
	if !ok {
		object := componentTargetObject(cursor.fact)
		var imports []string
		for _, imported := range pkg.Types.Imports() {
			candidate := imported.Scope().Lookup(object.Name())
			candidateText := "<nil>"
			if candidate != nil {
				candidateText = types.ObjectString(candidate, nil)
			}
			imports = append(imports, imported.Path()+"/"+imported.Name()+" candidate="+candidateText)
		}
		t.Fatalf("componentForObject returned no exact cross-package declaration: object=%s pos=%+v imports=%v",
			types.ObjectString(object, nil), pkg.Fset.Position(object.Pos()), imports)
	}
	position := fset.Position(component.NamePos)
	if position.Offset != strings.Index(card, "Card") {
		t.Fatalf("component position offset = %d, want %d", position.Offset, strings.Index(card, "Card"))
	}
}

func TestReferencesUseUnsavedUTF16Targets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "page.gsx")
	saved := "package page\ncomponent Card() {}; x Card\n"
	open := "package page\ncomponent Card() {}; \"\U0001f600\" Card\n"
	if err := os.WriteFile(path, []byte(saved), 0o600); err != nil {
		t.Fatal(err)
	}
	declStart := strings.Index(open, "Card")
	targetStart := strings.LastIndex(open, "Card")
	decl := authoredTokenPosition(path, open, declStart)
	ref := authoredTokenPosition(path, open, targetStart)
	a := &authoritativeLocationAnalyzer{refs: []CrossRef{{Name: "Card", Decl: decl, Refs: []token.Position{ref}}}}
	uri := pathToURI(path)
	out := drive(t, a, initFrame()+didOpenFrame(uri, open)+
		refsFrame(2, uri, decl.Line-1, positionForByteOffset(open, declStart, encUTF16).Character)+exitFrame())
	locations := referenceLocations(t, out, 2)
	want := rangeForSpan(open, targetStart, targetStart+len("Card"), encUTF16)
	if len(locations) != 1 || locations[0].URI != uri || locations[0].Range != want {
		t.Fatalf("references = %+v, want open-buffer UTF-16 target %s %+v", locations, uri, want)
	}
}

func TestHoverRangeUsesUnsavedUTF16Target(t *testing.T) {
	const open = "package page\nfunc helper() int { _ = \"\U0001f600\"; result := 1; return result }\n"
	pkg, path := analyzedLSPPackage(t, open)
	saved := strings.Replace(open, "\U0001f600", "x", 1)
	if err := os.WriteFile(path, []byte(saved), 0o600); err != nil {
		t.Fatal(err)
	}
	start := strings.Index(open, "return result") + len("return ")
	uri := pathToURI(path)
	out := drive(t, &authoritativeLocationAnalyzer{pkg: pkg}, initFrame()+didOpenFrame(uri, open)+
		hoverFrame(2, uri, positionForByteOffset(open, start, encUTF16))+exitFrame())
	got := hoverResult(t, out, 2)
	want := rangeForSpan(open, start, start+len("result"), encUTF16)
	if got == nil || got.Range == nil || *got.Range != want {
		t.Fatalf("hover range = %+v, want open-buffer UTF-16 range %+v", got, want)
	}
}

func TestDocumentSymbolsUseUnsavedUTF16Target(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "page.gsx")
	saved := "package page\nvar _ = \"x\"; var Target = 1\n"
	open := "package page\nvar _ = \"\U0001f600\"; var Target = 1\n"
	if err := os.WriteFile(path, []byte(saved), 0o600); err != nil {
		t.Fatal(err)
	}
	uri := pathToURI(path)
	out := drive(t, symbolFileAnalyzer{}, initFrame()+didOpenFrame(uri, open)+docSymFrame(2, uri)+exitFrame())
	var symbols []DocumentSymbol
	decodeResult(t, out, 2, &symbols)
	start := strings.Index(open, "Target")
	want := rangeForSpan(open, start, start+len("Target"), encUTF16)
	for _, symbol := range symbols {
		if symbol.Name == "Target" {
			if symbol.SelectionRange != want {
				t.Fatalf("document symbol range = %+v, want open-buffer UTF-16 range %+v", symbol.SelectionRange, want)
			}
			return
		}
	}
	t.Fatalf("Target missing from document symbols: %+v", symbols)
}

func TestWorkspaceSymbolsUseUnsavedUTF16Target(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "page.gsx")
	saved := "package page\nvar _ = \"x\"; var Target = 1\n"
	open := "package page\nvar _ = \"\U0001f600\"; var Target = 1\n"
	if err := os.WriteFile(path, []byte(saved), 0o600); err != nil {
		t.Fatal(err)
	}
	start := strings.Index(open, "Target")
	pos := authoredTokenPosition(path, open, start)
	a := &authoritativeLocationAnalyzer{syms: []Symbol{{Name: "Target", Kind: symKindVariable, Container: "page", NamePos: pos}}}
	uri := pathToURI(path)
	out := drive(t, a, initFrame()+didOpenFrame(uri, open)+wsSymFrame(2, "Target")+exitFrame())
	var symbols []SymbolInformation
	decodeResult(t, out, 2, &symbols)
	want := rangeForSpan(open, start, start+len("Target"), encUTF16)
	if len(symbols) != 1 || symbols[0].Location.URI != uri || symbols[0].Location.Range != want {
		t.Fatalf("workspace symbols = %+v, want open-buffer UTF-16 location %s %+v", symbols, uri, want)
	}
}

func authoredTokenPosition(path, source string, offset int) token.Position {
	lineStart := strings.LastIndex(source[:offset], "\n") + 1
	return token.Position{
		Filename: path,
		Offset:   offset,
		Line:     strings.Count(source[:offset], "\n") + 1,
		Column:   offset - lineStart + 1,
	}
}

func referenceLocations(t *testing.T, output string, id int) []Location {
	t.Helper()
	var locations []Location
	decodeResult(t, output, id, &locations)
	return locations
}

func decodeResult(t *testing.T, output string, id int, result any) {
	t.Helper()
	for _, message := range readFrames(t, output) {
		var gotID int
		if err := json.Unmarshal(message["id"], &gotID); err != nil || gotID != id {
			continue
		}
		if err := json.Unmarshal(message["result"], result); err != nil {
			t.Fatalf("decode result for id %d: %v", id, err)
		}
		return
	}
	t.Fatalf("no response for id %d in:\n%s", id, output)
}
