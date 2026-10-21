package task

// takeLastCheck returns the last failing check output of a builder session that ended without a pass, and clears it.
func (m *Manager) takeLastCheck() string {
	m.mu.Lock()
	defer m.mu.Unlock()

	s := m.lastCheck
	m.lastCheck = ""

	return s
}
