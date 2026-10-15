package state

import (
	"context"
	"errors"
	"time"
)

// updateProviders reads providers.json, lets fn change the body and saves it, all under the record's lock.
// fn returns false when it changed nothing, and then nothing is written.
func (m *Machine) updateProviders(fn func(b *ProvidersBody) bool) error {
	return m.update(KindProviders, func(can any) bool { return fn(can.(*ProvidersBody)) })
}

// link returns a link's record, adding an empty one when the link is new.
func link(b *ProvidersBody, name string) LinkRecord {
	if b.Links == nil {
		b.Links = map[string]LinkRecord{}
	}
	return b.Links[name]
}

// ResetSessions sets every link's session count to 0; the engine calls it at start.
func (m *Machine) ResetSessions() error {
	return m.updateProviders(func(b *ProvidersBody) bool {
		changed := false
		for n, l := range b.Links {
			if l.Sessions != 0 {
				l.Sessions = 0
				b.Links[n] = l
				changed = true
			}
		}
		return changed
	})
}

// ResetAt returns when a link's limit lifts; false when the link has no recorded limit or the record cannot be read.
func (m *Machine) ResetAt(name string) (time.Time, bool) {
	r, err := LoadMachine[ProvidersBody](m, KindProviders)
	if err != nil {
		return time.Time{}, false
	}
	at := r.Body.Links[name].ResetAt
	return at, !at.IsZero()
}

// Record stores when a link's limit lifts and which cap was hit.
func (m *Machine) Record(name string, reset time.Time, capKind string) error {
	return m.updateProviders(func(b *ProvidersBody) bool {
		l := link(b, name)
		l.ResetAt, l.Cap = reset, capKind
		b.Links[name] = l
		return true
	})
}

// TakeSlot counts one more live session on the link; false when the link already has most.
func (m *Machine) TakeSlot(name string, most int) (bool, error) {
	taken := false
	err := m.updateProviders(func(b *ProvidersBody) bool {
		l := link(b, name)
		if l.Sessions >= most {
			taken = false
			return false
		}
		l.Sessions++
		b.Links[name] = l
		taken = true
		return true
	})
	return taken && err == nil, err
}

// ReleaseSlot counts one session fewer on the link, never below 0.
func (m *Machine) ReleaseSlot(name string) error {
	return m.updateProviders(func(b *ProvidersBody) bool {
		l, ok := b.Links[name]
		if !ok || l.Sessions == 0 {
			return false
		}
		l.Sessions--
		b.Links[name] = l
		return true
	})
}

// LoadWindows returns every stored window record.
func (m *Machine) LoadWindows(ctx context.Context) (map[WindowKey]WindowRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r, err := LoadMachine[ProvidersBody](m, KindProviders)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	out := map[WindowKey]WindowRecord{}
	for ln, l := range r.Body.Links {
		for wn, w := range l.Windows {
			out[WindowKey{Link: ln, Window: wn}] = w
		}
	}
	return out, nil
}

// SaveWindow stores one window record under its link, leaving the rest of the link's record as it is.
func (m *Machine) SaveWindow(ctx context.Context, k WindowKey, rec WindowRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return m.updateProviders(func(b *ProvidersBody) bool {
		l := link(b, k.Link)
		if l.Windows == nil {
			l.Windows = map[string]WindowRecord{}
		}
		l.Windows[k.Window] = rec
		b.Links[k.Link] = l
		return true
	})
}
