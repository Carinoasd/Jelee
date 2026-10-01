package nfo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

func summaryFixture(t *testing.T, data []byte) (app.NFOReadSource, domain.NFOSource) {
	t.Helper()
	root, name := sourceFixture(t, data)
	reader, err := NewSummaryReader(DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	path := domain.NFOSource{RootPath: root, RelativePath: name}
	source, err := reader.Read(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if stamp := source.Stamp(); stamp.Size != int64(len(data)) || stamp.SHA256 != sourceDigest(data) || stamp.FingerprintVersion != SourceFingerprintVersion {
		t.Fatal("bridge stamp did not describe the retained original bytes")
	}
	return source, path
}

func TestSummaryReaderUsesOnlyCompiledIdentityAndPrivateSources(t *testing.T) {
	reader, err := NewSummaryReader(DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	want := domain.NFOIdentity{ParserVersion: domain.NFOParserVersion, SummarySchemaVersion: domain.NFOSummarySchemaVersion, FingerprintVersion: SourceFingerprintVersion, MaxSourceBytes: DefaultMaxBytes}
	if reader.Identity() != want {
		t.Fatal("reader identity did not reflect its compiled policy")
	}
	copy := reader.Identity()
	copy.ParserVersion, copy.MaxSourceBytes = "caller parser", 1
	if reader.Identity() != want {
		t.Fatal("caller changed trusted reader identity")
	}
	other, err := NewSummaryReader(1)
	if err != nil {
		t.Fatal(err)
	}
	one, err := domain.NFOIdentityDigest(reader.Identity())
	if err != nil {
		t.Fatal(err)
	}
	two, err := domain.NFOIdentityDigest(other.Identity())
	if err != nil || one == two {
		t.Fatal("source budget was not part of parser identity")
	}
	for _, limit := range []int64{0, -1, MaxAllowedBytes + 1} {
		if result, err := NewSummaryReader(limit); result != nil || err != domain.ErrInvalid {
			t.Fatal("unsafe reader byte budget accepted")
		}
	}
	source, _ := summaryFixture(t, []byte("<movie><title>PRIVATE_TITLE</title></movie>"))
	stamp := source.Stamp()
	stamp.SHA256 = "caller edit"
	if source.Stamp().SHA256 == stamp.SHA256 {
		t.Fatal("stamp was not a value copy")
	}
	encoded, err := json.Marshal(source)
	if err != nil || string(encoded) != "{}" {
		t.Fatal("source wrapper exposed private original data")
	}
	for _, text := range []string{fmt.Sprint(source), fmt.Sprintf("%+v", source), fmt.Sprintf("%#v", source)} {
		if text != "nfo validation source (data redacted)" {
			t.Fatal("formatted source exposed its internal representation")
		}
	}
	var nilReader *SummaryReader
	if nilReader.Identity() != (domain.NFOIdentity{}) {
		t.Fatal("nil reader exposed an identity")
	}
}

func TestSummaryProjectionNeverPersistsArbitraryMetadata(t *testing.T) {
	secrets := []string{"PRIVATE_ROOT", "PRIVATE_TITLE", "PRIVATE_PLOT", "PRIVATE_PERSON", "PRIVATE_ID", "PRIVATE_PROVIDER", "PRIVATE_URL"}
	data := []byte(`<PRIVATE_ROOT><title>PRIVATE_TITLE</title><plot>PRIVATE_PLOT</plot><actor><name>PRIVATE_PERSON</name><thumb>https://example.invalid/PRIVATE_URL</thumb></actor><uniqueid type="PRIVATE_PROVIDER">PRIVATE_ID</uniqueid><thumb>file:///PRIVATE_URL</thumb></PRIVATE_ROOT>`)
	source, path := summaryFixture(t, data)
	summary, err := source.Parse(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if summary.Status != domain.NFOStatusInvalid || summary.Root != "unknown" || summary.Entries != 1 || summary.Encoding != "UTF-8" || summary.ErrorCount != 1 || summary.WarningCount != 1 || summary.FailureCode != "" {
		t.Fatalf("semantic validation projection incorrect: %#v", summary)
	}
	encoded, err := domain.MarshalNFOSummary(summary)
	if err != nil || len(encoded) > domain.NFOSummaryMaxBytes {
		t.Fatal("summary did not fit its strict storage contract")
	}
	for _, secret := range append(secrets, path.RootPath, path.RelativePath) {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("summary disclosed source metadata or paths")
		}
	}
	if !strings.Contains(string(encoded), "nfo_unsafe_reference") || !strings.Contains(string(encoded), "nfo_unknown_root") {
		t.Fatal("fixed validation findings were lost")
	}
}

func TestSummaryCountsAllIssuesButRetainsOnlyBoundedPrefix(t *testing.T) {
	for _, kind := range []string{"warnings", "errors"} {
		t.Run(kind, func(t *testing.T) {
			data := strings.Repeat("<episode/>", 80)
			wantCount, wantStatus := int64(240), domain.NFOStatusValid
			if kind == "errors" {
				data = "<movie><title>Present</title>" + strings.Repeat("<year>invalid</year>", 80) + "</movie>"
				wantCount, wantStatus = 80, domain.NFOStatusInvalid
			}
			source, _ := summaryFixture(t, []byte(data))
			summary, err := source.Parse(context.Background())
			if err != nil || summary.Status != wantStatus || summary.IssueCount != wantCount || !summary.IssuesTruncated || len(summary.Issues) != domain.NFOIssuesMax || summary.WarningCount+summary.ErrorCount != wantCount {
				t.Fatalf("summary count/truncation failed: %#v, %v", summary, err)
			}
			if kind == "warnings" && summary.ErrorCount != 0 || kind == "errors" && summary.WarningCount != 0 {
				t.Fatal("warnings and errors were conflated")
			}
			encoded, err := domain.MarshalNFOSummary(summary)
			if err != nil || len(encoded) > domain.NFOSummaryMaxBytes {
				t.Fatal("bounded prefix exceeded storage budget")
			}
			document := mustRead(t, []byte(data))
			for index, issue := range summary.Issues {
				original := document.Issues[index]
				if issue != (domain.NFOIssue{Severity: original.Severity, Code: original.Code, Field: original.Field, Entry: original.Entry}) {
					t.Fatal("summary was not a source-order prefix")
				}
			}
		})
	}
}

func TestSummaryErrorAfterRetainedWarningPrefixStillMakesResultInvalid(t *testing.T) {
	data := `<root>` + strings.Repeat(`<item><title>Ignored wrapper field</title><movie><title>Present</title></movie></item>`, 64) + `<movie><title>Present</title><year>bad</year></movie></root>`
	source, _ := summaryFixture(t, []byte(data))
	summary, err := source.Parse(context.Background())
	if err != nil || summary.Status != domain.NFOStatusInvalid || summary.WarningCount != 64 || summary.ErrorCount != 1 || summary.IssueCount != 65 || !summary.IssuesTruncated || len(summary.Issues) != 64 || summary.Entries != 65 {
		t.Fatalf("unretained semantic error was lost: %#v, %v", summary, err)
	}
	for _, issue := range summary.Issues {
		if issue.Severity != "warning" || issue.Code != "nfo_wrapper_fields_ignored" {
			t.Fatal("fixture did not put the semantic error after the retained prefix")
		}
	}
}

func TestSummaryNamespacedRootRemainsUnknown(t *testing.T) {
	source, _ := summaryFixture(t, []byte(`<movie xmlns="urn:private:metadata"><title>Private</title></movie>`))
	summary, err := source.Parse(context.Background())
	if err != nil || summary.Root != "unknown" || summary.Status != domain.NFOStatusValid || summary.WarningCount == 0 {
		t.Fatal("unknown qualified root became a recognized kind")
	}
}

func TestSummaryPreservesKnownEncodingWithoutExportingContent(t *testing.T) {
	gbk := append([]byte(`<movie><title>`), 0xd6, 0xd0, 0xce, 0xc4)
	gbk = append(gbk, []byte(`</title></movie>`)...)
	for _, entry := range []struct {
		name, encoding string
		data           []byte
		guessed        bool
	}{
		{"utf8", "UTF-8", []byte(`<movie><title>中文</title></movie>`), false},
		{"utf16le", "UTF-16LE", encodeUTF16(`<movie><title>中文</title></movie>`, true, true), false},
		{"utf16be", "UTF-16BE", encodeUTF16(`<movie><title>中文</title></movie>`, false, true), false},
		{"utf16guessed", "UTF-16LE", encodeUTF16(`<movie><title>中文</title></movie>`, true, false), true},
		{"gbkguessed", "GBK", gbk, true},
		{"gbkdeclared", "GBK", append([]byte(`<?xml version="1.0" encoding="GBK"?>`), gbk...), false},
	} {
		t.Run(entry.name, func(t *testing.T) {
			source, _ := summaryFixture(t, entry.data)
			summary, err := source.Parse(context.Background())
			if err != nil || summary.Status != domain.NFOStatusValid || summary.Encoding != entry.encoding || summary.EncodingGuessed != entry.guessed || summary.Root != "movie" || summary.Entries != 1 {
				t.Fatalf("encoding summary mismatch: %#v, %v", summary, err)
			}
		})
	}
}

func TestSummaryCachesOnlyTheFiveStableParserFailures(t *testing.T) {
	for _, entry := range []struct {
		name string
		data []byte
		code domain.NFOFailureCode
	}{
		{"xml", []byte(`<movie><title>secret`), domain.NFOFailureInvalidXML},
		{"unsafe", []byte(`<!DOCTYPE movie [<!ENTITY secret "private">]><movie/>`), domain.NFOFailureUnsafeXML},
		{"encoding", []byte{0xff, 0xfe, '<'}, domain.NFOFailureInvalidEncoding},
		{"unsupported", []byte(`<?xml version="1.0" encoding="PRIVATE_ENCODING"?><movie/>`), domain.NFOFailureUnsupportedEncoding},
		{"complex", []byte(`<movie>` + strings.Repeat(`<unknown>`, 65) + strings.Repeat(`</unknown>`, 65) + `</movie>`), domain.NFOFailureTooComplex},
	} {
		t.Run(entry.name, func(t *testing.T) {
			source, _ := summaryFixture(t, entry.data)
			summary, err := source.Parse(context.Background())
			if err != nil || summary.Status != domain.NFOStatusInvalid || summary.FailureCode != entry.code || summary.Encoding != "unknown" || summary.Root != "unknown" || summary.Entries != 0 || summary.IssueCount != 0 || len(summary.Issues) != 0 || summary.WarningCount != 0 || summary.ErrorCount != 0 || summary.EncodingGuessed {
				t.Fatalf("failure was not a fixed unknown summary: %#v, %v", summary, err)
			}
		})
	}
}

func TestSummarySourceFailuresRemainUncacheable(t *testing.T) {
	for _, entry := range []struct{ input, want error }{
		{ErrChanged, domain.ErrNFOSourceChanged}, {ErrTooLarge, domain.ErrNFOSourceLimit},
		{ErrRead, domain.ErrNFOInputUnavailable}, {ErrNotFound, domain.ErrNFOInputUnavailable}, {ErrInvalidInput, domain.ErrNFOInputUnavailable},
		{context.Canceled, context.Canceled}, {context.DeadlineExceeded, context.DeadlineExceeded},
		{errors.New("private arbitrary failure"), domain.ErrNFOReaderUnavailable},
	} {
		summary, err := summaryParseFailure(fmt.Errorf("private root: %w", entry.input))
		if !reflect.DeepEqual(summary, domain.NFOValidationSummary{}) || err != entry.want {
			t.Fatalf("source error was cached or unsafe: %v", err)
		}
	}
	for _, input := range []error{context.Canceled, context.DeadlineExceeded, ErrRead, ErrTooLarge, ErrChanged} {
		if summary, err := summaryParseFailure(errors.Join(ErrInvalidXML, input)); !reflect.DeepEqual(summary, domain.NFOValidationSummary{}) || err == nil {
			t.Fatal("source failure lost priority to XML failure")
		}
	}
	reader, err := NewSummaryReader(8)
	if err != nil {
		t.Fatal(err)
	}
	root, name := sourceFixture(t, []byte("<movie/>\n"))
	path := domain.NFOSource{RootPath: root, RelativePath: name}
	if source, err := reader.Read(context.Background(), path); source != nil || err != domain.ErrNFOSourceLimit {
		t.Fatal("oversized source returned a stamp")
	}
	path.RelativePath = "../private.nfo"
	if source, err := reader.Read(context.Background(), path); source != nil || err != domain.ErrNFOInputUnavailable {
		t.Fatal("unsafe source returned a stamp")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if source, err := reader.Read(ctx, path); source != nil || err != context.Canceled {
		t.Fatal("reader lost cancellation priority")
	}
	if source, err := reader.Read(nil, path); source != nil || err != domain.ErrInvalid {
		t.Fatal("nil context accepted")
	}
	for _, absent := range []*SummaryReader{nil, {}} {
		if source, err := absent.Read(context.Background(), path); source != nil || err != domain.ErrNFOReaderUnavailable {
			t.Fatal("uninitialized reader accepted a path")
		}
	}
}

func TestSummaryUnknownVocabularyFailsClosedIncludingBeyondPrefix(t *testing.T) {
	for _, field := range []string{"code", "field", "severity", "entry"} {
		for _, index := range []int{0, domain.NFOIssuesMax} {
			document := &Document{Root: "movie", Encoding: "UTF-8", Entries: []Metadata{{}}}
			for range domain.NFOIssuesMax + 1 {
				document.Issues = append(document.Issues, Issue{Severity: "warning", Code: "nfo_title_missing", Field: "title", Entry: 0})
			}
			switch field {
			case "code":
				document.Issues[index].Code = "PRIVATE_CODE"
			case "field":
				document.Issues[index].Field = "PRIVATE_FIELD"
			case "severity":
				document.Issues[index].Severity = "PRIVATE_SEVERITY"
			case "entry":
				document.Issues[index].Entry = 1
			}
			if summary, err := projectSummary(context.Background(), document); !reflect.DeepEqual(summary, domain.NFOValidationSummary{}) || err != domain.ErrNFOReaderUnavailable {
				t.Fatal("unknown or inconsistent issue was hidden by truncation")
			}
		}
	}
}

func TestSummaryRetainedSourceIsImmutableAcrossParsesAndPathChanges(t *testing.T) {
	source, path := summaryFixture(t, []byte("<episode/>"))
	want, err := source.Parse(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	first, err := source.Parse(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	first.Issues[0].Field = "caller edit"
	if err := os.WriteFile(filepath.Join(path.RootPath, path.RelativePath), []byte("not XML now"), 0o600); err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			result, err := source.Parse(context.Background())
			if err != nil || !reflect.DeepEqual(result, want) {
				t.Error("retained source used a changed path or caller summary")
				return
			}
			result.Issues[0].Field = "private edit"
		}()
	}
	workers.Wait()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result, err := source.Parse(ctx); !reflect.DeepEqual(result, domain.NFOValidationSummary{}) || err != context.Canceled {
		t.Fatal("cancelled parse returned a summary")
	}
	if result, err := source.Parse(nil); !reflect.DeepEqual(result, domain.NFOValidationSummary{}) || err != domain.ErrInvalid {
		t.Fatal("nil parse context accepted")
	}
	for _, absent := range []*summarySource{nil, {}} {
		if absent.Stamp() != (domain.NFOStamp{}) {
			t.Fatal("empty source returned a stamp")
		}
		if result, err := absent.Parse(context.Background()); !reflect.DeepEqual(result, domain.NFOValidationSummary{}) || err != domain.ErrNFOReaderUnavailable {
			t.Fatal("empty source returned a summary")
		}
	}
}
