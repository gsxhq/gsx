package codegen

// rtImports records the imports the GENERATOR itself emits references to, keyed
// by import path. It is deliberately disjoint from emit's `imports` map (which
// holds the user's Go-chunk imports plus filter/type-arg/class-merger packages):
// both may need the same path under different names. The generator always
// reaches the runtime through a reserved `_gsx` alias; user Go always reaches it
// through the plain package name. Go permits one path under two names, and
// emit.go already relies on that for filter packages (see userPlainImports).
//
// Keeping the two sets disjoint answers two questions that a single seeded map
// conflated: "is this package needed?" and "is this name free?". Neither is
// constant. A .gsx with no gsx parts needs none of them (a seeded map emitted
// three "imported and not used" errors); a .gsx that binds `gsx`, `context`,
// `io` or `strconv` to something of its own — `import gsx "strings"`, `var io =
// 1` — makes those names unusable by the generator ("redeclared in this block").
// Reserved aliases plus emission-site need-recording make both impossible.
//
// Every accessor records the need AND returns the identifier to print, so a
// reference can never be emitted without its import, nor an import without a
// reference. This is why no site prints these package names literally.
type rtImports map[string]bool

// gsxRuntimePath is the import path of the gsx runtime.
const gsxRuntimePath = "github.com/gsxhq/gsx"

// Reserved aliases. The `_gsx` prefix is not a valid identifier for user code
// (checkReservedParams and checkReservedRecvVar reject it), so these can never
// collide with anything the user writes — which is what lets a .gsx file bind
// `gsx`, `context`, `io` or `strconv` to whatever it likes.
const (
	rtAlias  = "_gsxrt"
	ctxAlias = "_gsxctx"
	ioAlias  = "_gsxio"
	scAlias  = "_gsxsc"
)

// rt records a need for the gsx runtime and returns its alias.
func (r rtImports) rt() string { r[gsxRuntimePath] = true; return rtAlias }

// ctx records a need for "context" and returns its alias.
func (r rtImports) ctx() string { r["context"] = true; return ctxAlias }

// io records a need for "io" and returns its alias.
func (r rtImports) io() string { r["io"] = true; return ioAlias }

// sc records a need for "strconv" and returns its alias.
func (r rtImports) sc() string { r["strconv"] = true; return scAlias }
