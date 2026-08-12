package codegen

import "golang.org/x/tools/go/packages"

// loadPackages is this package's ONLY entry to golang.org/x/tools/go/packages.
// Every go-list load — loadExternalGraph's project half and its
// ineligible/fallback full loads, loadSharedWorld's closure load, filters.go's
// harvestFilters, resolver.go's cached-resolver bootstrap,
// unused_imports_syntactic.go's resolvePackageNames — runs through it so
// projectLoads counts them all.
//
// The counter is what pins the warm edit cycle's load budget
// (TestWatchSession_EditLoadBudget), and an uninstrumented call site would
// silently lower the measured number rather than fail the gate. Owning the
// increment here makes that structurally impossible;
// TestProjectLoadsHasOneLoadSite keeps the wrapper the sole caller.
func loadPackages(cfg *packages.Config, patterns ...string) ([]*packages.Package, error) {
	projectLoads.Add(1)
	return packages.Load(cfg, patterns...)
}
