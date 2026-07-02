# Component Type-Parameter LSP Design

## Goal

Support hover and go-to-definition for identifiers inside component type-parameter lists, such as `store.ID` in `component Box[T store.ID](value T)`.

## Scope

This covers the component declaration's type-parameter list only. It does not add new behavior for every later use of the type parameter in the component body, except where that behavior already exists through ordinary expression analysis.

## Architecture

The LSP already resolves identifiers inside component signature types by recording `SigTypeRef` spans during codegen analysis and bridging a `.gsx` cursor into the type-checked skeleton AST. Extend that path to include type-parameter names and constraint expressions.

`Component.TypeParamsPos` is the `.gsx` anchor for the trimmed type-parameter list. `buildSigTypeRefs` records each type parameter name and constraint type as spans relative to that anchor and pairs them with the corresponding skeleton `go/ast` nodes. Existing `signatureTypeIdentAt`, hover, and definition handlers can then resolve through `go/types` without a separate generic-specific resolver.

## Behavior

Hover on an identifier inside a constraint returns the same object string as hovering the equivalent Go type expression. Go-to-definition on a type name jumps to its declaration. Go-to-definition on a package qualifier in a constraint jumps to the imported package's package clauses, matching the existing parameter-type behavior.

Hover and go-to-definition on the declared type parameter name resolve to that type parameter's own `go/types.TypeName` and source span.

## Tests

Add focused tests for `signatureTypeIdentAt` using a generated package analysis fixture:

- cursor on `store.ID` type name inside `component Box[T store.ID](value T)` resolves to the imported type declaration
- cursor on the `store` qualifier in the same constraint resolves as a package name
- cursor on the declared type parameter name `T` resolves to a type-name object and the `T` span
- cursor outside the recorded type-parameter span still returns no signature-type result

These tests pin the mapping layer. Existing `handleHover` and `handleDefinition` already delegate to `signatureTypeIdentAt`, so the feature is covered without spinning up a JSON-RPC server.
