// Package playbundle embeds the playground type bundle — the transitive
// go/types closure of the gsx runtime, the std filter package, and the
// playground stdlib allowlist — and builds a CachedResolver from it with no
// packages.Load and no subprocess. This is the resolver the client-side WASM
// playground uses: all type resolution rides on the embedded bundle, so the
// transform runs entirely in the browser.
//
// Regenerate the bundle after changing the allowlist or the gsx runtime:
//
//	go generate ./playground/playbundle
package playbundle

import (
	_ "embed"

	"github.com/gsxhq/gsx/gen"
)

//go:generate go run github.com/gsxhq/gsx/cmd/gsx-typebundle -o playground.typebundle

//go:embed playground.typebundle
var bundle []byte

// NewResolver builds the playground's bundled resolver (built-in std filters)
// from the embedded type bundle. No packages.Load, no subprocess — WASM-safe.
func NewResolver() (*gen.CachedResolver, error) {
	return gen.NewBundledResolver(bundle, nil)
}

// Bundle returns the raw embedded bundle bytes (for size checks / diagnostics).
func Bundle() []byte { return bundle }
