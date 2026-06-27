package corpus

import (
	"bytes"
	"fmt"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/codegen"
	"github.com/gsxhq/gsx/parser"
)

func caseModuleDir(tmp string, c *caseDoc) string { return filepath.Join(tmp, "cases", c.dir) }
func caseImportRoot(c *caseDoc) string            { return "corpustest/cases/" + c.dir }

// mustTempModule creates a temp dir with a go.mod wiring the gsx replace.
func mustTempModule(repoRoot string) string {
	tmp, err := os.MkdirTemp("", "corpuscase")
	if err != nil {
		panic(err)
	}
	gomod := "module corpustest\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => " + repoRoot + "\n"
	if err := os.WriteFile(filepath.Join(tmp, "go.mod"), []byte(gomod), 0o644); err != nil {
		panic(err)
	}
	return tmp
}

// astAndParserDiag parses a single-package case's input.gsx. It returns the AST
// dump and any parser diagnostic text. single=false for multi-package cases
// (no input.gsx shorthand) — those carry no AST facet.
//
// Uses ParseFileWithClassifier so that recovery cases with multiple errors each
// produce one diagnostic line (rather than only the first error).
func (c *caseDoc) astAndParserDiag() (astDump []byte, parserDiag []byte, single bool) {
	src, has := c.files["input.gsx"]
	if !has || c.multiPkg {
		return nil, nil, false
	}
	fset := token.NewFileSet()
	// nil classifier → attrclass.Builtin(), same default as ParseFile.
	file, errs := parser.ParseFileWithClassifier(fset, "input.gsx", src, 0, nil)
	var dump, diag bytes.Buffer
	if file != nil {
		ast.Fprint(&dump, file)
	}
	for _, e := range errs {
		pos := fset.Position(e.Pos)
		if pos.IsValid() {
			fmt.Fprintf(&diag, "%d:%d: %s\n", pos.Line, pos.Column, e.Msg)
		} else {
			fmt.Fprintf(&diag, "%s\n", e.Msg)
		}
	}
	return dump.Bytes(), diag.Bytes(), true
}

// codegenGeneratePackages generates .x.go for every .gsx across the given
// package dirs via codegen.GenerateDirs (the Module-backed façade). Options:
// std filter, CSS+JS minify on, no overrides.
func codegenGeneratePackages(moduleDir string, dirs []string) (map[string]codegen.DirResult, error) {
	return codegen.GenerateDirs(moduleDir, dirs, codegen.GenOptions{
		FilterPkgs: []string{codegen.StdImportPath},
		CSSMinify:  true,
		JSMinify:   true,
	}, nil)
}

// packageDirs returns the distinct directories (relative to module root)
// containing .gsx files, sorted. "." for module-root files.
func (c *caseDoc) packageDirs() []string {
	seen := map[string]bool{}
	for name := range c.files {
		if !strings.HasSuffix(name, ".gsx") {
			continue
		}
		seen[filepath.ToSlash(filepath.Dir(name))] = true
	}
	var out []string
	for d := range seen {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// normalizeDiagPaths replaces occurrences of the temp module dir (and its
// filepath.Separator-trailing form) in diag with an empty prefix so that
// golden files contain stable relative paths independent of the OS temp dir.
func normalizeDiagPaths(diag []byte, tmpDir string) []byte {
	if len(diag) == 0 {
		return diag
	}
	// Replace "tmpDir/" (with separator) so remaining path is relative.
	prefix := tmpDir + string(filepath.Separator)
	return bytes.ReplaceAll(diag, []byte(prefix), nil)
}

func packageNameOf(src []byte) string {
	for _, line := range strings.Split(string(src), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "package ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "package "))
		}
	}
	return "views"
}
