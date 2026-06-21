package corpus

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

type renderResult struct {
	html        string
	diagnostics []byte
	generated   []byte
}

const caseMarkerPrefix = "\x00CASE "
const caseMarkerSuffix = "\x00"

func renderBatch(repoRoot string, cases []*caseDoc) (map[string]*renderResult, error) {
	res := map[string]*renderResult{}
	tmp := mustTempModule(repoRoot)
	defer os.RemoveAll(tmp)

	var imports, dispatch bytes.Buffer
	built := 0
	for _, c := range cases {
		if !c.renderable() {
			continue
		}
		moduleDir := caseModuleDir(tmp, c)
		root := caseImportRoot(c)
		gen, diag := c.generate(moduleDir, root)
		res[c.name] = &renderResult{diagnostics: diag, generated: gen}
		if len(diag) > 0 {
			continue // codegen failed; not buildable
		}
		entryPkg, err := c.writeEntry(moduleDir, root)
		if err != nil {
			return nil, fmt.Errorf("case %s: %w", c.name, err)
		}
		alias := fmt.Sprintf("case%d", built)
		built++
		fmt.Fprintf(&imports, "\t%s %q\n", alias, entryPkg)
		fmt.Fprintf(&dispatch, "\tos.Stdout.WriteString(%q)\n\t_ = %s.GsxEntryRender(ctx, os.Stdout)\n",
			caseMarkerPrefix+c.name+caseMarkerSuffix+"\n", alias)
	}
	if built == 0 {
		return res, nil
	}

	main := "package main\n\nimport (\n\t\"context\"\n\t\"os\"\n" + imports.String() + ")\n\nfunc main() {\n\tctx := context.Background()\n" + dispatch.String() + "}\n"
	if err := os.WriteFile(filepath.Join(tmp, "main.go"), []byte(main), 0o644); err != nil {
		return nil, err
	}

	cmd := exec.Command("go", "run", ".")
	cmd.Dir = tmp
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("batch go run: %w\n%s", err, stderr.String())
	}
	for name, html := range splitBatchOutput(stdout.String()) {
		if r := res[name]; r != nil {
			r.html = html
		}
	}
	return res, nil
}

// writeEntry writes the GsxEntryRender wrapper (codegen already ran in generate)
// and returns the import path of the package that holds it.
func (c *caseDoc) writeEntry(moduleDir, root string) (string, error) {
	entry := "import (\n\t_gsxctx \"context\"\n\t_gsxio \"io\"\n)\n\nfunc GsxEntryRender(ctx _gsxctx.Context, w _gsxio.Writer) error {\n\treturn (" + string(bytes.TrimSpace(c.invoke)) + ").Render(ctx, w)\n}\n"

	if c.multiPkg {
		entryDir := filepath.Join(moduleDir, "gsxentry")
		if err := os.MkdirAll(entryDir, 0o755); err != nil {
			return "", err
		}
		// Import only packages the invoke references, by package name.
		nameToPath := map[string]string{}
		for _, dir := range c.packageDirs() {
			nameToPath[c.packageNameInDir(dir)] = root + "/" + dir
		}
		var imps bytes.Buffer
		for name := range referencedQualifiers(c.invoke) {
			if p, ok := nameToPath[name]; ok {
				fmt.Fprintf(&imps, "\t%s %q\n", name, p)
			}
		}
		body := "package gsxentry\n\nimport (\n" + imps.String() + ")\n\n" + entry
		if err := os.WriteFile(filepath.Join(entryDir, "entry.go"), []byte(body), 0o644); err != nil {
			return "", err
		}
		return root + "/gsxentry", nil
	}

	pkgName := packageNameOf(c.files["input.gsx"])
	// If the invoke references gsx. (e.g. gsx.Raw, gsx.Attrs), add the import so
	// the generated entry file compiles. Each Go file in a package needs its own
	// import declarations even though other files in the package already import gsx.
	extraImport := ""
	if referencedQualifiers(c.invoke)["gsx"] {
		extraImport = "import \"github.com/gsxhq/gsx\"\n\n"
	}
	body := "package " + pkgName + "\n\n" + extraImport + entry
	if err := os.WriteFile(filepath.Join(moduleDir, "gsxentry.go"), []byte(body), 0o644); err != nil {
		return "", err
	}
	return root, nil
}

// packageNameInDir returns the package clause of the first .gsx file in dir.
func (c *caseDoc) packageNameInDir(dir string) string {
	for name, data := range c.files {
		if strings.HasSuffix(name, ".gsx") && filepath.ToSlash(filepath.Dir(name)) == dir {
			return packageNameOf(data)
		}
	}
	return "views"
}

var qualifierRe = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\.`)

// referencedQualifiers returns the set of identifiers used as `ident.` in src
// (a superset of package qualifiers; non-package matches are harmless because
// they won't match a known package name).
func referencedQualifiers(src []byte) map[string]bool {
	out := map[string]bool{}
	for _, m := range qualifierRe.FindAllSubmatch(src, -1) {
		out[string(m[1])] = true
	}
	return out
}

func splitBatchOutput(out string) map[string]string {
	res := map[string]string{}
	for _, p := range strings.Split(out, caseMarkerPrefix) {
		end := strings.Index(p, caseMarkerSuffix)
		if end < 0 {
			continue
		}
		res[p[:end]] = strings.TrimPrefix(p[end+len(caseMarkerSuffix):], "\n")
	}
	return res
}
