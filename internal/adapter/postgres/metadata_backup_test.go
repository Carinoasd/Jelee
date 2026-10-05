package postgres

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// Fixed identities of the seeded backup fixture.
const (
	bkLibMovies = "b0000000-0000-4000-8000-000000000001"
	bkLibShows  = "b0000000-0000-4000-8000-000000000002"
	bkRootMov   = "b0000000-0000-4000-8000-000000000011"
	bkRootShow  = "b0000000-0000-4000-8000-000000000012"
	bkAdmin     = "b0000000-0000-4000-8000-000000000021"
	bkKid       = "b0000000-0000-4000-8000-000000000022"
	bkGone      = "b0000000-0000-4000-8000-000000000023"
	bkMovie     = "b0000000-0000-4000-8000-000000000031"
	bkSeries    = "b0000000-0000-4000-8000-000000000032"
	bkSeason    = "b0000000-0000-4000-8000-000000000033"
	bkEpisode   = "b0000000-0000-4000-8000-000000000034"
	bkSrcMovie  = "b0000000-0000-4000-8000-000000000041"
	bkSrcEp     = "b0000000-0000-4000-8000-000000000042"
	bkDirSeries = "b0000000-0000-4000-8000-000000000051"
	bkDirSeason = "b0000000-0000-4000-8000-000000000052"
	bkImage     = "b0000000-0000-4000-8000-000000000061"
	bkRule      = "b0000000-0000-4000-8000-000000000071"
	bkRule2     = "b0000000-0000-4000-8000-000000000072"
	bkRule3     = "b0000000-0000-4000-8000-000000000073"
	bkNetRule   = "b0000000-0000-4000-8000-000000000074"
	bkShare     = "b0000000-0000-4000-8000-000000000075"
	bkWebhook   = "b0000000-0000-4000-8000-000000000081"
)

func bkExec(t testing.TB, ctx context.Context, s *Store, label, sql string, args ...any) {
	t.Helper()
	if _, err := s.Pool.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("seed %s: %v", label, err)
	}
}

func bkCount(t testing.TB, ctx context.Context, s *Store, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := s.Pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// seedMetadataBackup writes one row of every exported kind, rows that must
// not be exported, and extra episodes with progress and manual metadata.
func seedMetadataBackup(t testing.TB, ctx context.Context, s *Store, episodes int) {
	t.Helper()
	sha := strings.Repeat("ab", 32)
	nfoOrigin := fmt.Sprintf(`{"sourceId":%q,"rootId":%q,"generation":1,"sha256":%q,"identityDigest":%q,"projection":"five-field-projection-v1","readAt":"2026-09-01T10:00:00Z","locked":false}`, bkSrcMovie, bkRootMov, sha, sha)
	lockOrigin := fmt.Sprintf(`{"sourceId":%q,"rootId":%q,"generation":2,"stamp":{"size":120,"modifiedUnixNano":1700000000000000000,"sha256":%q,"fingerprintVersion":"sha256-full-v1"},"identityDigest":%q,"projection":"episode-details-v1","readAt":"2026-09-02T10:00:00Z","locked":true}`, bkSrcEp, bkRootShow, sha, sha)
	steps := []struct{ label, sql string }{
		{"libraries", `INSERT INTO libraries(id,name,nfo_mode,metadata_language,metadata_image_languages,catalog_sync_auto) VALUES
 ('` + bkLibMovies + `','Movies','read-only','ja-JP',ARRAY['ja','en'],true),('` + bkLibShows + `','Shows','off','zh-CN',ARRAY['zh','ja','en','null'],false)`},
		{"roots", `INSERT INTO library_roots(id,library_id,path) VALUES ('` + bkRootMov + `','` + bkLibMovies + `','/media/movies'),('` + bkRootShow + `','` + bkLibShows + `','/media/shows')`},
		{"users", `INSERT INTO users(id,name,is_admin,password_hash,display_name,locale,created_at) VALUES ('` + bkAdmin + `','admin',true,'` + accountTestHash + `','Admin','en-US','2026-01-02T03:04:05.123456Z')`},
		{"kid", `INSERT INTO users(id,name,hidden,display_name,locale,allow_native,max_streams,max_kbps,parental_rating_max,block_unrated,content_filtered,created_at,password_hash)
 VALUES ('` + bkKid + `','Kid',true,'小明','zh-TW',true,2,8000,12,true,true,'2026-02-03T04:05:06Z','` + changedAccountTestHash + `')`},
		{"gone", `INSERT INTO users(id,name,disabled,deleted_at,created_at) VALUES ('` + bkGone + `','gone',true,'2026-03-01T00:00:00Z','2026-01-01T00:00:00Z')`},
		{"sessions", `INSERT INTO sessions(user_id,token_hash,client_kind,expires_at) VALUES ('` + bkAdmin + `',sha256('fixture-token'::bytea),'web',now()+interval '1 day')`},
		{"acl", `INSERT INTO library_acl(user_id,library_id) VALUES ('` + bkKid + `','` + bkLibShows + `')`},
		{"items", `INSERT INTO items(id,library_id,title,kind) VALUES ('` + bkMovie + `','` + bkLibMovies + `','Film','Movie'),('` + bkSeries + `','` + bkLibShows + `','Show','Series'),
 ('` + bkSeason + `','` + bkLibShows + `','Season 1','Season'),('` + bkEpisode + `','` + bkLibShows + `','Pilot','Episode')`},
		{"sources", `INSERT INTO media_sources(id,item_id,library_id,root_id,relative_path,content_type) VALUES ('` + bkSrcMovie + `','` + bkMovie + `','` + bkLibMovies + `','` + bkRootMov + `','Film (2020)/Film.mkv','video/x-matroska'),
 ('` + bkSrcEp + `','` + bkEpisode + `','` + bkLibShows + `','` + bkRootShow + `','Show/Season 1/S01E01.mp4','video/mp4')`},
		{"directories", `INSERT INTO item_directory_sources(id,item_id,library_id,kind,root_id,relative_path) VALUES ('` + bkDirSeries + `','` + bkSeries + `','` + bkLibShows + `','Series','` + bkRootShow + `','Show'),
 ('` + bkDirSeason + `','` + bkSeason + `','` + bkLibShows + `','Season','` + bkRootShow + `','Show/Season 1')`},
		{"parents", `INSERT INTO item_parent_links(item_id,library_id,item_kind,parent_id,parent_kind) VALUES ('` + bkSeason + `','` + bkLibShows + `','Season','` + bkSeries + `','Series'),
 ('` + bkEpisode + `','` + bkLibShows + `','Episode','` + bkSeason + `','Season')`},
		{"scan items", `INSERT INTO catalog_scan_items(item_id,library_id,kind,group_digest,parser_version,scan_title,year) VALUES ('` + bkMovie + `','` + bkLibMovies + `','Movie',decode(repeat('11',32),'hex'),'v1','Film',2020)`},
		{"scan sources", `INSERT INTO catalog_scan_sources(source_id,library_id,item_id,root_id,relative_path,size,modified_unix_nano,parser_version) VALUES ('` + bkSrcMovie + `','` + bkLibMovies + `','` + bkMovie + `','` + bkRootMov + `','Film (2020)/Film.mkv',123456789,1700000000123456789,'v1')`},
		{"scan alias", `INSERT INTO catalog_scan_item_aliases(library_id,kind,group_digest,item_id) VALUES ('` + bkLibMovies + `','Movie',decode(repeat('22',32),'hex'),'` + bkMovie + `')`},
		{"manual scan source", `UPDATE catalog_scan_sources SET manual=true WHERE source_id='` + bkSrcMovie + `'`},
		{"version exclusion", `INSERT INTO item_version_exclusions(item_id,library_id,root_id,relative_path,created_at) VALUES ('` + bkMovie + `','` + bkLibMovies + `','` + bkRootMov + `','Film (2020)/Film.Trailer.mkv','2026-09-01T01:00:00Z')`},
		{"primary version", `INSERT INTO item_primary_versions(item_id,library_id,source_id,updated_at) VALUES ('` + bkMovie + `','` + bkLibMovies + `','` + bkSrcMovie + `','2026-09-01T02:00:00Z')`},
		{"track preferences", `INSERT INTO user_track_preferences(user_id,item_id,source_id,audio_language,audio_commentary,audio_track,subtitle_mode,subtitle_language,subtitle_sdh,subtitle_track,updated_at) VALUES
 ('` + bkKid + `',NULL,NULL,'zh-Hant',NULL,NULL,'auto','zh-Hant',false,NULL,'2026-09-03T00:00:00Z'),
 ('` + bkKid + `','` + bkEpisode + `',NULL,'ja',true,NULL,NULL,NULL,NULL,NULL,'2026-09-03T00:00:01Z'),
 ('` + bkKid + `','` + bkEpisode + `','` + bkSrcEp + `',NULL,NULL,'e:1','always',NULL,NULL,'x:b0000000-0000-4000-8000-0000000000aa','2026-09-03T00:00:02Z')`},
		{"metadata state", `INSERT INTO item_metadata_state(item_id,revision) VALUES ('` + bkMovie + `',5),('` + bkEpisode + `',3)`},
		{"metadata fields", `INSERT INTO item_metadata_fields(item_id,field,value,source,locked,updated_at) VALUES ('` + bkMovie + `','title','Film 手動','manual',true,'2026-09-01T00:00:00.5Z')`},
		{"tmdb field", `INSERT INTO item_metadata_fields(item_id,field,value,source,updated_at,provider_resource,provider_id,provider_source_url,provider_language,provider_fetched_at)
 VALUES ('` + bkMovie + `','overview','A film.','tmdb','2026-09-01T00:00:01Z','movie',603,'https://www.themoviedb.org/movie/603','en-US','2026-08-31T00:00:00Z')`},
		{"nfo field", `INSERT INTO item_metadata_fields(item_id,field,value,source,updated_at,nfo_origin) VALUES ('` + bkMovie + `','sortTitle','Film, The','nfo','2026-09-01T00:00:02Z','` + nfoOrigin + `')`},
		{"episode field", `INSERT INTO item_metadata_fields(item_id,field,value,source,updated_at) VALUES ('` + bkEpisode + `','overview','Pilot episode.','manual','2026-09-01T00:00:04Z')`},
		{"facts", `INSERT INTO item_metadata_facts(item_id,field,value,source,locked,updated_at) VALUES ('` + bkMovie + `','genres','["Drama","Sci-Fi"]','manual',true,'2026-09-01T00:00:03Z')`},
		{"nfo lock", `INSERT INTO item_nfo_field_locks(item_id,field,origin) VALUES ('` + bkEpisode + `','seasonNumber','` + lockOrigin + `')`},
		{"locked image", `INSERT INTO item_images(id,item_id,library_id,image_type,image_index,source_kind,remote_url,content_sha256,width,height,format,byte_size,average_color,fetched_at,locked,created_at,updated_at)
 VALUES ('` + bkImage + `','` + bkMovie + `','` + bkLibMovies + `','Primary',0,'remote','https://image.example/poster.jpg',decode(repeat('cd',32),'hex'),1000,1500,'jpeg',2048,123456,'2026-09-01T00:00:00Z',true,'2026-09-01T00:00:00Z','2026-09-02T00:00:00Z')`},
		{"unlocked image", `INSERT INTO item_images(item_id,library_id,image_type,image_index,source_kind,root_id,relative_path) VALUES ('` + bkMovie + `','` + bkLibMovies + `','Backdrop',1,'local','` + bkRootMov + `','Film (2020)/fanart.jpg')`},
		{"progress", `INSERT INTO user_item_data(user_id,item_id,resume_ticks,played,play_count,last_played_at,last_source_id,updated_at) VALUES
 ('` + bkKid + `','` + bkEpisode + `',123450000,false,1,'2026-09-03T20:00:00Z','` + bkSrcEp + `','2026-09-03T20:00:00Z'),('` + bkAdmin + `','` + bkMovie + `',0,true,3,'2026-09-04T20:00:00Z',NULL,'2026-09-04T20:00:00Z')`},
		{"access policy", `UPDATE access_policy SET restrict_admins=true`},
		{"rating", `INSERT INTO parental_ratings(code,level) VALUES ('KR-15',15)`},
		{"item rule", `INSERT INTO user_item_access_rules(user_id,item_id,effect,created_at) VALUES ('` + bkKid + `','` + bkMovie + `','hide','2026-09-05T00:00:00Z')`},
		{"blocked tag", `INSERT INTO user_blocked_tags(user_id,tag) VALUES ('` + bkKid + `','horror')`},
		{"client policy", `UPDATE client_control_policy SET unknown_clients='read_only',exempt_loopback=false`},
		{"client rule", `INSERT INTO client_rules(id,dimension,match_kind,pattern,priority,action,note,created_by,created_at,updated_at,hit_count)
 VALUES ('` + bkRule + `','user_agent','exact','BadBot/1.0',10,'deny','scraper','` + bkAdmin + `','2026-09-06T00:00:00Z','2026-09-06T00:00:00Z',42)`},
		{"client rule 2", `INSERT INTO client_rules(id,dimension,header_name,match_kind,pattern,case_fold,action,intent,rate_requests,rate_period_seconds,scope_kind,scope_values,daily_start,daily_end,weekdays,time_zone,created_at,updated_at)
 VALUES ('` + bkRule2 + `','header','X-Client','prefix','legacy',true,'observe','rate_limit',30,60,'user',ARRAY['` + bkKid + `'],'08:00','20:00',ARRAY[1,2,5]::smallint[],'Asia/Taipei','2026-09-06T00:00:00Z','2026-09-07T00:00:00Z')`},
		{"restrict libraries rule", `INSERT INTO client_rules(id,dimension,match_kind,pattern,action,libraries,created_at,updated_at)
 VALUES ('` + bkRule3 + `','ip','cidr','0.0.0.0/0','restrict_libraries',ARRAY['` + bkLibMovies + `']::uuid[],'2026-09-06T00:00:00Z','2026-09-06T00:00:00Z')`},
		{"network rule", `INSERT INTO library_network_rules(id,library_id,network,cidrs,client_kinds,include_admins,note,created_by,created_at,updated_at)
 VALUES ('` + bkNetRule + `','` + bkLibMovies + `','lan',ARRAY['10.0.0.0/8','2001:db8::/32']::cidr[],ARRAY['native'],true,'home only','` + bkAdmin + `','2026-09-06T00:00:00Z','2026-09-07T00:00:00Z')`},
		// A share, its guest account and the guest's progress never leave
		// the instance (G48.6).
		{"share", `INSERT INTO share_links(id,token_hash,library_id,expires_at,created_by) VALUES ('` + bkShare + `',decode(repeat('ee',32),'hex'),'` + bkLibMovies + `',now()+interval '1 day','` + bkAdmin + `')`},
		{"guest", `INSERT INTO users(name,hidden,share_id) VALUES ('share:` + bkShare + `',true,'` + bkShare + `')`},
		{"guest progress", `INSERT INTO user_item_data(user_id,item_id,resume_ticks,played,play_count,updated_at) SELECT id,'` + bkMovie + `',5,false,0,now() FROM users WHERE share_id='` + bkShare + `'`},
		{"webhook", `INSERT INTO webhooks(id,name,url,events,header_names,headers_sealed,timeout_ms,max_attempts,base_delay_ms,max_delay_ms,jitter,secret_sealed,previous_secret_sealed,previous_until,created_at,updated_at)
 VALUES ('` + bkWebhook + `','notify','https://hooks.example/jelee',ARRAY['media.added','playback.started'],ARRAY['X-Token'],decode(repeat('a1',40),'hex'),5000,5,2000,60000,0.25,
 decode(repeat('b2',48),'hex'),decode(repeat('c3',48),'hex'),'2026-10-10T00:00:00Z','2026-09-08T00:00:00Z','2026-09-09T00:00:00Z')`},
		{"schedule", `INSERT INTO scan_schedules(library_id,owner_id,revision,enabled,mode,interval_seconds,cron,timezone,probe,nfo,ignore_mode,ignore_case,next_due,watch_enabled)
 VALUES ('` + bkLibMovies + `','` + bkAdmin + `',4,true,'interval',3600,'','Asia/Tokyo',true,false,'jeleeignore','sensitive','2026-10-05T00:00:00Z',true)`},
		{"watch", `INSERT INTO scan_watch_state(library_id) VALUES ('` + bkLibMovies + `')`},
	}
	for _, step := range steps {
		bkExec(t, ctx, s, step.label, step.sql)
	}
	if episodes > 0 {
		seedMetadataBackupEpisodes(t, ctx, s, episodes)
	}
}

// seedMetadataBackupEpisodes adds n episodes under the season, each with a
// file, scan state, progress and a manual title.
func seedMetadataBackupEpisodes(t testing.TB, ctx context.Context, s *Store, n int) {
	t.Helper()
	bkExec(t, ctx, s, "episodes", `INSERT INTO items(id,library_id,title,kind) SELECT md5('ep'||g)::uuid,$1,'Episode '||g,'Episode' FROM generate_series(1,$2) g`, bkLibShows, n)
	bkExec(t, ctx, s, "episode sources", `INSERT INTO media_sources(id,item_id,library_id,root_id,relative_path,content_type) SELECT md5('src'||g)::uuid,md5('ep'||g)::uuid,$1,$2,'Show/Season 1/E'||g||'.mkv','video/x-matroska' FROM generate_series(1,$3) g`, bkLibShows, bkRootShow, n)
	bkExec(t, ctx, s, "episode parents", `INSERT INTO item_parent_links(item_id,library_id,item_kind,parent_id,parent_kind) SELECT md5('ep'||g)::uuid,$1,'Episode',$2,'Season' FROM generate_series(1,$3) g`, bkLibShows, bkSeason, n)
	bkExec(t, ctx, s, "episode scan items", `INSERT INTO catalog_scan_items(item_id,library_id,kind,group_digest,parser_version,scan_title,season,episode,episode_end) SELECT md5('ep'||g)::uuid,$1,'Episode',sha256(('grp'||g)::bytea),'v1','Episode '||g,1,g,g FROM generate_series(1,$2) g`, bkLibShows, n)
	bkExec(t, ctx, s, "episode scan sources", `INSERT INTO catalog_scan_sources(source_id,library_id,item_id,root_id,relative_path,size,modified_unix_nano,parser_version) SELECT md5('src'||g)::uuid,$1,md5('ep'||g)::uuid,$2,'Show/Season 1/E'||g||'.mkv',1000+g,1700000000000000000+g,'v1' FROM generate_series(1,$3) g`, bkLibShows, bkRootShow, n)
	bkExec(t, ctx, s, "episode state", `INSERT INTO item_metadata_state(item_id,revision) SELECT md5('ep'||g)::uuid,2 FROM generate_series(1,$1) g`, n)
	bkExec(t, ctx, s, "episode titles", `INSERT INTO item_metadata_fields(item_id,field,value,source,locked,updated_at) SELECT md5('ep'||g)::uuid,'title','第 '||g||' 集','manual',g%2=0,'2026-09-01T00:00:00Z'::timestamptz+g*interval '1 second' FROM generate_series(1,$1) g`, n)
	bkExec(t, ctx, s, "episode progress", `INSERT INTO user_item_data(user_id,item_id,resume_ticks,played,play_count,last_played_at,last_source_id,updated_at) SELECT $1,md5('ep'||g)::uuid,g*10000,g%3=0,g%3,'2026-09-02T00:00:00Z'::timestamptz+g*interval '1 second',md5('src'||g)::uuid,'2026-09-02T00:00:00Z'::timestamptz+g*interval '1 second' FROM generate_series(1,$2) g`, bkKid, n)
}

func exportMetadataDoc(t testing.TB, ctx context.Context, s *Store, hashes bool) ([]byte, domain.MetadataExportSummary) {
	t.Helper()
	var buf bytes.Buffer
	summary, err := s.ExportMetadata(ctx, &buf, domain.MetadataExportOptions{IncludePasswordHashes: hashes})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	return buf.Bytes(), summary
}

// metadataRecords returns the record lines: no header, no trailer.
func metadataRecords(t testing.TB, doc []byte) []string {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(string(doc), "\n"), "\n")
	if len(lines) < 2 {
		t.Fatal("document without header and trailer")
	}
	return lines[1 : len(lines)-1]
}

func metadataRecordsByKind(t testing.TB, doc []byte) map[string][]string {
	out := map[string][]string{}
	for _, line := range metadataRecords(t, doc) {
		kind := strings.TrimPrefix(line, `{"kind":"`)
		kind = kind[:strings.IndexByte(kind, '"')]
		out[kind] = append(out[kind], line)
	}
	return out
}

func importMetadataDoc(ctx context.Context, s *Store, doc []byte, opts domain.MetadataImportOptions) (domain.MetadataImportReport, error) {
	return s.ImportMetadata(ctx, bytes.NewReader(doc), opts)
}

func metadataReportTotals(r domain.MetadataImportReport) (inserted, updated, unchanged, skipped int64) {
	for _, k := range r.Kinds {
		inserted += k.Inserted
		updated += k.Updated
		unchanged += k.Unchanged
		for _, n := range k.Skipped {
			skipped += n
		}
	}
	return
}

func checkMetadataReportBalanced(t testing.TB, r domain.MetadataImportReport) {
	t.Helper()
	for kind, k := range r.Kinds {
		sum := k.Inserted + k.Updated + k.Unchanged
		for _, n := range k.Skipped {
			sum += n
		}
		if sum != k.Records || k.Unchanged < 0 {
			t.Fatalf("report for %s does not add up: %+v", kind, *k)
		}
	}
}

// G36.4 drill: create data, export, import into a new schema, export again
// and compare every record; then import twice more to prove idempotency.
// JELEE_BACKUP_DRILL_REPORT names a file for a sanitized run record.
func TestMetadataBackupDrillPostgres(t *testing.T) {
	ctx, source, _ := accountTestStoreWithTimeout(t, 5*time.Minute)
	_, target, _ := accountTestStoreWithTimeout(t, 5*time.Minute)
	const episodes = 2000
	seedMetadataBackup(t, ctx, source, episodes)
	var log strings.Builder
	logf := func(format string, args ...any) {
		fmt.Fprintf(&log, format+"\n", args...)
		t.Logf(format, args...)
	}
	logf("Jelee metadata backup drill (G36.4)")
	logf("date: %s", time.Now().UTC().Format(time.RFC3339))
	logf("go: %s %s/%s; schema: %d; format: %s v%d", runtime.Version(), runtime.GOOS, runtime.GOARCH, SchemaVersion, domain.MetadataBackupFormat, domain.MetadataBackupFormatVersion)
	logf("database: dedicated jelee_test database, two freshly migrated owned schemas (connection details omitted)")
	logf("seed: fixture rows of every record kind + %d generated episodes with files, scan state, progress and manual titles", episodes)

	started := time.Now()
	doc, summary := exportMetadataDoc(t, ctx, source, true)
	logf("\n[1] export (password hashes included): %d records, %d bytes, %s, sha256 %s", summary.Records, len(doc), time.Since(started).Round(time.Millisecond), summary.SHA256)
	if strings.Contains(string(doc), "token_hash") {
		t.Fatal("sessions leaked into the export")
	}
	if strings.Contains(string(doc), `"hit_count"`) || strings.Contains(string(doc), "Film (2020)/fanart.jpg") {
		t.Fatal("observations or unlocked images leaked into the export")
	}
	if n := bkCount(t, ctx, source, `SELECT count(*) FROM audit_logs WHERE event='metadata.exported' AND target_ref=$1`, "sha256:"+summary.SHA256); n != 1 {
		t.Fatalf("export audit rows = %d", n)
	}
	kinds := make([]string, 0, len(summary.Counts))
	for kind := range summary.Counts {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	for _, kind := range kinds {
		logf("    %-24s %6d", kind, summary.Counts[kind])
	}
	for _, kind := range domain.MetadataBackupKinds {
		if summary.Counts[kind] == 0 {
			t.Fatalf("fixture does not cover kind %s", kind)
		}
	}
	if bytes.Contains(doc, []byte(bkShare)) || bytes.Contains(doc, []byte("share:")) {
		t.Fatal("export contains a share link or its guest account")
	}

	// Dry run first: a full import that leaves nothing behind.
	dry, err := importMetadataDoc(ctx, target, doc, domain.MetadataImportOptions{DryRun: true})
	if err != nil || dry.Committed {
		t.Fatalf("dry run: committed=%t err=%v", dry.Committed, err)
	}
	if n := bkCount(t, ctx, target, `SELECT (SELECT count(*) FROM users)+(SELECT count(*) FROM items)+(SELECT count(*) FROM audit_logs WHERE event='metadata.imported')`); n != 0 {
		t.Fatalf("dry run left %d rows", n)
	}
	ins, _, _, _ := metadataReportTotals(dry)
	logf("\n[2] dry run into the empty schema: would insert %d rows; rolled back, target still empty", ins)

	started = time.Now()
	report, err := importMetadataDoc(ctx, target, doc, domain.MetadataImportOptions{})
	if err != nil || !report.Committed {
		t.Fatalf("import: committed=%t err=%v conflicts=%v", report.Committed, err, report.ConflictTotals)
	}
	checkMetadataReportBalanced(t, report)
	ins, upd, same, skipped := metadataReportTotals(report)
	logf("\n[3] import into the empty schema: inserted %d, updated %d, unchanged %d, skipped %d, %s", ins, upd, same, skipped, time.Since(started).Round(time.Millisecond))
	// Only the two single-row policies exist before the import.
	if skipped != 0 || upd != report.Kinds["access_policy"].Updated+report.Kinds["client_control_policy"].Updated {
		t.Fatalf("fresh import skipped %d updated %d", skipped, upd)
	}

	again, _ := exportMetadataDoc(t, ctx, target, true)
	want, got := metadataRecordsByKind(t, doc), metadataRecordsByKind(t, again)
	logf("\n[4] re-export of the restored schema compared record by record:")
	for _, kind := range domain.MetadataBackupKinds {
		equal := len(want[kind]) == len(got[kind])
		for i := 0; equal && i < len(want[kind]); i++ {
			equal = want[kind][i] == got[kind][i]
		}
		status := "identical"
		if !equal {
			status = "DIFFERENT"
		}
		logf("    %-24s %6d  %s", kind, len(want[kind]), status)
		if !equal {
			t.Errorf("kind %s differs after restore", kind)
		}
	}

	// Idempotency: the same file again changes nothing.
	second, err := importMetadataDoc(ctx, target, doc, domain.MetadataImportOptions{})
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	checkMetadataReportBalanced(t, second)
	ins, upd, same, skipped = metadataReportTotals(second)
	logf("\n[5] second import of the same file: inserted %d, updated %d, unchanged %d, skipped %d", ins, upd, same, skipped)
	if ins != 0 || upd != 0 || skipped != 0 {
		t.Fatalf("second import was not a no-op: inserted %d updated %d skipped %d", ins, upd, skipped)
	}
	third, _ := exportMetadataDoc(t, ctx, target, true)
	if strings.Join(metadataRecords(t, third), "\n") != strings.Join(metadataRecords(t, again), "\n") {
		t.Fatal("second import changed the exported state")
	}

	// Restored server state checks.
	checks := []struct {
		label string
		sql   string
		want  int64
	}{
		{"sessions not restored", `SELECT count(*) FROM sessions`, 0},
		{"setup marked complete (adopted)", `SELECT count(*) FROM setup_state WHERE adopted AND completed_at IS NOT NULL`, 1},
		{"admin password hash restored", `SELECT count(*) FROM users WHERE name='admin' AND password_hash IS NOT NULL`, 1},
		{"watch state for watched schedule", `SELECT count(*) FROM scan_watch_state`, 1},
		{"client rule hit counters restart", `SELECT count(*) FROM client_rules WHERE hit_count=0`, 3},
		{"metadata.imported audit rows", `SELECT count(*) FROM audit_logs WHERE event='metadata.imported' AND target_ref=$1`, 2},
	}
	logf("\n[6] restored server checks:")
	for _, c := range checks {
		var args []any
		if strings.Contains(c.sql, "$1") {
			args = append(args, "sha256:"+summary.SHA256)
		}
		got := bkCount(t, ctx, target, c.sql, args...)
		logf("    %-36s %d (want %d)", c.label, got, c.want)
		if got != c.want {
			t.Errorf("%s: got %d want %d", c.label, got, c.want)
		}
	}
	if t.Failed() {
		logf("\nRESULT: FAIL")
	} else {
		logf("\nRESULT: PASS")
	}
	if path := os.Getenv("JELEE_BACKUP_DRILL_REPORT"); path != "" {
		if err := os.WriteFile(path, []byte(log.String()), 0o644); err != nil {
			t.Fatalf("write drill report: %v", err)
		}
	}
}

// Without the opt-in no password hash leaves the database, and accounts
// restored from such a file have no password until reset.
func TestMetadataBackupPasswordHashesOptInPostgres(t *testing.T) {
	ctx, source, _ := accountTestStoreWithTimeout(t, 2*time.Minute)
	_, target, _ := accountTestStoreWithTimeout(t, 2*time.Minute)
	seedMetadataBackup(t, ctx, source, 0)
	doc, summary := exportMetadataDoc(t, ctx, source, false)
	if summary.PasswordHashes || strings.Contains(string(doc), "password_hash") || strings.Contains(string(doc), "$argon2id$") {
		t.Fatal("password hashes exported without opt-in")
	}
	if _, err := importMetadataDoc(ctx, target, doc, domain.MetadataImportOptions{}); err != nil {
		t.Fatalf("import: %v", err)
	}
	if n := bkCount(t, ctx, target, `SELECT count(*) FROM users WHERE password_hash IS NOT NULL`); n != 0 {
		t.Fatalf("%d accounts got a password", n)
	}
	// A file with hashes fills in the missing passwords but never replaces one.
	withHashes, _ := exportMetadataDoc(t, ctx, source, true)
	bkExec(t, ctx, target, "local password", `UPDATE users SET password_hash=$2 WHERE id=$1`, bkKid, accountTestHash)
	if _, err := importMetadataDoc(ctx, target, withHashes, domain.MetadataImportOptions{}); err != nil {
		t.Fatalf("import with hashes: %v", err)
	}
	if n := bkCount(t, ctx, target, `SELECT count(*) FROM users WHERE id=$1 AND password_hash=$2`, bkAdmin, accountTestHash); n != 1 {
		t.Fatal("missing password not filled in")
	}
	if n := bkCount(t, ctx, target, `SELECT count(*) FROM users WHERE id=$1 AND password_hash=$2`, bkKid, accountTestHash); n != 1 {
		t.Fatal("existing password replaced by import")
	}
}

// A target that already scanned the same media maps items by file and
// folder; user data lands on the target's IDs.
func TestMetadataBackupMapsExistingCatalogPostgres(t *testing.T) {
	ctx, source, _ := accountTestStoreWithTimeout(t, 2*time.Minute)
	_, target, _ := accountTestStoreWithTimeout(t, 2*time.Minute)
	seedMetadataBackup(t, ctx, source, 10)
	doc, _ := exportMetadataDoc(t, ctx, source, false)
	// The target scanned the same roots under the same library names first.
	const lib, root, item, src, user = "c0000000-0000-4000-8000-000000000001", "c0000000-0000-4000-8000-000000000011", "c0000000-0000-4000-8000-000000000031", "c0000000-0000-4000-8000-000000000041", "c0000000-0000-4000-8000-000000000021"
	bkExec(t, ctx, target, "library", `INSERT INTO libraries(id,name) VALUES ($1,'Shows')`, lib)
	bkExec(t, ctx, target, "root", `INSERT INTO library_roots(id,library_id,path) VALUES ($1,$2,'/media/shows')`, root, lib)
	bkExec(t, ctx, target, "item", `INSERT INTO items(id,library_id,title,kind) VALUES ($1,$2,'s01e01','Episode')`, item, lib)
	bkExec(t, ctx, target, "source", `INSERT INTO media_sources(id,item_id,library_id,root_id,relative_path,content_type) VALUES ($1,$2,$3,$4,'Show/Season 1/S01E01.mp4','video/mp4')`, src, item, lib, root)
	bkExec(t, ctx, target, "user", `INSERT INTO users(id,name,is_admin) VALUES ($1,'ADMIN',true)`, user)
	report, err := importMetadataDoc(ctx, target, doc, domain.MetadataImportOptions{})
	if err != nil {
		t.Fatalf("import: %v %v", err, report.ConflictTotals)
	}
	checkMetadataReportBalanced(t, report)
	if n := bkCount(t, ctx, target, `SELECT count(*) FROM user_item_data WHERE user_id=$1 AND item_id=$2 AND last_source_id=$3 AND resume_ticks=123450000`, bkKid, item, src); n != 1 {
		t.Fatal("progress not mapped onto the scanned item and file")
	}
	if n := bkCount(t, ctx, target, `SELECT count(*) FROM item_nfo_field_locks`); n != 0 {
		t.Fatal("NFO lock with foreign provenance imported onto a remapped item")
	}
	if k := report.Kinds["item_nfo_field_lock"]; k == nil || k.Skipped["nfo_origin_not_portable"] != 1 {
		t.Fatalf("NFO lock skip not reported: %+v", report.Kinds["item_nfo_field_lock"])
	}
	if n := bkCount(t, ctx, target, `SELECT count(*) FROM users WHERE lower(name)='admin'`); n != 1 {
		t.Fatal("user matched by name was duplicated")
	}
	if n := bkCount(t, ctx, target, `SELECT count(*) FROM scan_schedules WHERE owner_id=$1`, user); n != 1 {
		t.Fatal("schedule owner not mapped onto the existing account")
	}
	if n := bkCount(t, ctx, target, `SELECT count(*) FROM items WHERE library_id=$1`, lib); n != 1+3+10-1 {
		t.Fatalf("shows library holds %d items", n)
	}
	if n := bkCount(t, ctx, target, `SELECT revision FROM item_metadata_state WHERE item_id=$1`, item); n < 3 {
		t.Fatalf("remapped item revision not bumped: %d", n)
	}
	// Idempotent on a remapped target too.
	second, err := importMetadataDoc(ctx, target, doc, domain.MetadataImportOptions{})
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if ins, upd, _, _ := metadataReportTotals(second); ins != 0 || upd != 0 {
		t.Fatalf("second import changed %d/%d rows", ins, upd)
	}
}

// Conflicts are found before anything is written; SkipConflicts applies
// the rest and reports each skipped record.
func TestMetadataBackupConflictPreflightPostgres(t *testing.T) {
	ctx, source, _ := accountTestStoreWithTimeout(t, 2*time.Minute)
	_, target, _ := accountTestStoreWithTimeout(t, 2*time.Minute)
	seedMetadataBackup(t, ctx, source, 5)
	doc, _ := exportMetadataDoc(t, ctx, source, false)
	// The movie root path belongs to another library in the target, and
	// the exported admin ID is taken by an account named differently while
	// another account holds the name "admin".
	bkExec(t, ctx, target, "other library", `INSERT INTO libraries(id,name) VALUES ('d0000000-0000-4000-8000-000000000001','Elsewhere')`)
	bkExec(t, ctx, target, "root elsewhere", `INSERT INTO library_roots(library_id,path) VALUES ('d0000000-0000-4000-8000-000000000001','/media/movies')`)
	bkExec(t, ctx, target, "renamed admin", `INSERT INTO users(id,name,is_admin) VALUES ($1,'root',true)`, bkAdmin)
	bkExec(t, ctx, target, "name holder", `INSERT INTO users(name) VALUES ('Admin')`)
	snapshot := func() int64 {
		return bkCount(t, ctx, target, `SELECT (SELECT count(*) FROM users)*1000000+(SELECT count(*) FROM libraries)*10000+(SELECT count(*) FROM items)*10+(SELECT count(*) FROM audit_logs WHERE event='metadata.imported')`)
	}
	before := snapshot()
	report, err := importMetadataDoc(ctx, target, doc, domain.MetadataImportOptions{})
	if !errors.Is(err, domain.ErrMetadataBackupConflict) || report.Committed {
		t.Fatalf("conflicting import: err=%v committed=%t", err, report.Committed)
	}
	for _, key := range []string{"library_root:root_library_mismatch", "user:user_name_taken", "item:dependency_conflict"} {
		if report.ConflictTotals[key] == 0 {
			t.Fatalf("conflict %s not reported: %v", key, report.ConflictTotals)
		}
	}
	if len(report.Conflicts) == 0 || report.Conflicts[0].ID == "" {
		t.Fatal("conflict sample missing")
	}
	if snapshot() != before {
		t.Fatal("conflicting import wrote rows")
	}
	report, err = importMetadataDoc(ctx, target, doc, domain.MetadataImportOptions{SkipConflicts: true})
	if err != nil || !report.Committed {
		t.Fatalf("skip-conflicts import: %v", err)
	}
	checkMetadataReportBalanced(t, report)
	if k := report.Kinds["item"]; k.Skipped["dependency_conflict"] != 1 || k.Inserted != 3+5 {
		t.Fatalf("item report: %+v", *k)
	}
	if k := report.Kinds["user_item_data"]; k.Skipped["unresolved_reference"] != 1 {
		t.Fatalf("progress of the skipped admin/movie not reported: %+v", *k)
	}
	if n := bkCount(t, ctx, target, `SELECT count(*) FROM items WHERE id=$1`, bkMovie); n != 0 {
		t.Fatal("item under the conflicting root was imported")
	}
	if n := bkCount(t, ctx, target, `SELECT count(*) FROM users WHERE id=$1 AND name='root'`, bkAdmin); n != 1 {
		t.Fatal("conflicting account was changed")
	}
}

// Damaged files are refused before any row is applied.
func TestMetadataBackupRejectsDamagedFilesPostgres(t *testing.T) {
	ctx, source, _ := accountTestStoreWithTimeout(t, 2*time.Minute)
	_, target, _ := accountTestStoreWithTimeout(t, 2*time.Minute)
	seedMetadataBackup(t, ctx, source, 20)
	doc, _ := exportMetadataDoc(t, ctx, source, false)
	flipped := bytes.Replace(doc, []byte("Film 手動"), []byte("Film 手勤"), 1)
	cut := doc[:bytes.LastIndexByte(doc[:len(doc)-1], '\n')+1]
	midLine := doc[:len(doc)/2]
	cases := []struct {
		name string
		data []byte
		want error
	}{
		{"checksum mismatch", flipped, domain.ErrMetadataBackupCorrupt},
		{"trailer removed", cut, domain.ErrMetadataBackupTruncated},
		{"cut mid line", midLine, domain.ErrMetadataBackupTruncated},
		{"data after trailer", append(append([]byte{}, doc...), []byte("{}\n")...), domain.ErrMetadataBackupCorrupt},
		{"newer schema", bytes.Replace(doc, []byte(fmt.Sprintf(`"schemaVersion":%d`, SchemaVersion)), []byte(fmt.Sprintf(`"schemaVersion":%d`, SchemaVersion+1)), 1), domain.ErrMetadataBackupUnsupported},
	}
	for _, c := range cases {
		report, err := importMetadataDoc(ctx, target, c.data, domain.MetadataImportOptions{})
		if !errors.Is(err, c.want) || report.Committed {
			t.Fatalf("%s: err=%v want %v", c.name, err, c.want)
		}
	}
	if n := bkCount(t, ctx, target, `SELECT (SELECT count(*) FROM users)+(SELECT count(*) FROM libraries)+(SELECT count(*) FROM audit_logs WHERE event='metadata.imported')`); n != 0 {
		t.Fatalf("damaged imports left %d rows", n)
	}
}

// heapPeak samples the live heap while fn runs and returns the peak growth
// over the heap before it started.
func heapPeak(fn func()) uint64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	base := m.HeapAlloc
	var peak atomic.Uint64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var s runtime.MemStats
		for {
			runtime.ReadMemStats(&s)
			if s.HeapAlloc > base && s.HeapAlloc-base > peak.Load() {
				peak.Store(s.HeapAlloc - base)
			}
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Millisecond):
			}
		}
	}()
	fn()
	close(stop)
	wg.Wait()
	return peak.Load()
}

type countingWriter struct{ n int64 }

func (w *countingWriter) Write(p []byte) (int, error) {
	w.n += int64(len(p))
	return len(p), nil
}

// Large catalogs stream through export and import with a bounded heap: the
// document is several times larger than the limit. The default 20,000
// items keep the run short; JELEE_BACKUP_SCALE_ITEMS=100000 (make
// backup-scale) is the G36.4 acceptance size. Insert triggers bound import
// speed at roughly 2-3 ms per new item.
func TestMetadataBackupLargeCatalogMemoryPostgres(t *testing.T) {
	items := 20000
	if v := os.Getenv("JELEE_BACKUP_SCALE_ITEMS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1000 || n > 1000000 {
			t.Fatal("JELEE_BACKUP_SCALE_ITEMS must be 1000..1000000")
		}
		items = n
	}
	ctx, source, _ := accountTestStoreWithTimeout(t, 40*time.Minute)
	_, target, _ := accountTestStoreWithTimeout(t, 40*time.Minute)
	const limit = 16 << 20
	seedMetadataBackup(t, ctx, source, items)
	var size countingWriter
	var summary domain.MetadataExportSummary
	var err error
	started := time.Now()
	exportPeak := heapPeak(func() {
		summary, err = source.ExportMetadata(ctx, &size, domain.MetadataExportOptions{})
	})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	t.Logf("export: %d records, %d bytes, heap peak +%d KiB, %s", summary.Records, size.n, exportPeak>>10, time.Since(started).Round(time.Millisecond))
	if size.n < 2*limit {
		t.Fatalf("document of %d bytes does not exceed the heap limit enough to prove streaming", size.n)
	}
	if exportPeak > limit {
		t.Fatalf("export heap peak %d bytes exceeds %d", exportPeak, limit)
	}
	file, err := os.CreateTemp(t.TempDir(), "metadata-*.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err = source.ExportMetadata(ctx, file, domain.MetadataExportOptions{}); err != nil {
		t.Fatalf("export to file: %v", err)
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	var report domain.MetadataImportReport
	started = time.Now()
	importPeak := heapPeak(func() {
		report, err = target.ImportMetadata(ctx, file, domain.MetadataImportOptions{})
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	ins, _, _, skipped := metadataReportTotals(report)
	t.Logf("import: inserted %d, skipped %d, heap peak +%d KiB, %s", ins, skipped, importPeak>>10, time.Since(started).Round(time.Millisecond))
	if importPeak > limit {
		t.Fatalf("import heap peak %d bytes exceeds %d", importPeak, limit)
	}
	if n := bkCount(t, ctx, target, `SELECT count(*) FROM items`); n != int64(items)+4 {
		t.Fatalf("restored %d items", n)
	}
}
