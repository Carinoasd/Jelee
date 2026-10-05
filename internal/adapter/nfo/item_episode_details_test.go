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

func TestItemEpisodeRequiresMatchingMediaBasename(t *testing.T) {
	for _, root := range []string{"episode", "episodedetails"} {
		t.Run(root, func(t *testing.T) {
			reader, scope := itemSelectionFixture(t, "Episode")
			content := []byte("<" + root + "><title>Episode</title><season>0</season><episode>1</episode></" + root + ">")
			for _, name := range []string{"movie.nfo", "tvshow.nfo", "season.nfo", "Other.nfo"} {
				if err := os.WriteFile(filepath.Join(scope.Source.RootPath, "folder", name), content, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := reader.SelectItemNFO(context.Background(), scope); !errors.Is(err, domain.ErrNFOItemAbsent) {
				t.Fatal("episode selected an unrelated sidecar", err)
			}
			path := filepath.Join(scope.Source.RootPath, "folder", "fIlM.NfO")
			if err := os.WriteFile(path, content, 0600); err != nil {
				t.Fatal(err)
			}
			selected, err := reader.SelectItemNFO(context.Background(), scope)
			if err != nil || selected.RelativePath != "folder/fIlM.NfO" || selected.Fields.EpisodeDetails == nil || !domain.ValidNFOItemSelection(scope, selected) {
				t.Fatal("matching episode sidecar was not selected", err)
			}
			if got, err := os.ReadFile(path); err != nil || string(got) != string(content) {
				t.Fatal("episode sidecar was changed", err)
			}
			if got, err := os.ReadFile(filepath.Join(scope.Source.RootPath, "folder", "Film.mkv")); err != nil || string(got) != "original media" {
				t.Fatal("episode media was changed", err)
			}
		})
	}
}

func TestItemEpisodeOwnsOptionalNumbers(t *testing.T) {
	reader, scope := itemSelectionFixture(t, "Episode")
	path := filepath.Join(scope.Source.RootPath, "folder", "Film.nfo")
	content := `<episodedetails><season>0</season><episode>1</episode><displayseason>0</displayseason><displayepisode>2</displayepisode><aired>2024-02-29</aired><showtitle>Series</showtitle><lockdata>true</lockdata></episodedetails>`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	observed, err := reader.ObserveItemNFO(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	caller := observed.Selection().Fields
	if caller.Version != domain.NFOItemEpisodeFieldsVersion || caller.EpisodeDetails == nil {
		t.Fatal("missing episode projection")
	}
	v := caller.EpisodeDetails
	for _, n := range []*int{v.SeasonNumber, v.EpisodeNumber, v.DisplaySeason, v.DisplayEpisode} {
		if n == nil {
			t.Fatal("missing explicit episode number")
		}
		*n = 999
	}
	v.ShowTitle = "caller edit"
	owned := observed.Selection().Fields.EpisodeDetails
	if *owned.SeasonNumber != 0 || *owned.EpisodeNumber != 1 || *owned.DisplaySeason != 0 || *owned.DisplayEpisode != 2 || owned.ShowTitle != "Series" || owned.Aired != "2024-02-29" {
		t.Fatal("caller mutated owned episode values")
	}
	if _, err := observed.Recheck(context.Background()); err != nil {
		t.Fatal("caller mutation broke recheck", err)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(content, "<episode>1", "<episode>3", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := observed.Recheck(context.Background()); err == nil {
		t.Fatal("changed episode source passed recheck")
	}
	root, name := sourceFixture(t, []byte(`<episode><season>0</season></episode>`))
	fields, err := reader.ReadItemFields(context.Background(), domain.NFOSource{RootPath: root, RelativePath: name}, "Episode")
	if err != nil || fields.EpisodeDetails == nil || fields.EpisodeDetails.SeasonNumber == nil || *fields.EpisodeDetails.SeasonNumber != 0 || fields.EpisodeDetails.EpisodeNumber != nil || fields.EpisodeDetails.DisplaySeason != nil || fields.EpisodeDetails.DisplayEpisode != nil {
		t.Fatal("missing episode values became zero", err)
	}
}

func TestItemEpisodeRejectsAmbiguousOrInvalidValues(t *testing.T) {
	for _, content := range []string{
		`<season>-1</season>`, `<episode>1000001</episode>`, `<displayseason>1.5</displayseason>`, `<displayepisode>-1</displayepisode>`,
		`<season>1</season><seasonnumber>2</seasonnumber>`, `<episode>1</episode><episode>2</episode>`,
		`<displayseason>1</displayseason><displayseason>2</displayseason>`, `<displayepisode>1</displayepisode><displayepisode>2</displayepisode>`,
		`<aired>2023-02-29</aired>`, `<aired>2024-02-29</aired><aired>2024-03-01</aired>`,
		`<showtitle>A</showtitle><showtitle>B</showtitle>`, `<showtitle>` + strings.Repeat("a", 1025) + `</showtitle>`,
	} {
		root, name := sourceFixture(t, []byte(`<episodedetails><title>Episode</title>`+content+`</episodedetails>`))
		reader, _ := NewSummaryReader(DefaultMaxBytes)
		fields, err := reader.ReadItemFields(context.Background(), domain.NFOSource{RootPath: root, RelativePath: name}, "Episode")
		if !errors.Is(err, domain.ErrMetadataUnavailable) || fields.EpisodeDetails != nil || len(fields.Fields) != 0 {
			t.Fatal("invalid episode returned partial metadata", content, err)
		}
	}
	for _, content := range []string{
		`<movie><title>Wrong root</title></movie>`, `<tvshow><title>Wrong root</title></tvshow>`,
		`<episodedetails><title>One</title></episodedetails><episodedetails><title>Two</title></episodedetails>`,
		`<episodes><episodedetails><title>One</title></episodedetails></episodes>`,
	} {
		root, name := sourceFixture(t, []byte(content))
		reader, _ := NewSummaryReader(DefaultMaxBytes)
		if _, err := reader.ReadItemFields(context.Background(), domain.NFOSource{RootPath: root, RelativePath: name}, "Episode"); err == nil {
			t.Fatal("episode accepted wrong root or multiple entries")
		}
	}
}

func TestItemEpisodeMissingNamedLocksKeepNoValues(t *testing.T) {
	root, name := sourceFixture(t, []byte(`<episode><lockedfields>ParentIndexNumber|IndexNumber|DisplaySeason|DisplayEpisode|Aired|SeriesName</lockedfields></episode>`))
	reader, _ := NewSummaryReader(DefaultMaxBytes)
	fields, err := reader.ReadItemFields(context.Background(), domain.NFOSource{RootPath: root, RelativePath: name}, "Episode")
	if err != nil || fields.EpisodeDetails != nil || len(fields.Fields) != 0 {
		t.Fatal("missing named locks invented episode values", err)
	}
	for _, field := range domain.ItemMetadataEpisodeFieldNames() {
		if !domain.NFOFieldLocked(fields, field) {
			t.Fatal("episode named lock was lost", field)
		}
	}
}
