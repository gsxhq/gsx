// Package playbundle embeds the playground type bundle — the transitive
// go/types closure of the gsx runtime, the std filter package, and the
// playground stdlib allowlist — and builds a rootless resolver from it with no
// packages.Load and no subprocess. Both the client-side WASM playground and the
// server consume complete in-memory GSX source sets against this external type
// universe; neither resolver inspects a project module or host source tree.
//
// Regenerate the bundle after changing the allowlist or the gsx runtime:
//
//	go generate ./playground/playbundle
package playbundle

import (
	_ "embed"

	"github.com/gsxhq/gsx/gen"
)

//go:generate go run github.com/gsxhq/gsx/cmd/gsx-typebundle -compiler=gc -goos=linux -goarch=amd64 -cgo=0 -language-version=go1.26.1 -o playground.typebundle

//go:embed playground.typebundle
var bundle []byte

// NewResolver builds the playground's bundled resolver (built-in std filters)
// from the embedded type bundle. Callers use its in-memory GenerateSource(s)
// surface: no project source discovery, packages.Load, or subprocess.
func NewResolver() (*gen.BundledResolver, error) {
	return gen.NewBundledResolver(bundle, nil)
}
