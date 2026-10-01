package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

const (
	catalogUserID = "12345678-1234-1234-1234-123456789abc"
	catalogItemID = "abcdef12-abcd-abcd-abcd-abcdef123456"
)

type catalogRepositoryStub struct {
	list func(context.Context, string, string, int) ([]domain.Item, error)
	get  func(context.Context, string, string) (domain.Item, error)
}

func (s catalogRepositoryStub) ListItems(ctx context.Context, userID, cursor string, limit int) ([]domain.Item, error) {
	return s.list(ctx, userID, cursor, limit)
}

func (s catalogRepositoryStub) GetItem(ctx context.Context, userID, id string) (domain.Item, error) {
	return s.get(ctx, userID, id)
}

func TestListRejectsInvalidBoundsBeforeStorage(t *testing.T) {
	calls := 0
	catalog := NewCatalog(catalogRepositoryStub{list: func(context.Context, string, string, int) ([]domain.Item, error) {
		calls++
		return nil, nil
	}})
	for _, tc := range []struct {
		name, userID, cursor string
		limit                int
	}{
		{"missing_user", "", "", 10},
		{"untrusted_user", "../other-user", "", 10},
		{"invalid_cursor", catalogUserID, "cursor-with-sql'", 10},
		{"negative_limit", catalogUserID, "", -1},
		{"zero_limit", catalogUserID, "", 0},
		{"over_maximum", catalogUserID, "", 101},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items, err := catalog.List(context.Background(), tc.userID, tc.cursor, tc.limit)
			if !errors.Is(err, domain.ErrInvalid) || len(items) != 0 || calls != 0 {
				t.Fatalf("invalid input reached storage: items=%v err=%v calls=%d", items, err, calls)
			}
		})
	}
}

func TestListPreservesAuthorizedQueryAndContext(t *testing.T) {
	type correlationKey struct{}
	ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), correlationKey{}, "correlation"), time.Minute)
	defer cancel()
	want := []domain.Item{{ID: catalogItemID, LibraryID: "library", Title: "Visible title", Kind: "Movie"}}
	for _, tc := range []struct {
		cursor string
		limit  int
	}{{"", 1}, {catalogItemID, 100}} {
		calls := 0
		catalog := NewCatalog(catalogRepositoryStub{list: func(gotCtx context.Context, userID, cursor string, limit int) ([]domain.Item, error) {
			calls++
			if gotCtx != ctx || gotCtx.Value(correlationKey{}) != "correlation" || userID != catalogUserID || cursor != tc.cursor || limit != tc.limit {
				t.Error("repository lost authenticated query inputs or request context")
			}
			return want, nil
		}})
		got, err := catalog.List(ctx, catalogUserID, tc.cursor, tc.limit)
		if err != nil || !reflect.DeepEqual(got, want) || calls != 1 {
			t.Fatalf("list result=%v err=%v calls=%d", got, err, calls)
		}
	}
}

func TestGetMalformedIdentifiersAreHiddenWithoutLookup(t *testing.T) {
	calls := 0
	catalog := NewCatalog(catalogRepositoryStub{get: func(context.Context, string, string) (domain.Item, error) {
		calls++
		return domain.Item{Title: "private title"}, nil
	}})
	for _, tc := range []struct{ userID, itemID string }{
		{"", catalogItemID}, {catalogUserID, ""}, {"invalid-user", catalogItemID}, {catalogUserID, "../../secret"},
	} {
		item, err := catalog.Get(context.Background(), tc.userID, tc.itemID)
		if !errors.Is(err, domain.ErrNotFound) || item != (domain.Item{}) || calls != 0 {
			t.Fatalf("malformed identifier disclosed a resource: item=%v err=%v calls=%d", item, err, calls)
		}
	}
}

func TestGetKeepsUserScopeAndHiddenContract(t *testing.T) {
	type requestKey struct{}
	ctx := context.WithValue(context.Background(), requestKey{}, "request")
	want := domain.Item{ID: catalogItemID, LibraryID: "library", Title: "Authorized title", Kind: "Movie"}
	for _, visible := range []bool{true, false} {
		catalog := NewCatalog(catalogRepositoryStub{get: func(gotCtx context.Context, userID, itemID string) (domain.Item, error) {
			if gotCtx != ctx || userID != catalogUserID || itemID != catalogItemID {
				t.Fatal("detail lookup lost user scoping or request context")
			}
			if !visible {
				return domain.Item{}, fmt.Errorf("scoped lookup: %w", domain.ErrNotFound)
			}
			return want, nil
		}})
		got, err := catalog.Get(ctx, catalogUserID, catalogItemID)
		if visible && (err != nil || got != want) {
			t.Fatalf("authorized item changed: got=%v err=%v", got, err)
		}
		if !visible && (!errors.Is(err, domain.ErrNotFound) || got != (domain.Item{})) {
			t.Fatalf("hidden item disclosed: got=%v err=%v", got, err)
		}
	}
}

func TestRepositoryFailureIsNotReportedAsEmptySuccess(t *testing.T) {
	storageFailure := errors.New("storage unavailable")
	catalog := NewCatalog(catalogRepositoryStub{
		list: func(context.Context, string, string, int) ([]domain.Item, error) {
			return nil, fmt.Errorf("list operation: %w", storageFailure)
		},
		get: func(context.Context, string, string) (domain.Item, error) {
			return domain.Item{}, fmt.Errorf("detail operation: %w", storageFailure)
		},
	})
	if _, err := catalog.List(context.Background(), catalogUserID, "", 10); !errors.Is(err, storageFailure) {
		t.Fatalf("list swallowed repository failure: %v", err)
	}
	if _, err := catalog.Get(context.Background(), catalogUserID, catalogItemID); !errors.Is(err, storageFailure) {
		t.Fatalf("get swallowed repository failure: %v", err)
	}
}

func TestCancellationReachesRepository(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	seenList, seenGet := false, false
	catalog := NewCatalog(catalogRepositoryStub{
		list: func(got context.Context, _ string, _ string, _ int) ([]domain.Item, error) {
			seenList = got == ctx
			return nil, got.Err()
		},
		get: func(got context.Context, _ string, _ string) (domain.Item, error) {
			seenGet = got == ctx
			return domain.Item{}, got.Err()
		},
	})
	if _, err := catalog.List(ctx, catalogUserID, "", 10); !errors.Is(err, context.Canceled) || !seenList {
		t.Fatalf("list cancellation not propagated: %v", err)
	}
	if _, err := catalog.Get(ctx, catalogUserID, catalogItemID); !errors.Is(err, context.Canceled) || !seenGet {
		t.Fatalf("get cancellation not propagated: %v", err)
	}
}
