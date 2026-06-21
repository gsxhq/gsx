package corpus

import (
	"bytes"
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
func (c *caseDoc) astAndParserDiag() (astDump []byte, parserDiag []byte, single bool) {
	src, has := c.files["input.gsx"]
	if !has || c.multiPkg {
		return nil, nil, false
	}
	file, perr := parser.ParseFile(token.NewFileSet(), "input.gsx", src, 0)
	var dump, diag bytes.Buffer
	if file != nil {
		ast.Fprint(&dump, file)
	}
	if perr != nil {
		diag.WriteString(perr.Error())
		diag.WriteByte('\n')
	}
	return dump.Bytes(), diag.Bytes(), true
}

// generate is the SINGLE place GeneratePackage is invoked. It writes sources
// (rewriting the module path for multi-package cases), generates each package,
// writes the .x.go next to its source, and returns concatenated generated
// source + codegen diagnostics.
func (c *caseDoc) generate(moduleDir, importRoot string) (genConcat []byte, diag []byte) {
	var d bytes.Buffer
	for name, data := range c.files {
		if name == "go.mod" {
			continue
		}
		if c.multiPkg {
			data = rewriteImportPath(data, c.modulePath, importRoot)
		}
		dst := filepath.Join(moduleDir, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(dst), 0o755)
		os.WriteFile(dst, data, 0o644)
	}
	var parts []string
	for _, dir := range c.packageDirs() {
		pkgDir := filepath.Join(moduleDir, filepath.FromSlash(dir))
		gen, err := codegen.GeneratePackage(pkgDir)
		if err != nil {
			d.WriteString(err.Error())
			d.WriteByte('\n')
			continue
		}
		gsxPaths := make([]string, 0, len(gen))
		for p := range gen {
			gsxPaths = append(gsxPaths, p)
		}
		sort.Strings(gsxPaths)
		for _, p := range gsxPaths {
			out := rewriteImportPath(gen[p], c.modulePath, importRoot) // no-op when modulePath==""
			base := strings.TrimSuffix(filepath.Base(p), ".gsx")
			os.WriteFile(filepath.Join(pkgDir, base+".x.go"), out, 0o644)
			parts = append(parts, string(out))
		}
	}
	return []byte(strings.Join(parts, "")), d.Bytes()
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

func packageNameOf(src []byte) string {
	for _, line := range strings.Split(string(src), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "package ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "package "))
		}
	}
	return "views"
}
