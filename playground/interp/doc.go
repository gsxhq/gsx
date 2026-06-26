// Package interp explores running gsx-generated components through a Go
// interpreter (yaegi) so the playground's HTML preview can execute entirely
// client-side in WASM — no Go toolchain, no Cloud Run. The generated code only
// touches std plus the small gsx runtime, which keeps the interpreter's binding
// surface tractable.
package interp
