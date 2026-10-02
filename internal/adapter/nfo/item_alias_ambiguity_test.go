package nfo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestItemFieldsRejectsConflictingTitleAliases(t *testing.T) {
	root, name := sourceFixture(t, []byte(`<movie><title>First title</title><name>Conflicting title</name></movie>`))
	reader, err := NewSummaryReader(DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	fields, err := reader.ReadItemFields(context.Background(), domain.NFOSource{RootPath: root, RelativePath: name}, "Movie")
	if !errors.Is(err, domain.ErrMetadataUnavailable) || len(fields.Fields) != 0 {
		t.Fatal("conflicting aliases silently chose an NFO title", err)
	}
}

func TestItemFieldsAliasesShareSingletonDestination(t *testing.T) {
	for _, xml := range []string{
		`<movie><name>One</name><localtitle>Two</localtitle></movie>`,
		`<movie><localtitle>One</localtitle><seasonname>Two</seasonname></movie>`,
		`<movie><TITLE>One</TITLE><Name>Two</Name></movie>`,
		`<movie><title>One</title><name/><lockdata>true</lockdata></movie>`,
		`<movie><title>One</title><premiered>2024-01-01</premiered><releasedate>2025-01-01</releasedate></movie>`,
		`<movie><title>One</title><releasedate>2024-01-01</releasedate><RELEASEDATE>2025-01-01</RELEASEDATE></movie>`,
	} {
		root, name := sourceFixture(t, []byte(xml))
		reader, _ := NewSummaryReader(DefaultMaxBytes)
		fields, err := reader.ReadItemFields(context.Background(), domain.NFOSource{RootPath: root, RelativePath: name}, "Movie")
		if !errors.Is(err, domain.ErrMetadataUnavailable) || len(fields.Fields) != 0 {
			t.Fatal("ambiguous scalar aliases were accepted", err)
		}
		if raw, err := os.ReadFile(filepath.Join(root, name)); err != nil || string(raw) != xml {
			t.Fatal("rejected original changed", err)
		}
	}
}

func TestItemFieldsSingleAliasesAndNestedNamesRemainSupported(t *testing.T) {
	for _, alias := range []string{"title", "name", "localtitle", "seasonname"} {
		xml := `<movie><` + alias + `>Alias title</` + alias + `><releasedate>2024-05-06</releasedate><actor><name>Actor one</name></actor><actor><name>Actor two</name></actor></movie>`
		root, name := sourceFixture(t, []byte(xml))
		reader, _ := NewSummaryReader(DefaultMaxBytes)
		fields, err := reader.ReadItemFields(context.Background(), domain.NFOSource{RootPath: root, RelativePath: name}, "Movie")
		if err != nil || len(fields.Fields) != 2 || fields.Fields[0].Field != "title" || fields.Fields[0].Value != "Alias title" || fields.Fields[1].Field != "date" || fields.Fields[1].Value != "2024-05-06" {
			t.Fatal("single alias or nested actor names rejected", alias, err)
		}
	}
}
