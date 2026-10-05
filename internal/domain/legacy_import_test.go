package domain

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMapLegacyPath(t *testing.T) {
	rules := []LegacyPathRule{{From: "/media", To: "/srv/media"}, {From: "/media/tv", To: "/mnt/tv"}, {From: `D:\Media`, To: "/srv/d"},
		{From: `\\nas\share`, To: "/nas"}, {From: "/", To: "/root"}}
	for in, want := range map[string]string{
		"/media/movies/電影 (2001)/a.mkv": "/srv/media/movies/電影 (2001)/a.mkv",
		"/media/tv/Show/S01E01.mkv":     "/mnt/tv/Show/S01E01.mkv", // the longest rule wins
		"/media":                        "/srv/media",
		"/media2/x.mkv":                 "/root/media2/x.mkv", // component boundary: only "/" matches
		`D:\Media\電影\a.mkv`:             "/srv/d/電影/a.mkv",
		`d:\MEDIA\x\y.mkv`:              "/srv/d/x/y.mkv", // Windows paths compare case-insensitively
		`\\nas\share\a b\c.mkv`:         "/nas/a b/c.mkv",
		"/media//a/./b.mkv":             "",
		"/media/a/../b.mkv":             "",
		`E:\Other\a.mkv`:                "", // Windows path without a rule on a POSIX target
		"%AppDataPath%/x":               "",
		"":                              "",
		"relative/a.mkv":                "",
		"/media/a\x00b":                 "",
	} {
		got, ok := MapLegacyPath(rules, in, '/')
		if want == "" && in == "/media//a/./b.mkv" {
			// Empty components collapse, "." is refused.
			if ok {
				t.Fatalf("%q mapped to %q", in, got)
			}
			continue
		}
		if ok != (want != "") || got != want {
			t.Fatalf("%q: got %q %t want %q", in, got, ok, want)
		}
	}
	// An empty rule never matches (and never panics).
	if got, ok := MapLegacyPath([]LegacyPathRule{{From: "", To: "/x"}}, "/a", '/'); !ok || got != "/a" {
		t.Fatalf("empty rule %q %t", got, ok)
	}
	// Without rules POSIX paths pass through cleaned.
	if got, ok := MapLegacyPath(nil, "/a//b/", '/'); !ok || got != "/a/b" {
		t.Fatalf("identity %q %t", got, ok)
	}
	// A Windows target keeps drive and UNC forms and uses backslashes.
	win := []LegacyPathRule{{From: "/media", To: `E:\Media`}}
	for in, want := range map[string]string{"/media/a/b.mkv": `E:\Media\a\b.mkv`, `C:\x/y.mkv`: `C:\x\y.mkv`, `\\srv\s\a.mkv`: `\\srv\s\a.mkv`, `\\srv`: "", "/other/a": ""} {
		if got, ok := MapLegacyPath(win, in, '\\'); ok != (want != "") || got != want {
			t.Fatalf("windows %q: %q %t", in, got, ok)
		}
	}
}

func TestParseLegacyPathRuleAndRelative(t *testing.T) {
	rule, err := ParseLegacyPathRule(`D:\Media\=/srv/media/`)
	if err != nil || rule.From != `D:\Media` || rule.To != "/srv/media" {
		t.Fatalf("%+v %v", rule, err)
	}
	if rule, err = ParseLegacyPathRule(`C:\=/c`); err != nil || rule.From != `C:\` {
		t.Fatalf("drive root %+v %v", rule, err)
	}
	if rule, err = ParseLegacyPathRule("/=/"); err != nil || rule.From != "/" || rule.To != "/" {
		t.Fatalf("root %+v %v", rule, err)
	}
	for _, bad := range []string{"", "/a", "=/b", "/a=", "/a\x01=/b"} {
		if _, err := ParseLegacyPathRule(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
	for _, c := range []struct {
		root, path, want string
		sep              byte
	}{
		{"/srv/m", "/srv/m/a/b.mkv", "a/b.mkv", '/'}, {"/srv/m", "/srv/m", ".", '/'}, {"/srv/m", "/srv/movies/a", "", '/'},
		{"/", "/a/b", "a/b", '/'}, {`E:\M`, `E:\M\a\b.mkv`, "a/b.mkv", '\\'}, {"/srv/m", "/srv/m/", "", '/'},
	} {
		got, ok := LegacyRootRelative(c.root, c.path, c.sep)
		if ok != (c.want != "") || got != c.want {
			t.Fatalf("%+v: %q %t", c, got, ok)
		}
	}
}

func TestLegacyClassification(t *testing.T) {
	for typeName, want := range map[string][2]any{
		"Controller.Entities.Movies.Movie":   {LegacyShapeFile, ""},
		"Controller.Entities.TV.Episode":     {LegacyShapeFile, ""},
		"Controller.Entities.Video":          {LegacyShapeFile, ""},
		"Controller.Entities.TV.Series":      {LegacyShapeFolder, ""},
		"Controller.Entities.TV.Season":      {LegacyShapeFolder, ""},
		"Controller.Entities.Audio.Audio":    {LegacyShapeNone, LegacyReasonRemovedDomain},
		"Controller.Entities.Book":           {LegacyShapeNone, LegacyReasonRemovedDomain},
		"Controller.LiveTv.LiveTvProgram":    {LegacyShapeNone, LegacyReasonRemovedDomain},
		"Controller.Entities.Trailer":        {LegacyShapeNone, LegacyReasonUnsupportedKind},
		"Controller.Entities.Movies.BoxSet":  {LegacyShapeNone, LegacyReasonUnsupportedKind},
		"Controller.Entities.UserRootFolder": {LegacyShapeNone, LegacyReasonStructural},
		"PLACEHOLDER":                        {LegacyShapeNone, LegacyReasonStructural},
		"":                                   {LegacyShapeNone, LegacyReasonStructural},
	} {
		shape, reason := LegacyItemShape(typeName)
		if shape != want[0] || reason != want[1] {
			t.Fatalf("%s: %d %q", typeName, shape, reason)
		}
	}
	movie := LegacyItem{Type: "Controller.Entities.Movies.Movie", Path: "/a.mkv"}
	for item, want := range map[LegacyItem]string{movie: "", {Type: movie.Type, Path: " "}: LegacyReasonNoPath, {Type: movie.Type, Path: "/a", Virtual: true}: LegacyReasonVirtual,
		{Type: movie.Type, Path: "/a", Extra: true}: LegacyReasonExtra, {Type: "X.Audio", Path: "/a"}: LegacyReasonRemovedDomain} {
		if _, reason := item.Classify(); reason != want {
			t.Fatalf("%+v: %q", item, reason)
		}
	}
	for collection, want := range map[string]string{"": "", "movies": "", "TVShows": "", "homevideos": "", "mixed": "", "music": LegacyReasonRemovedDomain,
		"livetv": LegacyReasonRemovedDomain, "books": LegacyReasonRemovedDomain, "boxsets": LegacyReasonUnsupportedType, "playlists": LegacyReasonUnsupportedType} {
		if got := LegacyLibraryOutcome(collection); got != want {
			t.Fatalf("%s: %q", collection, got)
		}
	}
	for kind, want := range map[int]string{0: "", 1: "", 2: "", 16: "", 11: LegacyReasonDownloadRemoved, 6: LegacyReasonRemovedDomain, 7: LegacyReasonNotApplicable} {
		if got := LegacyPermissionOutcome(kind); got != want {
			t.Fatalf("permission %d: %q", kind, got)
		}
	}
	for kind, want := range map[int]string{0: "", 5: "", 10: "", 11: LegacyReasonNotApplicable} {
		if got := LegacyPreferenceOutcome(kind); got != want {
			t.Fatalf("preference %d: %q", kind, got)
		}
	}
	if len(LegacyImportPhases()) != 5 || len(LegacyImportCategories()) != 8 || legacyPermissionKindCount != 24 {
		t.Fatal("catalogues changed")
	}
}

func TestLegacyAccount(t *testing.T) {
	rating, high, bitrate, tiny := 13, 99, int64(25_000_000), int64(10)
	u := LegacyUser{Name: "bob", MaxParentalRating: &rating, MaxActiveSessions: 500, RemoteBitrate: &bitrate,
		Permissions: []LegacyFlag{{LegacyPermAdministrator, true}, {LegacyPermHidden, false}, {LegacyPermDisabled, true}},
		Preferences: []LegacyPreference{{LegacyPrefBlockedTags, " Gore,horror,,GORE," + strings.Repeat("x", 129)}, {LegacyPrefBlockUnrated, "Movie"}}}
	a := u.Account()
	if !a.Admin || a.Hidden || !a.Disabled || *a.ParentalRatingMax != 13 || !*a.BlockUnrated || *a.MaxStreams != 128 || *a.MaxKbps != 25000 ||
		strings.Join(a.BlockedTags, ",") != "gore,horror" {
		t.Fatalf("%+v", a)
	}
	u = LegacyUser{Name: "c", MaxParentalRating: &high, RemoteBitrate: &tiny, Preferences: []LegacyPreference{{LegacyPrefBlockUnrated, ""}}}
	if a = u.Account(); *a.ParentalRatingMax != 21 || a.BlockUnrated != nil || a.MaxStreams != nil || *a.MaxKbps != 1 || a.Admin {
		t.Fatalf("%+v", a)
	}
	if a = (LegacyUser{Name: "d", Preferences: []LegacyPreference{{LegacyPrefBlockUnrated, "Movie"}}}).Account(); a.ParentalRatingMax != nil || a.BlockUnrated != nil || a.MaxKbps != nil {
		t.Fatalf("unrated blocking needs a ceiling: %+v", a)
	}
	for name, ok := range map[string]bool{"Alice": true, "測試ユーザー": true, "": false, " a": false, "a\tb": false, strings.Repeat("長", 43): false, strings.Repeat("長", 42): true, "a\xffb": false} {
		if ValidLegacyName(name) != ok {
			t.Fatalf("%q", name)
		}
	}
	d := LegacyUserData{UserID: "u", ItemID: "i"}
	if !d.Empty() || d.Key() != "u|i" {
		t.Fatal("empty user data")
	}
	if (LegacyUserData{PlayCount: 1}).Empty() || (LegacyUserData{Position: 1}).Empty() || (LegacyUserData{Played: true}).Empty() {
		t.Fatal("progress is not empty")
	}
}

func TestLegacyReportAccounting(t *testing.T) {
	var r LegacyImportReport
	for _, name := range LegacyImportCategories() {
		r.Category(name).Source = 3
	}
	r.Category(LegacyCatLibraryAccess).Derived = true
	if r.Balanced() {
		t.Fatal("nothing accounted yet")
	}
	for _, name := range LegacyImportCategories() {
		c := r.Category(name)
		c.Add(&LegacyCategoryReport{Inserted: 1, Skipped: map[string]int64{"a": 1}, Pending: map[string]int64{"p": 1}})
		c.Add(nil)
		c.Conflict("x", 0)
	}
	if !r.Balanced() || r.ConflictTotal() != 0 {
		t.Fatalf("balanced %+v", r.Categories[LegacyCatUsers])
	}
	r.Category(LegacyCatUsers).Conflict("name_taken", 1)
	if r.Balanced() || r.ConflictTotal() != 1 {
		t.Fatal("an extra conflict unbalances")
	}
	for i := range LegacyImportSampleLimit + 5 {
		r.Sample(i%2 == 0, "users", "id", "r")
	}
	if len(r.Conflicts)+len(r.Pending) != LegacyImportSampleLimit+5 || len(r.Pending) > LegacyImportSampleLimit {
		t.Fatal("samples")
	}
	for range LegacyImportSampleLimit {
		r.Sample(true, "users", "id", "r")
	}
	if len(r.Pending) != LegacyImportSampleLimit {
		t.Fatal("sample bound")
	}
	delete(r.Categories, LegacyCatItems)
	if r.Balanced() {
		t.Fatal("a missing category cannot balance")
	}
}

func legacySchema(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "legacy-import.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(data), "<!-- legacy-import-report-schema -->\n```json\n")
	if !ok {
		t.Fatal("docs/legacy-import.md lacks the report schema block")
	}
	block, _, ok := strings.Cut(rest, "\n```")
	if !ok {
		t.Fatal("unterminated schema block")
	}
	var schema map[string]any
	if err := json.Unmarshal([]byte(block), &schema); err != nil {
		t.Fatal(err)
	}
	return schema
}

func TestLegacyReportMatchesTheDocumentedSchema(t *testing.T) {
	schema := legacySchema(t)
	finished := time.Now().UTC()
	r := LegacyImportReport{Schema: LegacyImportReportSchema, RunID: "00000000-0000-4000-8000-000000000001", State: LegacyStateCompleted, Resumed: true, TargetSchemaVersion: 75,
		Source:  LegacySourceReport{File: "upstream.db", SHA256: "ab", Size: 1, WALSHA256: "cd", LatestMigration: "m", Migrations: 2, Tested: true, Tables: map[string]int64{"Users": 1}},
		PathMap: []LegacyPathRule{{From: "/a", To: "/b"}}, MergeUsers: true, SkipConflicts: true, BatchSize: 10, Batches: 3,
		Phases: map[string]string{}, PasswordResetRequired: 1, FavoritesDropped: 1,
		Conflicts: []LegacySample{{Category: LegacyCatUsers, SourceID: "x", Reason: LegacyReasonNameTaken}}, Pending: []LegacySample{},
		Verification: &LegacyVerification{RowsBalanced: true, TargetRows: map[string]int64{LegacyCatUsers: 1}, TargetMatch: true},
		Error:        "batch_failed", StartedAt: finished, FinishedAt: &finished}
	for i, phase := range LegacyImportPhases() {
		r.Phases[phase] = []string{"pending", "partial", "done", "done", "pending"}[i]
	}
	for _, name := range LegacyImportCategories() {
		c := r.Category(name)
		c.Source, c.Inserted, c.Digest = 2, 1, "ef"
		c.Skip(LegacyReasonEmpty, 1)
		c.Conflict(LegacyReasonNameTaken, 1)
		c.Pend(LegacyReasonNotScanned, 1)
	}
	r.Category(LegacyCatLibraryAccess).Derived = true
	validate := func(report LegacyImportReport) error {
		data, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		var decoded any
		if err = json.Unmarshal(data, &decoded); err != nil {
			t.Fatal(err)
		}
		return validateSchema(schema, schema, decoded, "report")
	}
	if err := validate(r); err != nil {
		t.Fatal(err)
	}
	// Every state and error code the code produces is documented; an empty
	// category and a preflight report without run or verification pass too.
	for _, state := range []string{LegacyStatePreflight, LegacyStateRefused, LegacyStatePaused, LegacyStateFailed, LegacyStateCompleted} {
		for _, code := range []string{"batch_failed", "cancelled", ErrLegacySourceUnsupported.Error(), ""} {
			probe := r
			probe.State, probe.Error, probe.RunID, probe.Verification, probe.FinishedAt = state, code, "", nil, nil
			probe.Categories = map[string]*LegacyCategoryReport{LegacyCatUsers: {}}
			if err := validate(probe); err != nil {
				t.Fatalf("%s %s: %v", state, code, err)
			}
		}
	}
	// The validator can fail.
	bad := r
	bad.State = "running"
	if validate(bad) == nil {
		t.Fatal("an undocumented state passed")
	}
	bad = r
	bad.Categories = map[string]*LegacyCategoryReport{"favorites": {}}
	if validate(bad) == nil {
		t.Fatal("an undocumented category passed")
	}
	bad = r
	bad.Phases = map[string]string{LegacyPhaseUsers: "running"}
	if validate(bad) == nil {
		t.Fatal("an undocumented phase state passed")
	}
}
