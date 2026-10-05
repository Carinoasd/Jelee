package domain

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
)

// Legacy database import (G04.6): an upstream or C# Jelee SQLite database
// (EF Core schema, upstream 10.11 and later) is read through a LegacySource
// and merged into PostgreSQL in checkpointed batches. docs/legacy-import.md
// describes the procedure, the report and the compatibility range.

var (
	// ErrLegacySourceUnsupported means the file is not a supported SQLite
	// database (too old, missing tables or columns, unreadable).
	ErrLegacySourceUnsupported = errors.New("legacy_source_unsupported")
	// ErrLegacySourceChanged means the source file changed while it was
	// copied.
	ErrLegacySourceChanged = errors.New("legacy_source_changed")
	// ErrLegacyImportConflict means preflight found conflicts and the
	// operator did not ask to skip them. Nothing was written.
	ErrLegacyImportConflict = errors.New("legacy_import_conflict")
	// ErrLegacyImportBusy means another import holds the import lock.
	ErrLegacyImportBusy = errors.New("legacy_import_busy")
	// ErrLegacyImportRunMismatch means an unfinished run exists for another
	// source file or other options; resume it or restart explicitly.
	ErrLegacyImportRunMismatch = errors.New("legacy_import_run_mismatch")
	// ErrLegacyImportSchema means the target database is not at this
	// binary's schema version.
	ErrLegacyImportSchema = errors.New("legacy_import_schema")
	// ErrLegacyImportUnbalanced means the final row reconciliation failed.
	ErrLegacyImportUnbalanced = errors.New("legacy_import_unbalanced")
)

// LegacyImportReportSchema names the report format of docs/legacy-import.md.
const LegacyImportReportSchema = "jelee.legacy-import-report/v1"

// Report states.
const (
	LegacyStatePreflight = "preflight"
	LegacyStateRefused   = "refused"
	LegacyStatePaused    = "paused"
	LegacyStateFailed    = "failed"
	LegacyStateCompleted = "completed"
)

// Phases run in this order; each has its own checkpoint.
const (
	LegacyPhaseUsers     = "users"
	LegacyPhaseLibraries = "libraries"
	LegacyPhaseAccess    = "access"
	LegacyPhaseItems     = "items"
	LegacyPhaseUserData  = "user_data"
)

// LegacyImportPhases returns the phases in execution order.
func LegacyImportPhases() []string {
	return []string{LegacyPhaseUsers, LegacyPhaseLibraries, LegacyPhaseAccess, LegacyPhaseItems, LegacyPhaseUserData}
}

// Report categories. Every category except library_access counts rows of a
// source table; library_access counts grants derived from them.
const (
	LegacyCatUsers         = "users"
	LegacyCatPermissions   = "permissions"
	LegacyCatPreferences   = "preferences"
	LegacyCatLibraries     = "libraries"
	LegacyCatLibraryRoots  = "library_roots"
	LegacyCatLibraryAccess = "library_access"
	LegacyCatItems         = "items"
	LegacyCatUserData      = "user_data"
)

// LegacyImportCategories returns the report categories in display order.
func LegacyImportCategories() []string {
	return []string{LegacyCatUsers, LegacyCatPermissions, LegacyCatPreferences, LegacyCatLibraries, LegacyCatLibraryRoots,
		LegacyCatLibraryAccess, LegacyCatItems, LegacyCatUserData}
}

// Outcome reasons. Skipped rows are left out on purpose, conflicts disagree
// with the target, pending rows may resolve on a later run.
const (
	LegacyReasonRemovedDomain     = "removed_domain"      // music, books, photos, live TV (G02.2, G05.4)
	LegacyReasonUnsupportedKind   = "unsupported_kind"    // trailers, box sets, playlists
	LegacyReasonStructural        = "structural"          // folders, views, people, genres
	LegacyReasonVirtual           = "virtual_item"        // missing episodes and other placeholders
	LegacyReasonExtra             = "extra"               // extras owned by another item
	LegacyReasonNoPath            = "no_path"             // no file path
	LegacyReasonPathUnmapped      = "path_unmapped"       // no path rule maps it to an absolute target path
	LegacyReasonFolderUnmatched   = "folder_unmatched"    // series/season folder without a registered directory
	LegacyReasonOutsideRoots      = "outside_roots"       // pending: under no library root
	LegacyReasonNotScanned        = "not_scanned"         // pending: under a root, not in the catalog yet
	LegacyReasonFileMissing       = "source_file_missing" // pending: the mapped file does not exist
	LegacyReasonNameTaken         = "name_taken"
	LegacyReasonNameTakenDeleted  = "name_taken_deleted"
	LegacyReasonNameDuplicate     = "name_duplicate"
	LegacyReasonNameInvalid       = "name_invalid"
	LegacyReasonTargetDeleted     = "target_deleted"
	LegacyReasonRootTaken         = "root_path_taken"
	LegacyReasonRootUnmapped      = "root_path_unmapped"
	LegacyReasonNoLocations       = "no_locations"
	LegacyReasonUnsupportedType   = "unsupported_collection_type"
	LegacyReasonUserMerged        = "user_merged"
	LegacyReasonUserNotImported   = "user_not_imported"
	LegacyReasonUserMissing       = "user_missing"
	LegacyReasonNotApplicable     = "not_applicable"
	LegacyReasonDownloadRemoved   = "download_removed" // G06: no download permission exists
	LegacyReasonLibraryNotImport  = "library_not_imported"
	LegacyReasonItemDetached      = "item_detached"
	LegacyReasonItemMissing       = "item_missing"
	LegacyReasonItemNotMapped     = "item_not_mapped"
	LegacyReasonItemPending       = "item_pending"
	LegacyReasonEmpty             = "empty"
	LegacyReasonFavoriteOnly      = "favorite_unsupported"
	LegacyReasonMergedDuplicate   = "merged_duplicate" // further rows of one user and item (CustomDataKey)
	LegacyReasonMergedVersion     = "merged_version"   // another source item became the same Jelee item
	LegacyReasonDuplicateLocation = "duplicate_location"
	LegacyReasonInvalidID         = "invalid_id" // a source key Jelee cannot store
)

// LegacyImportSampleLimit bounds the conflicts and pending rows listed in a
// report; the totals are always complete.
const LegacyImportSampleLimit = 100

// Batch size bounds.
const (
	LegacyImportDefaultBatch = 1000
	LegacyImportMaxBatch     = 10000
)

// LegacyPlaceholderItemID is the item upstream points detached user data at.
const LegacyPlaceholderItemID = "00000000-0000-0000-0000-000000000001"

// LegacyPathRule maps a source path prefix to a target prefix.
type LegacyPathRule struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// LegacyImportOptions controls one invocation.
type LegacyImportOptions struct {
	PathMap       []LegacyPathRule
	MergeUsers    bool
	SkipConflicts bool
	PreflightOnly bool
	Restart       bool
	BatchSize     int
	// MaxBatches stops after this many committed batches (0: no limit); the
	// run stays resumable.
	MaxBatches int
	// FileExists tells a missing file from one the catalog has not seen
	// yet; nil leaves them all "not_scanned".
	FileExists func(path string) bool
}

// LegacyUser is one source account with its permission and preference rows.
type LegacyUser struct {
	ID                string
	Name              string
	HasPassword       bool
	MaxParentalRating *int
	MaxActiveSessions int
	RemoteBitrate     *int64
	Permissions       []LegacyFlag
	Preferences       []LegacyPreference
}

// LegacyFlag is one Permissions row.
type LegacyFlag struct {
	Kind  int
	Value bool
}

// LegacyPreference is one Preferences row.
type LegacyPreference struct {
	Kind  int
	Value string
}

// Upstream PermissionKind and PreferenceKind values the import reads.
const (
	LegacyPermAdministrator   = 0
	LegacyPermHidden          = 1
	LegacyPermDisabled        = 2
	LegacyPermLiveTVManage    = 5
	LegacyPermLiveTVAccess    = 6
	LegacyPermDownload        = 11
	LegacyPermAllFolders      = 16
	LegacyPrefBlockedTags     = 0
	LegacyPrefEnabledFolders  = 5
	LegacyPrefBlockUnrated    = 10
	legacyPermissionKindCount = 24
)

// Permission reports whether the user holds kind.
func (u LegacyUser) Permission(kind int) bool {
	for _, p := range u.Permissions {
		if p.Kind == kind {
			return p.Value
		}
	}
	return false
}

// Preference returns the comma separated values of kind, trimmed, empty
// entries dropped.
func (u LegacyUser) Preference(kind int) []string {
	var out []string
	for _, p := range u.Preferences {
		if p.Kind != kind {
			continue
		}
		for _, v := range strings.Split(p.Value, ",") {
			if v = strings.TrimSpace(v); v != "" {
				out = append(out, v)
			}
		}
	}
	return out
}

// LegacyPermissionOutcome classifies one permission row of a created user:
// "" when it is applied, otherwise the skip reason.
func LegacyPermissionOutcome(kind int) string {
	switch kind {
	case LegacyPermAdministrator, LegacyPermHidden, LegacyPermDisabled, LegacyPermAllFolders:
		return ""
	case LegacyPermDownload:
		return LegacyReasonDownloadRemoved
	case LegacyPermLiveTVManage, LegacyPermLiveTVAccess:
		return LegacyReasonRemovedDomain
	}
	return LegacyReasonNotApplicable
}

// LegacyPreferenceOutcome classifies one preference row of a created user.
func LegacyPreferenceOutcome(kind int) string {
	switch kind {
	case LegacyPrefBlockedTags, LegacyPrefEnabledFolders, LegacyPrefBlockUnrated:
		return ""
	}
	return LegacyReasonNotApplicable
}

// LegacyAccount is the Jelee account a created user receives. Passwords are
// never carried over: upstream PBKDF2 hashes do not satisfy the Argon2id
// policy, so every imported account starts without one.
type LegacyAccount struct {
	Name              string
	Admin             bool
	Disabled          bool
	Hidden            bool
	ParentalRatingMax *int
	BlockUnrated      *bool
	MaxStreams        *int
	MaxKbps           *int64
	BlockedTags       []string
}

// Account derives the Jelee account fields.
func (u LegacyUser) Account() LegacyAccount {
	a := LegacyAccount{Name: u.Name, Admin: u.Permission(LegacyPermAdministrator), Disabled: u.Permission(LegacyPermDisabled), Hidden: u.Permission(LegacyPermHidden)}
	if u.MaxParentalRating != nil {
		level := min(max(*u.MaxParentalRating, 0), 21)
		a.ParentalRatingMax = &level
		if len(u.Preference(LegacyPrefBlockUnrated)) > 0 {
			block := true
			a.BlockUnrated = &block
		}
	}
	if u.MaxActiveSessions > 0 {
		n := min(u.MaxActiveSessions, 128)
		a.MaxStreams = &n
	}
	if u.RemoteBitrate != nil && *u.RemoteBitrate > 0 {
		kbps := min(max(*u.RemoteBitrate/1000, 1), 10000000)
		a.MaxKbps = &kbps
	}
	seen := map[string]bool{}
	for _, tag := range u.Preference(LegacyPrefBlockedTags) {
		tag = strings.ToLower(tag)
		if len([]rune(tag)) <= 128 && !seen[tag] && !strings.ContainsFunc(tag, isControl) {
			seen[tag] = true
			a.BlockedTags = append(a.BlockedTags, tag)
		}
	}
	sort.Strings(a.BlockedTags)
	return a
}

func isControl(r rune) bool { return r < 0x20 || r == 0x7f || r >= 0x80 && r < 0xa0 }

// ValidLegacyName reports whether a user or library name fits Jelee's rule:
// 1–128 bytes of trimmed UTF-8 without control characters.
func ValidLegacyName(name string) bool {
	return name != "" && len(name) <= 128 && strings.TrimSpace(name) == name && !strings.ContainsFunc(name, isControl) && strings.ToValidUTF8(name, "�") == name
}

// LegacyLibrary is one upstream CollectionFolder.
type LegacyLibrary struct {
	ID             string
	Name           string
	CollectionType string
	Locations      []string
}

// LegacyLibraryOutcome classifies a library by collection type: "" imports.
func LegacyLibraryOutcome(collectionType string) string {
	switch strings.ToLower(collectionType) {
	case "", "unknown", "movies", "tvshows", "homevideos", "mixed":
		return ""
	case "music", "musicvideos", "books", "audiobooks", "photos", "livetv":
		return LegacyReasonRemovedDomain
	}
	return LegacyReasonUnsupportedType
}

// LegacyItem is one upstream BaseItems row.
type LegacyItem struct {
	ID      string
	Type    string
	Path    string
	Virtual bool
	Extra   bool
}

// Item shapes the import maps.
const (
	LegacyShapeNone = iota
	LegacyShapeFile
	LegacyShapeFolder
)

// LegacyItemShape classifies an upstream type name: file items match media
// sources, folder items match registered series/season directories.
// Anything else returns the skip reason.
func LegacyItemShape(typeName string) (int, string) {
	short := typeName[strings.LastIndexByte(typeName, '.')+1:]
	switch short {
	case "Movie", "Episode", "Video":
		return LegacyShapeFile, ""
	case "Series", "Season":
		return LegacyShapeFolder, ""
	case "Audio", "AudioBook", "MusicAlbum", "MusicArtist", "MusicGenre", "MusicVideo", "Book", "Photo", "PhotoAlbum",
		"LiveTvChannel", "LiveTvProgram", "Channel", "Recording", "LiveTvVideoRecording", "LiveTvAudioRecording":
		return LegacyShapeNone, LegacyReasonRemovedDomain
	case "Trailer", "BoxSet", "Playlist":
		return LegacyShapeNone, LegacyReasonUnsupportedKind
	}
	return LegacyShapeNone, LegacyReasonStructural
}

// Classify returns the shape and skip reason of an item, path aside.
func (i LegacyItem) Classify() (int, string) {
	shape, reason := LegacyItemShape(i.Type)
	switch {
	case shape == LegacyShapeNone:
		return shape, reason
	case i.Virtual:
		return LegacyShapeNone, LegacyReasonVirtual
	case i.Extra:
		return LegacyShapeNone, LegacyReasonExtra
	case strings.TrimSpace(i.Path) == "":
		return LegacyShapeNone, LegacyReasonNoPath
	}
	return shape, ""
}

// LegacyUserData is the user data of one user and item; upstream may hold
// several rows (CustomDataKey) that are merged into the most recent one.
type LegacyUserData struct {
	UserID     string
	ItemID     string
	ItemType   string // "" when the item row is gone
	UserExists bool
	Rows       int
	Position   int64
	Played     bool
	PlayCount  int
	LastPlayed *time.Time
	Favorite   bool
}

// Key is the ledger key of the row.
func (d LegacyUserData) Key() string { return d.UserID + "|" + d.ItemID }

// Empty reports a row that carries no playback state.
func (d LegacyUserData) Empty() bool {
	return !d.Played && d.PlayCount <= 0 && d.Position <= 0
}

// MaxLegacyTicks is Jelee's upper bound for positions (100 days).
const MaxLegacyTicks = 86400000000000

// LegacySourceInfo describes a source file after it was copied.
type LegacySourceInfo struct {
	File            string
	SHA256          string
	Size            int64
	WALSHA256       string
	LatestMigration string
	Migrations      int
	Tested          bool
	Counts          map[string]int64
}

// LegacySource reads one upstream database. Every list method returns at
// most limit rows in key order, starting after the cursor (empty: from the
// start), and the cursor of the last row returned. Cursors are opaque.
type LegacySource interface {
	Info() LegacySourceInfo
	Users(ctx context.Context, after string, limit int) ([]LegacyUser, string, error)
	Libraries(ctx context.Context, after string, limit int) ([]LegacyLibrary, string, error)
	Items(ctx context.Context, after string, limit int) ([]LegacyItem, string, error)
	UserData(ctx context.Context, after string, limit int) ([]LegacyUserData, string, error)
}

// LegacyCategoryReport counts one category. Source equals the sum of every
// other count once the run completed.
type LegacyCategoryReport struct {
	Source    int64            `json:"source"`
	Derived   bool             `json:"derived,omitempty"`
	Inserted  int64            `json:"inserted"`
	Updated   int64            `json:"updated"`
	Matched   int64            `json:"matched"`
	Unchanged int64            `json:"unchanged"`
	Skipped   map[string]int64 `json:"skipped,omitempty"`
	Conflicts map[string]int64 `json:"conflicts,omitempty"`
	Pending   map[string]int64 `json:"pending,omitempty"`
	Digest    string           `json:"digest,omitempty"`
}

func addReason(m *map[string]int64, reason string, n int64) {
	if n == 0 {
		return
	}
	if *m == nil {
		*m = map[string]int64{}
	}
	(*m)[reason] += n
}

// Skip counts n rows left out for reason.
func (c *LegacyCategoryReport) Skip(reason string, n int64) { addReason(&c.Skipped, reason, n) }

// Conflict counts n rows that disagree with the target for reason.
func (c *LegacyCategoryReport) Conflict(reason string, n int64) { addReason(&c.Conflicts, reason, n) }

// Pend counts n rows that a later run may still resolve.
func (c *LegacyCategoryReport) Pend(reason string, n int64) { addReason(&c.Pending, reason, n) }

func sumReasons(m map[string]int64) int64 {
	var n int64
	for _, v := range m {
		n += v
	}
	return n
}

// Accounted is the number of rows with an outcome.
func (c *LegacyCategoryReport) Accounted() int64 {
	return c.Inserted + c.Updated + c.Matched + c.Unchanged + sumReasons(c.Skipped) + sumReasons(c.Conflicts) + sumReasons(c.Pending)
}

// Add merges another count of the same category.
func (c *LegacyCategoryReport) Add(o *LegacyCategoryReport) {
	if o == nil {
		return
	}
	c.Inserted += o.Inserted
	c.Updated += o.Updated
	c.Matched += o.Matched
	c.Unchanged += o.Unchanged
	for k, v := range o.Skipped {
		c.Skip(k, v)
	}
	for k, v := range o.Conflicts {
		c.Conflict(k, v)
	}
	for k, v := range o.Pending {
		c.Pend(k, v)
	}
}

// LegacySample names one conflicting or pending source row. Paths are not
// included; the source ID finds the row upstream.
type LegacySample struct {
	Category string `json:"category"`
	SourceID string `json:"sourceId"`
	Reason   string `json:"reason"`
}

// LegacySourceReport is the source half of the report.
type LegacySourceReport struct {
	File            string           `json:"file"`
	SHA256          string           `json:"sha256"`
	Size            int64            `json:"size"`
	WALSHA256       string           `json:"walSha256,omitempty"`
	LatestMigration string           `json:"latestMigration"`
	Migrations      int              `json:"migrations"`
	Tested          bool             `json:"tested"`
	Tables          map[string]int64 `json:"tables"`
}

// LegacyVerification is the reconciliation done after the last batch.
type LegacyVerification struct {
	RowsBalanced bool             `json:"rowsBalanced"`
	TargetRows   map[string]int64 `json:"targetRows"`
	TargetMatch  bool             `json:"targetMatch"`
}

// LegacyImportReport is the JSON report (docs/legacy-import.md).
type LegacyImportReport struct {
	Schema                string                           `json:"schema"`
	RunID                 string                           `json:"runId,omitempty"`
	State                 string                           `json:"state"`
	Resumed               bool                             `json:"resumed"`
	TargetSchemaVersion   int                              `json:"targetSchemaVersion"`
	Source                LegacySourceReport               `json:"source"`
	PathMap               []LegacyPathRule                 `json:"pathMap"`
	MergeUsers            bool                             `json:"mergeUsers"`
	SkipConflicts         bool                             `json:"skipConflicts"`
	BatchSize             int                              `json:"batchSize"`
	Batches               int64                            `json:"batches"`
	Phases                map[string]string                `json:"phases"`
	Categories            map[string]*LegacyCategoryReport `json:"categories"`
	PasswordResetRequired int64                            `json:"passwordResetRequired"`
	FavoritesDropped      int64                            `json:"favoritesDropped"`
	Conflicts             []LegacySample                   `json:"conflicts"`
	Pending               []LegacySample                   `json:"pending"`
	Verification          *LegacyVerification              `json:"verification,omitempty"`
	Error                 string                           `json:"error,omitempty"`
	StartedAt             time.Time                        `json:"startedAt"`
	FinishedAt            *time.Time                       `json:"finishedAt,omitempty"`
}

// Category returns the report row of category, creating it.
func (r *LegacyImportReport) Category(name string) *LegacyCategoryReport {
	if r.Categories == nil {
		r.Categories = map[string]*LegacyCategoryReport{}
	}
	c := r.Categories[name]
	if c == nil {
		c = &LegacyCategoryReport{}
		r.Categories[name] = c
	}
	return c
}

// Sample records a conflict or pending row within the sample bound.
func (r *LegacyImportReport) Sample(pending bool, category, sourceID, reason string) {
	list := &r.Conflicts
	if pending {
		list = &r.Pending
	}
	if len(*list) < LegacyImportSampleLimit {
		*list = append(*list, LegacySample{Category: category, SourceID: sourceID, Reason: reason})
	}
}

// ConflictTotal is the number of conflicting rows of every category.
func (r *LegacyImportReport) ConflictTotal() int64 {
	var n int64
	for _, c := range r.Categories {
		n += sumReasons(c.Conflicts)
	}
	return n
}

// Balanced reports whether every source category is fully accounted for.
func (r *LegacyImportReport) Balanced() bool {
	for _, name := range LegacyImportCategories() {
		c := r.Categories[name]
		if c == nil || !c.Derived && c.Accounted() != c.Source {
			return false
		}
	}
	return true
}

// ParseLegacyPathRule parses "FROM=TO"; the first "=" separates them.
func ParseLegacyPathRule(s string) (LegacyPathRule, error) {
	from, to, ok := strings.Cut(s, "=")
	from, to = trimPathEnd(from), trimPathEnd(to)
	if !ok || from == "" || to == "" || strings.ContainsFunc(from+to, isControl) {
		return LegacyPathRule{}, ErrInvalid
	}
	return LegacyPathRule{From: from, To: to}, nil
}

func trimPathEnd(p string) string {
	for len(p) > 1 && (p[len(p)-1] == '/' || p[len(p)-1] == '\\') && !(len(p) == 3 && p[1] == ':') {
		p = p[:len(p)-1]
	}
	return p
}

// windowsPath reports a drive-letter or UNC path.
func windowsPath(p string) bool {
	return len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') && (p[0]|0x20 >= 'a' && p[0]|0x20 <= 'z') || strings.HasPrefix(p, `\\`)
}

func pathSeparator(b byte, windows bool) bool { return b == '/' || windows && b == '\\' }

// MapLegacyPath rewrites a source path for a target whose separator is sep.
// The longest matching rule wins; a rule matches at a path component
// boundary, case-insensitively for Windows paths. Without a rule the path
// must already be absolute in the target's form. The result uses sep
// throughout and never contains "." or ".." components.
func MapLegacyPath(rules []LegacyPathRule, p string, sep byte) (string, bool) {
	if p == "" || strings.HasPrefix(p, "%") || strings.ContainsFunc(p, isControl) {
		return "", false
	}
	win := windowsPath(p)
	best, bestLen := -1, 0
	for i, rule := range rules {
		from := rule.From
		if from == "" || len(from) > len(p) || best >= 0 && len(from) <= bestLen {
			continue
		}
		head := p[:len(from)]
		if !(head == from || win && strings.EqualFold(head, from)) {
			continue
		}
		rest := p[len(from):]
		if rest == "" || pathSeparator(rest[0], win) || strings.HasSuffix(from, "/") || win && strings.HasSuffix(from, `\`) {
			best, bestLen = i, len(from)
		}
	}
	out := p
	if best >= 0 {
		out = rules[best].To + string(sep) + strings.TrimLeft(p[len(rules[best].From):], `/\`)
	} else if win != (sep == '\\') {
		return "", false
	}
	return cleanLegacyPath(out, sep, win || sep == '\\')
}

func cleanLegacyPath(p string, sep byte, backslash bool) (string, bool) {
	split := func(r rune) bool { return r == '/' || backslash && r == '\\' }
	var prefix string
	switch {
	case sep == '/' && strings.HasPrefix(p, "/"):
		prefix = "/"
	case sep == '\\' && windowsPath(p) && !strings.HasPrefix(p, `\\`):
		prefix = p[:2] + `\`
		p = p[2:]
	case sep == '\\' && strings.HasPrefix(p, `\\`):
		prefix = `\\`
	default:
		return "", false
	}
	var parts []string
	for _, part := range strings.FieldsFunc(p, split) {
		if part == "." || part == ".." {
			return "", false
		}
		parts = append(parts, part)
	}
	if prefix == `\\` && len(parts) < 2 {
		return "", false
	}
	return prefix + strings.Join(parts, string(sep)), true
}

// LegacyRootRelative splits a mapped path at a library root: the path
// relative to root with "/" separators, "." for the root itself.
func LegacyRootRelative(root, p string, sep byte) (string, bool) {
	if p == root {
		return ".", true
	}
	prefix := root
	if !strings.HasSuffix(prefix, string(sep)) {
		prefix += string(sep)
	}
	if !strings.HasPrefix(p, prefix) || len(p) == len(prefix) {
		return "", false
	}
	rel := p[len(prefix):]
	if sep == '\\' {
		rel = strings.ReplaceAll(rel, `\`, "/")
	}
	return rel, true
}
