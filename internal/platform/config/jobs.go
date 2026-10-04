package config

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/calendar"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

type JobsConfig struct {
	WindowStart            string `json:"windowStart"`
	WindowEnd              string `json:"windowEnd"`
	WindowTimezone         string `json:"windowTimezone"`
	Workers                int    `json:"workers"`
	PollMilliseconds       int    `json:"pollMilliseconds"`
	LeaseSeconds           int    `json:"leaseSeconds"`
	DatabaseTimeoutSeconds int    `json:"databaseTimeoutSeconds"`
	MaxRuntimeSeconds      int    `json:"maxRuntimeSeconds"`
	QueueLimit             int    `json:"queueLimit"`
	HistoryLimit           int    `json:"historyLimit"`
	MaxEntries             int    `json:"maxEntries"`
	MaxDirectories         int    `json:"maxDirectories"`
	MaxAttempts            int    `json:"maxAttempts"`
	MissingCountLimit      int    `json:"missingCountLimit"`
	MissingPercentLimit    int    `json:"missingPercentLimit"`
	// ScanConcurrency bounds directories one inventory job reads at once.
	ScanConcurrency int `json:"scanConcurrency"`
	// ConsistencyIntervalHours queues a background consistency check of
	// every library this often (G50.3); zero, the default, disables it.
	ConsistencyIntervalHours int `json:"consistencyIntervalHours"`
	// ConsistencyStatBudget bounds the file probes of one check run and
	// ConsistencyWatchSample the statistics rows it recounts per library;
	// zero selects the checker defaults.
	ConsistencyStatBudget  int `json:"consistencyStatBudget"`
	ConsistencyWatchSample int `json:"consistencyWatchSample"`
}

func DefaultJobsConfig() JobsConfig {
	return JobsConfig{Workers: 2, PollMilliseconds: 250, LeaseSeconds: 30, DatabaseTimeoutSeconds: 2, MaxRuntimeSeconds: 3600, QueueLimit: 100, HistoryLimit: 20, MaxEntries: 100000, MaxDirectories: 10000, MaxAttempts: 3, MissingCountLimit: 100, MissingPercentLimit: 20, ScanConcurrency: 2}
}

func (c JobsConfig) Policy() domain.JobPolicy {
	return domain.JobPolicy{QueueLimit: c.QueueLimit, HistoryLimit: c.HistoryLimit, MaxEntries: c.MaxEntries, MaxDirectories: c.MaxDirectories, MaxAttempts: c.MaxAttempts, MissingCountLimit: c.MissingCountLimit, MissingPercentLimit: c.MissingPercentLimit}
}

func (c JobsConfig) Validate() error {
	if _, err := calendar.ParseDailyWindow(c.WindowStart, c.WindowEnd, c.WindowTimezone); err != nil {
		return errors.New("invalid job work window")
	}
	if c.Workers < 1 || c.Workers > 8 || c.PollMilliseconds < 100 || c.PollMilliseconds > 60000 || c.LeaseSeconds < 10 || c.LeaseSeconds > 300 || c.DatabaseTimeoutSeconds < 1 || c.DatabaseTimeoutSeconds > 99 || time.Duration(c.DatabaseTimeoutSeconds)*time.Second >= time.Duration(c.LeaseSeconds)*time.Second/3 || c.MaxRuntimeSeconds < 60 || c.MaxRuntimeSeconds > 86400 || c.ScanConcurrency < 1 || c.ScanConcurrency > 16 {
		return errors.New("job worker concurrency or timeout is outside supported limits")
	}
	if c.QueueLimit < 1 || c.QueueLimit > 1000 || c.HistoryLimit < 1 || c.HistoryLimit > 100 || c.MaxEntries < 100 || c.MaxEntries > 500000 || c.MaxDirectories < 1 || c.MaxDirectories > 100000 || c.MaxAttempts < 1 || c.MaxAttempts > 10 || c.MissingCountLimit < 1 || c.MissingCountLimit > 500000 || c.MissingPercentLimit < 1 || c.MissingPercentLimit > 100 {
		return errors.New("job capacity or comparison policy is outside supported limits")
	}
	if c.ConsistencyIntervalHours < 0 || c.ConsistencyIntervalHours > 8760 || c.ConsistencyStatBudget < 0 || c.ConsistencyStatBudget > domain.ConsistencyMaxStatBudget || c.ConsistencyWatchSample < 0 || c.ConsistencyWatchSample > domain.ConsistencyMaxWatchSample {
		return errors.New("consistency check schedule or bounds are outside supported limits")
	}
	return nil
}

// ConsistencyInterval is zero when the periodic consistency check is off.
func (c JobsConfig) ConsistencyInterval() time.Duration {
	return time.Duration(c.ConsistencyIntervalHours) * time.Hour
}

func (c *JobsConfig) loadEnvironment(lookup func(string) (string, bool)) error {
	for name, target := range map[string]*string{"JELEE_JOB_WINDOW_START": &c.WindowStart, "JELEE_JOB_WINDOW_END": &c.WindowEnd, "JELEE_JOB_WINDOW_TIMEZONE": &c.WindowTimezone} {
		if value, ok := lookup(name); ok {
			*target = value
		}
	}
	for name, target := range map[string]*int{
		"JELEE_JOB_WORKERS": &c.Workers, "JELEE_JOB_POLL_MILLISECONDS": &c.PollMilliseconds, "JELEE_JOB_LEASE_SECONDS": &c.LeaseSeconds, "JELEE_JOB_DATABASE_TIMEOUT_SECONDS": &c.DatabaseTimeoutSeconds, "JELEE_JOB_MAX_RUNTIME_SECONDS": &c.MaxRuntimeSeconds, "JELEE_JOB_QUEUE_LIMIT": &c.QueueLimit, "JELEE_JOB_HISTORY_LIMIT": &c.HistoryLimit, "JELEE_SCAN_MAX_ENTRIES": &c.MaxEntries, "JELEE_SCAN_MAX_DIRECTORIES": &c.MaxDirectories, "JELEE_JOB_MAX_ATTEMPTS": &c.MaxAttempts, "JELEE_SCAN_MISSING_COUNT_LIMIT": &c.MissingCountLimit, "JELEE_SCAN_MISSING_PERCENT_LIMIT": &c.MissingPercentLimit, "JELEE_SCAN_DIRECTORY_CONCURRENCY": &c.ScanConcurrency,
		"JELEE_JOB_CONSISTENCY_INTERVAL_HOURS": &c.ConsistencyIntervalHours, "JELEE_JOB_CONSISTENCY_STAT_BUDGET": &c.ConsistencyStatBudget, "JELEE_JOB_CONSISTENCY_WATCH_SAMPLE": &c.ConsistencyWatchSample,
	} {
		if value, ok := lookup(name); ok {
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid %s", name)
			}
			*target = n
		}
	}
	return nil
}
