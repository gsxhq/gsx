# LSP `documentSymbol` + `workspaceSymbol`

**Date:** 2026-07-07
**Status:** Design approved, ready for planning.

## Goal

Add `textDocument/documentSymbol` and `workspace/symbol` to the gsx language
server so editors can populate their Outline view (per-file) and the
"Go to Symbol in Workspace" picker (module-wide, e.g. VS Code's `Ctrl+T`).

gopls never sees inside `.gsx` files, so gsx-lsp is the *only* symbol provider
for them — including the top-level Go declarations embedded in `.gsx` source.

## Scope

A "symbol" in a `.gsx` file is:

1. **Component declarations** — `component [recv] Name(...) { ... }`.
2. **Top-level Go declarations inside `GoChunk`s** — `func`, `type`, `const`,
   `var` in the verbatim Go source copied through a `.gsx` file.

Imports are not surfaced as symbols. `GoWithElements` (a top-level Go chunk with
embedded markup, e.g. `var x = <div/>`) is *not* mined for Go declarations — see
Known Limitations.

`workspace/symbol` searches the whole module (every `.gsx` package under the
`go.mod`), mirroring how find-references already uses `AnalyzeModule`.

## Architecture

### Shared extractor (`internal/lsp/symbols.go`)

A single pure function is the heart of the feature; both handlers consume it, so
there is exactly one extraction implementation.

```go
type Symbol struct {
    Name      string
    Kind      int            // LSP SymbolKind
    Container string         // package name, or receiver type for methods
    NamePos   token.Position // selectionRange / workspace Location
    DeclStart token.Position // full-range start
    DeclEnd   token.Position // full-range end
}

// FileSymbols extracts the symbols declared in one parsed .gsx file. fset
// resolves gsx node positions (pkg.GSXFset, or the module-shared fset).
func FileSymbols(path string, file *gsxast.File, fset *token.FileSet) []Symbol
```

It walks `file.Decls`:

- **`*ast.Component`** → one symbol.
  - `Kind` = `Method` (6) if `Recv != ""`, else `Function` (12).
  - `NamePos` from `Component.NamePos`; `DeclStart/DeclEnd` from `Pos()/End()`.
  - `Container` = receiver type name (parsed from `Recv`) for methods, else the
    file's package name.

- **`*ast.GoChunk`** → parse `Src` with `go/parser` (source `"package p\n" + Src`,
  in `ParseComments` mode is unnecessary; plain mode). Walk top-level `Decls`:
  - `*ast.FuncDecl` → `Function`, or `Method` (6) with `Container` = receiver
    type if `Recv != nil`.
  - `*ast.GenDecl` with `token.TYPE` → per `TypeSpec`: `Struct` (23) for
    `*ast.StructType`, `Interface` (11) for `*ast.InterfaceType`, else `Class` (5).
  - `*ast.GenDecl` with `token.CONST` → `Constant` (14) per name.
  - `*ast.GenDecl` with `token.VAR` → `Variable` (13) per name.
  - `token.IMPORT` → skipped.
  - **Position mapping:** `Src` is a verbatim copy of the source span
    `[chunk.Pos(), chunk.End())` (confirmed in `parser/goexpr.go`: `GoChunk{Src: src}`
    with `SetSpan(gc, base, base+len(src))`), so offsets align 1:1. The wrapper
    prepends `len("package p\n")` bytes; subtract that constant to get the offset
    within `Src`, add `chunk.Pos()`'s file offset, then
    `fset.File(chunk.Pos()).Pos(globalOffset)` → `fset.Position(...)`.

- **`*ast.GoWithElements`** → skipped (see Known Limitations).

`FileSymbols` is exported. `gen` already imports `internal/lsp` (it returns
`lsp.CrossRef`), so the module walker calls `lsp.FileSymbols` directly — no
duplicate extraction, and no `*token.FileSet` crosses the `Analyzer` boundary.

### `textDocument/documentSymbol` (internal/lsp)

`handleDocumentSymbol`: resolve `dir := filepath.Dir(path)`, `pkg := s.pkgs[dir]`,
`file := pkg.Files[path]`; if any is nil, reply `[]`. Otherwise
`FileSymbols(path, file, pkg.GSXFset)` → `[]DocumentSymbol`.

Response uses the modern hierarchical `DocumentSymbol[]` form (flat list, since
gsx decls don't nest): `name`, `kind`, `range` = `[DeclStart, DeclEnd)`,
`selectionRange` = the name span `[NamePos, NamePos+len(Name))`. Ranges are
encoded against the open document text (the server has it in `s.docs`), reusing
`charForByteCol` / `lineAtFunc`.

Uses the already-warm analyzed package, so it is effectively instant and needs no
new analysis.

### `workspace/symbol` (internal/lsp + one Analyzer method)

New `Analyzer` method:

```go
// ModuleSymbols returns every symbol declared in every .gsx package in the
// module containing dir. override supplies unsaved buffers (abs path -> bytes).
ModuleSymbols(dir string, override map[string][]byte) ([]Symbol, error)
```

Implemented in `gen/lsp.go` with the same warm-Module pattern as `AnalyzeModule`:
`discoverDirs([]string{root})`, apply overrides, loop `m.Package(d)`, and for each
`PackageResult` call `lsp.FileSymbols(path, file, pr.GSXFset)` over `pr.GSXFiles`.
Un-analyzable dirs are skipped (partial results tolerated, matching
`AnalyzeModule`).

`handleWorkspaceSymbol`:
- Build (or reuse cached) the module symbol list.
- Filter by the request `query`: **case-insensitive substring** match on `Name`
  (a real, standard filter — not fuzzy magic). Empty query returns all.
- Reply `[]SymbolInformation`: `name`, `kind`, `containerName` = `Container`,
  `location` = `{uri, range}` where range is the name span (encoded against the
  target file's on-disk/override text via `locationForPos`-style logic).

**Caching:** add `moduleSyms []Symbol` / `moduleSymsValid bool` to `Server`,
alongside `moduleRefs`. The existing `invalidateModuleRefs()` (called on every
document mutation) also clears the symbol cache — rename to
`invalidateModuleCaches()` or extend it. The symbol picker fires `workspace/symbol`
on every keystroke, so the list is built once per edit-epoch and filtered
in-memory.

### Capabilities & dispatch

`serverCapabilities` gains:

```go
DocumentSymbolProvider  bool `json:"documentSymbolProvider"`
WorkspaceSymbolProvider bool `json:"workspaceSymbolProvider"`
```

both set `true` in `handleInitialize`. `handle()` dispatches
`textDocument/documentSymbol` → `handleDocumentSymbol` and `workspace/symbol` →
`handleWorkspaceSymbol`.

### Protocol types (`internal/lsp/protocol.go`)

Add wire structs: `documentSymbolParams` (`{textDocument}`), `DocumentSymbol`
(`name`, `kind`, `range`, `selectionRange`, plus optional `detail`/`children`
omitted), `workspaceSymbolParams` (`{query}`), and `SymbolInformation`
(`name`, `kind`, `containerName`, `location`).

## SymbolKind mapping (LSP numeric constants)

| gsx / Go decl              | Kind          | Value |
|----------------------------|---------------|-------|
| Component (no receiver)    | Function      | 12    |
| Component (with receiver)  | Method        | 6     |
| Go `func`                  | Function      | 12    |
| Go method (`func (r T)`)   | Method        | 6     |
| Go `type … struct`         | Struct        | 23    |
| Go `type … interface`      | Interface     | 11    |
| Go `type …` (other)        | Class         | 5     |
| Go `const`                 | Constant      | 14    |
| Go `var`                   | Variable      | 13    |

Define these as named constants in `symbols.go`.

## Testing

- **`internal/lsp/documentsymbol_test.go`** — via the existing fake-`Analyzer`
  test harness. Cases: a file with plain + method components; a `GoChunk` with
  one of each Go decl kind (func, method, struct, interface, type-alias, const,
  var); verify `kind`, `selectionRange` on the name, `range` on the whole decl;
  a `.gsx` with no symbols → `[]`; unknown/unopened URI → `[]`.
- **`internal/lsp/workspacesymbol_test.go`** — multi-file fake analyzer: query
  filtering (substring, case-insensitive, empty=all), `containerName`, `location`
  URI/range correctness, cache reuse across two requests without an intervening
  edit, cache invalidation after a `didChange`.
- **`gen/lsp_test.go`** — a `ModuleSymbols` case against the *real* analyzer with
  a multi-package module fixture: components across packages, a `GoChunk` decl,
  and an override buffer reflected in the results.

## Non-goals / Known Limitations

- **`GoWithElements` top-level decls** (e.g. `var x = <div/>` at file scope) are
  not mined for Go declarations — the interleaved `GoText` parts aren't
  standalone-parseable, and reconstructing valid Go by substituting placeholders
  for elements would be a heuristic. Rare at top level; documented, deferred.
  Component decls and plain `GoChunk` decls (the common cases) are fully covered.
- **No fuzzy matching** for `workspace/symbol` — substring only. gopls does
  fuzzy; substring is honest and sufficient. Could be revisited.
- **`ModuleSymbols` type-checks via `m.Package`** (warm-cached) rather than a
  parse-only walk. A parse-only fast path (symbols need no type info) is a
  possible later optimization; reusing the warm Module keeps this correct and
  simple.

## Not a syntax change

This adds LSP capabilities only — no grammar, codegen, or rendering change.
Therefore: no txtar corpus case, and no sibling-repo updates
(`tree-sitter-gsx`, `vscode-gsx`, docs syntax). VS Code's Outline and
"Go to Symbol in Workspace" light up automatically once the server advertises
`documentSymbolProvider` / `workspaceSymbolProvider`. `make ci` (build/vet/test)
is the gate.
