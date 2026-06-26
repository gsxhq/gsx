// Package typebundle serializes a transitively-closed set of go/types packages
// into a single self-contained blob and reconstructs it later with no Go
// toolchain and no subprocess. It exists so the gsx transform can resolve types
// against a fixed import allowlist (e.g. the playground's) inside a WASM build,
// where shelling out to `go list` (what packages.Load does) is impossible.
//
// The bundle is produced once at build time (where `go` exists) via Write, then
// embedded and consumed at runtime via Read — Read uses only go/types +
// gcexportdata and never execs anything.
package typebundle

import (
	"bytes"
	"go/token"
	"go/types"

	"golang.org/x/tools/go/gcexportdata"
)

// Write serializes pkgs — which MUST be transitively closed (every package any
// of them imports is also present) — into a single bundle. fset is the file set
// the packages were type-checked against. Package unsafe is dropped: it is
// predeclared/known to the compiler and cannot be exported (Read re-seeds it).
func Write(fset *token.FileSet, pkgs []*types.Package) ([]byte, error) {
	filtered := make([]*types.Package, 0, len(pkgs))
	for _, p := range pkgs {
		if p == types.Unsafe || p.Path() == "unsafe" {
			continue
		}
		filtered = append(filtered, p)
	}
	var buf bytes.Buffer
	if err := gcexportdata.WriteBundle(&buf, fset, filtered); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Read reconstructs the package set from a Write bundle and returns a
// path -> *types.Package map suitable as the backing of a types.Importer. It
// performs no subprocess: pure go/types + gcexportdata, so it is safe in WASM.
func Read(data []byte) (map[string]*types.Package, error) {
	fset := token.NewFileSet()
	// Seed unsafe so packages that import it resolve during reconstruction.
	imports := map[string]*types.Package{"unsafe": types.Unsafe}
	pkgs, err := gcexportdata.ReadBundle(bytes.NewReader(data), fset, imports)
	if err != nil {
		return nil, err
	}
	m := make(map[string]*types.Package, len(pkgs)+1)
	m["unsafe"] = types.Unsafe
	for _, p := range pkgs {
		m[p.Path()] = p
	}
	return m, nil
}
