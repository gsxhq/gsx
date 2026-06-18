package corpus

import (
	"bytes"
	"flag"
	"go/token"
	"os"
	"path/filepath"
	"testing"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/txtar"
	"github.com/gsxhq/gsx/parser"
)

var update = flag.Bool("update", false, "regenerate golden sections in testdata/pipeline/*.txtar")

func TestPipeline(t *testing.T) {
	files, _ := filepath.Glob("testdata/pipeline/*.txtar")
	if len(files) == 0 {
		t.Fatal("no testdata/pipeline/*.txtar cases")
	}
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			arc := txtar.Parse(data)
			sec := map[string]int{} // name -> index in arc.Files
			for i, f := range arc.Files {
				sec[f.Name] = i
			}
			inputIdx, ok := sec["input.gsx"]
			if !ok {
				t.Fatal("missing -- input.gsx --")
			}
			// run pipeline
			fset := token.NewFileSet()
			file, perr := parser.ParseFile(fset, "input.gsx", arc.Files[inputIdx].Data, 0)
			var astDump bytes.Buffer
			if file != nil {
				ast.Fprint(&astDump, file)
			}
			var diag bytes.Buffer
			if perr != nil {
				diag.WriteString(perr.Error())
				diag.WriteByte('\n')
			}
			// compare / update each golden section
			checkSection(t, arc, "ast.golden", astDump.Bytes(), path)
			checkSection(t, arc, "diagnostics.golden", diag.Bytes(), path)
			if *update {
				writeArchive(t, path, arc)
			}
		})
	}
}

// setSection replaces the Data of the named section if it exists, or appends it.
func setSection(arc *txtar.Archive, name string, data []byte) {
	for i, f := range arc.Files {
		if f.Name == name {
			arc.Files[i].Data = data
			return
		}
	}
	arc.Files = append(arc.Files, txtar.File{Name: name, Data: data})
}

// checkSection compares got to the named section. Under -update it calls setSection
// instead of comparing. An absent section is treated as empty.
func checkSection(t *testing.T, arc *txtar.Archive, name string, got []byte, path string) {
	t.Helper()
	if *update {
		setSection(arc, name, got)
		return
	}
	var want []byte
	for _, f := range arc.Files {
		if f.Name == name {
			want = f.Data
			break
		}
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s: %s mismatch\n--- got ---\n%s\n--- want ---\n%s", path, name, got, want)
	}
}

// writeArchive writes the archive back to path.
func writeArchive(t *testing.T, path string, arc *txtar.Archive) {
	t.Helper()
	if err := os.WriteFile(path, txtar.Format(arc), 0644); err != nil {
		t.Fatalf("writeArchive %s: %v", path, err)
	}
}
