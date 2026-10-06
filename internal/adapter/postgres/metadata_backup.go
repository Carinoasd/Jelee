package postgres

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

// Metadata export (G36.4). One REPEATABLE READ, read-only transaction gives
// every record the same snapshot; rows are streamed one at a time to the
// writer, so memory does not grow with the catalog. Records are what a rescan
// cannot rebuild: accounts and grants, libraries and roots, the catalog
// identity that per-item user data hangs on, item metadata and locks, locked
// images, playback progress, track preferences, manual version decisions,
// access, network and client control rules, webhooks and scan schedules.
// Collections and their manual members, playlists with their entries, user
// interface preferences, the site appearance and plugin documents and the
// audit retention periods follow (format version 2).
// Sessions, tokens, user creation keys, caches, jobs, inventories, outboxes,
// statistics and audit rows are never exported, nor are share links (G48.6):
// a share grants access to whoever holds its token, so restoring one would
// revive that access; share guest accounts and their data stay out with them.
// metadataBackupExcluded names every table left out and why; a test fails
// when a table is in neither list.

type metadataExportQuery struct {
	kind string
	sql  string
}

// metadataBackupTables maps each record kind to the tables it carries.
var metadataBackupTables = map[string][]string{
	"library": {"libraries"}, "library_root": {"library_roots"}, "user": {"users"}, "library_acl": {"library_acl"},
	"item": {"items"}, "media_source": {"media_sources"}, "item_directory_source": {"item_directory_sources"}, "item_parent_link": {"item_parent_links"},
	"catalog_scan_item": {"catalog_scan_items"}, "catalog_scan_source": {"catalog_scan_sources"}, "catalog_scan_item_alias": {"catalog_scan_item_aliases"},
	"item_version_exclusion": {"item_version_exclusions"}, "item_primary_version": {"item_primary_versions"},
	"item_metadata_state": {"item_metadata_state"}, "item_metadata_field": {"item_metadata_fields"}, "item_metadata_fact": {"item_metadata_facts"},
	"item_nfo_field_lock": {"item_nfo_field_locks"}, "item_image": {"item_images"},
	"user_item_data": {"user_item_data"}, "user_track_preference": {"user_track_preferences"},
	"access_policy": {"access_policy"}, "parental_rating": {"parental_ratings"}, "user_item_access_rule": {"user_item_access_rules"}, "user_blocked_tag": {"user_blocked_tags"},
	"user_blocked_keyword": {"user_blocked_keywords"}, "user_access_window": {"user_access_windows"}, "access_template": {"access_templates", "access_template_libraries"},
	"client_control_policy": {"client_control_policy"}, "client_rule": {"client_rules"}, "library_network_rule": {"library_network_rules"},
	"webhook": {"webhooks"}, "scan_schedule": {"scan_schedules"},
	"collection": {"collections"}, "collection_item": {"collection_items"}, "playlist": {"playlists"}, "playlist_item": {"playlist_items"},
	"user_preference": {"user_preferences"}, "site_appearance": {"site_appearance"}, "site_plugins": {"site_plugins"}, "audit_retention": {"audit_retention"},
}

// Reasons a table stays out of metadata backups. pg_dump carries all of
// them; docs/backup-restore.md lists the tables under each reason.
const (
	excludedRevivesAccess = "credential or one-time secret: restoring it would revive access (G48.6, G07)"
	excludedAudit         = "append-only audit trail: it stays with the database and its pg_dump; an import records itself instead"
	excludedJob           = "job queue, lease or job progress: meaningful only to the workers of the database that wrote it"
	excludedDerived       = "cache, quota or derived state: scans, probes, NFO reads and image processing rebuild it"
	excludedObservation   = "observation or statistic, not configuration: playback sessions, counters, hits, delivery attempts"
	excludedHistory       = "operation history of this database: repair and consistency journals, version operations and legacy import runs name rows and IDs of the source and can only be reverted there"
	excludedInstance      = "state of this instance: setup progress, developer mode session, runtime log levels and log file retention, and migration bookkeeping are set by the target itself"
)

// metadataBackupExcluded lists every table that is not exported, with its
// reason. Unlocked image rows of item_images are also left out (scans rebuild
// them) but the table itself is exported.
var metadataBackupExcluded = map[string]string{
	"sessions": excludedRevivesAccess, "user_creation_keys": excludedRevivesAccess, "dev_mode_tokens": excludedRevivesAccess, "user_totp": excludedRevivesAccess,
	"user_recovery_codes": excludedRevivesAccess, "login_challenges": excludedRevivesAccess, "app_passwords": excludedRevivesAccess, "share_links": excludedRevivesAccess,

	"audit_logs": excludedAudit,

	"jobs": excludedJob, "job_directories": excludedJob, "job_inventory": excludedJob, "probe_requests": excludedJob, "probe_job_state": excludedJob,
	"nfo_policy_requests": excludedJob, "nfo_job_state": excludedJob, "nfo_job_requests": excludedJob, "image_job_state": excludedJob,
	"job_ignore_requests": excludedJob, "job_ignore_manifests": excludedJob, "job_ignore_proofs": excludedJob, "job_ignore_comparisons": excludedJob,
	"job_ignore_decisions": excludedJob, "job_ignore_comparison_pages": excludedJob, "job_ignore_verifications": excludedJob, "job_ignore_scan_state": excludedJob,
	"job_ignore_exclusions": excludedJob, "job_ignore_legacy_manifests": excludedJob, "job_ignore_legacy_proofs": excludedJob, "job_ignore_legacy_queries": excludedJob,
	"job_ignore_legacy_verifications": excludedJob, "job_ignore_family_exclusions": excludedJob, "job_ignore_legacy_baseline_queries": excludedJob,
	"job_ignore_legacy_baseline_verifications": excludedJob, "job_ignore_family_decisions": excludedJob,
	"catalog_import_requests": excludedJob, "catalog_import_entries": excludedJob, "catalog_sync_requests": excludedJob,
	"nfo_write_preparations": excludedJob, "nfo_write_requests": excludedJob, "nfo_write_entries": excludedJob, "nfo_write_quota_fences": excludedJob,
	"nfo_write_commit_journal": excludedJob, "nfo_write_commit_file_plans": excludedJob, "nfo_write_commit_files_ready": excludedJob,
	"nfo_write_native_claims": excludedJob, "nfo_write_commit_file_checkpoints": excludedJob, "nfo_commit_attempt_quota_fence": excludedJob,
	"nfo_write_commit_attempt_reservations": excludedJob, "nfo_write_commit_attempts": excludedJob, "nfo_write_commit_attempt_checkpoints": excludedJob,
	"nfo_write_commit_attempt_ready": excludedJob, "nfo_write_commit_recovery_leases": excludedJob, "nfo_write_commit_settlements": excludedJob,
	"nfo_write_commit_resolutions": excludedJob, "webhook_outbox": excludedJob, "legacy_import_pending": excludedJob,

	"probe_cache": excludedDerived, "probe_cache_quota": excludedDerived, "probe_library_quota": excludedDerived, "tool_versions": excludedDerived,
	"nfo_cache": excludedDerived, "nfo_cache_quota": excludedDerived, "nfo_library_quota": excludedDerived, "item_nfo_observations": excludedDerived,
	"library_inventory_baseline_data": excludedDerived, "inventory_snapshot_preparations": excludedDerived, "inventory_missing_acceptances": excludedDerived,
	"catalog_scan_pending": excludedDerived, "image_variants": excludedDerived, "media_sidecar_tracks": excludedDerived,
	"item_embedded_cover_attempts": excludedDerived, "scan_watch_state": excludedDerived,

	"playback_sessions": excludedObservation, "playback_samples": excludedObservation, "watch_stats_daily": excludedObservation,
	"watch_stats_history": excludedObservation, "known_clients": excludedObservation, "known_client_sessions": excludedObservation,
	"client_control_hits": excludedObservation, "webhook_deliveries": excludedObservation, "webhook_delivery_attempts": excludedObservation,
	"job_metric_epoch": excludedObservation, "job_metric_totals": excludedObservation, "job_metric_buckets": excludedObservation,

	"consistency_runs": excludedHistory, "consistency_run_checks": excludedHistory, "consistency_fix_journal": excludedHistory,
	"repair_runs": excludedHistory, "repair_journal": excludedHistory, "item_version_operations": excludedHistory,
	"legacy_import_runs": excludedHistory, "legacy_import_checkpoints": excludedHistory, "legacy_import_map": excludedHistory,

	"setup_state": excludedInstance, "dev_mode_state": excludedInstance, "schema_migrations": excludedInstance, "log_settings": excludedInstance,
}

func metadataExportQueries(passwordHashes bool) []metadataExportQuery {
	password := ""
	if passwordHashes {
		password = ",password_hash"
	}
	return []metadataExportQuery{
		{"library", `SELECT to_jsonb(x)::text FROM (SELECT id,name,nfo_mode,metadata_language,metadata_image_languages,metadata_preferences_revision,catalog_sync_auto FROM libraries) x ORDER BY x.id`},
		{"library_root", `SELECT to_jsonb(x)::text FROM (SELECT id,library_id,path FROM library_roots) x ORDER BY x.id`},
		{"user", `SELECT to_jsonb(x)::text FROM (SELECT id,name,is_admin,disabled,hidden,display_name,locale,created_at,deleted_at,allow_native,max_streams,max_kbps,parental_rating_max,block_unrated,content_filtered` + password + ` FROM users WHERE share_id IS NULL) x ORDER BY x.id`},
		{"library_acl", `SELECT to_jsonb(x)::text FROM (SELECT user_id,library_id FROM library_acl a WHERE ` + notGuestSQL("a.user_id") + `) x ORDER BY x.user_id,x.library_id`},
		{"item", `SELECT to_jsonb(x)::text FROM (SELECT id,library_id,kind,title FROM items) x ORDER BY x.id`},
		{"media_source", `SELECT to_jsonb(x)::text FROM (SELECT id,item_id,library_id,root_id,relative_path,content_type FROM media_sources) x ORDER BY x.id`},
		{"item_directory_source", `SELECT to_jsonb(x)::text FROM (SELECT id,item_id,library_id,kind,root_id,relative_path FROM item_directory_sources) x ORDER BY x.id`},
		{"item_parent_link", `SELECT to_jsonb(x)::text FROM (SELECT item_id,library_id,item_kind,parent_id,parent_kind FROM item_parent_links) x ORDER BY x.item_id`},
		{"catalog_scan_item", `SELECT to_jsonb(x)::text FROM (SELECT item_id,library_id,kind,group_digest,parser_version,scan_title,year,season,episode,episode_end FROM catalog_scan_items) x ORDER BY x.item_id`},
		{"catalog_scan_source", `SELECT to_jsonb(x)::text FROM (SELECT source_id,library_id,item_id,root_id,relative_path,size,modified_unix_nano,parser_version,missing_since,manual FROM catalog_scan_sources) x ORDER BY x.source_id`},
		// Manual version decisions (G20.3): merged scan groups, excluded files
		// and main versions. The operation log only serves undo within its
		// window and stays behind, like the audit rows.
		{"catalog_scan_item_alias", `SELECT to_jsonb(x)::text FROM (SELECT library_id,kind,group_digest,item_id FROM catalog_scan_item_aliases) x ORDER BY x.library_id,x.kind,x.group_digest`},
		{"item_version_exclusion", `SELECT to_jsonb(x)::text FROM (SELECT item_id,library_id,root_id,relative_path,created_at FROM item_version_exclusions) x ORDER BY x.item_id,x.root_id,x.relative_path COLLATE "C"`},
		{"item_primary_version", `SELECT to_jsonb(x)::text FROM (SELECT item_id,library_id,source_id,updated_at FROM item_primary_versions) x ORDER BY x.item_id`},
		{"item_metadata_state", `SELECT to_jsonb(x)::text FROM (SELECT item_id,revision FROM item_metadata_state) x ORDER BY x.item_id`},
		{"item_metadata_field", `SELECT to_jsonb(x)::text FROM (SELECT item_id,field,value,source,locked,updated_at,provider_resource,provider_id,provider_source_url,provider_language,provider_fetched_at,nfo_origin FROM item_metadata_fields) x ORDER BY x.item_id,x.field`},
		{"item_metadata_fact", `SELECT to_jsonb(x)::text FROM (SELECT item_id,field,value,source,locked,updated_at,nfo_origin FROM item_metadata_facts) x ORDER BY x.item_id,x.field`},
		{"item_nfo_field_lock", `SELECT to_jsonb(x)::text FROM (SELECT item_id,field,origin FROM item_nfo_field_locks) x ORDER BY x.item_id,x.field`},
		// Unlocked image rows are rebuilt by scans and refreshes; a lock is the
		// administrator's choice and is kept with its source and content facts.
		{"item_image", `SELECT to_jsonb(x)::text FROM (SELECT id,item_id,library_id,image_type,image_index,source_kind,root_id,relative_path,remote_url,content_sha256,width,height,format,byte_size,average_color,fetched_at,source_mtime_unix_nano,source_size,locked,created_at,updated_at FROM item_images WHERE locked) x ORDER BY x.id`},
		{"user_item_data", `SELECT to_jsonb(x)::text FROM (SELECT user_id,item_id,resume_ticks,played,play_count,last_played_at,last_source_id,updated_at FROM user_item_data d WHERE ` + notGuestSQL("d.user_id") + `) x ORDER BY x.user_id,x.item_id`},
		{"user_track_preference", `SELECT to_jsonb(x)::text FROM (SELECT user_id,item_id,source_id,audio_language,audio_commentary,audio_track,subtitle_mode,subtitle_language,subtitle_sdh,subtitle_track,updated_at FROM user_track_preferences p WHERE ` + notGuestSQL("p.user_id") + `) x ORDER BY x.user_id,x.item_id NULLS FIRST,x.source_id NULLS FIRST`},
		{"access_policy", `SELECT to_jsonb(x)::text FROM (SELECT restrict_admins,block_unrated FROM access_policy WHERE id) x`},
		{"parental_rating", `SELECT to_jsonb(x)::text FROM (SELECT code,level FROM parental_ratings) x ORDER BY x.code COLLATE "C"`},
		{"user_item_access_rule", `SELECT to_jsonb(x)::text FROM (SELECT user_id,item_id,effect,created_at FROM user_item_access_rules r WHERE ` + notGuestSQL("r.user_id") + `) x ORDER BY x.user_id,x.item_id`},
		{"user_blocked_tag", `SELECT to_jsonb(x)::text FROM (SELECT user_id,tag FROM user_blocked_tags b WHERE ` + notGuestSQL("b.user_id") + `) x ORDER BY x.user_id,x.tag COLLATE "C"`},
		// Blocked keywords and restricted time windows (G48.4), and access
		// templates with their libraries (G48.7).
		{"user_blocked_keyword", `SELECT to_jsonb(x)::text FROM (SELECT user_id,keyword FROM user_blocked_keywords k WHERE ` + notGuestSQL("k.user_id") + `) x ORDER BY x.user_id,x.keyword COLLATE "C"`},
		{"user_access_window", `SELECT to_jsonb(x)::text FROM (SELECT user_id,position,weekdays,start_minute,end_minute,time_zone,rating_max FROM user_access_windows w WHERE ` + notGuestSQL("w.user_id") + `) x ORDER BY x.user_id,x.position`},
		{"access_template", `SELECT to_jsonb(x)::text FROM (SELECT t.id,t.name,t.rating_max,t.block_unrated,t.blocked_tags,t.blocked_keywords,t.created_at,t.updated_at,
 ARRAY(SELECT l.library_id FROM access_template_libraries l WHERE l.template_id=t.id ORDER BY l.library_id) AS library_ids FROM access_templates t) x ORDER BY x.id`},
		{"client_control_policy", `SELECT to_jsonb(x)::text FROM (SELECT unknown_clients,exempt_admins,exempt_loopback FROM client_control_policy WHERE id) x`},
		// Hit counters are observations, not configuration.
		{"client_rule", `SELECT to_jsonb(x)::text FROM (SELECT id,dimension,header_name,match_kind,pattern,case_fold,priority,action,intent,rate_requests,rate_period_seconds,scope_kind,scope_values,window_from,window_until,daily_start,daily_end,weekdays,time_zone,enabled,note,created_by,created_at,updated_at,libraries FROM client_rules) x ORDER BY x.id`},
		{"library_network_rule", `SELECT to_jsonb(x)::text FROM (SELECT id,library_id,network,cidrs,client_kinds,include_admins,enabled,note,created_by,created_at,updated_at FROM library_network_rules) x ORDER BY x.id`},
		// Secrets stay sealed with the master key; the seal binds the endpoint
		// ID, which is why webhooks keep their ID on import.
		{"webhook", `SELECT to_jsonb(x)::text FROM (SELECT id,name,url,enabled,events,header_names,headers_sealed,timeout_ms,max_attempts,base_delay_ms,max_delay_ms,jitter,secret_sealed,previous_secret_sealed,previous_until,created_at,updated_at FROM webhooks) x ORDER BY x.id`},
		{"scan_schedule", `SELECT to_jsonb(x)::text FROM (SELECT library_id,owner_id,revision,enabled,mode,interval_seconds,cron,timezone,probe,nfo,ignore_mode,ignore_case,next_due,watch_enabled FROM scan_schedules) x ORDER BY x.library_id`},
		// Collections and their manual members (G02.1); NFO-linked members
		// follow the metadata facts and are not copied.
		{"collection", `SELECT to_jsonb(x)::text FROM (SELECT id,name,overview,nfo_name,created_by,created_at,updated_at FROM collections) x ORDER BY x.id`},
		{"collection_item", `SELECT to_jsonb(x)::text FROM (SELECT collection_id,item_id,added_at FROM collection_items) x ORDER BY x.collection_id,x.item_id`},
		{"playlist", `SELECT to_jsonb(x)::text FROM (SELECT id,owner_id,name,public,created_at,updated_at FROM playlists p WHERE ` + notGuestSQL("p.owner_id") + `) x ORDER BY x.id`},
		{"playlist_item", `SELECT to_jsonb(x)::text FROM (SELECT e.id,e.playlist_id,e.item_id,e.position,e.added_at FROM playlist_items e JOIN playlists p ON p.id=e.playlist_id WHERE ` + notGuestSQL("p.owner_id") + `) x ORDER BY x.id`},
		{"user_preference", `SELECT to_jsonb(x)::text FROM (SELECT user_id,theme,density,layout,updated_at FROM user_preferences p WHERE ` + notGuestSQL("p.user_id") + `) x ORDER BY x.user_id`},
		// Single-row documents: revisions and change times belong to the
		// instance, like those of the access and client control policies.
		{"site_appearance", `SELECT to_jsonb(x)::text FROM (SELECT default_theme,tokens,custom_css,allow_external_fonts,font_hosts,default_layout FROM site_appearance WHERE id) x`},
		{"site_plugins", `SELECT to_jsonb(x)::text FROM (SELECT plugins,settings FROM site_plugins WHERE id) x`},
		{"audit_retention", `SELECT to_jsonb(x)::text FROM (SELECT audit_days,security_days FROM audit_retention WHERE singleton) x`},
	}
}

// notGuestSQL holds when the user ID expression column is not a share
// guest account (G48.6).
func notGuestSQL(column string) string {
	return `NOT EXISTS(SELECT 1 FROM users g WHERE g.id=` + column + ` AND g.share_id IS NOT NULL)`
}

// ExportMetadata streams a metadata backup to w. The audit row is written
// after the trailer, so a failed export leaves no export record and a
// recorded export names the digest of a complete document.
func (s *Store) ExportMetadata(ctx context.Context, w io.Writer, opts domain.MetadataExportOptions) (domain.MetadataExportSummary, error) {
	if ctx == nil || w == nil {
		return domain.MetadataExportSummary{}, domain.ErrInvalid
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return domain.MetadataExportSummary{}, storageError(err)
	}
	defer tx.Rollback(ctx)
	// Timestamps are rendered in UTC so documents do not depend on the
	// session time zone; the statement timeout of the role does not apply to
	// a long export.
	if _, err = tx.Exec(ctx, `SET LOCAL TimeZone='UTC'; SET LOCAL statement_timeout=0`); err != nil {
		return domain.MetadataExportSummary{}, storageError(err)
	}
	var version int
	var dirty bool
	if err = tx.QueryRow(ctx, `SELECT version,dirty FROM schema_migrations`).Scan(&version, &dirty); err != nil {
		return domain.MetadataExportSummary{}, storageError(err)
	}
	if dirty || version != SchemaVersion {
		return domain.MetadataExportSummary{}, domain.ErrMetadataBackupUnsupported
	}
	created := time.Now().UTC().Truncate(time.Second)
	out, err := domain.NewMetadataBackupWriter(w, domain.MetadataBackupHeader{SchemaVersion: version, CreatedAt: created, PasswordHashes: opts.IncludePasswordHashes})
	if err != nil {
		return domain.MetadataExportSummary{}, err
	}
	for _, q := range metadataExportQueries(opts.IncludePasswordHashes) {
		if err = exportMetadataKind(ctx, tx, out, q); err != nil {
			return domain.MetadataExportSummary{}, err
		}
	}
	trailer, err := out.Close()
	if err != nil {
		return domain.MetadataExportSummary{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.MetadataExportSummary{}, storageError(err)
	}
	summary := domain.MetadataExportSummary{FormatVersion: domain.MetadataBackupFormatVersion, SchemaVersion: version, CreatedAt: created,
		PasswordHashes: opts.IncludePasswordHashes, Records: trailer.Records, Counts: trailer.Counts, SHA256: trailer.SHA256}
	audit, err := s.Pool.Begin(ctx)
	if err != nil {
		return summary, storageError(err)
	}
	defer audit.Rollback(ctx)
	if err = appendAudit(ctx, audit, AuditEntry{Event: "metadata.exported", TargetRef: "sha256:" + trailer.SHA256,
		After: map[string]any{"records": trailer.Records, "schemaVersion": version, "passwordHashes": opts.IncludePasswordHashes}}); err != nil {
		return summary, err
	}
	return summary, storageError(audit.Commit(ctx))
}

func exportMetadataKind(ctx context.Context, tx pgx.Tx, out *domain.MetadataBackupWriter, q metadataExportQuery) error {
	rows, err := tx.Query(ctx, q.sql)
	if err != nil {
		return storageError(err)
	}
	defer rows.Close()
	for rows.Next() {
		// The text column arrives as its bytes; they are written before the
		// next row reuses the buffer.
		raw := rows.RawValues()
		if len(raw) != 1 || raw[0] == nil {
			return domain.ErrDatabase
		}
		if err = out.Write(q.kind, raw[0]); err != nil {
			if errors.Is(err, domain.ErrInvalid) {
				return domain.ErrDatabase
			}
			return err
		}
	}
	return storageError(rows.Err())
}
