package corpus

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/gsxhq/gsx/internal/codegen"
	"github.com/gsxhq/gsx/internal/txtar"
)

type caseDoc struct {
	name        string
	dir         string
	archive     *txtar.Archive
	files       map[string][]byte
	invoke      []byte
	doc         []byte
	goldens     map[string][]byte
	multiPkg    bool
	modulePath  string
	classMerger *codegen.ClassMergerRef // set when case has a gsx.toml with class_merger
	filterPkgs  []string                // resolved import paths from gsx.toml filterPackages; "./x" entries resolve against the case import root
}

// caseToml holds the subset of gsx.toml fields the corpus harness reads.
// Other fields are allowed but ignored.
type caseToml struct {
	ClassMerger    string   `toml:"class_merger"`
	FilterPackages []string `toml:"filterPackages"`
}

var goldenSections = map[string]bool{
	"diagnostics.golden":    true,
	"render.golden":         true,
	"generated.x.go.golden": true,
	"ast.golden":            true,
}

// loadCase parses one txtar case file. name is derived from path relative to
// testdata/cases (or any testdata/<root>): the portion after "testdata/<root>/"
// minus the .txtar suffix.
func loadCase(path string) (*caseDoc, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	arc := txtar.Parse(data)
	c := &caseDoc{
		archive: arc,
		files:   map[string][]byte{},
		goldens: map[string][]byte{},
	}
	// name: relative to testdata/, with a leading "cases/" stripped so real
	// cases (testdata/cases/<area>/<scenario>) get spec-form names like
	// "attrs/expr_attrs"; fixtures elsewhere keep their dir (e.g. loadertest/single).
	rel := filepath.ToSlash(path)
	if i := strings.Index(rel, "testdata/"); i >= 0 {
		rel = rel[i+len("testdata/"):]
	} else if i := strings.Index(rel, "examples/"); i >= 0 {
		rel = rel[i+len("examples/"):]
	}
	rel = strings.TrimPrefix(rel, "cases/")
	c.name = strings.TrimSuffix(rel, ".txtar")
	c.dir = strings.ReplaceAll(c.name, "/", "_")

	for _, f := range arc.Files {
		switch {
		case f.Name == "invoke":
			c.invoke = f.Data
		case goldenSections[f.Name]:
			c.goldens[f.Name] = f.Data
		case f.Name == "doc":
			c.doc = f.Data
		case f.Name == "gsx.toml":
			// Parse class_merger for per-case codegen options; do not write to disk.
			var tc caseToml
			if _, err := toml.Decode(string(f.Data), &tc); err != nil {
				return nil, fmt.Errorf("gsx.toml: %w", err)
			}
			if tc.ClassMerger != "" {
				pkgPath, funcName, err := splitCasePkgFunc(tc.ClassMerger)
				if err != nil {
					return nil, fmt.Errorf("gsx.toml: class_merger: %w", err)
				}
				c.classMerger = &codegen.ClassMergerRef{PkgPath: pkgPath, FuncName: funcName}
			}
			for _, p := range tc.FilterPackages {
				if strings.HasPrefix(p, "./") {
					p = caseImportRoot(c) + strings.TrimPrefix(p, ".")
				}
				c.filterPkgs = append(c.filterPkgs, p)
			}
		default:
			c.files[f.Name] = f.Data
			// multiPkg: only .gsx files in subdirs (or go.mod) signal a multi-package
			// case. Pure-Go helper packages (e.g. a case-local merger) live in
			// subdirs but must not trigger the cross-package entry path.
			if strings.Contains(f.Name, "/") && strings.HasSuffix(f.Name, ".gsx") {
				c.multiPkg = true
			}
			if f.Name == "go.mod" {
				c.multiPkg = true
				c.modulePath = codegen.ModulePathFromGoMod(f.Data)
			}
		}
	}
	return c, nil
}

func (c *caseDoc) renderable() bool { return c.invoke != nil }

func (c *caseDoc) facets() []string {
	var out []string
	diag := "diag"
	if !c.renderable() && len(c.goldens["diagnostics.golden"]) > 0 {
		diag = "diag(error)"
	}
	out = append(out, diag)
	if _, ok := c.goldens["render.golden"]; ok {
		out = append(out, "render")
	}
	if _, ok := c.goldens["generated.x.go.golden"]; ok {
		out = append(out, "gen")
	}
	if _, ok := c.goldens["ast.golden"]; ok {
		out = append(out, "ast")
	}
	sort.Strings(out)
	return out
}

var _ = fmt.Sprintf

// splitCasePkgFunc splits a "pkg/path.FuncName" string into its import path
// and exported func name by splitting at the last ".". Used to parse
// class_merger values from a case-local gsx.toml.
func splitCasePkgFunc(s string) (pkgPath, funcName string, err error) {
	dot := strings.LastIndex(s, ".")
	if dot < 0 {
		return "", "", fmt.Errorf("%q has no package-qualified name", s)
	}
	return s[:dot], s[dot+1:], nil
}
