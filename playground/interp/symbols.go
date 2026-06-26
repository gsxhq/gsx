package interp

import "reflect"

// Symbols is the yaegi binding registry. The generated github_com-*.go files
// populate it via init() with reflection bindings for the gsx runtime and std
// filters, so interpreted gsx-generated code can call them. Pass it to
// interp.Interpreter.Use alongside yaegi's stdlib symbols.
var Symbols = map[string]map[string]reflect.Value{}

// Bindings cover the gsx runtime, std filters, and ONLY the playground stdlib
// allowlist (gen.DefaultPlaygroundImports) — not yaegi's full stdlib — so the
// wasm binary stays deployable. Keep the stdlib list in sync with the allowlist.
//go:generate go run github.com/traefik/yaegi/cmd/yaegi extract github.com/gsxhq/gsx github.com/gsxhq/gsx/std
//go:generate go run github.com/traefik/yaegi/cmd/yaegi extract context io strconv fmt strings time sort errors math math/rand unicode unicode/utf8 html
