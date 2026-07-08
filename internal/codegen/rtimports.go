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

// Reserved aliases. The `_gsx` prefix is reserved for the generator, and that
// reservation is ENFORCED in three places: checkReservedDecls at package scope
// (including import aliases), checkReservedParams on component params,
// checkReservedRecvVar on method-component receiver vars. The parser's
// component-name scan admits no `_`, so a component can never reach the space
// either. Together these cover every binding that could collide with a
// generator-emitted import alias in the .x.go's package scope — which is what
// lets a .gsx file bind `gsx`, `context`, `io` or `strconv` to whatever it likes.
//
// Enforcement stops there. A `_gsx` name bound in a FUNCTION BODY — a local in a
// pass-through Go chunk, a `{{ … }}` GoBlock binding — is unchecked, and so is a
// hand-written sibling .go file in the same package. Such a name is caught only
// INCIDENTALLY, by go/types over the skeleton, and only when both hold:
//
//   - the skeleton itself binds the name. It binds `_gsxrt` and `_gsxctx` (its two
//     imports), the used filter aliases (`_gsxstd`, `_gsxf<i>`), the requalified
//     type-arg aliases (`_gsxti<N>`), the props param `_gsxp`, and its probe
//     helpers (`_gsxelem`, `_gsxuse`, `_gsxinfer<N>`, …). `_gsxio`, `_gsxsc`,
//     `_gsxcm`, `_gsxgw`, `_gsxw` and `_gsxnum` appear ONLY in the emitted file,
//     so no shadow of them is ever type-checked.
//   - the skeleton references it AFTER the user's binding, in the same scope. A
//     GoBlock `_gsxrt := "z"` is not caught even though the skeleton imports
//     `_gsxrt`: the skeleton's only reference is the component's `_gsxrt.Node`
//     return type, which precedes the body.
//
// So neither "caught" nor "harmless" is a property of the name alone. When a
// shadow is not caught, `gsx generate` exits 0 and `go build` rejects the .x.go
// ("io" imported as _gsxio and not used; _gsxsc.FormatBool undefined; no new
// variables on left side of :=).
//
// Closing that gap is tracked in docs/ROADMAP.md. Until it is closed, an `_gsx`
// name outside the three enforced scopes is undefined behaviour, not a supported
// pattern. See reservedPrefix (analyze.go) for why the whole prefix, not just the
// four names below, is the reserved unit.
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
