package resource

import (
	"cmp"
	"maps"
	"slices"
	"time"
)

// WindowKey names one window on one link.
type WindowKey struct {
	Link   string
	Window string
}

// WindowState is what the Resource Manager remembers about one window.
type WindowState struct {
	PercentUsed float64         // 100 minus the percent of headroom
	ResetAt     time.Time       // when the window resets; a past time means it has
	Sessions    int             // sessions finished in this window
	Warned      bool            // the warn error was raised in this window
	Full        bool            // the window_full error was raised in this window
	Paused      map[string]bool // groups whose pause order for this window was delivered
	Groups      map[string]bool // groups seen on the link since this window last reset
}

// Notice is a link-level overage or refusal: when it lifts and whether its error was raised.
type Notice struct {
	ResetAt time.Time // when the notice lifts; zero when the provider gave none
	Raised  bool      // the error was raised for this reset time
}

// applyHeadroom sets a window's percent used and reset time from one checked reading.
func (s *State) applyHeadroom(h Headroom, now time.Time) {
	k := WindowKey{Link: h.Link, Window: h.Window}
	s.expire(k, now)
	w := s.window(k)
	w.PercentUsed = float64(h.Limit-h.Remaining) * 100 / float64(h.Limit)
	w.ResetAt = h.ResetAt
}

// expire starts a window over once its reset time has passed: flags, sessions and groups seen
// are cleared.
func (s *State) expire(k WindowKey, now time.Time) {
	w := s.Windows[k]
	if w == nil || w.ResetAt.IsZero() || w.ResetAt.After(now) {
		return
	}
	w.Warned, w.Full, w.Sessions = false, false, 0
	w.Paused, w.Groups = map[string]bool{}, map[string]bool{}
}

// window returns the state of k, creating it with the groups already seen on the link.
func (s *State) window(k WindowKey) *WindowState {
	w := s.Windows[k]
	if w == nil {
		w = &WindowState{Paused: map[string]bool{}, Groups: maps.Clone(s.LinkGroups[k.Link])}
		if w.Groups == nil {
			w.Groups = map[string]bool{}
		}
		s.Windows[k] = w
	}
	return w
}

// applyOverage records that a link draws paid credits and when its window resets.
func (s *State) applyOverage(o Overage, now time.Time) {
	n := s.Overages[o.Link]
	if n == nil {
		n = &Notice{}
		s.Overages[o.Link] = n
	}
	expireNotice(n, now)
	if !o.ResetAt.IsZero() {
		n.ResetAt = o.ResetAt
	}
}

// applyLimit records a provider refusal; a new reset time starts a new refusal.
func (s *State) applyLimit(l Limit, now time.Time) {
	k := WindowKey{Link: l.Link, Window: l.Kind}
	n := s.Limits[k]
	if n == nil {
		n = &Notice{}
		s.Limits[k] = n
	}

	expireNotice(n, now)
	if !n.ResetAt.Equal(l.ResetAt) {
		n.Raised = false
	}

	n.ResetAt = l.ResetAt
}

// bumpSessions adds one finished session to each unreset window of a link and returns the keys
// it changed.
func (s *State) bumpSessions(link string, now time.Time) []WindowKey {
	var keys []WindowKey
	for _, k := range s.windowKeys(link) {
		s.expire(k, now)
		if w := s.Windows[k]; w.ResetAt.After(now) {
			w.Sessions++
			keys = append(keys, k)
		}
	}
	return keys
}

// windowKeys returns the keys of a link's windows in name order.
func (s *State) windowKeys(link string) []WindowKey {
	var keys []WindowKey
	for k := range s.Windows {
		if k.Link == link {
			keys = append(keys, k)
		}
	}
	slices.SortFunc(keys, func(a, b WindowKey) int { return cmp.Compare(a.Window, b.Window) })
	return keys
}

// expireNotice lets a notice be raised again once its reset time has passed.
func expireNotice(n *Notice, now time.Time) {
	if !n.ResetAt.IsZero() && !n.ResetAt.After(now) {
		n.Raised = false
	}
}
