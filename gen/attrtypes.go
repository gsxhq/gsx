package gen

import "github.com/gsxhq/gsx/internal/attrclass"

// Rule classifies an attribute name by exact Name (case-insensitive) OR by
// Prefix (exactly one set). Re-exported so users import only this package.
type Rule = attrclass.Rule
