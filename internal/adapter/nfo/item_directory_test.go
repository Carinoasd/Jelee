package nfo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func directoryItemFixture(t *testing.T, kind, directory string) (*SummaryReader, domain.NFOItemScope) {
	t.Helper()
	const id = "11111111-1111-4111-8111-111111111111"
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, directory), 0700); err != nil {
		t.Fatal(err)
	}
	name, ok := domain.DirectoryNFOPath(directory, kind)
	if !ok {
		t.Fatal("invalid fixture directory")
	}
	reader, _ := NewSummaryReader(domain.NFODefaultSourceBytes)
	return reader, domain.NFOItemScope{ItemID: id, LibraryID: id, SourceID: id, RootID: id, Kind: kind, DirectoryPath: directory, Revision: 1, Generation: 1, Source: domain.NFOSource{RootPath: root, RelativePath: name}}
}

func TestDirectoryNFOSelectionAndOwnedSeason(t *testing.T) {
	for _, directory := range []string{".", "series/season"} {
		t.Run(directory, func(t *testing.T) {
			reader, scope := directoryItemFixture(t, "Season", directory)
			for _, name := range []string{"movie.nfo", "tvshow.nfo", "Other.nfo"} {
				if err := os.WriteFile(filepath.Join(scope.Source.RootPath, directory, name), []byte(`<season><title>Wrong file</title></season>`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := reader.SelectItemNFO(context.Background(), scope); !errors.Is(err, domain.ErrNFOItemAbsent) {
				t.Fatal("season selected unrelated NFO", err)
			}
			file := filepath.Join(scope.Source.RootPath, directory, "SEASON.NFO")
			content := `<season><seasonnumber>0</seasonnumber><lockdata>true</lockdata></season>`
			if err := os.WriteFile(file, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			observed, err := reader.ObserveItemNFO(context.Background(), scope)
			if err != nil {
				t.Fatal(err)
			}
			value := observed.Selection().Fields
			if value.Version != domain.NFOItemSeasonFieldsVersion || value.SeasonDetails == nil || value.SeasonDetails.Number == nil || *value.SeasonDetails.Number != 0 || len(domain.NFOItemFieldNames(value.Version)) != 29 {
				t.Fatal("season projection lost zero or global fields")
			}
			*value.SeasonDetails.Number = 999
			if *observed.Selection().Fields.SeasonDetails.Number != 0 {
				t.Fatal("caller mutated owned season")
			}
			if _, err := observed.Recheck(context.Background()); err != nil {
				t.Fatal("owned season recheck failed", err)
			}
			if raw, err := os.ReadFile(file); err != nil || string(raw) != content {
				t.Fatal("season read changed source", err)
			}
		})
	}
}

func TestDirectoryNFORejectsReplacedDirectory(t *testing.T) {
	reader, scope := directoryItemFixture(t, "Series", "series")
	content := []byte(`<tvshow><title>Series</title></tvshow>`)
	directory := filepath.Join(scope.Source.RootPath, "series")
	if err := os.WriteFile(filepath.Join(directory, "tvshow.nfo"), content, 0600); err != nil {
		t.Fatal(err)
	}
	observed, err := reader.ObserveItemNFO(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(directory, filepath.Join(scope.Source.RootPath, "old-series")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "tvshow.nfo"), content, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := observed.Recheck(context.Background()); !errors.Is(err, domain.ErrNFOSourceChanged) {
		t.Fatal("replacement directory passed recheck", err)
	}
}

func TestSeasonNFORejectsInvalidNumbersAndRetainsMissingLock(t *testing.T) {
	for _, xml := range []string{`<seasonnumber>-1</seasonnumber>`, `<seasonnumber>1000001</seasonnumber>`, `<seasonnumber>1.5</seasonnumber>`, `<season>1</season><seasonnumber>2</seasonnumber>`} {
		root, name := sourceFixture(t, []byte(`<season><title>Season</title>`+xml+`</season>`))
		reader, _ := NewSummaryReader(DefaultMaxBytes)
		if _, err := reader.ReadItemFields(context.Background(), domain.NFOSource{RootPath: root, RelativePath: name}, "Season"); err == nil {
			t.Fatal("invalid season accepted", xml)
		}
	}
	root, name := sourceFixture(t, []byte(`<season><lockedfields>IndexNumber</lockedfields></season>`))
	reader, _ := NewSummaryReader(DefaultMaxBytes)
	value, err := reader.ReadItemFields(context.Background(), domain.NFOSource{RootPath: root, RelativePath: name}, "Season")
	if err != nil || value.SeasonDetails != nil || !domain.NFOFieldLocked(value, "seasonNumber") {
		t.Fatal("missing season lock invented a number", err)
	}
}

func TestDirectoryNFODoesNotFollowSymlink(t *testing.T) {
	reader, scope := directoryItemFixture(t, "Series", "series")
	if err := os.Symlink(filepath.Join(scope.Source.RootPath, "series"), filepath.Join(scope.Source.RootPath, "linked")); err != nil {
		t.Skip("symlink creation unavailable", err)
	}
	scope.DirectoryPath = "linked"
	scope.Source.RelativePath = "linked/tvshow.nfo"
	if _, err := reader.ObserveItemNFO(context.Background(), scope); !errors.Is(err, domain.ErrNFOInputUnavailable) {
		t.Fatal("directory NFO followed a symlink", err)
	}
}
