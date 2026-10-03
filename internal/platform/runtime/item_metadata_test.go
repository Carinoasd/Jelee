package runtime

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

type runtimeMetadataRepository struct {
	app.ItemMetadataRepository
	app.MetadataPreferencesRepository
	calls int
}

func (r *runtimeMetadataRepository) ItemMetadata(_ context.Context, _ domain.Actor, item string) (domain.ItemMetadata, error) {
	r.calls++
	return domain.ItemMetadata{ItemID: item, Revision: 1, Fields: []domain.ItemMetadataField{{Field: "title", Value: "Existing", Source: "existing"}}}, nil
}

func TestItemMetadataProductionBindingWithAndWithoutProvider(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	for _, key := range []string{"", strings.Repeat("a", 32)} {
		l := newLifetime(slog.New(slog.NewTextHandler(io.Discard, nil)))
		original, err := prepareMetadata(key, l)
		if err != nil {
			t.Fatal(err)
		}
		repo := &runtimeMetadataRepository{}
		service, err := bindMetadata(original, repo)
		if err != nil || !service.HasItemMetadata() || service.HasProvider() != (key != "") {
			t.Fatal("runtime metadata binding missing", err)
		}
		if original.HasItemMetadata() {
			t.Fatal("binding mutated original service")
		}
		value, err := service.ItemFields(context.Background(), domain.Actor{UserID: id, SessionID: id}, id)
		if err != nil || repo.calls != 1 || value.Fields[0].Value != "Existing" {
			t.Fatal("production item repository not reached", err)
		}
		l.closePool()
	}
}
