package gen

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

// tmplData is the substitution context for init templates.
type tmplData struct {
	Module string // full Go module path, e.g. "github.com/me/app"
	Name   string // path.Base(Module), e.g. "app" (npm name, etc.)
}

// initTemplate is one registered starter: a name, a one-line description, and
// the root path of its subtree within the embedded template FS.
type initTemplate struct {
	name string
	desc string
	root string
}

const defaultTemplate = "simple"

// templates is the registry. The embedded FS and the "simple" subtree land in
// the template task; the registry entry is declared here.
var templates = map[string]initTemplate{
	"simple": {
		name: "simple",
		desc: "Stock net/http ServeMux + gsx + Vite dev loop.",
		root: "templates/init/simple",
	},
}

// scaffold walks the template subtree rooted at root within srcFS, renders each
// file with render, maps its name with transformName, and writes it under
// destDir (creating parent dirs). It overwrites existing files; the
// project-level existence guard lives in runInit.
func scaffold(srcFS fs.FS, root, destDir string, data tmplData, force bool) error {
	return fs.WalkDir(srcFS, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel := strings.TrimPrefix(p, root+"/")
		raw, err := fs.ReadFile(srcFS, p)
		if err != nil {
			return err
		}
		rendered, err := render(raw, data)
		if err != nil {
			return err
		}
		dest := filepath.Join(destDir, transformName(rel))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dest, rendered, 0o644)
	})
}

// transformName maps a template-relative path to its output path: a trailing
// ".tmpl" is stripped, and any path segment prefixed "dot-" becomes a dotfile.
func transformName(rel string) string {
	parts := strings.Split(rel, "/")
	for i, seg := range parts {
		if strings.HasPrefix(seg, "dot-") {
			parts[i] = "." + strings.TrimPrefix(seg, "dot-")
		}
	}
	last := len(parts) - 1
	parts[last] = strings.TrimSuffix(parts[last], ".tmpl")
	return filepath.Join(parts...)
}

// render runs raw through text/template with «» delimiters (so the gsx {{ }} and
// { } in templates pass through untouched) and the given data.
func render(raw []byte, data tmplData) ([]byte, error) {
	t, err := template.New("f").Delims("«", "»").Parse(string(raw))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
