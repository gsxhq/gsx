package gen

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/pprof"
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
	fixtureStats := inspectOneLearningPerfFixture(t, destination)
	packageDirs, err := discoverDirs([]string{destination})
	if err != nil {
		t.Fatal(err)
	}
	badgePath := filepath.Join(destination, "ds", "badge", "badge.gsx")
	badgeSource, err := os.ReadFile(badgePath)
	if err != nil {
		t.Fatal(err)
	}
	// Initialize below the fixture-root go.work. That committed file includes
	// absolute developer checkouts in addition to one-learning, which would make
	// a root-workspace measurement machine-specific and would ask the current
	// multi-root server to do more work than the single-root baseline supports.
	// The nearest-module search still selects the complete one-learning module.
	workspaceRoot := filepath.Dir(badgePath)
	workspaceMode := os.Getenv("GSX_LSP_WORKSPACE_MODE")
	switch workspaceMode {
	case "", "module":
		workspaceMode = "module"
	case "repository":
		workspaceRoot = destination
	default:
		t.Fatalf("GSX_LSP_WORKSPACE_MODE = %q, want module or repository", workspaceMode)
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
		"params": map[string]any{"rootUri": perfFileURI(workspaceRoot), "capabilities": map[string]any{}},
	})
	wire.waitResult(t, runResult, 1)
	wire.reset()
	initialized := readStableMemStats()

	stopCPUProfile := startOneLearningPerfCPUProfile(t)
	queryStart := time.Now()
	writePerfFrame(t, inputWriter, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "workspace/symbol",
		"params": map[string]any{"query": "Badge"},
	})
	workspaceResult := wire.waitResult(t, runResult, 2)
	coldWorkspaceLatency := time.Since(queryStart)
	stopCPUProfile()
	if bytes.Equal(workspaceResult, []byte("[]")) {
		diagnoseOneLearningPerfPackages(t, analyzer, destination)
	}
	resultCount := assertOneLearningPerfBadgeResult(t, workspaceResult, badgePath, badgeSource)
	wire.reset()
	postQuery := readStableMemStats()
	writeOneLearningPerfHeapProfile(t)

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
	t.Logf("fixture tree: sha256=%x regular-files=%d gsx-files=%d bytes=%d package-dirs=%d",
		fixtureStats.sha256, fixtureStats.regularFiles, fixtureStats.gsxFiles, fixtureStats.bytes, len(packageDirs))
	t.Logf("workspace result: mode=%s query=Badge symbols=%d root=%s uri=%s", workspaceMode, resultCount, workspaceRoot, perfFileURI(badgePath))
	t.Logf("server caches: %s", inspectOneLearningPerfServer(server))
	t.Logf("analyzer caches: %s", inspectOneLearningPerfAnalyzer(analyzer))
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

type oneLearningPerfFixtureStats struct {
	sha256       [sha256.Size]byte
	regularFiles int
	gsxFiles     int
	bytes        int64
}

func inspectOneLearningPerfFixture(t *testing.T, root string) oneLearningPerfFixtureStats {
	t.Helper()
	hash := sha256.New()
	stats := oneLearningPerfFixtureStats{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		contentHash := sha256.Sum256(contents)
		if _, err := fmt.Fprintf(hash, "%q %o %d %x\n", filepath.ToSlash(relative), info.Mode().Perm(), len(contents), contentHash); err != nil {
			return err
		}
		stats.regularFiles++
		stats.bytes += int64(len(contents))
		if strings.HasSuffix(entry.Name(), ".gsx") {
			stats.gsxFiles++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	copy(stats.sha256[:], hash.Sum(nil))
	return stats
}

func startOneLearningPerfCPUProfile(t *testing.T) func() {
	t.Helper()
	path := os.Getenv("GSX_LSP_CPU_PROFILE")
	if path == "" {
		return func() {}
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := pprof.StartCPUProfile(file); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	var once sync.Once
	stop := func() {
		once.Do(func() {
			pprof.StopCPUProfile()
			if err := file.Close(); err != nil {
				t.Errorf("close CPU profile: %v", err)
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

func writeOneLearningPerfHeapProfile(t *testing.T) {
	t.Helper()
	path := os.Getenv("GSX_LSP_HEAP_PROFILE")
	if path == "" {
		return
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := pprof.WriteHeapProfile(file); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func inspectOneLearningPerfServer(server *lsp.Server) string {
	value := reflect.ValueOf(server).Elem()
	parts := []string{"pkgs=" + strconv.Itoa(reflectCollectionLen(value, "pkgs"))}
	moduleSymbols := value.FieldByName("moduleSyms")
	switch moduleSymbols.Kind() {
	case reflect.Slice:
		parts = append(parts, "module-symbol-roots=1", "module-symbols="+strconv.Itoa(moduleSymbols.Len()))
	case reflect.Map:
		symbols := 0
		iterator := moduleSymbols.MapRange()
		for iterator.Next() {
			cached := iterator.Value()
			if cached.Kind() == reflect.Struct {
				symbols += cached.FieldByName("symbols").Len()
			}
		}
		parts = append(parts, "module-symbol-roots="+strconv.Itoa(moduleSymbols.Len()), "module-symbols="+strconv.Itoa(symbols))
	}
	for _, field := range []string{"workspaceRoots", "workspaceModules"} {
		if count := reflectCollectionLen(value, field); count >= 0 {
			parts = append(parts, field+"="+strconv.Itoa(count))
		}
	}
	if modules := reflectStringSlice(value, "workspaceModules"); len(modules) != 0 {
		parts = append(parts, "module-paths="+strings.Join(modules, ","))
	}
	return strings.Join(parts, " ")
}

func inspectOneLearningPerfAnalyzer(analyzer lspAnalyzer) string {
	parts := []string{
		"module-roots=" + strconv.Itoa(len(analyzer.mods.byRoot)),
		"overrides=" + strconv.Itoa(len(analyzer.mods.overrideRoots)),
	}
	fieldTotals := map[string]int{}
	for _, module := range analyzer.mods.byRoot {
		value := reflect.ValueOf(module).Elem()
		for _, field := range []string{"pkgResults", "pkgTypes", "targetDeclTypes", "configuredDeclTypes", "targetDeclProvenance", "sourcePackages", "sourceGsxDirs", "extPkgs"} {
			if count := reflectCollectionLen(value, field); count >= 0 {
				fieldTotals[field] += count
			}
		}
		for _, field := range []string{"extLoads", "filterLoads", "sourceIndexBuildCount"} {
			candidate := value.FieldByName(field)
			if candidate.IsValid() && candidate.Kind() == reflect.Int {
				fieldTotals[field] += int(candidate.Int())
			}
		}
	}
	for _, field := range []string{"pkgResults", "pkgTypes", "targetDeclTypes", "configuredDeclTypes", "targetDeclProvenance", "sourcePackages", "sourceGsxDirs", "extPkgs", "extLoads", "filterLoads", "sourceIndexBuildCount"} {
		if count, ok := fieldTotals[field]; ok {
			parts = append(parts, field+"="+strconv.Itoa(count))
		}
	}
	return strings.Join(parts, " ")
}

func reflectCollectionLen(value reflect.Value, field string) int {
	candidate := value.FieldByName(field)
	if !candidate.IsValid() {
		return -1
	}
	switch candidate.Kind() {
	case reflect.Array, reflect.Chan, reflect.Map, reflect.Slice, reflect.String:
		return candidate.Len()
	default:
		return -1
	}
}

func reflectStringSlice(value reflect.Value, field string) []string {
	candidate := value.FieldByName(field)
	if !candidate.IsValid() || candidate.Kind() != reflect.Slice || candidate.Type().Elem().Kind() != reflect.String {
		return nil
	}
	result := make([]string, candidate.Len())
	for index := range candidate.Len() {
		result[index] = candidate.Index(index).String()
	}
	return result
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

func assertOneLearningPerfBadgeResult(t *testing.T, raw json.RawMessage, badgePath string, badgeSource []byte) int {
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
		return len(symbols)
	}
	t.Fatalf("workspace Badge query has no exact Badge declaration: %s", raw)
	return 0
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
