package metadata

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/outbound"
)

func TestGovernanceConfigurationValidationAndCapacity(t *testing.T) {
	c, _ := NewTMDB(testKey)
	defer c.Close()
	for _, g := range []Governance{
		{RequestInterval: 19 * time.Millisecond, MaxConcurrent: 4},
		{RequestInterval: time.Minute + 1, MaxConcurrent: 4},
		{RequestInterval: time.Second, MaxConcurrent: 0},
		{RequestInterval: time.Second, MaxConcurrent: 33},
		{RequestInterval: time.Second, MaxConcurrent: 1, DailyLimit: -1},
		{RequestInterval: time.Second, MaxConcurrent: 1, DailyLimit: MaxDailyLimit + 1},
	} {
		if err := c.WithGovernance(g); err != ErrGovernance {
			t.Fatalf("invalid governance accepted %+v", g)
		}
	}
	if c.governor.interval != 250*time.Millisecond || cap(c.governor.slots) != 4 || c.governor.daily != 0 {
		t.Fatal("rejected governance changed the default")
	}
	if err := c.WithGovernance(Governance{RequestInterval: 40 * time.Millisecond, MaxConcurrent: 2, DailyLimit: 9}); err != nil {
		t.Fatal(err)
	}
	if c.governor.interval != 40*time.Millisecond || cap(c.governor.slots) != 2 || c.governor.daily != 9 {
		t.Fatal("configured governance not applied")
	}
}

func TestGovernorDailyLimitCountsAttemptsAndResetsAtUTCMidnight(t *testing.T) {
	c, _ := NewTMDB(testKey)
	defer c.Close()
	if err := c.WithGovernance(Governance{RequestInterval: MinRequestInterval, MaxConcurrent: 1, DailyLimit: 3}); err != nil {
		t.Fatal(err)
	}
	// The day boundary follows UTC, not the local time zone.
	now := time.Date(2026, 10, 29, 23, 0, 0, 0, time.UTC)
	c.governor.now = func() time.Time { return now }
	c.governor.wait = func(_ context.Context, d time.Duration) error { now = now.Add(d); return nil }
	c.wait = func(context.Context, time.Duration) error { return nil }
	c.now = func() time.Time { return now }
	calls := 0
	statuses := []int{429, 200, 200, 200}
	c.fetch = func(context.Context, string, int64) (outbound.Response, error) {
		status := statuses[calls%len(statuses)]
		calls++
		return outbound.Response{Status: status, Body: []byte(findBody)}, nil
	}
	// A retried lookup spends two attempts.
	if _, err := c.FindByExternalID(context.Background(), domain.ExternalSourceTVDB, "81189", "en-US"); err != nil || calls != 2 {
		t.Fatalf("first lookup error=%v calls=%d", err, calls)
	}
	if _, err := c.FindByExternalID(context.Background(), domain.ExternalSourceTVDB, "81190", "en-US"); err != nil || calls != 3 {
		t.Fatalf("second lookup error=%v calls=%d", err, calls)
	}
	// The budget is spent: nothing is sent and the failure is not cached.
	for i := 0; i < 2; i++ {
		if _, err := c.FindByExternalID(context.Background(), domain.ExternalSourceTVDB, "81191", "en-US"); err == nil || calls != 3 {
			t.Fatalf("daily limit bypassed error=%v calls=%d", err, calls)
		}
	}
	if err := c.ValidateCredentials(context.Background()); !errors.Is(err, ErrUnavailable) || calls != 3 {
		t.Fatalf("credential check bypassed the daily limit: %v", err)
	}
	if len(c.governor.slots) != 0 {
		t.Fatal("exhausted budget leaked a slot")
	}
	// Cache hits stay available after the budget is spent.
	if _, err := c.FindByExternalID(context.Background(), domain.ExternalSourceTVDB, "81190", "en-US"); err != nil || calls != 3 {
		t.Fatal("cache hit charged against the daily limit")
	}
	now = time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	if _, err := c.FindByExternalID(context.Background(), domain.ExternalSourceTVDB, "81191", "en-US"); err != nil || calls != 4 {
		t.Fatalf("daily limit not reset at UTC midnight error=%v calls=%d", err, calls)
	}
}

func TestGovernorDailyLimitErrorIsDistinct(t *testing.T) {
	g := newGovernedRequests(Governance{RequestInterval: MinRequestInterval, MaxConcurrent: 1, DailyLimit: 1})
	now := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	g.now = func() time.Time { return now }
	g.wait = func(_ context.Context, d time.Duration) error { now = now.Add(d); return nil }
	release, err := g.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release()
	if _, err := g.acquire(context.Background()); err != ErrDailyBudget || len(g.slots) != 0 {
		t.Fatalf("error=%v slots=%d", err, len(g.slots))
	}
	if DefaultGovernance().DailyLimit != 0 || newRequestGovernor().daily != 0 {
		t.Fatal("default governance has a daily limit")
	}
}
