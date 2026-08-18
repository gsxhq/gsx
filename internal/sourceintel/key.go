package sourceintel

import (
	"go/types"
	"strconv"

	"golang.org/x/tools/go/types/objectpath"
)

// ObjectKey is the stable, cross-analysis identity of a Go object inside the
// module symbol graph. Two independent type-checks of the same source produce
// the same key for the same declaration, which pointer identity does not.
//
// Format: "<import path> <path>" where <path> is
//   - the objectpath (exported-reachable objects, unexported types and their
//     members, params, fields, type params);
//   - the bare name for package-level objects objectpath cannot address
//     (unexported funcs/vars/consts) — identical to what objectpath would emit;
//   - "#<n>" for the Keyer's own package's remaining objects (locals): only
//     referenced from within that package, so per-Keyer ordinals suffice.
type ObjectKey string

// Keyer assigns ObjectKeys for one type-checked package's objects. Objects of
// other packages are keyed too (they are what cross-package references point
// at), except their locals, which are never visible cross-package.
type Keyer struct {
	enc     objectpath.Encoder
	pkgPath string
	local   map[types.Object]int
}

func NewKeyer(pkg *types.Package) *Keyer {
	k := &Keyer{local: map[types.Object]int{}}
	if pkg != nil {
		k.pkgPath = pkg.Path()
	}
	return k
}

func (k *Keyer) Key(object types.Object) (ObjectKey, bool) {
	object = Origin(object)
	if object == nil || object.Pkg() == nil {
		return "", false // universe, builtins, nil
	}
	pkgPath := object.Pkg().Path()
	if path, err := k.enc.For(object); err == nil {
		return ObjectKey(pkgPath + " " + string(path)), true
	}
	if isPackageLevel(object) {
		return ObjectKey(pkgPath + " " + object.Name()), true
	}
	if k.pkgPath == "" || pkgPath != k.pkgPath {
		return "", false
	}
	n, ok := k.local[object]
	if !ok {
		n = len(k.local)
		k.local[object] = n
	}
	return ObjectKey(pkgPath + " #" + strconv.Itoa(n)), true
}

// isPackageLevel reports whether object is declared directly in its package
// scope (types, funcs without receiver, vars, consts). Methods and fields have
// no parent scope; locals, params and type params live in nested scopes.
func isPackageLevel(object types.Object) bool {
	switch o := object.(type) {
	case *types.PkgName, *types.Label, *types.Builtin, *types.Nil:
		return false
	case *types.Var:
		if o.IsField() {
			return false
		}
	case *types.Func:
		if sig, ok := o.Type().(*types.Signature); ok && sig.Recv() != nil {
			return false
		}
	}
	return object.Parent() == object.Pkg().Scope()
}
