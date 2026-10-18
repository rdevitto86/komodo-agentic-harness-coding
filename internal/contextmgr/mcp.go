package contextmgr

import (
	"fmt"
	"slices"
	"sort"
)

// Connection is one Model Context Protocol server the harness can offer to agents.
type Connection struct {
	Name    string   `json:"name"`
	Command string   `json:"command"` // program started for the server
	Args    []string `json:"args"`
	Env     []string `json:"env"` // names of environment variables passed through; never values
	Enabled bool     `json:"enabled"`
	Allow   []string `json:"allow"` // agents that may use it
	Hosts   []string `json:"hosts"` // network hosts the server may reach; empty means no network
	Origin  Origin   `json:"-"`     // set by the loader, never read from the file
}

// ConnectionFile is the whole of connections.json.
type ConnectionFile struct {
	V           int          `json:"v"` // schema version, 1
	Connections []Connection `json:"connections"`
}

// komodoConnection is the name of the embedded tool set entry.
const komodoConnection = "komodo"

// connAt returns the index of the outside connection with the name, or -1.
func (m *Manager) connAt(name string) int {
	return slices.IndexFunc(m.conns, func(c Connection) bool { return c.Name == name })
}

// toolsFor returns the connections an agent may use: the embedded entry, then enabled outside
// entries by name.
func (m *Manager) toolsFor(agent string) []Connection {
	out := []Connection{embeddedConnection()}

	var rest []Connection
	for _, c := range m.conns {
		if c.Enabled && slices.Contains(c.Allow, agent) {
			rest = append(rest, c)
		}
	}

	sort.Slice(rest, func(i, j int) bool { return rest[i].Name < rest[j].Name })
	return append(out, rest...)
}

// startConnections loads connections.json and applies the harness's starting state to its
// entries.
func (m *Manager) startConnections() {
	conns, warns := loadConnections(m.deps.Home)
	m.conns, m.connWarns = conns, warns

	for _, c := range m.deps.Conns {
		i := m.connAt(c.Name)

		switch {
		case i < 0:
			m.connWarns = append(m.connWarns, Warning{
				Code: "connection_unknown",
				Path: "connections.json",
				Note: fmt.Sprintf("starting state names %q, which is not in the file", c.Name),
			})
		case badAgent(c.Allow) != "":
			m.connWarns = append(m.connWarns, Warning{
				Code: "connection_refused",
				Path: "connections.json",
				Note: fmt.Sprintf("starting state of %q names an unknown agent", c.Name),
			})
		default:
			m.conns[i].Enabled, m.conns[i].Allow = c.Enabled, nonNil(c.Allow)
		}
	}
}

// reloadConnections rereads connections.json; an entry that is still there keeps its state.
func (m *Manager) reloadConnections() {
	old := m.conns
	conns, warns := loadConnections(m.deps.Home)
	for i := range conns {
		for _, o := range old {
			if o.Name == conns[i].Name {
				conns[i].Enabled, conns[i].Allow = o.Enabled, o.Allow
			}
		}
	}
	m.conns, m.connWarns = conns, warns
}

// embeddedConnection returns the tool set entry of the harness itself: always on, for every
// agent.
func embeddedConnection() Connection {
	return Connection{
		Name:    komodoConnection,
		Command: "komodo",
		Args:    []string{},
		Env:     []string{},
		Enabled: true,
		Allow:   append([]string(nil), Agents...),
		Hosts:   []string{},
		Origin:  OriginEmbedded,
	}
}
