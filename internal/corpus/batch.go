package corpus

import (
	"bytes"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/gsxhq/gsx/internal/codegen"
	"github.com/gsxhq/gsx/internal/diag"
)

// genFile is one generated .x.go: the package dir it belongs to, the original
// .gsx path it came from, and its bytes.
type genFile struct {
	dir  string
	path string
	data []byte
}

// caseGenFile is one successful generated file staged for the outer execution
// module. dir is relative to the case root; path is the original .gsx path.
type caseGenFile struct {
	dir  string
	path string
	data []byte
}

type caseCodegen struct {
	gen    []byte        // concatenated generated .x.go (sorted by gsx path), for the generated.x.go.golden facet
	files  []caseGenFile // successful generated files for the one shared execution module
	diag   []byte        // normalized codegen diagnostics (empty if clean; non-empty for warnings too, not just errors)
	hasErr bool          // true iff any diagnostic was error-severity; gates buildability (diag being non-empty does not, since warnings don't block a build)
	html   string        // rendered HTML (renderable cases only; empty otherwise)
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

// batchCodegen partitions code generation by exact ordered module-wide renderer
// registry, then stages every partition's successful output in one shared
// execution module and performs one go run. A renderer registration is
// module-wide, so putting differing case registries in the same codegen Module
// would make one case's generated aliases and validation depend on which other
// corpus subtests happened to run.
//
// The empty registry is one partition, preserving the single large batch for
// the no-renderer majority. Cases with identical registries also continue to
// share a codegen batch. Build and render are never repeated per partition.
func batchCodegen(repoRoot string, candidates []*caseDoc) (map[string]*caseCodegen, error) {
	return batchCodegenWithWorkers(repoRoot, candidates, batchCodegenPartition, executeBatch)
}

// batchCodegenWithWorkers exposes the codegen/execution boundary to focused
// harness tests without mutable package globals. partition runs once per exact
// renderer registry; execute runs once after every result passes integrity
// checks.
func batchCodegenWithWorkers(
	repoRoot string,
	candidates []*caseDoc,
	partition func(string, []*caseDoc) (map[string]*caseCodegen, error),
	execute func(string, []*caseDoc, map[string]*caseCodegen) error,
) (map[string]*caseCodegen, error) {
	seenCandidates := make(map[string]bool, len(candidates))
	byDirName := make(map[string]string, len(candidates))
	for _, c := range candidates {
		if seenCandidates[c.name] {
			return nil, fmt.Errorf("batchCodegen: duplicate candidate %q", c.name)
		}
		seenCandidates[c.name] = true
		if prev, dup := byDirName[c.dir]; dup {
			return nil, fmt.Errorf("case dir collision: %q and %q both map to %q; rename one", prev, c.name, c.dir)
		}
		byDirName[c.dir] = c.name
	}

	results := make(map[string]*caseCodegen, len(candidates))
	for i, group := range partitionCodegenCandidatesByRendererRegistry(candidates) {
		part, err := partition(repoRoot, group)
		if err != nil {
			return nil, fmt.Errorf("batchCodegen: renderer-registry partition %d: %w", i, err)
		}
		expected := make(map[string]bool, len(group))
		for _, c := range group {
			expected[c.name] = true
			r, ok := part[c.name]
			if !ok {
				return nil, fmt.Errorf("batchCodegen: renderer-registry partition %d produced no result for %q", i, c.name)
			}
			if r == nil {
				return nil, fmt.Errorf("batchCodegen: renderer-registry partition %d produced nil result for %q", i, c.name)
			}
		}
		for name := range part {
			if _, dup := results[name]; dup {
				return nil, fmt.Errorf("batchCodegen: duplicate result for %q", name)
			}
			if !expected[name] {
				return nil, fmt.Errorf("batchCodegen: renderer-registry partition %d produced unexpected result for %q", i, name)
			}
		}
		for _, c := range group {
			results[c.name] = part[c.name]
		}
	}
	if len(candidates) > 0 {
		if err := execute(repoRoot, candidates, results); err != nil {
			return nil, fmt.Errorf("batchCodegen: execute: %w", err)
		}
	}
	return results, nil
}

// partitionCodegenCandidatesByRendererRegistry groups candidates by typed,
// exact slice comparison of their complete ordered RendererAlias registries.
// First-seen group and candidate order are retained, making partition execution
// deterministic without an ambiguous string serialization.
func partitionCodegenCandidatesByRendererRegistry(candidates []*caseDoc) [][]*caseDoc {
	var groups [][]*caseDoc
	var registries [][]codegen.RendererAlias
	for _, c := range candidates {
		group := -1
		for i, registry := range registries {
			if slices.Equal(registry, c.renderers) {
				group = i
				break
			}
		}
		if group < 0 {
			registries = append(registries, slices.Clone(c.renderers))
			groups = append(groups, nil)
			group = len(groups) - 1
		}
		groups[group] = append(groups[group], c)
	}
	return groups
}

// batchCodegenPartition writes one renderer-registry partition's authored
// sources into a small temp module and runs codegenDirs once for all its dirs.
// It returns generated/diagnostic data only; build and render belong to the one
// outer execution module shared by every partition.
func batchCodegenPartition(repoRoot string, candidates []*caseDoc) (map[string]*caseCodegen, error) {
	if len(candidates) == 0 {
		return map[string]*caseCodegen{}, nil
	}

	// caseDoc.dir flattens the case name ("a/b" → "a_b") to name its directory in
	// the shared temp module, so "a/b_c" and "a_b/c" would land in the same place
	// and silently overwrite each other's sources. Reject that up front.
	byDirName := make(map[string]string, len(candidates))
	for _, c := range candidates {
		if prev, dup := byDirName[c.dir]; dup {
			return nil, fmt.Errorf("case dir collision: %q and %q both map to %q; rename one", prev, c.name, c.dir)
		}
		byDirName[c.dir] = c.name
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

	for i, c := range candidates {
		cs := &caseState{c: c}
		moduleDir := caseModuleDir(tmp, c)
		for _, relDir := range c.packageDirs() {
			absDir := filepath.Join(moduleDir, filepath.FromSlash(relDir))
			cs.pkgDirs = append(cs.pkgDirs, absDir)
		}
		states[i] = cs
	}

	// Step 3: codegen — ONE call for every dir. Cases carrying a gsx.toml
	// (class_merger, filter_packages, and/or url_attrs) contribute a PerDir entry;
	// their filter packages go into the shared load set. Previously each such
	// case opened its own Module, and every Module re-ran packages.Load over the
	// gsx runtime: 27 cases cost 10.7s of the corpus's 13.2s.
	var allDirs, loadPkgs []string
	var renderers []codegen.RendererAlias
	perDir := map[string]codegen.DirOptions{}
	seenPkg := map[string]bool{}
	for _, cs := range states {
		allDirs = append(allDirs, cs.pkgDirs...)
		// Renderers are module-wide (no PerDir override — see codegenDirs), so
		// every candidate case's registrations fold into the ONE shared list
		// regardless of whether it also needs a DirOptions entry below.
		renderers = append(renderers, cs.c.renderers...)
		if cs.c.classMerger == nil && len(cs.c.filterPkgs) == 0 && cs.c.classifier == nil {
			continue
		}
		var filters []string
		if len(cs.c.filterPkgs) > 0 {
			filters = append([]string{codegen.StdImportPath}, cs.c.filterPkgs...)
			for _, p := range cs.c.filterPkgs {
				if !seenPkg[p] {
					seenPkg[p] = true
					loadPkgs = append(loadPkgs, p)
				}
			}
		}
		if cs.c.classMerger != nil && !seenPkg[cs.c.classMerger.PkgPath] {
			seenPkg[cs.c.classMerger.PkgPath] = true
			loadPkgs = append(loadPkgs, cs.c.classMerger.PkgPath)
		}
		// Every dir of a multi-package case shares that case's options — an
		// imported sibling must resolve the same filters as the dir importing it.
		// classifier needs no packages.Load contribution: attrclass.New builds it
		// entirely in-process from the case's own toml rules.
		for _, d := range cs.pkgDirs {
			perDir[filepath.Clean(d)] = codegen.DirOptions{
				FilterPkgs:  filters, // nil ⇒ inherit the std-only default
				ClassMerger: cs.c.classMerger,
				Classifier:  cs.c.classifier,
			}
		}
	}
	pkgResults, err := codegenDirs(tmp, allDirs, loadPkgs, perDir, renderers)
	if err != nil {
		return nil, fmt.Errorf("batchCodegen: codegenDirs: %w", err)
	}

	// Step 4: reassemble per-case results.
	results := make(map[string]*caseCodegen, len(candidates))

	for _, cs := range states {
		c := cs.c
		cg := &caseCodegen{}
		root := caseImportRoot(c)
		caseRoot := caseModuleDir(tmp, c)

		// Collect package results for this case.
		// Check if any package has an error.
		var allFiles []genFile
		hasErr := false
		var diagBuf bytes.Buffer

		for _, pkgDir := range cs.pkgDirs {
			pr, ok := pkgResults[pkgDir]
			if !ok {
				// A harness invariant, not a case outcome: every pkgDir was passed
				// to codegenDirs, so a missing result means the batch is broken.
				// Never fold this into diagnostics.golden, where a golden could absorb it.
				return nil, fmt.Errorf("case %s: codegen produced no result for %s", c.name, pkgDir)
			}
			// Check if this package has any error-level diagnostics.
			pkgHasErr := false
			for _, d := range pr.Diags {
				if d.Severity == diag.Error {
					pkgHasErr = true
					break
				}
			}
			// Render diagnostics from pr.Diags.
			// Format: "line:col: message" for positioned diagnostics (Start.Line > 0),
			// or just "message" for positionless ones (so codegen goldens stay unchanged).
			// Non-error severities get a prefix: "warning: ", "info: ", "hint: ".
			if len(pr.Diags) > 0 {
				for _, d := range pr.Diags {
					formatDiagLine(&diagBuf, d)
				}
				if pkgHasErr {
					hasErr = true
					continue
				}
			}
			// Collect files for this dir.
			for gsxPath, genData := range pr.Files {
				// Rewrite import paths in generated output (no-op when modulePath=="").
				out := rewriteImportPath(genData, c.modulePath, root)
				allFiles = append(allFiles, genFile{dir: pkgDir, path: gsxPath, data: out})
			}
		}

		cg.hasErr = hasErr
		if diagBuf.Len() > 0 {
			cg.diag = normalizeDiagPaths(diagBuf.Bytes(), tmp)
		}
		if !hasErr {
			// Sort files: by pkgDir order (matching packageDirs() order), then by gsx path.
			// Build ordered dir list to match concatByDir behaviour.
			orderedDirs := cs.pkgDirs // already in packageDirs() order

			// Group files by dir.
			byDir := map[string][]genFile{}
			for _, f := range allFiles {
				byDir[f.dir] = append(byDir[f.dir], f)
			}
			// Sort within each dir by gsx path.
			for dir := range byDir {
				slices.SortFunc(byDir[dir], func(a, b genFile) int {
					return strings.Compare(a.path, b.path)
				})
			}

			var genBuf bytes.Buffer
			for _, dir := range orderedDirs {
				relDir, err := filepath.Rel(caseRoot, dir)
				if err != nil {
					return nil, fmt.Errorf("case %s: generated package dir: %w", c.name, err)
				}
				relDir = filepath.ToSlash(relDir)
				for _, f := range byDir[dir] {
					genBuf.Write(f.data)
					cg.files = append(cg.files, caseGenFile{dir: relDir, path: f.path, data: f.data})
				}
			}
			gen := genBuf.Bytes()
			if len(gen) > 0 {
				cg.gen = gen
			}
		}

		results[c.name] = cg
	}

	return results, nil
}

// executeBatch stages every case's authored sources and successful generated
// files into one canonical shared temp module, then builds and runs all cases in
// one go run. Non-renderable generated cases are blank-imported so they compile.
func executeBatch(repoRoot string, candidates []*caseDoc, results map[string]*caseCodegen) error {
	tmp := mustTempModule(repoRoot)
	defer os.RemoveAll(tmp)

	for _, c := range candidates {
		if err := writeCaseSources(tmp, c); err != nil {
			return fmt.Errorf("case %s: stage authored sources: %w", c.name, err)
		}
		cg := results[c.name]
		if cg == nil {
			return fmt.Errorf("case %s: stage generated files: missing result", c.name)
		}
		if cg.hasErr {
			continue
		}
		caseRoot := caseModuleDir(tmp, c)
		for _, f := range cg.files {
			base := strings.TrimSuffix(filepath.Base(f.path), ".gsx")
			xgoPath := filepath.Join(caseRoot, filepath.FromSlash(f.dir), base+".x.go")
			if err := os.MkdirAll(filepath.Dir(xgoPath), 0o755); err != nil {
				return fmt.Errorf("case %s: stage generated dir: %w", c.name, err)
			}
			if err := os.WriteFile(xgoPath, f.data, 0o644); err != nil {
				return fmt.Errorf("case %s: stage generated file: %w", c.name, err)
			}
		}
	}

	// Build and run all renderable cases with a single `go run`.
	// Non-renderable cases that produced generated output are blank-imported into
	// the same main.go so they COMPILE too. A .gsx with no component has nothing
	// to invoke, so before this its .x.go was golden-pinned but never built — the
	// blind spot that hid `generate` emitting unused imports and redeclared
	// identifiers while exiting 0.
	var imports, dispatch bytes.Buffer
	built := 0
	compiled := 0

	for _, c := range candidates {
		if !c.renderable() {
			continue
		}
		cg := results[c.name]
		if cg == nil || cg.hasErr {
			continue // codegen failed; not buildable
		}
		moduleDir := caseModuleDir(tmp, c)
		root := caseImportRoot(c)
		entryPkg, err := c.writeEntry(moduleDir, root)
		if err != nil {
			return fmt.Errorf("case %s: %w", c.name, err)
		}
		alias := fmt.Sprintf("case%d", built)
		built++
		fmt.Fprintf(&imports, "\t%s %q\n", alias, entryPkg)
		fmt.Fprintf(&dispatch, "\tos.Stdout.WriteString(%q)\n\tif err := %s.GsxEntryRender(ctx, os.Stdout); err != nil {\n\t\tfmt.Fprintf(os.Stdout, \"\\n[render error] %%v\", err)\n\t}\n",
			caseMarkerPrefix+c.name+caseMarkerSuffix+"\n", alias)
	}

	for _, c := range candidates {
		if c.renderable() {
			continue // already imported (and thus compiled) by the loop above
		}
		cg := results[c.name]
		if cg == nil || len(cg.gen) == 0 || cg.hasErr {
			// No generated output, or the case pins an error diagnostic — an
			// error case is not meant to compile.
			continue
		}
		root := caseImportRoot(c)
		pkgs := []string{root}
		if c.multiPkg {
			pkgs = pkgs[:0]
			for _, dir := range c.packageDirs() {
				pkgs = append(pkgs, root+"/"+dir)
			}
		}
		for _, p := range pkgs {
			fmt.Fprintf(&imports, "\t_ %q\n", p)
			compiled++
		}
	}

	if built > 0 || compiled > 0 {
		// With no renderable case, context/fmt/os would be unused imports in the
		// harness's own main.go — emit the minimal program instead.
		main := "package main\n\nimport (\n" + imports.String() + ")\n\nfunc main() {}\n"
		if built > 0 {
			main = "package main\n\nimport (\n\t\"context\"\n\t\"fmt\"\n\t\"os\"\n" + imports.String() + ")\n\nfunc main() {\n\tctx := context.Background()\n" + dispatch.String() + "}\n"
		}
		if err := os.WriteFile(filepath.Join(tmp, "main.go"), []byte(main), 0o644); err != nil {
			return err
		}

		cmd := exec.Command("go", "run", ".")
		cmd.Dir = tmp
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("batch go run: %w\n%s", err, stderr.String())
		}
		for name, html := range splitBatchOutput(stdout.String()) {
			if cg := results[name]; cg != nil {
				cg.html = html
			}
		}
	}

	return nil
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
			pkgName, err := c.packageNameInDir(dir)
			if err != nil {
				return "", fmt.Errorf("dir %s: %w", dir, err)
			}
			nameToPath[pkgName] = root + "/" + dir
		}
		var imps bytes.Buffer
		for _, name := range slices.Sorted(maps.Keys(referencedQualifiers(c.invoke))) {
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

	// Read the clause from whichever root .gsx exists — single-package cases are
	// not required to name their file input.gsx (examples/100-template-composition
	// uses components.gsx + page.gsx).
	pkgName, err := c.packageNameInDir(".")
	if err != nil {
		return "", err
	}
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

// packageNameInDir returns the package clause of the first .gsx file in dir,
// in sorted filename order so the choice is deterministic.
func (c *caseDoc) packageNameInDir(dir string) (string, error) {
	for _, name := range slices.Sorted(maps.Keys(c.files)) {
		if strings.HasSuffix(name, ".gsx") && filepath.ToSlash(filepath.Dir(name)) == dir {
			return packageNameOf(c.files[name])
		}
	}
	return "", fmt.Errorf("no .gsx file in dir %q", dir)
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
// Non-error severities are prefixed: "warning: ", "info: ", "hint: ".
func formatDiagLine(buf *bytes.Buffer, d diag.Diagnostic) {
	sev := ""
	if d.Severity != diag.Error {
		sev = d.Severity.String() + ": "
	}
	if d.Start.Line > 0 {
		fmt.Fprintf(buf, "%d:%d: %s%s\n", d.Start.Line, d.Start.Column, sev, d.Message)
	} else {
		buf.WriteString(sev)
		buf.WriteString(d.Message)
		buf.WriteByte('\n')
	}
}

func splitBatchOutput(out string) map[string]string {
	res := map[string]string{}
	for p := range strings.SplitSeq(out, caseMarkerPrefix) {
		before, after, ok := strings.Cut(p, caseMarkerSuffix)
		if !ok {
			continue
		}
		res[before] = strings.TrimPrefix(after, "\n")
	}
	return res
}
