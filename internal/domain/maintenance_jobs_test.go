package domain

import (
	"testing"
	"time"
)

func TestEvaluateBackupStatus(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.FixedZone("x", 3600))
	if v := EvaluateBackupStatus(now, nil, 7); v.Status != BackupReminderNever || v.LastExportAt != nil || v.AgeSeconds != nil || v.ThresholdDays != 7 || v.CheckedAt.Location() != time.UTC {
		t.Fatalf("never %+v", v)
	}
	for _, c := range []struct {
		age  time.Duration
		want string
		secs int64
	}{
		{0, BackupReminderOK, 0},
		{7 * 24 * time.Hour, BackupReminderOK, 604800},
		{7*24*time.Hour + time.Second, BackupReminderOverdue, 604801},
		{-time.Hour, BackupReminderOK, 0},
	} {
		last := now.Add(-c.age)
		v := EvaluateBackupStatus(now, &last, 7)
		if v.Status != c.want || v.AgeSeconds == nil || *v.AgeSeconds != c.secs || v.LastExportAt == nil || !v.LastExportAt.Equal(last) || v.LastExportAt.Location() != time.UTC {
			t.Fatalf("age %s: %+v", c.age, v)
		}
	}
	for days, ok := range map[int]bool{0: false, 1: true, 365: true, 366: false, -1: false} {
		if ValidBackupReminderDays(days) != ok {
			t.Fatal("threshold", days)
		}
	}
}

func TestSubtitleCacheSweepAdd(t *testing.T) {
	var s SubtitleCacheSweep
	s.Add(SubtitleCacheSweep{Orphaned: 1, Revisions: 2, Capacity: 3, Failed: 4, Bytes: 5})
	s.Add(SubtitleCacheSweep{Orphaned: 1, Bytes: 5})
	if s != (SubtitleCacheSweep{Orphaned: 2, Revisions: 2, Capacity: 3, Failed: 4, Bytes: 10}) || !ValidSubtitleCacheSweep(s) {
		t.Fatalf("%+v", s)
	}
	for _, bad := range []SubtitleCacheSweep{{Orphaned: -1}, {Revisions: -1}, {Capacity: -1}, {Failed: -1}, {Bytes: -1}} {
		if ValidSubtitleCacheSweep(bad) {
			t.Fatalf("negative accepted %+v", bad)
		}
	}
}
