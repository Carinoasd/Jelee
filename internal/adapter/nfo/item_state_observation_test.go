package nfo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestItemNFOStateObservationsAndTransitions(t *testing.T) {
	for _, mode := range []string{"valid", "missing", "missing-appears", "invalid", "invalid-repaired", "invalid-same-stamp-change"} {
		t.Run(mode, func(t *testing.T) {
			reader, scope := itemSelectionFixture(t, "Movie")
			file := filepath.Join(scope.Source.RootPath, "folder", "Film.nfo")
			valid := []byte(`<movie><title>Local</title></movie>`)
			broken := []byte(`<movie><title>broken`)
			want := domain.NFOItemObservedValid
			switch {
			case strings.HasPrefix(mode, "missing"):
				want = domain.NFOItemObservedMissing
			case strings.HasPrefix(mode, "invalid"):
				want = domain.NFOItemObservedInvalid
				if err := os.WriteFile(file, broken, 0600); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.WriteFile(file, valid, 0600); err != nil {
					t.Fatal(err)
				}
			}
			observed, err := reader.ObserveItemNFO(context.Background(), scope)
			if err != nil {
				t.Fatal("state observation unavailable", err)
			}
			state := observed.(app.NFOItemStateObservation).State()
			if state.Status != want || !domain.ValidNFOItemObservationState(scope, state) {
				t.Fatal("invalid state", state.Status)
			}
			if want != domain.NFOItemObservedValid && len(state.Selection.Fields.Fields) != 0 {
				t.Fatal("nonvalid state exposed partial fields")
			}
			if want == domain.NFOItemObservedMissing && state.Stamp != (domain.NFOStamp{}) {
				t.Fatal("missing state invented file stamp")
			}
			if want == domain.NFOItemObservedInvalid && state.Stamp.SHA256 == "" {
				t.Fatal("invalid state lost original byte observation")
			}
			conflict := false
			switch mode {
			case "missing-appears", "invalid-repaired":
				if err := os.WriteFile(file, valid, 0600); err != nil {
					t.Fatal(err)
				}
				conflict = true
			case "invalid-same-stamp-change":
				info, err := os.Stat(file)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, []byte(strings.Replace(string(broken), "broken", "broKen", 1)), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(file, info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
				conflict = true
			}
			fresh, err := observed.Recheck(context.Background())
			if conflict {
				if !errors.Is(err, domain.ErrNFOSourceChanged) || fresh != nil {
					t.Fatal("state transition or full-byte change accepted", mode, err)
				}
			} else if err != nil || fresh == nil {
				t.Fatal("stable observation rejected", err)
			}
		})
	}
}

func TestItemNFOValidUnsupportedProjectionIsNotCorruption(t *testing.T) {
	for _, document := range []string{`<movie/>`, `<movie><lockdata>true</lockdata></movie>`, `<root><movie><title>Wrapped</title></movie></root>`, `<tvshow><title>Wrong kind</title></tvshow>`, `<movie><title>One</title><title>Two</title></movie>`, `<movie><title>Local</title><lockdata>true</lockdata><lockdata>false</lockdata></movie>`, `<!DOCTYPE movie [<!ENTITY unsafe SYSTEM "file:///private">]><movie><title>&unsafe;</title></movie>`, `<?xml version="1.0" encoding="ISO-8859-1"?><movie><title>Local</title></movie>`} {
		reader, scope := itemSelectionFixture(t, "Movie")
		if err := os.WriteFile(filepath.Join(scope.Source.RootPath, "folder", "Film.nfo"), []byte(document), 0600); err != nil {
			t.Fatal(err)
		}
		if observed, err := reader.ObserveItemNFO(context.Background(), scope); observed != nil || !errors.Is(err, domain.ErrMetadataUnavailable) {
			t.Fatal("unsupported valid XML was treated as corrupt fallback input", err)
		}
	}
}

func TestItemNFOInvalidEncodingHasOriginalByteStamp(t *testing.T) {
	reader, scope := itemSelectionFixture(t, "Movie")
	file := filepath.Join(scope.Source.RootPath, "folder", "Film.nfo")
	original := []byte{0xff, 0xfe, '<'}
	if err := os.WriteFile(file, original, 0600); err != nil {
		t.Fatal(err)
	}
	observed, err := reader.ObserveItemNFO(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	state := observed.(app.NFOItemStateObservation).State()
	if state.Status != domain.NFOItemObservedInvalid || state.Stamp.Size != int64(len(original)) || !domain.ValidNFOItemObservationState(scope, state) {
		t.Fatal("invalid encoding lost trusted original observation", state)
	}
	if _, err := observed.Recheck(context.Background()); err != nil {
		t.Fatal("stable invalid encoding rejected", err)
	}
	if _, err := reader.SelectItemNFO(context.Background(), scope); !errors.Is(err, domain.ErrMetadataUnavailable) {
		t.Fatal("field selection accepted invalid encoding", err)
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != string(original) {
		t.Fatal("invalid original bytes changed", err)
	}
}
