package codegen

import (
	goast "go/ast"
	"go/token"
	"go/types"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
)

// CrossRef is one component's cross-boundary entry: its name, its .gsx
// declaration, and every reference (resolved positions — .go call sites stay
// .go; .gsx <Card/> tags map to .gsx via the skeleton's child-tag //line).
// Name's length bounds the cursor-on-reference span check in the LSP.
//
// Decls holds every build-tag variant's declaration position (sorted by
// filename then offset); Decl is kept equal to Decls[0] as the primary
// position for callers that only care about "the" declaration.
type CrossRef struct {
	Name  string
	Decl  token.Position
	Decls []token.Position
	Refs  []token.Position
}

// NavRef is one navigable Go reference (in a .go or skeleton file) and the .gsx
// position it targets. From is the reference site; To is the .gsx declaration.
type NavRef struct {
	From token.Position
	Name string // identifier text, for the cursor-span length check in the LSP
	To   token.Position
}

// ComponentParamRole is the semantic role of an authored callable parameter.
// It is published for retained tooling facts only; generated code continues to
// consume the private componentSignatureModel role directly.
type ComponentParamRole uint8

const (
	ComponentParamOrdinary ComponentParamRole = iota
	ComponentParamAttrs
	ComponentParamChildren
	ComponentParamGoOnlyVariadic
)

// ComponentParamFact identifies the exact callable parameter bound by one
// authored markup attribute. Var belongs to the instantiated signature used at
// this call; Origin is the declaration identity retained across generic
// instantiation. Ordinal is stable within that origin signature.
type ComponentParamFact struct {
	Var     *types.Var
	Origin  *types.Var
	Name    string
	Ordinal int
	Role    ComponentParamRole
}

// ComponentCallFact is the retained semantic identity of one successfully
// planned markup call. Params contains only attribute names that semantically
// reference a callable parameter: exact ordinary bindings and explicit
// lowercase attrs contributors. Fallthrough attribute names are deliberately
// absent even though their values feed the attrs bag.
//
// PackageResult owns this map and its nested maps; LSP consumers treat them as
// immutable snapshots, like the retained go/types objects alongside them.
type ComponentCallFact struct {
	Target        types.Object
	TargetOrigin  types.Object
	TargetPackage string
	TargetKey     string
	Signature     *types.Signature
	Params        map[gsxast.Attr]ComponentParamFact
}

// ComponentParamDeclFact is one semantically validated GSX component
// parameter family. PackagePath, ComponentKey, and Ordinal form its stable
// identity. Decls contains the exact authored name position for every
// equivalent build-tag variant. BlockedNames is the union of typed names whose
// scopes would collide with a renamed parameter in any variant.
type ComponentParamDeclFact struct {
	PackagePath  string
	ComponentKey string
	Ordinal      int
	Name         string
	Role         ComponentParamRole
	Origin       *types.Var
	Decls        []token.Position
	BlockedNames []string
}

// ComponentParamRefFact is one exact authored parameter reference: either an
// invocation attribute bound by the component planner or a semantic use inside
// a GSX component body. Unmatched fallthrough attrs and mere name matches are
// absent from Ref; invocation refs carry their call's other authored attribute
// names in BlockedNames so a rename cannot silently change planner binding.
type ComponentParamRefFact struct {
	PackagePath  string
	ComponentKey string
	Ordinal      int
	Name         string
	Role         ComponentParamRole
	Origin       *types.Var
	Ref          token.Position
	BlockedNames []string
}

// PackageResult is the per-package outcome of code generation.
type PackageResult struct {
	Files map[string][]byte // .gsx path -> generated .x.go source
	Diags []diag.Diagnostic // all diagnostics collected for this package

	// Retained analysis for the language server (read-only; nil when the package
	// failed before type-checking). The two FileSets are distinct: GSXFset is the
	// gsx parse fset; Fset is the go/packages skeleton fset.
	GSXFset    *token.FileSet
	Fset       *token.FileSet
	Info       *types.Info
	ExprMap    map[gsxast.Node]goast.Expr
	GSXFiles   map[string]*gsxast.File
	CrossIndex map[string]CrossRef // componentKey → cross-boundary index entry
	NavIndex   []NavRef            // navigable Go references → .gsx declaration targets
	// ComponentCalls maps each successfully planned component element to its
	// exact callable target and bound-parameter identities. It is the retained
	// definition/hover surface for markup calls; consumers must not reconstruct
	// these facts from tag spelling or a reconstructed callable shape.
	ComponentCalls      map[*gsxast.Element]ComponentCallFact
	ComponentParamDecls []ComponentParamDeclFact
	ComponentParamRefs  []ComponentParamRefFact

	// CtrlMap maps each control-flow node (ForMarkup/IfMarkup/GoBlock, and each
	// value-form if condition's *ValueIf) to its
	// skeleton clause position and smallest containing skeleton go/ast node.
	// Used by the LSP to bridge a cursor in a for/if/goblock clause to the
	// skeleton for go-to-definition on loop variables and condition identifiers.
	CtrlMap map[gsxast.Node]ctrlRef

	// SigTypes maps each component to the navigable spans in its signature —
	// parameter types (e.g. `store.Comment` in `component C(c []store.Comment)`),
	// type-parameter names and constraints, and a method receiver type — so the
	// LSP can answer go-to-definition / hover on the identifiers inside them.
	SigTypes map[*gsxast.Component][]SigTypeRef

	// UnusedImports lists, per .gsx file path, the imports the file declares but
	// does not use — safe to drop on format. Empty unless the package's ONLY type
	// errors are unused-import errors (else removal is unsafe).
	UnusedImports map[string][]UnusedImport

	// MissingImports lists, per .gsx file path, the qualifiers the file uses that
	// resolve to nothing — candidates for an added import. Unresolved by design;
	// see MissingImport.
	MissingImports map[string][]MissingImport

	// Types is the analyzed package's go/types.Package, retained for the LSP
	// (e.g. hover's qualifier). nil when the package failed before type-checking.
	Types *types.Package
}

// UnusedImport is one import a .gsx file declares but never references, as
// determined by the type-checker. Name is "" for a default import.
type UnusedImport struct {
	Name string
	Path string
}

// MissingImport is a qualifier used in a .gsx file that resolves to nothing: no
// local, no import. Name is the qualifier ("fmt"), Symbol is the selector on it
// ("Sprintf") — Symbol is what lets an ambiguous name like `rand` be resolved to
// the one candidate that actually exports it. Pos is the qualifier's position in
// the .gsx source.
//
// Deliberately UNRESOLVED: turning a Name into an import path may read package
// export data, which must never happen on the Package() hot path. The LSP
// resolves it in a user-triggered code-action handler via
// Module.ResolveImportCandidates.
type MissingImport struct {
	Name   string
	Symbol string
	Pos    token.Position
}
