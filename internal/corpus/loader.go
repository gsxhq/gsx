package corpus

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/gsxhq/gsx/internal/attrclass"
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
	classifier  *attrclass.Classifier   // set when case has a gsx.toml with [[urlAttrs]] rules
}

// caseToml holds the subset of gsx.toml fields the corpus harness reads.
// Other fields are allowed but ignored.
type caseToml struct {
	ClassMerger    string        `toml:"class_merger"`
	FilterPackages []string      `toml:"filterPackages"`
	URLAttrs       []caseURLRule `toml:"urlAttrs"`
	URLPresets     []string      `toml:"url_presets"`
}

// caseURLRule mirrors attrclass.Rule's toml shape for a case's [[urlAttrs]]
// entries: exactly one of Name/Prefix must be set (validated via Rule.Valid).
type caseURLRule struct {
	Name   string `toml:"name"`
	Prefix string `toml:"prefix"`
}

var goldenSections = map[string]bool{
	"diagnostics.golden":    true,
	"render.golden":         true,
	"generated.x.go.golden": true,
	"ast.golden":            true,
}

// txtarFiles returns every *.txtar under root, sorted. Walk errors are
// reported rather than skipped: a corpus that silently loses cases to an
// unreadable directory would still pass.
func txtarFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, ".txtar") {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.Sort(files)
	return files, nil
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
			var rules []attrclass.Rule
			for i, u := range tc.URLAttrs {
				r := attrclass.Rule{Name: u.Name, Prefix: u.Prefix}
				if err := r.Valid(); err != nil {
					return nil, fmt.Errorf("gsx.toml: urlAttrs[%d]: %w", i, err)
				}
				rules = append(rules, r)
			}
			for _, name := range tc.URLPresets {
				pr, ok := attrclass.Preset(name)
				if !ok {
					return nil, fmt.Errorf("gsx.toml: url_presets: unknown preset %q", name)
				}
				rules = append(rules, pr.URL...)
			}
			if len(rules) > 0 {
				c.classifier = attrclass.New(attrclass.Rules{URL: rules}, nil)
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
	slices.Sort(out)
	return out
}

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
