package metadata

import (
	"context"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/outbound"
)

func (t *TMDB) authenticationRequest(ctx context.Context, target string) (outbound.Response, error) {
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return outbound.Response{}, err
		}
		r, err := t.fetch(ctx, target, 4096)
		// Transport/security/size failures are not blindly retried. Only
		// provider responses can authorize a retry in this startup phase.
		if err != nil {
			return outbound.Response{}, err
		}
		if r.Status != 429 && (r.Status < 500 || r.Status > 599) || attempt == 2 {
			return r, nil
		}
		delay, ok := retryDelay(r.RetryAfter, t.now(), attempt)
		if !ok {
			return r, nil
		}
		if deadline, ok := ctx.Deadline(); ok && delay >= time.Until(deadline) {
			return r, nil
		}
		if err := t.wait(ctx, delay); err != nil {
			return outbound.Response{}, err
		}
	}
	return outbound.Response{}, ErrUnavailable
}

func retryDelay(raw string, now time.Time, attempt int) (time.Duration, bool) {
	base := 250 * time.Millisecond * time.Duration(1<<attempt)
	backoff := base + time.Duration(rand.Int64N(int64(base)/4+1))
	if raw == "" {
		return backoff, true
	}
	if len(raw) > 128 {
		return 0, false
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	var delay time.Duration
	digits := true
	for _, ch := range raw {
		if ch < '0' || ch > '9' {
			digits = false
			break
		}
	}
	if digits {
		seconds, err := strconv.ParseUint(raw, 10, 64)
		// Do not overflow or shorten a large valid server minimum.
		if err != nil || seconds > uint64((1<<63-1)/int64(time.Second)) {
			return 0, false
		}
		delay = time.Duration(seconds) * time.Second
	} else {
		at, err := http.ParseTime(raw)
		if err != nil {
			return 0, false
		}
		delay = at.Sub(now)
	}
	if delay > backoff {
		return delay, true
	}
	return backoff, true
}

func waitRetry(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
