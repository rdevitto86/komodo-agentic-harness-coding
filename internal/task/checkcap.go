package task

// takeLastCheck returns the last failing check output of every task still pending after a builder session that ended
// without finishing, and clears all of them.
func (m *Manager) takeLastCheck() []string {
	pending := m.pendingTasks()

	m.mu.Lock()
	defer m.mu.Unlock()

	var out []string
	for _, t := range pending {
		if s := m.lastCheck[t.ID]; s != "" {
			out = append(out, s)
		}
	}
	m.lastCheck = nil

	return out
}
