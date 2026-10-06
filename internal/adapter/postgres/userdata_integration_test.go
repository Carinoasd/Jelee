package postgres

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

// userReferenceColumns is every column that refers to a user, with what a
// permanent deletion does to it (G07.7). Foreign keys come from the catalog;
// TestUserPurgeLeavesNoUserRows fails when a new one is not listed here, so
// a table added later cannot keep a deleted user's rows unnoticed.
var userReferenceColumns = map[string]string{
	// ON DELETE CASCADE from users.
	"app_passwords.user_id":          "cascade",
	"library_acl.user_id":            "cascade",
	"login_challenges.user_id":       "cascade",
	"playback_sessions.user_id":      "cascade",
	"playlists.owner_id":             "cascade",
	"sessions.user_id":               "cascade",
	"user_blocked_tags.user_id":      "cascade",
	"user_item_access_rules.user_id": "cascade",
	"user_item_data.user_id":         "cascade",
	"user_preferences.user_id":       "cascade",
	"user_recovery_codes.user_id":    "cascade",
	"user_totp.user_id":              "cascade",
	"user_track_preferences.user_id": "cascade",
	"watch_stats_daily.user_id":      "cascade",
	"watch_stats_history.user_id":    "cascade",
	// ON DELETE SET NULL: the row stays without attribution.
	"client_rules.created_by":                "set null",
	"collections.created_by":                 "set null",
	"inventory_missing_acceptances.actor_id": "set null",
	"item_version_operations.actor_id":       "set null",
	"item_version_operations.undone_by":      "set null",
	"jobs.actor_id":                          "set null",
	"known_clients.last_user_id":             "set null",
	"library_network_rules.created_by":       "set null",
	"repair_runs.actor_id":                   "set null",
	"share_links.revoked_by":                 "set null",
	// No action or restrict: PurgeUser deletes or moves the rows first.
	"nfo_policy_requests.actor_id":    "explicit",
	"nfo_write_preparations.actor_id": "explicit",
	"scan_schedules.owner_id":         "explicit",
	"share_links.created_by":          "explicit",
	"user_creation_keys.actor_id":     "explicit",
	"user_creation_keys.user_id":      "explicit",
	// Without a foreign key.
	"client_control_hits.user_id":   "explicit",
	"legacy_import_map.target_user": "explicit",
	"audit_logs.actor_id":           "redacted",
	"audit_logs.target_id":          "redacted",
}

// userColumnPattern finds uuid columns that look like user references but
// have no foreign key; each must be listed above or in notUserColumns.
const userColumnPattern = `(^|_)(user|actor|owner)(_id)?$|_by$|^target_user$`

var notUserColumns = map[string]bool{
	"scan_watch_state.lease_owner": true, // a process lease, not an account
}

type userDataFixture struct {
	progressFixture
}

func userDataDigest(seed string) []byte {
	sum := sha256.Sum256([]byte(seed))
	return sum[:]
}

func (f userDataFixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := f.s.Pool.Exec(f.ctx, query, args...); err != nil {
		t.Fatalf("%s: %v", strings.SplitN(query, "(", 2)[0], err)
	}
}

func (f userDataFixture) number(t *testing.T, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := f.s.Pool.QueryRow(f.ctx, query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// webAdmin makes user an administrator with a password and a web session.
func (f userDataFixture) webActor(t *testing.T, user domain.Actor, name string, admin bool) domain.Actor {
	t.Helper()
	f.exec(t, `UPDATE users SET is_admin=$2,password_hash=$3 WHERE id=$1::uuid`, user.UserID, admin, accountTestHash)
	web := accountActor(accountLogin(t, f.ctx, f.s, name))
	web.IP = "203.0.113.9"
	return web
}

// seed gives the user a row in every table that can hold user data, through
// the store where it has an operation and by statement otherwise.
func (f userDataFixture) seed(t *testing.T, native, web domain.Actor) (share, guest string) {
	t.Helper()
	uid := native.UserID
	f.report(t, native, domain.PlaybackReportStart, "purge-play", f.item, 10*time.Minute)
	f.report(t, native, domain.PlaybackReportProgress, "purge-play", f.item, 20*time.Minute)
	if _, err := f.progress.Flush(f.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.SetPreferences(f.ctx, web, domain.UserPreferences{Theme: "dark", Density: "compact"}); err != nil {
		t.Fatal(err)
	}
	lib := f.registration.Library.ID
	f.exec(t, `INSERT INTO user_track_preferences(user_id,item_id,audio_language) VALUES($1::uuid,$2::uuid,'ja')`, uid, f.item)
	f.exec(t, `INSERT INTO user_blocked_tags(user_id,tag) VALUES($1::uuid,'horror')`, uid)
	f.exec(t, `WITH p AS (INSERT INTO playlists(owner_id,name) VALUES($1::uuid,'purge list') RETURNING id) INSERT INTO playlist_items(playlist_id,item_id,position) SELECT id,$2::uuid,0 FROM p`, uid, f.item)
	f.exec(t, `INSERT INTO collections(name,created_by) VALUES('purge collection',$1::uuid)`, uid)
	f.exec(t, `INSERT INTO repair_runs(action,origin,actor_id) VALUES('stats','api',$1::uuid)`, uid)
	f.exec(t, `INSERT INTO user_item_access_rules(user_id,item_id,effect) VALUES($1::uuid,$2::uuid,'allow')`, uid, f.item)
	f.exec(t, `INSERT INTO watch_stats_daily(user_id,day,item_id,library_id,effective_ms,sessions,views,first_plays) VALUES($1::uuid,current_date,$2::uuid,$3::uuid,60000,1,1,1)`, uid, f.item, lib)
	f.exec(t, `INSERT INTO watch_stats_history(user_id,item_id,views) VALUES($1::uuid,$2::uuid,1)`, uid, f.item)
	f.exec(t, `INSERT INTO app_passwords(user_id,name,digest) VALUES($1::uuid,'tv',$2)`, uid, userDataDigest("app-"+uid))
	f.exec(t, `INSERT INTO login_challenges(user_id,token_digest,auth_version,device_name,expires_at) VALUES($1::uuid,$2,1,'phone',now()+interval '5 minutes')`, uid, userDataDigest("challenge-"+uid))
	f.exec(t, `INSERT INTO client_control_hits(bucket,mode,action,surface,user_id,ip,user_agent,hits) VALUES(date_trunc('hour',now()),'default','allow','native',$1::uuid,'198.51.100.7','agent',3)`, uid)
	f.exec(t, `INSERT INTO known_clients(client_key,device_name,last_ip,last_user_id) VALUES($1,'Purge phone','198.51.100.7',$2::uuid)`, hex.EncodeToString(userDataDigest("client-"+uid)), uid)
	f.exec(t, `INSERT INTO known_client_sessions(client_id,session_id) SELECT id,$2::uuid FROM known_clients WHERE last_user_id=$1::uuid`, uid, native.SessionID)
	f.exec(t, `INSERT INTO nfo_policy_requests(actor_id,idempotency_key,library_id,requested_mode,expected_generation,result_generation) SELECT $1::uuid,'purge-key',id,'off',nfo_generation,nfo_generation FROM libraries WHERE id=$2::uuid`, uid, lib)
	f.exec(t, `INSERT INTO scan_schedules(library_id,owner_id,revision,enabled,mode,interval_seconds,cron,timezone,probe,nfo,ignore_mode,ignore_case) VALUES($2::uuid,$1::uuid,1,false,'interval',3600,'','UTC',false,false,'','')`, uid, lib)
	f.exec(t, `INSERT INTO legacy_import_map(kind,source_key,target_id,created) VALUES('user','legacy-purge',$1::uuid,true)`, uid)
	f.exec(t, `INSERT INTO legacy_import_map(kind,source_key,target_id,target_user,created) VALUES('user_data','legacy-purge-data',$2::uuid,$1::uuid,true)`, uid, f.item)
	f.exec(t, `INSERT INTO webhook_outbox(event_type,occurred_at,subject_kind,subject_id,data) VALUES('user.login',now(),'user',$1,'{}'),('playback.started',now(),'session',gen_random_uuid()::text,jsonb_build_object('userId',$1::text))`, uid)
	f.exec(t, `INSERT INTO client_rules(dimension,match_kind,pattern,action,scope_kind,scope_values,created_by) VALUES('app_name','exact','x','deny','user',ARRAY[$1::text],$1::uuid),
 ('app_name','exact','y','deny','user',ARRAY[upper($1::text),$2::text],$1::uuid)`, uid, f.a.UserID)
	f.exec(t, `INSERT INTO library_network_rules(library_id,network,cidrs,client_kinds,enabled,created_by) VALUES($2::uuid,'lan','{10.0.0.0/8}','{web}',false,$1::uuid)`, uid, lib)
	f.exec(t, `INSERT INTO user_totp(user_id,secret_sealed,enabled_at) VALUES($1::uuid,$2,now())`, uid, []byte(strings.Repeat("s", 48)))
	f.exec(t, `INSERT INTO user_recovery_codes(user_id,code_digest) VALUES($1::uuid,$2)`, uid, userDataDigest("recovery-"+uid))
	if _, _, err := f.s.CreateUser(f.ctx, web, accountInput("purge-created"), "purge-create-key"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.s.SubmitJob(f.ctx, web, lib, "purge-job", domain.JobPriorityManual, f.policy); err != nil {
		t.Fatal(err)
	}
	g, err := f.s.CreateShare(f.ctx, web, domain.ShareInput{ItemID: f.item, ExpiresAt: time.Now().Add(time.Hour), MaxStreams: 1, Note: "for a friend"})
	if err != nil {
		t.Fatal(err)
	}
	grant, err := f.s.RedeemShare(f.ctx, domain.ShareRedemption{Token: g.Token, MaxSessions: 4, SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.CreateShare(f.ctx, f.a, domain.ShareInput{ItemID: f.item, ExpiresAt: time.Now().Add(time.Hour), MaxStreams: 1}); err != nil {
		t.Fatal(err)
	}
	return g.Share.ID, grant.User.ID
}

func newUserDataFixture(t *testing.T) userDataFixture {
	t.Helper()
	return userDataFixture{progressFixture: newProgressFixture(t)}
}

func TestUserPurgeLeavesNoUserRows(t *testing.T) {
	f := newUserDataFixture(t)
	native := f.viewer
	web := f.webActor(t, native, "progress-viewer", true)
	uid := native.UserID
	share, guest := f.seed(t, native, web)
	// A bystander keeps everything.
	bystander := imageRepositoryActor(t, f.jobFixture, "purge-bystander", "native")
	f.exec(t, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid)`, bystander.UserID, f.registration.Library.ID)
	f.report(t, bystander, domain.PlaybackReportStart, "bystander-play", f.item, time.Minute)
	if _, err := f.progress.Flush(f.ctx); err != nil {
		t.Fatal(err)
	}
	auditBefore := f.number(t, `SELECT count(*) FROM audit_logs WHERE actor_id=$1::uuid OR target_id=$1::uuid`, uid)

	// A non-administrator cannot, a native session cannot, the target's
	// own administrator session must use the self path.
	if err := f.s.PurgeUser(f.ctx, bystander, uid, nil); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("non-administrator purge: %v", err)
	}
	if err := f.s.PurgeUser(f.ctx, web, uid, nil); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("own account through the administrator path: %v", err)
	}
	nativeAdmin := imageRepositoryActor(t, f.jobFixture, "purge-native-admin", "native")
	f.exec(t, `UPDATE users SET is_admin=true WHERE id=$1::uuid`, nativeAdmin.UserID)
	if err := f.s.PurgeUser(f.ctx, nativeAdmin, uid, nil); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("native administrator session purge: %v", err)
	}
	f.exec(t, `UPDATE users SET is_admin=false WHERE id=$1::uuid`, nativeAdmin.UserID)

	if err := f.s.PurgeUser(f.ctx, f.a, uid, nil); err != nil {
		t.Fatalf("administrator purge: %v", err)
	}

	// Catalog guard: every foreign key to users is accounted for.
	rows, err := f.s.Pool.Query(f.ctx, `SELECT c.conrelid::regclass::text||'.'||a.attname FROM pg_constraint c
 JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attnum=ANY(c.conkey)
 WHERE c.contype='f' AND c.confrelid='users'::regclass AND c.connamespace=current_schema()::regnamespace`)
	if err != nil {
		t.Fatal(err)
	}
	var foreign []string
	for rows.Next() {
		var column string
		if err = rows.Scan(&column); err != nil {
			t.Fatal(err)
		}
		foreign = append(foreign, column)
	}
	rows.Close()
	if len(foreign) == 0 {
		t.Fatal("catalog lists no foreign key to users")
	}
	for _, column := range foreign {
		if _, ok := userReferenceColumns[column]; !ok {
			t.Errorf("foreign key %s to users is not handled by PurgeUser; add it to userPurgeStatements or userReferenceColumns", column)
		}
	}
	rows, err = f.s.Pool.Query(f.ctx, `SELECT table_name||'.'||column_name FROM information_schema.columns
 WHERE table_schema=current_schema() AND data_type='uuid' AND column_name ~ $1`, userColumnPattern)
	if err != nil {
		t.Fatal(err)
	}
	var lookalikes []string
	for rows.Next() {
		var column string
		if err = rows.Scan(&column); err != nil {
			t.Fatal(err)
		}
		lookalikes = append(lookalikes, column)
	}
	rows.Close()
	for _, column := range lookalikes {
		if _, ok := userReferenceColumns[column]; !ok && !notUserColumns[column] && !slices.Contains(foreign, column) {
			t.Errorf("uuid column %s looks like a user reference without a foreign key; handle it in PurgeUser and list it", column)
		}
	}

	// No row refers to the deleted user any more, except de-identified audit
	// events.
	columns := make([]string, 0, len(userReferenceColumns))
	for column := range userReferenceColumns {
		columns = append(columns, column)
	}
	sort.Strings(columns)
	for _, column := range columns {
		if userReferenceColumns[column] == "redacted" {
			continue
		}
		table, name, _ := strings.Cut(column, ".")
		if n := f.number(t, `SELECT count(*) FROM `+table+` WHERE `+name+`=$1::uuid`, uid); n != 0 {
			t.Errorf("%s keeps %d rows of the deleted user", column, n)
		}
	}
	for name, query := range map[string]string{
		"users":                 `SELECT count(*) FROM users WHERE id=@uid::uuid`,
		"guest of a share":      `SELECT count(*) FROM users WHERE id=@guest::uuid`,
		"share":                 `SELECT count(*) FROM share_links WHERE id=@share::uuid`,
		"playback samples":      `SELECT count(*) FROM playback_samples s LEFT JOIN playback_sessions p ON p.id=s.session_id WHERE p.id IS NULL`,
		"known client sessions": `SELECT count(*) FROM known_client_sessions k LEFT JOIN sessions s ON s.id=k.session_id WHERE s.id IS NULL`,
		"known client address":  `SELECT count(*) FROM known_clients WHERE device_name='Purge phone' AND (last_ip IS NOT NULL OR last_user_id IS NOT NULL)`,
		"webhook events":        `SELECT count(*) FROM webhook_outbox WHERE subject_id=@uid::text OR data->>'userId'=@uid::text`,
		"client rule scopes":    `SELECT count(*) FROM client_rules WHERE EXISTS(SELECT 1 FROM unnest(scope_values) v WHERE lower(v)=@uid::text)`,
		"audit actor addresses": `SELECT count(*) FROM audit_logs WHERE actor_id=@uid::uuid AND actor_ip IS NOT NULL`,
		"audit target states": `SELECT count(*) FROM audit_logs WHERE target_id=@uid::uuid AND event<>'user.purged' AND (before_state IS NOT NULL AND jsonb_typeof(before_state)<>'null' AND before_state<>'{"redacted":"user_purged"}'
 OR after_state IS NOT NULL AND jsonb_typeof(after_state)<>'null' AND after_state<>'{"redacted":"user_purged"}')`,
		"audit text":     `SELECT count(*) FROM audit_logs WHERE before_state::text LIKE '%progress-viewer%' OR after_state::text LIKE '%progress-viewer%' OR before_state::text LIKE '%for a friend%' OR after_state::text LIKE '%for a friend%'`,
		"session states": `SELECT count(*) FROM audit_logs WHERE event IN ('session.created','session.rotated') AND after_state::text LIKE '%203.0.113.9%'`,
	} {
		if n := f.number(t, query, pgx.NamedArgs{"uid": uid, "guest": guest, "share": share}); n != 0 {
			t.Errorf("%s: %d rows remain", name, n)
		}
	}
	// What other users own is untouched; what moved or became anonymous is
	// still there.
	for name, query := range map[string]string{
		"bystander playback":        `SELECT count(*)-1 FROM playback_sessions WHERE user_id=@bystander::uuid`,
		"bystander account":         `SELECT count(*)-1 FROM users WHERE id=@bystander::uuid`,
		"other administrator share": `SELECT count(*)-1 FROM share_links WHERE created_by=@admin::uuid`,
		"schedule moved":            `SELECT count(*)-1 FROM scan_schedules WHERE owner_id=@admin::uuid`,
		"shared rule kept":          `SELECT count(*)-1 FROM client_rules WHERE pattern='y' AND scope_values=ARRAY[@admin::text]`,
		"own rule removed":          `SELECT count(*) FROM client_rules WHERE pattern='x'`,
		"anonymous job":             `SELECT count(*)-1 FROM jobs WHERE actor_id IS NULL AND idempotency_key='purge-job'`,
		"anonymous network rule":    `SELECT count(*)-1 FROM library_network_rules WHERE created_by IS NULL`,
		"anonymous collection":      `SELECT count(*)-1 FROM collections WHERE name='purge collection' AND created_by IS NULL`,
		"anonymous repair run":      `SELECT count(*)-1 FROM repair_runs WHERE origin='api' AND actor_id IS NULL`,
		"playlist items removed":    `SELECT count(*) FROM playlist_items i LEFT JOIN playlists p ON p.id=i.playlist_id WHERE p.id IS NULL`,
		"created user kept":         `SELECT count(*)-1 FROM users WHERE name='purge-created'`,
		"audit events kept":         `SELECT count(*)-` + strconv.FormatInt(auditBefore, 10) + ` FROM audit_logs WHERE (actor_id=@uid::uuid OR target_id=@uid::uuid) AND event<>'user.purged'`,
		"purge audited":             `SELECT count(*)-1 FROM audit_logs WHERE event='user.purged' AND target_id=@uid::uuid AND actor_id=@admin::uuid AND after_state->>'self'='false' AND (after_state->>'schedulesTransferred')::int=1`,
	} {
		if n := f.number(t, query, pgx.NamedArgs{"bystander": bystander.UserID, "admin": f.a.UserID, "uid": uid}); n != 0 {
			t.Errorf("%s: off by %d", name, n)
		}
	}
	// Nothing can bring the account back.
	if _, err = f.s.RestoreUser(f.ctx, f.a, uid); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("restore after purge: %v", err)
	}
	if err = f.s.PurgeUser(f.ctx, f.a, uid, nil); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("second purge: %v", err)
	}
	// The audit log is still append-only outside the redaction function, and
	// that function cannot touch an existing account's events.
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE audit_logs SET actor_ip=NULL WHERE actor_id=$1::uuid`, f.a.UserID); err == nil {
		t.Fatal("audit update accepted")
	}
	if _, err = f.s.Pool.Exec(f.ctx, `SELECT redact_audit_subject(current_schema(),$1::uuid,'{}')`, f.a.UserID); err == nil {
		t.Fatal("redaction of a live account accepted")
	}
}

func TestUserPurgeSelfReauthenticationAndLastAdmin(t *testing.T) {
	f := newUserDataFixture(t)
	native := f.viewer
	web := f.webActor(t, native, "progress-viewer", false)
	uid := native.UserID
	credentials, err := f.s.CredentialsFor(f.ctx, web, uid)
	if err != nil {
		t.Fatal(err)
	}
	proof := &domain.UserPurgeProof{Expected: credentials}
	// Native sessions cannot delete the account.
	if err = f.s.PurgeUser(f.ctx, native, uid, proof); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("native self purge: %v", err)
	}
	// A stale credential snapshot (password changed since) is refused.
	stale := *proof
	stale.Expected.Version--
	if err = f.s.PurgeUser(f.ctx, web, uid, &stale); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale snapshot: %v", err)
	}
	// With a second factor, the password alone is not enough, a wrong or
	// spent recovery code fails and changes nothing.
	f.exec(t, `INSERT INTO user_totp(user_id,secret_sealed,enabled_at) VALUES($1::uuid,$2,now())`, uid, []byte(strings.Repeat("s", 48)))
	f.exec(t, `INSERT INTO user_recovery_codes(user_id,code_digest) VALUES($1::uuid,$2),($1::uuid,$3)`, uid, userDataDigest("good"), userDataDigest("used"))
	f.exec(t, `UPDATE user_recovery_codes SET used_at=now() WHERE code_digest=$1`, userDataDigest("used"))
	for name, p := range map[string]domain.UserPurgeProof{
		"no factor":      {Expected: credentials},
		"wrong recovery": {Expected: credentials, Recovery: userDataDigest("wrong")},
		"spent recovery": {Expected: credentials, Recovery: userDataDigest("used")},
		"wrong code":     {Expected: credentials, Verify: func(string, []byte, int64) (int64, error) { return 0, domain.ErrSecondFactorMismatch }},
	} {
		if err = f.s.PurgeUser(f.ctx, web, uid, &p); !errors.Is(err, domain.ErrSecondFactorMismatch) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if f.number(t, `SELECT count(*) FROM users WHERE id=$1::uuid`, uid) != 1 {
		t.Fatal("refused purge deleted the user")
	}
	good := domain.UserPurgeProof{Expected: credentials, Recovery: userDataDigest("good")}
	if err = f.s.PurgeUser(f.ctx, web, uid, &good); err != nil {
		t.Fatalf("self purge: %v", err)
	}
	if f.number(t, `SELECT count(*) FROM users WHERE id=$1::uuid`, uid) != 0 ||
		f.number(t, `SELECT count(*) FROM audit_logs WHERE event='user.purged' AND target_id=$1::uuid AND actor_id=$1::uuid AND actor_ip IS NULL AND after_state->>'self'='true'`, uid) != 1 {
		t.Fatal("self purge not complete or not audited without address")
	}
	// The session that deleted the account is gone with it.
	if _, err = f.s.CredentialsFor(f.ctx, web, uid); err == nil {
		t.Fatal("deleted session still authorizes")
	}

	// The last active administrator cannot delete their account; with a
	// second administrator they can, and their schedule moves to that one.
	adminCredentials, err := f.s.CredentialsFor(f.ctx, f.a, f.a.UserID)
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, `INSERT INTO scan_schedules(library_id,owner_id,revision,enabled,mode,interval_seconds,cron,timezone,probe,nfo,ignore_mode,ignore_case) VALUES($2::uuid,$1::uuid,1,false,'interval',3600,'','UTC',false,false,'','')`, f.a.UserID, f.registration.Library.ID)
	if err = f.s.PurgeUser(f.ctx, f.a, f.a.UserID, &domain.UserPurgeProof{Expected: adminCredentials}); !errors.Is(err, domain.ErrLastAdmin) {
		t.Fatalf("last administrator purge: %v", err)
	}
	second := createAccount(t, f.ctx, f.s, f.a, "second-admin")
	f.exec(t, `UPDATE users SET is_admin=true WHERE id=$1::uuid`, second.ID)
	if err = f.s.PurgeUser(f.ctx, f.a, f.a.UserID, &domain.UserPurgeProof{Expected: adminCredentials}); err != nil {
		t.Fatalf("administrator self purge: %v", err)
	}
	if f.number(t, `SELECT count(*) FROM scan_schedules WHERE owner_id=$1::uuid`, second.ID) != 1 {
		t.Fatal("schedule did not move to the remaining administrator")
	}
}

func TestUserDataExportSnapshotWithoutSecrets(t *testing.T) {
	f := newUserDataFixture(t)
	native := f.viewer
	web := f.webActor(t, native, "progress-viewer", true)
	uid := native.UserID
	f.seed(t, native, web)
	f.exec(t, `UPDATE users SET is_admin=false WHERE id=$1::uuid`, uid)
	other := imageRepositoryActor(t, f.jobFixture, "export-other", "native")

	export := func(actor domain.Actor, user string) (domain.UserDataExportHeader, []domain.UserDataRecord, error) {
		var header domain.UserDataExportHeader
		var records []domain.UserDataRecord
		begun := 0
		err := f.s.ExportUserData(f.ctx, actor, user, func(h domain.UserDataExportHeader) error {
			header = h
			begun++
			return nil
		}, func(r domain.UserDataRecord) error {
			if begun != 1 {
				t.Fatal("record before begin")
			}
			records = append(records, r)
			return nil
		})
		return header, records, err
	}
	if _, _, err := export(other, uid); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("export of another user: %v", err)
	}
	header, records, err := export(native, uid)
	if err != nil {
		t.Fatal(err)
	}
	if header.UserID != uid || header.UserName != "progress-viewer" || header.Format != domain.UserDataExportFormat || header.GeneratedAt.IsZero() {
		t.Fatalf("header %+v", header)
	}
	counts := map[string]int{}
	var text strings.Builder
	for _, r := range records {
		counts[r.Type]++
		var object map[string]any
		if err = json.Unmarshal(r.Data, &object); err != nil {
			t.Fatalf("%s record is not an object: %v", r.Type, err)
		}
		text.Write(r.Data)
	}
	for _, kind := range []string{"account", "preferences", "trackPreference", "blockedTag", "libraryAccess", "itemAccessRule", "itemData", "playbackSession",
		"playbackSample", "watchStatsDay", "watchStatsItem", "session", "appPassword", "shareLink", "clientControlHit", "auditEvent"} {
		if counts[kind] == 0 {
			t.Errorf("export has no %s record", kind)
		}
	}
	if counts["account"] != 1 || counts["session"] < 2 {
		t.Fatalf("counts %v", counts)
	}
	all := text.String()
	for _, secret := range []string{"$argon2", hex.EncodeToString(userDataDigest("app-" + uid)), hex.EncodeToString(userDataDigest("recovery-" + uid)),
		"token", "digest", "secret", "password_hash", strings.Repeat("s", 48)} {
		if strings.Contains(strings.ToLower(all), strings.ToLower(secret)) {
			t.Errorf("export contains %q", secret)
		}
	}
	if !strings.Contains(all, `"twoFactorEnabled": true`) || !strings.Contains(all, `"note": "for a friend"`) {
		t.Fatalf("export lacks account or share details: %s", all)
	}
	// Rows about items the user can no longer see are left out (G48).
	f.exec(t, `DELETE FROM library_acl WHERE user_id=$1::uuid`, uid)
	if _, records, err = export(native, uid); err != nil {
		t.Fatal(err)
	}
	for _, r := range records {
		if strings.Contains(string(r.Data), f.item) {
			t.Fatalf("%s record names an invisible item: %s", r.Type, r.Data)
		}
	}
	// Administrators export anyone, including a soft-deleted user; every
	// export is audited.
	if err = f.s.DeleteUser(f.ctx, f.a, uid); err != nil {
		t.Fatal(err)
	}
	if _, records, err = export(f.a, uid); err != nil || len(records) == 0 || records[0].Type != "account" {
		t.Fatalf("administrator export of a deleted user: %v", err)
	}
	if f.number(t, `SELECT count(*) FROM audit_logs WHERE event='user.data_exported' AND target_id=$1::uuid AND category='security'`, uid) != 3 {
		t.Fatal("exports not audited")
	}
	// A failing writer stops the stream with its own error.
	stop := errors.New("client gone")
	if err = f.s.ExportUserData(f.ctx, f.a, uid, func(domain.UserDataExportHeader) error { return nil }, func(domain.UserDataRecord) error { return stop }); !errors.Is(err, stop) {
		t.Fatalf("writer error: %v", err)
	}
}

func TestUserDataRightsMigrationDownUp(t *testing.T) {
	f := newJobFixture(t)
	want := downgradeAboveMigration(t, f, "user_data_rights")
	dsn := f.s.Pool.Config().ConnString()
	version, dirty, err := Migrate(f.ctx, dsn, "down")
	if err != nil || dirty || version >= want {
		t.Fatalf("down: version=%d dirty=%t err=%v", version, dirty, err)
	}
	var gone bool
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT to_regprocedure('redact_audit_subject(text,uuid,uuid[])') IS NULL AND to_regclass('audit_logs_actor_idx') IS NULL`).Scan(&gone); err != nil || !gone {
		t.Fatalf("down left redaction objects: %v", err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE audit_logs SET actor_ip=NULL`); err == nil {
		t.Fatal("audit update accepted below the migration")
	}
	if version, dirty, err = Migrate(f.ctx, dsn, "up"); err != nil || dirty || version != SchemaVersion {
		t.Fatalf("up: version=%d dirty=%t err=%v", version, dirty, err)
	}
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT to_regprocedure('redact_audit_subject(text,uuid,uuid[])') IS NOT NULL AND to_regclass('audit_logs_actor_idx') IS NOT NULL`).Scan(&gone); err != nil || !gone {
		t.Fatalf("up did not restore redaction objects: %v", err)
	}
}
