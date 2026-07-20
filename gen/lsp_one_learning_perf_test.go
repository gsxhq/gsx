package gen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/gsxhq/gsx/internal/lsp"
)

const oneLearningPerfFixtureCommit = "ba0bc63096f5fa3d2b1464ab8eadf51673e61789"

// TestLSPManualOneLearningPerf measures one reproducible production LSP
// session against the pinned one-learning fixture. It is intentionally opt-in:
// setup copies a large repository, and the cold workspace query analyzes every
// GSX package in the module.
//
// The three heap snapshots follow two forced GCs each:
//   - fixture-ready: copied fixture on disk, before analyzer/server creation;
//   - initialized: fresh production server initialized, no module-symbol query;
//   - post-query: same live server/analyzer after its first workspace/symbol.
//
// The post-query HeapAlloc/HeapInuse values are total live Go-process heap, not
// object-attributed sizes. Their deltas remove some process/test-harness noise,
// but neither total nor delta is a causal attribution to the source index.
func TestLSPManualOneLearningPerf(t *testing.T) {
	if os.Getenv("GSX_PERF") == "" || os.Getenv("GSX_LSP_FIXTURE") == "" {
		t.Skip("set both GSX_PERF=1 and GSX_LSP_FIXTURE to the pinned one-learning checkout")
	}
	fixture, err := filepath.Abs(os.Getenv("GSX_LSP_FIXTURE"))
	if err != nil {
		t.Fatal(err)
	}
	fixtureCommit := perfGitCommit(t, fixture)
	if fixtureCommit != oneLearningPerfFixtureCommit {
		t.Fatalf("fixture commit = %s, want pinned %s", fixtureCommit, oneLearningPerfFixtureCommit)
	}
	codeCommit := perfGitCommit(t, ".")

	destination := filepath.Join(t.TempDir(), "one-learning")
	if err := copyOneLearningPerfFixture(fixture, destination); err != nil {
		t.Fatal(err)
	}
	// macOS exposes temporary directories through both /var and the canonical
	// /private/var path. Canonicalize once so the legacy cwd-derived baseline and
	// the current rootUri-derived server report byte-identical file URIs.
	destination, err = filepath.EvalSymlinks(destination)
	if err != nil {
		t.Fatal(err)
	}
	assertOneLearningPerfNoGeneratedGo(t, destination)
	badgePath := filepath.Join(destination, "ds", "badge", "badge.gsx")
	badgeSource, err := os.ReadFile(badgePath)
	if err != nil {
		t.Fatal(err)
	}
	// The pre-workspace-root baseline server derives a no-document workspace from
	// its process directory, while current servers consume rootUri. Running both
	// from the copied module makes that legacy routing and the current explicit
	// root semantically identical without opening (and warming) any package.
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(destination); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalDirectory); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	fixtureReady := readStableMemStats()
	inputReader, inputWriter := io.Pipe()
	wire := newPerfResponseWire()
	analyzer := newLSPAnalyzer(config{}, io.Discard)
	server := lsp.NewServer(inputReader, wire, analyzer)
	runResult := make(chan error, 1)
	go func() { runResult <- server.Run() }()

	writePerfFrame(t, inputWriter, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"rootUri": perfFileURI(destination), "capabilities": map[string]any{}},
	})
	wire.waitResult(t, runResult, 1)
	wire.reset()
	initialized := readStableMemStats()

	queryStart := time.Now()
	writePerfFrame(t, inputWriter, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "workspace/symbol",
		"params": map[string]any{"query": "Badge"},
	})
	workspaceResult := wire.waitResult(t, runResult, 2)
	coldWorkspaceLatency := time.Since(queryStart)
	if bytes.Equal(workspaceResult, []byte("[]")) {
		diagnoseOneLearningPerfPackages(t, analyzer, destination)
	}
	assertOneLearningPerfBadgeResult(t, workspaceResult, badgePath, badgeSource)
	wire.reset()
	postQuery := readStableMemStats()

	// Keep the measured state live through the post-query snapshot. The copied
	// fixture itself lives on disk; transient copy buffers were collected before
	// fixtureReady, while the server/analyzer retain the parsed module session.
	runtime.KeepAlive(server)
	runtime.KeepAlive(analyzer)
	runtime.KeepAlive(destination)
	runtime.KeepAlive(wire)

	writePerfFrame(t, inputWriter, map[string]any{"jsonrpc": "2.0", "method": "exit"})
	select {
	case runErr := <-runResult:
		if runErr != nil {
			t.Fatalf("LSP server: %v", runErr)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("LSP server did not exit")
	}
	if err := inputWriter.Close(); err != nil {
		t.Fatal(err)
	}

	t.Logf("fixture commit: %s", fixtureCommit)
	t.Logf("gsx code commit: %s", codeCommit)
	t.Logf("cold workspace/symbol latency: %s", coldWorkspaceLatency)
	logOneLearningPerfHeap(t, "fixture-ready", fixtureReady)
	logOneLearningPerfHeap(t, "initialized", initialized)
	logOneLearningPerfHeap(t, "post-query", postQuery)
	t.Logf("post-query minus fixture-ready: HeapAlloc=%d bytes (%.3f MiB) HeapInuse=%d bytes (%.3f MiB)",
		int64(postQuery.HeapAlloc)-int64(fixtureReady.HeapAlloc), bytesToMiB(int64(postQuery.HeapAlloc)-int64(fixtureReady.HeapAlloc)),
		int64(postQuery.HeapInuse)-int64(fixtureReady.HeapInuse), bytesToMiB(int64(postQuery.HeapInuse)-int64(fixtureReady.HeapInuse)))
	t.Logf("post-query minus initialized: HeapAlloc=%d bytes (%.3f MiB) HeapInuse=%d bytes (%.3f MiB)",
		int64(postQuery.HeapAlloc)-int64(initialized.HeapAlloc), bytesToMiB(int64(postQuery.HeapAlloc)-int64(initialized.HeapAlloc)),
		int64(postQuery.HeapInuse)-int64(initialized.HeapInuse), bytesToMiB(int64(postQuery.HeapInuse)-int64(initialized.HeapInuse)))
}

func diagnoseOneLearningPerfPackages(t *testing.T, analyzer lspAnalyzer, root string) {
	t.Helper()
	symbols, symbolErr := analyzer.ModuleSymbols(root, nil)
	t.Logf("diagnostic direct ModuleSymbols: symbols=%d error=%v", len(symbols), symbolErr)
	for index, symbol := range symbols {
		if index == 10 {
			break
		}
		t.Logf("diagnostic symbol %d: %s %s", index, symbol.Container, symbol.Name)
	}
	dirs, err := discoverDirs([]string{root})
	if err != nil {
		t.Logf("diagnostic module discovery: %v", err)
		return
	}
	successes := 0
	logged := 0
	for _, dir := range dirs {
		if _, err := analyzer.Analyze(dir, nil); err != nil {
			if logged < 10 {
				t.Logf("diagnostic package %s: %v", dir, err)
				logged++
			}
			continue
		}
		successes++
	}
	t.Logf("diagnostic package summary: %d/%d analyzed successfully", successes, len(dirs))
}

func readStableMemStats() runtime.MemStats {
	runtime.GC()
	runtime.GC()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return stats
}

func logOneLearningPerfHeap(t *testing.T, label string, stats runtime.MemStats) {
	t.Helper()
	t.Logf("%s total heap: HeapAlloc=%d bytes (%.3f MiB) HeapInuse=%d bytes (%.3f MiB)",
		label, stats.HeapAlloc, bytesToMiB(int64(stats.HeapAlloc)), stats.HeapInuse, bytesToMiB(int64(stats.HeapInuse)))
}

func bytesToMiB(value int64) float64 {
	return float64(value) / (1024 * 1024)
}

func perfGitCommit(t *testing.T, directory string) string {
	t.Helper()
	output, err := exec.Command("git", "-C", directory, "rev-parse", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("git commit for %s: %v: %s", directory, err, output)
	}
	return strings.TrimSpace(string(output))
}

func copyOneLearningPerfFixture(source, destination string) error {
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

func assertOneLearningPerfNoGeneratedGo(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".x.go") {
			t.Errorf("copied fixture contains generated output: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func perfFileURI(path string) string {
	return (&url.URL{Scheme: "file", Path: filepath.Clean(path)}).String()
}

func writePerfFrame(t *testing.T, writer io.Writer, value any) {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	frame := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)
	if _, err := io.WriteString(writer, frame); err != nil {
		t.Fatal(err)
	}
}

type perfResponseWire struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	notify chan struct{}
}

func newPerfResponseWire() *perfResponseWire {
	return &perfResponseWire{notify: make(chan struct{}, 1)}
}

func (wire *perfResponseWire) Write(data []byte) (int, error) {
	wire.mu.Lock()
	written, err := wire.buffer.Write(data)
	wire.mu.Unlock()
	select {
	case wire.notify <- struct{}{}:
	default:
	}
	return written, err
}

func (wire *perfResponseWire) reset() {
	wire.mu.Lock()
	wire.buffer.Reset()
	wire.mu.Unlock()
}

func (wire *perfResponseWire) waitResult(t *testing.T, runResult <-chan error, id int) json.RawMessage {
	t.Helper()
	timeout := time.NewTimer(10 * time.Minute)
	defer timeout.Stop()
	for {
		if result, ok := wire.result(id); ok {
			return result
		}
		select {
		case <-wire.notify:
		case err := <-runResult:
			t.Fatalf("LSP server stopped before response %d: %v", id, err)
		case <-timeout.C:
			t.Fatalf("timed out waiting for LSP response %d", id)
		}
	}
}

func (wire *perfResponseWire) result(id int) (json.RawMessage, bool) {
	wire.mu.Lock()
	stream := bytes.Clone(wire.buffer.Bytes())
	wire.mu.Unlock()
	for len(stream) != 0 {
		headerEnd := bytes.Index(stream, []byte("\r\n\r\n"))
		if headerEnd < 0 {
			return nil, false
		}
		header := string(stream[:headerEnd])
		lengthText, found := strings.CutPrefix(header, "Content-Length: ")
		if !found {
			return nil, false
		}
		length, err := strconv.Atoi(strings.TrimSpace(lengthText))
		if err != nil || len(stream) < headerEnd+4+length {
			return nil, false
		}
		body := stream[headerEnd+4 : headerEnd+4+length]
		stream = stream[headerEnd+4+length:]
		var response struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(body, &response); err != nil || response.ID != id {
			continue
		}
		if len(response.Error) != 0 && !bytes.Equal(response.Error, []byte("null")) {
			return nil, false
		}
		return bytes.Clone(response.Result), true
	}
	return nil, false
}

func assertOneLearningPerfBadgeResult(t *testing.T, raw json.RawMessage, badgePath string, badgeSource []byte) {
	t.Helper()
	type position struct {
		Line      int `json:"line"`
		Character int `json:"character"`
	}
	type symbol struct {
		Name          string `json:"name"`
		Kind          int    `json:"kind"`
		ContainerName string `json:"containerName"`
		Location      struct {
			URI   string `json:"uri"`
			Range struct {
				Start position `json:"start"`
				End   position `json:"end"`
			} `json:"range"`
		} `json:"location"`
	}
	var symbols []symbol
	if err := json.Unmarshal(raw, &symbols); err != nil {
		t.Fatalf("decode workspace symbols: %v: %s", err, raw)
	}
	nameOffset := bytes.Index(badgeSource, []byte("component Badge")) + len("component ")
	if nameOffset < len("component ") {
		t.Fatal("badge fixture has no component Badge declaration")
	}
	wantStart := perfUTF16PositionAt(string(badgeSource), nameOffset)
	wantEnd := perfUTF16PositionAt(string(badgeSource), nameOffset+len("Badge"))
	for _, got := range symbols {
		if got.Name != "Badge" || got.Location.URI != perfFileURI(badgePath) {
			continue
		}
		if got.Kind != 12 || got.ContainerName != "badge" || got.Location.Range.Start != wantStart || got.Location.Range.End != wantEnd {
			t.Fatalf("Badge workspace symbol = %+v, want exact function symbol badge %s:%+v-%+v", got, perfFileURI(badgePath), wantStart, wantEnd)
		}
		return
	}
	t.Fatalf("workspace Badge query has no exact Badge declaration: %s", raw)
}

func perfUTF16PositionAt(source string, offset int) struct {
	Line      int `json:"line"`
	Character int `json:"character"`
} {
	position := struct {
		Line      int `json:"line"`
		Character int `json:"character"`
	}{}
	for _, r := range source[:offset] {
		if r == '\n' {
			position.Line++
			position.Character = 0
			continue
		}
		position.Character += utf16.RuneLen(r)
	}
	return position
}
