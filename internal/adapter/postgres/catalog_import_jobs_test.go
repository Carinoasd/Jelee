package postgres

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/scan"
	"github.com/MoYuanCN/Jelee/internal/domain"
	worker "github.com/MoYuanCN/Jelee/internal/platform/jobs"
)

func catalogImportFixture(t *testing.T, count int) (jobFixture, domain.Job, []domain.CatalogImportSelection) {
	t.Helper()
	f := newJobFixture(t)
	source := f.submit(t, "source-scan")
	lease := f.claim(t, "source-worker")
	directory := f.directory(t, lease)
	for i := range count {
		if err := os.WriteFile(filepath.Join(directory.RootPath, fmt.Sprintf("film-%03d.mkv", i)), []byte("original movie"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := scan.New().ScanDirectory(f.ctx, directory, func(batch domain.ScanBatch) error { return f.s.SaveScanBatch(f.ctx, lease, directory, batch) }); err != nil {
		t.Fatal(err)
	}
	if err := f.s.FinishJob(f.ctx, lease, domain.JobSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	entries, err := f.s.ListInventory(f.ctx, f.a, source.ID, "", 100)
	if err != nil || len(entries) != count {
		t.Fatal("source inventory", err)
	}
	items := make([]domain.CatalogImportSelection, len(entries))
	for i, entry := range entries {
		items[i] = domain.CatalogImportSelection{EntryID: entry.ID, Title: entry.Path, Kind: "Movie"}
	}
	return f, source, items
}

func runCatalogWorker(t *testing.T, f jobFixture, id string) domain.Job {
	t.Helper()
	options := worker.DefaultOptions()
	options.Workers = 1
	options.PollInterval = 100 * time.Millisecond
	options.CatalogImport = &worker.CatalogImportOptions{Repository: f.s, Verifier: scan.New()}
	runner, err := worker.New(f.s, scan.New(), options, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err = runner.Start(f.ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := runner.Stop(ctx); err != nil {
			t.Error(err)
		}
	}()
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		job := f.get(t, id)
		if job.State == domain.JobSucceeded || job.State == domain.JobFailed || job.State == domain.JobCancelled {
			return job
		}
		select {
		case <-deadline.C:
			t.Fatal("catalog worker did not finish")
		case <-tick.C:
		}
	}
}

func TestCatalogImportWorkerAndAdmission(t *testing.T) {
	f, source, items := catalogImportFixture(t, 100)
	job, replay, err := f.s.SubmitCatalogImport(f.ctx, f.a, source.ID, "batch", domain.JobPriorityManual, items, f.policy)
	if err != nil || replay || job.Kind != domain.JobCatalogImport {
		t.Fatal("admission", err)
	}
	again, replay, err := f.s.SubmitCatalogImport(f.ctx, f.a, source.ID, "batch", domain.JobPriorityManual, items, f.policy)
	if err != nil || !replay || again.ID != job.ID {
		t.Fatal("replay", err)
	}
	changed := append([]domain.CatalogImportSelection(nil), items...)
	changed[0].Title = "different"
	if _, _, err := f.s.SubmitCatalogImport(f.ctx, f.a, source.ID, "batch", domain.JobPriorityManual, changed, f.policy); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("different retained intent", err)
	}
	if _, err := f.s.ClaimJob(f.ctx, "legacy-worker", false, time.Minute); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("incapable worker claimed import", err)
	}
	if _, _, err := f.s.SubmitCatalogImport(f.ctx, f.a, source.ID, "busy", domain.JobPriorityManual, items, f.policy); !errors.Is(err, domain.ErrJobBusy) {
		t.Fatal("overlapping library import", err)
	}
	if _, _, err := f.s.SubmitJob(f.ctx, f.a, job.LibraryID, "batch", domain.JobPriorityManual, f.policy); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("scan replay returned import", err)
	}
	terminal := runCatalogWorker(t, f, job.ID)
	if terminal.State != domain.JobSucceeded || terminal.Files != 100 {
		t.Fatal("batch did not import", terminal.State, terminal.Files, terminal.ErrorCode)
	}
	report, err := f.s.GetCatalogImportReport(f.ctx, f.a, job.ID)
	if err != nil || report.Total != 100 || report.Completed != 100 {
		t.Fatal("progress", err)
	}
	for _, entry := range report.Entries {
		if !entry.Completed || !domain.ValidID(entry.ItemID) || !domain.ValidID(entry.SourceID) {
			t.Fatal("missing durable result")
		}
	}
	var count int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_logs WHERE event='inventory.imported'`).Scan(&count); err != nil || count != 100 {
		t.Fatal("import audit count", count, err)
	}
	nfoMigrationDenied(t, f, "000040_catalog_import.down.sql")
}

func TestCatalogImportResumeCancellationAndFence(t *testing.T) {
	f, source, items := catalogImportFixture(t, 3)
	job, _, err := f.s.SubmitCatalogImport(f.ctx, f.a, source.ID, "batch", domain.JobPriorityManual, items, f.policy)
	if err != nil {
		t.Fatal(err)
	}
	claim := func(owner string) domain.JobLease {
		t.Helper()
		lease, err := f.s.ClaimJobWithCapabilities(f.ctx, owner, false, time.Minute, domain.ScanCapabilities{CatalogImport: true})
		if err != nil {
			t.Fatal(err)
		}
		return lease
	}
	first := claim("first-worker")
	task, err := f.s.NextCatalogImport(f.ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if err := scan.New().VerifyInventoryImport(f.ctx, task.Source); err != nil {
		t.Fatal(err)
	}
	if err := f.s.CommitCatalogImport(f.ctx, first, task); err != nil {
		t.Fatal(err)
	}
	if err := f.s.ReleaseJob(f.ctx, first); err != nil {
		t.Fatal(err)
	}
	second := claim("replacement-worker")
	next, err := f.s.NextCatalogImport(f.ctx, second)
	if err != nil || next.Sequence != 2 {
		t.Fatal("resume repeated completed entry", err)
	}
	if err := f.s.CommitCatalogImport(f.ctx, first, task); !errors.Is(err, domain.ErrJobLeaseLost) {
		t.Fatal("stale owner committed", err)
	}
	if _, err := f.s.CancelJob(f.ctx, f.a, job.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.s.CommitCatalogImport(f.ctx, second, next); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled entry committed", err)
	}
	if err := f.s.FinishCatalogImport(f.ctx, second, domain.JobCancelled, ""); err != nil {
		t.Fatal(err)
	}
	report, err := f.s.GetCatalogImportReport(f.ctx, f.a, job.ID)
	if err != nil || report.Completed != 1 {
		t.Fatal("cancel lost durable prefix", err)
	}
	retry, _, err := f.s.SubmitCatalogImport(f.ctx, f.a, source.ID, "retry-confirmation", domain.JobPriorityManual, items, f.policy)
	if err != nil {
		t.Fatal(err)
	}
	if terminal := runCatalogWorker(t, f, retry.ID); terminal.State != domain.JobSucceeded {
		t.Fatal("resubmission failed", terminal.ErrorCode)
	}
	var count int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM items`).Scan(&count); err != nil || count != 3 {
		t.Fatal("retry duplicated items", count, err)
	}
}

func TestCatalogImportProgressRollbackAndLeaseExpiry(t *testing.T) {
	for _, slow := range []bool{false, true} {
		t.Run(fmt.Sprint(slow), func(t *testing.T) {
			f, source, items := catalogImportFixture(t, 1)
			if _, _, err := f.s.SubmitCatalogImport(f.ctx, f.a, source.ID, "batch", domain.JobPriorityManual, items, f.policy); err != nil {
				t.Fatal(err)
			}
			lease, err := f.s.ClaimJobWithCapabilities(f.ctx, "fault-worker", false, time.Second, domain.ScanCapabilities{CatalogImport: true})
			if err != nil {
				t.Fatal(err)
			}
			task, err := f.s.NextCatalogImport(f.ctx, lease)
			if err != nil {
				t.Fatal(err)
			}
			body := "RAISE EXCEPTION 'injected progress failure';"
			if slow {
				body = "PERFORM pg_sleep(1.1); RETURN NEW;"
			}
			if _, err := f.s.Pool.Exec(f.ctx, `CREATE FUNCTION import_progress_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+body+` END $$; CREATE TRIGGER import_progress_fault BEFORE UPDATE ON catalog_import_entries FOR EACH ROW EXECUTE FUNCTION import_progress_fault()`); err != nil {
				t.Fatal(err)
			}
			err = f.s.CommitCatalogImport(f.ctx, lease, task)
			want := domain.ErrDatabase
			if slow {
				want = domain.ErrJobLeaseLost
			}
			if !errors.Is(err, want) {
				t.Fatal("fault did not reject", err)
			}
			var count int
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM items)+(SELECT count(*) FROM catalog_import_entries WHERE completed)+(SELECT count(*) FROM audit_logs WHERE event='inventory.imported')`).Scan(&count); err != nil || count != 0 {
				t.Fatal("partial item/checkpoint survived", count, err)
			}
		})
	}
}

func TestCatalogImportPinsActiveSource(t *testing.T) {
	f, source, items := catalogImportFixture(t, 1)
	policy := f.policy
	policy.HistoryLimit = 1
	if _, _, err := f.s.SubmitCatalogImport(f.ctx, f.a, source.ID, "batch", domain.JobPriorityManual, items, policy); err != nil {
		t.Fatal(err)
	}
	other, err := f.s.RegisterLibrary(f.ctx, "other-library", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	otherFixture := f
	otherFixture.registration = other
	otherFixture.policy = policy
	otherFixture.complete(t, "other-scan", []string{"other.mkv"}, 0)
	if _, err := f.s.GetJob(f.ctx, f.a, source.ID); err != nil {
		t.Fatal("history cleanup removed active source", err)
	}
}

func TestCatalogImportChangedSourceFailsWithoutWrites(t *testing.T) {
	f, source, items := catalogImportFixture(t, 1)
	candidate, err := f.s.ResolveInventoryImport(f.ctx, source.ID, items[0].EntryID)
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := f.s.SubmitCatalogImport(f.ctx, f.a, source.ID, "batch", domain.JobPriorityManual, items, f.policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candidate.RootPath, candidate.Path), []byte("changed size after confirmation"), 0600); err != nil {
		t.Fatal(err)
	}
	terminal := runCatalogWorker(t, f, job.ID)
	if terminal.State != domain.JobFailed || terminal.ErrorCode != "catalog_import_failed" || terminal.Files != 0 {
		t.Fatal("changed file imported", terminal.State, terminal.ErrorCode)
	}
	var count int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM items`).Scan(&count); err != nil || count != 0 {
		t.Fatal("changed source left item", count, err)
	}
}

func TestCatalogImportInvalidSelections(t *testing.T) {
	f, source, items := catalogImportFixture(t, 1)
	tooMany := make([]domain.CatalogImportSelection, 101)
	for i := range tooMany {
		tooMany[i] = items[0]
	}
	invalid := append([]domain.CatalogImportSelection(nil), items...)
	invalid[0].Kind = "Series"
	for _, input := range [][]domain.CatalogImportSelection{nil, tooMany, {items[0], items[0]}, invalid} {
		if _, _, err := f.s.SubmitCatalogImport(f.ctx, f.a, source.ID, "invalid", domain.JobPriorityManual, input, f.policy); !errors.Is(err, domain.ErrInvalid) {
			t.Fatal("invalid batch accepted", err)
		}
	}
}
