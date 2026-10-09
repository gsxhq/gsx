package typebundle

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"go/token"
	"go/types"
	"io"
	"sort"

	"golang.org/x/tools/go/gcexportdata"
)

// The package payload is a dependency-ordered sequence of records, one per
// package:
//
//	uint32 record count
//	per record:
//	  string path
//	  uint32 import count, then each import path (sorted)
//	  uint64 export data length, then gcexportdata.Write output
//
// Strings are a uint32 length followed by their bytes; integers are big-endian.
//
// gcexportdata export data is per package and self-contained: it redeclares
// whatever part of its dependencies it refers to. Decoding records in order
// into one shared imports map makes every dependency already complete when a
// dependent is decoded, so the importer reuses it and the bundle keeps one
// object identity per package across the whole universe. Export data does not
// carry a package's import list (gcexportdata.Read substitutes the packages it
// happened to reference), so the record carries it explicitly.

// encodePackages encodes pkgs, which must be closed under Imports and exclude
// unsafe, in a canonical dependency order: a depth-first post-order over
// path-sorted roots and path-sorted imports.
func encodePackages(pkgs []*types.Package) ([]byte, error) {
	sorted := append([]*types.Package(nil), pkgs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path() < sorted[j].Path() })
	order := make([]*types.Package, 0, len(sorted))
	visited := make(map[*types.Package]bool, len(sorted))
	var visit func(*types.Package)
	visit = func(pkg *types.Package) {
		if pkg == types.Unsafe || visited[pkg] {
			return
		}
		visited[pkg] = true
		for _, imported := range sortedImports(pkg) {
			visit(imported)
		}
		order = append(order, pkg)
	}
	for _, pkg := range sorted {
		visit(pkg)
	}

	var out bytes.Buffer
	writeUint32(&out, uint32(len(order)))
	var exportData bytes.Buffer
	for _, pkg := range order {
		writeString(&out, pkg.Path())
		imports := sortedImports(pkg)
		writeUint32(&out, uint32(len(imports)))
		for _, imported := range imports {
			writeString(&out, imported.Path())
		}
		exportData.Reset()
		// A nil FileSet writes every position as "none"; see Write.
		if err := gcexportdata.Write(&exportData, nil, pkg); err != nil {
			return nil, fmt.Errorf("typebundle: export package %q: %w", pkg.Path(), err)
		}
		_ = binary.Write(&out, binary.BigEndian, uint64(exportData.Len()))
		out.Write(exportData.Bytes())
	}
	return out.Bytes(), nil
}

func sortedImports(pkg *types.Package) []*types.Package {
	imports := append([]*types.Package(nil), pkg.Imports()...)
	sort.Slice(imports, func(i, j int) bool { return imports[i].Path() < imports[j].Path() })
	return imports
}

func writeUint32(out *bytes.Buffer, v uint32) {
	_ = binary.Write(out, binary.BigEndian, v)
}

func writeString(out *bytes.Buffer, s string) {
	writeUint32(out, uint32(len(s)))
	out.WriteString(s)
}

// decodePackages decodes an encodePackages payload. It returns the decoded
// packages in record order and the shared imports map they were decoded into,
// which also holds the types.Unsafe singleton.
func decodePackages(payload []byte) ([]*types.Package, map[string]*types.Package, error) {
	r := bytes.NewReader(payload)
	count, err := readUint32(r)
	if err != nil {
		return nil, nil, fmt.Errorf("read package count: %w", err)
	}
	// Every record is at least 16 bytes; bound the allocation by the input.
	if uint64(count) > uint64(r.Len())/16 {
		return nil, nil, fmt.Errorf("package count %d exceeds payload size", count)
	}
	fset := token.NewFileSet()
	imports := map[string]*types.Package{"unsafe": types.Unsafe}
	pkgs := make([]*types.Package, 0, count)
	// reachable[path] is the transitive import closure of a decoded package.
	reachable := make(map[string]map[*types.Package]bool, count)
	for range count {
		path, err := readString(r)
		if err != nil {
			return nil, nil, fmt.Errorf("read package path: %w", err)
		}
		if path == "" || path == "unsafe" {
			return nil, nil, fmt.Errorf("invalid package record path %q", path)
		}
		if imports[path] != nil {
			return nil, nil, fmt.Errorf("package %q is encoded more than once", path)
		}
		importCount, err := readUint32(r)
		if err != nil {
			return nil, nil, fmt.Errorf("package %q: read import count: %w", path, err)
		}
		if uint64(importCount) > uint64(r.Len())/4 {
			return nil, nil, fmt.Errorf("package %q: import count %d exceeds payload size", path, importCount)
		}
		pkgImports := make([]*types.Package, 0, importCount)
		closure := map[*types.Package]bool{types.Unsafe: true}
		previous := ""
		for i := range importCount {
			importPath, err := readString(r)
			if err != nil {
				return nil, nil, fmt.Errorf("package %q: read import: %w", path, err)
			}
			if i > 0 && importPath <= previous {
				return nil, nil, fmt.Errorf("package %q: imports are not strictly sorted at %q", path, importPath)
			}
			previous = importPath
			imported := imports[importPath]
			if imported == nil {
				return nil, nil, fmt.Errorf("package %q imports %q, which is not encoded before it", path, importPath)
			}
			pkgImports = append(pkgImports, imported)
			closure[imported] = true
			for dep := range reachable[importPath] {
				closure[dep] = true
			}
		}
		var size uint64
		if err := binary.Read(r, binary.BigEndian, &size); err != nil {
			return nil, nil, fmt.Errorf("package %q: read export data size: %w", path, err)
		}
		if size > uint64(r.Len()) {
			return nil, nil, fmt.Errorf("package %q: export data length exceeds payload size", path)
		}
		exportData := make([]byte, size)
		if _, err := io.ReadFull(r, exportData); err != nil {
			return nil, nil, fmt.Errorf("package %q: read export data: %w", path, err)
		}
		pkg, err := gcexportdata.Read(bytes.NewReader(exportData), fset, imports, path)
		if err != nil {
			return nil, nil, fmt.Errorf("package %q: %w", path, err)
		}
		if imports[path] != pkg {
			return nil, nil, fmt.Errorf("package %q: export data decoded a different package path", path)
		}
		// Read leaves Imports set to every package the export data refers to.
		// Each must be a transitive import: one decoded earlier is reused, and
		// one not decoded yet would have been materialized from this package's
		// partial view of it.
		for _, referenced := range pkg.Imports() {
			if !closure[referenced] {
				return nil, nil, fmt.Errorf("package %q export data refers to %q, which is not among its transitive imports", path, referenced.Path())
			}
		}
		delete(closure, types.Unsafe)
		reachable[path] = closure
		pkg.SetImports(pkgImports)
		pkgs = append(pkgs, pkg)
	}
	if r.Len() != 0 {
		return nil, nil, fmt.Errorf("package payload has %d trailing bytes", r.Len())
	}
	return pkgs, imports, nil
}

func readUint32(r *bytes.Reader) (uint32, error) {
	var v uint32
	err := binary.Read(r, binary.BigEndian, &v)
	return v, err
}

func readString(r *bytes.Reader) (string, error) {
	n, err := readUint32(r)
	if err != nil {
		return "", err
	}
	if uint64(n) > uint64(r.Len()) {
		return "", fmt.Errorf("string length %d exceeds payload size", n)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	return string(b), nil
}
