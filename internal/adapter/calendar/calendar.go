package calendar

import (
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/robfig/cron/v3"
	"strings"
	"time"
	_ "time/tzdata"
)

type Calendar struct{}

// Next does no I/O except loading the embedded IANA zone data. Parser errors
// are deliberately mapped to a static domain error, never echoed to clients.
func (Calendar) Next(v domain.ScheduleTiming, after time.Time) (time.Time, error) {
	if after.IsZero() || after.Year() < 1970 || after.Year() > 9990 || len(v.Timezone) < 1 || len(v.Timezone) > 128 || v.Timezone == "Local" || strings.ContainsAny(v.Timezone, " \t\r\n\\") {
		return time.Time{}, domain.ErrInvalid
	}
	zone, err := time.LoadLocation(v.Timezone)
	if err != nil {
		return time.Time{}, domain.ErrInvalid
	}
	if v.Mode == "interval" {
		if v.IntervalSeconds < 60 || v.IntervalSeconds > 31536000 || v.Cron != "" {
			return time.Time{}, domain.ErrInvalid
		}
		return after.UTC().Add(time.Duration(v.IntervalSeconds) * time.Second), nil
	}
	if v.Mode != "cron" || v.IntervalSeconds != 0 || len(v.Cron) > 256 || len(strings.Fields(v.Cron)) != 5 || strings.ContainsAny(v.Cron, "=@\r\n") {
		return time.Time{}, domain.ErrInvalid
	}
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	parsed, err := parser.Parse(v.Cron)
	if err != nil {
		return time.Time{}, domain.ErrInvalid
	}
	spec, ok := parsed.(*cron.SpecSchedule)
	if !ok {
		return time.Time{}, domain.ErrInvalid
	}
	spec.Location = zone
	next := spec.Next(after.UTC())
	if next.IsZero() || !next.After(after) {
		return time.Time{}, domain.ErrInvalid
	}
	return next.UTC(), nil
}
