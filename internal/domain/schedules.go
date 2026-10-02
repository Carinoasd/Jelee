package domain

import "time"

// ScheduleTiming uses an explicit zone; host-local time is never implied.
type ScheduleTiming struct {
	Mode            string `json:"mode"`
	IntervalSeconds int    `json:"intervalSeconds"`
	Cron            string `json:"cron"`
	Timezone        string `json:"timezone"`
}

type ScheduleIgnore struct {
	Mode     string `json:"mode"`
	CaseMode string `json:"caseMode"`
}

type ScanScheduleInput struct {
	ExpectedRevision int64          `json:"expectedRevision"`
	Enabled          bool           `json:"enabled"`
	Timing           ScheduleTiming `json:"timing"`
	Probe            bool           `json:"probe"`
	NFO              bool           `json:"nfo"`
	Ignore           ScheduleIgnore `json:"ignore"`
}

type ScanSchedule struct {
	LibraryID  string         `json:"libraryId"`
	Revision   int64          `json:"revision"`
	Enabled    bool           `json:"enabled"`
	Timing     ScheduleTiming `json:"timing"`
	Probe      bool           `json:"probe"`
	NFO        bool           `json:"nfo"`
	Ignore     ScheduleIgnore `json:"ignore"`
	NextDue    *time.Time     `json:"nextDue,omitempty"`
	RetryAfter *time.Time     `json:"retryAfter,omitempty"`
	LastJobID  string         `json:"lastJobId,omitempty"`
	LastError  string         `json:"lastError,omitempty"`
	UpdatedAt  time.Time      `json:"updatedAt"`
}
