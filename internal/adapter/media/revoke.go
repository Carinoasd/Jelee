package media

import (
	"context"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
)

// SessionChecker reports whether a session may keep streaming. It must read
// shared state (the database) so a revocation made through any instance
// reaches the streams every instance serves (G07.4).
type SessionChecker interface {
	SessionActive(ctx context.Context, userID, sessionID string) (bool, error)
}

const (
	// DefaultSessionCheckInterval is how often a running stream rechecks its
	// session when Options.SessionCheckInterval is zero.
	DefaultSessionCheckInterval = 5 * time.Second
	// maxSessionCheckFailures consecutive failed checks end a stream: a
	// session that cannot be confirmed for that long is treated as revoked,
	// while a single slow or failed query does not interrupt playback.
	maxSessionCheckFailures = 3
)

// watchSession rechecks the session every interval until ctx ends. It cancels
// the stream with ErrSessionRevoked when the session is no longer active or
// cannot be confirmed maxSessionCheckFailures times in a row.
func (h *Handler) watchSession(ctx context.Context, stop context.CancelCauseFunc, p access.Principal, done chan<- struct{}) {
	defer close(done)
	failures := 0
	for {
		timer := h.clock.NewTimer(h.options.SessionCheckInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C():
		}
		checkCtx, cancel := context.WithTimeout(ctx, h.options.LookupTimeout)
		active, err := h.options.Sessions.SessionActive(checkCtx, p.UserID, p.SessionID)
		cancel()
		if ctx.Err() != nil {
			return
		}
		switch {
		case err != nil:
			if failures++; failures < maxSessionCheckFailures {
				continue
			}
		case active:
			failures = 0
			continue
		}
		stop(ErrSessionRevoked)
		return
	}
}
