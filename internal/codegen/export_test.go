package codegen

// testSourceManifestEpoch exposes the source-manifest epoch counter for tests
// pinning override/disk-refresh reload behavior.
func (m *Module) testSourceManifestEpoch() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sourceManifestEpoch
}
