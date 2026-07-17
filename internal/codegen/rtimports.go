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
// `io`, `strconv` or `strings` to something of its own — `import gsx "strings"`,
// `var io = 1` — makes those names unusable by the generator ("redeclared in this
// block").
// Reserved aliases plus emission-site need-recording make both impossible.
//
// Every accessor records the need AND returns the identifier to print, so a
// reference can never be emitted without its import, nor an import without a
// reference. This is why no site prints these package names literally.
type rtImports map[string]bool

// gsxRuntimePath is the import path of the gsx runtime.
const gsxRuntimePath = "github.com/gsxhq/gsx"

// Reserved aliases. The `_gsx` prefix is reserved for the generator: it emits
// EVERY import and internal binding under a `_gsx`-prefixed name (the five below,
// plus `_gsxgw`/`_gsxw`/`_gsxnum` in render closures, the filter aliases
// `_gsxf<i>`/`_gsxstd`, the type-arg aliases `_gsxti<N>`, and transient probe
// bindings). That is what lets a .gsx file bind
// `gsx`, `context`, `io`, `strconv` or `strings` to whatever it likes.
//
// checkReservedDecls (reserved_scan.go) enforces the prefix directly, by lexing
// every user Go fragment — top-level declarations, function-body locals, GoBlock
// statements, and every embedded expression — and reporting any `_gsx`
// identifier before the skeleton is type-checked. Exact component signature
// validation covers parameters and receiver vars. So a shadow of
// any alias below is a clean gsx diagnostic, not an incidental `go build` failure
// on the emitted .x.go. (A hand-written sibling .go file is the one place gsx does
// not look — its `_gsx` name is still caught by `go build`; see docs/ROADMAP.md.)
// See reservedPrefix (reserved_scan.go) for why the whole prefix, not just the
// five names below, is the reserved unit.
const (
	rtAlias  = "_gsxrt"
	ctxAlias = "_gsxctx"
	ioAlias  = "_gsxio"
	scAlias  = "_gsxsc"
	// stAlias neighbours the std-filter alias `_gsxstd` in generated output.
	// Distinct identifiers, but read them carefully: _gsxst is "strings",
	// _gsxstd is the gsx std filter package.
	stAlias = "_gsxst"
)

// rt records a need for the gsx runtime and returns its alias.
func (r rtImports) rt() string { r[gsxRuntimePath] = true; return rtAlias }

// ctx records a need for "context" and returns its alias.
func (r rtImports) ctx() string { r["context"] = true; return ctxAlias }

// io records a need for "io" and returns its alias.
func (r rtImports) io() string { r["io"] = true; return ioAlias }

// sc records a need for "strconv" and returns its alias.
func (r rtImports) sc() string { r["strconv"] = true; return scAlias }

// st records a need for "strings" and returns its alias. Used by the
// catStringSlice arms, which lower a []string to strings.Join(v, " ").
func (r rtImports) st() string { r["strings"] = true; return stAlias }
