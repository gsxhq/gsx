package codegen

import (
	"fmt"
	goast "go/ast"
	"go/types"
	"sort"
	"strconv"
	"strings"

	"github.com/gsxhq/gsx/internal/diag"
)

const externalMainModuleBackedgeCode = "external-main-module-backedge"

type externalMainModuleBackedgeError struct {
	path      string
	localDeps []string
}

func (e *externalMainModuleBackedgeError) Error() string {
	return fmt.Sprintf(
		"external package %q imports back into the main module through %s; external-to-main-module import backedges are unsupported",
		e.path,
		quotedPathList(e.localDeps),
	)
}

func quotedPathList(paths []string) string {
	quoted := make([]string, len(paths))
	for i, path := range paths {
		quoted[i] = strconv.Quote(path)
	}
	return strings.Join(quoted, ", ")
}

// externalBackedgeImporter makes the one-way external import boundary a hard
// importer invariant even for callers that do not own authored syntax. Source
// checkers additionally call rejectExternalBackedgeImports before go/types so
// user imports receive a stable positioned diagnostic instead of a generic
// importer failure.
type externalBackedgeImporter struct {
	packages  mapImporter
	backedges map[string][]string
}

func (i externalBackedgeImporter) Import(path string) (*types.Package, error) {
	if localDeps := i.backedges[path]; len(localDeps) != 0 {
		return nil, &externalMainModuleBackedgeError{path: path, localDeps: append([]string(nil), localDeps...)}
	}
	return i.packages.Import(path)
}

func (m *Module) externalBackedgeFor(path string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.externalBackedges[path]...)
}

// rejectExternalBackedgeImports reports every direct authored import whose
// external dependency graph re-enters the main module. The cold load computes
// the transitive boundary once; this pass only maps that exact fact onto the
// importing source syntax. It runs before checking in every local declaration
// universe, so no phase can accidentally observe a reconstructed or stale ABI.
func (m *Module) rejectExternalBackedgeImports(files []*goast.File) error {
	if len(files) == 0 {
		return nil
	}
	m.mu.Lock()
	backedges := make(map[string][]string, len(m.externalBackedges))
	for path, localDeps := range m.externalBackedges {
		backedges[path] = append([]string(nil), localDeps...)
	}
	m.mu.Unlock()
	if len(backedges) == 0 {
		return nil
	}
	var diagnostics []diag.Diagnostic
	for _, file := range files {
		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil || len(backedges[path]) == 0 {
				continue
			}
			boundaryErr := (&externalMainModuleBackedgeError{path: path, localDeps: backedges[path]}).Error()
			diagnostics = append(diagnostics, diag.Diagnostic{
				Start:    m.fset.Position(spec.Path.Pos()),
				End:      m.fset.Position(spec.Path.End()),
				Severity: diag.Error,
				Code:     externalMainModuleBackedgeCode,
				Message:  boundaryErr,
				Source:   "codegen",
			})
		}
	}
	if len(diagnostics) == 0 {
		return nil
	}
	sort.Slice(diagnostics, func(i, j int) bool {
		left, right := diagnostics[i].Start, diagnostics[j].Start
		if left.Filename != right.Filename {
			return left.Filename < right.Filename
		}
		if left.Offset != right.Offset {
			return left.Offset < right.Offset
		}
		return diagnostics[i].Message < diagnostics[j].Message
	})
	return sourceDiagnosticsError{diags: diagnostics}
}
