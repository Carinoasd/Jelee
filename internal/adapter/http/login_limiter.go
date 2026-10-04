package httpapi

import (
	"container/heap"
	"crypto/sha256"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

// LoginLimiterOptions bounds the combined number of IP and username buckets.
// Each bucket has a fixed Window starting at its first attempt. Now is optional;
// an injected clock must not call back into the limiter.
type LoginLimiterOptions struct {
	Window     time.Duration
	IPLimit    int
	UserLimit  int
	MaxEntries int
	Now        func() time.Time
	// Relaxed, when set and true, admits every well-formed attempt without
	// counting it: the developer mode relax_login_rate_limit toggle (G45.4).
	// Account lockout after failed passwords still applies.
	Relaxed func() bool
}

// LoginLimiter limits password work before a KDF runs. It is process-local and
// starts empty after a restart. It owns no goroutines or timers.
type LoginLimiter struct {
	mu      sync.Mutex
	opts    LoginLimiterOptions
	entries map[loginBucketKey]*loginBucket
	expires loginExpiryHeap
}

type loginBucketKey struct {
	kind   byte
	digest [sha256.Size]byte
}

type loginBucket struct {
	key     loginBucketKey
	count   int
	expires time.Time
}

type loginExpiryHeap []*loginBucket

func (h loginExpiryHeap) Len() int           { return len(h) }
func (h loginExpiryHeap) Less(i, j int) bool { return h[i].expires.Before(h[j].expires) }
func (h loginExpiryHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *loginExpiryHeap) Push(v any)        { *h = append(*h, v.(*loginBucket)) }
func (h *loginExpiryHeap) Pop() any {
	old := *h
	n := len(old) - 1
	v := old[n]
	old[n] = nil
	*h = old[:n]
	return v
}

func NewLoginLimiter(opts LoginLimiterOptions) (*LoginLimiter, error) {
	if opts.Window <= 0 || opts.IPLimit < 1 || opts.UserLimit < 1 || opts.MaxEntries < 2 {
		return nil, errors.New("invalid login limiter options")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &LoginLimiter{opts: opts, entries: make(map[loginBucketKey]*loginBucket)}, nil
}

// Allow consumes both attempt budgets atomically, including rejected attempts
// where either existing bucket is already exhausted. Counts saturate at their
// limits. A full store rejects new pairs without partially creating buckets or
// evicting active entries. retry is positive on denial, zero on acceptance.
//
// ip must be the canonical, unmapped address returned by ClientIP. Forwarded
// headers are not trusted. Usernames are trimmed/lowercased before hashing; no
// original username or IP string is retained in the store.
func (l *LoginLimiter) Allow(ip, name string) (allowed bool, retry time.Duration) {
	if l == nil {
		return false, time.Second
	}
	addr, err := netip.ParseAddr(ip)
	if err != nil || addr.Zone() != "" || addr.Unmap().String() != ip {
		return false, l.opts.Window
	}
	if l.opts.Relaxed != nil && l.opts.Relaxed() {
		return true, 0
	}
	keys := [2]loginBucketKey{
		{kind: 'i', digest: sha256.Sum256([]byte(ip))},
		{kind: 'u', digest: sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(name))))},
	}
	limits := [2]int{l.opts.IPLimit, l.opts.UserLimit}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.opts.Now()
	for len(l.expires) > 0 && !l.expires[0].expires.After(now) {
		old := heap.Pop(&l.expires).(*loginBucket)
		delete(l.entries, old.key)
	}
	needed := 0
	for _, key := range keys {
		if l.entries[key] == nil {
			needed++
		}
	}
	if needed > l.opts.MaxEntries-len(l.entries) {
		return false, l.expires[0].expires.Sub(now)
	}
	allowed = true
	for i, key := range keys {
		bucket := l.entries[key]
		if bucket == nil {
			bucket = &loginBucket{key: key, expires: now.Add(l.opts.Window)}
			l.entries[key] = bucket
			heap.Push(&l.expires, bucket)
		}
		if bucket.count >= limits[i] {
			allowed = false
		} else {
			bucket.count++
		}
	}
	if !allowed {
		for i, key := range keys {
			bucket := l.entries[key]
			if remaining := bucket.expires.Sub(now); bucket.count >= limits[i] && remaining > retry {
				retry = remaining
			}
		}
	}
	return allowed, retry
}

// ClientIP uses only the transport peer. Invalid addresses fail closed via an
// empty result. Zones and IPv4-mapped IPv6 forms cannot create extra buckets.
func ClientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	host, port, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || port == "" {
		return ""
	}
	for _, c := range port {
		if c < '0' || c > '9' {
			return ""
		}
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil || n == 0 {
		return ""
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return ""
	}
	return addr.WithZone("").Unmap().String()
}
