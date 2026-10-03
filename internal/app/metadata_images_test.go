package app

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type testImageProvider struct {
	movieProviderFunc
	images func(context.Context, string, int32, []string) (domain.MetadataImages, error)
}

func (p testImageProvider) Images(ctx context.Context, r string, id int32, languages []string) (domain.MetadataImages, error) {
	return p.images(ctx, r, id, languages)
}

func TestImagePreferenceOrderingConfirmationAndProviderIsolation(t *testing.T) {
	stored := domain.MetadataImages{Resource: "movie", ProviderID: 12, ImageLanguages: []string{"en", "zh", "null"}, Candidates: []domain.MetadataImageCandidate{
		{Kind: "poster", Language: "zh", FilePath: "/z.jpg", VoteAverage: 10},
		{Kind: "poster", Language: "en", FilePath: "/b.jpg", VoteAverage: 8, VoteCount: 1},
		{Kind: "poster", Language: "en", FilePath: "/a.jpg", VoteAverage: 8, VoteCount: 1},
		{Kind: "poster", Language: "fr", FilePath: "/fr.jpg", VoteAverage: 10},
		{Kind: "backdrop", Language: "null", FilePath: "/background.jpg"},
	}}
	service, _ := NewMetadata(testImageProvider{images: func(_ context.Context, _ string, _ int32, languages []string) (domain.MetadataImages, error) {
		languages[0] = "ja"
		return stored, nil
	}})
	languages := []string{"en", "zh", "null"}
	v, err := service.Images(context.Background(), "movie", 12, languages)
	if err != nil || len(v.Candidates) != 4 || v.Candidates[0].FilePath != "/a.jpg" || v.Candidates[1].FilePath != "/b.jpg" || v.Candidates[2].Language != "zh" || v.Candidates[3].Kind != "backdrop" {
		t.Fatalf("ordered=%+v error=%v", v, err)
	}
	for _, image := range v.Candidates {
		if !image.NeedsConfirmation {
			t.Fatal("image automatically selected")
		}
	}
	v.Candidates[0].FilePath = "changed"
	v.ImageLanguages[0] = "null"
	if stored.Candidates[0].FilePath != "/z.jpg" || stored.Candidates[0].NeedsConfirmation || stored.ImageLanguages[0] != "en" || languages[0] != "en" {
		t.Fatal("app mutated provider or caller arrays")
	}
}

func TestImageApplicationValidationAndSafeErrors(t *testing.T) {
	for _, tc := range []struct{ source, want error }{{domain.ErrNotFound, domain.ErrNotFound}, {context.Canceled, context.Canceled}, {context.DeadlineExceeded, context.DeadlineExceeded}, {errors.New("secret://127.0.0.1"), domain.ErrMetadataUnavailable}} {
		service, _ := NewMetadata(testImageProvider{images: func(context.Context, string, int32, []string) (domain.MetadataImages, error) {
			return domain.MetadataImages{}, tc.source
		}})
		if _, err := service.Images(context.Background(), "series", 12, []string{"en"}); err != tc.want {
			t.Fatal("unsafe image error")
		}
	}
	for _, languages := range [][]string{nil, {}, {"en", "en"}, {"en-US"}, {"fr"}, {"zh", "ja", "en", "null", "en"}} {
		if domain.ValidMetadataImageLanguages(languages) {
			t.Fatalf("invalid preference accepted %v", languages)
		}
	}
	if !reflect.DeepEqual(domain.DefaultMetadataImageLanguages("ja-JP"), []string{"ja", "en", "null"}) || !reflect.DeepEqual(domain.DefaultMetadataImageLanguages("en-US"), []string{"en", "null"}) {
		t.Fatal("profile image defaults differ")
	}
}
