package resource

import "time"

// WindowKey names one window on one link.
type WindowKey struct {
	Link   string
	Window string
}

// WindowState is what the Resource Manager remembers about one window.
type WindowState struct {
	PercentUsed float64   // 100 minus the percent of headroom
	ResetAt     time.Time // when the window resets; a past time means it has
	Sessions    int       // sessions finished in this window
	Warned      bool      // the warn error was raised in this window
	Full        bool      // the window_full error was raised in this window
	Paused      bool      // the pause order for this window was delivered
	Limited     bool      // the limit error was raised in this window
	Overage     bool      // the overage warning was raised in this window
	Reading     bool      // a headroom reading or a stored record set this window; only these are stored
}

// overageKey names the link-level entry that holds a link's overage flag.
func overageKey(link string) WindowKey { return WindowKey{Link: link, Window: "overage"} }

// limitKey names the link-level entry that holds the limit flag of one cap kind.
func limitKey(link, kind string) WindowKey { return WindowKey{Link: link, Window: "limit:" + kind} }

// window returns the state of k, creating it when the link has never been heard from.
func (s *State) window(k WindowKey) *WindowState {
	w := s.Windows[k]
	if w == nil {
		w = &WindowState{}
		s.Windows[k] = w
	}
	return w
}

// expire clears a window's flags once its reset time has passed; a reading window also drops its session count and its link's group list.
func (s *State) expire(k WindowKey, now time.Time) {
	w := s.Windows[k]
	if w == nil || w.ResetAt.IsZero() || w.ResetAt.After(now) {
		return
	}
	w.Warned, w.Full, w.Paused, w.Limited, w.Overage = false, false, false, false, false
	if w.Reading {
		w.Sessions = 0
		delete(s.LinkGroups, k.Link)
	}
}

// applyHeadroom sets a window's percent used and reset time from one reading; it ignores a reading with no window total.
func (s *State) applyHeadroom(h Headroom, now time.Time) {
	if h.Limit <= 0 {
		return
	}
	k := WindowKey{Link: h.Link, Window: h.Window}
	s.expire(k, now)
	w := s.window(k)
	w.PercentUsed = min(max(float64(h.Limit-h.Remaining)*100/float64(h.Limit), 0), 100)
	w.ResetAt = h.ResetAt
	w.Reading = true
}

// applyOverage records that a link draws paid credits and when its window resets.
func (s *State) applyOverage(o Overage, now time.Time) {
	k := overageKey(o.Link)
	s.expire(k, now)
	if !o.ResetAt.IsZero() {
		s.window(k).ResetAt = o.ResetAt
	}
	s.window(k)
}

// applyLimit records a provider refusal; a new reset time starts a new refusal.
func (s *State) applyLimit(l Limit, now time.Time) {
	k := limitKey(l.Link, l.Kind)
	s.expire(k, now)
	w := s.window(k)
	if !w.ResetAt.Equal(l.ResetAt) {
		w.Limited = false
	}
	w.ResetAt = l.ResetAt
}

// bumpSessions adds one finished session to each unreset window of a link and returns the keys it changed.
func (s *State) bumpSessions(link string, now time.Time) []WindowKey {
	var keys []WindowKey
	for k, w := range s.Windows {
		if k.Link != link || !w.Reading {
			continue
		}
		s.expire(k, now)
		if w.ResetAt.After(now) {
			w.Sessions++
			keys = append(keys, k)
		}
	}
	return keys
}
