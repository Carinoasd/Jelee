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
// images, playback progress, access and client control rules, webhooks and
// scan schedules. Sessions, tokens, user creation keys, caches, jobs,
// inventories, outboxes, statistics and audit rows are never exported.

type metadataExportQuery struct {
	kind string
	sql  string
}

func metadataExportQueries(passwordHashes bool) []metadataExportQuery {
	password := ""
	if passwordHashes {
		password = ",password_hash"
	}
	return []metadataExportQuery{
		{"library", `SELECT to_jsonb(x)::text FROM (SELECT id,name,nfo_mode,metadata_language,metadata_image_languages,metadata_preferences_revision,catalog_sync_auto FROM libraries) x ORDER BY x.id`},
		{"library_root", `SELECT to_jsonb(x)::text FROM (SELECT id,library_id,path FROM library_roots) x ORDER BY x.id`},
		{"user", `SELECT to_jsonb(x)::text FROM (SELECT id,name,is_admin,disabled,hidden,display_name,locale,created_at,deleted_at,allow_native,max_streams,max_kbps,parental_rating_max,block_unrated,content_filtered` + password + ` FROM users) x ORDER BY x.id`},
		{"library_acl", `SELECT to_jsonb(x)::text FROM (SELECT user_id,library_id FROM library_acl) x ORDER BY x.user_id,x.library_id`},
		{"item", `SELECT to_jsonb(x)::text FROM (SELECT id,library_id,kind,title FROM items) x ORDER BY x.id`},
		{"media_source", `SELECT to_jsonb(x)::text FROM (SELECT id,item_id,library_id,root_id,relative_path,content_type FROM media_sources) x ORDER BY x.id`},
		{"item_directory_source", `SELECT to_jsonb(x)::text FROM (SELECT id,item_id,library_id,kind,root_id,relative_path FROM item_directory_sources) x ORDER BY x.id`},
		{"item_parent_link", `SELECT to_jsonb(x)::text FROM (SELECT item_id,library_id,item_kind,parent_id,parent_kind FROM item_parent_links) x ORDER BY x.item_id`},
		{"catalog_scan_item", `SELECT to_jsonb(x)::text FROM (SELECT item_id,library_id,kind,group_digest,parser_version,scan_title,year,season,episode,episode_end FROM catalog_scan_items) x ORDER BY x.item_id`},
		{"catalog_scan_source", `SELECT to_jsonb(x)::text FROM (SELECT source_id,library_id,item_id,root_id,relative_path,size,modified_unix_nano,parser_version,missing_since FROM catalog_scan_sources) x ORDER BY x.source_id`},
		{"item_metadata_state", `SELECT to_jsonb(x)::text FROM (SELECT item_id,revision FROM item_metadata_state) x ORDER BY x.item_id`},
		{"item_metadata_field", `SELECT to_jsonb(x)::text FROM (SELECT item_id,field,value,source,locked,updated_at,provider_resource,provider_id,provider_source_url,provider_language,provider_fetched_at,nfo_origin FROM item_metadata_fields) x ORDER BY x.item_id,x.field`},
		{"item_metadata_fact", `SELECT to_jsonb(x)::text FROM (SELECT item_id,field,value,source,locked,updated_at,nfo_origin FROM item_metadata_facts) x ORDER BY x.item_id,x.field`},
		{"item_nfo_field_lock", `SELECT to_jsonb(x)::text FROM (SELECT item_id,field,origin FROM item_nfo_field_locks) x ORDER BY x.item_id,x.field`},
		// Unlocked image rows are rebuilt by scans and refreshes; a lock is the
		// administrator's choice and is kept with its source and content facts.
		{"item_image", `SELECT to_jsonb(x)::text FROM (SELECT id,item_id,library_id,image_type,image_index,source_kind,root_id,relative_path,remote_url,content_sha256,width,height,format,byte_size,average_color,fetched_at,source_mtime_unix_nano,source_size,locked,created_at,updated_at FROM item_images WHERE locked) x ORDER BY x.id`},
		{"user_item_data", `SELECT to_jsonb(x)::text FROM (SELECT user_id,item_id,resume_ticks,played,play_count,last_played_at,last_source_id,updated_at FROM user_item_data) x ORDER BY x.user_id,x.item_id`},
		{"access_policy", `SELECT to_jsonb(x)::text FROM (SELECT restrict_admins,block_unrated FROM access_policy WHERE id) x`},
		{"parental_rating", `SELECT to_jsonb(x)::text FROM (SELECT code,level FROM parental_ratings) x ORDER BY x.code COLLATE "C"`},
		{"user_item_access_rule", `SELECT to_jsonb(x)::text FROM (SELECT user_id,item_id,effect,created_at FROM user_item_access_rules) x ORDER BY x.user_id,x.item_id`},
		{"user_blocked_tag", `SELECT to_jsonb(x)::text FROM (SELECT user_id,tag FROM user_blocked_tags) x ORDER BY x.user_id,x.tag COLLATE "C"`},
		{"client_control_policy", `SELECT to_jsonb(x)::text FROM (SELECT unknown_clients,exempt_admins,exempt_loopback FROM client_control_policy WHERE id) x`},
		// Hit counters are observations, not configuration.
		{"client_rule", `SELECT to_jsonb(x)::text FROM (SELECT id,dimension,header_name,match_kind,pattern,case_fold,priority,action,intent,rate_requests,rate_period_seconds,scope_kind,scope_values,window_from,window_until,daily_start,daily_end,weekdays,time_zone,enabled,note,created_by,created_at,updated_at FROM client_rules) x ORDER BY x.id`},
		// Secrets stay sealed with the master key; the seal binds the endpoint
		// ID, which is why webhooks keep their ID on import.
		{"webhook", `SELECT to_jsonb(x)::text FROM (SELECT id,name,url,enabled,events,header_names,headers_sealed,timeout_ms,max_attempts,base_delay_ms,max_delay_ms,jitter,secret_sealed,previous_secret_sealed,previous_until,created_at,updated_at FROM webhooks) x ORDER BY x.id`},
		{"scan_schedule", `SELECT to_jsonb(x)::text FROM (SELECT library_id,owner_id,revision,enabled,mode,interval_seconds,cron,timezone,probe,nfo,ignore_mode,ignore_case,next_due,watch_enabled FROM scan_schedules) x ORDER BY x.library_id`},
	}
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
