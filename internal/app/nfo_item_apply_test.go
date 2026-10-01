package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type nfoApplyTestRepository struct {
	ItemMetadataRepository
	scope        domain.NFOItemScope
	writes       int
	resolveError error
}

func (r *nfoApplyTestRepository) ResolveItemNFO(context.Context, domain.Actor, string, int64) (domain.NFOItemScope, error) {
	return r.scope, r.resolveError
}

func (r *nfoApplyTestRepository) ApplyItemNFO(_ context.Context, _ domain.Actor, scope domain.NFOItemScope, fields domain.NFOItemFields) (domain.MetadataApplyResult, error) {
	r.writes++
	if scope != r.scope || !domain.ValidNFOItemFields(fields) {
		return domain.MetadataApplyResult{}, domain.ErrInvalid
	}
	return domain.MetadataApplyResult{Metadata: domain.ItemMetadata{ItemID: scope.ItemID, Revision: scope.Revision + 1}, Applied: []string{"title"}}, nil
}

type nfoApplyTestReader struct {
	value  domain.NFOItemFields
	calls  int
	change string
}

func (r *nfoApplyTestReader) ReadItemFields(_ context.Context, _ domain.NFOSource, _ string) (domain.NFOItemFields, error) {
	r.calls++
	value := r.value
	value.Fields = append([]domain.NFOTextField{}, value.Fields...)
	if r.calls == 2 {
		switch r.change {
		case "hash":
			value.Stamp.SHA256 = strings.Repeat("b", 64)
		case "value":
			value.Fields[0].Value = "Different"
		case "missing":
			return domain.NFOItemFields{}, domain.ErrNFOInputUnavailable
		}
	}
	return value, nil
}

func TestNFOApplyUsesAuthorizedSourceAndRefusesChangedObservations(t *testing.T) {
	id := "11111111-1111-4111-8111-111111111111"
	actor := domain.Actor{UserID: id, SessionID: id}
	for _, change := range []string{"", "hash", "value", "missing", "authorization", "unconfirmed"} {
		t.Run(change, func(t *testing.T) {
			repo := &nfoApplyTestRepository{scope: domain.NFOItemScope{ItemID: id, LibraryID: id, SourceID: id, RootID: id, Kind: "Movie", Revision: 1, Generation: 1, MediaPath: "film.mkv", Source: domain.NFOSource{RootPath: "/trusted", RelativePath: "film.nfo"}}}
			reader := &nfoApplyTestReader{change: change, value: domain.NFOItemFields{Version: domain.NFOItemFieldsVersion, Kind: "Movie", Identity: domain.DefaultNFOIdentity(), Stamp: domain.NFOStamp{Size: 10, SHA256: strings.Repeat("a", 64), FingerprintVersion: domain.NFOFingerprintVersion}, ReadAt: time.Now().UTC(), Fields: []domain.NFOTextField{{Field: "title", Value: "Film"}}}}
			if change == "authorization" {
				repo.resolveError = domain.ErrUnauthenticated
			}
			base, _ := NewLocalMetadata(repo)
			service, _ := base.WithNFOItemFields(reader)
			result, err := service.ApplyNFO(context.Background(), actor, id, 1, change != "unconfirmed")
			if change == "" {
				if err != nil || result.Metadata.Revision != 2 || reader.calls != 2 || repo.writes != 1 {
					t.Fatal("confirmed NFO not applied", err)
				}
			} else if err == nil || repo.writes != 0 {
				t.Fatal("unsafe NFO observation committed", err)
			}
			if (change == "authorization" || change == "unconfirmed") && reader.calls != 0 {
				t.Fatal("file read before authorization/confirmation")
			}
		})
	}
}
