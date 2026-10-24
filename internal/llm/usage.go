package llm

import "strconv"

// MsgUsage is the usage one model message reported, keyed by the message identifier.
type MsgUsage struct {
	ID    string // the provider's message identifier; empty when the line has none
	Usage Usage
}

// UsageTally adds up the usage of the messages a session streamed, for a session that ends without a result.
type UsageTally struct {
	byID  map[string]Usage
	order []string
	anon  int
}

// IsZero reports whether the count holds no tokens and no cost.
func (u Usage) IsZero() bool { return u == Usage{} }

// Add records one message; a repeated identifier replaces its earlier count, so a message counts once.
func (t *UsageTally) Add(m *MsgUsage) {
	if m == nil {
		return
	}
	if t.byID == nil {
		t.byID = map[string]Usage{}
	}

	id := m.ID
	if id == "" {
		t.anon++
		id = "\x00" + strconv.Itoa(t.anon)
	}
	if _, seen := t.byID[id]; !seen {
		t.order = append(t.order, id)
	}

	t.byID[id] = m.Usage
}

// Total returns the sum over the counted messages, or nil when none carried usage.
func (t *UsageTally) Total() *Usage {
	if len(t.order) == 0 {
		return nil
	}

	var sum Usage
	for _, id := range t.order {
		u := t.byID[id]
		sum.TokensIn += u.TokensIn
		sum.TokensOut += u.TokensOut
		sum.CacheRead += u.CacheRead
		sum.CacheWrite += u.CacheWrite
		sum.USD += u.USD
		sum.USDEquiv += u.USDEquiv
	}

	return &sum
}
