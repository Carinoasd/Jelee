package media

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// Clock abstracts time for stream limits and revocation checks so tests can
// drive them without sleeping.
type Clock interface {
	Now() time.Time
	NewTimer(time.Duration) Timer
}

type Timer interface {
	C() <-chan time.Time
	Stop() bool
}

type systemClock struct{}

func (systemClock) Now() time.Time                 { return time.Now() }
func (systemClock) NewTimer(d time.Duration) Timer { return systemTimer{time.NewTimer(d)} }

type systemTimer struct{ timer *time.Timer }

func (t systemTimer) C() <-chan time.Time { return t.timer.C }
func (t systemTimer) Stop() bool          { return t.timer.Stop() }

// Limits configures per-user direct delivery limits (G07.4). Each limit has
// its own switch (G45.4); a disabled limit ignores both its server-wide value
// and every per-user override.
type Limits struct {
	// StreamLimit enables concurrent playback limits.
	StreamLimit bool
	// MaxStreamsPerUser is the server-wide number of distinct playbacks a user
	// may run at once; 0 leaves users without an override unlimited.
	MaxStreamsPerUser int
	// MaxStreamsPerDevice bounds distinct playbacks per device of a user; 0
	// disables the device limit. It has no per-user override.
	MaxStreamsPerDevice int
	// BandwidthLimit enables the bandwidth limit.
	BandwidthLimit bool
	// MaxKbpsPerUser is the server-wide rate in kilobits per second shared by
	// all streams of a user; 0 leaves users without an override unlimited.
	MaxKbpsPerUser int64
	// BandwidthPerDevice gives each device of a user its own rate instead of
	// one rate shared by all of the user's devices.
	BandwidthPerDevice bool
	// RelaxStreams and RelaxBandwidth, when set, suspend the concurrency and
	// bandwidth limits for playbacks admitted while they return true: the
	// developer mode toggles (G45.4). A playback keeps what it was admitted
	// with; limits apply again to the next admission after they end.
	RelaxStreams   func() bool
	RelaxBandwidth func() bool
}

func (l Limits) validate() error {
	if l.MaxStreamsPerUser < 0 || l.MaxStreamsPerUser > domain.MaxDeliveryStreams || l.MaxStreamsPerDevice < 0 || l.MaxStreamsPerDevice > domain.MaxDeliveryStreams || l.MaxKbpsPerUser < 0 || l.MaxKbpsPerUser > domain.MaxDeliveryKbps {
		return fmt.Errorf("direct delivery limits: %w", ErrInvalidRequest)
	}
	return nil
}

const (
	// throttleChunk bounds one rate-limited network write so a large write
	// cannot overshoot the bucket by a whole copy buffer.
	throttleChunk = 16 << 10
	// burstWindow is how much unused rate a bucket keeps, so a stream that
	// briefly stalls may catch up without exceeding the rate over time.
	burstWindow = 250 * time.Millisecond
)

// limiter admits streams against the concurrency limits and hands out shared
// bandwidth buckets. Its state is per process: with several instances each
// enforces the limits for the streams it serves.
type limiter struct {
	limits  Limits
	clock   Clock
	mu      sync.Mutex
	users   map[string]*userStreams
	buckets map[string]*tokenBucket
}

// userStreams counts distinct playbacks: requests of one session for one
// source share a key, so a player's overlapping Range requests and seeks
// count once.
type userStreams struct {
	streams map[string]*activeStream
	devices map[string]int
}

type activeStream struct {
	device string
	refs   int
}

// admission is held by one request for the duration of its response.
type admission struct {
	limiter   *limiter
	user, key string
	counted   bool
	bucketKey string
	bucket    *tokenBucket
}

func newLimiter(limits Limits, clock Clock) *limiter {
	return &limiter{limits: limits, clock: clock, users: map[string]*userStreams{}, buckets: map[string]*tokenBucket{}}
}

// admit applies the concurrency limits and returns the bandwidth bucket of the
// stream, if any. A nil admission means no limit applies.
func (l *limiter) admit(p access.Principal, sourceID string, source Source) (*admission, error) {
	streamLimit, kbps := 0, int64(0)
	streams := l.limits.StreamLimit && (l.limits.RelaxStreams == nil || !l.limits.RelaxStreams())
	if streams {
		streamLimit = l.limits.MaxStreamsPerUser
		if source.Limits.MaxStreams != nil {
			streamLimit = *source.Limits.MaxStreams
		}
	}
	if l.limits.BandwidthLimit && (l.limits.RelaxBandwidth == nil || !l.limits.RelaxBandwidth()) {
		kbps = l.limits.MaxKbpsPerUser
		if source.Limits.MaxKbps != nil {
			kbps = *source.Limits.MaxKbps
		}
	}
	deviceLimit := 0
	if streams {
		deviceLimit = l.limits.MaxStreamsPerDevice
	}
	if streamLimit == 0 && deviceLimit == 0 && kbps == 0 {
		return nil, nil
	}
	// A session without a reported device ID is its own device.
	device := "d:" + source.DeviceID
	if source.DeviceID == "" {
		device = "s:" + p.SessionID
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	a := &admission{limiter: l, user: p.UserID, key: p.SessionID + "\x00" + sourceID}
	if streamLimit > 0 || deviceLimit > 0 {
		user := l.users[p.UserID]
		if user == nil {
			user = &userStreams{streams: map[string]*activeStream{}, devices: map[string]int{}}
		}
		if stream := user.streams[a.key]; stream != nil {
			stream.refs++
		} else {
			if streamLimit > 0 && len(user.streams) >= streamLimit {
				return nil, ErrUserStreamLimit
			}
			if deviceLimit > 0 && user.devices[device] >= deviceLimit {
				return nil, ErrDeviceStreamLimit
			}
			user.streams[a.key] = &activeStream{device: device, refs: 1}
			user.devices[device]++
		}
		l.users[p.UserID] = user
		a.counted = true
	}
	if kbps > 0 {
		a.bucketKey = p.UserID
		if l.limits.BandwidthPerDevice {
			a.bucketKey += "\x00" + device
		}
		now := l.clock.Now()
		bucket := l.buckets[a.bucketKey]
		if bucket == nil {
			bucket = newTokenBucket(kbps, now)
			l.buckets[a.bucketKey] = bucket
		} else {
			// The newest admission carries the current override.
			bucket.setRate(kbps, now)
		}
		bucket.refs++
		a.bucket = bucket
	}
	return a, nil
}

func (a *admission) release() {
	if a == nil {
		return
	}
	l := a.limiter
	l.mu.Lock()
	defer l.mu.Unlock()
	if a.counted {
		user := l.users[a.user]
		stream := user.streams[a.key]
		if stream.refs--; stream.refs == 0 {
			delete(user.streams, a.key)
			if user.devices[stream.device]--; user.devices[stream.device] == 0 {
				delete(user.devices, stream.device)
			}
		}
		if len(user.streams) == 0 {
			delete(l.users, a.user)
		}
	}
	if a.bucket != nil {
		if a.bucket.refs--; a.bucket.refs == 0 && l.buckets[a.bucketKey] == a.bucket {
			delete(l.buckets, a.bucketKey)
		}
	}
}

// throttle returns the rate limiter for the response writer, or nil when the
// stream is not rate limited. A nil throttle is the unlimited fast path.
func (a *admission) throttle() *throttle {
	if a == nil || a.bucket == nil {
		return nil
	}
	return &throttle{bucket: a.bucket, clock: a.limiter.clock}
}

// tokenBucket is shared by all streams with the same bucket key. Tokens may
// go negative: a reservation always succeeds and returns how long the writer
// must wait, which keeps concurrent streams in arrival order.
type tokenBucket struct {
	mu     sync.Mutex
	rate   float64 // bytes per second
	burst  float64
	tokens float64
	last   time.Time
	refs   int // guarded by limiter.mu
}

func newTokenBucket(kbps int64, now time.Time) *tokenBucket {
	b := &tokenBucket{last: now}
	b.configure(kbps)
	b.tokens = b.burst
	return b
}

func (b *tokenBucket) configure(kbps int64) {
	b.rate = float64(kbps) * 1000 / 8
	b.burst = max(b.rate*burstWindow.Seconds(), throttleChunk)
}

func (b *tokenBucket) setRate(kbps int64, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refill(now)
	b.configure(kbps)
	b.tokens = min(b.tokens, b.burst)
}

func (b *tokenBucket) refill(now time.Time) {
	if elapsed := now.Sub(b.last); elapsed > 0 {
		b.tokens = min(b.burst, b.tokens+elapsed.Seconds()*b.rate)
		b.last = now
	}
}

// reserve takes n bytes and returns how long to wait before sending them.
func (b *tokenBucket) reserve(n int, now time.Time) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refill(now)
	b.tokens -= float64(n)
	if b.tokens >= 0 {
		return 0
	}
	return time.Duration(-b.tokens / b.rate * float64(time.Second))
}

type throttle struct {
	bucket *tokenBucket
	clock  Clock
}

func (t *throttle) wait(ctx context.Context, n int) error {
	delay := t.bucket.reserve(n, t.clock.Now())
	if delay <= 0 {
		return ctx.Err()
	}
	timer := t.clock.NewTimer(delay)
	select {
	case <-ctx.Done():
		timer.Stop()
		return ctx.Err()
	case <-timer.C():
		return ctx.Err()
	}
}
