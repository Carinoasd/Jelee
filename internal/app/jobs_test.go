package app

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type jobRepositoryFake struct {
	JobRepository
	submit    func(context.Context, domain.Actor, string, string, string, domain.JobPolicy) (domain.Job, bool, error)
	retry     func(context.Context, domain.Actor, string, string, domain.JobPolicy) (domain.Job, bool, error)
	cancel    func(context.Context, domain.Actor, string) (domain.Job, error)
	get       func(context.Context, domain.Actor, string) (domain.Job, error)
	list      func(context.Context, domain.Actor, string, int, string) ([]domain.Job, error)
	entries   func(context.Context, domain.Actor, string, string, int) ([]domain.InventoryEntry, error)
	libraries func(context.Context, domain.Actor, string, int) ([]domain.LibrarySummary, error)
}

func (f jobRepositoryFake) SubmitJob(c context.Context, a domain.Actor, l, k, p string, o domain.JobPolicy) (domain.Job, bool, error) {
	return f.submit(c, a, l, k, p, o)
}
func (f jobRepositoryFake) RetryJob(c context.Context, a domain.Actor, id, k string, o domain.JobPolicy) (domain.Job, bool, error) {
	return f.retry(c, a, id, k, o)
}
func (f jobRepositoryFake) CancelJob(c context.Context, a domain.Actor, id string) (domain.Job, error) {
	return f.cancel(c, a, id)
}
func (f jobRepositoryFake) GetJob(c context.Context, a domain.Actor, id string) (domain.Job, error) {
	return f.get(c, a, id)
}
func (f jobRepositoryFake) ListJobs(c context.Context, a domain.Actor, cursor string, limit int, state string) ([]domain.Job, error) {
	return f.list(c, a, cursor, limit, state)
}
func (f jobRepositoryFake) ListInventory(c context.Context, a domain.Actor, id, cursor string, limit int) ([]domain.InventoryEntry, error) {
	return f.entries(c, a, id, cursor, limit)
}
func (f jobRepositoryFake) ListLibraries(c context.Context, a domain.Actor, cursor string, limit int) ([]domain.LibrarySummary, error) {
	return f.libraries(c, a, cursor, limit)
}

func jobTestPolicy() domain.JobPolicy {
	return domain.JobPolicy{QueueLimit: 10, HistoryLimit: 10, MaxEntries: 1000, MaxDirectories: 100, MaxAttempts: 3, MissingCountLimit: 100, MissingPercentLimit: 20}
}
func newJobService(t *testing.T, r JobRepository) *Jobs {
	t.Helper()
	j, err := NewJobs(r, jobTestPolicy())
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func TestJobsPolicySecurityBounds(t *testing.T) {
	for _, bound := range []struct {
		min, max int
		set      func(*domain.JobPolicy, int)
	}{
		{1, 1000, func(p *domain.JobPolicy, n int) { p.QueueLimit = n }},
		{1, 100, func(p *domain.JobPolicy, n int) { p.HistoryLimit = n }},
		{100, 500000, func(p *domain.JobPolicy, n int) { p.MaxEntries = n }},
		{1, 100000, func(p *domain.JobPolicy, n int) { p.MaxDirectories = n }},
		{1, 10, func(p *domain.JobPolicy, n int) { p.MaxAttempts = n }},
		{1, 500000, func(p *domain.JobPolicy, n int) { p.MissingCountLimit = n }},
		{1, 100, func(p *domain.JobPolicy, n int) { p.MissingPercentLimit = n }},
	} {
		for _, n := range []int{bound.min - 1, bound.min, bound.max, bound.max + 1} {
			p := jobTestPolicy()
			bound.set(&p, n)
			_, err := NewJobs(jobRepositoryFake{}, p)
			valid := n >= bound.min && n <= bound.max
			if (err == nil) != valid {
				t.Fatalf("value=%d valid=%v err=%v", n, valid, err)
			}
		}
	}
	if _, err := NewJobs(nil, jobTestPolicy()); err != domain.ErrInvalid {
		t.Fatal("nil repository accepted")
	}
}

func TestJobsForwardValidatedContractsAndReplay(t *testing.T) {
	ctx := context.WithValue(context.Background(), struct{}{}, "request-context")
	actor := accountTestActor()
	policy := jobTestPolicy()
	calls := 0
	job := domain.Job{ID: accountTargetID, LibraryID: accountUserID, State: domain.JobQueued, Priority: domain.JobPriorityManual}
	check := func(c context.Context, a domain.Actor) {
		t.Helper()
		calls++
		if c != ctx || a != actor {
			t.Fatal("context/verified actor changed")
		}
	}
	repo := jobRepositoryFake{
		submit: func(c context.Context, a domain.Actor, l, k, p string, o domain.JobPolicy) (domain.Job, bool, error) {
			check(c, a)
			if l != accountUserID || k != "request-key" || p != domain.JobPriorityManual || o != policy {
				t.Fatal("submit contract changed")
			}
			return job, true, nil
		},
		retry: func(c context.Context, a domain.Actor, id, k string, o domain.JobPolicy) (domain.Job, bool, error) {
			check(c, a)
			if id != job.ID || k != "retry-key" || o != policy {
				t.Fatal("retry contract changed")
			}
			return job, true, nil
		},
		cancel: func(c context.Context, a domain.Actor, id string) (domain.Job, error) {
			check(c, a)
			if id != job.ID {
				t.Fatal("cancel target changed")
			}
			return job, nil
		},
		get: func(c context.Context, a domain.Actor, id string) (domain.Job, error) {
			check(c, a)
			if id != job.ID {
				t.Fatal("get target changed")
			}
			return job, nil
		},
		list: func(c context.Context, a domain.Actor, cursor string, limit int, state string) ([]domain.Job, error) {
			check(c, a)
			if cursor != accountSessionID || limit != 100 || state != domain.JobFailed {
				t.Fatal("list filter changed")
			}
			return []domain.Job{job}, nil
		},
		entries: func(c context.Context, a domain.Actor, id, cursor string, limit int) ([]domain.InventoryEntry, error) {
			check(c, a)
			if id != job.ID || cursor != accountSessionID || limit != 1 {
				t.Fatal("inventory paging changed")
			}
			return []domain.InventoryEntry{{ID: accountSessionID, Kind: "video"}}, nil
		},
		libraries: func(c context.Context, a domain.Actor, cursor string, limit int) ([]domain.LibrarySummary, error) {
			check(c, a)
			if cursor != "" || limit != 10 {
				t.Fatal("library paging changed")
			}
			return []domain.LibrarySummary{{ID: accountUserID}}, nil
		},
	}
	j := newJobService(t, repo)
	if got, replay, err := j.Submit(ctx, actor, accountUserID, "request-key", domain.JobPriorityManual); err != nil || !replay || !reflect.DeepEqual(got, job) {
		t.Fatal("submit result changed")
	}
	if got, replay, err := j.Retry(ctx, actor, job.ID, "retry-key"); err != nil || !replay || got.ID != job.ID {
		t.Fatal("retry result changed")
	}
	if got, err := j.Cancel(ctx, actor, job.ID); err != nil || got.ID != job.ID {
		t.Fatal("cancel result changed")
	}
	if got, err := j.Get(ctx, actor, job.ID); err != nil || got.ID != job.ID {
		t.Fatal("get result changed")
	}
	if got, err := j.List(ctx, actor, accountSessionID, 100, domain.JobFailed); err != nil || len(got) != 1 {
		t.Fatal("list result changed")
	}
	if got, err := j.Entries(ctx, actor, job.ID, accountSessionID, 1); err != nil || len(got) != 1 {
		t.Fatal("inventory result changed")
	}
	if got, err := j.Libraries(ctx, actor, "", 10); err != nil || len(got) != 1 {
		t.Fatal("libraries result changed")
	}
	if calls != 7 {
		t.Fatalf("wrong operation count %d", calls)
	}
}

func jobOperations(j *Jobs, ctx context.Context, a domain.Actor) []func() error {
	return []func() error{
		func() error {
			_, _, err := j.Submit(ctx, a, accountTargetID, "key", domain.JobPriorityManual)
			return err
		},
		func() error { _, _, err := j.Retry(ctx, a, accountTargetID, "key"); return err },
		func() error { _, err := j.Cancel(ctx, a, accountTargetID); return err },
		func() error { _, err := j.Get(ctx, a, accountTargetID); return err },
		func() error { _, err := j.List(ctx, a, "", 1, ""); return err },
		func() error { _, err := j.Entries(ctx, a, accountTargetID, "", 1); return err },
		func() error { _, err := j.Libraries(ctx, a, "", 1); return err },
	}
}

func TestJobsRejectInvalidIdentityOrCancelledRequestBeforeRepository(t *testing.T) {
	j := newJobService(t, jobRepositoryFake{})
	for _, a := range []domain.Actor{{}, {UserID: accountUserID}, {UserID: "invalid", SessionID: accountSessionID}, {UserID: accountUserID, SessionID: strings.ToUpper(accountSessionID)}, {UserID: accountUserID, SessionID: accountSessionID, IP: strings.Repeat("x", 46)}} {
		for _, operation := range jobOperations(j, context.Background(), a) {
			if err := operation(); err != domain.ErrInvalid {
				t.Fatal("invalid actor reached repository")
			}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, operation := range jobOperations(j, ctx, accountTestActor()) {
		if err := operation(); !errors.Is(err, context.Canceled) {
			t.Fatal("cancelled request reached repository")
		}
	}
}

func TestJobsRejectInvalidInputBeforeRepository(t *testing.T) {
	j := newJobService(t, jobRepositoryFake{})
	ctx := context.Background()
	actor := accountTestActor()
	for _, id := range []string{"", "bad", strings.ToUpper(accountTargetID)} {
		if _, _, err := j.Submit(ctx, actor, id, "key", domain.JobPriorityManual); err != domain.ErrInvalid {
			t.Fatal("invalid library")
		}
		if _, _, err := j.Retry(ctx, actor, id, "key"); err != domain.ErrInvalid {
			t.Fatal("invalid retry target")
		}
		if _, err := j.Get(ctx, actor, id); err != domain.ErrInvalid {
			t.Fatal("invalid target")
		}
		if _, err := j.Cancel(ctx, actor, id); err != domain.ErrInvalid {
			t.Fatal("invalid cancel")
		}
		if _, err := j.Entries(ctx, actor, id, "", 1); err != domain.ErrInvalid {
			t.Fatal("invalid inventory target")
		}
	}
	for _, key := range []string{"", "has space", "\tkey", "金鑰", strings.Repeat("x", 129)} {
		if _, _, err := j.Submit(ctx, actor, accountTargetID, key, domain.JobPriorityManual); err != domain.ErrInvalid {
			t.Fatal("invalid key")
		}
		if _, _, err := j.Retry(ctx, actor, accountTargetID, key); err != domain.ErrInvalid {
			t.Fatal("invalid retry key")
		}
	}
	for _, priority := range []string{"", "urgent", "Manual"} {
		if _, _, err := j.Submit(ctx, actor, accountTargetID, "key", priority); err != domain.ErrInvalid {
			t.Fatal("invalid priority")
		}
	}
	for _, state := range []string{"cancel_requested", "done", "RUNNING"} {
		if _, err := j.List(ctx, actor, "", 1, state); err != domain.ErrInvalid {
			t.Fatal("invalid state")
		}
	}
	for _, page := range []struct {
		cursor string
		limit  int
	}{{"bad", 1}, {strings.ToUpper(accountTargetID), 1}, {"", 0}, {"", 101}} {
		if _, err := j.List(ctx, actor, page.cursor, page.limit, ""); err != domain.ErrInvalid {
			t.Fatal("invalid job page")
		}
		if _, err := j.Entries(ctx, actor, accountTargetID, page.cursor, page.limit); err != domain.ErrInvalid {
			t.Fatal("invalid inventory page")
		}
		if _, err := j.Libraries(ctx, actor, page.cursor, page.limit); err != domain.ErrInvalid {
			t.Fatal("invalid library page")
		}
	}
}

func TestJobsPreserveRepositoryAuthorizationAndCapacityErrors(t *testing.T) {
	for _, failure := range []error{domain.ErrForbidden, domain.ErrUnauthenticated, domain.ErrJobQueueFull, domain.ErrJobBusy, domain.ErrNotFound, domain.ErrDatabase} {
		repo := jobRepositoryFake{submit: func(context.Context, domain.Actor, string, string, string, domain.JobPolicy) (domain.Job, bool, error) {
			return domain.Job{}, false, failure
		}}
		if _, _, err := newJobService(t, repo).Submit(context.Background(), accountTestActor(), accountTargetID, "key", domain.JobPriorityBackground); err != failure {
			t.Fatal("repository rejection was hidden")
		}
	}
}
