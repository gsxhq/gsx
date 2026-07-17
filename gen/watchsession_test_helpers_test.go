package gen

// startWatchSessionForTest is the fixture form used by tests whose subject is a
// fully initialized warm session rather than startup ordering. Startup-order
// tests use armWatchSession and initialGenerate as separate phases.
func startWatchSessionForTest(cfg watchConfig) (*watchSession, []cycleResult, error) {
	session, err := prepareWatchSession(cfg)
	if err != nil {
		return nil, nil, err
	}
	startup, err := session.initialGenerate()
	if err != nil {
		return nil, nil, err
	}
	return session, startup, nil
}
