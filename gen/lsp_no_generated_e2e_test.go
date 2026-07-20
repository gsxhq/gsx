package gen

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gsxhq/gsx/internal/lsp"
)

func TestLSPStructuredAnswersIgnorePhysicalGeneratedFile(t *testing.T) {
	if testing.Short() {
		t.Skip("real module analysis")
	}
	root := t.TempDir()
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	writeLSPIndependenceFile(t, root, "go.mod", "module example.com/independent\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	const widgets = `package widgets

type Label string
type Labelish interface{ ~string }

component Box[T Labelish](value T) {
	<strong>{value}</strong>
}
`
	const widgetTypes = `package widgets

type ExternalLabel string
`
	const page = `package page

import widgets "example.com/independent/widgets"

func helper(input widgets.Label) widgets.Label { return input }

func mixed(input widgets.Label) widgets.Label {
	local := helper(input)
	node := <span>{local}</span>
	_ = node
	return local
}

component Card[T widgets.Labelish](value T) {
	<em>{value}</em>
}

component Page(label widgets.Label) {
	<Card[widgets.Label] value={mixed(label)}/>
	<widgets.Box[widgets.ExternalLabel] value={widgets.ExternalLabel(label)}/>
}
`
	widgetsPath := writeLSPIndependenceFile(t, root, "widgets/widgets.gsx", widgets)
	widgetTypesPath := writeLSPIndependenceFile(t, root, "widgets/types.go", widgetTypes)
	pagePath := writeLSPIndependenceFile(t, root, "page/page.gsx", page)
	generatedPaths := []string{
		strings.TrimSuffix(widgetsPath, ".gsx") + ".x.go",
		strings.TrimSuffix(pagePath, ".gsx") + ".x.go",
	}

	type positionProbe struct {
		name           string
		source         string
		path           string
		offset         int
		targetSource   string
		targetPath     string
		targetName     string
		targetPoint    bool
		hoverSubstring string
		hoverRange     string
	}
	probes := []positionProbe{
		{name: "GoWithElements local", source: page, path: pagePath, offset: strings.Index(page, "return local") + len("return "), targetSource: page, targetPath: pagePath, targetName: "local", hoverSubstring: "var local widgets.Label"},
		{name: "raw Go helper", source: page, path: pagePath, offset: strings.Index(page, "helper(input)"), targetSource: page, targetPath: pagePath, targetName: "helper", hoverSubstring: "func helper(input widgets.Label) widgets.Label"},
		{name: "component declaration", source: page, path: pagePath, offset: strings.Index(page, "<Card[") + 1, targetSource: page, targetPath: pagePath, targetName: "Card", hoverSubstring: "component Card[T widgets.Labelish](value T)"},
		{name: "component parameter", source: page, path: pagePath, offset: strings.Index(page, "{value}") + 1, targetSource: page, targetPath: pagePath, targetName: "value", hoverSubstring: "var value T"},
		{name: "explicit generic type argument", source: page, path: pagePath, offset: strings.Index(page, "Box[widgets.ExternalLabel]") + len("Box[widgets."), targetSource: widgetTypes, targetPath: widgetTypesPath, targetName: "ExternalLabel", targetPoint: true, hoverSubstring: "type widgets.ExternalLabel string"},
		{name: "cross-package component", source: page, path: pagePath, offset: strings.Index(page, "widgets.Box") + len("widgets."), targetSource: widgets, targetPath: widgetsPath, targetName: "Box", hoverSubstring: "component Box[T Labelish](value T)", hoverRange: "widgets.Box"},
	}

	run := func(t *testing.T) map[int]json.RawMessage {
		t.Helper()
		pageURI := lspTestPathURI(pagePath)
		frames := []string{frameMsg(t, map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "initialize",
			"params": map[string]any{"rootUri": lspTestPathURI(root), "capabilities": map[string]any{}},
		}), frameMsg(t, map[string]any{
			"jsonrpc": "2.0", "method": "textDocument/didOpen",
			"params": map[string]any{"textDocument": map[string]any{"uri": pageURI, "version": 1, "text": page}},
		}), frameMsg(t, map[string]any{
			"jsonrpc": "2.0", "method": "textDocument/didOpen",
			"params": map[string]any{"textDocument": map[string]any{"uri": lspTestPathURI(widgetsPath), "version": 1, "text": widgets}},
		})}
		for probeIndex, probe := range probes {
			frames = append(frames,
				lspPositionRequestFrame(t, 2+probeIndex, "textDocument/definition", lspTestPathURI(probe.path), probe.source, probe.offset),
				lspPositionRequestFrame(t, 2+len(probes)+probeIndex, "textDocument/hover", lspTestPathURI(probe.path), probe.source, probe.offset),
			)
		}
		pageSymbolsID := 2 + 2*len(probes)
		widgetsSymbolsID := pageSymbolsID + 1
		workspaceSymbolsID := widgetsSymbolsID + 1
		workspaceSymbolsRepeatID := workspaceSymbolsID + 1
		frames = append(frames, frameMsg(t, map[string]any{
			"jsonrpc": "2.0", "id": pageSymbolsID, "method": "textDocument/documentSymbol",
			"params": map[string]any{"textDocument": map[string]any{"uri": pageURI}},
		}), frameMsg(t, map[string]any{
			"jsonrpc": "2.0", "id": widgetsSymbolsID, "method": "textDocument/documentSymbol",
			"params": map[string]any{"textDocument": map[string]any{"uri": lspTestPathURI(widgetsPath)}},
		}))
		for _, id := range []int{workspaceSymbolsID, workspaceSymbolsRepeatID} {
			frames = append(frames, frameMsg(t, map[string]any{
				"jsonrpc": "2.0", "id": id, "method": "workspace/symbol",
				"params": map[string]any{"query": ""},
			}))
		}
		frames = append(frames, frameMsg(t, map[string]any{"jsonrpc": "2.0", "method": "exit"}))

		var output, stderr bytes.Buffer
		if code := runLSP(strings.NewReader(strings.Join(frames, "")), &output, &stderr, config{}, nil); code != 0 {
			t.Fatalf("runLSP=%d stderr=%s", code, stderr.String())
		}
		results := make(map[int]json.RawMessage, workspaceSymbolsRepeatID-1)
		for id := 2; id <= workspaceSymbolsRepeatID; id++ {
			response := lspTestResponse(t, output.String(), id)
			if len(response.Error) != 0 || len(response.Result) == 0 || bytes.Equal(response.Result, []byte("null")) || bytes.Equal(response.Result, []byte("[]")) {
				t.Fatalf("structured response %d is empty: result=%s error=%s\nall output:\n%s", id, response.Result, response.Error, output.String())
			}
			results[id] = bytes.Clone(response.Result)
		}
		for probeIndex, probe := range probes {
			assertExactDefinitionResult(t, results[2+probeIndex], probe.targetPath, probe.targetSource, probe.targetName, probe.targetPoint)
			assertHoverResult(t, results[2+len(probes)+probeIndex], probe.hoverSubstring, probe.hoverRange, probe.source, probe.offset)
		}
		assertSyntheticDocumentSymbols(t, results[pageSymbolsID], pagePath, page, []syntheticSymbolExpectation{
			{name: "helper", kind: 12, marker: "func helper"},
			{name: "mixed", kind: 12, marker: "func mixed"},
			{name: "Card", kind: 12, marker: "component Card"},
			{name: "Page", kind: 12, marker: "component Page"},
		})
		assertSyntheticDocumentSymbols(t, results[widgetsSymbolsID], widgetsPath, widgets, []syntheticSymbolExpectation{
			{name: "Label", kind: 5, marker: "type Label string"},
			{name: "Labelish", kind: 11, marker: "type Labelish interface"},
			{name: "Box", kind: 12, marker: "component Box"},
		})
		var workspaceSymbols []lsp.SymbolInformation
		if err := json.Unmarshal(results[workspaceSymbolsID], &workspaceSymbols); err != nil {
			t.Fatalf("decode workspace symbols: %v", err)
		}
		wantWorkspace := []struct {
			name, container, path, source string
			kind                          int
		}{
			{"Box", "widgets", widgetsPath, widgets, 12},
			{"Card", "page", pagePath, page, 12},
			{"Label", "widgets", widgetsPath, widgets, 5},
			{"Labelish", "widgets", widgetsPath, widgets, 11},
			{"Page", "page", pagePath, page, 12},
			{"helper", "page", pagePath, page, 12},
			{"mixed", "page", pagePath, page, 12},
		}
		if len(workspaceSymbols) != len(wantWorkspace) {
			t.Fatalf("workspace symbols = %+v, want %d exact entries", workspaceSymbols, len(wantWorkspace))
		}
		for index, wantSymbol := range wantWorkspace {
			nameOffset := declarationNameOffset(t, wantSymbol.source, wantSymbol.name)
			wantLocation := lsp.Location{URI: lspTestPathURI(wantSymbol.path), Range: sourceRange(wantSymbol.source, nameOffset, nameOffset+len(wantSymbol.name))}
			gotSymbol := workspaceSymbols[index]
			if gotSymbol.Name != wantSymbol.name || gotSymbol.Kind != wantSymbol.kind || gotSymbol.ContainerName != wantSymbol.container || gotSymbol.Location != wantLocation {
				t.Errorf("workspace symbol[%d] = %+v, want name=%q kind=%d container=%q location=%+v", index, gotSymbol, wantSymbol.name, wantSymbol.kind, wantSymbol.container, wantLocation)
			}
		}
		if !bytes.Equal(results[workspaceSymbolsID], results[workspaceSymbolsRepeatID]) {
			t.Fatalf("workspace symbol ordering changed within one server:\nfirst=%s\nsecond=%s", results[workspaceSymbolsID], results[workspaceSymbolsRepeatID])
		}
		return results
	}

	for _, path := range generatedPaths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("generated output exists before absent run: %s", path)
		}
	}
	want := run(t)
	assertNoGeneratedGoFiles(t, root)

	poisons := []struct {
		name     string
		contents string
	}{
		{name: "invalid", contents: "not Go\x00\xff"},
		{name: "stale", contents: "package poison\n\nvar StaleOnly = 1\n"},
		{name: "conflicting", contents: "package page\n\nfunc helper() {}\nfunc Card() {}\n"},
	}
	for _, poison := range poisons {
		t.Run(poison.name, func(t *testing.T) {
			stamp := time.Unix(1_700_000_000, 456_000_000)
			before := make(map[string]fs.FileInfo, len(generatedPaths))
			beforeBytes := make(map[string][]byte, len(generatedPaths))
			for _, path := range generatedPaths {
				contents := poison.contents
				if strings.Contains(path, "widgets") && poison.name == "conflicting" {
					contents = "package widgets\n\ntype Label int\nfunc Box() {}\n"
				}
				if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(path, stamp, stamp); err != nil {
					t.Fatal(err)
				}
				before[path], err = os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				beforeBytes[path] = []byte(contents)
			}
			got := run(t)
			for id := 2; id < 2+2*len(probes)+4; id++ {
				if !bytes.Equal(got[id], want[id]) {
					t.Fatalf("response %d changed with %s generated output:\nabsent=%s\npoisoned=%s", id, poison.name, want[id], got[id])
				}
			}
			for _, path := range generatedPaths {
				contents, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(contents, beforeBytes[path]) || !info.ModTime().Equal(before[path].ModTime()) {
					t.Fatalf("poison output changed: %s size=%d modtime=%v, want size=%d modtime=%v", path, len(contents), info.ModTime(), before[path].Size(), before[path].ModTime())
				}
			}
			for _, path := range generatedPaths {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestLSPDefinitionAllowsUnpairedAuthoredXGo(t *testing.T) {
	if testing.Short() {
		t.Skip("real module analysis")
	}
	root := t.TempDir()
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	writeLSPIndependenceFile(t, root, "go.mod", "module example.com/xgo\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	const page = "package page\n\ncomponent Page() { <p>{Handwritten()} {Ordinary()}</p> }\n"
	const handwritten = "package page\n\nfunc Handwritten() string { return \"x.go is authored here\" }\n"
	const ordinary = "package page\n\nfunc Ordinary() string { return \"ordinary Go\" }\n"
	const pairedPoison = "package page\n\nfunc Handwritten() int { return 0 }\n"
	pagePath := writeLSPIndependenceFile(t, root, "page/page.gsx", page)
	handwrittenPath := writeLSPIndependenceFile(t, root, "page/hand.x.go", handwritten)
	ordinaryPath := writeLSPIndependenceFile(t, root, "page/ordinary.go", ordinary)
	pairedPoisonPath := writeLSPIndependenceFile(t, root, "page/page.x.go", pairedPoison)
	pageURI := lspTestPathURI(pagePath)

	frames := []string{
		frameMsg(t, map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "initialize",
			"params": map[string]any{"rootUri": lspTestPathURI(root), "capabilities": map[string]any{}},
		}),
		frameMsg(t, map[string]any{
			"jsonrpc": "2.0", "method": "textDocument/didOpen",
			"params": map[string]any{"textDocument": map[string]any{"uri": pageURI, "version": 1, "text": page}},
		}),
		lspPositionRequestFrame(t, 2, "textDocument/definition", pageURI, page, strings.Index(page, "Handwritten")),
		lspPositionRequestFrame(t, 3, "textDocument/definition", pageURI, page, strings.Index(page, "Ordinary")),
		frameMsg(t, map[string]any{"jsonrpc": "2.0", "method": "exit"}),
	}
	var output, stderr bytes.Buffer
	if code := runLSP(strings.NewReader(strings.Join(frames, "")), &output, &stderr, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, stderr.String())
	}
	assertDefinition := func(id int, targetPath, targetSource, targetName string) {
		t.Helper()
		locations := definitionLocationList(t, output.String(), id)
		if len(locations) != 1 {
			t.Fatalf("definition %d = %+v, want one exact target; output:\n%s", id, locations, output.String())
		}
		start := strings.Index(targetSource, targetName)
		want := lsp.Location{
			URI: lspTestPathURI(targetPath),
			Range: lsp.Range{
				Start: lspUTF16PositionAt(targetSource, start),
				End:   lspUTF16PositionAt(targetSource, start+len(targetName)),
			},
		}
		if locations[0] != want {
			t.Fatalf("definition %d = %+v, want %+v", id, locations[0], want)
		}
	}
	assertDefinition(2, handwrittenPath, handwritten, "Handwritten")
	assertDefinition(3, ordinaryPath, ordinary, "Ordinary")
	poison, err := os.ReadFile(pairedPoisonPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(poison, []byte(pairedPoison)) {
		t.Fatalf("paired poison mutated: %q", poison)
	}
}

type syntheticSymbolExpectation struct {
	name   string
	kind   int
	marker string
}

func assertExactDefinitionResult(t *testing.T, raw json.RawMessage, targetPath, targetSource, targetName string, pointRange bool) {
	t.Helper()
	var locations []lsp.Location
	if len(raw) > 0 && raw[0] == '{' {
		var location lsp.Location
		if err := json.Unmarshal(raw, &location); err != nil {
			t.Fatalf("decode definition result: %v: %s", err, raw)
		}
		locations = []lsp.Location{location}
	} else if err := json.Unmarshal(raw, &locations); err != nil {
		t.Fatalf("decode definition result: %v: %s", err, raw)
	}
	if len(locations) != 1 {
		t.Fatalf("definition = %+v, want one target", locations)
	}
	start := declarationNameOffset(t, targetSource, targetName)
	end := start + len(targetName)
	if pointRange {
		end = start
	}
	want := lsp.Location{URI: lspTestPathURI(targetPath), Range: sourceRange(targetSource, start, end)}
	if locations[0] != want {
		t.Fatalf("definition = %+v, want %+v", locations[0], want)
	}
}

func assertHoverResult(t *testing.T, raw json.RawMessage, wantSubstring, rangeNeedle, source string, offset int) {
	t.Helper()
	var hover lsp.Hover
	if err := json.Unmarshal(raw, &hover); err != nil {
		t.Fatalf("decode hover result: %v: %s", err, raw)
	}
	if !strings.Contains(hover.Contents.Value, wantSubstring) {
		t.Fatalf("hover = %q, want substring %q", hover.Contents.Value, wantSubstring)
	}
	start, end := identifierSpanAt(t, source, offset)
	if rangeNeedle != "" {
		start = -1
		for candidate := 0; ; {
			relative := strings.Index(source[candidate:], rangeNeedle)
			if relative < 0 {
				break
			}
			candidate += relative
			if candidate <= offset && offset < candidate+len(rangeNeedle) {
				start = candidate
				break
			}
			candidate++
		}
		if start < 0 {
			t.Fatalf("hover range needle %q does not cover offset %d", rangeNeedle, offset)
		}
		end = start + len(rangeNeedle)
	}
	wantRange := sourceRange(source, start, end)
	if hover.Range == nil || *hover.Range != wantRange {
		t.Fatalf("hover range = %+v, want exact queried identifier range %+v", hover.Range, wantRange)
	}
}

func assertSyntheticDocumentSymbols(t *testing.T, raw json.RawMessage, path, source string, want []syntheticSymbolExpectation) {
	t.Helper()
	var symbols []lsp.DocumentSymbol
	if err := json.Unmarshal(raw, &symbols); err != nil {
		t.Fatalf("decode document symbols: %v: %s", err, raw)
	}
	if len(symbols) != len(want) {
		t.Fatalf("document symbols for %s = %+v, want %d entries", path, symbols, len(want))
	}
	for index, expectation := range want {
		markerOffset := strings.Index(source, expectation.marker)
		if markerOffset < 0 {
			t.Fatalf("source missing declaration marker %q", expectation.marker)
		}
		nameRelative := strings.Index(expectation.marker, expectation.name)
		if nameRelative < 0 {
			t.Fatalf("marker %q does not contain name %q", expectation.marker, expectation.name)
		}
		nameOffset := markerOffset + nameRelative
		declStart, declEnd := declarationSpanAt(t, source, markerOffset)
		wantSelection := sourceRange(source, nameOffset, nameOffset+len(expectation.name))
		wantRange := sourceRange(source, declStart, declEnd)
		got := symbols[index]
		if got.Name != expectation.name || got.Kind != expectation.kind || got.SelectionRange != wantSelection || got.Range != wantRange {
			t.Errorf("document symbol[%d] = %+v, want name=%q kind=%d selection=%+v range=%+v", index, got, expectation.name, expectation.kind, wantSelection, wantRange)
		}
	}
}

func declarationNameOffset(t *testing.T, source, name string) int {
	t.Helper()
	for _, prefix := range []string{"component ", "func ", "type ", "var ", "const "} {
		if offset := strings.Index(source, prefix+name); offset >= 0 {
			return offset + len(prefix)
		}
	}
	if offset := strings.Index(source, name+" :="); offset >= 0 {
		return offset
	}
	if offset := strings.Index(source, name+" T"); offset >= 0 {
		return offset
	}
	t.Fatalf("source has no declaration of %q", name)
	return -1
}

func declarationSpanAt(t *testing.T, source string, start int) (int, int) {
	t.Helper()
	open := strings.IndexByte(source[start:], '{')
	lineEnd := strings.IndexByte(source[start:], '\n')
	if open < 0 || (lineEnd >= 0 && lineEnd < open) {
		if lineEnd < 0 {
			return start, len(source)
		}
		return start, start + lineEnd
	}
	depth := 0
	for offset := start + open; offset < len(source); offset++ {
		switch source[offset] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return start, offset + 1
			}
		}
	}
	t.Fatalf("unterminated declaration at offset %d", start)
	return 0, 0
}

func identifierSpanAt(t *testing.T, source string, offset int) (int, int) {
	t.Helper()
	isIdent := func(b byte) bool {
		return b == '_' || b >= '0' && b <= '9' || b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z'
	}
	if offset < 0 || offset >= len(source) || !isIdent(source[offset]) {
		t.Fatalf("offset %d is not on an identifier", offset)
	}
	start, end := offset, offset+1
	for start > 0 && isIdent(source[start-1]) {
		start--
	}
	for end < len(source) && isIdent(source[end]) {
		end++
	}
	return start, end
}

func sourceRange(source string, start, end int) lsp.Range {
	return lsp.Range{Start: lspUTF16PositionAt(source, start), End: lspUTF16PositionAt(source, end)}
}

func assertNoGeneratedGoFiles(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".x.go") {
			t.Errorf("unexpected generated output after absent run: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func lspPositionRequestFrame(t *testing.T, id int, method, uri, source string, offset int) string {
	t.Helper()
	position := lspUTF16PositionAt(source, offset)
	return frameMsg(t, map[string]any{
		"jsonrpc": "2.0", "id": id, "method": method,
		"params": map[string]any{
			"textDocument": map[string]any{"uri": uri},
			"position":     map[string]any{"line": position.Line, "character": position.Character},
		},
	})
}

func writeLSPIndependenceFile(t *testing.T, root, name, contents string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLSPManualOneLearning(t *testing.T) {
	fixture := os.Getenv("GSX_LSP_FIXTURE")
	if fixture == "" {
		t.Skip("set GSX_LSP_FIXTURE to a real gsx consumer")
	}
	fixture, err := filepath.Abs(fixture)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := exec.Command("git", "-C", fixture, "rev-parse", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("fixture commit: %v: %s", err, commit)
	}
	t.Logf("fixture commit: %s", strings.TrimSpace(string(commit)))

	destination := filepath.Join(t.TempDir(), "fixture")
	if err := copyTreeWithoutGeneratedGo(fixture, destination); err != nil {
		t.Fatal(err)
	}
	assertNoGeneratedGoFiles(t, destination)
	for _, relative := range []string{"email/templates.gsx", "ds/badge/badge.gsx", "ui/pacm_edit.gsx", "ui/dashboard_npt.gsx"} {
		if _, err := os.Stat(filepath.Join(destination, filepath.FromSlash(relative))); err != nil {
			t.Fatalf("fixture does not contain %s: %v", relative, err)
		}
	}
	// The exact production-server queries live in runOneLearningProbe. Keeping the
	// absent and poisoned analyzers fresh proves neither warm cache can hide a
	// physical generated-file dependency.
	want := runOneLearningProbe(t, destination)
	poisonPaths := []string{
		"email/templates.x.go",
		"ds/badge/badge.x.go",
		"ui/pacm_edit.x.go",
		"ui/dashboard_npt.x.go",
	}
	const poison = "package poison\n\nvar ConflictingGeneratedOutput = doesNotCompile\n"
	stamp := time.Unix(1_700_000_000, 789_000_000)
	poisonBytes := make(map[string][]byte, len(poisonPaths))
	for _, relative := range poisonPaths {
		path := filepath.Join(destination, filepath.FromSlash(relative))
		if err := os.WriteFile(path, []byte(poison), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		poisonBytes[path] = []byte(poison)
	}
	got := runOneLearningProbe(t, destination)
	if len(want) != len(got) {
		t.Fatalf("one-learning response count changed after poisoning: absent=%d poisoned=%d", len(want), len(got))
	}
	for id, absent := range want {
		if !bytes.Equal(absent, got[id]) {
			t.Fatalf("one-learning response %d changed after poisoning generated outputs:\nabsent=%s\npoisoned=%s", id, absent, got[id])
		}
	}
	for _, relative := range poisonPaths {
		path := filepath.Join(destination, filepath.FromSlash(relative))
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(contents, poisonBytes[path]) || !info.ModTime().Equal(stamp) {
			t.Fatalf("poison output mutated: %s", path)
		}
	}
}

func copyTreeWithoutGeneratedGo(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if entry.IsDir() && (entry.Name() == ".git" || entry.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if strings.HasSuffix(entry.Name(), ".x.go") {
			return nil
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, contents, info.Mode().Perm())
	})
}

type oneLearningTarget struct {
	path           string
	needle         string
	delta          int
	targetName     string
	targetMarker   string
	externalImport string
	hoverSubstring string
}

func runOneLearningProbe(t *testing.T, root string) map[int]json.RawMessage {
	t.Helper()
	targets := []oneLearningTarget{
		{path: "email/templates.gsx", needle: "time.Duration", delta: len("time."), targetName: "Duration", externalImport: "time", hoverSubstring: "type time.Duration int64"},
		{path: "ds/badge/badge.gsx", needle: "component Badge", delta: len("component "), targetName: "Badge", targetMarker: "component Badge", hoverSubstring: "component Badge(variant Variant, children gsx.Node)"},
		{path: "ds/badge/badge.gsx", needle: "variant Variant", targetName: "variant", targetMarker: "variant Variant", hoverSubstring: "var variant"},
		{path: "ui/pacm_edit.gsx", needle: "pgtype.Bool", delta: len("pgtype."), targetName: "Bool", externalImport: "github.com/jackc/pgx/v5/pgtype", hoverSubstring: "type pgtype.Bool struct"},
		{path: "ui/dashboard_npt.gsx", needle: "duration.Hours", delta: len("duration."), targetName: "Hours", externalImport: "time", hoverSubstring: "func (time.Duration).Hours() float64"},
	}
	sources := make(map[string]string)
	for _, target := range targets {
		if _, ok := sources[target.path]; ok {
			continue
		}
		contents, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(target.path)))
		if err != nil {
			t.Fatal(err)
		}
		sources[target.path] = string(contents)
	}
	frames := []string{frameMsg(t, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"rootUri": lspTestPathURI(root), "capabilities": map[string]any{}},
	})}
	opened := make(map[string]bool)
	for _, target := range targets {
		if opened[target.path] {
			continue
		}
		opened[target.path] = true
		path := filepath.Join(root, filepath.FromSlash(target.path))
		frames = append(frames, frameMsg(t, map[string]any{
			"jsonrpc": "2.0", "method": "textDocument/didOpen",
			"params": map[string]any{"textDocument": map[string]any{"uri": lspTestPathURI(path), "version": 1, "text": sources[target.path]}},
		}))
	}
	nextID := 2
	var resultIDs []int
	for _, target := range targets {
		source := sources[target.path]
		offset := strings.Index(source, target.needle)
		if offset < 0 {
			t.Fatalf("%s missing %q", target.path, target.needle)
		}
		offset += target.delta
		uri := lspTestPathURI(filepath.Join(root, filepath.FromSlash(target.path)))
		for _, method := range []string{"textDocument/definition", "textDocument/hover"} {
			frames = append(frames, lspPositionRequestFrame(t, nextID, method, uri, source, offset))
			resultIDs = append(resultIDs, nextID)
			nextID++
		}
	}
	for _, relative := range []string{"email/templates.gsx", "ds/badge/badge.gsx", "ui/pacm_edit.gsx", "ui/dashboard_npt.gsx"} {
		frames = append(frames, frameMsg(t, map[string]any{
			"jsonrpc": "2.0", "id": nextID, "method": "textDocument/documentSymbol",
			"params": map[string]any{"textDocument": map[string]any{"uri": lspTestPathURI(filepath.Join(root, filepath.FromSlash(relative)))}},
		}))
		resultIDs = append(resultIDs, nextID)
		nextID++
	}
	workspaceIDs := []int{nextID, nextID + 1}
	for _, id := range workspaceIDs {
		frames = append(frames, frameMsg(t, map[string]any{
			"jsonrpc": "2.0", "id": id, "method": "workspace/symbol",
			"params": map[string]any{"query": "Badge"},
		}))
		resultIDs = append(resultIDs, id)
	}
	frames = append(frames, frameMsg(t, map[string]any{"jsonrpc": "2.0", "method": "exit"}))

	var output, stderr bytes.Buffer
	if code := runLSP(strings.NewReader(strings.Join(frames, "")), &output, &stderr, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, stderr.String())
	}
	results := make(map[int]json.RawMessage, len(resultIDs))
	for _, id := range resultIDs {
		response := lspTestResponse(t, output.String(), id)
		if len(response.Error) != 0 || len(response.Result) == 0 || bytes.Equal(response.Result, []byte("null")) || bytes.Equal(response.Result, []byte("[]")) {
			t.Fatalf("one-learning response %d is empty: result=%s error=%s\nstderr=%s", id, response.Result, response.Error, stderr.String())
		}
		results[id] = bytes.Clone(response.Result)
	}
	assertOneLearningProbeResults(t, root, sources, targets, results, workspaceIDs)
	return results
}

func assertOneLearningProbeResults(t *testing.T, root string, sources map[string]string, targets []oneLearningTarget, results map[int]json.RawMessage, workspaceIDs []int) {
	t.Helper()
	for index, target := range targets {
		source := sources[target.path]
		queryOffset := strings.Index(source, target.needle) + target.delta
		if target.externalImport != "" {
			definition := findGoDefinition(t, root, target.externalImport, target.targetName)
			assertExactLocationResult(t, results[2+2*index], definition.path, definition.source, definition.offset, definition.offset)
		} else {
			targetOffset := strings.Index(source, target.targetMarker) + strings.Index(target.targetMarker, target.targetName)
			if targetOffset < 0 {
				t.Fatalf("%s missing target marker %q", target.path, target.targetMarker)
			}
			assertExactLocationResult(t, results[2+2*index], filepath.Join(root, filepath.FromSlash(target.path)), source, targetOffset, targetOffset+len(target.targetName))
		}
		assertHoverResult(t, results[3+2*index], target.hoverSubstring, "", source, queryOffset)
	}

	documentExpectations := []struct {
		id, kind     int
		path         string
		name, marker string
	}{
		{12, 12, "email/templates.gsx", "formatExpiryDuration", "func formatExpiryDuration"},
		{13, 12, "ds/badge/badge.gsx", "Badge", "component Badge"},
		{14, 23, "ui/pacm_edit.gsx", "PacmEditPage", "type PacmEditPage struct"},
		{15, 12, "ui/dashboard_npt.gsx", "formatSyncTime", "func formatSyncTime"},
	}
	for _, expectation := range documentExpectations {
		assertContainsDocumentSymbol(t, results[expectation.id], sources[expectation.path], expectation.name, expectation.marker, expectation.kind)
	}

	if len(workspaceIDs) != 2 || !bytes.Equal(results[workspaceIDs[0]], results[workspaceIDs[1]]) {
		t.Fatalf("one-learning workspace symbol ordering is not deterministic:\nfirst=%s\nsecond=%s", results[workspaceIDs[0]], results[workspaceIDs[1]])
	}
	var workspace []lsp.SymbolInformation
	if err := json.Unmarshal(results[workspaceIDs[0]], &workspace); err != nil {
		t.Fatalf("decode one-learning workspace symbols: %v", err)
	}
	badgeSource := sources["ds/badge/badge.gsx"]
	badgeOffset := strings.Index(badgeSource, "component Badge") + len("component ")
	wantBadge := lsp.SymbolInformation{
		Name:          "Badge",
		Kind:          12,
		ContainerName: "badge",
		Location:      lsp.Location{URI: lspTestPathURI(filepath.Join(root, "ds/badge/badge.gsx")), Range: sourceRange(badgeSource, badgeOffset, badgeOffset+len("Badge"))},
	}
	found := false
	for _, symbol := range workspace {
		if symbol.Name == "Badge" && symbol.Location.URI == wantBadge.Location.URI {
			found = true
			if symbol != wantBadge {
				t.Errorf("one-learning Badge workspace symbol = %+v, want %+v", symbol, wantBadge)
			}
		}
	}
	if !found {
		t.Fatalf("one-learning workspace symbols have no exact Badge declaration: %+v", workspace)
	}
}

func assertExactLocationResult(t *testing.T, raw json.RawMessage, targetPath, targetSource string, start, end int) {
	t.Helper()
	var locations []lsp.Location
	if len(raw) > 0 && raw[0] == '{' {
		var location lsp.Location
		if err := json.Unmarshal(raw, &location); err != nil {
			t.Fatalf("decode definition result: %v: %s", err, raw)
		}
		locations = []lsp.Location{location}
	} else if err := json.Unmarshal(raw, &locations); err != nil {
		t.Fatalf("decode definition result: %v: %s", err, raw)
	}
	if len(locations) != 1 {
		t.Fatalf("definition = %+v, want one target", locations)
	}
	want := lsp.Location{URI: lspTestPathURI(targetPath), Range: sourceRange(targetSource, start, end)}
	if locations[0] != want {
		t.Fatalf("definition = %+v, want %+v", locations[0], want)
	}
}

func assertContainsDocumentSymbol(t *testing.T, raw json.RawMessage, source, name, marker string, kind int) {
	t.Helper()
	var symbols []lsp.DocumentSymbol
	if err := json.Unmarshal(raw, &symbols); err != nil {
		t.Fatalf("decode document symbols: %v", err)
	}
	markerOffset := strings.Index(source, marker)
	nameOffset := markerOffset + strings.Index(marker, name)
	declStart, declEnd := declarationSpanAt(t, source, markerOffset)
	wantSelection := sourceRange(source, nameOffset, nameOffset+len(name))
	wantRange := sourceRange(source, declStart, declEnd)
	for _, symbol := range symbols {
		if symbol.Name != name {
			continue
		}
		if symbol.Kind != kind || symbol.SelectionRange != wantSelection || symbol.Range != wantRange {
			t.Fatalf("document symbol %q = %+v, want kind=%d selection=%+v range=%+v", name, symbol, kind, wantSelection, wantRange)
		}
		return
	}
	t.Fatalf("document symbols have no %q: %+v", name, symbols)
}

type goDefinition struct {
	path   string
	source string
	offset int
}

func findGoDefinition(t *testing.T, root, importPath, name string) goDefinition {
	t.Helper()
	command := exec.Command("go", "list", "-json", importPath)
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("go list %s: %v: %s", importPath, err, output)
	}
	var listed struct {
		Dir      string
		GoFiles  []string
		CgoFiles []string
	}
	if err := json.Unmarshal(output, &listed); err != nil {
		t.Fatalf("decode go list %s: %v", importPath, err)
	}
	fset := token.NewFileSet()
	var definitions []goDefinition
	for _, filename := range append(append([]string(nil), listed.GoFiles...), listed.CgoFiles...) {
		path := filepath.Join(listed.Dir, filename)
		file, parseErr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			t.Fatalf("parse Go file %s: %v", path, parseErr)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			var ident *ast.Ident
			switch declaration := node.(type) {
			case *ast.TypeSpec:
				ident = declaration.Name
			case *ast.FuncDecl:
				ident = declaration.Name
			}
			if ident == nil || ident.Name != name {
				return true
			}
			position := fset.Position(ident.Pos())
			source, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("read Go definition %s: %v", path, readErr)
			}
			definitions = append(definitions, goDefinition{path: path, source: string(source), offset: position.Offset})
			return false
		})
	}
	if len(definitions) != 1 {
		t.Fatalf("Go package %s has %d top-level definitions named %s, want one", importPath, len(definitions), name)
	}
	return definitions[0]
}
