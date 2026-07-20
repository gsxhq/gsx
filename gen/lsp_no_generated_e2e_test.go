package gen

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
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
	<widgets.Box[widgets.Label] value={label}/>
}
`
	widgetsPath := writeLSPIndependenceFile(t, root, "widgets/widgets.gsx", widgets)
	pagePath := writeLSPIndependenceFile(t, root, "page/page.gsx", page)
	generatedPaths := []string{
		strings.TrimSuffix(widgetsPath, ".gsx") + ".x.go",
		strings.TrimSuffix(pagePath, ".gsx") + ".x.go",
	}

	run := func(t *testing.T) map[int]json.RawMessage {
		t.Helper()
		pageURI := lspTestPathURI(pagePath)
		input := frameMsg(t, map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "initialize",
			"params": map[string]any{"rootUri": lspTestPathURI(root), "capabilities": map[string]any{}},
		})
		input += frameMsg(t, map[string]any{
			"jsonrpc": "2.0", "method": "textDocument/didOpen",
			"params": map[string]any{"textDocument": map[string]any{"uri": pageURI, "version": 1, "text": page}},
		})
		definitionOffset := strings.Index(page, "return local") + len("return ")
		input += lspPositionRequestFrame(t, 2, "textDocument/definition", pageURI, page, definitionOffset)
		hoverOffset := strings.Index(page, "{value}") + 1
		input += lspPositionRequestFrame(t, 3, "textDocument/hover", pageURI, page, hoverOffset)
		input += frameMsg(t, map[string]any{
			"jsonrpc": "2.0", "id": 4, "method": "textDocument/documentSymbol",
			"params": map[string]any{"textDocument": map[string]any{"uri": pageURI}},
		})
		input += frameMsg(t, map[string]any{
			"jsonrpc": "2.0", "id": 5, "method": "workspace/symbol",
			"params": map[string]any{"query": ""},
		})
		input += frameMsg(t, map[string]any{"jsonrpc": "2.0", "method": "exit"})

		var output, stderr bytes.Buffer
		if code := runLSP(strings.NewReader(input), &output, &stderr, config{}, nil); code != 0 {
			t.Fatalf("runLSP=%d stderr=%s", code, stderr.String())
		}
		results := make(map[int]json.RawMessage, 4)
		for _, id := range []int{2, 3, 4, 5} {
			response := lspTestResponse(t, output.String(), id)
			if len(response.Error) != 0 || len(response.Result) == 0 || bytes.Equal(response.Result, []byte("null")) || bytes.Equal(response.Result, []byte("[]")) {
				t.Fatalf("structured response %d is empty: result=%s error=%s\nall output:\n%s", id, response.Result, response.Error, output.String())
			}
			results[id] = bytes.Clone(response.Result)
		}
		return results
	}

	for _, path := range generatedPaths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("generated output exists before absent run: %s", path)
		}
	}
	want := run(t)

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
			}
			got := run(t)
			for _, id := range []int{2, 3, 4, 5} {
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
				if !info.ModTime().Equal(before[path].ModTime()) || int64(len(contents)) != before[path].Size() {
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
	for _, relative := range poisonPaths {
		path := filepath.Join(destination, filepath.FromSlash(relative))
		if err := os.WriteFile(path, []byte(poison), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	got := runOneLearningProbe(t, destination)
	if !slices.Equal(want, got) {
		t.Fatalf("one-learning structured answers changed after poisoning generated outputs:\nabsent=%q\npoisoned=%q", want, got)
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
		if string(contents) != poison || !info.ModTime().Equal(stamp) {
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

func runOneLearningProbe(t *testing.T, root string) []string {
	t.Helper()
	type target struct {
		path   string
		needle string
		delta  int
	}
	targets := []target{
		{path: "email/templates.gsx", needle: "time.Duration", delta: len("time.")},
		{path: "ds/badge/badge.gsx", needle: "component Badge", delta: len("component ")},
		{path: "ds/badge/badge.gsx", needle: "variant Variant"},
		{path: "ui/pacm_edit.gsx", needle: "pgtype.Bool", delta: len("pgtype.")},
		{path: "ui/dashboard_npt.gsx", needle: "duration.Hours", delta: len("duration.")},
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
	frames = append(frames, frameMsg(t, map[string]any{
		"jsonrpc": "2.0", "id": nextID, "method": "workspace/symbol",
		"params": map[string]any{"query": "Badge"},
	}))
	resultIDs = append(resultIDs, nextID)
	frames = append(frames, frameMsg(t, map[string]any{"jsonrpc": "2.0", "method": "exit"}))

	var output, stderr bytes.Buffer
	if code := runLSP(strings.NewReader(strings.Join(frames, "")), &output, &stderr, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, stderr.String())
	}
	results := make([]string, 0, len(resultIDs))
	for _, id := range resultIDs {
		response := lspTestResponse(t, output.String(), id)
		if len(response.Error) != 0 || len(response.Result) == 0 || bytes.Equal(response.Result, []byte("null")) || bytes.Equal(response.Result, []byte("[]")) {
			t.Fatalf("one-learning response %d is empty: result=%s error=%s\nstderr=%s", id, response.Result, response.Error, stderr.String())
		}
		results = append(results, string(response.Result))
	}
	return results
}
