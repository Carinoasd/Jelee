package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// This barrier models the gap occupied by file verification. The actual file
// verifier and real scan are independently exercised by TestInventoryImportActualTLS.
type inventoryAuthorizationBarrier struct{ after func() error }

func (b inventoryAuthorizationBarrier) VerifyInventoryImport(context.Context, domain.InventoryImportSource) error {
	return b.after()
}

func TestInventoryImportRechecksAuthorityAfterVerification(t *testing.T) {
	for _, scenario := range []string{"revoked", "disabled", "demoted", "expired"} {
		t.Run(scenario, func(t *testing.T) {
			f := newJobFixture(t)
			job := f.complete(t, "accepted", []string{"film.mkv"}, 0)
			entries, err := f.s.ListInventory(f.ctx, f.a, job.ID, "", 10)
			if err != nil || len(entries) != 1 {
				t.Fatal(err)
			}
			base, err := app.NewJobs(f.s, f.policy)
			if err != nil {
				t.Fatal(err)
			}
			verifier := inventoryAuthorizationBarrier{after: func() error {
				var query string
				switch scenario {
				case "revoked":
					query = `UPDATE sessions SET revoked_at=clock_timestamp()`
				case "disabled":
					query = `UPDATE users SET disabled=true`
				case "demoted":
					query = `UPDATE users SET is_admin=false`
				case "expired":
					query = `UPDATE sessions SET expires_at=clock_timestamp()-interval '1 second'`
				}
				_, err := f.s.Pool.Exec(f.ctx, query)
				return err
			}}
			service, err := app.NewJobsWithInventoryImport(base, f.s, verifier)
			if err != nil {
				t.Fatal(err)
			}
			_, err = service.ImportInventory(f.ctx, f.a, job.ID, entries[0].ID, domain.InventoryImportInput{Title: "Film", Kind: "Movie"})
			if !errors.Is(err, domain.ErrUnauthenticated) && !errors.Is(err, domain.ErrForbidden) {
				t.Fatal("authority change did not reject", err)
			}
			var count int
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM items)+(SELECT count(*) FROM media_sources)+(SELECT count(*) FROM audit_logs WHERE event='inventory.imported')`).Scan(&count); err != nil || count != 0 {
				t.Fatal("revoked request wrote data", count, err)
			}
		})
	}
}

func TestInventoryImportConcurrentPUT(t *testing.T) {
	f := newJobFixture(t)
	job := f.complete(t, "accepted", []string{"film.mkv"}, 0)
	entries, err := f.s.ListInventory(f.ctx, f.a, job.ID, "", 10)
	if err != nil || len(entries) != 1 {
		t.Fatal(err)
	}
	source, err := f.s.ResolveAuthorizedInventoryImport(f.ctx, f.a, job.ID, entries[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	type answer struct {
		value domain.InventoryImportResult
		err   error
	}
	answers := make(chan answer, 6)
	var group sync.WaitGroup
	for range 6 {
		group.Add(1)
		go func() {
			defer group.Done()
			value, err := f.s.PutInventoryVideo(f.ctx, f.a, source, domain.InventoryImportInput{Title: "Film", Kind: "Movie"})
			answers <- answer{value, err}
		}()
	}
	group.Wait()
	close(answers)
	var first domain.InventoryImportResult
	for answer := range answers {
		if answer.err != nil {
			t.Fatal(answer.err)
		}
		if first.ItemID == "" {
			first = answer.value
		}
		if first != answer.value {
			t.Fatal("concurrent PUT returned different items")
		}
	}
	var count int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM items)+(SELECT count(*) FROM media_sources)+(SELECT count(*) FROM audit_logs WHERE event='inventory.imported')`).Scan(&count); err != nil || count != 3 {
		t.Fatal("concurrent PUT repeated writes", count, err)
	}
}
