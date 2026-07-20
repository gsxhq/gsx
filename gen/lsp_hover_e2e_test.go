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

func TestHoverAuthoredSourceIndexE2E(t *testing.T) {
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
	write("widgets/types.go", `package widgets

type Label string
type Labelish interface{ ~string }
`)
	write("widgets/widgets.gsx", `package widgets

component Box[T Labelish](value T) {
	<span>{value}</span>
}
`)
	const source = `package page

import (
	"time"
	widgets "example.com/app/widgets"
)

type Alias string

func duration(d time.Duration) float64 {
	result := d.Hours()
	return result
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

component Page() {
	<Card[Alias] value={nested("x")}/>
	<widgets.Box[widgets.Label] value={widgets.Label("x")}/>
}
`
	pagePath := write("page/page.gsx", source)
	uri := "file://" + pagePath

	type request struct {
		id            int
		name          string
		cursor        int
		wantFragments []string
	}
	requests := []request{
		{id: 2, name: "external signature type", cursor: strings.Index(source, "time.Duration") + len("time."), wantFragments: []string{"type time.Duration int64"}},
		{id: 3, name: "top-level local", cursor: strings.Index(source, "return result") + len("return "), wantFragments: []string{"var result float64"}},
		{id: 4, name: "method selection", cursor: strings.Index(source, "d.Hours") + len("d."), wantFragments: []string{"func (time.Duration).Hours() float64"}},
		{id: 5, name: "whole top-level call", cursor: strings.Index(source, "d.Hours()") + len("d.Hours"), wantFragments: []string{"float64"}},
		{id: 6, name: "component declaration", cursor: strings.Index(source, "Card"), wantFragments: []string{"func Card", "[T widgets.Labelish]", "value T"}},
		{id: 7, name: "component parameter declaration", cursor: strings.Index(source, "value T"), wantFragments: []string{"var value T"}},
		{id: 8, name: "component parameter use", cursor: strings.Index(source, "{value}") + 1, wantFragments: []string{"var value T"}},
		{id: 9, name: "same-package type argument", cursor: strings.Index(source, "Card[Alias]") + len("Card["), wantFragments: []string{"type Alias string"}},
		{id: 10, name: "cross-package type argument", cursor: strings.Index(source, "Box[widgets.Label]") + len("Box[widgets."), wantFragments: []string{"type widgets.Label string"}},
		{id: 11, name: "GoWithElements declaration", cursor: strings.Index(source, "nested"), wantFragments: []string{"func nested(input Alias) Alias"}},
		{id: 12, name: "GoWithElements before markup", cursor: strings.Index(source, "local :="), wantFragments: []string{"var local Alias"}},
		{id: 13, name: "GoWithElements inside markup", cursor: strings.Index(source, "{local}") + 1, wantFragments: []string{"var local Alias"}},
		{id: 14, name: "GoWithElements after markup", cursor: strings.Index(source, "return local") + len("return "), wantFragments: []string{"var local Alias"}},
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
			"jsonrpc": "2.0", "id": request.id, "method": "textDocument/hover",
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
		t.Fatalf("hover response exposed virtual generated Go:\n%s", output.String())
	}
	for _, request := range requests {
		t.Run(request.name, func(t *testing.T) {
			hover := authoredHoverResult(t, output.String(), request.id)
			if hover == nil {
				t.Fatalf("hover returned null; output:\n%s", output.String())
			}
			if hover.Contents.Kind != "markdown" || !strings.HasPrefix(hover.Contents.Value, "```go\n") {
				t.Fatalf("hover contents = %+v, want Go Markdown", hover.Contents)
			}
			for _, fragment := range request.wantFragments {
				if !strings.Contains(hover.Contents.Value, fragment) {
					t.Errorf("hover %q does not contain %q", hover.Contents.Value, fragment)
				}
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

func authoredHoverResult(t *testing.T, output string, id int) *lsp.Hover {
	t.Helper()
	for part := range strings.SplitSeq(output, "Content-Length:") {
		_, body, ok := strings.Cut(part, "\r\n\r\n")
		if !ok {
			continue
		}
		var response struct {
			ID     int        `json:"id"`
			Result *lsp.Hover `json:"result"`
		}
		if err := json.Unmarshal([]byte(body), &response); err != nil || response.ID != id {
			continue
		}
		return response.Result
	}
	t.Fatalf("no hover response for id %d in:\n%s", id, output)
	return nil
}
