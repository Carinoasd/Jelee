package domain

import (
	"errors"
	"strings"
	"time"
)

// G13.1 generic schedules: a persisted definition queues one job kind for a
// library or for the whole server on an interval, a five-field cron or once,
// in an explicit IANA zone and optionally only inside a daily window.

// JobInventoryScan is the job kind of a library inventory scan.
const JobInventoryScan = "inventory_scan"

// Schedule modes, retry spacing and the lastError values of a schedule.
const (
	ScheduleModeInterval = "interval"
	ScheduleModeCron     = "cron"
	ScheduleModeOnce     = "once"

	// ScheduleRetryDelay spaces the retries of an occurrence whose job could
	// not be admitted (busy library, full queue).
	ScheduleRetryDelay = 30 * time.Second

	ScheduleErrorAdmission       = "admission_unavailable"
	ScheduleErrorKindUnavailable = "kind_unavailable"
	// ScheduleErrorOwnerUnavailable is only found on scan schedules disabled
	// before schema 107, when they still ran as their owner.
	ScheduleErrorOwnerUnavailable = "owner_unavailable"
)

// ErrScheduleKindNotAllowed refuses a schedule for a job kind that cannot
// run unattended: destructive kinds that need an explicit confirmation
// (external_metadata_removal), kinds that need a prepared request (nfo_write,
// nfo_export, nfo_apply, catalog imports) and unknown kinds.
var ErrScheduleKindNotAllowed = errors.New("job kind cannot be scheduled")

// scheduleKinds maps every schedulable kind to whether it targets a library
// (true) or the whole server (false).
var scheduleKinds = map[string]bool{
	JobInventoryScan:        true,
	JobMetadataRefresh:      true,
	JobImageRefresh:         true,
	JobConsistencyCheck:     true,
	JobSubtitleCacheCleanup: false,
	JobBackupReminder:       false,
}

// ScheduleKinds lists the schedulable kinds in a stable order.
func ScheduleKinds() []string {
	return []string{JobInventoryScan, JobMetadataRefresh, JobImageRefresh, JobConsistencyCheck, JobSubtitleCacheCleanup, JobBackupReminder}
}

// ScheduleKindAllowed reports whether kind may be scheduled; it is also the
// set of kinds the system actor may run.
func ScheduleKindAllowed(kind string) bool {
	_, ok := scheduleKinds[kind]
	return ok
}

// ScheduleKindLibraryScoped reports whether a schedulable kind targets one
// library; the others are server-wide.
func ScheduleKindLibraryScoped(kind string) bool { return scheduleKinds[kind] }

// ScheduleWindow restricts when occurrences start: HH:MM to HH:MM local time
// in the schedule's zone, end exclusive; an end before the start crosses
// midnight. Both empty means no window.
type ScheduleWindow struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// Empty reports a window without bounds.
func (w ScheduleWindow) Empty() bool { return w.Start == "" && w.End == "" }

func scheduleClock(s string) (int, bool) {
	if len(s) != 5 || s[2] != ':' {
		return 0, false
	}
	for _, i := range []int{0, 1, 3, 4} {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	h, m := int(s[0]-'0')*10+int(s[1]-'0'), int(s[3]-'0')*10+int(s[4]-'0')
	return h*60 + m, h < 24 && m < 60
}

// Valid checks the bounds; both or neither are set and they differ.
func (w ScheduleWindow) Valid() bool {
	if w.Empty() {
		return true
	}
	a, okA := scheduleClock(w.Start)
	b, okB := scheduleClock(w.End)
	return okA && okB && a != b
}

// Allows uses the instant's actual offset in zone: both occurrences of a
// repeated DST minute follow the same wall-clock rule and a skipped minute
// never matches. An empty window allows every instant.
func (w ScheduleWindow) Allows(at time.Time, zone *time.Location) bool {
	if w.Empty() {
		return true
	}
	start, _ := scheduleClock(w.Start)
	end, _ := scheduleClock(w.End)
	local := at.In(zone)
	m := local.Hour()*60 + local.Minute()
	if start < end {
		return m >= start && m < end
	}
	return m >= start || m < end
}

// NextAllowed returns the first instant at or after at that the window
// allows. It walks whole minutes, so DST gaps and repeats need no special
// date arithmetic; any valid window opens within two days.
func (w ScheduleWindow) NextAllowed(at time.Time, zone *time.Location) (time.Time, bool) {
	if w.Allows(at, zone) {
		return at, true
	}
	t := at.Truncate(time.Minute)
	for range 2 * 24 * 60 {
		t = t.Add(time.Minute)
		if w.Allows(t, zone) {
			return t, true
		}
	}
	return time.Time{}, false
}

// JobScheduleParams are the kind-specific options of a schedule. Fields of
// other kinds must stay zero.
type JobScheduleParams struct {
	// inventory_scan: probe new and changed files, read NFO, ignore rules.
	Probe  bool           `json:"probe"`
	NFO    bool           `json:"nfo"`
	Ignore ScheduleIgnore `json:"ignore"`
	// subtitle_cache_cleanup: idle days, zero for the default.
	MaxIdleDays int `json:"maxIdleDays"`
	// backup_reminder: threshold days, zero for the configured threshold.
	ThresholdDays int `json:"thresholdDays"`
}

// JobScheduleInput creates or replaces a schedule. Kind and library are fixed
// at creation; a replacement must repeat them.
type JobScheduleInput struct {
	Kind             string            `json:"kind"`
	LibraryID        string            `json:"libraryId,omitempty"`
	ExpectedRevision int64             `json:"expectedRevision"`
	Enabled          bool              `json:"enabled"`
	Timing           ScheduleTiming    `json:"timing"`
	Window           ScheduleWindow    `json:"window"`
	Params           JobScheduleParams `json:"params"`
}

// JobSchedule is a persisted schedule definition and its dispatch state.
type JobSchedule struct {
	ID         string            `json:"id"`
	Kind       string            `json:"kind"`
	LibraryID  string            `json:"libraryId,omitempty"`
	Revision   int64             `json:"revision"`
	Enabled    bool              `json:"enabled"`
	Timing     ScheduleTiming    `json:"timing"`
	Window     ScheduleWindow    `json:"window"`
	Params     JobScheduleParams `json:"params"`
	NextDue    *time.Time        `json:"nextDue,omitempty"`
	RetryAfter *time.Time        `json:"retryAfter,omitempty"`
	LastJobID  string            `json:"lastJobId,omitempty"`
	LastError  string            `json:"lastError,omitempty"`
	LastRunAt  *time.Time        `json:"lastRunAt,omitempty"`
	CreatedAt  time.Time         `json:"createdAt"`
	UpdatedAt  time.Time         `json:"updatedAt"`
}

// ValidateJobScheduleInput checks an input without the calendar: a kind that
// cannot be scheduled is ErrScheduleKindNotAllowed, anything else malformed
// ErrInvalid. Cron syntax and the zone are checked by the calendar.
func ValidateJobScheduleInput(in JobScheduleInput) error {
	if !ScheduleKindAllowed(in.Kind) {
		return ErrScheduleKindNotAllowed
	}
	if ScheduleKindLibraryScoped(in.Kind) != (in.LibraryID != "") || in.LibraryID != "" && !ValidID(in.LibraryID) {
		return ErrInvalid
	}
	if in.ExpectedRevision < 0 || in.ExpectedRevision >= 1<<62 || !in.Window.Valid() || !validScheduleTimingShape(in.Timing) {
		return ErrInvalid
	}
	p := in.Params
	switch in.Kind {
	case JobInventoryScan:
		if p.MaxIdleDays != 0 || p.ThresholdDays != 0 || ValidateScanIntentWithFamilyIgnore(ScanIntent{Ignore: IgnoreIntent(p.Ignore)}) != nil {
			return ErrInvalid
		}
	case JobSubtitleCacheCleanup:
		if p.Probe || p.NFO || p.Ignore != (ScheduleIgnore{}) || p.ThresholdDays != 0 || p.MaxIdleDays < 0 || p.MaxIdleDays > SubtitleCacheIdleDaysMax {
			return ErrInvalid
		}
	case JobBackupReminder:
		if p.Probe || p.NFO || p.Ignore != (ScheduleIgnore{}) || p.MaxIdleDays != 0 || p.ThresholdDays != 0 && !ValidBackupReminderDays(p.ThresholdDays) {
			return ErrInvalid
		}
	default:
		if p != (JobScheduleParams{}) {
			return ErrInvalid
		}
	}
	return nil
}

func validScheduleTimingShape(t ScheduleTiming) bool {
	if len(t.Timezone) < 1 || len(t.Timezone) > 128 || t.Timezone == "Local" || strings.ContainsAny(t.Timezone, " \t\r\n\\") {
		return false
	}
	switch t.Mode {
	case ScheduleModeInterval:
		return t.IntervalSeconds >= 60 && t.IntervalSeconds <= 31536000 && t.Cron == "" && t.RunAt == nil
	case ScheduleModeCron:
		return t.IntervalSeconds == 0 && t.Cron != "" && len(t.Cron) <= 256 && t.RunAt == nil
	case ScheduleModeOnce:
		return t.IntervalSeconds == 0 && t.Cron == "" && t.RunAt != nil && !t.RunAt.IsZero() && t.RunAt.Year() >= 1970 && t.RunAt.Year() <= 9990
	}
	return false
}

// ScheduleRecurrence computes the next recurring occurrence after an instant
// (interval and cron); the calendar adapter implements it.
type ScheduleRecurrence func(ScheduleTiming, time.Time) (time.Time, error)

// NextScheduleRun is the next occurrence strictly after after, moved forward
// to the window's next opening. A once schedule has its run time while that
// is still ahead and none (ok false) once it passed; a once run time inside
// the past is therefore never replayed. The zone must load.
func NextScheduleRun(t ScheduleTiming, w ScheduleWindow, after time.Time, recurrence ScheduleRecurrence) (time.Time, bool, error) {
	if !validScheduleTimingShape(t) || !w.Valid() || after.IsZero() {
		return time.Time{}, false, ErrInvalid
	}
	zone, err := time.LoadLocation(t.Timezone)
	if err != nil {
		return time.Time{}, false, ErrInvalid
	}
	var next time.Time
	if t.Mode == ScheduleModeOnce {
		if !t.RunAt.After(after) {
			return time.Time{}, false, nil
		}
		next = t.RunAt.UTC()
	} else {
		if recurrence == nil {
			return time.Time{}, false, ErrInvalid
		}
		if next, err = recurrence(t, after); err != nil {
			return time.Time{}, false, err
		}
	}
	allowed, ok := w.NextAllowed(next, zone)
	if !ok {
		return time.Time{}, false, ErrInvalid
	}
	return allowed.UTC(), true, nil
}

// System actor sources: "system:<trigger>:<uuid>".
const (
	SystemTriggerSchedule    = "schedule"
	SystemTriggerCatalogSync = "catalog_sync"
)

// SystemActor is the identity of unattended work: trigger is
// SystemTriggerSchedule with the schedule ID or SystemTriggerCatalogSync with
// the synchronisation job that queued it. It has no user and no session; the
// stores accept it with administrator rights, but only jobs of a schedulable
// kind can carry it and the application rejects it everywhere except the job
// workers' own calls. Audit rows record it with no actor_id and the source as
// their request ID.
func SystemActor(trigger, source string) Actor {
	return Actor{System: "system:" + trigger + ":" + source}
}

// IsSystem reports the system actor.
func (a Actor) IsSystem() bool {
	return a.UserID == "" && a.SessionID == "" && ValidSystemSource(a.System)
}

// ValidSystemSource checks "system:schedule:<uuid>" or
// "system:catalog_sync:<uuid>".
func ValidSystemSource(s string) bool {
	rest, ok := strings.CutPrefix(s, "system:")
	if !ok {
		return false
	}
	trigger, id, ok := strings.Cut(rest, ":")
	return ok && (trigger == SystemTriggerSchedule || trigger == SystemTriggerCatalogSync) && ValidID(id)
}
