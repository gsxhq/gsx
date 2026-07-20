package gen

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/lsp"
)

func TestDefinitionAuthoredSourceIndexE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := t.TempDir()
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) string {
		t.Helper()
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	write("go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	const dependency = `package widgets

type Label string
type Labelish interface{ ~string }
`
	dependencyPath := write("widgets/types.go", dependency)
	write("widgets/widgets.gsx", `package widgets

component Box[T Labelish](value T) {
	<span>{value}</span>
}
`)
	const source = `package page

import widgets "example.com/app/widgets"

type Page struct{}
type Alias string

func chunk(input Alias) Alias {
	cleaned := input
	return cleaned
}

func nested(input Alias) Alias {
	local := input
	node := <span>{local}</span>
	_ = node
	return local
}

component Card[T widgets.Labelish](value T) {
	<strong>{value}</strong>
}

component (p *Page) Render(label Alias) {
	<Card[Alias] value={label}/>
	<widgets.Box[widgets.Label] value={widgets.Label(label)}/>
	<p>{chunk(label)} {nested(label)} {p != nil}</p>
}
`
	pagePath := write("page/page.gsx", source)
	uri := "file://" + pagePath

	type request struct {
		id         int
		name       string
		cursor     int
		wantURI    string
		wantStart  int
		wantLength int
		wantSource string
	}
	requests := []request{
		{
			id: 2, name: "component declaration", cursor: strings.Index(source, "Card"),
			wantURI: uri, wantStart: strings.Index(source, "Card"), wantLength: len("Card"), wantSource: source,
		},
		{
			id: 3, name: "signature receiver declaration", cursor: strings.Index(source, "p *Page"),
			wantURI: uri, wantStart: strings.Index(source, "p *Page"), wantLength: len("p"), wantSource: source,
		},
		{
			id: 4, name: "top-level GoChunk local", cursor: strings.Index(source, "return cleaned") + len("return "),
			wantURI: uri, wantStart: strings.Index(source, "cleaned"), wantLength: len("cleaned"), wantSource: source,
		},
		{
			id: 5, name: "GoWithElements local after markup", cursor: strings.Index(source, "return local") + len("return "),
			wantURI: uri, wantStart: strings.Index(source, "local :="), wantLength: len("local"), wantSource: source,
		},
		{
			id: 6, name: "same-package explicit type argument", cursor: strings.Index(source, "Card[Alias]") + len("Card["),
			wantURI: uri, wantStart: strings.Index(source, "type Alias") + len("type "), wantLength: len("Alias"), wantSource: source,
		},
		{
			id: 7, name: "cross-package explicit type argument", cursor: strings.Index(source, "Box[widgets.Label]") + len("Box[widgets."),
			wantURI: "file://" + dependencyPath, wantStart: strings.Index(dependency, "Label"), wantSource: dependency,
		},
		{
			id: 8, name: "component parameter declaration to self", cursor: strings.Index(source, "value T"),
			wantURI: uri, wantStart: strings.Index(source, "value T"), wantLength: len("value"), wantSource: source,
		},
		{
			id: 9, name: "component parameter use", cursor: strings.Index(source, "{value}") + 1,
			wantURI: uri, wantStart: strings.Index(source, "value T"), wantSource: source,
		},
	}

	frame := func(value any) string {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return "Content-Length: " + strconv.Itoa(len(data)) + "\r\n\r\n" + string(data)
	}
	input := frame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	input += frame(map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri, "version": 1, "text": source}},
	})
	for _, request := range requests {
		position := lspPositionAt(source, request.cursor)
		input += frame(map[string]any{
			"jsonrpc": "2.0", "id": request.id, "method": "textDocument/definition",
			"params": map[string]any{
				"textDocument": map[string]any{"uri": uri},
				"position":     map[string]any{"line": position.Line, "character": position.Character},
			},
		})
	}
	input += frame(map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var output, stderr bytes.Buffer
	if code := runLSP(strings.NewReader(input), &output, &stderr, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, stderr.String())
	}
	if strings.Contains(output.String(), ".x.go") {
		t.Fatalf("definition response exposed virtual generated Go:\n%s", output.String())
	}
	for _, request := range requests {
		t.Run(request.name, func(t *testing.T) {
			location := definitionResult(t, output.String(), request.id)
			if location == nil {
				t.Fatalf("definition returned null; output:\n%s", output.String())
			}
			wantStart := lspPositionAt(request.wantSource, request.wantStart)
			wantEnd := wantStart
			if request.wantLength > 0 {
				wantEnd = lspPositionAt(request.wantSource, request.wantStart+request.wantLength)
			}
			wantRange := lsp.Range{Start: wantStart, End: wantEnd}
			if location.URI != request.wantURI || location.Range != wantRange {
				t.Fatalf("definition = %+v, want URI %q range %+v", location, request.wantURI, wantRange)
			}
		})
	}

	for _, generated := range []string{
		filepath.Join(dir, "page", "page.x.go"),
		filepath.Join(dir, "widgets", "widgets.x.go"),
	} {
		if _, err := os.Stat(generated); !os.IsNotExist(err) {
			t.Fatalf("physical generated file exists: %s", generated)
		}
	}
}

func lspPositionAt(source string, offset int) lsp.Position {
	line := strings.Count(source[:offset], "\n")
	lineStart := strings.LastIndexByte(source[:offset], '\n') + 1
	return lsp.Position{Line: line, Character: offset - lineStart}
}
