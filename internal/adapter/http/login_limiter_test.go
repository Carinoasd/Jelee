package httpapi

import (
	"crypto/sha256"
	"fmt"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func limiterForTest(t *testing.T, ipLimit, userLimit, capacity int, now func() time.Time) *LoginLimiter {
	t.Helper()
	l, err := NewLoginLimiter(LoginLimiterOptions{Window: time.Minute, IPLimit: ipLimit, UserLimit: userLimit, MaxEntries: capacity, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestLoginLimiterOptions(t *testing.T) {
	valid := LoginLimiterOptions{Window: time.Minute, IPLimit: 1, UserLimit: 1, MaxEntries: 2}
	for _, mutate := range []func(*LoginLimiterOptions){
		func(o *LoginLimiterOptions) { o.Window = 0 },
		func(o *LoginLimiterOptions) { o.Window = -time.Second },
		func(o *LoginLimiterOptions) { o.IPLimit = 0 },
		func(o *LoginLimiterOptions) { o.UserLimit = -1 },
		func(o *LoginLimiterOptions) { o.MaxEntries = 1 },
	} {
		opts := valid
		mutate(&opts)
		if _, err := NewLoginLimiter(opts); err == nil {
			t.Fatal("invalid limiter options accepted")
		}
	}
	l, err := NewLoginLimiter(valid)
	if err != nil || l.opts.Now == nil {
		t.Fatal("default clock missing")
	}
	var missing *LoginLimiter
	if ok, retry := missing.Allow("192.0.2.1", "user"); ok || retry <= 0 {
		t.Fatal("nil limiter did not fail closed")
	}
}

func TestLoginLimiterBothBudgetsAndFixedExpiry(t *testing.T) {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	l := limiterForTest(t, 2, 2, 20, func() time.Time { return now })
	for _, pair := range [][2]string{{"192.0.2.1", "Alice"}, {"192.0.2.1", "Bob"}} {
		if ok, retry := l.Allow(pair[0], pair[1]); !ok || retry != 0 {
			t.Fatal("allowed attempt was rejected")
		}
	}
	now = now.Add(10 * time.Second)
	if ok, retry := l.Allow("192.0.2.1", "alice"); ok || retry != 50*time.Second {
		t.Fatalf("IP limit: allowed=%v retry=%v", ok, retry)
	}
	// The denied IP attempt also consumed Alice's remaining username budget.
	if ok, retry := l.Allow("192.0.2.2", " ALICE "); ok || retry != 50*time.Second {
		t.Fatalf("user limit across IP/case/space: allowed=%v retry=%v", ok, retry)
	}
	now = now.Add(50 * time.Second)
	if ok, retry := l.Allow("192.0.2.1", "alice"); !ok || retry != 0 {
		t.Fatalf("exact expiry did not reset window: allowed=%v retry=%v", ok, retry)
	}
	if len(l.entries) != 3 || len(l.expires) != 3 {
		t.Fatalf("expired entries were retained: map=%d heap=%d", len(l.entries), len(l.expires))
	}
}

func TestLoginLimiterRetryIncludesNewlyExhaustedBucket(t *testing.T) {
	now := time.Unix(1000, 0)
	l := limiterForTest(t, 1, 1, 10, func() time.Time { return now })
	l.Allow("192.0.2.1", "first")
	now = now.Add(50 * time.Second)
	if ok, retry := l.Allow("192.0.2.1", "second"); ok || retry != time.Minute {
		t.Fatalf("retry must include second username's consumed budget: %v %v", ok, retry)
	}
}

func TestLoginLimiterCapacityFloodAndRecovery(t *testing.T) {
	for _, distinct := range []string{"both", "users", "ips"} {
		t.Run(distinct, func(t *testing.T) {
			now := time.Unix(1000, 0)
			l := limiterForTest(t, 20000, 20000, 32, func() time.Time { return now })
			for i := 0; i < 10000; i++ {
				ip, name := fmt.Sprintf("198.18.%d.%d", i/256, i%256), fmt.Sprintf("private-user-%d", i)
				if distinct == "users" {
					ip = "192.0.2.1"
				}
				if distinct == "ips" {
					name = "one-user"
				}
				ok, retry := l.Allow(ip, name)
				if !ok && retry != time.Minute {
					t.Fatalf("capacity denial retry=%v", retry)
				}
				if len(l.entries) > 32 || len(l.expires) != len(l.entries) {
					t.Fatalf("unbounded or stale storage: map=%d heap=%d", len(l.entries), len(l.expires))
				}
			}
			now = now.Add(time.Minute)
			if ok, _ := l.Allow("203.0.113.1", "new-user"); !ok {
				t.Fatal("capacity did not recover after expiry")
			}
			if len(l.entries) != 2 || len(l.expires) != 2 {
				t.Fatal("expired flood buckets were not reclaimed")
			}
		})
	}
}

func TestLoginLimiterNoPartialAllocationOrEviction(t *testing.T) {
	now := time.Unix(1000, 0)
	l := limiterForTest(t, 1, 1, 3, func() time.Time { return now })
	l.Allow("192.0.2.1", "private-one")
	if ok, retry := l.Allow("192.0.2.2", "private-two"); ok || retry <= 0 {
		t.Fatal("pair exceeding capacity was accepted")
	}
	if len(l.entries) != 2 || len(l.expires) != 2 {
		t.Fatal("denial partially allocated a pair")
	}
	if ok, _ := l.Allow("192.0.2.1", "private-one"); ok {
		t.Fatal("active counters were evicted by capacity pressure")
	}
	key := loginBucketKey{kind: 'u', digest: sha256.Sum256([]byte("private-one"))}
	if l.entries[key] == nil || l.entries[key].count != 1 {
		t.Fatal("username hash lookup or saturated count is wrong")
	}
}

func TestLoginLimiterConcurrentAdmissionIsAtomic(t *testing.T) {
	l := limiterForTest(t, 37, 13, 100, nil)
	var admitted atomic.Int32
	var wait sync.WaitGroup
	for i := 0; i < 1000; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if ok, retry := l.Allow("192.0.2.1", "alice"); ok {
				admitted.Add(1)
				if retry != 0 {
					t.Error("admitted attempt has retry delay")
				}
			} else if retry <= 0 {
				t.Error("denied attempt has no retry delay")
			}
		}()
	}
	wait.Wait()
	if admitted.Load() != 13 || len(l.entries) != 2 || len(l.expires) != 2 {
		t.Fatalf("concurrent budget bypass or resource growth: admitted=%d entries=%d heap=%d", admitted.Load(), len(l.entries), len(l.expires))
	}
}

func TestClientIPIgnoresHeadersAndCanonicalizesPeer(t *testing.T) {
	for _, tc := range []struct{ peer, want string }{
		{"192.0.2.10:12345", "192.0.2.10"},
		{"[::ffff:192.0.2.10]:12345", "192.0.2.10"},
		{"[2001:0DB8:0:0::1]:443", "2001:db8::1"},
		{"[fe80::1%eth0]:12345", "fe80::1"},
		{"host.example:443", ""}, {"192.0.2.10", ""}, {"[::1]", ""},
		{"192.0.2.10:0", ""}, {"192.0.2.10:+80", ""}, {"192.0.2.10:65536", ""},
	} {
		r := httptest.NewRequest("POST", "/", nil)
		r.RemoteAddr = tc.peer
		r.Header.Set("Forwarded", "for=203.0.113.5")
		r.Header.Set("X-Forwarded-For", "203.0.113.6")
		r.Header.Set("X-Real-IP", "203.0.113.7")
		if got := ClientIP(r); got != tc.want {
			t.Errorf("peer %q: got %q want %q", tc.peer, got, tc.want)
		}
	}
	l := limiterForTest(t, 1, 1, 10, nil)
	for _, ip := range []string{"", "host.example", "192.0.2.1:80", "2001:DB8::1", "::ffff:192.0.2.1", "fe80::1%eth0"} {
		if ok, retry := l.Allow(ip, "alice"); ok || retry <= 0 {
			t.Fatalf("noncanonical IP accepted: %q", ip)
		}
	}
	if len(l.entries) != 0 || len(l.expires) != 0 || ClientIP(nil) != "" {
		t.Fatal("invalid peers left resources behind")
	}
}
