package runtime

import (
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/matroska"
	"github.com/MoYuanCN/Jelee/internal/adapter/postgres"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	jobworker "github.com/MoYuanCN/Jelee/internal/platform/jobs"
)

var _ app.SubtitleCacheSweeper = (*matroska.Sweeper)(nil)

// prepareSubtitleCacheSweeper opens the extraction cache for cleanup jobs
// when extraction is enabled and the cache root is private; a missing
// mkvtoolnix runtime does not matter, the cache is only files.
func prepareSubtitleCacheSweeper(c config.Config) app.SubtitleCacheSweeper {
	if !c.Matroska.EnableExtraction {
		return nil
	}
	sweeper, err := matroska.NewSweeper(matroska.Config{CacheRoot: c.Matroska.CacheRoot, MaxCacheBytes: c.Matroska.CacheMaxBytes})
	if err != nil {
		return nil
	}
	return sweeper
}

// maintenanceKinds binds the workers of the maintenance job kinds.
func maintenanceKinds(store *postgres.Store, sweeper app.SubtitleCacheSweeper, dbTimeout time.Duration) (map[string]jobworker.KindOptions, error) {
	cleanup, err := app.NewSubtitleCacheCleanupWorker(sweeper, store, dbTimeout)
	if err != nil {
		return nil, err
	}
	reminder, err := app.NewBackupReminderWorker(store, dbTimeout)
	if err != nil {
		return nil, err
	}
	removal, err := app.NewExternalMetadataRemovalWorker(store, dbTimeout)
	if err != nil {
		return nil, err
	}
	return map[string]jobworker.KindOptions{
		domain.JobSubtitleCacheCleanup:    {Worker: cleanup, Finish: store.FinishSubtitleCacheCleanup, FailureCode: "subtitle_cache_cleanup_failed"},
		domain.JobBackupReminder:          {Worker: reminder, Finish: store.FinishBackupReminder, FailureCode: "backup_reminder_failed"},
		domain.JobExternalMetadataRemoval: {Worker: removal, Finish: store.FinishExternalMetadataRemoval, FailureCode: "external_metadata_removal_failed"},
	}, nil
}
