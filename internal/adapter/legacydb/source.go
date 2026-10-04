// Package legacydb reads a Jellyfin or C# Jelee SQLite database (EF Core
// schema, Jellyfin 10.11 and later) for the legacy import of G04.6.
//
// The source file is never opened by SQLite: it is copied, with its -wal or
// -journal companion, into a private work directory while its SHA-256 is
// computed, and only the copy is queried. A file that changes during the
// copy is refused. This is the only package that links the SQLite driver;
// the server binary must never reach it (G04.8).
package legacydb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	_ "modernc.org/sqlite" // pure Go SQLite driver, registered as "sqlite"
)

// MinimumMigration is the EF Core migration that moved the library into
// jellyfin.db (Jellyfin 10.11). Older databases keep items in library.db.
const MinimumMigration = "20241020103111_LibraryDbMigration"

// TestedMigration is the newest upstream migration this reader was written
// against (tag upstream-csharp-final). Newer files are read when the
// required columns are present and reported as untested.
const TestedMigration = "20260815063607_RemoveOrphanedUserPermissionsAndPreferences"

const collectionFolderType = "MediaBrowser.Controller.Entities.CollectionFolder"

// Source table counts reported by Info.
const (
	CountUsers             = "Users"
	CountPermissions       = "Permissions"
	CountPreferences       = "Preferences"
	CountBaseItems         = "BaseItems"
	CountCollectionFolders = "CollectionFolders"
	CountLocations         = "PhysicalLocations"
	CountUserData          = "UserData"
)

var requiredColumns = map[string][]string{
	"__EFMigrationsHistory": {"MigrationId"},
	"Users":                 {"Id", "Username"},
	"Permissions":           {"UserId", "Kind", "Value"},
	"Preferences":           {"UserId", "Kind", "Value"},
	"BaseItems":             {"Id", "Type", "Path", "Name", "Data", "IsVirtualItem", "ExtraType"},
	"UserData":              {"UserId", "ItemId", "CustomDataKey", "PlaybackPositionTicks", "Played", "PlayCount", "LastPlayedDate", "IsFavorite"},
}

// Source is an opened copy of a legacy database.
type Source struct {
	db   *sql.DB
	dir  string
	info domain.LegacySourceInfo
	// Optional Users columns, as SQL expressions.
	userPassword, userRating, userSessions, userBitrate string
}

func unsupported(reason string) error {
	return fmt.Errorf("%w: %s", domain.ErrLegacySourceUnsupported, reason)
}

// Open copies path into a new directory under workDir (the system temporary
// directory when empty) and opens the copy. Close removes the copy.
func Open(ctx context.Context, path, workDir string) (*Source, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, unsupported("source_unreadable")
	}
	dir, err := os.MkdirTemp(workDir, "jelee-legacy-*")
	if err != nil {
		return nil, fmt.Errorf("legacy work directory: %w", err)
	}
	s := &Source{dir: dir}
	ok := false
	defer func() {
		if !ok {
			_ = s.Close()
		}
	}()
	copyPath := filepath.Join(dir, "source.db")
	sum, size, err := copyHashed(path, copyPath)
	if err != nil {
		return nil, err
	}
	s.info = domain.LegacySourceInfo{File: filepath.Base(path), SHA256: sum, Size: size, Counts: map[string]int64{}}
	// A companion holds committed (WAL) or rolled back (hot journal) pages;
	// the copy is only consistent with it.
	for _, suffix := range []string{"-wal", "-journal"} {
		companion := path + suffix
		if _, statErr := os.Stat(companion); errors.Is(statErr, fs.ErrNotExist) {
			continue
		}
		walSum, walSize, copyErr := copyHashed(companion, copyPath+suffix)
		if copyErr != nil {
			return nil, copyErr
		}
		if walSize > 0 {
			s.info.WALSHA256 = walSum
		}
	}
	// The copy is private; query_only keeps this reader from writing even to
	// it. Recovery of a copied WAL or journal still happens on open.
	dsn := "file:" + (&url.URL{Path: sqlitePath(copyPath)}).EscapedPath() + "?_pragma=query_only(1)&_pragma=busy_timeout(5000)"
	s.db, err = sql.Open("sqlite", dsn)
	if err != nil {
		return nil, unsupported("source_unreadable")
	}
	s.db.SetMaxOpenConns(1)
	if err = s.inspect(ctx); err != nil {
		return nil, err
	}
	ok = true
	return s, nil
}

// sqlitePath turns a file name into a file: URI path.
func sqlitePath(p string) string {
	p = filepath.ToSlash(p)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}

// copyHashed copies src to dst while hashing it. The source must look the
// same (size and modification time) before and after.
func copyHashed(src, dst string) (string, int64, error) {
	before, err := os.Stat(src)
	if err != nil {
		return "", 0, unsupported("source_unreadable")
	}
	in, err := os.Open(src) //nolint:gosec // G304: the operator names the source database
	if err != nil {
		return "", 0, unsupported("source_unreadable")
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // G304: inside our own temporary directory
	if err != nil {
		return "", 0, fmt.Errorf("legacy copy: %w", err)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), in)
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", 0, fmt.Errorf("legacy copy: %w", err)
	}
	after, err := os.Stat(src)
	if err != nil || n != before.Size() || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return "", 0, domain.ErrLegacySourceChanged
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// Close closes the copy and removes the work directory.
func (s *Source) Close() error {
	var err error
	if s.db != nil {
		err = s.db.Close()
		s.db = nil
	}
	if s.dir != "" {
		if removeErr := os.RemoveAll(s.dir); err == nil {
			err = removeErr
		}
		s.dir = ""
	}
	return err
}

// Info describes the source; valid after Open.
func (s *Source) Info() domain.LegacySourceInfo { return s.info }

func (s *Source) columns(ctx context.Context, table string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

func (s *Source) inspect(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table'`)
	if err != nil {
		return unsupported("not_sqlite")
	}
	present := map[string]bool{}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			_ = rows.Close()
			return unsupported("not_sqlite")
		}
		present[name] = true
	}
	_ = rows.Close()
	if err = rows.Err(); err != nil {
		return unsupported("not_sqlite")
	}
	if !present["BaseItems"] && present["TypedBaseItems"] {
		return unsupported("library_db_before_10_11")
	}
	var userColumns map[string]bool
	for table, required := range requiredColumns {
		if !present[table] {
			return unsupported("missing_table_" + table)
		}
		cols, colErr := s.columns(ctx, table)
		if colErr != nil {
			return unsupported("not_sqlite")
		}
		for _, c := range required {
			if !cols[c] {
				return unsupported("missing_column_" + table + "_" + c)
			}
		}
		if table == "Users" {
			userColumns = cols
		}
	}
	s.userPassword, s.userRating, s.userSessions, s.userBitrate = "0", "NULL", "0", "NULL"
	if userColumns["Password"] {
		s.userPassword = "(Password IS NOT NULL AND Password<>'')"
	}
	if userColumns["MaxParentalRatingScore"] {
		s.userRating = "MaxParentalRatingScore"
	} else if userColumns["MaxParentalAgeRating"] {
		s.userRating = "MaxParentalAgeRating"
	}
	if userColumns["MaxActiveSessions"] {
		s.userSessions = "MaxActiveSessions"
	}
	if userColumns["RemoteClientBitrateLimit"] {
		s.userBitrate = "RemoteClientBitrateLimit"
	}
	var latest sql.NullString
	if err = s.db.QueryRowContext(ctx, `SELECT max(MigrationId),count(*) FROM __EFMigrationsHistory`).Scan(&latest, &s.info.Migrations); err != nil {
		return unsupported("not_sqlite")
	}
	if !latest.Valid || latest.String < MinimumMigration {
		return unsupported("schema_before_10_11")
	}
	s.info.LatestMigration, s.info.Tested = latest.String, latest.String <= TestedMigration
	// Cursors compare stored keys, so identifiers must be stored as text.
	for _, table := range []string{"Users", "BaseItems"} {
		var kind sql.NullString
		err = s.db.QueryRowContext(ctx, `SELECT typeof(Id) FROM `+table+` WHERE typeof(Id)<>'text' LIMIT 1`).Scan(&kind) //nolint:gosec // G202: table comes from the fixed list above
		if err == nil {
			return unsupported("non_text_identifiers")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return unsupported("not_sqlite")
		}
	}
	for name, query := range map[string]string{
		CountUsers:             `SELECT count(*) FROM Users`,
		CountPermissions:       `SELECT count(*) FROM Permissions`,
		CountPreferences:       `SELECT count(*) FROM Preferences`,
		CountBaseItems:         `SELECT count(*) FROM BaseItems`,
		CountCollectionFolders: `SELECT count(*) FROM BaseItems WHERE Type='` + collectionFolderType + `'`,
		CountUserData:          `SELECT count(*) FROM UserData`,
	} {
		var n int64
		if err = s.db.QueryRowContext(ctx, query).Scan(&n); err != nil {
			return unsupported("not_sqlite")
		}
		s.info.Counts[name] = n
	}
	// Library locations live in the item JSON; count them once.
	cursor := ""
	for {
		libraries, next, listErr := s.Libraries(ctx, cursor, 500)
		if listErr != nil {
			return listErr
		}
		if len(libraries) == 0 {
			break
		}
		for _, l := range libraries {
			s.info.Counts[CountLocations] += int64(len(l.Locations))
		}
		cursor = next
	}
	return nil
}

// normalizeID lower-cases a stored GUID.
func normalizeID(raw string) string { return strings.ToLower(strings.TrimSpace(raw)) }

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// Users returns accounts with their permission and preference rows.
func (s *Source) Users(ctx context.Context, after string, limit int) ([]domain.LegacyUser, string, error) {
	// The column expressions are fixed strings inspect chose by schema.
	query := `SELECT Id,Username,` + s.userPassword + `,` + s.userRating + `,` + s.userSessions + `,` + s.userBitrate + ` FROM Users WHERE Id>? ORDER BY Id LIMIT ?` //nolint:gosec // G202: fixed column expressions
	rows, err := s.db.QueryContext(ctx, query, after, limit)
	if err != nil {
		return nil, "", sourceError(err)
	}
	var users []domain.LegacyUser
	var raw []any
	index := map[string]int{}
	cursor := after
	for rows.Next() {
		var id, name string
		var password bool
		var rating, bitrate sql.NullInt64
		var sessions sql.NullInt64
		if err = rows.Scan(&id, &name, &password, &rating, &sessions, &bitrate); err != nil {
			_ = rows.Close()
			return nil, "", sourceError(err)
		}
		u := domain.LegacyUser{ID: normalizeID(id), Name: name, HasPassword: password, MaxActiveSessions: int(sessions.Int64)}
		if rating.Valid {
			v := int(rating.Int64)
			u.MaxParentalRating = &v
		}
		if bitrate.Valid {
			v := bitrate.Int64
			u.RemoteBitrate = &v
		}
		index[id] = len(users)
		users = append(users, u)
		raw = append(raw, id)
		cursor = id
	}
	_ = rows.Close()
	if err = rows.Err(); err != nil || len(users) == 0 {
		return users, cursor, sourceError(err)
	}
	if err = s.userRows(ctx, `SELECT UserId,Kind,Value FROM Permissions WHERE UserId IN (`+placeholders(len(raw))+`) ORDER BY UserId,Kind,Id`, raw, func(id string, kind int, value sql.RawBytes) {
		v, _ := strconv.ParseBool(string(value))
		users[index[id]].Permissions = append(users[index[id]].Permissions, domain.LegacyFlag{Kind: kind, Value: v})
	}); err != nil {
		return nil, "", err
	}
	if err = s.userRows(ctx, `SELECT UserId,Kind,Value FROM Preferences WHERE UserId IN (`+placeholders(len(raw))+`) ORDER BY UserId,Kind,Id`, raw, func(id string, kind int, value sql.RawBytes) {
		users[index[id]].Preferences = append(users[index[id]].Preferences, domain.LegacyPreference{Kind: kind, Value: string(value)})
	}); err != nil {
		return nil, "", err
	}
	return users, cursor, nil
}

func (s *Source) userRows(ctx context.Context, query string, args []any, add func(string, int, sql.RawBytes)) error {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return sourceError(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		var kind int
		var value sql.RawBytes
		if err = rows.Scan(&id, &kind, &value); err != nil {
			return sourceError(err)
		}
		add(id, kind, value)
	}
	return sourceError(rows.Err())
}

// libraryData is the part of a CollectionFolder's serialized item the
// import reads. System.Text.Json writes PascalCase names.
type libraryData struct {
	PhysicalLocationsList []string `json:"PhysicalLocationsList"`
	CollectionType        *string  `json:"CollectionType"`
}

// Libraries returns the collection folders with their physical locations.
func (s *Source) Libraries(ctx context.Context, after string, limit int) ([]domain.LegacyLibrary, string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT Id,COALESCE(Name,''),COALESCE(Data,'') FROM BaseItems WHERE Type='`+collectionFolderType+`' AND Id>? ORDER BY Id LIMIT ?`, after, limit)
	if err != nil {
		return nil, "", sourceError(err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.LegacyLibrary
	cursor := after
	for rows.Next() {
		var id, name, data string
		if err = rows.Scan(&id, &name, &data); err != nil {
			return nil, "", sourceError(err)
		}
		var d libraryData
		// Unreadable JSON leaves a library without locations; the import
		// reports it as such.
		_ = json.Unmarshal([]byte(data), &d)
		l := domain.LegacyLibrary{ID: normalizeID(id), Name: name}
		if d.CollectionType != nil {
			l.CollectionType = *d.CollectionType
		}
		for _, p := range d.PhysicalLocationsList {
			if p != "" {
				l.Locations = append(l.Locations, p)
			}
		}
		out = append(out, l)
		cursor = id
	}
	return out, cursor, sourceError(rows.Err())
}

// Items returns every BaseItems row in key order.
func (s *Source) Items(ctx context.Context, after string, limit int) ([]domain.LegacyItem, string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT Id,COALESCE(Type,''),COALESCE(Path,''),COALESCE(IsVirtualItem,0),ExtraType IS NOT NULL FROM BaseItems WHERE Id>? ORDER BY Id LIMIT ?`, after, limit)
	if err != nil {
		return nil, "", sourceError(err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.LegacyItem
	cursor := after
	for rows.Next() {
		var id string
		var item domain.LegacyItem
		if err = rows.Scan(&id, &item.Type, &item.Path, &item.Virtual, &item.Extra); err != nil {
			return nil, "", sourceError(err)
		}
		item.ID = normalizeID(id)
		out = append(out, item)
		cursor = id
	}
	return out, cursor, sourceError(rows.Err())
}

const userDataColumns = `SELECT d.UserId,d.ItemId,COALESCE(d.PlaybackPositionTicks,0),COALESCE(d.Played,0),COALESCE(d.PlayCount,0),d.LastPlayedDate,COALESCE(d.IsFavorite,0),
 COALESCE(b.Type,''),u.Id IS NOT NULL FROM UserData d LEFT JOIN BaseItems b ON b.Id=d.ItemId LEFT JOIN Users u ON u.Id=d.UserId `

type userDataRow struct {
	user, item string
	data       domain.LegacyUserData
}

func (s *Source) userDataRows(ctx context.Context, query string, args ...any) ([]userDataRow, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, sourceError(err)
	}
	defer func() { _ = rows.Close() }()
	var out []userDataRow
	for rows.Next() {
		var r userDataRow
		var last sql.NullString
		if err = rows.Scan(&r.user, &r.item, &r.data.Position, &r.data.Played, &r.data.PlayCount, &last, &r.data.Favorite, &r.data.ItemType, &r.data.UserExists); err != nil {
			return nil, sourceError(err)
		}
		r.data.UserID, r.data.ItemID, r.data.Rows = normalizeID(r.user), normalizeID(r.item), 1
		if last.Valid {
			if t, ok := parseTime(last.String); ok {
				r.data.LastPlayed = &t
			}
		}
		out = append(out, r)
	}
	return out, sourceError(rows.Err())
}

// UserData returns the user data of each user and item, several rows
// (CustomDataKey) merged into the most recently played one. The cursor is
// the stored user and item key; a group is never split between batches.
func (s *Source) UserData(ctx context.Context, after string, limit int) ([]domain.LegacyUserData, string, error) {
	afterUser, afterItem, _ := strings.Cut(after, "\x1f")
	rows, err := s.userDataRows(ctx, userDataColumns+`WHERE (d.UserId,d.ItemId)>(?,?) ORDER BY d.UserId,d.ItemId,d.CustomDataKey LIMIT ?`, afterUser, afterItem, limit)
	if err != nil || len(rows) == 0 {
		return nil, after, err
	}
	if len(rows) == limit {
		// The last group may continue past the limit: read it whole.
		last := rows[len(rows)-1]
		cut := len(rows)
		for cut > 0 && rows[cut-1].user == last.user && rows[cut-1].item == last.item {
			cut--
		}
		rest, restErr := s.userDataRows(ctx, userDataColumns+`WHERE d.UserId=? AND d.ItemId=? ORDER BY d.CustomDataKey`, last.user, last.item)
		if restErr != nil {
			return nil, after, restErr
		}
		rows = append(rows[:cut], rest...)
	}
	var out []domain.LegacyUserData
	for i := 0; i < len(rows); {
		j := i + 1
		merged := rows[i].data
		for ; j < len(rows) && rows[j].user == rows[i].user && rows[j].item == rows[i].item; j++ {
			merged = mergeUserData(merged, rows[j].data)
		}
		out = append(out, merged)
		i = j
	}
	last := rows[len(rows)-1]
	return out, last.user + "\x1f" + last.item, nil
}

// mergeUserData keeps the more recently played row; ties keep the one
// with more progress. Favorite holds when any row has it.
func mergeUserData(a, b domain.LegacyUserData) domain.LegacyUserData {
	keep := a
	switch {
	case b.LastPlayed != nil && (a.LastPlayed == nil || b.LastPlayed.After(*a.LastPlayed)):
		keep = b
	case (a.LastPlayed == nil) == (b.LastPlayed == nil) && (a.LastPlayed == nil || a.LastPlayed.Equal(*b.LastPlayed)) &&
		(b.Played && !a.Played || b.Played == a.Played && b.Position > a.Position):
		keep = b
	}
	keep.Rows = a.Rows + b.Rows
	keep.Favorite = a.Favorite || b.Favorite
	return keep
}

// EF Core writes DateTime as "yyyy-MM-dd HH:mm:ss.FFFFFFF"; Jellyfin stores
// UTC. Values with an offset are honoured.
var timeLayouts = []string{"2006-01-02 15:04:05.9999999", "2006-01-02T15:04:05.9999999", "2006-01-02 15:04:05.9999999Z07:00", "2006-01-02T15:04:05.9999999Z07:00"}

func parseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, layout := range timeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			if t.Year() < 1971 || t.Year() > 9999 {
				return time.Time{}, false
			}
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

func sourceError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("%w: read_failed", domain.ErrLegacySourceUnsupported)
}
