package metadata

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/outbound"
)

func TestRetryAfterMinimumAndInvalidValues(t *testing.T) {
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, raw string
		minimum   time.Duration
		valid     bool
	}{
		{"absent", "", 250 * time.Millisecond, true}, {"seconds", "2", 2 * time.Second, true},
		{"zero", "0", 250 * time.Millisecond, true}, {"trim", " 2 ", 2 * time.Second, true},
		{"http_date", now.Add(3 * time.Second).Format(http.TimeFormat), 3 * time.Second, true},
		{"past_date", now.Add(-time.Hour).Format(http.TimeFormat), 250 * time.Millisecond, true},
		{"huge_valid", "86400", 24 * time.Hour, true}, {"overflow", strings.Repeat("9", 80), 0, false},
		{"oversize", strings.Repeat("9", 129), 0, false}, {"negative", "-1", 0, false},
		{"plus", "+1", 0, false}, {"decimal", "1.2", 0, false}, {"secret", "secret internal-address", 0, false},
		{"empty_present", " ", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, ok := retryDelay(tc.raw, now, 0)
			if ok != tc.valid || ok && d < tc.minimum {
				t.Fatalf("delay=%v valid=%v", d, ok)
			}
		})
	}
	for attempt := 0; attempt < 2; attempt++ {
		base := 250 * time.Millisecond * time.Duration(1<<attempt)
		for i := 0; i < 20; i++ {
			d, ok := retryDelay("", now, attempt)
			if !ok || d < base || d > base+base/4 {
				t.Fatalf("backoff=%v", d)
			}
		}
	}
}

func TestAuthenticationRetriesAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		statuses   []int
		header     string
		fetchErr   error
		want       error
		calls      int
		cancelWait bool
	}{
		{"rate_then_success", []int{429, 200}, "2", nil, nil, 2, false},
		{"server_then_success", []int{503, 200}, "", nil, nil, 2, false},
		{"exhaust_rate", []int{429, 429, 429}, "", nil, ErrRateLimited, 3, false},
		{"exhaust_server", []int{500, 502, 504}, "", nil, ErrUnavailable, 3, false},
		{"auth_not_retried", []int{401}, "", nil, ErrCredentials, 1, false},
		{"denied_not_retried", nil, "", outbound.ErrDenied, ErrUnavailable, 1, false},
		{"network_not_retried", nil, "", outbound.ErrUnavailable, ErrUnavailable, 1, false},
		{"invalid_header", []int{429}, "secret", nil, ErrRateLimited, 1, false},
		{"outside_budget", []int{429}, "86400", nil, ErrRateLimited, 1, false},
		{"cancel_wait", []int{429}, "2", nil, context.Canceled, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := NewTMDB(testKey)
			defer c.Close()
			c.governor = nil
			calls := 0
			waits := 0
			c.fetch = func(context.Context, string, int64) (outbound.Response, error) {
				calls++
				if tc.fetchErr != nil {
					return outbound.Response{}, tc.fetchErr
				}
				return outbound.Response{Status: tc.statuses[calls-1], Body: []byte(`{"success":true,"status_code":1}`), RetryAfter: tc.header}, nil
			}
			c.wait = func(ctx context.Context, d time.Duration) error {
				waits++
				if tc.header == "2" && d < 2*time.Second {
					t.Fatal("retried before server minimum")
				}
				if tc.cancelWait {
					return context.Canceled
				}
				return nil
			}
			if err := c.ValidateCredentials(context.Background()); !errors.Is(err, tc.want) || calls != tc.calls {
				t.Fatalf("error=%v calls=%d", err, calls)
			}
			if tc.calls > 1 && waits != tc.calls-1 {
				t.Fatalf("waits=%d", waits)
			}
		})
	}
}

func TestRetryWaitStopsOnDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := waitRetry(ctx, time.Second); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v", err)
	}
	ctx2, stop := context.WithCancel(context.Background())
	stop()
	if err := waitRetry(ctx2, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}
