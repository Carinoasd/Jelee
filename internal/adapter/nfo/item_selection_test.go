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

func itemSelectionFixture(t *testing.T, kind string) (*SummaryReader, domain.NFOItemScope) {
	t.Helper()
	const id = "11111111-1111-4111-8111-111111111111"
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "folder"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "folder", "Film.mkv"), []byte("original media"), 0600); err != nil {
		t.Fatal(err)
	}
	reader, err := NewSummaryReader(domain.NFODefaultSourceBytes)
	if err != nil {
		t.Fatal(err)
	}
	return reader, domain.NFOItemScope{ItemID: id, LibraryID: id, SourceID: id, RootID: id, Kind: kind, Revision: 1, Generation: 1, MediaPath: "folder/Film.mkv", Source: domain.NFOSource{RootPath: root, RelativePath: "folder/Film.nfo"}}
}

func TestItemNFOSelectionNamesPriorityAndOwnership(t *testing.T) {
	for _, kind := range []string{"Movie", "HomeVideo", "Series"} {
		t.Run(kind, func(t *testing.T) {
			reader, scope := itemSelectionFixture(t, kind)
			root, generic := "movie", "MOVIE.NFO"
			if kind == "Series" {
				root, generic = "tvshow", "TVSHOW.NFO"
			}
			write := func(name, title string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(scope.Source.RootPath, "folder", name), []byte("<"+root+"><title>"+title+"</title></"+root+">"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			write(generic, "Generic")
			selected, err := reader.SelectItemNFO(context.Background(), scope)
			if err != nil || selected.RelativePath != "folder/"+generic || selected.Fields.Fields[0].Value != "Generic" || !domain.ValidNFOItemSelection(scope, selected) {
				t.Fatal("generic NFO selection missing", err, selected)
			}
			originalDigest := selected.CandidateDigest
			write("fIlM.NfO", "Specific")
			selected, err = reader.SelectItemNFO(context.Background(), scope)
			if err != nil || selected.RelativePath != "folder/fIlM.NfO" || selected.Fields.Fields[0].Value != "Specific" || selected.CandidateDigest == originalDigest {
				t.Fatal("specific priority or candidate observation missing", err, selected)
			}
			selected.Fields.Fields[0].Value = "caller"
			fresh, err := reader.SelectItemNFO(context.Background(), scope)
			if err != nil || fresh.Fields.Fields[0].Value != "Specific" {
				t.Fatal("caller mutation reached next observation", err)
			}
			if strings.Contains(selected.String(), scope.Source.RootPath) || strings.Contains(selected.String(), "fIlM") {
				t.Fatal("selection exposed path")
			}
			if media, err := os.ReadFile(filepath.Join(scope.Source.RootPath, "folder", "Film.mkv")); err != nil || string(media) != "original media" {
				t.Fatal("media changed", err)
			}
			if data, err := os.ReadFile(filepath.Join(scope.Source.RootPath, "folder", "fIlM.NfO")); err != nil || !strings.Contains(string(data), "Specific") {
				t.Fatal("NFO changed", err)
			}
		})
	}
}

func TestItemNFOAbsenceIsNotUnavailableOrUnsafe(t *testing.T) {
	for _, mode := range []string{"absent", "missing-media", "missing-parent", "missing-root", "candidate-directory", "invalid-xml", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			reader, scope := itemSelectionFixture(t, "Movie")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := domain.ErrNFOInputUnavailable
			switch mode {
			case "absent":
				want = domain.ErrNFOItemAbsent
			case "missing-media":
				if err := os.Remove(filepath.Join(scope.Source.RootPath, "folder", "Film.mkv")); err != nil {
					t.Fatal(err)
				}
			case "missing-parent":
				scope.MediaPath = "missing/Film.mkv"
				scope.Source.RelativePath = "missing/Film.nfo"
			case "missing-root":
				scope.Source.RootPath = filepath.Join(scope.Source.RootPath, "missing")
			case "candidate-directory":
				if err := os.Mkdir(filepath.Join(scope.Source.RootPath, "folder", "movie.nfo"), 0700); err != nil {
					t.Fatal(err)
				}
			case "invalid-xml":
				if err := os.WriteFile(filepath.Join(scope.Source.RootPath, "folder", "movie.nfo"), []byte("<movie>broken"), 0600); err != nil {
					t.Fatal(err)
				}
				want = domain.ErrMetadataUnavailable
			case "cancelled":
				cancel()
				want = context.Canceled
			}
			actual, err := reader.SelectItemNFO(ctx, scope)
			if !errors.Is(err, want) || !reflect.DeepEqual(actual, domain.NFOItemSelection{}) {
				t.Fatal("absence/error classification differs", err, actual)
			}
			if err != nil && strings.Contains(err.Error(), scope.Source.RootPath) {
				t.Fatal("error exposed root")
			}
		})
	}
}
