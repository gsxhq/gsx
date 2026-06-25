package corpus

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/gsxhq/gsx/internal/diag"
)

type caseCodegen struct {
	gen  []byte // concatenated generated .x.go (sorted by gsx path), for the generated.x.go.golden facet
	diag []byte // normalized codegen diagnostics (empty if clean)
	html string // rendered HTML (renderable cases only; empty otherwise)
}

const caseMarkerPrefix = "\x00CASE "
const caseMarkerSuffix = "\x00"

// writeCaseSources writes a case's source files into moduleDir/<c.dir>,
// rewriting import paths for multi-package cases.
func writeCaseSources(moduleDir string, c *caseDoc) error {
	dir := caseModuleDir(moduleDir, c)
	root := caseImportRoot(c)
	for name, data := range c.files {
		if name == "go.mod" {
			continue
		}
		if c.multiPkg {
			data = rewriteImportPath(data, c.modulePath, root)
		}
		dst := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// batchCodegen writes ALL candidate cases' sources into ONE shared temp module,
// runs codegen.GeneratePackages ONCE, then builds+runs the renderable cases in a
// single `go run`. Returns per-case results keyed by case name.
func batchCodegen(repoRoot string, candidates []*caseDoc) (map[string]*caseCodegen, error) {
	if len(candidates) == 0 {
		return map[string]*caseCodegen{}, nil
	}

	tmp := mustTempModule(repoRoot)
	defer os.RemoveAll(tmp)

	// Step 1: write all candidates' sources (sequentially — file I/O is fast).
	for _, c := range candidates {
		if err := writeCaseSources(tmp, c); err != nil {
			return nil, fmt.Errorf("case %s: write sources: %w", c.name, err)
		}
	}

	// Step 2: collect all package dirs across all candidates, and build a map
	// from absolute package dir → owning case.
	// Also track each case's ordered package dirs.
	type caseState struct {
		c       *caseDoc
		pkgDirs []string // absolute paths
	}
	states := make([]*caseState, len(candidates))
	var allPkgDirs []string

	for i, c := range candidates {
		cs := &caseState{c: c}
		moduleDir := caseModuleDir(tmp, c)
		for _, relDir := range c.packageDirs() {
			absDir := filepath.Join(moduleDir, filepath.FromSlash(relDir))
			cs.pkgDirs = append(cs.pkgDirs, absDir)
			allPkgDirs = append(allPkgDirs, absDir)
		}
		states[i] = cs
	}

	// Step 3: ONE GeneratePackages call for all dirs.
	pkgResults, err := codegenGeneratePackages(tmp, allPkgDirs)
	if err != nil {
		return nil, fmt.Errorf("batchCodegen: GeneratePackages: %w", err)
	}

	// Step 4: reassemble per-case results.
	results := make(map[string]*caseCodegen, len(candidates))

	for _, cs := range states {
		c := cs.c
		cg := &caseCodegen{}
		root := caseImportRoot(c)

		// Collect package results for this case.
		// Check if any package has an error.
		var allFiles []struct {
			dir  string
			path string // original .gsx path
			data []byte
		}
		hasErr := false
		var diagBuf bytes.Buffer

		for _, pkgDir := range cs.pkgDirs {
			pr, ok := pkgResults[pkgDir]
			if !ok {
				// Shouldn't happen; treat as error.
				fmt.Fprintf(&diagBuf, "codegen: no result for %s\n", pkgDir)
				hasErr = true
				continue
			}
			// Render diagnostics from pr.Diags.
			// Format: "line:col: message" for positioned diagnostics (Start.Line > 0),
			// or just "message" for positionless ones (so codegen goldens stay unchanged).
			if len(pr.Diags) > 0 {
				for _, d := range pr.Diags {
					formatDiagLine(&diagBuf, d)
				}
				hasErr = true
				continue
			}
			// Collect files for this dir.
			for gsxPath, genData := range pr.Files {
				// Rewrite import paths in generated output (no-op when modulePath=="").
				out := rewriteImportPath(genData, c.modulePath, root)
				allFiles = append(allFiles, struct {
					dir  string
					path string
					data []byte
				}{dir: pkgDir, path: gsxPath, data: out})
			}
		}

		if hasErr {
			cg.diag = normalizeDiagPaths(diagBuf.Bytes(), tmp)
		} else {
			// Sort files: by pkgDir order (matching packageDirs() order), then by gsx path.
			// Build ordered dir list to match concatByDir behaviour.
			orderedDirs := cs.pkgDirs // already in packageDirs() order

			// Group files by dir.
			byDir := map[string][]struct {
				path string
				data []byte
			}{}
			for _, f := range allFiles {
				byDir[f.dir] = append(byDir[f.dir], struct {
					path string
					data []byte
				}{f.path, f.data})
			}
			// Sort within each dir by gsx path.
			for dir := range byDir {
				sort.Slice(byDir[dir], func(i, j int) bool {
					return byDir[dir][i].path < byDir[dir][j].path
				})
			}

			var genBuf bytes.Buffer
			for _, dir := range orderedDirs {
				files := byDir[dir]
				for _, f := range files {
					genBuf.Write(f.data)
				}
			}
			gen := genBuf.Bytes()
			if len(gen) > 0 {
				cg.gen = gen
			}

			// Write .x.go files to disk for renderable cases (needed by go run).
			for dir, files := range byDir {
				for _, f := range files {
					base := strings.TrimSuffix(filepath.Base(f.path), ".gsx")
					xgoPath := filepath.Join(dir, base+".x.go")
					if err := os.WriteFile(xgoPath, f.data, 0o644); err != nil {
						return nil, fmt.Errorf("case %s: write .x.go: %w", c.name, err)
					}
				}
			}
		}

		results[c.name] = cg
	}

	// Step 5: build and run all renderable cases with a single `go run`.
	var imports, dispatch bytes.Buffer
	built := 0

	for _, c := range candidates {
		if !c.renderable() {
			continue
		}
		cg := results[c.name]
		if cg == nil || len(cg.diag) > 0 {
			continue // codegen failed; not buildable
		}
		moduleDir := caseModuleDir(tmp, c)
		root := caseImportRoot(c)
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

	if built > 0 {
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
			if cg := results[name]; cg != nil {
				cg.html = html
			}
		}
	}

	return results, nil
}

// writeEntry writes the GsxEntryRender wrapper (codegen already ran in batchCodegen)
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

// formatDiagLine formats one diagnostic into the diagBuf for golden comparison.
// Positioned diagnostics (Start.Line > 0) get "line:col: message\n".
// Positionless ones (e.g. codegen-layer errors in Task 2) get just "message\n",
// matching the old pr.Err.Error() format so existing codegen goldens are unchanged.
func formatDiagLine(buf *bytes.Buffer, d diag.Diagnostic) {
	if d.Start.Line > 0 {
		fmt.Fprintf(buf, "%d:%d: %s\n", d.Start.Line, d.Start.Column, d.Message)
	} else {
		buf.WriteString(d.Message)
		buf.WriteByte('\n')
	}
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
