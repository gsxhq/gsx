package lsp

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"go/token"
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

func TestSourceTextClassifiesOnlyExactPairedGeneratedGoAsUnavailable(t *testing.T) {
	dir := t.TempDir()
	const source = "package page\nvar Target = 1\n"
	write := func(name string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	write("page.gsx")
	paired := write("page.x.go")
	unpaired := write("hand.x.go")
	ordinary := write("ordinary.go")
	s := &Server{docs: newDocStore()}
	if got, ok := s.sourceText(paired); ok {
		t.Fatalf("sourceText paired generated file = %q, want unavailable", got)
	}
	for _, path := range []string{unpaired, ordinary} {
		if got, ok := s.sourceText(path); !ok || string(got) != source {
			t.Fatalf("sourceText authored %s = (%q, %t), want exact bytes", path, got, ok)
		}
	}

	openGSX := filepath.Join(dir, "open.gsx")
	openPaired := write("open.x.go")
	s.docs.open(pathToURI(openGSX), "package page\ncomponent Open() {}\n", 1)
	s.docs.open(pathToURI(openPaired), source, 1)
	if got, ok := s.sourceText(openPaired); ok {
		t.Fatalf("sourceText output paired with unsaved GSX = %q, want unavailable", got)
	}
}

func TestRequestSourceSnapshotFreezesExactPairedGeneratedOwnership(t *testing.T) {
	const source = "package page\nvar Target = 1\n"
	assertUnavailable := func(t *testing.T, snapshot *requestSourceSnapshot, path string) {
		t.Helper()
		if got, ok := snapshot.sourceText(path); ok {
			t.Fatalf("sourceText = %q, want paired output unavailable", got)
		}
		if got, ok := snapshot.position(path, 0); ok {
			t.Fatalf("position = %+v, want paired output unavailable", got)
		}
		if got, ok := snapshot.locationForGoPosition(token.Position{Filename: path, Line: 1, Column: 1}, 1); ok {
			t.Fatalf("locationForGoPosition = %+v, want paired output unavailable", got)
		}
	}
	assertAvailable := func(t *testing.T, snapshot *requestSourceSnapshot, path string) {
		t.Helper()
		if got, ok := snapshot.sourceText(path); !ok || string(got) != source {
			t.Fatalf("sourceText = (%q, %t), want exact authored bytes", got, ok)
		}
		if got, ok := snapshot.position(path, strings.Index(source, "Target")); !ok || got != (Position{Line: 1, Character: 4}) {
			t.Fatalf("position = (%+v, %t), want stable authored position", got, ok)
		}
		if got, ok := snapshot.locationForGoPosition(token.Position{Filename: path, Line: 2, Column: 5}, len("Target")); !ok || got.Range != (Range{Start: Position{Line: 1, Character: 4}, End: Position{Line: 1, Character: 10}}) {
			t.Fatalf("locationForGoPosition = (%+v, %t), want stable authored location", got, ok)
		}
	}

	t.Run("disk ownership observed present remains generated after sibling removal", func(t *testing.T) {
		dir := t.TempDir()
		gsxPath := filepath.Join(dir, "page.gsx")
		xgoPath := filepath.Join(dir, "page.x.go")
		if err := os.WriteFile(gsxPath, []byte("package page\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(xgoPath, []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
		snapshot := (&Server{docs: newDocStore()}).sourceSnapshot()
		assertUnavailable(t, snapshot, xgoPath)
		if err := os.Remove(gsxPath); err != nil {
			t.Fatal(err)
		}
		assertUnavailable(t, snapshot, xgoPath)
		assertUnavailable(t, snapshot, xgoPath)
	})

	t.Run("disk ownership observed absent remains authored after sibling creation", func(t *testing.T) {
		dir := t.TempDir()
		gsxPath := filepath.Join(dir, "hand.gsx")
		xgoPath := filepath.Join(dir, "hand.x.go")
		if err := os.WriteFile(xgoPath, []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
		snapshot := (&Server{docs: newDocStore()}).sourceSnapshot()
		assertAvailable(t, snapshot, xgoPath)
		if err := os.WriteFile(gsxPath, []byte("package page\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		assertAvailable(t, snapshot, xgoPath)
		assertAvailable(t, snapshot, xgoPath)
	})

	t.Run("open authoritative gsx is captured without disk source", func(t *testing.T) {
		dir := t.TempDir()
		gsxPath := filepath.Join(dir, "open.gsx")
		xgoPath := filepath.Join(dir, "open.x.go")
		if err := os.WriteFile(xgoPath, []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
		server := &Server{docs: newDocStore()}
		server.docs.open(pathToURI(gsxPath), "package page\ncomponent Open() {}\n", 1)
		snapshot := server.sourceSnapshot()
		assertUnavailable(t, snapshot, xgoPath)
		server.docs.close(pathToURI(gsxPath))
		assertUnavailable(t, snapshot, xgoPath)
	})
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

func TestAuthoredPositionAcceptsAuthoritativeOffsetWithoutLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "page.gsx")
	source := "package page\ncomponent Target() {}\n"
	start := strings.Index(source, "Target")
	s := &Server{docs: newDocStore(), enc: encUTF16}
	s.docs.open(pathToURI(path), source, 1)

	position := token.Position{Filename: path, Offset: start}
	got, ok := s.locationForAuthoredPosition(position, len("Target"))
	want := rangeForSpan(source, start, start+len("Target"), encUTF16)
	if !ok || got.Range != want {
		t.Fatalf("authored zero-line location = (%+v, %t), want exact-offset range %+v", got, ok, want)
	}
}

func TestRequestSourceSnapshotIsImmutableAndMemoizesDisk(t *testing.T) {
	dir := t.TempDir()
	openPath := filepath.Join(dir, "open.gsx")
	diskPath := filepath.Join(dir, "disk.gsx")
	if err := os.WriteFile(diskPath, []byte("disk one"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Server{docs: newDocStore(), enc: encUTF16}
	s.docs.open(pathToURI(openPath), "open one", 1)
	snapshot := s.sourceSnapshot()

	s.docs.update(pathToURI(openPath), "open two", 2)
	overrides := snapshot.openGSXOverrides()
	if got := string(overrides[openPath]); got != "open one" {
		t.Fatalf("captured analysis override = %q, want open one", got)
	}
	if got := snapshot.anyOpenDir(); got != dir {
		t.Fatalf("captured analysis directory = %q, want %q", got, dir)
	}
	if got, ok := snapshot.sourceString(openPath); !ok || got != "open one" {
		t.Fatalf("captured open string = (%q, %t), want open one", got, ok)
	}
	if err := os.WriteFile(diskPath, []byte("disk two"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok := snapshot.sourceString(diskPath); !ok || got != "disk two" {
		t.Fatalf("first disk string = (%q, %t), want disk two", got, ok)
	}
	if got, ok := snapshot.sourceText(openPath); !ok || string(got) != "open one" {
		t.Fatalf("captured open bytes = (%q, %t), want open one", got, ok)
	}
	if err := os.WriteFile(diskPath, []byte("disk three"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok := snapshot.sourceText(diskPath); !ok || string(got) != "disk two" {
		t.Fatalf("memoized disk bytes = (%q, %t), want disk two", got, ok)
	}
}

func TestRequestSourceSnapshotPositionConversionsDoNotCopyFullSource(t *testing.T) {
	dir := t.TempDir()
	source := strings.Repeat("a", 1<<20) + "\nTarget\n"
	start := strings.Index(source, "Target")
	span := sourceintel.Span{Start: start, End: start + len("Target")}

	for _, test := range []struct {
		name string
		open bool
	}{
		{name: "open", open: true},
		{name: "disk"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(dir, test.name+".gsx")
			if test.open {
				server := &Server{docs: newDocStore(), enc: encUTF16}
				server.docs.open(pathToURI(path), source, 1)
				snapshot := server.sourceSnapshot()
				assertSnapshotPositionAllocations(t, snapshot, path, span)
				return
			}
			if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
				t.Fatal(err)
			}
			snapshot := (&Server{docs: newDocStore(), enc: encUTF16}).sourceSnapshot()
			assertSnapshotPositionAllocations(t, snapshot, path, span)
		})
	}
}

func assertSnapshotPositionAllocations(t *testing.T, snapshot *requestSourceSnapshot, path string, span sourceintel.Span) {
	t.Helper()
	span.Path = path
	if _, ok := snapshot.rangeForSpan(span); !ok {
		t.Fatal("rangeForSpan warmup failed")
	}
	if _, ok := snapshot.position(path, span.Start); !ok {
		t.Fatal("position warmup failed")
	}

	for name, operation := range map[string]func() bool{
		"range": func() bool {
			_, ok := snapshot.rangeForSpan(span)
			return ok
		},
		"position": func() bool {
			_, ok := snapshot.position(path, span.Start)
			return ok
		},
	} {
		t.Run(name, func(t *testing.T) {
			result := testing.Benchmark(func(b *testing.B) {
				b.ReportAllocs()
				for range b.N {
					if !operation() {
						b.Fatal("position conversion failed")
					}
				}
			})
			if got := result.AllocedBytesPerOp(); got != 0 {
				t.Fatalf("position conversion allocated %d bytes/op, want 0 after warmup", got)
			}
		})
	}
}

func TestVersionedSpanValidatesTheCapturedRequestSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "target.gsx")
	analyzed := []byte("package target\ncomponent Card() {}\n")
	open := []byte("package target\n/*😀*/ component Card() {}\n")
	start := strings.Index(string(analyzed), "Card")
	span := sourceintel.VersionedSpan{
		Span:          sourceintel.Span{Path: path, Start: start, End: start + len("Card")},
		SourceVersion: sourceintel.SourceVersion{Size: len(analyzed), SHA256: sha256.Sum256(analyzed)},
	}
	s := &Server{docs: newDocStore(), enc: encUTF16}
	s.docs.open(pathToURI(path), string(open), 1)
	if got, ok := s.sourceSnapshot().locationForVersionedSpan(span); ok {
		t.Fatalf("stale versioned location = %+v, want fail closed", got)
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
func (a *authoritativeLocationAnalyzer) AnalyzeEphemeral(string, string, []byte) (*Package, error) {
	return nil, errors.New("not implemented")
}
func (a *authoritativeLocationAnalyzer) AnalyzeEphemeralNonBlocking(string, string, []byte) (*Package, bool, error) {
	return nil, true, errors.New("not implemented")
}
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

func TestDefinitionRetainsCrossPackageComponentExactAuthoredSpan(t *testing.T) {
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
	if len(cursor.fact.TargetDecls) != 1 {
		t.Fatalf("retained target declarations = %+v, want one", cursor.fact.TargetDecls)
	}
	span := cursor.fact.TargetDecls[0].Span
	if filepath.Base(span.Path) != "card.gsx" || span.Start != strings.Index(card, "Card") || span.End != span.Start+len("Card") {
		t.Fatalf("retained target span = %+v, want card.gsx Card span", span)
	}
}

func TestDefinitionRetainsCrossPackageComponentIndexForExpressionObject(t *testing.T) {
	const page = `package page

import "example.com/app/helpers"

component Page() { <div>{helpers.Thing()}</div> }
`
	const dependency = `package helpers

component Thing() { <span/> }
`
	pkg, path := analyzedLSPModule(t, map[string]string{
		"page/page.gsx":       page,
		"helpers/helpers.gsx": dependency,
	}, "page/page.gsx")
	offset := strings.Index(page, "Thing") + 1
	target, ok := exprDefinitionTargetAt(pkg, path, offset)
	if !ok {
		t.Fatal("expression target was not resolved")
	}
	object := sourceintel.Origin(target.object)
	if object.Pkg() == nil {
		t.Fatalf("expression object = %v, want package object", object)
	}
	key := ComponentDeclKey{PackagePath: object.Pkg().Path(), ComponentKey: componentObjectKey(object)}
	declarations := pkg.ComponentDecls[key]
	if len(declarations) != 1 {
		t.Fatalf("component declaration index[%+v] = %+v; object=%T %s position=%+v all=%+v", key, declarations, object, object, pkg.Fset.Position(object.Pos()), pkg.ComponentDecls)
	}
	s := &Server{docs: newDocStore(), enc: encUTF16}
	stale := strings.Replace(dependency, "component Thing", "/*😀*/ component Thing", 1)
	s.docs.open(pathToURI(declarations[0].Span.Path), stale, 2)
	if result, ok := objectDefinitionResult(s.sourceSnapshot(), pkg, object); ok {
		t.Fatalf("stale expression component definition = %+v, want fail closed", result)
	}
}

func TestComponentCallHoverFailsClosedOnStaleTargetVersion(t *testing.T) {
	const page = `package page

import "example.com/app/widgets"

component Page() { <widgets.Card title="hello"/> }
`
	const dependency = `package widgets

component Card(title string) { <span/> }
`
	pkg, pagePath := analyzedLSPModule(t, map[string]string{
		"page/page.gsx":    page,
		"widgets/card.gsx": dependency,
	}, "page/page.gsx")
	var targetPath string
	for _, call := range pkg.ComponentCalls {
		if len(call.TargetDecls) == 1 {
			targetPath = call.TargetDecls[0].Span.Path
		}
	}
	if targetPath == "" {
		t.Fatal("cross-package call retained no target provenance")
	}
	stale := strings.Replace(dependency, "component Card", "/*😀*/ component Card", 1)
	pageURI, targetURI := pathToURI(pagePath), pathToURI(targetPath)
	cursor := strings.Index(page, "widgets.Card") + len("widgets.")
	out := drive(t, &authoritativeLocationAnalyzer{pkg: pkg}, initFrame()+didOpenFrame(targetURI, stale)+didOpenFrame(pageURI, page)+
		hoverFrame(2, pageURI, positionForByteOffset(page, cursor, encUTF16))+exitFrame())
	if got := hoverResult(t, out, 2); got != nil {
		t.Fatalf("stale target hover = %+v, want fail closed", got)
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
	writeWorkspaceSymbolSource(t, filepath.Join(dir, "go.mod"), "module example.test/page\n\ngo 1.26.1\n")
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
	out := drive(t, a, workspaceSymbolInitializeFrame(dir)+didOpenFrame(uri, open)+wsSymFrame(2, "Target")+exitFrame())
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
