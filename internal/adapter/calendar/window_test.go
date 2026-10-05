package calendar

import (
	"errors"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestDailyWindowValidation(t *testing.T) {
	for _, v := range [][3]string{{"", "", "UTC"}, {"01:00", "", "UTC"}, {"1:00", "02:00", "UTC"}, {"24:00", "02:00", "UTC"}, {"00:60", "02:00", "UTC"}, {"01:00", "01:00", "UTC"}, {"01:00", "02:00", "Local"}, {"01:00", "02:00", "bad/zone"}, {"01:00", "02:00", " UTC"}, {"01:00", "02:00", ""}} {
		if _, err := ParseDailyWindow(v[0], v[1], v[2]); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("accepted invalid window: %q", v)
		}
	}
	w, err := ParseDailyWindow("", "", "")
	if err != nil || w != nil || !w.Allows(time.Time{}) {
		t.Fatal("empty window must be unrestricted")
	}
}

func TestDailyWindowBoundariesAndDST(t *testing.T) {
	tests := []struct {
		start, end, zone, instant string
		want                      bool
	}{
		{"01:00", "03:00", "UTC", "2026-01-01T00:59:59Z", false},
		{"01:00", "03:00", "UTC", "2026-01-01T01:00:00Z", true},
		{"01:00", "03:00", "UTC", "2026-01-01T02:59:59Z", true},
		{"01:00", "03:00", "UTC", "2026-01-01T03:00:00Z", false},
		{"22:00", "06:00", "Asia/Taipei", "2026-01-01T14:00:00Z", true},
		{"22:00", "06:00", "Asia/Taipei", "2026-01-01T21:59:59Z", true},
		{"22:00", "06:00", "Asia/Taipei", "2026-01-01T22:00:00Z", false},
		{"22:00", "06:00", "Asia/Taipei", "2026-01-01T13:59:59Z", false},
		{"01:00", "02:00", "America/New_York", "2026-11-01T05:30:00Z", true},
		{"01:00", "02:00", "America/New_York", "2026-11-01T06:30:00Z", true},
		{"01:00", "02:00", "America/New_York", "2026-11-01T07:00:00Z", false},
		{"02:00", "03:00", "America/New_York", "2026-03-08T06:59:59Z", false},
		{"02:00", "03:00", "America/New_York", "2026-03-08T07:00:00Z", false},
	}
	for _, tt := range tests {
		w, err := ParseDailyWindow(tt.start, tt.end, tt.zone)
		if err != nil {
			t.Fatal(err)
		}
		at, err := time.Parse(time.RFC3339, tt.instant)
		if err != nil {
			t.Fatal(err)
		}
		if w.Allows(at) != tt.want {
			t.Errorf("%+v", tt)
		}
	}
}
