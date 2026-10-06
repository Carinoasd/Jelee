package config

import (
	"errors"
	"fmt"
	"strconv"
	"time"
)

// AuditConfig controls the audit retention purge (G46.9). The retention
// periods themselves live in the database (audit_retention); these settings
// only decide how often and how much the server purges. A zero
// PurgeIntervalMinutes switches the purge off; zero batch fields select the
// defaults.
type AuditConfig struct {
	PurgeIntervalMinutes int `json:"purgeIntervalMinutes"`
	PurgeBatch           int `json:"purgeBatch"`
	PurgeMaxBatches      int `json:"purgeMaxBatches"`
}

const (
	defaultAuditPurgeIntervalMinutes = 60
	defaultAuditPurgeBatch           = 1000
	defaultAuditPurgeMaxBatches      = 100
	maxAuditPurgeIntervalMinutes     = 7 * 24 * 60
	// The batch bounds match app.AuditPurgeBatchMax and
	// app.AuditPurgeMaxBatchesMax.
	maxAuditPurgeBatch      = 10000
	maxAuditPurgeMaxBatches = 1000
)

// DefaultAuditConfig purges hourly, at most 100 batches of 1,000 rows.
func DefaultAuditConfig() AuditConfig {
	return AuditConfig{PurgeIntervalMinutes: defaultAuditPurgeIntervalMinutes, PurgeBatch: defaultAuditPurgeBatch, PurgeMaxBatches: defaultAuditPurgeMaxBatches}
}

// Validate never includes configured values in its errors.
func (c AuditConfig) Validate() error {
	switch {
	case c.PurgeIntervalMinutes < 0 || c.PurgeIntervalMinutes > maxAuditPurgeIntervalMinutes:
		return errors.New("audit purgeIntervalMinutes must be between 0 and 10080")
	case c.PurgeBatch < 0 || c.PurgeBatch > maxAuditPurgeBatch:
		return errors.New("audit purgeBatch must be between 1 and 10000")
	case c.PurgeMaxBatches < 0 || c.PurgeMaxBatches > maxAuditPurgeMaxBatches:
		return errors.New("audit purgeMaxBatches must be between 1 and 1000")
	}
	return nil
}

// PurgeInterval is zero when the purge is off.
func (c AuditConfig) PurgeInterval() time.Duration {
	return time.Duration(c.PurgeIntervalMinutes) * time.Minute
}

// Batch is the rows one purge transaction deletes.
func (c AuditConfig) Batch() int {
	if c.PurgeBatch == 0 {
		return defaultAuditPurgeBatch
	}
	return c.PurgeBatch
}

// MaxBatches bounds the batches of one purge pass.
func (c AuditConfig) MaxBatches() int {
	if c.PurgeMaxBatches == 0 {
		return defaultAuditPurgeMaxBatches
	}
	return c.PurgeMaxBatches
}

func (c *AuditConfig) loadEnvironment(lookup func(string) (string, bool)) error {
	for key, target := range map[string]*int{
		"JELEE_AUDIT_PURGE_INTERVAL_MINUTES": &c.PurgeIntervalMinutes,
		"JELEE_AUDIT_PURGE_BATCH":            &c.PurgeBatch,
		"JELEE_AUDIT_PURGE_MAX_BATCHES":      &c.PurgeMaxBatches,
	} {
		if value, ok := lookup(key); ok {
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid %s", key)
			}
			*target = n
		}
	}
	return nil
}
