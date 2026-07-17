package gen

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/lsp"
)

func TestLSPDidCloseRestoresSavedOrAbsentSourceInWarmModule(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in short mode")
	}
	for _, test := range []struct {
		name           string
		path           string
		diskSource     string
		bufferSource   string
		wantAfterOpen  string
		wantAfterClose string
	}{
		{
			name:           "saved file",
			path:           "page.gsx",
			diskSource:     "package page\n\ncomponent Disk() { <div>disk</div> }\n",
			bufferSource:   "package page\n\ncomponent Buffer() { <div>{ nope }</div> }\n",
			wantAfterOpen:  ".Buffer",
			wantAfterClose: ".Disk",
		},
		{
			name:           "unsaved new file",
			path:           "new.gsx",
			bufferSource:   "package page\n\ncomponent New() { <div>{ nope }</div> }\n",
			wantAfterOpen:  ".New",
			wantAfterClose: ".Disk",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			repoRoot, err := filepath.Abs("..")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			pageDir := filepath.Join(root, "page")
			if err := os.MkdirAll(pageDir, 0o755); err != nil {
				t.Fatal(err)
			}
			basePath := filepath.Join(pageDir, "base.gsx")
			if err := os.WriteFile(basePath, []byte("package page\n\ncomponent Disk() { <div>disk</div> }\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(pageDir, test.path)
			if test.diskSource != "" {
				if err := os.Remove(basePath); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(test.diskSource), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			analyzer := newLSPAnalyzer(config{}, nil)
			warm, err := analyzer.Analyze(pageDir, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := warm.CrossIndex[".Disk"]; !ok {
				t.Fatalf("warm disk analysis components = %v, want .Disk", warm.CrossIndex)
			}

			uri := "file://" + path
			in := frameMsg(t, map[string]any{
				"jsonrpc": "2.0", "method": "textDocument/didOpen",
				"params": map[string]any{"textDocument": map[string]any{
					"uri": uri, "version": 1, "text": test.bufferSource,
				}},
			})
			in += frameMsg(t, map[string]any{
				"jsonrpc": "2.0", "method": "textDocument/didClose",
				"params": map[string]any{"textDocument": map[string]any{"uri": uri}},
			})
			in += frameMsg(t, map[string]any{"jsonrpc": "2.0", "method": "exit"})
			var out bytes.Buffer
			if err := lsp.NewServer(strings.NewReader(in), &out, analyzer).Run(); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "nope") {
				t.Fatalf("didOpen did not analyze the unsaved buffer: %s", out.String())
			}

			// A superseded worker may still call Analyze with the snapshot it took
			// before didClose. Analysis input is read-only: it must not restart the
			// lifetime of a buffer that the serialized ClearOverride transition ended.
			postClose, err := analyzer.Analyze(pageDir, map[string][]byte{path: []byte(test.bufferSource)})
			if err != nil {
				t.Fatal(err)
			}
			if _, stale := postClose.CrossIndex[test.wantAfterOpen]; stale {
				t.Fatalf("closed buffer component %s remains authoritative: %v", test.wantAfterOpen, postClose.CrossIndex)
			}
			if _, ok := postClose.CrossIndex[test.wantAfterClose]; !ok {
				t.Fatalf("post-close components = %v, want %s", postClose.CrossIndex, test.wantAfterClose)
			}
		})
	}
}
