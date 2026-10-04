package tracing

import (
	"sync"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// DefaultLinkEntries bounds the process link registry.
const DefaultLinkEntries = 4096

type linkKey struct{ kind, id string }

// Links remembers which span submitted a piece of background work (a job or
// a webhook event) so the worker that later claims it can continue the same
// trace. It is process-local, bounded and best effort: the oldest entry is
// evicted first, and work claimed by another instance or after a restart
// starts a new root instead (correlated by taskId or eventId).
type Links struct {
	mu      sync.Mutex
	limit   int
	entries map[linkKey]domain.SpanContext
	order   []linkKey
	next    int
}

// NewLinks returns a registry holding at most limit entries (at least one).
func NewLinks(limit int) *Links {
	if limit < 1 {
		limit = 1
	}
	return &Links{limit: limit, entries: make(map[linkKey]domain.SpanContext, limit), order: make([]linkKey, 0, limit)}
}

var defaultLinks = NewLinks(DefaultLinkEntries)

// DefaultLinks is the registry shared by producers and workers.
func DefaultLinks() *Links { return defaultLinks }

// Put records sc for (kind, id), replacing an earlier entry.
func (l *Links) Put(kind, id string, sc domain.SpanContext) {
	if l == nil || kind == "" || id == "" || len(id) > 128 || !sc.IsValid() {
		return
	}
	key := linkKey{kind, id}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.entries[key]; ok {
		l.entries[key] = sc
		return
	}
	if len(l.order) < l.limit {
		l.order = append(l.order, key)
	} else {
		delete(l.entries, l.order[l.next])
		l.order[l.next] = key
		l.next = (l.next + 1) % l.limit
	}
	l.entries[key] = sc
}

// Lookup returns the span remembered for (kind, id). Entries are kept after
// a lookup so a retried job joins the same trace again.
func (l *Links) Lookup(kind, id string) (domain.SpanContext, bool) {
	if l == nil {
		return domain.SpanContext{}, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	sc, ok := l.entries[linkKey{kind, id}]
	return sc, ok
}

// Len reports the number of remembered entries.
func (l *Links) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}
