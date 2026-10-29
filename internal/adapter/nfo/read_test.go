package nfo

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf16"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

const goldenMovie = `<?xml version="1.0" encoding="UTF-8"?>
<movie source="hand-edited" xmlns:custom="urn:example:metadata">
  <!-- keep this exact comment and order -->
  <title>原始 &amp; Original</title><originaltitle>Original title</originaltitle><sorttitle>Sort title</sorttitle>
  <plot><![CDATA[<p>Untrusted <script>alert(1)</script> markup</p>]]></plot><outline>Outline</outline><tagline>Tagline</tagline>
  <year>2024</year><premiered>2024-02-29</premiered><dateadded>2024-03-01 12:00:00</dateadded><runtime>92 min</runtime>
  <mpaa>PG-13</mpaa><certification>General</certification>
  <genre>Drama / Mystery</genre><genre>Animation</genre><tag>Favorite / Reviewed</tag>
  <studio>Studio A / Studio B</studio><country>JP / TW</country><language>ja / zh</language>
  <director>Director A / Director B</director><writer>Writer A</writer><credits>Writer B / Writer C</credits><producer>Producer A</producer>
  <actor><name>Performer</name><role>Lead</role><thumb>people/actor.jpg</thumb><order>1</order></actor>
  <uniqueid type="IMDB" default="true">tt1234567</uniqueid><uniqueid type="tmdb">123</uniqueid><tvdbid>456</tvdbid>
  <rating>8,5</rating><userrating>9</userrating>
  <ratings><rating name="imdb" max="10" default="true"><value>8.5</value><votes>1234</votes></rating><rating name="critic" max="100"><value>95</value></rating></ratings>
  <lockdata>true</lockdata><lockedfields>Name|Overview|Genres</lockedfields>
  <set custom-set="preserve"><name>Collection A</name><overview>Collection plot</overview></set>
  <thumb aspect="poster" season="1" preview="preview.jpg">poster.jpg</thumb>
  <fanart><thumb preview="preview1.jpg">fanart.jpg</thumb><thumb>fanart2.jpg</thumb></fanart>
  <art><clearlogo>logo.png</clearlogo><landscape>landscape.jpg</landscape></art>
  <trailer>https://example.invalid/trailer</trailer>
  <custom:extension custom-attribute="keep"><title>Do not map this title</title></custom:extension>
  <unrecognized><![CDATA[ untouched <raw> ]]></unrecognized>
</movie>`

func mustRead(t *testing.T, original []byte) *Document {
	t.Helper()
	document, err := Read(context.Background(), bytes.NewReader(original), DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func TestGoldenMovieMapsFieldsAndRetainsOriginalBytes(t *testing.T) {
	original := append([]byte{0xef, 0xbb, 0xbf}, []byte(strings.ReplaceAll(goldenMovie, "\n", "\r\n"))...)
	document := mustRead(t, original)
	metadata := document.Metadata
	if document.Root != "movie" || document.Encoding != "UTF-8" || document.OriginalSize != int64(len(original)) || len(document.Entries) != 1 {
		t.Fatalf("document identity incorrect: %#v", document)
	}
	if metadata.Title != "原始 & Original" || metadata.OriginalTitle != "Original title" || metadata.SortTitle != "Sort title" || metadata.Plot != "<p>Untrusted <script>alert(1)</script> markup</p>" || metadata.Outline != "Outline" || metadata.Tagline != "Tagline" {
		t.Fatalf("text mapping mismatch: %#v", metadata)
	}
	if metadata.Year == nil || *metadata.Year != 2024 || metadata.RuntimeMinutes == nil || *metadata.RuntimeMinutes != 92 || metadata.LockData == nil || !*metadata.LockData {
		t.Fatal("year/runtime/lock mapping failed")
	}
	for name, pair := range map[string][2][]string{
		"genres": {metadata.Genres, {"Drama", "Mystery", "Animation"}}, "tags": {metadata.Tags, {"Favorite", "Reviewed"}},
		"studios": {metadata.Studios, {"Studio A", "Studio B"}}, "countries": {metadata.Countries, {"JP", "TW"}},
		"languages": {metadata.Languages, {"ja", "zh"}}, "directors": {metadata.Directors, {"Director A", "Director B"}},
		"writers": {metadata.Writers, {"Writer A", "Writer B", "Writer C"}}, "locked": {metadata.LockedFields, {"Name", "Overview", "Genres"}},
	} {
		if !reflect.DeepEqual(pair[0], pair[1]) {
			t.Errorf("%s mapping=%v want=%v", name, pair[0], pair[1])
		}
	}
	if len(metadata.UniqueIDs) != 3 || metadata.UniqueIDs[0] != (UniqueID{Type: "imdb", Value: "tt1234567", Default: true}) || metadata.UniqueIDs[2].Type != "tvdb" {
		t.Fatalf("IDs incorrect: %v", metadata.UniqueIDs)
	}
	if len(metadata.Actors) != 1 || metadata.Actors[0].Name != "Performer" || metadata.Actors[0].Role != "Lead" || *metadata.Actors[0].Order != 1 {
		t.Fatalf("actor mapping=%v", metadata.Actors)
	}
	if len(metadata.Art) != 5 || metadata.Art[0].Kind != "poster" || *metadata.Art[0].Season != 1 || metadata.Art[1].Kind != "fanart" || metadata.Art[3].Kind != "clearlogo" {
		t.Fatalf("art mapping=%v", metadata.Art)
	}
	if metadata.Collection != "Collection A" || metadata.CollectionOverview != "Collection plot" || len(metadata.Ratings) != 2 || *metadata.Rating != 8.5 || *metadata.Ratings[1].Value != 95 || *metadata.Ratings[1].Max != 100 {
		t.Fatal("collection or rating mapping failed")
	}
	if issues := document.Validate(); len(issues) != 0 {
		t.Fatalf("valid golden NFO has issues: %v", issues)
	}
	// The metadata view is deliberately independent of original-copy export.
	document.Metadata.Title = "Edited view must not rewrite the file"
	var copied bytes.Buffer
	if err := document.WriteOriginal(context.Background(), &copied); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(copied.Bytes(), original) {
		t.Fatal("original export changed comments, attributes, unknown tags, encoding, or whitespace")
	}
}

func TestSeriesSeasonEpisodeAndWrappers(t *testing.T) {
	for _, tc := range []struct {
		name, xml, root, title string
		entries                int
	}{
		{"series", `<tvshow><title>Series</title><status>Continuing</status><season>3</season><episode>24</episode></tvshow>`, "tvshow", "Series", 1},
		{"season", `<season><seasonname>Season zero</seasonname><seasonnumber>0</seasonnumber><poster>season00-poster.jpg</poster></season>`, "season", "Season zero", 1},
		{"episode", `<episode><title>Special</title><season>0</season><episode>1</episode><aired>2024-01-02</aired></episode>`, "episode", "Special", 1},
		{"episode_details", `<episodedetails><title>Episode</title><season>2</season><episode>7</episode><displayseason>3</displayseason><displayepisode>1</displayepisode><showtitle>Series</showtitle></episodedetails>`, "episodedetails", "Episode", 1},
		{"multi_episode", `<episodedetails><title>Part two</title><season>1</season><episode>2</episode></episodedetails><!-- boundary --><episodedetails><title>Part one</title><season>1</season><episode>1</episode></episodedetails>`, "episodedetails", "Part two", 2},
		{"root_wrapper", `<root><movie><title>Movie</title></movie></root>`, "root", "Movie", 1},
		{"item_wrapper", `<Item><movie><title>Movie</title></movie></Item>`, "item", "Movie", 1},
		{"nested_wrapper", `<MediaBrowser><root><Item><tvshow><title>Series</title></tvshow></Item></root></MediaBrowser>`, "mediabrowser", "Series", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			document := mustRead(t, []byte(tc.xml))
			if document.Root != tc.root || document.Metadata.Title != tc.title || len(document.Entries) != tc.entries {
				t.Fatalf("mapped document=%#v", document)
			}
			if issues := document.Validate(); len(issues) != 0 {
				t.Fatalf("unexpected issues=%v", issues)
			}
			if tc.entries == 2 && (*document.Entries[0].Episode != 2 || *document.Entries[1].Episode != 1) {
				t.Fatal("multi-episode source order was changed")
			}
			summary, err := projectSummary(context.Background(), document)
			wantRoot := tc.root
			if strings.HasSuffix(tc.name, "_wrapper") {
				wantRoot = "wrapper"
			}
			if err != nil || summary.Root != wantRoot || summary.Entries != tc.entries || summary.Status != domain.NFOStatusValid {
				t.Fatalf("normalized summary: %+v, %v", summary, err)
			}
		})
	}
}

func TestUnknownAndAmbiguousRootsProduceWarnings(t *testing.T) {
	for _, tc := range []struct{ xml, code, title string }{
		{`<customroot><title>Retained</title></customroot>`, "nfo_unknown_root", "Retained"},
		{`<Item><title>Legacy item</title></Item>`, "nfo_kind_unknown", "Legacy item"},
		{`<root><title>Wrapper title</title><movie><title>Movie title</title></movie></root>`, "nfo_wrapper_fields_ignored", "Movie title"},
	} {
		document := mustRead(t, []byte(tc.xml))
		if document.Metadata.Title != tc.title || !hasIssue(document, tc.code, "warning") {
			t.Fatalf("unknown/wrapped root findings=%v", document.Issues)
		}
	}
}

func encodeUTF16(value string, little, bom bool) []byte {
	var order binary.ByteOrder = binary.BigEndian
	if little {
		order = binary.LittleEndian
	}
	var result []byte
	if bom {
		if little {
			result = append(result, 0xff, 0xfe)
		} else {
			result = append(result, 0xfe, 0xff)
		}
	}
	for _, unit := range utf16.Encode([]rune(value)) {
		var pair [2]byte
		order.PutUint16(pair[:], unit)
		result = append(result, pair[:]...)
	}
	return result
}

func TestEncodingsPreserveBytesAndText(t *testing.T) {
	utfXML := `<?xml version="1.0" encoding="UTF-16"?><movie><title>中文 🎬</title></movie>`
	gbk := append([]byte(`<?xml version="1.0" encoding="GBK"?><movie><title>`), 0xd6, 0xd0, 0xce, 0xc4)
	gbk = append(gbk, []byte(`</title></movie>`)...)
	gbkGuessed := append([]byte(`<movie><title>`), 0xd6, 0xd0, 0xce, 0xc4)
	gbkGuessed = append(gbkGuessed, []byte(`</title></movie>`)...)
	for _, tc := range []struct {
		name, encoding, title string
		data                  []byte
		guessed               bool
	}{
		{"utf16le", "UTF-16LE", "中文 🎬", encodeUTF16(utfXML, true, true), false},
		{"utf16be", "UTF-16BE", "中文 🎬", encodeUTF16(utfXML, false, true), false},
		{"utf16_no_bom", "UTF-16LE", "中文 🎬", encodeUTF16(utfXML, true, false), true},
		{"gbk", "GBK", "中文", gbk, false},
		{"gbk_guessed", "GBK", "中文", gbkGuessed, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			document := mustRead(t, tc.data)
			if document.Encoding != tc.encoding || document.Metadata.Title != tc.title || hasIssue(document, "nfo_encoding_guessed", "warning") != tc.guessed {
				t.Fatalf("encoding result=%#v", document)
			}
			var output bytes.Buffer
			if err := document.WriteOriginal(context.Background(), &output); err != nil || !bytes.Equal(output.Bytes(), tc.data) {
				t.Fatalf("encoded roundtrip failed: %v", err)
			}
		})
	}
}

func TestInvalidAndUnsafeXMLIsRejected(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
		want error
	}{
		{"empty", nil, ErrInvalidXML},
		{"unclosed", []byte(`<movie><title>bad</movie>`), ErrInvalidXML},
		{"unknown_entity", []byte(`<movie><title>&external;</title></movie>`), ErrInvalidXML},
		{"external_entity", []byte(`<!DOCTYPE movie [<!ENTITY file SYSTEM "file:///private/secret">]><movie><title>&file;</title></movie>`), ErrUnsafeXML},
		{"entity_bomb", []byte(`<!DOCTYPE movie [<!ENTITY a "x"><!ENTITY b "&a;&a;">]><movie><title>&b;</title></movie>`), ErrUnsafeXML},
		{"stylesheet", []byte(`<?xml-stylesheet href="https://example.invalid/x"?><movie/>`), ErrUnsafeXML},
		{"duplicate_attribute", []byte(`<movie source="one" source="two"/>`), ErrInvalidXML},
		{"trailing_text", []byte(`<movie/>https://example.invalid/id`), ErrInvalidXML},
		{"multiple_movies", []byte(`<movie/><movie/>`), ErrInvalidXML},
		{"invalid_utf8_declared", append([]byte(`<?xml version="1.0" encoding="UTF-8"?><movie>`), 0xff), ErrInvalidEncoding},
		{"unsupported_encoding", []byte(`<?xml version="1.0" encoding="ISO-8859-1"?><movie/>`), ErrUnsupportedEncoding},
		{"invalid_gbk", []byte{'<', 'm', 'o', 'v', 'i', 'e', '>', 0x81}, ErrInvalidEncoding},
		{"utf16_odd", []byte{0xff, 0xfe, '<'}, ErrInvalidEncoding},
		{"utf16_unpaired", []byte{0xff, 0xfe, 0x00, 0xd8}, ErrInvalidEncoding},
		{"encoding_mismatch", encodeUTF16(`<?xml version="1.0" encoding="UTF-8"?><movie/>`, true, true), ErrInvalidEncoding},
		{"long_utf16_mismatch", []byte(`<?xml version="1.0"` + strings.Repeat(" ", 1100) + `encoding="UTF-16"?><movie><title>ASCII</title></movie>`), ErrTooComplex},
		{"bom_long_gbk_mismatch", append([]byte{0xef, 0xbb, 0xbf}, []byte(`<?xml version="1.0"`+strings.Repeat(" ", 1100)+`encoding="GBK"?><movie><title>ASCII</title></movie>`)...), ErrTooComplex},
		{"bom_short_gbk_mismatch", append([]byte{0xef, 0xbb, 0xbf}, []byte(`<?xml version="1.0" encoding="GBK"?><movie/>`)...), ErrInvalidEncoding},
		{"duplicate_declaration", []byte(`<?xml version="1.0"?><?xml version="1.0"?><movie/>`), ErrInvalidXML},
		{"malformed_declaration", []byte(`<?xml nonsense?><movie/>`), ErrInvalidXML},
		{"depth", []byte(`<movie>` + strings.Repeat(`<unknown>`, 65) + strings.Repeat(`</unknown>`, 65) + `</movie>`), ErrTooComplex},
		{"elements", []byte(`<movie>` + strings.Repeat(`<unknown/>`, maxElements) + `</movie>`), ErrTooComplex},
		{"entries", []byte(strings.Repeat(`<episodedetails/>`, maxEntries+1)), ErrTooComplex},
	} {
		t.Run(tc.name, func(t *testing.T) {
			document, err := Read(context.Background(), bytes.NewReader(tc.data), DefaultMaxBytes)
			if !errors.Is(err, tc.want) || document != nil {
				t.Fatalf("err=%v want=%v document=%v", err, tc.want, document)
			}
			if err != nil && (strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "example.invalid")) {
				t.Fatal("parser error leaked source values")
			}
		})
	}
}

func TestSemanticValidationReportsInvalidFieldsWithoutRewriting(t *testing.T) {
	original := []byte(`<episodedetails><year>not-year</year><season>-1</season><episode>oops</episode><rating>NaN</rating><runtime>-10</runtime><lockdata>maybe</lockdata><aired>2024-02-30</aired><uniqueid type="tmdb">1</uniqueid><uniqueid type="tmdb">2</uniqueid><thumb>../private.jpg</thumb><art><poster>file:///private/image</poster></art><actor><name>Person</name><thumb>C:\private\image.jpg</thumb></actor></episodedetails>`)
	document := mustRead(t, original)
	for _, expected := range []struct{ code, severity string }{{"nfo_invalid_integer", "error"}, {"nfo_invalid_number", "error"}, {"nfo_invalid_boolean", "error"}, {"nfo_invalid_date", "error"}, {"nfo_unsafe_reference", "error"}, {"nfo_title_missing", "warning"}, {"nfo_season_missing", "warning"}, {"nfo_episode_missing", "warning"}, {"nfo_conflicting_id", "warning"}} {
		if !hasIssue(document, expected.code, expected.severity) {
			t.Errorf("missing finding %s/%s", expected.code, expected.severity)
		}
	}
	copyIssues := document.Validate()
	copyIssues[0].Code = "changed"
	if document.Issues[0].Code == "changed" {
		t.Fatal("validation result aliases issue storage")
	}
	var output bytes.Buffer
	if err := document.WriteOriginal(context.Background(), &output); err != nil || !bytes.Equal(original, output.Bytes()) {
		t.Fatal("invalid metadata was silently repaired")
	}
}

func hasIssue(document *Document, code, severity string) bool {
	for _, issue := range document.Issues {
		if issue.Code == code && issue.Severity == severity {
			return true
		}
	}
	return false
}

func TestReaderSizeAndCancellationBounds(t *testing.T) {
	original := []byte(`<movie><title>Title</title></movie>`)
	if _, err := Read(context.Background(), bytes.NewReader(original), int64(len(original))); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(context.Background(), bytes.NewReader(original), int64(len(original)-1)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("size limit err=%v", err)
	}
	for _, limit := range []int64{0, -1, MaxAllowedBytes + 1} {
		if _, err := Read(context.Background(), bytes.NewReader(original), limit); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("invalid limit err=%v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Read(ctx, panicReader{}, DefaultMaxBytes); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel err=%v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	reader := &cancelReader{cancel: cancel, content: original}
	if _, err := Read(ctx, reader, DefaultMaxBytes); !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-read cancellation err=%v", err)
	}
	if _, err := Read(context.Background(), failureReader{}, DefaultMaxBytes); !errors.Is(err, ErrRead) || strings.Contains(err.Error(), "private") {
		t.Fatalf("read failure leaked: %v", err)
	}
}

type panicReader struct{}

func (panicReader) Read([]byte) (int, error) { panic("cancelled read must not touch reader") }

type failureReader struct{}

func (failureReader) Read([]byte) (int, error) { return 0, errors.New("/private/secret") }

type cancelReader struct {
	cancel  context.CancelFunc
	content []byte
}

func (r *cancelReader) Read(target []byte) (int, error) {
	r.cancel()
	return copy(target, r.content), io.EOF
}

type shortWriter struct{}

func (shortWriter) Write(data []byte) (int, error) { return len(data) - 1, nil }

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("/private/destination") }

func TestOriginalCopyReportsFailuresAndCancellation(t *testing.T) {
	document := mustRead(t, []byte(`<movie><title>Title</title></movie>`))
	if err := document.WriteOriginal(context.Background(), shortWriter{}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write err=%v", err)
	}
	if err := document.WriteOriginal(context.Background(), failingWriter{}); !errors.Is(err, ErrWrite) || strings.Contains(err.Error(), "private") {
		t.Fatalf("write failure leaked: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var output bytes.Buffer
	if err := document.WriteOriginal(ctx, &output); !errors.Is(err, context.Canceled) || output.Len() != 0 {
		t.Fatalf("cancelled export wrote bytes: %v", err)
	}
}

func TestArtworkReferencesNeverTriggerNetworkRequests(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	document := mustRead(t, []byte(`<movie><title>Title</title><thumb>`+server.URL+`/poster.jpg</thumb><actor><name>Actor</name><thumb>`+server.URL+`/actor.jpg</thumb></actor></movie>`))
	if len(document.Metadata.Art) != 1 || requests.Load() != 0 {
		t.Fatal("metadata parsing performed network access")
	}
}

func TestReadFileBoundaryAndOriginalProtection(t *testing.T) {
	root := t.TempDir()
	original := []byte(goldenMovie)
	if err := os.WriteFile(filepath.Join(root, "MOVIE.NFO"), original, 0o600); err != nil {
		t.Fatal(err)
	}
	document, err := ReadFile(context.Background(), root, "MOVIE.NFO", DefaultMaxBytes)
	if err != nil || document.Metadata.Title != "原始 & Original" {
		t.Fatalf("file parse err=%v", err)
	}
	after, err := os.ReadFile(filepath.Join(root, "MOVIE.NFO"))
	if err != nil || !bytes.Equal(after, original) {
		t.Fatal("source NFO changed")
	}
	for _, relative := range []string{"", ".", "..", "../MOVIE.NFO", "/etc/passwd", `C:\private`, `sub\file.nfo`, "MOVIE.NFO:stream", "missing.nfo"} {
		if _, err := ReadFile(context.Background(), root, relative, DefaultMaxBytes); !errors.Is(err, ErrNotFound) || strings.Contains(err.Error(), root) {
			t.Fatalf("unsafe path result=%v", err)
		}
	}
	if _, err := ReadFile(context.Background(), root, "MOVIE.NFO", 1); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("file size bypass=%v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "directory.nfo"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(context.Background(), root, "directory.nfo", DefaultMaxBytes); !errors.Is(err, ErrNotFound) {
		t.Fatalf("directory accepted: %v", err)
	}
	t.Run("symlink_escape", func(t *testing.T) {
		outside := t.TempDir()
		if err := os.WriteFile(filepath.Join(outside, "outside.nfo"), original, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(outside, "outside.nfo"), filepath.Join(root, "escape.nfo")); err != nil {
			t.Skipf("symlink creation unavailable: %v", err)
		}
		if _, err := ReadFile(context.Background(), root, "escape.nfo", DefaultMaxBytes); !errors.Is(err, ErrNotFound) {
			t.Fatalf("symlink escaped root: %v", err)
		}
	})
}

func TestNFOFilenameRecognitionAndMixedText(t *testing.T) {
	for _, name := range []string{"movie.nfo", "TVSHOW.NFO", "Season.Nfo", "Film (2024).nfo", "Series S01E01.nfo"} {
		if !IsNFOName(name) {
			t.Errorf("valid NFO filename rejected: %s", name)
		}
	}
	for _, name := range []string{".nfo", "movie.nfo.bak", "../movie.nfo", `folder\movie.nfo`, "movie.xml", ""} {
		if IsNFOName(name) {
			t.Errorf("invalid NFO filename accepted: %s", name)
		}
	}
	document := mustRead(t, []byte(`<movie><title>Title</title><plot>Before <b>bold</b> after</plot><unknown><title>Hidden title</title></unknown></movie>`))
	if document.Metadata.Title != "Title" || document.Metadata.Plot != "Before bold after" {
		t.Fatalf("mixed text mapping=%v", document.Metadata)
	}
}

func FuzzReadRetainsAcceptedOriginal(f *testing.F) {
	for _, seed := range []string{`<movie><title>T</title></movie>`, `<episode/>`, `<!DOCTYPE movie><movie/>`, `\x00`, `<root><season><seasonnumber>0</seasonnumber></season></root>`} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 128<<10 {
			return
		}
		document, err := Read(context.Background(), bytes.NewReader(data), 128<<10)
		if err != nil {
			return
		}
		var output bytes.Buffer
		if err := document.WriteOriginal(context.Background(), &output); err != nil || !bytes.Equal(output.Bytes(), data) {
			t.Fatal("accepted original was not retained byte for byte")
		}
	})
}
