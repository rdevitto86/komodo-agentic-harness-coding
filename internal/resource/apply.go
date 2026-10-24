package resource

import (
	"errors"
	"fmt"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

// errUnknownSubtype marks a resource event of a subtype the Resource Manager does not read.
var errUnknownSubtype = errors.New("unknown resource subtype")

// apply updates the totals from a checked event and returns what it changed and the window keys whose figures moved; ok is
// false for an event it ignores.
func (s *State) apply(e bus.Event, now time.Time) (o Observation, keys []WindowKey, ok bool) {
	run, group := string(e.Run), string(e.Group)
	o = Observation{Subtype: e.Subtype, Run: run, Group: group, Now: now}

	switch d := e.Data.(type) {
	case *Usage:
		o.Link = d.Link
		keys = s.applyUsage(*d, run, group, now)
	case *Headroom:
		if d.Limit <= 0 {
			return o, nil, false
		}
		o.Link, o.Key = d.Link, WindowKey{Link: d.Link, Window: d.Window}
		s.applyHeadroom(*d, now)
		keys = []WindowKey{o.Key}
	case *Overage:
		o.Link, o.Key = d.Link, WindowKey{Link: d.Link}
		s.applyOverage(*d, now)
	case *Limit:
		o.Link, o.Key = d.Link, WindowKey{Link: d.Link, Window: d.Kind}
		s.applyLimit(*d, now)
	default:
		return o, nil, false
	}

	s.seen(o.Link, run, group)
	return o, keys, true
}

// check refuses an event whose subtype it does not read, or whose data is missing or unusable;
// it changes nothing.
func check(e bus.Event) error {
	bad := func(why string) error {
		return fmt.Errorf("resource %s event: %s: %w", e.Subtype, why, errMalformed)
	}

	switch d := e.Data.(type) {
	case *Usage:
		if e.Subtype != subtypeUsage {
			return bad("data does not match the subtype")
		}
		if d.Link == "" || e.Run == "" {
			return bad("link or run is empty")
		}
		if d.TokensIn < 0 || d.TokensOut < 0 || d.CacheRead < 0 || d.CacheWrite < 0 || d.USD < 0 || d.USDEquiv < 0 {
			return bad("a count or a cost is negative")
		}
	case *Headroom:
		if e.Subtype != subtypeHeadroom {
			return bad("data does not match the subtype")
		}
		if d.Link == "" || d.Window == "" {
			return bad("link or window is empty")
		}
		if d.Limit < 0 || d.Remaining < 0 || d.Remaining > d.Limit {
			return bad("remaining is outside the window total")
		}
	case *Overage:
		if e.Subtype != subtypeOverage {
			return bad("data does not match the subtype")
		}
		if d.Link == "" {
			return bad("link is empty")
		}
	case *Limit:
		if e.Subtype != subtypeLimit {
			return bad("data does not match the subtype")
		}
		if d.Link == "" || d.Kind == "" {
			return bad("link or kind is empty")
		}
	default:
		switch e.Subtype {
		case subtypeUsage, subtypeHeadroom, subtypeOverage, subtypeLimit:
			return bad("data is missing")
		}
		return fmt.Errorf("%q: %w", e.Subtype, errUnknownSubtype)
	}

	return nil
}
