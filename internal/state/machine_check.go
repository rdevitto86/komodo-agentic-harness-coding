package state

import (
	"fmt"
	"sort"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

// installHosts lists the hosts install may be asked to set up.
var installHosts = []string{"claude", "codex"}

// normalizeMachine puts a decoded machine-wide body in its stored form: probes compacted and times
// in UTC.
func normalizeMachine(can any) {
	switch b := can.(type) {
	case *ProvidersBody:
		for name, l := range b.Links {
			if p, ok := objectOrEmpty(l.Probe); ok {
				l.Probe = p
			}

			l.ResetAt = l.ResetAt.UTC()
			for wn, w := range l.Windows {
				w.ResetAt = w.ResetAt.UTC()
				l.Windows[wn] = w
			}

			b.Links[name] = l
		}
	case *UpdateBody:
		b.CheckedAt = b.CheckedAt.UTC()
	case *ScheduleBody:
		for i := range b.Entries {
			e := &b.Entries[i]
			e.At = e.At.UTC()
			e.QueuedAt = e.QueuedAt.UTC()
			e.Start.Schedule = e.Start.Schedule.UTC()
		}
	}
}

// validateMachine runs the rules of one machine-wide record on its normalised body.
func validateMachine(k MachineKind, can any) error {
	f := func(field, why string) error { return invalid(string(k), "body."+field, why) }

	switch b := can.(type) {
	case *ProvidersBody:
		return validateProviders(b, f)
	case *UpdateBody:
		versions := []struct{ name, val string }{
			{"latest", b.Latest},
			{"previous", b.Previous},
		}

		for _, v := range versions {
			if _, err := core.ParseSemVer(v.val); v.val != "" && err != nil {
				return f(v.name, fmt.Sprintf("%q is not a version", v.val))
			}
		}

		if b.CheckedAt.IsZero() {
			return f("checked_at", "empty")
		}
	case *InstallBody:
		for i, e := range b.Entries {
			p := fmt.Sprintf("entries[%d].", i)

			switch {
			case e.Target == "":
				return f(p+"target", "empty")
			case !isHash(e.Hash):
				return f(p+"hash", "not 64 lowercase hexadecimal characters")
			case e.Step < 1:
				return f(p+"step", "below 1")
			}
		}

		for i, h := range b.Selections.Hosts {
			if !oneOf(h, installHosts) {
				return f(fmt.Sprintf("selections.hosts[%d]", i), fmt.Sprintf("%q is not a host", h))
			}
		}
	case *ScheduleBody:
		for i, e := range b.Entries {
			p := fmt.Sprintf("entries[%d].", i)

			switch {
			case e.Target == "":
				return f(p+"target", "empty")
			case e.Root == "":
				return f(p+"root", "empty")
			case e.RunID == "":
				return f(p+"run_id", "empty")
			case e.At.IsZero():
				return f(p+"at", "empty")
			case e.QueuedAt.IsZero():
				return f(p+"queued_at", "empty")
			case len(e.Start.Groups) == 0:
				return f(p+"start.groups", "empty")
			}
		}
	}

	return nil
}

// validateProviders checks every link and window of providers.json.
func validateProviders(b *ProvidersBody, f func(field, why string) error) error {
	names := make([]string, 0, len(b.Links))
	for n := range b.Links {
		names = append(names, n)
	}

	sort.Strings(names)

	for _, n := range names {
		l := b.Links[n]

		p := "links." + n
		switch {
		case n == "":
			return f("links", "a link has no name")
		case !validProbe(l):
			return f(p+".probe", "not a JSON object")
		case l.Sessions < 0:
			return f(p+".sessions", "negative")
		case !oneOf(l.Cap, fieldEnums["LinkRecord.Cap"]):
			return f(p+".cap", fmt.Sprintf("%q is not a cap", l.Cap))
		}

		wins := make([]string, 0, len(l.Windows))
		for wn := range l.Windows {
			wins = append(wins, wn)
		}

		sort.Strings(wins)

		for _, wn := range wins {
			w := l.Windows[wn]
			switch {
			case wn == "":
				return f(p+".windows", "a window has no name")
			case w.PercentUsed < 0:
				return f(p+".windows."+wn+".percent_used", "negative")
			case w.Sessions < 0:
				return f(p+".windows."+wn+".sessions", "negative")
			}
		}
	}

	return nil
}

// validProbe reports whether a link's probe is a JSON object.
func validProbe(l LinkRecord) bool {
	_, ok := objectOrEmpty(l.Probe)

	return ok
}
