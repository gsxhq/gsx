package codegen

// Version is the codegen-output version tag. BUMP THIS whenever a change to
// lowering/emit alters generated .x.go for unchanged input. The gsx incremental
// cache folds it into every cache key, so bumping it invalidates all cached
// output project-wide.
const version = "19"

// Version returns the codegen-output version tag (see the version constant).
func Version() string { return version }
