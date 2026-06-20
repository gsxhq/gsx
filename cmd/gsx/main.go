// Command gsx is the stock gsx CLI: it generates .x.go from .gsx files using the
// hardcoded standard codegen. Custom binaries can call gen.Main with options to
// extend it (a later slice).
package main

import "github.com/gsxhq/gsx/gen"

func main() { gen.Main() }
