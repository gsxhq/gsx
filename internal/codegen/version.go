package codegen

// Version is the codegen-output version tag, a COARSE manual lever for forcing a
// project-wide cache bust (the gsx incremental cache folds it into every key).
// Bumping it is now OPTIONAL: the cache also folds in a content hash of the gsx
// binary (see gen.selfHash), so any change to lowering/emit auto-invalidates
// cached output even without a bump. Bump only to force invalidation explicitly
// (e.g. to drop pre-existing cache entries on a release boundary).
const version = "22"

// Version returns the codegen-output version tag (see the version constant).
func Version() string { return version }
