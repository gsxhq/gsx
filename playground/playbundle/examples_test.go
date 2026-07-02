package playbundle

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/examplegen"
)

func examplesDir(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "examples")
}

// TestAllExamplesTransform runs every playground example through the WASM
// transform engine (embedded bundle, no subprocess), asserting each generates
// with no error diagnostics. The live playground depends on this: the browser
// transforms; the server only compiles+runs. Run under GOOS=js GOARCH=wasm in CI
// to prove it holds in the browser runtime too.
func TestAllExamplesTransform(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping example-corpus transform in -short mode")
	}
	exs, err := examplegen.Load(examplesDir(t))
	if err != nil {
		t.Fatalf("load examples: %v", err)
	}
	if len(exs) == 0 {
		t.Fatal("no examples found")
	}
	r, err := NewResolver()
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	for _, ex := range exs {
		t.Run(ex.Name, func(t *testing.T) {
			res, _ := r.GenerateSources(splitForTest(ex.Source))
			for _, d := range res.Diags {
				if d.Severity.String() == "error" {
					t.Errorf("%d:%d %s", d.Start.Line, d.Start.Column, d.Message)
				}
			}
			if len(res.Files) == 0 && !t.Failed() {
				t.Errorf("no generated files")
			}
		})
	}
}

// splitForTest mirrors the wasm splitSources for the `-- file.gsx --` format.
func splitForTest(src string) map[string][]byte {
	files := map[string][]byte{}
	cur := ""
	var buf []string
	flush := func() {
		if cur != "" {
			files[cur] = []byte(strings.Join(buf, "\n"))
		}
	}
	for ln := range strings.SplitSeq(src, "\n") {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "-- ") && strings.HasSuffix(t, " --") {
			flush()
			cur = strings.TrimSpace(t[3 : len(t)-3])
			buf = nil
			continue
		}
		buf = append(buf, ln)
	}
	flush()
	if len(files) == 0 {
		files["source.gsx"] = []byte(src)
	}
	return files
}
