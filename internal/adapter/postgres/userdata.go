package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

var _ app.UserDataRepository = (*Store)(nil)

// userDataSection is one record type of the personal data export (G07.7).
// The query selects the rows of user $1 with their JSON field names; it never
// names a secret column (password_hash, digest, token_hash, code_digest,
// secret_sealed, token_digest). Items are named by ID only: a title would
// describe an item the user may no longer see (G48). order sorts the
// selected rows.
type userDataSection struct {
	kind, query, order string
}

var userDataSections = []userDataSection{
	{"account", `SELECT u.id::text AS "id",u.name AS "name",u.display_name AS "displayName",u.locale AS "locale",u.hidden AS "hidden",u.is_admin AS "admin",
 u.disabled AS "disabled",u.allow_native AS "allowNative",u.created_at AS "createdAt",u.deleted_at AS "deletedAt",u.failed_login AS "failedLogins",
 u.locked_until AS "lockedUntil",u.max_streams AS "maxStreams",u.max_kbps AS "maxKbps",u.parental_rating_max AS "parentalRatingMax",
 u.block_unrated AS "blockUnrated",u.content_filtered AS "contentFiltered",u.share_id::text AS "shareId",
 t.enabled_at IS NOT NULL AS "twoFactorEnabled",t.enabled_at AS "twoFactorEnabledAt",
 (SELECT count(*) FROM user_recovery_codes c WHERE c.user_id=u.id AND c.used_at IS NULL) AS "recoveryCodesRemaining"
 FROM users u LEFT JOIN user_totp t ON t.user_id=u.id WHERE u.id=$1::uuid`, `"id"`},
	{"preferences", `SELECT theme AS "theme",density AS "density",layout AS "layout",updated_at AS "updatedAt" FROM user_preferences WHERE user_id=$1::uuid`, `"updatedAt"`},
	{"trackPreference", `SELECT id::text AS "id",item_id::text AS "itemId",source_id::text AS "sourceId",audio_language AS "audioLanguage",audio_commentary AS "audioCommentary",
 audio_track AS "audioTrack",subtitle_mode AS "subtitleMode",subtitle_language AS "subtitleLanguage",subtitle_sdh AS "subtitleSdh",subtitle_track AS "subtitleTrack",
 updated_at AS "updatedAt" FROM user_track_preferences WHERE user_id=$1::uuid AND (item_id IS NULL OR ` + exportItemVisible("item_id") + `)`, `"id"`},
	{"playlist", `SELECT id::text AS "id",name AS "name",public AS "public",created_at AS "createdAt",updated_at AS "updatedAt" FROM playlists WHERE owner_id=$1::uuid`, `"createdAt","id"`},
	{"playlistItem", `SELECT i.playlist_id::text AS "playlistId",i.item_id::text AS "itemId",i.position AS "position",i.added_at AS "addedAt"
 FROM playlist_items i JOIN playlists p ON p.id=i.playlist_id WHERE p.owner_id=$1::uuid AND ` + exportItemVisible("i.item_id"), `"playlistId","position","itemId"`},
	{"blockedTag", `SELECT tag AS "tag" FROM user_blocked_tags WHERE user_id=$1::uuid`, `"tag"`},
	{"blockedKeyword", `SELECT keyword AS "keyword" FROM user_blocked_keywords WHERE user_id=$1::uuid`, `"keyword"`},
	{"accessWindow", `SELECT position AS "position",weekdays AS "weekdays",start_minute AS "startMinute",end_minute AS "endMinute",time_zone AS "timeZone",rating_max AS "ratingMax"
 FROM user_access_windows WHERE user_id=$1::uuid`, `"position"`},
	{"libraryAccess", `SELECT a.library_id::text AS "libraryId",l.name AS "libraryName" FROM library_acl a JOIN libraries l ON l.id=a.library_id WHERE a.user_id=$1::uuid AND ` + exportLibraryVisible("a.library_id"), `"libraryId"`},
	{"itemAccessRule", `SELECT r.item_id::text AS "itemId",r.effect AS "effect",r.created_at AS "createdAt"
 FROM user_item_access_rules r WHERE r.user_id=$1::uuid AND ` + exportItemVisible("r.item_id"), `"itemId"`},
	{"itemData", `SELECT d.item_id::text AS "itemId",d.resume_ticks AS "resumeTicks",d.played AS "played",d.play_count AS "playCount",
 d.last_played_at AS "lastPlayedAt",d.last_source_id::text AS "lastSourceId",d.updated_at AS "updatedAt"
 FROM user_item_data d WHERE d.user_id=$1::uuid AND ` + exportItemVisible("d.item_id"), `"itemId"`},
	{"playbackSession", `SELECT p.id::text AS "id",p.item_id::text AS "itemId",p.library_id::text AS "libraryId",p.source_id::text AS "sourceId",
 p.device_id AS "deviceId",p.client_name AS "clientName",p.delivery AS "delivery",p.state AS "state",p.failure_reason AS "failureReason",
 p.started_at AS "startedAt",p.last_report_at AS "lastReportAt",p.ended_at AS "endedAt",p.position_ticks AS "positionTicks",p.runtime_ticks AS "runtimeTicks",
 p.paused AS "paused",p.report_count AS "reportCount",p.sample_count AS "sampleCount",p.stats_counted AS "counted",p.stats_completed AS "completed"
 FROM playback_sessions p WHERE p.user_id=$1::uuid AND ` + exportItemVisible("p.item_id"), `"startedAt","id"`},
	{"playbackSample", `SELECT s.session_id::text AS "sessionId",s.seq AS "seq",s.at AS "at",s.kind AS "kind",s.position_ticks AS "positionTicks",s.paused AS "paused"
 FROM playback_samples s JOIN playback_sessions p ON p.id=s.session_id WHERE p.user_id=$1::uuid AND ` + exportItemVisible("p.item_id"), `"sessionId","seq"`},
	{"watchStatsDay", `SELECT to_char(d.day,'YYYY-MM-DD') AS "day",d.item_id::text AS "itemId",d.library_id::text AS "libraryId",
 d.effective_ms AS "effectiveMillis",d.sessions AS "sessions",d.views AS "views",d.first_plays AS "firstPlays",d.rewatches AS "rewatches",
 d.completions AS "completions",d.updated_at AS "updatedAt" FROM watch_stats_daily d WHERE d.user_id=$1::uuid AND ` + exportItemVisible("d.item_id"), `"day","itemId"`},
	{"watchStatsItem", `SELECT h.item_id::text AS "itemId",h.views AS "views",h.completions AS "completions",h.last_completed AS "lastCompleted",
 h.updated_at AS "updatedAt" FROM watch_stats_history h WHERE h.user_id=$1::uuid AND ` + exportItemVisible("h.item_id"), `"itemId"`},
	{"session", `SELECT id::text AS "id",client_kind AS "clientKind",device_name AS "deviceName",device_id AS "deviceId",client_name AS "clientName",
 client_version AS "clientVersion",created_at AS "createdAt",expires_at AS "expiresAt",revoked_at AS "revokedAt",last_seen_at AS "lastSeenAt",
 last_ip AS "lastIp",app_password_id::text AS "appPasswordId" FROM sessions WHERE user_id=$1::uuid`, `"createdAt","id"`},
	{"appPassword", `SELECT id::text AS "id",name AS "name",created_at AS "createdAt",last_used_at AS "lastUsedAt" FROM app_passwords WHERE user_id=$1::uuid`, `"createdAt","id"`},
	{"shareLink", `SELECT id::text AS "id",library_id::text AS "libraryId",item_id::text AS "itemId",expires_at AS "expiresAt",read_only AS "readOnly",
 allow_playback AS "allowPlayback",max_streams AS "maxStreams",note AS "note",created_at AS "createdAt",revoked_at AS "revokedAt"
 FROM share_links sl WHERE created_by=$1::uuid AND (sl.library_id IS NULL OR ` + exportLibraryVisible("sl.library_id") + `) AND (sl.item_id IS NULL OR ` + exportItemVisible("sl.item_id") + `)`, `"createdAt","id"`},
	{"clientControlHit", `SELECT bucket AS "bucket",action AS "action",surface AS "surface",mode AS "mode",host(ip) AS "ip",user_agent AS "userAgent",
 app_name AS "appName",hits AS "hits" FROM client_control_hits WHERE user_id=$1::uuid`, `"bucket","action","surface"`},
	// Audit events name the event, not its states: a state may describe
	// another user. The IP is the user's own only when they were the actor.
	{"auditEvent", `SELECT id AS "id",category AS "category",event AS "event",occurred_at AS "occurredAt",
 CASE WHEN actor_id=$1::uuid AND target_id=$1::uuid THEN 'actor_and_target' WHEN actor_id=$1::uuid THEN 'actor' ELSE 'target' END AS "role",
 CASE WHEN actor_id=$1::uuid THEN host(actor_ip) END AS "ip"
 FROM audit_logs WHERE actor_id=$1::uuid OR target_id=$1::uuid`, `"id"`},
}

// exportItemVisible and exportLibraryVisible keep a row only when its item
// or library is visible to the exported user under the current request
// restriction ($2, requestScopeArg): an export must not reveal what that
// user can no longer see (G48). Rows about hidden items are still deleted
// with the account.
func exportItemVisible(item string) string {
	return `EXISTS(SELECT 1 FROM principal u JOIN items vi ON vi.id=` + item + ` WHERE ` + itemVisibleSQL("$2", "vi.library_id", "vi.id") + `)`
}

func exportLibraryVisible(library string) string {
	return `EXISTS(SELECT 1 FROM principal u WHERE ` + libraryVisibleSQL("$2", library) + `)`
}

// ExportUserData authorizes and audits the export in an account transaction,
// then streams every section from one read-only snapshot. Rows are read and
// written one at a time, so the export holds no section in memory.
func (s *Store) ExportUserData(ctx context.Context, actor domain.Actor, userID string, begin func(domain.UserDataExportHeader) error, write func(domain.UserDataRecord) error) error {
	if ctx == nil || begin == nil || write == nil {
		return domain.ErrInvalid
	}
	tx, admin, err := s.authorizedTransaction(ctx, actor, false)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if !admin && !strings.EqualFold(actor.UserID, userID) {
		return domain.ErrForbidden
	}
	subject, err := userInTransaction(ctx, tx, userID)
	if err != nil {
		return err
	}
	userID = subject.ID
	if err = auditAccount(ctx, tx, actor, "user.data_exported", userID, nil, map[string]any{"format": domain.UserDataExportFormat, "version": domain.UserDataExportVersion, "self": strings.EqualFold(actor.UserID, userID)}); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return storageError(err)
	}
	snapshot, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return storageError(err)
	}
	defer snapshot.Rollback(ctx)
	header := domain.UserDataExportHeader{Format: domain.UserDataExportFormat, Version: domain.UserDataExportVersion, UserID: userID}
	if err = snapshot.QueryRow(ctx, `SELECT name,now() FROM users WHERE id=$1::uuid`, userID).Scan(&header.UserName, &header.GeneratedAt); err != nil {
		return storageError(err)
	}
	header.GeneratedAt = header.GeneratedAt.UTC()
	if err = begin(header); err != nil {
		return err
	}
	for _, section := range userDataSections {
		if err = streamUserDataSection(ctx, snapshot, section, userID, requestScopeArg(ctx), write); err != nil {
			return err
		}
	}
	return nil
}

func streamUserDataSection(ctx context.Context, tx pgx.Tx, section userDataSection, userID string, rq any, write func(domain.UserDataRecord) error) error {
	rows, err := tx.Query(ctx, `WITH principal AS MATERIALIZED (SELECT `+visibilityUserColumns+` FROM users WHERE id=$1::uuid AND $2::jsonb IS NOT DISTINCT FROM $2::jsonb)
SELECT jsonb_strip_nulls(to_jsonb(x))::text FROM (`+section.query+`) x ORDER BY `+section.order, userID, rq)
	if err != nil {
		return storageError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var data string
		if err = rows.Scan(&data); err != nil {
			return storageError(err)
		}
		if err = write(domain.UserDataRecord{Type: section.kind, Data: json.RawMessage(data)}); err != nil {
			return err
		}
	}
	return storageError(rows.Err())
}

// userPurgeStatements remove what belongs to the user but does not go with
// the users row by itself: rows whose reference has no ON DELETE action, or
// that refer to the user without a foreign key. Everything else cascades
// from DELETE FROM users (sessions, application passwords, second factor,
// preferences, access grants, playback sessions and samples, user data,
// watch statistics) or keeps an anonymous row through ON DELETE SET NULL
// (job, version operation, inventory acceptance, client and network rule
// attribution). TestUserPurgeLeavesNoUserRows checks this list against the
// catalog.
var userPurgeStatements = []string{
	`DELETE FROM user_creation_keys WHERE actor_id=$1::uuid OR user_id=$1::uuid`,
	`DELETE FROM nfo_policy_requests WHERE actor_id=$1::uuid`,
	`DELETE FROM nfo_write_preparations WHERE actor_id=$1::uuid`,
	`DELETE FROM client_control_hits WHERE user_id=$1::uuid`,
	`UPDATE known_clients SET last_ip=NULL WHERE last_user_id=$1::uuid`,
	`DELETE FROM legacy_import_map WHERE (kind='user' AND target_id=$1::uuid) OR target_user=$1::uuid`,
	// Shares the user created end with them; their guest accounts cascade.
	`DELETE FROM share_links WHERE created_by=$1::uuid`,
	// Undelivered and delivered webhook events about the user or their
	// playback.
	`DELETE FROM webhook_outbox WHERE (subject_kind='user' AND subject_id=$1::text) OR data->>'userId'=$1::text`,
}

// PurgeUser permanently deletes a user and everything that belongs to them
// in one transaction (G07.7). Nothing is kept for a restore. Audit events
// stay for their retention but are de-identified (redact_audit_subject).
// Scan schedules the user owned move to the acting administrator, or for a
// self deletion to the longest-standing other active administrator, so
// library schedules survive. The last active administrator cannot be
// deleted.
func (s *Store) PurgeUser(ctx context.Context, actor domain.Actor, userID string, proof *domain.UserPurgeProof) error {
	self := proof != nil
	if self != strings.EqualFold(actor.UserID, userID) || !domain.ValidID(userID) {
		return domain.ErrForbidden
	}
	var tx pgx.Tx
	var err error
	if self {
		tx, err = s.selfWebTransaction(ctx, actor)
	} else {
		tx, _, err = s.authorizedTransaction(ctx, actor, true)
		if err == nil {
			if err = requireWebSession(ctx, tx, actor); err != nil {
				_ = tx.Rollback(ctx)
			}
		}
	}
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// The cascade of a heavy user (playback samples, statistics) may take
	// longer than an ordinary account statement.
	if _, err = tx.Exec(ctx, `SET LOCAL statement_timeout='30s'`); err != nil {
		return storageError(err)
	}
	old, err := userInTransaction(ctx, tx, userID)
	if err != nil {
		return err
	}
	// Text comparisons (webhook subjects, client rule scopes) use the
	// canonical spelling.
	userID = old.ID
	if self {
		if err = verifyPurgeProof(ctx, tx, userID, *proof); err != nil {
			return err
		}
	}
	gone := old
	now := time.Now()
	gone.DeletedAt = &now
	if err = protectLastAdmin(ctx, tx, old, gone); err != nil {
		return err
	}
	successor := actor.UserID
	if self {
		successor = ""
		err = tx.QueryRow(ctx, `SELECT id::text FROM users WHERE is_admin AND NOT disabled AND deleted_at IS NULL AND id<>$1::uuid ORDER BY created_at,id LIMIT 1`, userID).Scan(&successor)
		if errors.Is(err, pgx.ErrNoRows) {
			// No administrator could take over a schedule the user owns.
			err = tx.QueryRow(ctx, `SELECT CASE WHEN EXISTS(SELECT 1 FROM scan_schedules WHERE owner_id=$1::uuid) THEN 'none' ELSE '' END`, userID).Scan(&successor)
			if err == nil && successor != "" {
				return domain.ErrLastAdmin
			}
		}
		if err != nil {
			return storageError(err)
		}
	}
	tag, err := tx.Exec(ctx, `UPDATE scan_schedules SET owner_id=$2::uuid,updated_at=clock_timestamp() WHERE owner_id=$1::uuid`, userID, successor)
	if err != nil {
		return storageError(err)
	}
	transferred := tag.RowsAffected()
	rules, err := purgeUserFromClientRules(ctx, tx, userID)
	if err != nil {
		return err
	}
	var objects []string
	if err = tx.QueryRow(ctx, `SELECT COALESCE(array_agg(id::text),'{}') FROM (SELECT id FROM sessions WHERE user_id=$1::uuid UNION ALL SELECT id FROM app_passwords WHERE user_id=$1::uuid
 UNION ALL SELECT id FROM share_links WHERE created_by=$1::uuid UNION ALL SELECT id FROM playback_sessions WHERE user_id=$1::uuid) o`, userID).Scan(&objects); err != nil {
		return storageError(err)
	}
	var shares int64
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM share_links WHERE created_by=$1::uuid`, userID).Scan(&shares); err != nil {
		return storageError(err)
	}
	for _, statement := range userPurgeStatements {
		if _, err = tx.Exec(ctx, statement, userID); err != nil {
			return storageError(err)
		}
	}
	if _, err = tx.Exec(ctx, `DELETE FROM users WHERE id=$1::uuid`, userID); err != nil {
		return storageError(err)
	}
	var redacted int64
	if err = tx.QueryRow(ctx, `SELECT redact_audit_subject(current_schema(),$1::uuid,$2::uuid[])`, userID, objects).Scan(&redacted); err != nil {
		return storageError(err)
	}
	// The deleting user's own IP is not recorded with the final event.
	recorder := actor
	if self {
		recorder.IP = ""
	}
	if err = auditAccount(ctx, tx, recorder, "user.purged", userID, nil, map[string]any{
		"permanent": true, "self": self, "wasAdmin": old.Admin, "wasDeleted": old.DeletedAt != nil,
		"schedulesTransferred": transferred, "sharesDeleted": shares, "clientRulesChanged": rules, "auditEventsRedacted": redacted,
	}); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}

// verifyPurgeProof checks the re-authentication of a self deletion inside
// the deleting transaction: the verified password is still current and,
// when the account has a second factor, one code or recovery code verifies.
func verifyPurgeProof(ctx context.Context, tx pgx.Tx, userID string, proof domain.UserPurgeProof) error {
	current, err := scanCredentials(tx.QueryRow(ctx, `SELECT `+credentialColumns+` FROM users WHERE id=$1::uuid FOR UPDATE`, userID))
	if err != nil {
		return err
	}
	if proof.Expected.UserID != userID || current.PasswordHash != proof.Expected.PasswordHash || current.Version != proof.Expected.Version {
		return domain.ErrConflict
	}
	var enabled bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM user_totp WHERE user_id=$1::uuid AND enabled_at IS NOT NULL)`, userID).Scan(&enabled); err != nil {
		return storageError(err)
	}
	switch {
	case !enabled:
		return nil
	case proof.Verify != nil && proof.Recovery == nil:
		return verifyTOTP(ctx, tx, userID, true, proof.Verify)
	case proof.Recovery != nil && proof.Verify == nil && validDigest(proof.Recovery):
		return spendRecoveryCode(ctx, tx, userID, proof.Recovery)
	default:
		return domain.ErrSecondFactorMismatch
	}
}

// purgeUserFromClientRules removes the user from user-scoped client control
// rules (G47): a rule left without users is deleted, since it would apply to
// nobody. The policy version moves so every instance recompiles.
func purgeUserFromClientRules(ctx context.Context, tx pgx.Tx, userID string) (int64, error) {
	var listed bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM client_rules WHERE scope_kind='user' AND EXISTS(SELECT 1 FROM unnest(scope_values) v WHERE lower(v)=$1::text))`, userID).Scan(&listed); err != nil || !listed {
		return 0, storageError(err)
	}
	if _, err := lockClientPolicy(ctx, tx); err != nil {
		return 0, err
	}
	deleted, err := tx.Exec(ctx, `DELETE FROM client_rules WHERE scope_kind='user' AND NOT EXISTS(SELECT 1 FROM unnest(scope_values) v WHERE lower(v)<>$1::text)`, userID)
	if err != nil {
		return 0, storageError(err)
	}
	updated, err := tx.Exec(ctx, `UPDATE client_rules SET scope_values=ARRAY(SELECT v FROM unnest(scope_values) WITH ORDINALITY u(v,n) WHERE lower(v)<>$1::text ORDER BY n),updated_at=now()
 WHERE scope_kind='user' AND EXISTS(SELECT 1 FROM unnest(scope_values) v WHERE lower(v)=$1::text)`, userID)
	if err != nil {
		return 0, storageError(err)
	}
	return deleted.RowsAffected() + updated.RowsAffected(), bumpClientVersion(ctx, tx)
}
