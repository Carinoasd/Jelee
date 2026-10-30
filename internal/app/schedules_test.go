package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type jobScheduleFake struct {
	ScheduleRepository
	stored domain.JobSchedule
	caps   ScheduleCapabilities
	puts   int
}

func (f *jobScheduleFake) DispatchJobSchedule(_ context.Context, _ domain.JobPolicy, _ ScheduleCalendar, caps ScheduleCapabilities) (bool, error) {
	f.caps = caps
	return true, nil
}

func (f *jobScheduleFake) GetJobSchedule(context.Context, domain.Actor, string) (domain.JobSchedule, error) {
	return f.stored, nil
}

func (f *jobScheduleFake) PutJobSchedule(context.Context, domain.Actor, string, domain.JobScheduleInput, ScheduleCalendar) (domain.JobSchedule, error) {
	f.puts++
	return f.stored, nil
}

type scheduleCalendarFake struct{}

func (scheduleCalendarFake) Next(_ domain.ScheduleTiming, after time.Time) (time.Time, error) {
	return after.Add(time.Hour), nil
}

func TestJobSchedulesRefuseSystemActorAndUnschedulableKinds(t *testing.T) {
	repo := &jobScheduleFake{}
	j, err := NewJobsWithSchedules(newJobService(t, jobRepositoryFake{}), repo, scheduleCalendarFake{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	library := "11111111-2222-4333-8444-555555555555"
	input := domain.JobScheduleInput{Kind: domain.JobExternalMetadataRemoval, LibraryID: library, Enabled: true, Timing: domain.ScheduleTiming{Mode: "interval", IntervalSeconds: 3600, Timezone: "UTC"}}
	if _, err = j.PutJobSchedule(ctx, accountTestActor(), "", input); !errors.Is(err, domain.ErrScheduleKindNotAllowed) || repo.puts != 0 {
		t.Fatal("destructive kind reached storage", err, repo.puts)
	}
	input.Kind = domain.JobImageRefresh
	if _, err = j.PutJobSchedule(ctx, accountTestActor(), "", input); err != nil || repo.puts != 1 {
		t.Fatal(err)
	}
	// The system actor never manages schedules or submits jobs itself.
	system := domain.SystemActor(domain.SystemTriggerSchedule, library)
	if _, err = j.PutJobSchedule(ctx, system, "", input); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("system actor changed a schedule", err)
	}
	if _, _, err = j.SubmitScanOptions(ctx, system, library, "k", domain.JobPriorityManual, false, false, domain.IgnoreIntent{}); err == nil {
		t.Fatal("system actor submitted a scan")
	}
	if _, err = j.ListJobSchedules(ctx, system, ""); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("system actor listed schedules", err)
	}
	if err = j.DeleteJobSchedule(ctx, accountTestActor(), library, 0); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("delete without revision", err)
	}
	// consistency_check has no administrator submission to run now.
	repo.stored = domain.JobSchedule{ID: library, Kind: domain.JobConsistencyCheck, LibraryID: library}
	if _, _, err = j.RunJobSchedule(ctx, accountTestActor(), library, "run-now"); !errors.Is(err, domain.ErrScheduleKindNotAllowed) {
		t.Fatal("consistency run now", err)
	}
	if worked, err := j.DispatchSchedule(ctx); err != nil || !worked {
		t.Fatal(worked, err)
	}
	// Without the optional services nothing beyond scans is admitted.
	if repo.caps.MetadataRefresh || repo.caps.ImageRefresh || repo.caps.SubtitleCache {
		t.Fatal("capabilities without services", repo.caps)
	}
}

func TestMetadataAcceptsSystemActorOnlyForWorkerCalls(t *testing.T) {
	m, err := NewMetadata(testSeriesProvider{})
	if err != nil {
		t.Fatal(err)
	}
	system := domain.SystemActor(domain.SystemTriggerCatalogSync, "11111111-2222-4333-8444-555555555555")
	item := "11111111-2222-4333-8444-666666666666"
	// The worker's reads pass the identity check and stop at the missing
	// repository; an administrator-only change refuses the system actor.
	if _, err = m.ItemFields(context.Background(), system, item); !errors.Is(err, domain.ErrMetadataUnavailable) {
		t.Fatal("worker read", err)
	}
	if _, err = m.UpdateItemFields(context.Background(), system, item, 1, nil); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatal("system actor edited metadata", err)
	}
	if _, err = m.ItemFields(context.Background(), domain.Actor{System: "system:other:" + item}, item); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatal("forged system actor accepted", err)
	}
}
