//go:build js

package codegen

// diskExists always reports false on js/wasm: there is no filesystem, so an
// overlay-only key can never collide with a real on-disk file — the overlay map
// is the sole source of truth for collisions in a browser build.
func diskExists(string) (bool, error) { return false, nil }
