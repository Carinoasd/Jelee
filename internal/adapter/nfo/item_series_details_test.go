package nfo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestItemSeriesAcceptsPublishedUnknownCounts(t *testing.T) {
	// The retained SeriesNfoSaver writes -1 when the count is unknown.
	root, name := sourceFixture(t, []byte(`<tvshow><title>Series</title><season>-1</season><episode>-1</episode><status>Continuing</status></tvshow>`))
	reader, _ := NewSummaryReader(DefaultMaxBytes)
	fields, err := reader.ReadItemFields(context.Background(), domain.NFOSource{RootPath: root, RelativePath: name}, "Series")
	if err != nil || fields.Kind != "Series" || fields.SeriesDetails == nil || fields.SeriesDetails.SeasonCount == nil || *fields.SeriesDetails.SeasonCount != -1 || fields.SeriesDetails.EpisodeCount == nil || *fields.SeriesDetails.EpisodeCount != -1 {
		t.Fatal("published unknown series counts were rejected", err)
	}
}

func TestItemSeriesDetailsOwnCountsAndPreserveMissing(t *testing.T) {
	reader, scope := itemSelectionFixture(t, "Series")
	content := `<tvshow><season>0</season><status>Custom status</status><airs_dayofweek>Monday / Friday</airs_dayofweek><airs_time>9 PM</airs_time></tvshow>`
	if err := os.WriteFile(filepath.Join(scope.Source.RootPath, "folder", "Film.nfo"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	observed, err := reader.ObserveItemNFO(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	caller := observed.Selection()
	details := caller.Fields.SeriesDetails
	if caller.Fields.Version != domain.NFOItemSeriesFieldsVersion || details == nil || details.SeasonCount == nil || *details.SeasonCount != 0 || details.EpisodeCount != nil || details.Status != "Custom status" || details.AirsDayOfWeek != "Monday / Friday" || details.AirsTime != "9 PM" {
		t.Fatal("series-only observation lost original values")
	}
	*details.SeasonCount = 999
	details.AirsTime = "caller edit"
	owned := observed.Selection().Fields.SeriesDetails
	if *owned.SeasonCount != 0 || owned.AirsTime != "9 PM" {
		t.Fatal("caller mutated owned series metadata")
	}
	if _, err := observed.Recheck(context.Background()); err != nil {
		t.Fatal("caller edit broke series recheck", err)
	}
}

func TestItemSeriesDetailsRejectPartialOrAmbiguousValues(t *testing.T) {
	for _, content := range []string{
		`<season>-2</season>`, `<episode>1000001</episode>`, `<season>1.5</season>`,
		`<season>1</season><seasonnumber>2</seasonnumber>`, `<episode>1</episode><episode>2</episode>`,
		`<status>A</status><status>B</status>`, `<airs_time>9 PM</airs_time><airs_time>10 PM</airs_time>`,
		`<airs_dayofweek>Monday</airs_dayofweek><airs_dayofweek>Friday</airs_dayofweek>`,
		`<status>` + strings.Repeat("a", 129) + `</status>`, `<airs_time>` + strings.Repeat("a", 129) + `</airs_time>`,
	} {
		root, name := sourceFixture(t, []byte(`<tvshow><title>Series</title>`+content+`</tvshow>`))
		reader, _ := NewSummaryReader(DefaultMaxBytes)
		fields, err := reader.ReadItemFields(context.Background(), domain.NFOSource{RootPath: root, RelativePath: name}, "Series")
		if !errors.Is(err, domain.ErrMetadataUnavailable) || fields.SeriesDetails != nil || len(fields.Fields) != 0 {
			t.Fatal("invalid series details returned partial metadata", err)
		}
	}
}
