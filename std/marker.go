package std

// pkgMarker is the unexported type behind Pkg. Its only purpose is to carry the
// std package's import path: a value of this type reflects back to
// "github.com/gsxhq/gsx/std" via reflect.TypeOf(...).PkgPath().
type pkgMarker struct{}

// Pkg is the registration token for this filter package. Pass it to
// gen.WithFilters to register std's filters with a custom gsx binary:
//
//	gen.Main(gen.WithFilters(std.Pkg, myfilters.Pkg))
//
// gen.WithFilters recovers the package's import path from Pkg's type, so the
// build configuration carries no import-path string by hand. A user filter
// package exports its own Pkg the same way (an unexported marker type plus an
// exported var of that type) to make itself registrable.
var Pkg pkgMarker
