package corpus

import (
	"bytes"
	"fmt"
	goparser "go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"slices"
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

// codegenDirs generates .x.go for every .gsx across the given package dirs in ONE
// codegen.Module. Options: std filter, CSS+JS minify on.
//
// loadPkgs is the union of every case's filter packages: they are loaded once,
// into one importer. perDir then narrows each dir to its OWN filter table and
// class merger, harvested from those loaded types with no further packages.Load.
// The union must not leak into the tables — a case that asserts a non-whitelisted
// package is rejected as a filter source would otherwise pass while testing nothing.
// renderers is the corpus-wide union of every candidate case's [renderers]
// registrations (see caseDoc.renderers). Unlike FilterPkgs/ClassMerger,
// Options.Renderers has no PerDir override — it is module-wide — so it
// needs no per-dir loadPkgs union step: Module's externalImporter already
// folds Options.Renderers' package paths into its own packages.Load.
func codegenDirs(moduleDir string, dirs []string, loadPkgs []string, perDir map[string]codegen.DirOptions, renderers []codegen.RendererAlias) (map[string]codegen.DirResult, error) {
	return codegen.GenerateDirs(moduleDir, dirs, codegen.Options{
		FilterPkgs: []string{codegen.StdImportPath},
		LoadPkgs:   loadPkgs,
		PerDir:     perDir,
		Renderers:  renderers,
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
	out := slices.Collect(maps.Keys(seen))
	slices.Sort(out)
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

// packageNameOf returns the package clause of a .gsx source. PackageClauseOnly
// stops the parse right after the clause, so the gsx-specific body that follows
// (component declarations, elements) is never scanned as Go.
func packageNameOf(src []byte) (string, error) {
	f, err := goparser.ParseFile(token.NewFileSet(), "input.gsx", src, goparser.PackageClauseOnly)
	if err != nil {
		return "", fmt.Errorf("package clause: %w", err)
	}
	return f.Name.Name, nil
}
