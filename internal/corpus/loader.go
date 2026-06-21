package corpus

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gsxhq/gsx/internal/txtar"
)

type caseDoc struct {
	name       string
	dir        string
	archive    *txtar.Archive
	files      map[string][]byte
	invoke     []byte
	goldens    map[string][]byte
	multiPkg   bool
	modulePath string
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
	// name: strip leading path up to and including "testdata/" and trailing ".txtar".
	rel := filepath.ToSlash(path)
	if i := strings.Index(rel, "testdata/"); i >= 0 {
		rel = rel[i+len("testdata/"):]
	}
	c.name = strings.TrimSuffix(rel, ".txtar")
	c.dir = strings.ReplaceAll(c.name, "/", "_")

	for _, f := range arc.Files {
		switch {
		case f.Name == "invoke":
			c.invoke = f.Data
		case goldenSections[f.Name]:
			c.goldens[f.Name] = f.Data
		default:
			c.files[f.Name] = f.Data
			if strings.Contains(f.Name, "/") {
				c.multiPkg = true
			}
			if f.Name == "go.mod" {
				c.multiPkg = true
				c.modulePath = parseModulePath(f.Data)
			}
		}
	}
	return c, nil
}

func parseModulePath(gomod []byte) string {
	for _, line := range strings.Split(string(gomod), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module "))
		}
	}
	return ""
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
