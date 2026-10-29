package nfo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestLexicalPathCodeClassifiesIllegalRelativePaths(t *testing.T) {
	for _, tc := range []struct{ path, want string }{
		{"movie.nfo", ""},
		{"a/b/Movie [2024].nfo", ""},
		{"系列/片名.nfo", ""},
		{strings.Repeat("x", 255), ""},
		{strings.Repeat("x", 256), domain.NFOPathTooLong},
		{"a/" + strings.Repeat("y", 256) + "/m.nfo", domain.NFOPathTooLong},
		{strings.Repeat("a/", 600) + "m.nfo", domain.NFOPathTooLong},
		{"/etc/movie.nfo", domain.NFOPathOutsideRoot},
		{"../movie.nfo", domain.NFOPathOutsideRoot},
		{"a/../../movie.nfo", domain.NFOPathOutsideRoot},
		{"a//movie.nfo", domain.NFOPathInvalidName},
		{"a/movie.nfo/", domain.NFOPathInvalidName},
		{"", domain.NFOPathInvalidName},
		{".", domain.NFOPathInvalidName},
		{`a\movie.nfo`, domain.NFOPathInvalidName},
		{"a:b/movie.nfo", domain.NFOPathInvalidName},
		{"a\nb.nfo", domain.NFOPathInvalidName},
		{string([]byte{'a', 0xff}) + ".nfo", domain.NFOPathInvalidName},
	} {
		if got := lexicalPathCode(tc.path); got != tc.want {
			t.Errorf("lexicalPathCode(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestPathRefusalIsStillUnavailableToCallersThatDoNotAskWhy(t *testing.T) {
	for _, code := range domain.NFOPathCodes {
		err := pathIssue(code)
		if !errors.Is(err, domain.ErrNFOInputUnavailable) || pathIssueCode(err) != code || errors.Is(err, domain.ErrNFOSourceChanged) {
			t.Fatalf("%s: %v", code, err)
		}
		wrapped := errors.Join(errors.New("context"), err)
		if pathIssueCode(wrapped) != code || !errors.Is(wrapped, domain.ErrNFOInputUnavailable) {
			t.Fatalf("%s: a wrapped refusal lost its reason", code)
		}
		if strings.Contains(err.Error(), string(os.PathSeparator)+"tmp") {
			t.Fatal("a refusal names a path")
		}
	}
	if pathIssueCode(domain.ErrNFOInputUnavailable) != "" || pathIssueCode(nil) != "" {
		t.Fatal("an ordinary refusal has a reason")
	}
}

func TestInspectNFOPathLeavesOrdinaryAndMissingFilesAlone(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "dir", "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dir", "sub", "movie.nfo"), []byte("<movie/>"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, relative := range []string{"dir/sub/movie.nfo", "dir/sub/absent.nfo", "dir/nodir/movie.nfo", "absent.nfo"} {
		if code, err := InspectNFOPath(ctx, root, relative); err != nil || code != "" {
			t.Fatalf("%s: %q %v", relative, code, err)
		}
	}
	// An unusable root is the ordinary read's problem, not a path issue.
	for _, unusable := range []string{"relative/root", filepath.Join(root, "absent"), filepath.Join(root, "dir", "sub", "movie.nfo")} {
		if code, err := InspectNFOPath(ctx, unusable, "dir/sub/movie.nfo"); err != nil || code != "" {
			t.Fatalf("%s: %q %v", unusable, code, err)
		}
	}
	if code, err := InspectNFOPath(ctx, root, "../movie.nfo"); err != nil || code != domain.NFOPathOutsideRoot {
		t.Fatalf("escape: %q %v", code, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := InspectNFOPath(cancelled, root, "dir/sub/movie.nfo"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
	if _, err := InspectNFOPath(nil, root, "x.nfo"); !errors.Is(err, domain.ErrInvalid) { //nolint:staticcheck // a nil context is the case under test
		t.Fatalf("nil context: %v", err)
	}
}

func TestSummaryReaderInspectNFOPath(t *testing.T) {
	reader, err := NewSummaryReader(domain.NFODefaultSourceBytes)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if code, err := reader.InspectNFOPath(context.Background(), domain.NFOSource{RootPath: root, RelativePath: "../x.nfo"}); err != nil || code != domain.NFOPathOutsideRoot {
		t.Fatalf("%q %v", code, err)
	}
	if code, err := reader.InspectNFOPath(context.Background(), domain.NFOSource{RootPath: root, RelativePath: "x.nfo"}); err != nil || code != "" {
		t.Fatalf("%q %v", code, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := reader.InspectNFOPath(cancelled, domain.NFOSource{RootPath: root, RelativePath: "x.nfo"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	var none *SummaryReader
	if _, err := none.InspectNFOPath(context.Background(), domain.NFOSource{}); !errors.Is(err, domain.ErrNFOReaderUnavailable) {
		t.Fatal(err)
	}
}

func writeItemNFO(t *testing.T, scope domain.NFOItemScope, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(scope.Source.RootPath, filepath.FromSlash(scope.Source.RelativePath)), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestValidateItemNFOReportsWhatTheValidationJobWouldStore(t *testing.T) {
	reader, scope := itemSelectionFixture(t, "Movie")
	ctx := context.Background()
	missing, err := reader.ValidateItemNFO(ctx, scope)
	if err != nil || missing.Status != domain.NFOItemValidationMissing || missing.PathCode != "" || len(missing.IssueCodes) != 0 {
		t.Fatalf("missing: %+v %v", missing, err)
	}
	for _, tc := range []struct {
		name, body string
		status     string
		failure    domain.NFOFailureCode
		errors     int64
		codes      []string
		lockData   bool
		locks      []string
	}{
		{name: "clean", body: `<movie><title>T</title><uniqueid type="imdb">tt1</uniqueid></movie>`, status: domain.NFOStatusValid},
		{name: "title missing, no id", body: `<movie><plot>P</plot></movie>`, status: domain.NFOStatusValid, codes: []string{"nfo_title_missing"}},
		{name: "no id is not an issue", body: `<movie><title>T</title></movie>`, status: domain.NFOStatusValid},
		{name: "incomplete id is not missing", body: `<movie><title>T</title><imdbid></imdbid></movie>`, status: domain.NFOStatusInvalid, errors: 1, codes: []string{"nfo_id_incomplete"}},
		{name: "invalid date", body: `<movie><title>T</title><uniqueid type="x">1</uniqueid><premiered>last week</premiered></movie>`, status: domain.NFOStatusInvalid, errors: 1, codes: []string{"nfo_invalid_date"}},
		{name: "unknown root", body: `<thing><title>T</title></thing>`, status: domain.NFOStatusValid, codes: []string{"nfo_unknown_root"}},
		{name: "invalid xml", body: `<movie><title>broken`, status: domain.NFOStatusInvalid, failure: domain.NFOFailureInvalidXML},
		{name: "locks", body: `<movie><title>T</title><lockdata>true</lockdata><lockedfields>Name|Genres</lockedfields></movie>`, status: domain.NFOStatusValid, lockData: true, locks: []string{"Name", "Genres"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeItemNFO(t, scope, tc.body)
			got, err := reader.ValidateItemNFO(ctx, scope)
			if err != nil || got.Status != domain.NFOItemValidationChecked || got.Summary.Status != tc.status || got.Summary.FailureCode != tc.failure || got.Summary.ErrorCount != tc.errors {
				t.Fatalf("%+v %v", got, err)
			}
			if !reflect.DeepEqual(got.IssueCodes, tc.codes) && !(len(got.IssueCodes) == 0 && len(tc.codes) == 0) {
				t.Fatalf("issue codes %v, want %v", got.IssueCodes, tc.codes)
			}
			if got.LockData != tc.lockData || !reflect.DeepEqual(got.LockedFields, tc.locks) && !(len(got.LockedFields) == 0 && len(tc.locks) == 0) {
				t.Fatalf("locks %v %v", got.LockData, got.LockedFields)
			}
			// The summary is exactly what the validation job stores for the file.
			if domain.ValidateNFOSummary(got.Summary) != nil {
				t.Fatal("the summary is not storable")
			}
		})
	}
}

func TestValidateItemNFOKeepsCodesBeyondTheStoredPrefix(t *testing.T) {
	reader, scope := itemSelectionFixture(t, "Movie")
	// 70 bad years come before the title check, which the
	// stored prefix of 64 issues therefore cuts off.
	writeItemNFO(t, scope, "<movie>"+strings.Repeat("<year>x</year>", 70)+"</movie>")
	got, err := reader.ValidateItemNFO(context.Background(), scope)
	if err != nil || !got.Summary.IssuesTruncated || !got.Has("nfo_title_missing") || !got.Has("nfo_invalid_integer") {
		t.Fatalf("%+v %v", got, err)
	}
	for _, issue := range got.Summary.Issues {
		if issue.Code == "nfo_title_missing" {
			t.Fatal("the fixture no longer truncates the title issue")
		}
	}
}

func TestValidateItemNFORejectsBadInput(t *testing.T) {
	reader, scope := itemSelectionFixture(t, "Movie")
	if _, err := reader.ValidateItemNFO(nil, scope); !errors.Is(err, domain.ErrInvalid) { //nolint:staticcheck // a nil context is the case under test
		t.Fatal(err)
	}
	bad := scope
	bad.MediaPath = "../escape.mkv"
	if _, err := reader.ValidateItemNFO(context.Background(), bad); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal(err)
	}
	var none *SummaryReader
	if _, err := none.ValidateItemNFO(context.Background(), scope); !errors.Is(err, domain.ErrNFOReaderUnavailable) {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := reader.ValidateItemNFO(cancelled, scope); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// The media file of the item is gone: not a path issue, an error.
	if err := os.Remove(filepath.Join(scope.Source.RootPath, "folder", "Film.mkv")); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ValidateItemNFO(context.Background(), scope); !errors.Is(err, domain.ErrNFOInputUnavailable) || pathIssueCode(err) != "" {
		t.Fatal(err)
	}
}

func TestNoIdentifierIsNotAnIssue(t *testing.T) {
	for _, body := range []string{
		`<movie><title>T</title></movie>`,
		`<tvshow><title>T</title></tvshow>`,
		`<season><seasonname>T</seasonname></season>`,
		`<episodedetails><title>T</title><season>1</season><episode>1</episode></episodedetails>`,
		`<movie><title>T</title><uniqueid type="jelee">3f2504e0-4f89-41d3-9a0c-0305e82c3301</uniqueid></movie>`,
		`<movie><title>T</title><imdbid>tt1</imdbid></movie>`,
	} {
		if document := mustRead(t, []byte(body)); len(document.Issues) != 0 {
			t.Errorf("%s: %v", body, document.Issues)
		}
	}
}
