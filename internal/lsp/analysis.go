package lsp

import (
	"go/ast"
	"go/token"
	"go/types"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/internal/gsxfmt"
	"github.com/gsxhq/gsx/internal/sourceintel"
)

// CrossRef is one component's cross-boundary entry (see the .go->.gsx design):
// its name, its .gsx declaration, and every reference, as resolved positions.
// Decls holds every build-tag variant's declaration position (Decl is the
// primary/first, kept for back-compat with single-decl callers) — see
// codegen.CrossRef.Decls (Task 6).
type CrossRef struct {
	Name  string
	Decl  token.Position
	Decls []token.Position
	Refs  []token.Position
}

// NavRef is one navigable Go reference (in a .go file) and the .gsx position it
// targets. From is the reference site; Name is the identifier text (used for
// cursor-span checking); To is the .gsx declaration target.
type NavRef struct {
	From token.Position
	Name string
	To   token.Position
}

type ComponentParamRole uint8

const (
	ComponentParamOrdinary ComponentParamRole = iota
	ComponentParamAttrs
	ComponentParamChildren
	ComponentParamGoOnlyVariadic
)

// ComponentParamFact is the exact callable parameter selected for one authored
// component attribute. Var is the instantiated identity and Origin is its
// declaration identity.
type ComponentParamFact struct {
	Var     *types.Var
	Origin  *types.Var
	Name    string
	Ordinal int
	Role    ComponentParamRole
}

// ComponentCallFact is codegen's retained semantic answer for one successfully
// planned markup call. Params is keyed by the authored attribute node, so LSP
// never needs to reproduce component binding rules.
type ComponentCallFact struct {
	Target             types.Object
	TargetOrigin       types.Object
	TargetPackage      string
	TargetKey          string
	Signature          *types.Signature
	Params             map[gsxast.Attr]ComponentParamFact
	TargetDecls        []sourceintel.VersionedSpan
	ParamDecls         map[int][]sourceintel.VersionedSpan
	TargetPresentation string
}

type ComponentDeclKey struct {
	PackagePath  string
	ComponentKey string
}

// ComponentParamKey is the stable semantic identity of one callable parameter
// on a logical component family.
type ComponentParamKey struct {
	PackagePath  string
	ComponentKey string
	Ordinal      int
}

// ComponentParamRenameFact is the complete module-wide rename surface for one
// semantically validated GSX parameter. Decls contains every equivalent
// build-tag variant; Refs contains exact semantic body uses and planner-bound
// invocation attrs. BlockedNames is the complete module-wide union of typed
// declaration-scope and authored call-attribute namespace collisions.
type ComponentParamRenameFact struct {
	Key          ComponentParamKey
	Name         string
	Role         ComponentParamRole
	Origin       *types.Var
	Decls        []token.Position
	Refs         []token.Position
	BlockedNames []string
}

// CtrlRef is the LSP mirror of codegen.ctrlRef: a control-flow clause's
// skeleton position and smallest containing skeleton node, used for
// go-to-definition on loop variables and condition identifiers.
type CtrlRef struct {
	ClauseStart token.Pos
	Node        ast.Node // skeleton node scoping innermostIdent
}

// SigTypeRef is the LSP mirror of codegen.SigTypeRef: one navigable signature
// span's position (GSXPos) and byte length (Len) in the .gsx — a parameter
// type, a type-parameter name or constraint, or a method receiver type —
// paired with the type-checked skeleton expression (SkelTyp) whose bytes are
// identical to that source span. The LSP bridges a cursor into SkelTyp by
// relative offset and resolves the identifier via go/types.
type SigTypeRef struct {
	GSXPos  token.Pos
	Len     int
	SkelTyp ast.Expr
}

// Package is the retained, read-only result of analyzing one .gsx package: the
// diagnostics plus everything the read-intelligence features need. GSXFset
// resolves gsx node positions; Fset resolves skeleton/object positions
// (honoring //line). Under the Module path both may point to the same
// *token.FileSet (the module-wide shared fset); callers must not assume they
// are distinct objects.
type Package struct {
	Diags   []diag.Diagnostic
	GSXFset *token.FileSet
	Fset    *token.FileSet
	Info    *types.Info

	// SourceIndex is codegen's immutable authored-source semantic index. The
	// adapter preserves its pointer and package-snapshot lifetime directly.
	SourceIndex *sourceintel.Index

	Types      *types.Package
	ExprMap    map[gsxast.Node]ast.Expr // gsx Interp/ExprAttr → skeleton go/ast expr
	Files      map[string]*gsxast.File  // .gsx path → parsed gsx AST
	CrossIndex map[string]CrossRef
	NavIndex   []NavRef // navigable Go references → .gsx declaration targets
	// ComponentCalls maps authored elements to exact target and parameter facts.
	// The package owns this immutable analysis snapshot and its nested maps.
	ComponentCalls map[*gsxast.Element]ComponentCallFact
	ComponentDecls map[ComponentDeclKey][]sourceintel.VersionedSpan

	// CtrlMap maps each control-flow node (ForMarkup/IfMarkup/GoBlock, and each
	// value-form if condition's *ValueIf) to its
	// skeleton clause position and smallest containing skeleton node. Used by the
	// LSP for go-to-definition on loop variables and condition identifiers.
	CtrlMap map[gsxast.Node]CtrlRef

	// SigTypes maps each component to the type spans in its parameter list, so a
	// cursor on an identifier inside a parameter TYPE (e.g. `store.Comment` in
	// `component C(c []store.Comment)`) can be bridged into the type-checked
	// skeleton and resolved for go-to-definition / hover.
	SigTypes map[*gsxast.Component][]SigTypeRef

	// UnusedImports lists, per .gsx file path, imports that file declares but does
	// not use — what formatting may safely drop. Empty when analysis is unreliable.
	UnusedImports map[string][]gsxfmt.ImportRef

	// MissingImports lists, per .gsx file path, qualifiers that resolve to nothing
	// — candidates for an added import. Unresolved: the code-action handler calls
	// Analyzer.ResolveImport, which may read export data and must stay off the
	// analysis path.
	MissingImports map[string][]MissingImport

	// Filters is the sorted list of pipeline-filter completion candidates from
	// the package's resolved filter table.
	Filters []FilterCandidate

	// URLPresets names the url-attribute presets in effect for this package's dir
	// (e.g. "htmx"). HTML attribute-name completion consults it: when "htmx" is
	// present the htmx hx-* attributes join the candidate list. Position-independent
	// (a package-wide config fact), so a stale retained snapshot is a safe fallback.
	URLPresets []string
}

// MissingImport is a qualifier the file uses that resolves to nothing. Symbol is
// the selector on it, which disambiguates an ambiguous name (`rand.IntN`).
type MissingImport struct {
	Name   string
	Symbol string
	Pos    token.Position
}

// FilterCandidate is one pipeline-filter completion candidate, from the dir's
// resolved filter table.
type FilterCandidate struct {
	Name     string // template name, e.g. "upper"
	Pkg      string // winning package import path
	Func     string // exported Go func name, e.g. "Upper"
	WantsCtx bool
}
