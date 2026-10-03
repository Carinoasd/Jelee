package nfo

import (
	"context"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestItemFieldsProjectsOwnedYearFactAndLock(t *testing.T) {
	root, name := sourceFixture(t, []byte(`<movie><year>2024</year><lockedfields>ProductionYear</lockedfields></movie>`))
	reader, err := NewSummaryReader(DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	fields, err := reader.ReadItemFields(context.Background(), domain.NFOSource{RootPath: root, RelativePath: name}, "Movie")
	if err != nil || fields.Version != domain.NFOItemYearFieldsVersion || len(fields.Facts) != 1 || fields.Facts[0].Value != 2024 || !domain.NFOFieldLocked(fields, "year") || len(fields.Fields) != 0 {
		t.Fatal("year-only projection lost type or lock", err)
	}
	fields.Facts[0].Value = 9999
	fields.LockedFields[0] = "Unknown"
	again, err := reader.ReadItemFields(context.Background(), domain.NFOSource{RootPath: root, RelativePath: name}, "Movie")
	if err != nil || again.Facts[0].Value != 2024 || !domain.NFOFieldLocked(again, "year") {
		t.Fatal("caller changed reader-owned year projection", err)
	}
}
