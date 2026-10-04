package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"path"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

// Page queries of the consistency checker (G50.3). Every query is a single
// read-only statement that reads at most one page through an index; cursors
// are opaque to the caller. docs/consistency.md describes each check.

const (
	consistencyCursorSeparator = "\x1f"
	// consistencySampleScan bounds the rows a sampling query reads per
	// sampled row, so a library with few statistics rows in a large table
	// still costs a bounded scan.
	consistencySampleScan = 20
	// consistencyNFOFallbackRows bounds a case-insensitive directory lookup.
	consistencyNFOFallbackRows = 4096
)

// ConsistencyPage reads the page of one check after cursor.
func (s *Store) ConsistencyPage(ctx context.Context, check string, scope domain.ConsistencyScope, cursor string) (domain.ConsistencyPage, error) {
	if ctx == nil || !domain.ValidConsistencyCheck(check) {
		return domain.ConsistencyPage{}, domain.ErrInvalid
	}
	global := check == domain.ConsistencyImageVariant || check == domain.ConsistencyConstraints
	if !global && !domain.ValidID(scope.Library.ID) {
		return domain.ConsistencyPage{}, domain.ErrInvalid
	}
	switch check {
	case domain.ConsistencyOrphanItem:
		return s.consistencyOrphanItems(ctx, scope, cursor)
	case domain.ConsistencyOrphanFile:
		return s.consistencyOrphanFiles(ctx, scope, cursor)
	case domain.ConsistencyVersionCount:
		return s.consistencyVersions(ctx, scope, cursor)
	case domain.ConsistencyWatchStats:
		return s.consistencyWatchStats(ctx, scope, cursor)
	case domain.ConsistencyImageFile:
		return s.consistencyImageFiles(ctx, scope, cursor)
	case domain.ConsistencyImageVariant:
		return s.consistencyImageVariants(ctx, scope)
	case domain.ConsistencyNFOState:
		return s.consistencyNFO(ctx, scope, cursor)
	case domain.ConsistencySidecarFile:
		return s.consistencySidecars(ctx, scope, cursor)
	case domain.ConsistencyProbeCache:
		return s.consistencyProbeCache(ctx, scope, cursor)
	}
	return s.consistencyConstraints(ctx)
}

// idCursor accepts an empty cursor (start) or a UUID.
func idCursor(cursor string) (string, error) {
	if cursor == "" {
		return consistencyZeroID, nil
	}
	if !domain.ValidID(cursor) {
		return "", domain.ErrInvalid
	}
	return cursor, nil
}

// pathCursor accepts an empty cursor or "<root id><US><path>".
func pathCursor(cursor string) (string, string, error) {
	if cursor == "" {
		return consistencyZeroID, "", nil
	}
	root, rest, ok := strings.Cut(cursor, consistencyCursorSeparator)
	if !ok || !domain.ValidID(root) || len(rest) > domain.ScanPathMaxBytes {
		return "", "", domain.ErrInvalid
	}
	return root, rest, nil
}

func nextCursor(n int, last string) string {
	if n < domain.ConsistencyPageSize {
		return ""
	}
	return last
}

func (s *Store) consistencyOrphanItems(ctx context.Context, scope domain.ConsistencyScope, cursor string) (domain.ConsistencyPage, error) {
	after, err := idCursor(cursor)
	if err != nil {
		return domain.ConsistencyPage{}, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT ms.id::text,ms.item_id::text,ms.root_id::text,ms.relative_path,
 EXISTS(SELECT 1 FROM library_inventory_baseline_data b WHERE b.library_id=ms.library_id AND b.snapshot_id=$2 AND b.root_id=ms.root_id AND b.path=ms.relative_path)
 FROM media_sources ms WHERE ms.library_id=$1::uuid AND ms.id>$3::uuid ORDER BY ms.id LIMIT $4`, scope.Library.ID, scope.Library.Snapshot, after, domain.ConsistencyPageSize)
	if err != nil {
		return domain.ConsistencyPage{}, storageError(err)
	}
	defer rows.Close()
	var page domain.ConsistencyPage
	last := ""
	for rows.Next() {
		var f domain.ConsistencyFinding
		var present bool
		if err = rows.Scan(&f.SourceID, &f.ItemID, &f.RootID, &f.RelativePath, &present); err != nil {
			return domain.ConsistencyPage{}, storageError(err)
		}
		page.Examined++
		last = f.SourceID
		if !present {
			f.Code, f.LibraryID, f.Confirm = domain.ConsistencySourceMissing, scope.Library.ID, domain.ConsistencyConfirmAbsent
			page.Candidates = append(page.Candidates, f)
		}
	}
	if err = rows.Err(); err != nil {
		return domain.ConsistencyPage{}, storageError(err)
	}
	page.Next = nextCursor(int(page.Examined), last)
	return page, nil
}

func (s *Store) consistencyOrphanFiles(ctx context.Context, scope domain.ConsistencyScope, cursor string) (domain.ConsistencyPage, error) {
	root, after, err := pathCursor(cursor)
	if err != nil {
		return domain.ConsistencyPage{}, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT b.root_id::text,b.path,
 EXISTS(SELECT 1 FROM media_sources ms WHERE ms.root_id=b.root_id AND ms.relative_path=b.path),
 EXISTS(SELECT 1 FROM catalog_scan_pending p WHERE p.root_id=b.root_id AND p.relative_path=b.path)
 FROM library_inventory_baseline_data b
 WHERE b.library_id=$1::uuid AND b.snapshot_id=$2 AND b.kind='video' AND (b.root_id,(b.path COLLATE "C"))>($3::uuid,($4::text COLLATE "C"))
 ORDER BY b.root_id,(b.path COLLATE "C") LIMIT $5`, scope.Library.ID, scope.Library.Snapshot, root, after, domain.ConsistencyPageSize)
	if err != nil {
		return domain.ConsistencyPage{}, storageError(err)
	}
	defer rows.Close()
	page := domain.ConsistencyPage{Info: map[string]int64{}}
	last := ""
	for rows.Next() {
		var f domain.ConsistencyFinding
		var cataloged, pending bool
		if err = rows.Scan(&f.RootID, &f.RelativePath, &cataloged, &pending); err != nil {
			return domain.ConsistencyPage{}, storageError(err)
		}
		page.Examined++
		last = f.RootID + consistencyCursorSeparator + f.RelativePath
		switch {
		case cataloged:
		case pending:
			// Files waiting for an administrator decision are known; they
			// are counted, not reported.
			page.Info[domain.ConsistencyInfoPending]++
		default:
			f.Code, f.LibraryID = domain.ConsistencyVideoNotCataloged, scope.Library.ID
			page.Candidates = append(page.Candidates, f)
		}
	}
	if err = rows.Err(); err != nil {
		return domain.ConsistencyPage{}, storageError(err)
	}
	page.Next = nextCursor(int(page.Examined), last)
	return page, nil
}

// consistencyVersions walks three phases: items, user data and sessions.
// Versions are the media sources of one logical item (G20); a video item
// needs at least one, a series or season none, and per-user data and
// sessions may only point at a version of their own item, or at a version
// an administrator split off that item (G20.3): the history of the original
// item stays with it and still names the version it played.
func (s *Store) consistencyVersions(ctx context.Context, scope domain.ConsistencyScope, cursor string) (domain.ConsistencyPage, error) {
	phase, rest, _ := strings.Cut(cursor, ":")
	switch phase {
	case "", "items":
		return s.consistencyVersionItems(ctx, scope, rest)
	case "userdata":
		return s.consistencyVersionUserData(ctx, scope, rest)
	case "sessions":
		return s.consistencyVersionSessions(ctx, scope, rest)
	}
	return domain.ConsistencyPage{}, domain.ErrInvalid
}

func (s *Store) consistencyVersionItems(ctx context.Context, scope domain.ConsistencyScope, cursor string) (domain.ConsistencyPage, error) {
	after, err := idCursor(cursor)
	if err != nil {
		return domain.ConsistencyPage{}, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT i.id::text,i.kind,(SELECT count(*) FROM media_sources ms WHERE ms.item_id=i.id)
 FROM items i WHERE i.library_id=$1::uuid AND i.id>$2::uuid ORDER BY i.id LIMIT $3`, scope.Library.ID, after, domain.ConsistencyPageSize)
	if err != nil {
		return domain.ConsistencyPage{}, storageError(err)
	}
	defer rows.Close()
	var page domain.ConsistencyPage
	last := ""
	for rows.Next() {
		var f domain.ConsistencyFinding
		var kind string
		var versions int64
		if err = rows.Scan(&f.ItemID, &kind, &versions); err != nil {
			return domain.ConsistencyPage{}, storageError(err)
		}
		page.Examined++
		last = f.ItemID
		f.LibraryID, f.Actual = scope.Library.ID, map[string]int64{"versions": versions}
		switch {
		case versions == 0 && domain.ValidVideoItemKind(kind):
			f.Code, f.Expected = domain.ConsistencyVideoWithoutSource, map[string]int64{"minVersions": 1}
			page.Candidates = append(page.Candidates, f)
		case versions > 0 && (kind == "Series" || kind == "Season"):
			f.Code, f.Expected = domain.ConsistencyContainerWithSource, map[string]int64{"versions": 0}
			page.Candidates = append(page.Candidates, f)
		}
	}
	if err = rows.Err(); err != nil {
		return domain.ConsistencyPage{}, storageError(err)
	}
	page.Next = "userdata:"
	if next := nextCursor(int(page.Examined), last); next != "" {
		page.Next = "items:" + next
	}
	return page, nil
}

func (s *Store) consistencyVersionUserData(ctx context.Context, scope domain.ConsistencyScope, cursor string) (domain.ConsistencyPage, error) {
	user, item := consistencyZeroID, consistencyZeroID
	if cursor != "" {
		var ok bool
		user, item, ok = strings.Cut(cursor, "/")
		if !ok || !domain.ValidID(user) || !domain.ValidID(item) {
			return domain.ConsistencyPage{}, domain.ErrInvalid
		}
	}
	// Paging follows the primary key of all user data; the library is a
	// filter, so a page reads at most PageSize rows of other libraries too.
	rows, err := s.Pool.Query(ctx, `SELECT d.user_id::text,d.item_id::text,d.last_source_id::text,i.library_id=$1::uuid,COALESCE(ms.item_id=d.item_id OR `+versionSplitFromSQL("d.item_id", "ms.id")+`,true)
 FROM (SELECT user_id,item_id,last_source_id FROM user_item_data WHERE (user_id,item_id)>($2::uuid,$3::uuid) AND last_source_id IS NOT NULL ORDER BY user_id,item_id LIMIT $4) d
 JOIN items i ON i.id=d.item_id LEFT JOIN media_sources ms ON ms.id=d.last_source_id ORDER BY d.user_id,d.item_id`, scope.Library.ID, user, item, domain.ConsistencyPageSize)
	if err != nil {
		return domain.ConsistencyPage{}, storageError(err)
	}
	defer rows.Close()
	var page domain.ConsistencyPage
	read, last := 0, ""
	for rows.Next() {
		var f domain.ConsistencyFinding
		var mine, own bool
		if err = rows.Scan(&f.UserID, &f.ItemID, &f.SourceID, &mine, &own); err != nil {
			return domain.ConsistencyPage{}, storageError(err)
		}
		read++
		last = f.UserID + "/" + f.ItemID
		if !mine {
			continue
		}
		page.Examined++
		if !own {
			f.Code, f.LibraryID, f.Fixable = domain.ConsistencyUserDataForeignSource, scope.Library.ID, true
			page.Candidates = append(page.Candidates, f)
		}
	}
	if err = rows.Err(); err != nil {
		return domain.ConsistencyPage{}, storageError(err)
	}
	page.Next = "sessions:"
	if next := nextCursor(read, last); next != "" {
		page.Next = "userdata:" + next
	}
	return page, nil
}

func (s *Store) consistencyVersionSessions(ctx context.Context, scope domain.ConsistencyScope, cursor string) (domain.ConsistencyPage, error) {
	after, err := idCursor(cursor)
	if err != nil {
		return domain.ConsistencyPage{}, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT p.id::text,p.user_id::text,p.item_id::text,p.source_id::text,COALESCE(ms.item_id=p.item_id OR `+versionSplitFromSQL("p.item_id", "ms.id")+`,true)
 FROM playback_sessions p LEFT JOIN media_sources ms ON ms.id=p.source_id
 WHERE p.library_id=$1::uuid AND p.source_id IS NOT NULL AND p.id>$2::uuid ORDER BY p.id LIMIT $3`, scope.Library.ID, after, domain.ConsistencyPageSize)
	if err != nil {
		return domain.ConsistencyPage{}, storageError(err)
	}
	defer rows.Close()
	var page domain.ConsistencyPage
	last := ""
	for rows.Next() {
		var f domain.ConsistencyFinding
		var own bool
		if err = rows.Scan(&f.Object, &f.UserID, &f.ItemID, &f.SourceID, &own); err != nil {
			return domain.ConsistencyPage{}, storageError(err)
		}
		page.Examined++
		last = f.Object
		if !own {
			f.Code, f.LibraryID, f.Fixable = domain.ConsistencySessionForeignSource, scope.Library.ID, true
			page.Candidates = append(page.Candidates, f)
		}
	}
	if err = rows.Err(); err != nil {
		return domain.ConsistencyPage{}, storageError(err)
	}
	if next := nextCursor(int(page.Examined), last); next != "" {
		page.Next = "sessions:" + next
	}
	return page, nil
}

// versionSplitFromSQL is true when an active split moved source out of
// item; it reads the operation log through its item index.
func versionSplitFromSQL(item, source string) string {
	return `EXISTS(SELECT 1 FROM item_version_operations vo WHERE vo.item_id=` + item + ` AND vo.kind='split' AND vo.undone_at IS NULL AND ` + source + `=ANY(vo.source_ids))`
}

// randomID returns a random UUID-shaped key to start a sample at.
func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", domain.ErrDatabase
	}
	raw := hex.EncodeToString(b[:])
	return raw[:8] + "-" + raw[8:12] + "-" + raw[12:16] + "-" + raw[16:20] + "-" + raw[20:], nil
}

func optionalDay(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	day := t.UTC().Format(time.DateOnly)
	return &day
}

// watchStatsRecount recomputes the counters of a daily row from the marks
// of its counted sessions (docs/watch-statistics.md). Effective time is a
// union over samples that retention may already have removed, so it is not
// recomputed.
const watchStatsRecount = `SELECT count(*)::bigint AS sessions,
 count(*) FILTER(WHERE p.stats_play='first')::bigint AS first_plays,
 count(*) FILTER(WHERE p.stats_play='rewatch')::bigint AS rewatches,
 count(*) FILTER(WHERE p.stats_completed)::bigint AS completions,
 COALESCE(sum(p.stats_completion_milli),0)::bigint AS completion_milli
 FROM playback_sessions p WHERE p.user_id=d.user_id AND p.item_id=d.item_id AND p.stats_counted AND p.stats_day=d.day`

// consistencyWatchStats compares a bounded sample of daily rows with their
// recount (phase "daily"), then a bounded sample of counted sessions with
// the existence of their daily row (phase "sessions"). A sample starts at a
// random key and wraps once.
func (s *Store) consistencyWatchStats(ctx context.Context, scope domain.ConsistencyScope, cursor string) (domain.ConsistencyPage, error) {
	n := scope.WatchSample
	if n < 1 || n > domain.ConsistencyMaxWatchSample {
		return domain.ConsistencyPage{}, domain.ErrInvalid
	}
	start, err := randomID()
	if err != nil {
		return domain.ConsistencyPage{}, err
	}
	page := domain.ConsistencyPage{Info: map[string]int64{}}
	switch cursor {
	case "":
		rows, err := s.Pool.Query(ctx, `WITH sample AS (
 (SELECT * FROM (SELECT * FROM watch_stats_daily WHERE user_id>=$2::uuid ORDER BY user_id,day,item_id LIMIT $5) a WHERE a.library_id=$1::uuid AND ($3::date IS NULL OR a.day>=$3::date) LIMIT $4)
 UNION ALL
 (SELECT * FROM (SELECT * FROM watch_stats_daily WHERE user_id<$2::uuid ORDER BY user_id,day,item_id LIMIT $5) b WHERE b.library_id=$1::uuid AND ($3::date IS NULL OR b.day>=$3::date) LIMIT $4))
 SELECT d.user_id::text,d.item_id::text,to_char(d.day,'YYYY-MM-DD'),d.sessions,d.first_plays,d.rewatches,d.views,d.completions,d.completion_milli,
  c.sessions,c.first_plays,c.rewatches,c.completions,c.completion_milli
 FROM (SELECT * FROM sample LIMIT $4) d CROSS JOIN LATERAL (`+watchStatsRecount+`) c`,
			scope.Library.ID, start, optionalDay(scope.SessionCutoff), n, n*consistencySampleScan)
		if err != nil {
			return domain.ConsistencyPage{}, storageError(err)
		}
		defer rows.Close()
		for rows.Next() {
			var f domain.ConsistencyFinding
			var have, want [6]int64
			if err = rows.Scan(&f.UserID, &f.ItemID, &f.Day, &have[0], &have[1], &have[2], &have[3], &have[4], &have[5], &want[0], &want[1], &want[2], &want[4], &want[5]); err != nil {
				return domain.ConsistencyPage{}, storageError(err)
			}
			want[3] = want[1] + want[2]
			page.Examined++
			page.Info[domain.ConsistencyInfoSampled]++
			if have != want {
				names := [6]string{"sessions", "firstPlays", "rewatches", "views", "completions", "completionMilli"}
				f.Expected, f.Actual = map[string]int64{}, map[string]int64{}
				for i, name := range names {
					f.Expected[name], f.Actual[name] = want[i], have[i]
				}
				f.Code, f.LibraryID, f.Fixable = domain.ConsistencyDailyCounterDrift, scope.Library.ID, true
				page.Candidates = append(page.Candidates, f)
			}
		}
		if err = rows.Err(); err != nil {
			return domain.ConsistencyPage{}, storageError(err)
		}
		page.Next = "sessions"
		return page, nil
	case "sessions":
		rows, err := s.Pool.Query(ctx, `WITH sample AS (
 (SELECT * FROM (SELECT id,user_id,item_id,library_id,stats_day,stats_counted FROM playback_sessions WHERE id>=$2::uuid ORDER BY id LIMIT $5) a WHERE a.library_id=$1::uuid AND a.stats_counted AND ($3::date IS NULL OR a.stats_day>=$3::date) LIMIT $4)
 UNION ALL
 (SELECT * FROM (SELECT id,user_id,item_id,library_id,stats_day,stats_counted FROM playback_sessions WHERE id<$2::uuid ORDER BY id LIMIT $5) b WHERE b.library_id=$1::uuid AND b.stats_counted AND ($3::date IS NULL OR b.stats_day>=$3::date) LIMIT $4))
 SELECT p.id::text,p.user_id::text,p.item_id::text,to_char(p.stats_day,'YYYY-MM-DD'),
  EXISTS(SELECT 1 FROM watch_stats_daily d WHERE d.user_id=p.user_id AND d.day=p.stats_day AND d.item_id=p.item_id)
 FROM (SELECT * FROM sample LIMIT $4) p`, scope.Library.ID, start, optionalDay(scope.DailyCutoff), n, n*consistencySampleScan)
		if err != nil {
			return domain.ConsistencyPage{}, storageError(err)
		}
		defer rows.Close()
		for rows.Next() {
			var f domain.ConsistencyFinding
			var present bool
			if err = rows.Scan(&f.Object, &f.UserID, &f.ItemID, &f.Day, &present); err != nil {
				return domain.ConsistencyPage{}, storageError(err)
			}
			page.Examined++
			page.Info[domain.ConsistencyInfoSampled]++
			if !present {
				f.Code, f.LibraryID = domain.ConsistencyDailyRowMissing, scope.Library.ID
				page.Candidates = append(page.Candidates, f)
			}
		}
		return page, storageError(rows.Err())
	}
	return domain.ConsistencyPage{}, domain.ErrInvalid
}

// fileReferences pages item_images or media_sidecar_tracks rows that point
// at a file below a root and compares each with the baseline.
func (s *Store) consistencyFileReferences(ctx context.Context, scope domain.ConsistencyScope, cursor, query, missing, changed string) (domain.ConsistencyPage, error) {
	after, err := idCursor(cursor)
	if err != nil {
		return domain.ConsistencyPage{}, err
	}
	rows, err := s.Pool.Query(ctx, query, scope.Library.ID, scope.Library.Snapshot, after, domain.ConsistencyPageSize)
	if err != nil {
		return domain.ConsistencyPage{}, storageError(err)
	}
	defer rows.Close()
	var page domain.ConsistencyPage
	last := ""
	for rows.Next() {
		var f domain.ConsistencyFinding
		var size, modified, baseSize, baseModified *int64
		var present, known bool
		if err = rows.Scan(&f.Object, &f.ItemID, &f.SourceID, &f.RootID, &f.RelativePath, &size, &modified, &present, &known, &baseSize, &baseModified); err != nil {
			return domain.ConsistencyPage{}, storageError(err)
		}
		page.Examined++
		last = f.Object
		f.LibraryID = scope.Library.ID
		switch {
		case !present:
			f.Code, f.Confirm = missing, domain.ConsistencyConfirmAbsent
			page.Candidates = append(page.Candidates, f)
		case known && size != nil && modified != nil && baseSize != nil && baseModified != nil && (*size != *baseSize || *modified != *baseModified):
			f.Code = changed
			f.Expected = map[string]int64{"size": *baseSize, "modifiedUnixNano": *baseModified}
			f.Actual = map[string]int64{"size": *size, "modifiedUnixNano": *modified}
			page.Candidates = append(page.Candidates, f)
		}
	}
	if err = rows.Err(); err != nil {
		return domain.ConsistencyPage{}, storageError(err)
	}
	page.Next = nextCursor(int(page.Examined), last)
	return page, nil
}

func (s *Store) consistencyImageFiles(ctx context.Context, scope domain.ConsistencyScope, cursor string) (domain.ConsistencyPage, error) {
	return s.consistencyFileReferences(ctx, scope, cursor, `SELECT i.id::text,i.item_id::text,'',i.root_id::text,i.relative_path,i.source_size,i.source_mtime_unix_nano,
 b.path IS NOT NULL,COALESCE(b.attributes_known,false),b.size,b.modified_unix_nano
 FROM item_images i LEFT JOIN library_inventory_baseline_data b ON b.library_id=i.library_id AND b.snapshot_id=$2 AND b.root_id=i.root_id AND b.path=i.relative_path
 WHERE i.library_id=$1::uuid AND i.root_id IS NOT NULL AND i.id>$3::uuid ORDER BY i.id LIMIT $4`, domain.ConsistencyImageSourceMissing, domain.ConsistencyImageSourceChanged)
}

func (s *Store) consistencySidecars(ctx context.Context, scope domain.ConsistencyScope, cursor string) (domain.ConsistencyPage, error) {
	return s.consistencyFileReferences(ctx, scope, cursor, `SELECT t.id::text,COALESCE(ms.item_id::text,''),t.source_id::text,t.root_id::text,t.relative_path,t.size,t.modified_unix_nano,
 b.path IS NOT NULL,COALESCE(b.attributes_known,false),b.size,b.modified_unix_nano
 FROM media_sidecar_tracks t LEFT JOIN media_sources ms ON ms.id=t.source_id
 LEFT JOIN library_inventory_baseline_data b ON b.library_id=t.library_id AND b.snapshot_id=$2 AND b.root_id=t.root_id AND b.path=t.relative_path
 WHERE t.library_id=$1::uuid AND t.id>$3::uuid ORDER BY t.id LIMIT $4`, domain.ConsistencySidecarMissing, domain.ConsistencySidecarChanged)
}

func (s *Store) consistencyProbeCache(ctx context.Context, scope domain.ConsistencyScope, cursor string) (domain.ConsistencyPage, error) {
	root, after, err := pathCursor(cursor)
	if err != nil {
		return domain.ConsistencyPage{}, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT p.root_id::text,p.relative_path,COALESCE(p.item_id::text,''),p.size,p.modified_unix_nano,
 b.path IS NOT NULL,COALESCE(b.attributes_known,false),COALESCE(b.size,0),COALESCE(b.modified_unix_nano,0)
 FROM probe_cache p LEFT JOIN library_inventory_baseline_data b ON b.library_id=p.library_id AND b.snapshot_id=$2 AND b.root_id=p.root_id AND b.path=p.relative_path
 WHERE p.library_id=$1::uuid AND p.state='ready' AND (p.root_id,p.relative_path)>($3::uuid,$4::text) ORDER BY p.root_id,p.relative_path LIMIT $5`,
		scope.Library.ID, scope.Library.Snapshot, root, after, domain.ConsistencyPageSize)
	if err != nil {
		return domain.ConsistencyPage{}, storageError(err)
	}
	defer rows.Close()
	var page domain.ConsistencyPage
	last := ""
	for rows.Next() {
		var f domain.ConsistencyFinding
		var size, modified, baseSize, baseModified int64
		var present, known bool
		if err = rows.Scan(&f.RootID, &f.RelativePath, &f.ItemID, &size, &modified, &present, &known, &baseSize, &baseModified); err != nil {
			return domain.ConsistencyPage{}, storageError(err)
		}
		page.Examined++
		last = f.RootID + consistencyCursorSeparator + f.RelativePath
		f.LibraryID = scope.Library.ID
		switch {
		case !present:
			f.Code, f.Confirm = domain.ConsistencyProbeCacheOrphan, domain.ConsistencyConfirmAbsent
			page.Candidates = append(page.Candidates, f)
		case known && (size != baseSize || modified != baseModified):
			f.Code = domain.ConsistencyProbeCacheChanged
			f.Expected = map[string]int64{"size": baseSize, "modifiedUnixNano": baseModified}
			f.Actual = map[string]int64{"size": size, "modifiedUnixNano": modified}
			page.Candidates = append(page.Candidates, f)
		}
	}
	if err = rows.Err(); err != nil {
		return domain.ConsistencyPage{}, storageError(err)
	}
	page.Next = nextCursor(int(page.Examined), last)
	return page, nil
}

// consistencyImageVariants samples the variant index from a random key; the
// runner confirms each row against the store directory.
func (s *Store) consistencyImageVariants(ctx context.Context, scope domain.ConsistencyScope) (domain.ConsistencyPage, error) {
	n := scope.VariantSample
	if n < 1 || n > domain.ConsistencyMaxWatchSample {
		return domain.ConsistencyPage{}, domain.ErrInvalid
	}
	var start [32]byte
	if _, err := rand.Read(start[:]); err != nil {
		return domain.ConsistencyPage{}, domain.ErrDatabase
	}
	rows, err := s.Pool.Query(ctx, `SELECT encode(content_sha256,'hex'),encode(variant_key,'hex') FROM (
 (SELECT content_sha256,variant_key FROM image_variants WHERE content_sha256>=$1 ORDER BY content_sha256,variant_key LIMIT $2)
 UNION ALL
 (SELECT content_sha256,variant_key FROM image_variants WHERE content_sha256<$1 ORDER BY content_sha256,variant_key LIMIT $2)) v LIMIT $2`, start[:], n)
	if err != nil {
		return domain.ConsistencyPage{}, storageError(err)
	}
	defer rows.Close()
	page := domain.ConsistencyPage{Info: map[string]int64{}}
	for rows.Next() {
		var source, variant string
		if err = rows.Scan(&source, &variant); err != nil {
			return domain.ConsistencyPage{}, storageError(err)
		}
		page.Examined++
		page.Info[domain.ConsistencyInfoSampled]++
		page.Candidates = append(page.Candidates, domain.ConsistencyFinding{Code: domain.ConsistencyVariantFileMissing, Object: source + "/" + variant, Confirm: domain.ConsistencyConfirmVariant})
	}
	return page, storageError(rows.Err())
}

// consistencyIntentionallyUnvalidated lists constraints a migration adds NOT
// VALID on purpose: they bind new rows only (schema 60).
var consistencyIntentionallyUnvalidated = []string{"audit_logs_event_format", "audit_logs_state_size"}

// consistencyConstraints reports constraints that are not validated and
// indexes that are invalid or not ready in the current schema. Foreign keys
// and unique constraints are enforced by PostgreSQL while they are valid;
// an unvalidated foreign key or an index left behind by a failed concurrent
// build is where violations can hide.
func (s *Store) consistencyConstraints(ctx context.Context) (domain.ConsistencyPage, error) {
	rows, err := s.Pool.Query(ctx, `SELECT 'constraint',c.conname,c.conrelid::regclass::text FROM pg_constraint c
 WHERE c.connamespace=current_schema()::regnamespace AND NOT c.convalidated AND c.conname<>ALL($1::text[])
 UNION ALL
 SELECT 'index',x.relname,i.indrelid::regclass::text FROM pg_index i JOIN pg_class x ON x.oid=i.indexrelid
 WHERE x.relnamespace=current_schema()::regnamespace AND (NOT i.indisvalid OR NOT i.indisready)
 ORDER BY 1,2`, consistencyIntentionallyUnvalidated)
	if err != nil {
		return domain.ConsistencyPage{}, storageError(err)
	}
	defer rows.Close()
	var page domain.ConsistencyPage
	for rows.Next() {
		var kind, name, table string
		if err = rows.Scan(&kind, &name, &table); err != nil {
			return domain.ConsistencyPage{}, storageError(err)
		}
		f := domain.ConsistencyFinding{Code: domain.ConsistencyConstraintNotValid, Object: table + "." + name}
		if kind == "index" {
			f.Code = domain.ConsistencyIndexInvalid
		}
		page.Candidates = append(page.Candidates, f)
	}
	if err = rows.Err(); err != nil {
		return domain.ConsistencyPage{}, storageError(err)
	}
	var total int64
	if err = s.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM pg_constraint WHERE connamespace=current_schema()::regnamespace)+(SELECT count(*) FROM pg_index i JOIN pg_class x ON x.oid=i.indexrelid WHERE x.relnamespace=current_schema()::regnamespace)`).Scan(&total); err != nil {
		return domain.ConsistencyPage{}, storageError(err)
	}
	page.Examined = total
	return page, nil
}

// nfoObservation is one stored item NFO observation with the source it
// names, resolved against the item's own sources.
type nfoObservation struct {
	item, kind, status       string
	size, modified           int64
	root, media, directory   string
	directoryKind            string
	candidates               []string
	foundPath                string
	found, known, unresolved bool
	baseSize, baseModified   int64
}

// nfoCaseVariants are the spellings looked up exactly before a bounded
// case-insensitive directory read; the NFO reader matches names ignoring
// case.
func nfoCaseVariants(candidate string) []string {
	directory, name := path.Split(candidate)
	ext := path.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	variants := []string{name, stem + strings.ToUpper(ext), strings.ToLower(name), strings.ToUpper(name)}
	switch strings.ToLower(name) {
	case "movie.nfo":
		variants = append(variants, "Movie.nfo")
	case "tvshow.nfo":
		variants = append(variants, "TVShow.nfo", "Tvshow.nfo", "TvShow.nfo")
	}
	out := make([]string, 0, len(variants))
	seen := map[string]bool{}
	for _, v := range variants {
		if !seen[v] {
			seen[v] = true
			out = append(out, directory+v)
		}
	}
	return out
}

// consistencyNFO compares stored item NFO observations with the NFO files of
// the baseline: an observed file that is gone, a file that appeared after a
// "missing" observation, a file whose size or modification time changed
// since it was read, and an observation naming a source of another item.
func (s *Store) consistencyNFO(ctx context.Context, scope domain.ConsistencyScope, cursor string) (domain.ConsistencyPage, error) {
	after, err := idCursor(cursor)
	if err != nil {
		return domain.ConsistencyPage{}, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT o.item_id::text,i.kind,o.observation->>'status',
 COALESCE((o.observation->'stamp'->>'size')::bigint,0),COALESCE((o.observation->'stamp'->>'modifiedUnixNano')::bigint,0),
 COALESCE(ms.root_id::text,ds.root_id::text,''),COALESCE(ms.relative_path,''),COALESCE(ds.relative_path,''),COALESCE(ds.kind,'')
 FROM item_nfo_observations o JOIN items i ON i.id=o.item_id
 LEFT JOIN media_sources ms ON ms.id=(o.observation->>'sourceId')::uuid AND ms.item_id=o.item_id
 LEFT JOIN item_directory_sources ds ON ds.id=(o.observation->>'sourceId')::uuid AND ds.item_id=o.item_id
 WHERE i.library_id=$1::uuid AND o.item_id>$2::uuid ORDER BY o.item_id LIMIT $3`, scope.Library.ID, after, domain.ConsistencyPageSize)
	if err != nil {
		return domain.ConsistencyPage{}, storageError(err)
	}
	observations, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (nfoObservation, error) {
		var o nfoObservation
		err := r.Scan(&o.item, &o.kind, &o.status, &o.size, &o.modified, &o.root, &o.media, &o.directory, &o.directoryKind)
		return o, err
	})
	if err != nil {
		return domain.ConsistencyPage{}, storageError(err)
	}
	page := domain.ConsistencyPage{Examined: int64(len(observations)), Info: map[string]int64{}}
	var roots, paths []string
	for i := range observations {
		o := &observations[i]
		o.candidates = nfoObservationCandidates(*o)
		for _, c := range o.candidates {
			for _, v := range nfoCaseVariants(c) {
				roots, paths = append(roots, o.root), append(paths, v)
			}
		}
	}
	present := map[string][3]int64{}
	if len(paths) > 0 {
		rows, err = s.Pool.Query(ctx, `SELECT b.root_id::text,b.path,b.attributes_known,COALESCE(b.size,0),COALESCE(b.modified_unix_nano,0)
 FROM unnest($3::uuid[],$4::text[]) c(root_id,path) JOIN library_inventory_baseline_data b ON b.library_id=$1::uuid AND b.snapshot_id=$2 AND b.root_id=c.root_id AND b.path=c.path`,
			scope.Library.ID, scope.Library.Snapshot, roots, paths)
		if err != nil {
			return domain.ConsistencyPage{}, storageError(err)
		}
		for rows.Next() {
			var root, p string
			var known bool
			var size, modified int64
			if err = rows.Scan(&root, &p, &known, &size, &modified); err != nil {
				rows.Close()
				return domain.ConsistencyPage{}, storageError(err)
			}
			flag := int64(0)
			if known {
				flag = 1
			}
			present[root+consistencyCursorSeparator+p] = [3]int64{flag, size, modified}
		}
		if err = rows.Err(); err != nil {
			return domain.ConsistencyPage{}, storageError(err)
		}
	}
	for i := range observations {
		o := &observations[i]
		resolveNFOObservation(o, present)
		if !o.found && o.status != domain.NFOItemObservedMissing && o.root != "" && len(o.candidates) > 0 {
			if err = s.resolveNFOFallback(ctx, scope, o); err != nil {
				return domain.ConsistencyPage{}, err
			}
		}
		f := domain.ConsistencyFinding{ItemID: o.item, LibraryID: scope.Library.ID, RootID: o.root}
		switch {
		case o.root == "":
			f.Code = domain.ConsistencyNFOForeignSource
		case len(o.candidates) == 0 || o.unresolved:
			page.Info[domain.ConsistencyInfoUnverified]++
			continue
		case o.status == domain.NFOItemObservedMissing && o.found:
			f.Code, f.RelativePath = domain.ConsistencyNFOAppeared, o.foundPath
		case o.status != domain.NFOItemObservedMissing && !o.found:
			f.Code, f.RelativePath = domain.ConsistencyNFOMissing, o.candidates[0]
		case o.status != domain.NFOItemObservedMissing && o.known && (o.size != o.baseSize || o.modified != o.baseModified):
			f.Code, f.RelativePath = domain.ConsistencyNFOChanged, o.foundPath
			f.Expected = map[string]int64{"size": o.baseSize, "modifiedUnixNano": o.baseModified}
			f.Actual = map[string]int64{"size": o.size, "modifiedUnixNano": o.modified}
		default:
			continue
		}
		page.Candidates = append(page.Candidates, f)
	}
	last := ""
	if len(observations) > 0 {
		last = observations[len(observations)-1].item
	}
	page.Next = nextCursor(len(observations), last)
	return page, nil
}

// nfoObservationCandidates lists the NFO paths the reader would consider for
// the observed source, in priority order.
func nfoObservationCandidates(o nfoObservation) []string {
	if o.root == "" {
		return nil
	}
	scope := domain.NFOItemScope{ItemID: consistencyZeroID, LibraryID: consistencyZeroID, SourceID: consistencyZeroID, RootID: consistencyZeroID, Kind: o.kind, Revision: 1, Generation: 1, Source: domain.NFOSource{RootPath: "/"}}
	if o.directory != "" {
		scope.DirectoryPath = o.directory
		scope.Source.RelativePath, _ = domain.DirectoryNFOPath(o.directory, o.kind)
	} else {
		scope.MediaPath = o.media
		scope.Source.RelativePath, _ = domain.AdjacentNFOPath(o.media)
	}
	return domain.NFOItemCandidatePaths(scope)
}

func resolveNFOObservation(o *nfoObservation, present map[string][3]int64) {
	for _, c := range o.candidates {
		for _, v := range nfoCaseVariants(c) {
			if hit, ok := present[o.root+consistencyCursorSeparator+v]; ok {
				o.found, o.foundPath, o.known, o.baseSize, o.baseModified = true, v, hit[0] == 1, hit[1], hit[2]
				return
			}
		}
	}
}

// resolveNFOFallback reads at most consistencyNFOFallbackRows NFO entries of
// the candidate directory and matches names ignoring case, as the reader
// does. A directory larger than that leaves the observation unresolved
// rather than reporting a file as missing.
func (s *Store) resolveNFOFallback(ctx context.Context, scope domain.ConsistencyScope, o *nfoObservation) error {
	directory := path.Dir(o.candidates[0])
	prefix, upper := "", "\U0010FFFF"
	if directory != "." {
		prefix, upper = directory+"/", directory+"0"
	}
	rows, err := s.Pool.Query(ctx, `SELECT path,kind,attributes_known,COALESCE(size,0),COALESCE(modified_unix_nano,0) FROM library_inventory_baseline_data
 WHERE library_id=$1::uuid AND snapshot_id=$2 AND root_id=$3::uuid AND (path COLLATE "C")>=($4::text COLLATE "C") AND (path COLLATE "C")<($5::text COLLATE "C")
 ORDER BY (path COLLATE "C") LIMIT $6`, scope.Library.ID, scope.Library.Snapshot, o.root, prefix, upper, consistencyNFOFallbackRows+1)
	if err != nil {
		return storageError(err)
	}
	defer rows.Close()
	read := 0
	for rows.Next() {
		var p string
		var kind *string
		var known bool
		var size, modified int64
		if err = rows.Scan(&p, &kind, &known, &size, &modified); err != nil {
			return storageError(err)
		}
		read++
		if path.Dir(p) != directory || kind != nil && *kind != "nfo" {
			continue
		}
		for _, c := range o.candidates {
			if strings.EqualFold(path.Base(c), path.Base(p)) {
				o.found, o.foundPath, o.known, o.baseSize, o.baseModified = true, p, known, size, modified
				return nil
			}
		}
	}
	if err = rows.Err(); err != nil {
		return storageError(err)
	}
	o.unresolved = read > consistencyNFOFallbackRows
	return nil
}
