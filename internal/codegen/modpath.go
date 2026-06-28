package codegen

import "golang.org/x/mod/modfile"

// ModulePathFromGoMod returns the module path declared in go.mod content, or ""
// if the content has no module directive. It delegates to modfile.ModulePath,
// which correctly handles inline comments (module x // c) and quoted module
// paths (module "x") — both of which a naive strings.TrimPrefix(line, "module ")
// mishandles. The module path is load-bearing for computeKey, so correctness here
// matters for incremental-cache invalidation.
func ModulePathFromGoMod(data []byte) string {
	return modfile.ModulePath(data)
}
