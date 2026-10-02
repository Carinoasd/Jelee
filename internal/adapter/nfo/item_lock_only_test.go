package nfo

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestItemFieldsReadsLockOnlyProjection(t *testing.T) {
	original := []byte(`<movie><lockdata>true</lockdata></movie>`)
	root, name := sourceFixture(t, original)
	reader, err := NewSummaryReader(DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	fields, err := reader.ReadItemFields(context.Background(), domain.NFOSource{RootPath: root, RelativePath: name}, "Movie")
	if err != nil {
		t.Fatal("valid lock-only NFO was rejected", err)
	}
	if fields.Version != domain.NFOItemMovieFieldsVersion || !domain.ValidNFOItemFields(fields) || len(fields.Fields) != 0 || !fields.LockData || fields.Stamp.SHA256 != sourceDigest(original) || !domain.NFOFieldLocked(fields, "overview") {
		t.Fatal("lock-only projection lost positive intent or invented text")
	}
	if actual, err := os.ReadFile(filepath.Join(root, name)); err != nil || string(actual) != string(original) {
		t.Fatal("lock-only read changed original", err)
	}
}

func TestLockOnlyNFOOwnsValidObservationAndRechecks(t *testing.T) {
	for _, entry := range []struct {
		name, kind string
		original   []byte
	}{
		{"global", "Movie", []byte(`<movie><lockdata>true</lockdata></movie>`)},
		{"named", "Movie", []byte(`<movie><lockedfields> Overview | Unknown </lockedfields></movie>`)},
		{"series-utf16", "Series", encodeUTF16(`<tvshow><lockedfields>PremiereDate</lockedfields></tvshow>`, true, true)},
	} {
		t.Run(entry.name, func(t *testing.T) {
			reader, scope := itemSelectionFixture(t, entry.kind)
			file := filepath.Join(scope.Source.RootPath, filepath.FromSlash(scope.Source.RelativePath))
			if err := os.WriteFile(file, entry.original, 0600); err != nil {
				t.Fatal(err)
			}
			observed, err := reader.ObserveItemNFO(context.Background(), scope)
			if err != nil {
				t.Fatal("lock-only observation unavailable", err)
			}
			state := observed.(app.NFOItemStateObservation).State()
			expectedVersion := domain.NFOItemLockFieldsVersion
			if entry.name == "global" {
				expectedVersion = domain.NFOItemMovieFieldsVersion
			}
			if state.Status != domain.NFOItemObservedValid || !domain.ValidNFOItemObservationState(scope, state) || state.Selection.Fields.Version != expectedVersion || len(state.Selection.Fields.Fields) != 0 || state.Stamp.SHA256 != sourceDigest(entry.original) {
				t.Fatal("lock-only observation became fallback or invented text")
			}
			if _, err := observed.Recheck(context.Background()); err != nil {
				t.Fatal("stable lock-only observation rejected", err)
			}
			if err := os.WriteFile(file, []byte(`<movie/>`), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := observed.Recheck(context.Background()); err == nil {
				t.Fatal("changed lock intent passed recheck")
			}
		})
	}
}
