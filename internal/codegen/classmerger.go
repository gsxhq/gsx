package codegen

import (
	"fmt"
	"go/types"
)

// classMergerAlias is the reserved import alias for the configured class-merger
// package. Uses the _gsx prefix so it never collides with a user symbol.
const classMergerAlias = "_gsxcm"

// ClassMergerRef names the configured class merger: an exported package-level
// identifier (func decl or var of func type) whose type is exactly
// func([]string) string. Codegen emits a direct reference _gsxcm.<FuncName>.
type ClassMergerRef struct {
	PkgPath  string
	FuncName string
}

// ValidateConfiguredMergers validates every class merger configured on an
// ALREADY-OPEN Module (the module-wide ClassMerger plus any PerDir override),
// against that Module's own composed shared-world closure — the one its later
// Generate calls will use. Prefer this over the package-level
// ValidateClassMerger for a caller (e.g. gen.prepareWatchSession) that already
// holds the fully-configured Module: a throwaway probe Module built from just
// a ClassMergerRef composes a NARROWER shared-world path set whenever the real
// Module also carries FilterPkgs/Aliases/Renderers/LoadPkgs, which mints a
// second, differently-keyed world at session startup instead of sharing the
// one this Module is about to load anyway — exactly the duplicate cold load
// gen.TestWatchSession_ConfiguredModuleWorldBudget pins against. The result is
// memoized on m (classMergersDone), so a later Generate's own defense-in-depth
// call is a no-op.
//
// Holds analysisMu, like every other top-level entry point and like the
// package-level ValidateClassMerger below: validation resolves the merger
// through configuredSourcePackages, which reaches externalImporter and can
// therefore reach rebuildFset — the one operation that swaps m.fset and drops
// the cached importer, and which every analysis assumes is serialized against
// it. In-package callers already hold the mutex and use the unexported form.
func (m *Module) ValidateConfiguredMergers() error {
	m.analysisMu.Lock()
	defer m.analysisMu.Unlock()
	return m.validateConfiguredMergers()
}

// ValidateClassMerger type-checks ref.PkgPath and verifies ref.FuncName names an
// exported package-level object whose type is exactly func([]string) string.
// Returns a clear, user-facing error otherwise (missing symbol, or wrong
// signature with a pointer at the wrapper idiom).
//
// This opens its OWN throwaway Module scoped to just ref, so its composed
// shared-world closure can be narrower than a caller's real, fully-configured
// Module — see ValidateConfiguredMergers above for the caller that already
// holds one. Standalone callers with no Module of their own (tests; any future
// caller that only has a dir and a ref) should still use this.
// Do NOT call this from codegen.Open: that path is shared by the LSP and fmt,
// which must not pay a packages.Load per-Open or fail on merger config.
func ValidateClassMerger(dir string, ref *ClassMergerRef) error {
	modulePath, err := readModulePath(dir)
	if err != nil {
		return fmt.Errorf("class_merger: read module: %w", err)
	}
	module, err := Open(Options{ModuleRoot: dir, ModulePath: modulePath, ClassMerger: ref})
	if err != nil {
		return fmt.Errorf("class_merger: open module: %w", err)
	}
	module.analysisMu.Lock()
	defer module.analysisMu.Unlock()
	return module.validateConfiguredMergers()
}

// validateClassMergerObj holds the check shared by both validators: ref.FuncName
// must name an exported package-level func([]string) string.
func validateClassMergerObj(pkg *types.Package, ref *ClassMergerRef) error {
	obj := pkg.Scope().Lookup(ref.FuncName)
	if obj == nil || !obj.Exported() {
		return fmt.Errorf("class_merger: %q has no exported %s", ref.PkgPath, ref.FuncName)
	}
	sig, ok := obj.Type().(*types.Signature)
	if !ok {
		return classMergerSigErr(ref, obj.Type())
	}
	if !isStringSliceToString(sig) {
		return classMergerSigErr(ref, sig)
	}
	return nil
}

// isStringSliceToString reports whether sig is exactly func([]string) string
// (non-variadic, one param []string, one result string).
func isStringSliceToString(sig *types.Signature) bool {
	if sig.Variadic() || sig.Params().Len() != 1 || sig.Results().Len() != 1 {
		return false
	}
	p, ok := types.Unalias(sig.Params().At(0).Type()).(*types.Slice)
	if !ok {
		return false
	}
	if b, ok := types.Unalias(p.Elem()).(*types.Basic); !ok || b.Kind() != types.String {
		return false
	}
	r, ok := types.Unalias(sig.Results().At(0).Type()).(*types.Basic)
	return ok && r.Kind() == types.String
}

func classMergerSigErr(ref *ClassMergerRef, got types.Type) error {
	return fmt.Errorf("class_merger %q.%s has signature %s; it must be func([]string) string. "+
		"Wrap it in a one-line exported func in your own package — see docs/guide/config.md#class_merger",
		ref.PkgPath, ref.FuncName, got)
}
