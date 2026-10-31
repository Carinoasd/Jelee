package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
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
	// ArchiveDir switches the archive on (G36.3): rows are written there as
	// dated NDJSON+gzip files before the purge deletes them. Empty keeps the
	// delete-only purge. The archive's own limits are independent of the
	// database retention: ArchiveRetentionDays bounds audit-category files
	// (and security files unless ArchiveSecurityRetentionDays is set), and
	// ArchiveMaxMB caps the directory; zero means unlimited.
	ArchiveDir                   string `json:"archiveDir"`
	ArchiveRetentionDays         int    `json:"archiveRetentionDays"`
	ArchiveSecurityRetentionDays int    `json:"archiveSecurityRetentionDays"`
	ArchiveMaxMB                 int    `json:"archiveMaxMb"`
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
	maxAuditArchiveDays     = 36500
	maxAuditArchiveMB       = 1 << 20
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
	case c.ArchiveRetentionDays < 0 || c.ArchiveRetentionDays > maxAuditArchiveDays ||
		c.ArchiveSecurityRetentionDays < 0 || c.ArchiveSecurityRetentionDays > maxAuditArchiveDays:
		return errors.New("audit archive retention days must be between 0 and 36500")
	case c.ArchiveMaxMB < 0 || c.ArchiveMaxMB > maxAuditArchiveMB:
		return errors.New("audit archiveMaxMb must be between 0 and 1048576")
	case c.ArchiveDir == "" && (c.ArchiveRetentionDays != 0 || c.ArchiveSecurityRetentionDays != 0 || c.ArchiveMaxMB != 0):
		return errors.New("audit archive limits need archiveDir")
	case c.ArchiveDir != "" && (!filepath.IsAbs(c.ArchiveDir) || strings.ContainsRune(c.ArchiveDir, 0)):
		return errors.New("audit archiveDir must be an absolute path")
	case c.ArchiveDir != "" && c.PurgeIntervalMinutes == 0:
		return errors.New("audit archiveDir needs the purge enabled")
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
	if value, ok := lookup("JELEE_AUDIT_ARCHIVE_DIR"); ok {
		c.ArchiveDir = value
	}
	for key, target := range map[string]*int{
		"JELEE_AUDIT_ARCHIVE_RETENTION_DAYS":          &c.ArchiveRetentionDays,
		"JELEE_AUDIT_ARCHIVE_SECURITY_RETENTION_DAYS": &c.ArchiveSecurityRetentionDays,
		"JELEE_AUDIT_ARCHIVE_MAX_MB":                  &c.ArchiveMaxMB,
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
