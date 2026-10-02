package nfo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestItemFieldsReadsBoundedOriginalAndRetainsLockIntent(t *testing.T) {
	data := []byte(`<?xml version="1.0"?><movie><title>電影</title><originaltitle>Original</originaltitle><plot>&lt;script&gt;untrusted&lt;/script&gt;</plot><premiered>2024-02-29</premiered><lockdata>true</lockdata><lockedfields>Name|Overview</lockedfields><extension><title>Ignored</title></extension></movie>`)
	root, name := sourceFixture(t, data)
	path := domain.NFOSource{RootPath: root, RelativePath: name}
	reader, err := NewSummaryReader(DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	value, err := reader.ReadItemFields(context.Background(), path, "HomeVideo")
	if err != nil || !domain.ValidNFOItemFields(value) || value.Kind != "Movie" || value.Stamp.SHA256 != sourceDigest(data) || !value.LockData || value.ReadAt.IsZero() {
		t.Fatalf("invalid original-byte observation: %v", err)
	}
	want := []domain.NFOTextField{{Field: "title", Value: "電影"}, {Field: "originalTitle", Value: "Original"}, {Field: "overview", Value: "<script>untrusted</script>"}, {Field: "date", Value: "2024-02-29"}}
	if !reflect.DeepEqual(value.Fields, want) || !reflect.DeepEqual(value.LockedFields, []string{"Name", "Overview"}) {
		t.Fatal("fields or NFO lock intent differ")
	}
	if strings.Contains(fmt.Sprintf("%v %#v", value, value), "untrusted") || strings.Contains(fmt.Sprint(value), root) {
		t.Fatal("diagnostic formatting disclosed private data")
	}
	value.Fields[0].Value = "Caller edit"
	value.LockedFields[0] = "Caller lock"
	again, err := reader.ReadItemFields(context.Background(), path, "Movie")
	if err != nil || !reflect.DeepEqual(again.Fields, want) || again.LockedFields[0] != "Name" {
		t.Fatal("caller mutation changed a later observation")
	}
	actual, err := os.ReadFile(filepath.Join(root, name))
	if err != nil || string(actual) != string(data) {
		t.Fatal("original NFO changed")
	}
}

func TestItemFieldsRejectsAmbiguousOrInvalidSources(t *testing.T) {
	for _, entry := range []struct{ name, kind, xml string }{
		{"wrong-root", "Movie", `<tvshow><title>Series</title></tvshow>`},
		{"namespace", "Movie", `<movie xmlns="urn:private"><title>Movie</title></movie>`},
		{"wrapper", "Movie", `<root><movie><title>Movie</title></movie></root>`},
		{"multiple", "Movie", `<movie><title>One</title></movie><movie><title>Two</title></movie>`},
		{"duplicate-title", "Movie", `<movie><title>One</title><TITLE>Two</TITLE></movie>`},
		{"duplicate-date", "Movie", `<movie><title>One</title><premiered>2024-01-01</premiered><premiered>2024-01-02</premiered></movie>`},
		{"duplicate-lock", "Movie", `<movie><title>One</title><lockdata>true</lockdata><lockdata>false</lockdata></movie>`},
		{"bad-date", "Movie", `<movie><title>One</title><premiered>2024-02-30</premiered></movie>`},
		{"oversized-title", "Movie", `<movie><title>` + strings.Repeat("字", 400) + `</title></movie>`},
		{"no-supported-fields", "Movie", `<movie><status>Released</status></movie>`},
		{"external-entity", "Movie", `<!DOCTYPE movie [<!ENTITY x SYSTEM "file:///private">]><movie><title>&x;</title></movie>`},
		{"malformed", "Movie", `<movie><title>One`},
	} {
		t.Run(entry.name, func(t *testing.T) {
			root, name := sourceFixture(t, []byte(entry.xml))
			reader, _ := NewSummaryReader(DefaultMaxBytes)
			value, err := reader.ReadItemFields(context.Background(), domain.NFOSource{RootPath: root, RelativePath: name}, entry.kind)
			if !errors.Is(err, domain.ErrMetadataUnavailable) || len(value.Fields) != 0 || value.Stamp.SHA256 != "" {
				t.Fatal("invalid source produced partial fields")
			}
		})
	}
}

func TestItemFieldsSeriesEncodingAndCancellation(t *testing.T) {
	data := encodeUTF16(`<tvshow><title>劇集</title><plot>Summary</plot></tvshow>`, true, true)
	root, name := sourceFixture(t, data)
	reader, _ := NewSummaryReader(DefaultMaxBytes)
	path := domain.NFOSource{RootPath: root, RelativePath: name}
	value, err := reader.ReadItemFields(context.Background(), path, "Series")
	if err != nil || value.Kind != "Series" || len(value.Fields) != 2 || value.Fields[0].Value != "劇集" || value.Stamp.SHA256 != sourceDigest(data) {
		t.Fatal("encoded series observation differs")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if value, err := reader.ReadItemFields(ctx, path, "Series"); !errors.Is(err, context.Canceled) || len(value.Fields) != 0 {
		t.Fatal("cancelled read produced fields")
	}
	if _, err := reader.ReadItemFields(context.Background(), path, "Season"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("unsupported item kind accepted")
	}
	if _, err := reader.ReadItemFields(nil, path, "Series"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("nil context accepted")
	}
}
