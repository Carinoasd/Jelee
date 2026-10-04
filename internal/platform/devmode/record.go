package devmode

import (
	"slices"
	"time"
)

// Record is the persisted developer-mode session every instance shares. The
// State machine stays the only place transitions are decided: a Controller
// restores a State from the Record, applies one transition and persists the
// result together with the emitted events.
type Record struct {
	Active    bool
	EnabledAt time.Time
	ExpiresAt time.Time
	Source    string
	// Toggles lists the switched-on toggles in catalogue order.
	Toggles []Toggle
	// Version increases with every persisted change.
	Version int64
}

// restore rebuilds a State from a persisted record. Unknown toggles, for
// example from a newer binary, are dropped: they can never take effect here.
func restore(clock Clock, ttl time.Duration, rec Record, observer Observer) *State {
	s := &State{clock: clock, observer: observer, ttl: ttl, toggles: map[Toggle]bool{}}
	if rec.Active {
		s.active, s.enabledAt, s.expiresAt, s.source = true, rec.EnabledAt, rec.ExpiresAt, rec.Source
		for _, t := range rec.Toggles {
			if t.Known() {
				s.toggles[t] = true
			}
		}
	}
	return s
}

// record snapshots s for persistence; the caller sets Version.
func (s *State) record() Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.statusLocked()
	return Record{Active: st.Active, EnabledAt: st.EnabledAt, ExpiresAt: st.ExpiresAt, Source: st.Source, Toggles: st.Toggles}
}

// sameSession reports whether two records describe the same session,
// ignoring Version.
func sameSession(a, b Record) bool {
	if a.Active != b.Active {
		return false
	}
	if !a.Active {
		return true
	}
	return a.EnabledAt.Equal(b.EnabledAt) && a.ExpiresAt.Equal(b.ExpiresAt) && a.Source == b.Source && slices.Equal(a.Toggles, b.Toggles)
}

// statusOf converts a record into a Status after applying expiry at now.
func statusOf(rec Record, now time.Time) Status {
	if !rec.Active || !now.Before(rec.ExpiresAt) {
		return Status{}
	}
	return Status{Active: true, EnabledAt: rec.EnabledAt, ExpiresAt: rec.ExpiresAt, Source: rec.Source, Toggles: slices.Clone(rec.Toggles)}
}
