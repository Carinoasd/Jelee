package nfo

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestItemFieldsProjectsNFOTagline(t *testing.T) {
	root, name := sourceFixture(t, []byte(`<movie><title>Display title</title><tagline>A short tagline</tagline></movie>`))
	reader, err := NewSummaryReader(DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	fields, err := reader.ReadItemFields(context.Background(), domain.NFOSource{RootPath: root, RelativePath: name}, "Movie")
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range fields.Fields {
		if field.Field == "tagline" && field.Value == "A short tagline" {
			return
		}
	}
	t.Fatal("supported NFO tagline was dropped from confirmed item projection")
}

func TestItemFieldsExtendedTextBoundsAndSingletons(t *testing.T) {
	for _, tag := range []string{"tagline", "outline", "mpaa", "certification"} {
		t.Run(tag, func(t *testing.T) {
			reader, err := NewSummaryReader(DefaultMaxBytes)
			if err != nil {
				t.Fatal(err)
			}
			limit := 1024
			if tag == "outline" {
				limit = 16384
			}
			for _, xml := range []string{fmt.Sprintf("<movie><title>T</title><%s>%s</%s></movie>", tag, strings.Repeat("X", limit+1), tag), fmt.Sprintf("<movie><%s/><%s>Second</%s><lockdata>true</lockdata></movie>", tag, strings.ToUpper(tag), strings.ToUpper(tag))} {
				root, name := sourceFixture(t, []byte(xml))
				if _, err := reader.ReadItemFields(context.Background(), domain.NFOSource{RootPath: root, RelativePath: name}, "Movie"); !errors.Is(err, domain.ErrMetadataUnavailable) {
					t.Fatal("oversized or ambiguous text accepted", tag, err)
				}
			}
		})
	}
	checkItemTextField(t, "tagline", "tagline", strings.Repeat("繁", 341))
}

func checkItemTextField(t *testing.T, field, tag, value string) {
	t.Helper()
	root, name := sourceFixture(t, []byte(fmt.Sprintf("<movie><title>Display title</title><%s>%s</%s></movie>", tag, value, tag)))
	reader, err := NewSummaryReader(DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	fields, err := reader.ReadItemFields(context.Background(), domain.NFOSource{RootPath: root, RelativePath: name}, "Movie")
	if err != nil {
		t.Fatal(err)
	}
	for _, actual := range fields.Fields {
		if actual.Field == field && actual.Value == value {
			return
		}
	}
	t.Fatal("supported NFO text field was dropped", field)
}

func TestItemFieldsProjectsNFOOutline(t *testing.T) {
	checkItemTextField(t, "outline", "outline", "Short outline")
}

func TestItemFieldsProjectsNFOMPAA(t *testing.T) {
	checkItemTextField(t, "mpaa", "mpaa", "PG-13")
}

func TestItemFieldsProjectsNFOCertification(t *testing.T) {
	checkItemTextField(t, "certification", "certification", "TW:12")
}

func TestItemFieldsRejectsRepeatedTagline(t *testing.T) {
	root, name := sourceFixture(t, []byte(`<movie><title>Display title</title><tagline>First</tagline><TAGLINE>Second</TAGLINE></movie>`))
	reader, err := NewSummaryReader(DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	_, err = reader.ReadItemFields(context.Background(), domain.NFOSource{RootPath: root, RelativePath: name}, "Movie")
	if !errors.Is(err, domain.ErrMetadataUnavailable) {
		t.Fatal("repeated tagline silently chose a value", err)
	}
}

func TestItemFieldsAcceptsOfficialRatingLockWithoutText(t *testing.T) {
	root, name := sourceFixture(t, []byte(`<movie><lockedfields>OfficialRating</lockedfields></movie>`))
	reader, err := NewSummaryReader(DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	fields, err := reader.ReadItemFields(context.Background(), domain.NFOSource{RootPath: root, RelativePath: name}, "Movie")
	if err != nil || len(fields.Fields) != 0 || fields.Version != domain.NFOItemTextFieldsVersion {
		t.Fatal("known official rating lock-only NFO was rejected", err)
	}
}
