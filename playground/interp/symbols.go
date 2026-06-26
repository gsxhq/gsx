package interp

import "reflect"

// Symbols is the yaegi binding registry. The generated github_com-*.go files
// populate it via init() with reflection bindings for the gsx runtime and std
// filters, so interpreted gsx-generated code can call them. Pass it to
// interp.Interpreter.Use alongside yaegi's stdlib symbols.
var Symbols = map[string]map[string]reflect.Value{}

//go:generate go run github.com/traefik/yaegi/cmd/yaegi extract github.com/gsxhq/gsx github.com/gsxhq/gsx/std
