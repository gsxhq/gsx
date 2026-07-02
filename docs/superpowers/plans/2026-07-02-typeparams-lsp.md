# Component Type-Parameter LSP Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add hover and go-to-definition support for identifiers inside component type-parameter declarations and constraints.

**Architecture:** Extend the existing signature-type bridge by recording type-parameter name and constraint spans in `buildSigTypeRefs`. Reuse `signatureTypeIdentAt`, `handleHover`, and `handleDefinition` without adding a new resolver path.

**Tech Stack:** Go, `go/ast`, `go/types`, gsx parser/codegen analysis, internal LSP tests.

---

### Task 1: Pin Type-Parameter Constraint Resolution

**Files:**
- Modify: `internal/lsp/definition_test.go`
- Read: `internal/codegen/module_test.go`

- [ ] Add a helper-backed LSP test that builds a temporary module with `store.ID` and a `.gsx` component `Box[T store.ID]`.
- [ ] Assert `signatureTypeIdentAt` resolves a cursor on `ID` to a `*types.TypeName` named `ID`.
- [ ] Assert `signatureTypeIdentAt` resolves a cursor on `store` to a `*types.PkgName`.
- [ ] Assert `signatureTypeIdentAt` resolves a cursor on declared `T` to a `*types.TypeName` named `T`.
- [ ] Run `GOCACHE=/tmp/gsx-typeparams-lsp-gocache go test ./internal/lsp -run 'TestSignatureTypeIdentAtTypeParam' -count=1` and verify the tests fail before implementation.

### Task 2: Record Type-Parameter Spans

**Files:**
- Modify: `internal/codegen/analyze.go`

- [ ] In `buildSigTypeRefs`, inspect the skeleton component function's `Type.TypeParams`.
- [ ] For each type-parameter name, compute its source span inside `c.TypeParams` from a synthetic Go parse of `func _[` + `c.TypeParams` + `]() {}`.
- [ ] For each type-parameter constraint, compute its source span inside `c.TypeParams` from the same synthetic parse.
- [ ] Append `SigTypeRef` entries pairing `.gsx` spans with corresponding skeleton `go/ast` identifiers and constraint expressions.
- [ ] Keep receiver and parameter type behavior unchanged.

### Task 3: Verify and Clean Up

**Files:**
- Modify as needed: `internal/codegen/analyze.go`, `internal/lsp/definition_test.go`

- [ ] Run the focused LSP test and verify it passes.
- [ ] Run `GOCACHE=/tmp/gsx-typeparams-lsp-gocache go test ./internal/lsp ./internal/codegen -count=1`.
- [ ] Run `gofmt -w internal/lsp/definition_test.go internal/codegen/analyze.go`.
- [ ] Run `GOCACHE=/tmp/gsx-typeparams-lsp-gocache make check` for final verification.
