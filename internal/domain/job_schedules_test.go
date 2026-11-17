package domain

import (
	"errors"
	"testing"
	"time"
)

func mustZone(t *testing.T, name string) *time.Location {
	t.Helper()
	zone, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return zone
}

func TestScheduleWindowAllowsAcrossZonesAndDST(t *testing.T) {
	newYork := mustZone(t, "America/New_York")
	taipei := mustZone(t, "Asia/Taipei")
	for _, tc := range []struct {
		name   string
		window ScheduleWindow
		at     time.Time
		zone   *time.Location
		want   bool
	}{
		{"no window", ScheduleWindow{}, time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC), taipei, true},
		{"inside", ScheduleWindow{"01:00", "05:00"}, time.Date(2026, 5, 1, 2, 0, 0, 0, taipei), taipei, true},
		{"start inclusive", ScheduleWindow{"01:00", "05:00"}, time.Date(2026, 5, 1, 1, 0, 0, 0, taipei), taipei, true},
		{"end exclusive", ScheduleWindow{"01:00", "05:00"}, time.Date(2026, 5, 1, 5, 0, 0, 0, taipei), taipei, false},
		// 02:00 in Taipei is 18:00 UTC the day before: the zone decides.
		{"zone not UTC", ScheduleWindow{"01:00", "05:00"}, time.Date(2026, 4, 30, 18, 0, 0, 0, time.UTC), taipei, true},
		{"same instant in UTC", ScheduleWindow{"01:00", "05:00"}, time.Date(2026, 4, 30, 18, 0, 0, 0, time.UTC), time.UTC, false},
		{"crosses midnight late", ScheduleWindow{"22:00", "02:00"}, time.Date(2026, 5, 1, 23, 30, 0, 0, taipei), taipei, true},
		{"crosses midnight early", ScheduleWindow{"22:00", "02:00"}, time.Date(2026, 5, 2, 1, 59, 0, 0, taipei), taipei, true},
		{"crosses midnight outside", ScheduleWindow{"22:00", "02:00"}, time.Date(2026, 5, 2, 2, 0, 0, 0, taipei), taipei, false},
		// 2026-11-01 01:30 happens twice in New York; both follow the wall clock.
		{"repeated hour first", ScheduleWindow{"01:00", "02:00"}, time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC), newYork, true},
		{"repeated hour second", ScheduleWindow{"01:00", "02:00"}, time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC), newYork, true},
	} {
		if got := tc.window.Allows(tc.at, tc.zone); got != tc.want {
			t.Errorf("%s: Allows=%t", tc.name, got)
		}
	}
}

func TestScheduleWindowNextAllowed(t *testing.T) {
	newYork := mustZone(t, "America/New_York")
	berlin := mustZone(t, "Europe/Berlin")
	for _, tc := range []struct {
		name   string
		window ScheduleWindow
		at     time.Time
		zone   *time.Location
		want   time.Time
	}{
		{"already open", ScheduleWindow{"01:00", "05:00"}, time.Date(2026, 5, 1, 3, 17, 42, 0, berlin), berlin, time.Date(2026, 5, 1, 3, 17, 42, 0, berlin)},
		{"later today", ScheduleWindow{"01:00", "05:00"}, time.Date(2026, 5, 1, 0, 10, 30, 0, berlin), berlin, time.Date(2026, 5, 1, 1, 0, 0, 0, berlin)},
		{"tomorrow", ScheduleWindow{"01:00", "05:00"}, time.Date(2026, 5, 1, 6, 0, 0, 0, berlin), berlin, time.Date(2026, 5, 2, 1, 0, 0, 0, berlin)},
		// 2026-03-08 02:00-03:00 does not exist in New York: a window that
		// opens at 02:30 opens at 03:00 local, the first existing minute.
		{"spring gap", ScheduleWindow{"02:30", "04:00"}, time.Date(2026, 3, 8, 1, 0, 0, 0, newYork), newYork, time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC)},
		// A window entirely inside the gap waits for the next day.
		{"window inside gap", ScheduleWindow{"02:10", "02:50"}, time.Date(2026, 3, 8, 1, 0, 0, 0, newYork), newYork, time.Date(2026, 3, 9, 2, 10, 0, 0, newYork)},
		// The fall-back day has 25 hours; the next 01:00 is the first one.
		{"fall back", ScheduleWindow{"01:00", "01:30"}, time.Date(2026, 10, 31, 23, 0, 0, 0, newYork), newYork, time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)},
	} {
		got, ok := tc.window.NextAllowed(tc.at, tc.zone)
		if !ok || !got.Equal(tc.want) {
			t.Errorf("%s: NextAllowed=%v %t want %v", tc.name, got, ok, tc.want)
		}
	}
}

func TestNextScheduleRun(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	hourly := ScheduleTiming{Mode: ScheduleModeInterval, IntervalSeconds: 3600, Timezone: "Asia/Taipei"}
	interval := func(v ScheduleTiming, after time.Time) (time.Time, error) {
		return after.Add(time.Duration(v.IntervalSeconds) * time.Second), nil
	}
	future, past := now.Add(90*time.Minute), now.Add(-time.Minute)
	for _, tc := range []struct {
		name   string
		timing ScheduleTiming
		window ScheduleWindow
		want   time.Time
		more   bool
	}{
		{"interval", hourly, ScheduleWindow{}, now.Add(time.Hour), true},
		// 13:00 UTC is 21:00 in Taipei; a 01:00-03:00 window moves it to
		// 01:00 Taipei, 17:00 UTC.
		{"interval into window", hourly, ScheduleWindow{"01:00", "03:00"}, time.Date(2026, 5, 1, 17, 0, 0, 0, time.UTC), true},
		{"once ahead", ScheduleTiming{Mode: ScheduleModeOnce, Timezone: "UTC", RunAt: &future}, ScheduleWindow{}, future, true},
		{"once passed", ScheduleTiming{Mode: ScheduleModeOnce, Timezone: "UTC", RunAt: &past}, ScheduleWindow{}, time.Time{}, false},
	} {
		got, more, err := NextScheduleRun(tc.timing, tc.window, now, interval)
		if err != nil || more != tc.more || !got.Equal(tc.want) {
			t.Errorf("%s: %v %t %v want %v", tc.name, got, more, err, tc.want)
		}
	}
	for _, bad := range []ScheduleTiming{
		{Mode: ScheduleModeInterval, IntervalSeconds: 59, Timezone: "UTC"},
		{Mode: ScheduleModeInterval, IntervalSeconds: 3600, Timezone: "Local"},
		{Mode: ScheduleModeInterval, IntervalSeconds: 3600, Timezone: "Mars/Olympus"},
		{Mode: ScheduleModeCron, Timezone: "UTC"},
		{Mode: ScheduleModeOnce, Timezone: "UTC"},
		{Mode: ScheduleModeInterval, IntervalSeconds: 3600, Timezone: "UTC", RunAt: &future},
		{Mode: "weekly", Timezone: "UTC"},
	} {
		if _, _, err := NextScheduleRun(bad, ScheduleWindow{}, now, interval); !errors.Is(err, ErrInvalid) {
			t.Errorf("accepted %+v: %v", bad, err)
		}
	}
	if _, _, err := NextScheduleRun(hourly, ScheduleWindow{"01:00", "01:00"}, now, interval); !errors.Is(err, ErrInvalid) {
		t.Error("empty window accepted", err)
	}
}

func TestValidateJobScheduleInput(t *testing.T) {
	library := "11111111-2222-4333-8444-555555555555"
	hourly := ScheduleTiming{Mode: ScheduleModeInterval, IntervalSeconds: 3600, Timezone: "UTC"}
	for _, tc := range []struct {
		name string
		in   JobScheduleInput
		want error
	}{
		{"scan", JobScheduleInput{Kind: JobInventoryScan, LibraryID: library, Timing: hourly, Params: JobScheduleParams{Probe: true, NFO: true, Ignore: ScheduleIgnore{Mode: "jeleeignore", CaseMode: "sensitive"}}}, nil},
		{"refresh", JobScheduleInput{Kind: JobMetadataRefresh, LibraryID: library, Timing: hourly}, nil},
		{"images", JobScheduleInput{Kind: JobImageRefresh, LibraryID: library, Timing: hourly}, nil},
		{"consistency", JobScheduleInput{Kind: JobConsistencyCheck, LibraryID: library, Timing: hourly}, nil},
		{"cleanup", JobScheduleInput{Kind: JobSubtitleCacheCleanup, Timing: hourly, Params: JobScheduleParams{MaxIdleDays: 10}}, nil},
		{"reminder", JobScheduleInput{Kind: JobBackupReminder, Timing: hourly, Window: ScheduleWindow{"02:00", "04:00"}}, nil},
		{"destructive", JobScheduleInput{Kind: JobExternalMetadataRemoval, LibraryID: library, Timing: hourly}, ErrScheduleKindNotAllowed},
		{"nfo apply", JobScheduleInput{Kind: JobNFOApply, LibraryID: library, Timing: hourly}, ErrScheduleKindNotAllowed},
		{"server kind with library", JobScheduleInput{Kind: JobBackupReminder, LibraryID: library, Timing: hourly}, ErrInvalid},
		{"library kind without library", JobScheduleInput{Kind: JobMetadataRefresh, Timing: hourly}, ErrInvalid},
		{"foreign option", JobScheduleInput{Kind: JobMetadataRefresh, LibraryID: library, Timing: hourly, Params: JobScheduleParams{Probe: true}}, ErrInvalid},
		{"reminder days", JobScheduleInput{Kind: JobBackupReminder, Timing: hourly, Params: JobScheduleParams{ThresholdDays: BackupReminderDaysMax + 1}}, ErrInvalid},
		{"half window", JobScheduleInput{Kind: JobBackupReminder, Timing: hourly, Window: ScheduleWindow{Start: "02:00"}}, ErrInvalid},
		{"bad clock", JobScheduleInput{Kind: JobBackupReminder, Timing: hourly, Window: ScheduleWindow{"24:00", "02:00"}}, ErrInvalid},
		{"negative revision", JobScheduleInput{Kind: JobBackupReminder, Timing: hourly, ExpectedRevision: -1}, ErrInvalid},
	} {
		if err := ValidateJobScheduleInput(tc.in); !errors.Is(err, tc.want) && !(err == nil && tc.want == nil) {
			t.Errorf("%s: %v want %v", tc.name, err, tc.want)
		}
	}
	for _, kind := range ScheduleKinds() {
		if !ScheduleKindAllowed(kind) {
			t.Error("listed kind not allowed", kind)
		}
	}
}

func TestSystemActor(t *testing.T) {
	id := "11111111-2222-4333-8444-555555555555"
	a := SystemActor(SystemTriggerSchedule, id)
	if !a.IsSystem() || a.System != "system:schedule:"+id || a.UserID != "" {
		t.Fatal("schedule actor", a)
	}
	if !SystemActor(SystemTriggerCatalogSync, id).IsSystem() {
		t.Fatal("catalog sync actor")
	}
	for _, bad := range []Actor{{}, {System: "system:manual:" + id}, {System: "system:schedule:x"}, {System: "schedule:" + id},
		{System: a.System, UserID: id}, {System: a.System, SessionID: id}} {
		if bad.IsSystem() {
			t.Error("accepted", bad)
		}
	}
}
