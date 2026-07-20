package codegen

import (
	"crypto/sha256"
	"go/token"
	"reflect"
	"strings"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/attrclass"
	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/internal/sourceintel"
	gsxparser "github.com/gsxhq/gsx/parser"
)

func TestSkeletonSourceWriterRejectsNonIdenticalAuthoredBytes(t *testing.T) {
	w := newSkeletonSourceWriter("input.gsx", []byte("alpha"))
	w.writeGenerated("prefix:")
	if err := w.writeAuthored(0, 5, "omega", sourceintel.Definition); err == nil {
		t.Fatal("writeAuthored accepted bytes that differ from the authored source")
	}
	if got := w.builder.String(); got != "prefix:" {
		t.Fatalf("writer contents = %q, want generated prefix only", got)
	}
}

func TestSkeletonSourceWriterPreservesFirstErrorAndStopsMutation(t *testing.T) {
	const path = "input.gsx"
	source := []byte("alpha beta")
	w := newSkeletonSourceWriter(path, source)
	w.writeGenerated("prefix:")
	if err := w.writeAuthored(0, 5, "alpha", sourceintel.Definition); err != nil {
		t.Fatalf("initial writeAuthored: %v", err)
	}
	if err := w.addDeclarationRegion(sourceintel.Span{Path: path, Start: 0, End: 5}, 0, w.builder.Len()); err != nil {
		t.Fatalf("initial addDeclarationRegion: %v", err)
	}

	firstErr := w.writeAuthored(6, 10, "BETA", sourceintel.Hover)
	if firstErr == nil {
		t.Fatal("writeAuthored accepted non-identical bytes")
	}
	wantBytes := w.builder.String()
	wantSegments := append([]sourceintel.Segment(nil), w.segments...)
	wantRegions := append([]sourceintel.DeclarationRegion(nil), w.regions...)

	if n, err := w.Write([]byte("write")); n != 0 || err != firstErr {
		t.Errorf("Write after error = (%d, %v), want (0, original error %v)", n, err, firstErr)
	}
	if n, err := w.WriteString("string"); n != 0 || err != firstErr {
		t.Errorf("WriteString after error = (%d, %v), want (0, original error %v)", n, err, firstErr)
	}
	if err := w.WriteByte('b'); err != firstErr {
		t.Errorf("WriteByte after error = %v, want original error %v", err, firstErr)
	}
	w.writeGenerated("generated")
	if err := w.writeAuthored(6, 10, "beta", sourceintel.Hover); err != firstErr {
		t.Errorf("writeAuthored after error = %v, want original error %v", err, firstErr)
	}
	if err := w.writeAuthoredAt(nil, token.NoPos, "later", sourceintel.Hover); err != firstErr {
		t.Errorf("writeAuthoredAt after error = %v, want original error %v", err, firstErr)
	}
	if err := w.addDeclarationRegion(sourceintel.Span{Path: path, Start: 6, End: 10}, 0, w.builder.Len()); err != firstErr {
		t.Errorf("addDeclarationRegion after error = %v, want original error %v", err, firstErr)
	}
	child := newSkeletonSourceWriter(path, source)
	child.writeGenerated("child")
	if err := child.writeAuthored(6, 10, "beta", sourceintel.Definition); err != nil {
		t.Fatalf("child writeAuthored: %v", err)
	}
	if err := child.addDeclarationRegion(sourceintel.Span{Path: path, Start: 6, End: 10}, 0, child.builder.Len()); err != nil {
		t.Fatalf("child addDeclarationRegion: %v", err)
	}
	if err := w.appendMapped(child); err != firstErr {
		t.Errorf("appendMapped after error = %v, want original error %v", err, firstErr)
	}
	if _, _, err := w.finish(); err != firstErr {
		t.Errorf("finish after error = %v, want original error %v", err, firstErr)
	}

	if got := w.builder.String(); got != wantBytes {
		t.Errorf("builder after error = %q, want unchanged %q", got, wantBytes)
	}
	if !reflect.DeepEqual(w.segments, wantSegments) {
		t.Errorf("segments after error = %+v, want unchanged %+v", w.segments, wantSegments)
	}
	if !reflect.DeepEqual(w.regions, wantRegions) {
		t.Errorf("regions after error = %+v, want unchanged %+v", w.regions, wantRegions)
	}
}

func TestSkeletonSourceWriterMapsExactWrites(t *testing.T) {
	source := []byte("alpha beta")
	w := newSkeletonSourceWriter("input.gsx", source)
	w.writeGenerated("prefix:")
	const capabilities = sourceintel.Definition | sourceintel.Hover | sourceintel.Completion
	if err := w.writeAuthored(6, 10, "beta", capabilities); err != nil {
		t.Fatalf("writeAuthored: %v", err)
	}

	generated, sourceMap, err := w.finish()
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if generated != "prefix:beta" {
		t.Fatalf("generated = %q, want %q", generated, "prefix:beta")
	}
	for _, capability := range []sourceintel.Capability{sourceintel.Definition, sourceintel.Hover, sourceintel.Completion} {
		span, ok := sourceMap.SourceSpan(len("prefix:"), len("prefix:beta"), capability)
		if !ok {
			t.Fatalf("SourceSpan capability %d not found", capability)
		}
		if span != (sourceintel.Span{Path: "input.gsx", Start: 6, End: 10}) {
			t.Fatalf("SourceSpan capability %d = %+v", capability, span)
		}
	}
	if _, ok := sourceMap.SourceSpan(len("prefix:"), len("prefix:beta"), sourceintel.Symbol); ok {
		t.Fatal("SourceSpan unexpectedly grants Symbol")
	}
}

func TestSkeletonSourceWriterRebasesMappedChild(t *testing.T) {
	source := []byte("alpha beta")
	parent := newSkeletonSourceWriter("input.gsx", source)
	parent.writeGenerated("package p\n")
	parentLen := parent.builder.Len()

	child := newSkeletonSourceWriter("input.gsx", source)
	child.writeGenerated("func ")
	if err := child.writeAuthored(0, 5, "alpha", sourceintel.Definition|sourceintel.Hover); err != nil {
		t.Fatalf("child writeAuthored: %v", err)
	}
	if err := child.addDeclarationRegion(sourceintel.Span{Path: "input.gsx", Start: 0, End: 10}, 0, child.builder.Len()); err != nil {
		t.Fatalf("child addDeclarationRegion: %v", err)
	}
	if err := parent.appendMapped(child); err != nil {
		t.Fatalf("appendMapped: %v", err)
	}

	generated, sourceMap, err := parent.finish()
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if generated != "package p\nfunc alpha" {
		t.Fatalf("generated = %q", generated)
	}
	span, ok := sourceMap.SourceSpan(parentLen+len("func "), len(generated), sourceintel.Definition|sourceintel.Hover)
	if !ok || span != (sourceintel.Span{Path: "input.gsx", Start: 0, End: 5}) {
		t.Fatalf("rebased source span = %+v, %v", span, ok)
	}
	decl, ok := sourceMap.DeclarationSpan(parentLen, len(generated))
	if !ok || decl != (sourceintel.Span{Path: "input.gsx", Start: 0, End: 10}) {
		t.Fatalf("rebased declaration span = %+v, %v", decl, ok)
	}
}

func TestBuildMappedSkeletonAuthoredRegions(t *testing.T) {
	const read = sourceintel.Definition | sourceintel.Hover
	tests := []struct {
		name       string
		source     string
		target     string
		component  []string
		completion bool
		symbol     bool
	}{
		{
			name:       "top-level GoChunk",
			source:     "package p\n\ntype NativeThing struct{}\n",
			target:     "type NativeThing struct{}",
			completion: true,
			symbol:     true,
		},
		{
			name:       "component receiver",
			source:     "package p\n\ntype Receiver struct{}\ncomponent (receiver *Receiver) Widget() {}\n",
			target:     "(receiver *Receiver)",
			component:  []string{"Widget"},
			completion: true,
		},
		{
			name:       "component name",
			source:     "package p\n\ncomponent UniqueWidget() {}\n",
			target:     "UniqueWidget",
			component:  []string{"UniqueWidget"},
			completion: true,
		},
		{
			name:       "component type parameters",
			source:     "package p\n\ncomponent GenericWidget[UniqueType any]() {}\n",
			target:     "UniqueType any",
			component:  []string{"GenericWidget"},
			completion: true,
		},
		{
			name:       "component parameters",
			source:     "package p\n\ncomponent ParameterWidget(uniqueParameter string) {}\n",
			target:     "uniqueParameter string",
			component:  []string{"ParameterWidget"},
			completion: true,
		},
		{
			name: "explicit invocation type arguments",
			source: "package p\n\ncomponent GenericTarget[T any](value T) {}\n" +
				"component Caller(value string) { <GenericTarget[UniqueAlias] value={value}/> }\n",
			target:     "UniqueAlias",
			component:  []string{"GenericTarget", "Caller"},
			completion: true,
		},
		{
			name:       "ordinary expression",
			source:     "package p\n\ncomponent ExpressionWidget(ordinaryExpression string) { <p>{ ordinaryExpression }</p> }\n",
			target:     "ordinaryExpression",
			component:  []string{"ExpressionWidget"},
			completion: true,
		},
		{
			name:       "control clause",
			source:     "package p\n\ncomponent ControlWidget(controlCondition bool) { { if controlCondition { <p/> } } }\n",
			target:     "controlCondition",
			component:  []string{"ControlWidget"},
			completion: true,
		},
		{
			name:       "pipeline expression",
			source:     "package p\n\ncomponent PipelineWidget(pipelineInput string) { <p>{ pipelineInput |> upper }</p> }\n",
			target:     "pipelineInput",
			component:  []string{"PipelineWidget"},
			completion: true,
		},
		{
			name:       "embedded literal hole",
			source:     "package p\n\ncomponent LiteralWidget(literalHole string) { <p>{ f`prefix @{literalHole}` }</p> }\n",
			target:     "literalHole",
			component:  []string{"LiteralWidget"},
			completion: true,
		},
		{
			name:       "nested markup in Go expression",
			source:     "package p\n\ncomponent NestedWidget(nestedValue string) { <p>{ wrap(<span>{nestedValue}</span>) }</p> }\n",
			target:     "nestedValue",
			component:  []string{"NestedWidget"},
			completion: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := test.name + ".gsx"
			fset := token.NewFileSet()
			file, err := gsxparser.ParseFile(fset, path, []byte(test.source), 0)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			bag := diag.NewBag(fset)
			declNames := make(map[string]bool, len(test.component))
			for _, name := range test.component {
				declNames[name] = true
			}
			if _, err := preprocessComponentCallSites(map[string]*gsxast.File{path: file}, declNames, fset, attrclass.Builtin(), bag); err != nil {
				t.Fatalf("preprocess: %v", err)
			}
			gsxast.Inspect(file, func(node gsxast.Node) bool {
				if element, ok := node.(*gsxast.Element); ok && declNames[element.Tag] {
					element.IsComponent = true
				}
				return true
			})
			table := filterTable{"upper": {funcName: "Upper", alias: "_gsxstd", pkgPath: stdImportPath}}
			build, err := buildMappedSkeleton(file, funcTables{filters: table}, fset, bag, nil, skeletonFull, path, []byte(test.source))
			if err != nil {
				t.Fatalf("buildMappedSkeleton: %v", err)
			}
			if build.sourceHash != sha256.Sum256([]byte(test.source)) {
				t.Fatal("sourceHash does not match the exact authored bytes")
			}
			assertMappedSkeletonTarget(t, build, path, test.source, test.target, read, test.completion, test.symbol)
		})
	}
}

func assertMappedSkeletonTarget(t *testing.T, build skeletonBuild, path, source, target string, read sourceintel.Capability, completion, symbol bool) {
	t.Helper()
	sourceStart := strings.LastIndex(source, target)
	if sourceStart < 0 {
		t.Fatalf("source does not contain target %q", target)
	}
	want := sourceintel.Span{Path: path, Start: sourceStart, End: sourceStart + len(target)}
	generatedStart := 0
	for {
		rel := strings.Index(build.source[generatedStart:], target)
		if rel < 0 {
			break
		}
		start := generatedStart + rel
		end := start + len(target)
		if got, ok := build.sourceMap.SourceSpan(start, end, read); ok && got == want {
			if _, ok := build.sourceMap.SourceSpan(start, end, sourceintel.Completion); ok != completion {
				t.Fatalf("Completion mapping = %v, want %v", ok, completion)
			}
			if _, ok := build.sourceMap.SourceSpan(start, end, sourceintel.Symbol); ok != symbol {
				t.Fatalf("Symbol mapping = %v, want %v", ok, symbol)
			}
			return
		}
		generatedStart = start + 1
	}
	t.Fatalf("no generated %q occurrence maps to authored span %+v\nskeleton:\n%s", target, want, build.source)
}

func TestBuildMappedSkeletonGoWithElements(t *testing.T) {
	const path = "go-with-elements.gsx"
	const source = "package p\n\nvar composed = Wrap(<span>{firstValue}</span>, <b>{secondValue}</b>)\n"
	fset := token.NewFileSet()
	file, err := gsxparser.ParseFile(fset, path, []byte(source), 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	bag := diag.NewBag(fset)
	if _, err := preprocessComponentCallSites(map[string]*gsxast.File{path: file}, nil, fset, attrclass.Builtin(), bag); err != nil {
		t.Fatalf("preprocess: %v", err)
	}
	build, err := buildMappedSkeleton(file, funcTables{}, fset, bag, nil, skeletonFull, path, []byte(source))
	if err != nil {
		t.Fatalf("buildMappedSkeleton: %v", err)
	}

	var declaration *gsxast.GoWithElements
	for _, decl := range file.Decls {
		if value, ok := decl.(*gsxast.GoWithElements); ok {
			declaration = value
			break
		}
	}
	if declaration == nil {
		t.Fatal("parsed file has no GoWithElements declaration")
	}
	for _, part := range declaration.Parts {
		text, ok := part.(gsxast.GoText)
		if !ok || text.Src == "" {
			continue
		}
		start := fset.File(text.Pos()).Offset(text.Pos())
		assertGeneratedSpanMaps(t, build, text.Src, sourceintel.Span{Path: path, Start: start, End: start + len(text.Src)}, sourceintel.Definition|sourceintel.Hover|sourceintel.Completion|sourceintel.Symbol)
	}

	generatedStart := strings.Index(build.source, "/*line "+path)
	if generatedStart < 0 {
		t.Fatal("skeleton has no GoWithElements line anchor")
	}
	tokenFile := fset.File(declaration.Pos())
	wantDeclaration := sourceintel.Span{Path: path, Start: tokenFile.Offset(declaration.Pos()), End: tokenFile.Offset(declaration.End())}
	if got, ok := build.sourceMap.DeclarationSpan(generatedStart, len(build.source)); !ok || got != wantDeclaration {
		t.Fatalf("DeclarationSpan = %+v, %v; want %+v", got, ok, wantDeclaration)
	}
}

func TestBuildMappedSkeletonRepeatedProbeHasOneReadableCopy(t *testing.T) {
	const path = "repeated-probe.gsx"
	const source = "package p\n\ncomponent Page(repeatedAttrs any) { <div { repeatedAttrs... }/> }\n"
	fset := token.NewFileSet()
	file, err := gsxparser.ParseFile(fset, path, []byte(source), 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	bag := diag.NewBag(fset)
	if _, err := preprocessComponentCallSites(map[string]*gsxast.File{path: file}, map[string]bool{"Page": true}, fset, attrclass.Builtin(), bag); err != nil {
		t.Fatalf("preprocess: %v", err)
	}
	build, err := buildMappedSkeleton(file, funcTables{}, fset, bag, nil, skeletonFull, path, []byte(source))
	if err != nil {
		t.Fatalf("buildMappedSkeleton: %v", err)
	}

	sourceStart := strings.LastIndex(source, "repeatedAttrs")
	want := sourceintel.Span{Path: path, Start: sourceStart, End: sourceStart + len("repeatedAttrs")}
	readable := 0
	for generatedStart := 0; generatedStart < len(build.source); {
		relative := strings.Index(build.source[generatedStart:], "repeatedAttrs")
		if relative < 0 {
			break
		}
		start := generatedStart + relative
		if got, ok := build.sourceMap.SourceSpan(start, start+len("repeatedAttrs"), sourceintel.Definition|sourceintel.Hover); ok && got == want {
			readable++
		}
		generatedStart = start + 1
	}
	if readable != 1 {
		t.Fatalf("readable copies = %d, want exactly one\nskeleton:\n%s", readable, build.source)
	}
}

func assertGeneratedSpanMaps(t *testing.T, build skeletonBuild, generatedText string, want sourceintel.Span, capabilities sourceintel.Capability) {
	t.Helper()
	for generatedStart := 0; generatedStart < len(build.source); {
		relative := strings.Index(build.source[generatedStart:], generatedText)
		if relative < 0 {
			break
		}
		start := generatedStart + relative
		if got, ok := build.sourceMap.SourceSpan(start, start+len(generatedText), capabilities); ok && got == want {
			return
		}
		generatedStart = start + 1
	}
	t.Fatalf("no generated %q maps to %+v with capabilities %d\nskeleton:\n%s", generatedText, want, capabilities, build.source)
}
