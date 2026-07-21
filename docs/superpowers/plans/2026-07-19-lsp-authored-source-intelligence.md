# LSP Authored-Source Intelligence Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make go-to-definition, hover, document symbols, and workspace symbols cover every semantically valid authored `.gsx` region without reading, writing, or depending on a physical generated `.x.go` file.

**Architecture:** Codegen will attach an exact byte-for-byte source map to each in-memory skeleton and harvest a retained immutable semantic index from `go/types`. Existing GSX-specific facts remain authoritative; definition, hover, and symbols use the new index only as their shared fallback. Workspace state will follow initialized LSP roots and Go workspace/module ownership, while every returned range will be encoded from the authoritative open buffer before disk.

**Tech Stack:** Go 1.26.1, `go/ast`, `go/parser`, `go/token`, `go/types`, `golang.org/x/mod/modfile`, existing GSX parser/codegen/LSP packages, txtar corpus, standard `testing`, gopls checks, Make CI/lint gates.

## Global Constraints

- A physical sibling `.x.go` is never semantic input. Tests must pass when it is absent, invalid, stale, or contains conflicting declarations, and analysis must not modify it.
- A virtual in-memory filename may end in `.x.go`; only its in-memory bytes and `token.File` may be consumed.
- Every reverse mapping is backed by an exact byte-identical authored segment. Generated glue has no read capability.
- Existing exact GSX facts retain precedence for component calls, attributes, component-family declarations, signature spans, expression probes, and control-flow probes.
- Do not add identifier-spelling, nearest-line-directive, reconstructed-signature, filename-suffix, or directory-guessing fallbacks.
- Do not implement or advertise `textDocument/completion` in this plan. The source map exposes a completion capability bit only so the later completion design does not require a mapping ABI rewrite.
- Keep the root runtime package dependency-free. New source-intelligence code belongs under `internal/` and may use only dependencies already permitted for tooling.
- Do not retain a second Go AST or a second copy of source buffers after index construction.
- Use the branch worktree at `/Users/jackieli/personal/gsxhq/gsx/.worktrees/post-migration-optimisation-audit`.
- Before each commit, run `gofmt` on changed Go files and `gopls check -severity=hint` on them. Before handoff, run `make check`, `make lint`, and the authoritative `make ci`.

## File and Responsibility Map

| Path | Responsibility after this change |
| --- | --- |
| `internal/sourceintel/source_map.go` | Immutable exact generated-to-authored byte segments and validated declaration regions. |
| `internal/sourceintel/index.go` | Immutable occurrences, definitions, hover values, and raw-Go declarations harvested from `types.Info`. |
| `internal/codegen/skeleton_source.go` | Codegen-only mapped skeleton writer; verifies authored byte identity while emitting. |
| `internal/codegen/analyze.go` | Shared skeleton construction; classifies canonical authored copies and generated glue. |
| `internal/codegen/module_importer.go` | Builds the source map/index from in-memory skeleton ASTs and retains it in package analysis. |
| `internal/codegen/component_target_package.go` | Keeps the existing exact exclusion of each `.gsx` file's paired physical generated output from companion Go input. |
| `internal/codegen/results.go` | Publishes the immutable semantic index in `PackageResult`. |
| `internal/lsp/analysis.go` | Retains the shared semantic index in `lsp.Package`. |
| `gen/lsp.go` | Adapts codegen results and enumerates per-module symbols without a second analysis model. |
| `internal/lsp/definition.go` | Preserves specialized definition precedence, then queries the semantic index. |
| `internal/lsp/hover.go` | Preserves specialized hover precedence, then formats indexed object/expression types. |
| `internal/lsp/symbols.go` | Merges component symbols with indexed raw-Go declarations and partial-parser recovery. |
| `internal/lsp/source_text.go` | Central open-buffer-first byte/range conversion used by definition, references, hover, and symbol handlers. |
| `internal/lsp/workspace.go` | Initialized roots, `go.work`/`go.mod` module ownership, and workspace-folder lifecycle. |
| `internal/lsp/workspacesymbol.go` | Per-module aggregation, cache ownership, filtering, and deterministic ordering. |
| `internal/lsp/protocol.go` | Workspace-folder protocol types and initialization/change parameters. |
| `internal/lsp/server.go` | Stores root/module state and dispatches workspace-folder notifications. |
| `docs/ROADMAP.md` | Replaces stale LSP gap claims with verified status and leaves completion as follow-up. |

---

### Task 1: Add the exact source-map value model

**Files:**
- Create: `internal/sourceintel/source_map.go`
- Create: `internal/sourceintel/source_map_test.go`

**Interfaces:**
- Consumes: generated byte offsets, one authoritative source path/size, and byte-identical segments produced by codegen.
- Produces:
  ```go
  package sourceintel

  type Capability uint8

  const (
      Definition Capability = 1 << iota
      Hover
      Symbol
      Completion
  )

  type Span struct {
      Path  string
      Start int
      End   int
  }

  type Segment struct {
      Source         Span
      GeneratedStart int
      GeneratedEnd   int
      Capabilities   Capability
  }

  type DeclarationRegion struct {
      Source         Span
      GeneratedStart int
      GeneratedEnd   int
  }

  type SourceMap struct {
      generatedSize int
      sourceSize    int
      sourcePath    string
      segments      []Segment
      regions       []DeclarationRegion
  }

  func NewSourceMap(generatedSize, sourceSize int, sourcePath string, segments []Segment, regions []DeclarationRegion) (*SourceMap, error)
  func (m *SourceMap) SourceSpan(generatedStart, generatedEnd int, capability Capability) (Span, bool)
  func (m *SourceMap) DeclarationSpan(generatedStart, generatedEnd int) (Span, bool)
  ```

- [ ] Write `TestNewSourceMapRejectsInvalidSegments` covering negative/out-of-bounds endpoints, unequal source/generated lengths, wrong source paths, unsorted segments, generated overlap, zero capabilities, and invalid declaration regions. Run `go test ./internal/sourceintel -run TestNewSourceMapRejectsInvalidSegments -count=1`; expect a compile failure because the package/API does not exist.

- [ ] Implement the types and constructor validation. Copy the input slices, sort neither one, and reject unsorted input so callers cannot hide emission-order bugs. Keep all fields private after construction.

- [ ] Write `TestSourceSpanRequiresExactCapabilityCoverage` with ASCII and multibyte UTF-8 bytes, adjacent segments, a generated gap, repeated source spans, line-edge/mid-expression spans, and mixed capability masks. Assert half-open edge behavior and require the entire requested range to be covered by contiguous segments with the requested capability. Run the test; expect failure until lookup exists.

- [ ] Implement `SourceSpan` with binary search on generated starts plus a bounded adjacent-segment walk. It may merge only source-contiguous segments from the same path; it must reject generated gaps, source discontinuities, and missing capability bits.

- [ ] Write `TestDeclarationSpanRequiresOwnedRegionEndpoints` proving that a declaration may span generated glue only when one explicit `DeclarationRegion` contains it and both generated endpoints map to that region's source endpoints. Include a rejected range crossing a GoWithElements IIFE without an owning region. Implement `DeclarationSpan` without name or line matching.

- [ ] Run `gofmt -w internal/sourceintel/*.go`, `gopls check -severity=hint internal/sourceintel/source_map.go internal/sourceintel/source_map_test.go`, and `go test ./internal/sourceintel -count=1`; expect all green.

- [ ] Commit: `git add internal/sourceintel && git commit -m 'feat(lsp): add exact authored source maps'`

### Task 2: Emit mapped in-memory skeletons

**Files:**
- Create: `internal/codegen/skeleton_source.go`
- Create: `internal/codegen/skeleton_source_test.go`
- Modify: `internal/codegen/analyze.go`
- Modify: `internal/codegen/renderer_decls.go`
- Modify: `internal/codegen/module_importer.go`

**Interfaces:**
- Consumes: the existing parsed `.gsx` AST, `parsed.sources[path]`, its `token.File`, and every authored substring copied into the in-memory skeleton.
- Produces:
  ```go
  type skeletonBuild struct {
      source             string
      sourceHash         [sha256.Size]byte
      components         []*gsxast.Component
      imports            []importSpec
      ctrlStarts         map[gsxast.Node]int
      markupGroups       [][]gsxast.Markup
      sourceMap          *sourceintel.SourceMap
  }

  type skeletonSourceWriter struct {
      sourcePath string
      source     []byte
      builder    strings.Builder
      segments   []sourceintel.Segment
      regions    []sourceintel.DeclarationRegion
      enabled    bool
      err        error
  }

  func newSkeletonSourceWriter(path string, source []byte) *skeletonSourceWriter
  func newUnmappedSkeletonSourceWriter() *skeletonSourceWriter
  func (w *skeletonSourceWriter) writeGenerated(text string)
  func (w *skeletonSourceWriter) writeAuthored(start, end int, emitted string, capabilities sourceintel.Capability) error
  func (w *skeletonSourceWriter) addDeclarationRegion(source sourceintel.Span, generatedStart, generatedEnd int) error
  func (w *skeletonSourceWriter) appendMapped(child *skeletonSourceWriter) error
  func (w *skeletonSourceWriter) finish() (string, *sourceintel.SourceMap, error)
  func buildSkeletonResult(file *gsxast.File, table funcTables, fset *token.FileSet, bag *diag.Bag, plan *componentTargetPlan, mode skeletonMode, recorder *skeletonSourceWriter) (skeletonBuild, error)
  func buildMappedSkeleton(file *gsxast.File, table funcTables, fset *token.FileSet, bag *diag.Bag, plan *componentTargetPlan, mode skeletonMode, sourcePath string, source []byte) (skeletonBuild, error)
  ```
- `buildSkeleton` keeps its current signature as a generation/test compatibility wrapper. Full analysis calls the exact `buildMappedSkeleton` signature above.

- [ ] Write `TestSkeletonSourceWriterRejectsNonIdenticalAuthoredBytes`, `TestSkeletonSourceWriterMapsExactWrites`, and `TestSkeletonSourceWriterRebasesMappedChild`. The child test represents the current component buffer appended after the file prelude and asserts all child segments/regions shift by the exact parent byte length. Run `go test ./internal/codegen -run 'TestSkeletonSourceWriter' -count=1`; expect a compile failure.

- [ ] Implement `skeletonSourceWriter`. `writeAuthored` must compare `source[start:end]` with `emitted` before appending when enabled; the unmapped writer appends the same emitted bytes without allocating segment/region storage. `writeGenerated` appends without a segment. `appendMapped` appends the child bytes once and rebases copied segments/regions by the parent's previous length. `finish` constructs the immutable map and returns an error rather than weakening a mapping; `buildMappedSkeleton` records `sha256.Sum256(source)` in `skeletonBuild.sourceHash`.

- [ ] Refactor the existing `buildSkeleton` body into `buildSkeletonResult`, keeping the old return signature as a thin wrapper that supplies `newUnmappedSkeletonSourceWriter()`. Add `buildMappedSkeleton` and pass `parsed.sources[path]` from the full-analysis loop in `module_importer.go`; generation-only and declaration-only callers retain no source map.

- [ ] Add table-driven `TestBuildMappedSkeletonAuthoredRegions` cases for top-level `GoChunk`, component receiver/name/type parameters/parameters, explicit invocation type arguments, ordinary expressions, control clauses, pipeline expressions, embedded literal holes, and nested markup in Go expressions. Each case must assert the exact source bytes returned for Definition and Hover capabilities; canonical native copies also receive Completion, while only raw top-level Go declaration source receives Symbol. Run the focused test; expect failures for every region not yet classified.

- [ ] Thread the writer through `emitComponentSkeleton`, `emitNamedComponentSkeleton`, `emitProbes`, and their recursive call sites. Mark only the canonical native semantic copy read-capable; validation/quiet duplicate probes stay generated. For formatted signature output, record the receiver, public declaration name, type-parameter content, parameter content, and explicit type-argument content as separate exact segments.

- [ ] Add `TestBuildMappedSkeletonGoWithElements` with a top-level declaration whose Go text occurs before, between, and after two markup elements. Record every exact `GoText` subsegment, leave generated element IIFEs unmapped, and add one explicit declaration region from the owning `GoWithElements` source span. If parenthesis stripping transforms a part, map only unchanged subspans.

- [ ] Add `TestBuildMappedSkeletonRepeatedProbeHasOneReadableCopy`, count segments for a source expression emitted into multiple probes, and assert exactly one copy has Definition/Hover. Fix canonical-copy selection at the emitter branch that owns the semantic expression.

- [ ] Run `gofmt -w internal/codegen/analyze.go internal/codegen/renderer_decls.go internal/codegen/module_importer.go internal/codegen/skeleton_source.go internal/codegen/skeleton_source_test.go`, then `gopls check -severity=hint` on those files, `go test ./internal/codegen -run 'TestSkeletonSourceWriter|TestBuildMappedSkeleton' -count=1`, and `go test ./internal/codegen -count=1`.

- [ ] Commit: `git add internal/codegen internal/sourceintel && git commit -m 'feat(lsp): map authored skeleton bytes'`

### Task 3: Harvest an immutable semantic index from `types.Info`

**Files:**
- Create: `internal/sourceintel/index.go`
- Create: `internal/sourceintel/index_test.go`

**Interfaces:**
- Consumes:
  ```go
  type MappedFile struct {
      AST           *ast.File
      TokenFile     *token.File
      SourceMap     *SourceMap
      SourceVersion SourceVersion
  }
  ```
  plus the package `*types.Info` containing `Defs`, `Uses`, and `Types`.
- Produces:
  ```go
  type OccurrenceKind uint8
  const (
      IdentifierDefinition OccurrenceKind = iota
      IdentifierUse
      Expression
  )

  type Occurrence struct {
      Span         Span
      Kind         OccurrenceKind
      Object       types.Object
      TypeAndValue types.TypeAndValue
      HasTypeValue bool
  }

  type DeclarationKind uint8
  const (
      DeclarationFunction DeclarationKind = iota
      DeclarationMethod
      DeclarationType
      DeclarationStruct
      DeclarationInterface
      DeclarationConstant
      DeclarationVariable
  )

  type Declaration struct {
      Name      string
      Kind      DeclarationKind
      Container string
      NameSpan  Span
      DeclSpan  Span
      Object    types.Object
  }

  type SourceVersion struct {
      Size   int
      SHA256 [sha256.Size]byte
  }

  type Index struct {
      occurrences  map[string][]Occurrence
      definitions  map[types.Object]Span
      declarations map[string][]Declaration
      sources      map[string]SourceVersion
  }

  func BuildIndex(info *types.Info, files []MappedFile) *Index
  func Origin(object types.Object) types.Object
  func (i *Index) At(path string, offset int) (Occurrence, bool)
  func (i *Index) Definition(object types.Object) (Span, bool)
  func (i *Index) Declarations(path string) []Declaration
  func (i *Index) MatchesSource(path string, source []byte) bool
  ```

- [ ] Write `TestBuildIndexMapsDefinitionsUsesAndExpressions` using an in-memory parsed/type-checked Go file and a hand-built exact source map. Assert declaration/use object identity, nested-expression preference for the smallest mapped expression, and no occurrence for generated glue. Run `go test ./internal/sourceintel -run TestBuildIndexMapsDefinitionsUsesAndExpressions -count=1`; expect a compile failure.

- [ ] Implement occurrence harvesting from `types.Info.Defs`, `Uses`, and `Types`. Copy each `MappedFile.SourceVersion` into the index. Identifier definitions/uses require both Definition and Hover mapping; non-identifier `TypeAndValue` expressions require Hover mapping. Translate an AST range only through `SourceSpan`; ignore incomplete mappings. Sort per path by start, then identifiers before expressions, then shorter ranges. Implement `At` with binary search and a bounded overlap scan.

- [ ] Write `TestBuildIndexDefinitionsUseOriginIdentity` with instantiated generic function and variable objects and assert `Definition` resolves each concrete object and `Origin(object)` to the same mapped declaration. Implement `Origin` as a type switch calling `(*types.Func).Origin()` and `(*types.Var).Origin()` and returning other object kinds unchanged.

- [ ] Write `TestBuildIndexDeclarations` for function, method, type alias, named struct, named interface, grouped constants, grouped variables, and a declaration region spanning unmapped generated glue. Assert exact name/full ranges, receiver container names, and deterministic source order.

- [ ] Implement declaration harvesting from real `ast.Decl` nodes only. Require Symbol capability on mapped names plus either complete Symbol coverage or a validated `DeclarationRegion`; do not infer declarations from `types.Scope` names. Return defensive slice copies from `Declarations`.

- [ ] Write `TestIndexDoesNotRetainASTOrSourceBytes` in package `sourceintel`: construct the index and inspect every concrete private field with reflection. Permit only `map[string][]Occurrence`, `map[types.Object]Span`, `map[string][]Declaration`, and `map[string]SourceVersion`; reject `*ast.File`, AST nodes, `*token.File`, byte slices, and `*SourceMap` at every nested field.

- [ ] Write `TestIndexMatchesExactSourceVersion` with same-length byte changes and assert only the exact SHA-256/size pair matches. Implement `MatchesSource`; handlers use this guard instead of applying retained offsets to a newer editor snapshot.

- [ ] Run `gofmt -w internal/sourceintel/*.go`, `gopls check -severity=hint internal/sourceintel/index.go internal/sourceintel/index_test.go`, and `go test ./internal/sourceintel -count=1`.

- [ ] Commit: `git add internal/sourceintel && git commit -m 'feat(lsp): index authored Go semantics'`

### Task 4: Retain the semantic index through package analysis

**Files:**
- Modify: `internal/codegen/module_importer.go`
- Modify: `internal/codegen/module.go`
- Modify: `internal/codegen/results.go`
- Modify: `internal/codegen/module_test.go`
- Modify: `internal/lsp/analysis.go`
- Modify: `gen/lsp.go`
- Modify: `gen/lsp_test.go`

**Interfaces:**
- `internal/codegen.PackageResult` and `internal/lsp.Package` each gain `SourceIndex *sourceintel.Index`.
- `adaptPackageResult(pr *codegen.PackageResult) *lsp.Package` assigns the immutable pointer directly; it does not translate or duplicate the index.
- The index is owned by the existing analyzed-package snapshot and invalidated with it.

- [ ] Add `TestModulePackageResultRetainsSourceIndex` to a temp module containing one `.gsx` `GoChunk`, one component signature, and one explicit generic type argument. Analyze without generating a sibling `.x.go`; expect the new field to be absent at compile time.

- [ ] In the full analysis path, retain each mapped skeleton AST/token file until type checking completes, call `sourceintel.BuildIndex` once with the final `types.Info`, then drop the mapped-file inputs. Populate `PackageResult.SourceIndex`; do not build an index for generation-only/declaration-only modes.

- [ ] Extend `gen/lsp_test.go` to assert `adaptPackageResult` preserves pointer identity for `SourceIndex`. Add the field to `lsp.Package` and adapter.

- [ ] Add `TestModuleSourceIndexInvalidatesWithEditedPackage` using the warm Module override lifecycle. Change a helper's type in an unsaved `.gsx` buffer and assert a subsequent package result has a different index pointer and updated occurrence type, while the superseded result remains immutable.

- [ ] Run `gofmt -w internal/codegen/module_importer.go internal/codegen/module.go internal/codegen/results.go internal/codegen/module_test.go internal/lsp/analysis.go gen/lsp.go gen/lsp_test.go`, `gopls check -severity=hint` on those files, `go test ./internal/codegen ./gen -run 'SourceIndex|adaptPackageResult' -count=1`, then `go test ./internal/codegen ./gen -count=1`.

- [ ] Commit: `git add internal/codegen internal/lsp/analysis.go gen && git commit -m 'feat(lsp): retain authored semantic index'`

### Task 5: Complete go-to-definition with semantic fallback

**Files:**
- Modify: `internal/lsp/definition.go`
- Create: `internal/lsp/definition_source_index_test.go`
- Create: `gen/lsp_definition_e2e_test.go`

**Interfaces:**
- Consumes: `pkg.SourceIndex.At(path, byteOffset)` after all existing specialized GSX definition paths return no answer and only when `pkg.SourceIndex.MatchesSource(path, authoritativeBytes)` succeeds.
- Produces: one `Location` for an indexed use/definition. If `SourceIndex.Definition(object)` succeeds, target `.gsx`; otherwise use `pkg.Fset.Position(sourceintel.Origin(object).Pos())` for a real `.go` declaration.
- Precedence remains: component/import/attr/specialized expression/control/signature facts first, semantic index second, null last.
  ```go
  type semanticDefinitionTarget struct {
      Authored sourceintel.Span
      Go       token.Position
  }

  func semanticDefinition(pkg *Package, path string, source []byte, offset int) (semanticDefinitionTarget, bool)
  ```

- [ ] Add table-driven unit cases for a component declaration name, receiver variable/type, type-parameter name/constraint, parameter declaration/use/type, top-level helper signature/body local/selector/return type/import qualifier, same- and cross-package explicit component type arguments, and Go text inside/around nested markup. Include declaration-site definition-to-self and generated-glue-to-null rows. Run `go test ./internal/lsp -run TestDefinitionSourceIndex -count=1`; expect null results.

- [ ] Implement `semanticDefinition` with the exact signature above and call it only after current exact GSX routes. Set `Authored` for mapped `.gsx` declarations and `Go` for real `.go` objects; never collapse an exact authored span into a generated token position. Canonicalize generic function/variable objects with `sourceintel.Origin`; never search by object name.

- [ ] Add `TestDefinitionSpecializedFactsPrecedeSourceIndex` with deliberately conflicting test doubles for a component call and signature type. Assert the existing exact target wins.

- [ ] Add an end-to-end temp-module test through production `lspAnalyzer` that sends `textDocument/definition` for all five gap families, with no physical `.x.go`. Assert exact `.gsx` or dependency `.go` URIs and ranges.

- [ ] Run `gofmt -w internal/lsp/definition.go internal/lsp/definition_source_index_test.go gen/lsp_definition_e2e_test.go`, `gopls check -severity=hint` on them, `go test ./internal/lsp ./gen -run 'DefinitionSourceIndex|DefinitionSpecializedFacts|DefinitionAuthored' -count=1`, then `go test ./internal/lsp ./gen -count=1`.

- [ ] Commit: `git add internal/lsp gen && git commit -m 'feat(lsp): complete definition over authored source'`

### Task 6: Complete hover with the same semantic answer

**Files:**
- Modify: `internal/lsp/hover.go`
- Create: `internal/lsp/hover_source_index_test.go`
- Create: `gen/lsp_hover_e2e_test.go`

**Interfaces:**
- Consumes: the same indexed occurrence chosen by definition, guarded by `MatchesSource` against the authoritative buffer.
- Produces: Markdown/plaintext hover using the existing `types.TypeString` qualifier and the current object-kind formatting conventions. Identifier occurrences use `Occurrence.Object`; expressions use `Occurrence.TypeAndValue` only when `HasTypeValue` is true.
- Existing component/attribute/signature hover facts retain precedence.
  ```go
  func semanticHover(pkg *Package, path string, source []byte, offset int) (Hover, bool)
  ```

- [ ] Add table-driven unit cases matching Task 5 plus whole-expression hover for a top-level `d.Hours()` call. Assert object kind/name/type, constraint/type-argument presentation, expression type, and generated-glue-to-null behavior. Run `go test ./internal/lsp -run TestHoverSourceIndex -count=1`; expect null results.

- [ ] Implement `semanticHover` with the exact signature above. Reuse the current package qualifier/markdown builders; do not construct declarations by slicing generated text.

- [ ] Add `TestHoverSpecializedFactsPrecedeSourceIndex` for a component invocation and bound attribute, asserting the existing callable/parameter hover wins over an injected indexed object.

- [ ] Add the production end-to-end hover matrix with no physical `.x.go`, including external types, local variables, method selections, component/parameter declarations, type arguments, and GoWithElements text.

- [ ] Run `gofmt -w internal/lsp/hover.go internal/lsp/hover_source_index_test.go gen/lsp_hover_e2e_test.go`, `gopls check -severity=hint` on them, `go test ./internal/lsp ./gen -run 'HoverSourceIndex|HoverSpecializedFacts|HoverAuthored' -count=1`, then `go test ./internal/lsp ./gen -count=1`.

- [ ] Commit: `git add internal/lsp gen && git commit -m 'feat(lsp): complete hover over authored source'`

### Task 7: Rebuild document symbols from semantic declarations

**Files:**
- Modify: `internal/lsp/symbols.go`
- Modify: `internal/lsp/documentsymbol.go`
- Modify: `internal/lsp/documentsymbol_test.go`
- Create: `internal/lsp/symbols_partial_test.go`
- Modify: `gen/lsp.go`
- Create: `gen/lsp_symbols_e2e_test.go`

**Interfaces:**
- Consumes: GSX component AST symbols plus `pkg.SourceIndex.Declarations(path)` when `MatchesSource` succeeds.
- Produces existing `lsp.Symbol` values with exact `NamePos`, `Start`, `End`, `Kind`, and method `Container`. Component symbols remain AST-owned; generated component skeleton declarations have no Symbol capability and cannot duplicate them.
- Error recovery calls `go/parser.ParseFile` with `parser.AllErrors|parser.SkipObjectResolution` and emits only declarations present in the returned partial AST.

- [ ] Extend current symbol tests with a matrix for functions, methods, types, structs, interfaces, grouped vars/consts, components, GoWithElements declarations, and duplicate-prevention. First run `go test ./internal/lsp -run 'TestDocumentSymbols|TestSemanticSymbols' -count=1`; expect GoWithElements and some declaration kinds to fail.

- [ ] Add an adapter from `sourceintel.DeclarationKind` to LSP `SymbolKind`, preserving receiver/type container names. Merge component and semantic declarations, then stable-sort by source start, tighter range, kind, and name.

- [ ] Remove the successful-analysis path that reparses each `GoChunk`. Keep a focused partial-parser fallback when no index exists or `MatchesSource` rejects the current buffer. Never merge declarations from a mismatched retained snapshot with current-buffer ranges.

- [ ] Add `TestPartialGoSymbolsKeepRecoveredDeclarations`: feed one chunk with valid declarations before and after a syntax error, run `parser.AllErrors|parser.SkipObjectResolution`, and assert only AST-returned declarations appear. Add an invalid fragment that resembles a declaration textually and assert no fabricated symbol appears.

- [ ] Update production `ModuleSymbols` aggregation in `gen/lsp.go` to consume analyzed package symbols, including `GoWithElements`, rather than maintaining a divergent raw parser route.

- [ ] Add an end-to-end document-symbol test over an open unsaved buffer and assert exact UTF-16 ranges, containers, source order, and no component duplicates.

- [ ] Run `gofmt -w internal/lsp/symbols.go internal/lsp/documentsymbol.go internal/lsp/documentsymbol_test.go internal/lsp/symbols_partial_test.go gen/lsp.go gen/lsp_symbols_e2e_test.go`, `gopls check -severity=hint` on them, `go test ./internal/lsp ./gen -run 'Symbol' -count=1`, then `go test ./internal/lsp ./gen -count=1`.

- [ ] Commit: `git add internal/lsp gen && git commit -m 'feat(lsp): complete authored document symbols'`

### Task 8: Centralize authoritative source/range conversion

**Files:**
- Create: `internal/lsp/source_text.go`
- Create: `internal/lsp/source_text_test.go`
- Modify: `internal/lsp/definition.go`
- Modify: `internal/lsp/hover.go`
- Modify: `internal/lsp/references.go`
- Modify: `internal/lsp/documentsymbol.go`
- Modify: `internal/lsp/workspacesymbol.go`

**Interfaces:**
- Produces:
  ```go
  func (s *Server) sourceText(path string) ([]byte, bool)
  func (s *Server) position(path string, offset int) (Position, bool)
  func (s *Server) rangeForSpan(span sourceintel.Span) (Range, bool)
  func (s *Server) locationForSpan(span sourceintel.Span) (Location, bool)
  ```
- `sourceText` returns the open `docStore` snapshot first, otherwise saved disk bytes. All definition/reference/hover/symbol ranges route through these helpers.

- [ ] Add `TestSourceTextPrefersOpenDocument` with saved ASCII bytes and an unsaved UTF-8 prefix that changes UTF-16 columns. Assert the returned range matches the open buffer. Run `go test ./internal/lsp -run TestSourceTextPrefersOpenDocument -count=1`; expect a compile failure.

- [ ] Implement the source accessor and offset-to-LSP conversion using the server's negotiated encoding. Fail closed when a span is outside the authoritative snapshot; never silently clamp.

- [ ] Replace `locationForPos`, `locationForNameSpan`, reference-location conversion, and handler-local disk reads with span-based helpers where exact spans exist. Keep a token-position adapter only for real `.go` dependency targets, and have it call `sourceText` before disk, then preserve the existing byte-column fallback only when those source bytes are unavailable.

- [ ] Add regression tests for definition, references, hover range, document symbols, and workspace symbols against an unsaved buffer containing astral Unicode before the target. Assert identical encoding rules across all five handlers.

- [ ] Run `gofmt -w internal/lsp/source_text.go internal/lsp/source_text_test.go internal/lsp/definition.go internal/lsp/hover.go internal/lsp/references.go internal/lsp/documentsymbol.go internal/lsp/workspacesymbol.go`, `gopls check -severity=hint` on them, and `go test ./internal/lsp -run 'SourceText|Unsaved|UTF16|Definition|References|Hover|Symbol' -count=1`.

- [ ] Commit: `git add internal/lsp && git commit -m 'fix(lsp): use authoritative buffers for locations'`

### Task 9: Model initialized workspace roots and Go module ownership

**Files:**
- Modify: `internal/lsp/protocol.go`
- Modify: `internal/lsp/server.go`
- Create: `internal/lsp/workspace.go`
- Create: `internal/lsp/workspace_test.go`
- Modify: `internal/lsp/server_lifecycle_test.go`

**Interfaces:**
- Protocol additions:
  ```go
  type workspaceFolder struct {
      URI  string `json:"uri"`
      Name string `json:"name"`
  }

  type initializeParams struct {
      Capabilities     clientCapabilities `json:"capabilities"`
      RootURI          string             `json:"rootUri,omitempty"`
      WorkspaceFolders []workspaceFolder  `json:"workspaceFolders,omitempty"`
  }

  type didChangeWorkspaceFoldersParams struct {
      Event workspaceFoldersChangeEvent `json:"event"`
  }

  type workspaceFoldersChangeEvent struct {
      Added   []workspaceFolder `json:"added"`
      Removed []workspaceFolder `json:"removed"`
  }
  ```
- Server helpers:
  ```go
  func discoverWorkspaceModules(roots []string) ([]string, error)
  func (s *Server) setWorkspaceFolders(folders []workspaceFolder) error
  func (s *Server) changeWorkspaceFolders(added, removed []workspaceFolder) error
  ```
- Discovery rule: a root `go.work` is parsed with `modfile.ParseWork` and its explicit `use` directories become modules; otherwise the nearest `go.mod` at/above the root owns it. Do not recursively guess nested modules.

- [ ] Add table-driven tests for one module root, a subdirectory root, a `go.work` with two relative `use` entries, duplicate/canonical paths, a nonexistent root, and a nested unlisted `go.mod`. Run `go test ./internal/lsp -run TestDiscoverWorkspaceModules -count=1`; expect a compile failure.

- [ ] Implement URI-to-clean-absolute-path normalization and `go.work`/`go.mod` discovery with deterministic sorting. Return actionable errors for malformed workspace files; do not fall back to process cwd when explicit roots exist.

- [ ] Store normalized folders/modules on `Server` during `initialize`. Use `RootURI` only when workspace folders are absent; use cwd only when both are absent for old clients.

- [ ] Advertise workspace-folder support and dispatch `workspace/didChangeWorkspaceFolders`. On add/remove, recompute modules and invalidate only whole-module caches (`moduleRefs`, `moduleParams`, `moduleSyms`), leaving open per-package analyses owned by their document lifecycle.

- [ ] Add lifecycle tests proving folder removal excludes that module from later workspace operations and folder addition includes it without restarting the server.

- [ ] Run `gofmt -w internal/lsp/protocol.go internal/lsp/server.go internal/lsp/workspace.go internal/lsp/workspace_test.go internal/lsp/server_lifecycle_test.go`, `gopls check -severity=hint` on them, and `go test ./internal/lsp -run 'Workspace|Initialize|Lifecycle' -count=1`.

- [ ] Commit: `git add internal/lsp && git commit -m 'feat(lsp): track Go workspace roots'`

### Task 10: Make workspace symbols complete and deterministic

**Files:**
- Modify: `internal/lsp/workspacesymbol.go`
- Modify: `internal/lsp/workspacesymbol_test.go`
- Modify: `internal/lsp/server.go`
- Modify: `gen/lsp.go`
- Create: `gen/lsp_workspace_symbol_e2e_test.go`

**Interfaces:**
- `Server` caches module symbols by normalized module root:
  ```go
  type moduleSymbolCache struct {
      symbols []Symbol
      valid   bool
  }
  ```
- `handleWorkspaceSymbol` calls `Analyzer.ModuleSymbols(moduleRoot, overridesForModule)` once per initialized module, merges matches, then sorts by symbol name, workspace-relative path, source start, and kind.
- Query policy remains case-insensitive substring matching; ranking is outside this plan.

- [ ] Extend unit tests to initialize two modules containing equal symbol names and assert both appear in stable order independent of analyzer return order. Run `go test ./internal/lsp -run TestWorkspaceSymbol -count=1`; expect nondeterministic/current-single-module failures.

- [ ] Replace the single `moduleSyms` cache with a map keyed by module root. Partition open overrides by module ownership and call `ModuleSymbols` for each initialized module. Remove last-document/cwd ownership from the request path.

- [ ] Normalize and stable-sort results by the specified tuple before encoding. Set method containers to the receiver identity; set package containers to the module-relative package path whenever the package name is ambiguous across initialized modules. Compute every range through `locationForSpan`.

- [ ] Add an invalidation test: edit a document in module A, assert A's cache refreshes while module B's cached result is reused; remove module B and assert its cache entry disappears.

- [ ] Add an end-to-end `go.work` test with two temp modules, one unsaved UTF-8 edit, components plus raw Go/GoWithElements declarations, and no `.x.go` files. Send the same query repeatedly and assert byte-identical JSON result ordering and correct UTF-16 positions.

- [ ] Run `gofmt -w internal/lsp/workspacesymbol.go internal/lsp/workspacesymbol_test.go internal/lsp/server.go gen/lsp.go gen/lsp_workspace_symbol_e2e_test.go`, `gopls check -severity=hint` on them, `go test ./internal/lsp ./gen -run 'WorkspaceSymbol' -count=1`, then `go test ./internal/lsp ./gen -count=1`.

- [ ] Commit: `git add internal/lsp gen && git commit -m 'feat(lsp): complete workspace symbols'`

### Task 11: Prove generated-file independence, measure cost, and close the audit

**Files:**
- Create: `gen/lsp_no_generated_e2e_test.go`
- Create: `internal/codegen/source_index_bench_test.go`
- Modify: `internal/codegen/module_stale_xgo_test.go`
- Modify: `internal/codegen/module_perf_test.go`
- Create: `internal/lsp/source_index_bench_test.go`
- Create: `gen/lsp_bench_test.go`
- Modify: `gen/perf_test.go`
- Modify: `docs/ROADMAP.md`
- Modify: `docs/superpowers/specs/2026-07-19-lsp-authored-source-intelligence-design.md`

**Interfaces:**
- The adversarial e2e fixture exposes one helper, component declaration/parameter, explicit generic type argument, GoWithElements declaration, and cross-package target through all four structured capabilities.
- `TestLSPManualOneLearning` is an opt-in real-consumer probe selected by `GSX_LSP_FIXTURE`; it queries `email/templates.gsx` (`time.Duration`), `ds/badge/badge.gsx` (`Badge` and `variant`), `ui/pacm_edit.gsx` (`pgtype.Bool`), and `ui/dashboard_npt.gsx` (`duration.Hours`).
- Benchmark names:
  ```go
  func BenchmarkModuleAnalyzeSourceIndex(b *testing.B)
  func BenchmarkSourceIndexLookup(b *testing.B)
  func BenchmarkSemanticDefinition(b *testing.B)
  func BenchmarkSemanticHover(b *testing.B)
  func BenchmarkDocumentSymbols(b *testing.B)
  func BenchmarkCachedWorkspaceSymbols(b *testing.B)
  func BenchmarkColdModuleWorkspaceSymbols(b *testing.B)
  ```
- The design document receives an implementation-evidence appendix with measured one-learning numbers and exact verification commands; the approved design itself is not rewritten.

- [ ] Write `TestLSPStructuredAnswersIgnorePhysicalGeneratedFile`. Capture definition, hover, document-symbol, and workspace-symbol JSON with no sibling `.x.go`; write an invalid/conflicting `.x.go`; create a fresh analyzer/server and rerun; assert identical responses and unchanged poison-file bytes/modtime. Run `go test ./gen -run TestLSPStructuredAnswersIgnorePhysicalGeneratedFile -count=1`; expect failure if any inventory or analysis path consumes the file.

- [ ] Extend `internal/codegen/module_stale_xgo_test.go` with `TestModuleSourceIndexIgnoresOnDiskXGo`: run fresh analyses for absent, invalid, stale, and conflicting paired outputs; compare indexed occurrences/declarations and package scope, and assert the paired files' bytes/modtimes are untouched. This pins the existing exact paired-output exclusion in `parseTargetCompanionGoFiles`; an unexpected failure must be root-caused with `superpowers:systematic-debugging` before any implementation continues.

- [ ] Implement `TestLSPManualOneLearning` in `gen/lsp_no_generated_e2e_test.go`. It must skip when `GSX_LSP_FIXTURE` is empty, log `git -C $GSX_LSP_FIXTURE rev-parse HEAD`, copy the named fixture module into `t.TempDir()` while deliberately excluding every `.x.go`, run the production analyzer/server, then add conflicting poison files at the four would-be generated paths and repeat. Assert identical non-null exact targets/hovers plus document/workspace symbols across absent/poisoned runs. Run `GSX_LSP_FIXTURE=/Users/jackieli/work/one-learning-gsx go test ./gen -run TestLSPManualOneLearning -count=1 -v`; record the fixture commit, command, and results in the design appendix.

- [ ] Add all seven named benchmarks with `b.ReportAllocs()`. Run `go test ./internal/codegen ./internal/lsp ./gen -run '^$' -bench 'SourceIndex|SemanticDefinition|SemanticHover|DocumentSymbols|WorkspaceSymbols' -benchmem -count=10 | tee /tmp/gsx-lsp-index-after.txt` and retain the output for the design appendix.

- [ ] Measure the existing comparable 50-package fixture before and after. Run `git worktree add --detach /tmp/gsx-lsp-baseline b915e57c`, then from that worktree run `GSX_PERF=1 go test ./gen -run TestPerfBaseline -count=5 -v | tee /tmp/gsx-lsp-perf-before.txt`; run the same command from the feature worktree into `/tmp/gsx-lsp-perf-after.txt`; finally run `git worktree remove -f /tmp/gsx-lsp-baseline`. Extend `TestPerfBaseline` to report source-index bytes and use its existing two-GC `runtime.ReadMemStats` measurements. Record warm/cold latency and retained heap before/after, and record the seven new benchmark time/op, allocations/op, and bytes/op as absolute post-change costs.

- [ ] Extend `TestWarmRegenDoesNoGoListReloads` or add `TestWarmLSPSourceIndexDoesNoGoListReloads` to assert external/filter load counts remain one across index-bearing warm analyses. Confirm by the retention test and code review that the index stores no AST, `token.File`, `SourceMap`, or source bytes.

- [ ] Update `docs/ROADMAP.md`: mark the four read capabilities complete from executable evidence, remove claims about already-supported dotted tags and removed attrs-only syntax, and add one separate completion-design item covering cursor-time invalid syntax, candidates, edits, and ranking.

- [ ] Run the full focused matrix: `go test ./internal/sourceintel ./internal/codegen ./internal/lsp ./gen -count=1`.

- [ ] Run generated/docs/example drift checks through `make check`; expect green and inspect the diff to ensure no generated `.x.go` or golden file was hand-edited.

- [ ] Run `make lint`; expect green. Run `make ci`; expect the authoritative uncached suite to pass on Go 1.26.1.

- [ ] Request one independent adversarial reviewer. The reviewer must build a fresh throwaway module—not just read the diff—and probe all four capabilities with the `.x.go` absent, invalid, stale, and conflicting. Address every correctness finding, rerun focused tests, `make lint`, and `make ci`.

- [ ] Commit: `git add gen/lsp.go gen/lsp_no_generated_e2e_test.go gen/lsp_bench_test.go gen/perf_test.go internal/codegen/module_importer.go internal/codegen/module_perf_test.go internal/codegen/module_stale_xgo_test.go internal/codegen/source_index_bench_test.go internal/lsp/source_index_bench_test.go docs/ROADMAP.md docs/superpowers/specs/2026-07-19-lsp-authored-source-intelligence-design.md && git commit -m 'test(lsp): prove authored-source independence'`

- [ ] Inspect `git status --short`, `git diff --check`, and `git log --oneline origin/main..HEAD`; require a clean worktree and the intended task commits only before invoking `superpowers:finishing-a-development-branch`.
