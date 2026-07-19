# LSP authored-source intelligence

**Date:** 2026-07-19
**Status:** approved design
**Scope:** complete go-to-definition, hover, document-symbol, and
workspace-symbol coverage across valid `.gsx` source; establish the exact
source-mapping foundation for a later completion design

## Goal

Make read intelligence follow one rule:

> If gsx type-checks an authored identifier, type, expression, or declaration,
> the language server can locate it in the authoritative `.gsx` buffer and can
> answer every applicable read request from the same semantic result.

The implementation must cover every valid authored region, including ordinary
component markup, component declarations and signatures, explicit component
type arguments, top-level `GoChunk` source, and the Go text surrounding markup
inside `GoWithElements`.

This is an in-memory language-server feature. A physical generated `.x.go` file
is neither an input nor a prerequisite. The LSP may parse an in-memory Go
skeleton whose virtual filename ends in `.x.go`, because that name preserves Go
tooling and diagnostic conventions, but it must not read or write the sibling
generated file or let that file's existence/content affect semantic analysis.
A source-inventory walk may observe the directory entry only to exclude it.
Removing the file or replacing it with invalid bytes must not change an LSP
answer.

## Evidence and current gaps

The current LSP has strong specialised coverage: exact component-call and
attribute facts, component signature type spans, cross-package component
navigation, and an expression/control-flow matrix covering markup, pipelines,
embedded literals, and `{{ }}` blocks.

The missing cases are whole source regions rather than isolated syntax bugs.
Live requests against `one-learning-gsx` on 2026-07-19 confirmed:

| Authored cursor | Current result |
| --- | --- |
| `time.Duration` and `d.Hours()` inside a top-level Go helper | null definition and hover |
| the name in `component Badge(...)` | null definition and hover |
| the declaration name in `variant Variant` | null definition and hover |
| `pgtype.Bool` in `<form.EditCheckbox[pgtype.Bool] ...>` | null definition and hover |

The symbol path has separate gaps:

- `GoWithElements` declarations are intentionally skipped;
- one parse error in a `GoChunk` drops every symbol in that chunk, even when
  `go/parser` recovered intact declarations;
- workspace-symbol locations encode columns from the saved file even when an
  open override supplied the positions, causing UTF-16 drift;
- result order depends on map iteration;
- `workspace/symbol` derives its module from an arbitrary open document or the
  process working directory instead of the workspace roots supplied by LSP.

Historical roadmap text still lists some already-closed gaps (for example,
dotted component-tag navigation) and removed syntax (attrs-only components).
The roadmap must be corrected from executable evidence as part of this work.

## Non-goals

- Implement `textDocument/completion` in this change. The source map and
  semantic index are prerequisites; completion needs a separate design for
  invalid cursor-time syntax repair, candidate enumeration, edits, and ranking.
- Proxy requests to gopls, import gopls internals, or require gopls to be
  installed.
- Write generated code from the LSP.
- Replace exact GSX semantic facts such as `ComponentCalls`, variant families,
  parameter binding, or imported component targets with spelling-based lookup.
- Add fuzzy workspace-symbol ranking. Substring matching is an explicit search
  policy, not a source-coverage defect; ranking can be designed independently.
- Promise semantic answers for arbitrary invalid source. The server remains
  best-effort under parse/type errors. Document symbols should retain genuine
  declarations returned by Go's partial parser, but no fabricated declaration
  or heuristic repair belongs in this read-intelligence change.

## Design principles

1. **Authored bytes are the source of truth.** Every mapping begins from an
   exact `.gsx` byte span owned by the parser or skeleton emitter.
2. **Generated glue has no read capability.** A token is navigable only when an
   explicit mapping says it came from authored source.
3. **One semantic answer, many handlers.** Definition, hover, and symbols
   consume a shared retained index rather than recreating component or Go
   semantics.
4. **Specialised GSX semantics keep precedence.** Multi-variant component
   declarations, exact component targets, and authored attribute binding remain
   owned by their existing codegen facts.
5. **No heuristic mapping.** Filename suffixes, nearby `//line` directives,
   identifier spelling, and reconstructed callable shapes are not sufficient
   evidence that generated syntax represents a user span.
6. **The editor buffer is authoritative.** Position conversion consults an open
   document before disk, consistently across all handlers.

## Exact in-memory source map

### Segment model

`buildSkeleton` will return a compact source map alongside the skeleton string.
It records each authored copy at the moment the copy is written:

```go
type sourceSegment struct {
    sourcePath  string
    sourceStart int // byte offset in the .gsx file
    sourceEnd   int // half-open
    skeletonStart int // byte offset in the in-memory skeleton
    skeletonEnd   int // half-open
    capabilities sourceCapabilities
}

type sourceCapabilities uint8

const (
    sourceDefinition sourceCapabilities = 1 << iota
    sourceHover
    sourceSymbol
    sourceCompletion
)
```

The concrete names may change during planning, but these invariants may not:

- both sides are half-open byte ranges;
- an authored segment maps bytes exactly, not merely lines;
- generated text is absent from the map or has zero capabilities;
- maps support multiple skeleton copies of one source span;
- only one canonical semantic copy of a repeated probe is read-capable;
- segments are immutable, sorted, non-overlapping on the skeleton side, and
  binary-searchable;
- a segment never crosses source files.

`//line` directives continue to serve Go diagnostics and object positions. They
are not the LSP reverse map. The explicit segments are the authority that tells
the LSP whether a skeleton token is authored and how its exact range maps back.

### Mapped skeleton writer

Direct writes to the skeleton builder will be classified through a focused
writer:

- `writeGenerated(text)` emits glue with no authored capabilities;
- `writeAuthored(source span, text, capabilities)` verifies byte identity and
  records the segment;
- a deliberate transformed-authored operation records subsegments explicitly
  rather than claiming a non-identical region is byte-mapped.

This writer is introduced at the shared `buildSkeleton` boundary. It covers:

- hoisted imports and verbatim top-level `GoChunk` bodies;
- every `GoText` part of `GoWithElements`;
- component receiver, name, type-parameter, and parameter source;
- explicit component invocation type arguments;
- expression and control-flow text copied into native probes;
- embedded literal holes and markup nested in Go expressions;
- authored declarations surrounding generated element-value IIFEs.

Several authored expressions currently appear in more than one probe. The
emitter must mark the native, semantically representative copy read-capable and
leave quiet/validation duplicates unmapped for read requests. This prevents two
different skeleton objects from competing for the same cursor.

All skeleton modes may share the writer, but only the full retained package
analysis needs to publish read-intelligence segments. Generation-only and
declaration-only paths must not pay to retain an unused map.

### Mapping positions after parsing

The skeleton is still parsed and type-checked in memory with the Module's shared
`token.FileSet`. Once parsed, skeleton byte offsets are converted to `token.Pos`
through that virtual file's `token.File`. Source offsets are converted through
the already-parsed `.gsx` file's `token.File`.

No mapping step opens `<name>.x.go`. The virtual skeleton bytes and its token
file are sufficient.

## Retained semantic index

After type-checking, codegen harvests a read-only semantic index from the
canonical source segments and `types.Info`.

For authored identifiers it records:

- exact `.gsx` span;
- `types.Object` from `Info.Defs` or `Info.Uses`;
- whether the occurrence is a declaration or use;
- the exact authored declaration span when the defining object is owned by a
  mapped `.gsx` declaration;
- the ordinary `token.Position` for a declaration in a real `.go` dependency.

For authored expressions it records:

- exact `.gsx` span only when the whole expression is covered by compatible
  authored segments;
- `types.TypeAndValue` from `Info.Types`;
- the smallest expression first when spans nest, matching cursor expectations.

For top-level Go declarations it records:

- name, declaration kind, receiver/container identity, name span, and complete
  authored declaration range;
- the declaration's source kind (`GoChunk` or `GoWithElements`) for testing and
  diagnostics only, not handler branching.

The index is grouped per source file and sorted by start offset, then by tighter
span. Requests use binary search plus a short overlap scan, not a linear walk of
the package's full `types.Info` maps.

Objects already retained by `types.Info` are referenced, not cloned. The source
map and index must be measured on one-learning before and after; they must not
introduce a second retained Go AST or duplicate source buffers.

`PackageResult` and `lsp.Package` expose the semantic index as immutable package
facts. Invalidation follows the existing warm Module package-result lifecycle:
an edit invalidates the same package/dependant closure, and no separate semantic
cache can outlive its owning analysis snapshot.

## Definition behavior

The handler keeps a deliberate precedence order:

1. exact component target and build-variant facts;
2. exact authored component-attribute parameter binding;
3. component tag family behavior, including multi-location variants;
4. import-path package navigation;
5. the general semantic index.

The general path provides definition from both uses and declaration sites.
Clicking an authored declaration name returns its own source location, matching
Go editor behavior. A mapped use whose object is declared in `.gsx` resolves
through the index's object-to-authored-declaration table; an object declared in
a real `.go` file resolves through the shared `FileSet`.

Generated helper declarations and virtual `.x.go` locations are never returned.
A missing authored mapping returns null rather than guessing.

This closes, uniformly:

- names and uses inside top-level Go functions, types, vars, and consts;
- Go surrounding markup in `GoWithElements`;
- component declaration names;
- receiver variable/type, type-parameter, and ordinary parameter declarations;
- explicit type arguments on component invocations;
- existing expression, control-flow, pipeline, and embedded-literal positions.

## Hover behavior

Hover uses the same precedence as definition for exact component semantics, then
the semantic index:

- an identifier renders `types.ObjectString` with the package-aware qualifier;
- a non-identifier expression renders `types.TypeString` from the tightest
  indexed `TypeAndValue`;
- component declarations and invocations retain GSX-native `component ...`
  presentation;
- component parameter declarations and uses render their actual Go object;
- explicit call-site type arguments render the resolved type/object;
- generated helper names never appear.

This spec does not add documentation text to hover. Documentation association
for mixed Go/GSX comments is a separate presentation feature; semantic identity
and exact ranges come first.

## Document symbols

Components continue to come from the GSX AST so their full declaration range
and `Method`/`Function` presentation stay source-native.

Top-level Go symbols come from the mapped, parsed skeleton declarations rather
than independently reparsing each `GoChunk`. Only declaration names covered by
`sourceSymbol` segments qualify, which excludes component probe functions and
all helper declarations by construction. This covers ordinary `GoChunk`
declarations and declarations whose bodies or initializers contain markup in
`GoWithElements`.

Symbols remain top-level. Struct fields, interface methods, component params,
and local declarations are not Outline entries in this change.

When full semantic analysis is unavailable during an incomplete edit,
`FileSymbols` may fall back to the GSX AST plus a Go AST returned by
`go/parser` with `parser.AllErrors`. It publishes only real declarations present
in that partial AST. It does not insert placeholders or reconstruct a missing
declaration. The last successful semantic snapshot is not mixed with current
buffer positions.

Document-symbol ranges are always encoded against the requested open buffer.

## Workspace symbols and workspace ownership

Initialization retains the protocol's workspace identity:

- `workspaceFolders` when supplied;
- otherwise `rootUri`;
- otherwise an explicitly documented process-working-directory fallback for
  minimal clients.

Module symbol discovery runs for every configured workspace root and every gsx
module owned by those roots. It is not anchored to an arbitrary open document.
Ownership follows Go's real workspace model: a `go.work` root contributes its
declared `use` modules (parsed with `x/mod/modfile`), while a module root
contributes its `go.mod` module. The server does not recursively guess that every
`go.mod` found somewhere below an arbitrary directory belongs to the workspace.
An open `.gsx` document is also associated with its containing configured
module, but it cannot silently add an unrelated module outside the initialized
roots. `workspace/didChangeWorkspaceFolders` updates the root/module set and
invalidates the module symbol cache.

The current case-insensitive substring query stays unchanged. Results are
sorted deterministically by:

1. name;
2. workspace-relative source path;
3. source offset;
4. symbol kind.

Duplicate names retain a container that distinguishes their package; when a
package name alone is ambiguous across modules, the module-relative package
path is used.

Every location conversion goes through one authoritative-source accessor:

1. the open `docStore` snapshot for that path, when present;
2. otherwise the saved file bytes;
3. otherwise byte-column fallback only when the source is unavailable.

The accessor is shared by definition, references, workspace symbols, and any
future location-producing handler. This fixes unsaved UTF-8/UTF-16 drift instead
of patching workspace symbols alone.

## Relationship to completion

The source map includes a reserved completion capability because completion
must eventually translate a cursor and edits between `.gsx` and an in-memory Go
view. This change does not advertise `completionProvider` and does not implement
candidate generation.

The follow-up completion design must answer three separate problems:

1. classify the cursor's GSX context (Go expression, selector, component tag,
   component attribute, type argument, pipeline stage, import, or markup);
2. repair only the in-memory analysis form when cursor-time syntax is invalid;
3. enumerate, rank, and map candidates/edits without exposing generated glue.

The exact source map solves translation but deliberately does not pretend that
valid-source `go/types` information solves partial-source completion.

## No-physical-generated-file acceptance boundary

Every end-to-end read-intelligence test runs with no generated file present.
At least one adversarial fixture additionally places invalid or misleading bytes
at the would-be `<name>.x.go` path and proves the answer is unchanged. The test
must cover:

- definition into `.gsx` and into a real `.go` dependency;
- hover in a `GoChunk`, `GoWithElements`, component declaration, and explicit
  type argument;
- document and workspace symbols;
- unsaved-buffer position conversion.

The test should compare structured protocol results, not merely assert that no
error occurred. It must also verify that the LSP did not rewrite the poisoned
file.

## Testing matrix

### Source-map unit tests

- exact round-trip for ASCII and multibyte UTF-8 source;
- multiple skeleton copies with one canonical read-capable segment;
- generated glue between two authored segments;
- authored text at the beginning/end of a line and mid-expression;
- a segment crossing a `GoWithElements` IIFE boundary is rejected;
- binary-search containment and nested-expression ordering;
- no segment points outside either source or skeleton buffer.

### Definition and hover tests

- top-level helper signature, body locals, selectors, return types, and imported
  package qualifiers;
- `GoWithElements` function params/locals plus identifiers inside and around the
  embedded markup;
- component name, receiver variable/type, type parameter name/constraint,
  parameter name/type, and return-free signature boundary;
- same-package and cross-package explicit component type arguments;
- declaration-site definition returns self;
- whole-expression hover in top-level Go;
- existing tag/attribute/variant/pipeline/control-flow/embedded-literal matrices
  remain byte-for-byte compatible;
- generated helper and glue positions return null.

### Symbol tests

- ordinary `GoChunk` declarations and `GoWithElements` declarations;
- no duplicate synthetic component function;
- stable ordering across repeated runs;
- partial `go/parser` AST retains intact declarations around an error;
- document positions follow an unsaved multibyte buffer;
- workspace positions follow unsaved buffers in multiple modules;
- workspace-folder add/remove invalidates results;
- identical package names receive unambiguous containers.

### Real-consumer probes

Against a disposable clone of `one-learning-gsx` at the exact GSX commit:

- hover/definition inside `email/templates.gsx` top-level helpers;
- hover/definition on a component declaration and parameter;
- hover/definition on `pgtype.Bool` in an explicit `EditCheckbox` type argument;
- document symbols for a large mixed helper/component file;
- workspace symbol lookup before and after an unsaved multibyte prefix edit;
- generated files absent, then poisoned, with identical structured answers.

## Performance and retention

The index is built during the existing package analysis after one type-check. It
must not call `packages.Load`, parse the module again, or read generated output.

Measurements report:

- index build time per package;
- retained segment/index bytes per package;
- definition, hover, document-symbol, and cached workspace-symbol latency;
- cold module workspace-symbol latency on one-learning;
- before/after total retained heap for the existing 50-package perf fixture and
  the realistic one-learning session.

No arbitrary threshold is baked into production code. A regression large enough
to make the LSP visibly slower or materially undo the warm-Module gains must be
addressed before merge.

## Delivery sequence

1. Add the mapped skeleton writer, invariant tests, and retained source-map
   facts without changing protocol behavior.
2. Build the general semantic index and route definition/hover fallback through
   it; close every valid source-region matrix row.
3. Rebuild document/workspace symbols on the mapped declaration facts and fix
   authoritative-buffer/workspace-root handling.
4. Run adversarial no-`.x.go` probes, real one-learning probes, retention
   measurements, and full repository gates.
5. Correct roadmap/editor documentation from the verified final matrix.

Each behavior slice starts with a failing protocol-level test. The final branch
requires `make check`, `make lint`, the authoritative `make ci`, an exact-commit
one-learning consumer gate, and an independent adversarial review that builds
throwaway probe programs rather than reading only the diff.

## Acceptance criteria

- Every valid authored identifier/type region listed in this spec has definition
  and hover coverage from one shared semantic index.
- `GoChunk` and `GoWithElements` top-level declarations appear in document and
  workspace symbols without generated duplicates.
- Workspace locations are correct for unsaved UTF-8 and UTF-16 buffers and for
  every initialized workspace root.
- Existing specialised component behavior and all prior LSP matrices remain
  unchanged.
- No handler reads or requires a physical `.x.go`; absent and poisoned generated
  files produce identical structured responses.
- No new `packages.Load` call, no spelling heuristic, and no reconstructed
  component binding is introduced.
- Completion remains unadvertised until its own partial-source design is
  approved.
