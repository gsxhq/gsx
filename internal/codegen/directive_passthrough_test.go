package codegen

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestGoDirectiveLines(t *testing.T) {
	doc := `// Copyright 2026 Prose. Must not copy.
//
//go:build !windows && !never

// +build !windows,!never

//go:generate stringer -type=Kind
//line input.gsx:1
// go:build not-a-directive (space after //)
//golintish prose that merely starts with //go... but no colon-directive
//go:debug panicnil=1`
	want := []string{
		"//go:build !windows && !never",
		"// +build !windows,!never",
		"//go:generate stringer -type=Kind",
		"//go:debug panicnil=1",
	}
	if got := goDirectiveLines(doc); !reflect.DeepEqual(got, want) {
		t.Fatalf("goDirectiveLines:\n got %q\nwant %q", got, want)
	}
	if got := goDirectiveLines(""); got != nil {
		t.Fatalf("empty doc: got %q, want nil", got)
	}
}

func TestBuildTagExcludesGeneratedFile(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns the go toolchain")
	}
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module tagx\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	writeFile(t, tmp, "on.gsx", "package tagx\n\ncomponent On() {\n\t<p>on</p>\n}\n")
	writeFile(t, tmp, "off.gsx", "//go:build never\n\npackage tagx\n\ncomponent Off() {\n\t<p>off</p>\n}\n")

	out, err := GenerateDirs(tmp, []string{tmp}, Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for gsxPath, src := range out[tmp].Files {
		base := strings.TrimSuffix(filepath.Base(gsxPath), ".gsx")
		if werr := os.WriteFile(filepath.Join(tmp, base+".x.go"), src, 0o644); werr != nil {
			t.Fatal(werr)
		}
	}
	list := exec.Command("go", "list", "-f", "{{.GoFiles}}")
	list.Dir = tmp
	lout, lerr := list.CombinedOutput()
	if lerr != nil {
		t.Fatalf("go list: %v\n%s", lerr, lout)
	}
	if !strings.Contains(string(lout), "on.x.go") || strings.Contains(string(lout), "off.x.go") {
		t.Fatalf("go list = %s; want on.x.go included, off.x.go tag-excluded", lout)
	}
	build := exec.Command("go", "build", "./...")
	build.Dir = tmp
	if bout, berr := build.CombinedOutput(); berr != nil {
		t.Fatalf("go build: %v\n%s", berr, bout)
	}
}
