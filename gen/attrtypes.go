package gen

import (
	"github.com/gsxhq/gsx/internal/attrclass"
	"github.com/gsxhq/gsx/internal/codegen"
)

// Rule classifies an attribute name by exact Name (case-insensitive) OR by
// Prefix (exactly one set). Re-exported so users import only this package.
type Rule = attrclass.Rule

// Rules groups attribute-classification rules by context.
type Rules = attrclass.Rules

// Context is the escaping context implied by an attribute name.
type Context = attrclass.Context

// FieldMatcher maps an HTML attribute name to a Go struct field name for the
// byo (bring-your-own Props) path. Re-exported so users import only this package.
// See gen.WithFieldMatcher.
type FieldMatcher = codegen.FieldMatcher

const (
	CtxPlain = attrclass.CtxPlain
	CtxJS    = attrclass.CtxJS
	CtxURL   = attrclass.CtxURL
	CtxCSS   = attrclass.CtxCSS
)
