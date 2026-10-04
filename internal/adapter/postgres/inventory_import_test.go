package postgres

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/adapter/scan"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestInventoryImportActualCLI(t *testing.T) {
	ctx, store, dsn := accountTestStore(t)
	if _, err := store.BootstrapAdmin(ctx, accountInput("inventory-import")); err != nil {
		t.Fatal(err)
	}
	actor := accountActor(accountLogin(t, ctx, store, "inventory-import"))
	root := t.TempDir()
	original := []byte("original video remains unchanged")
	if err := os.WriteFile(filepath.Join(root, "movie.mkv"), original, 0600); err != nil {
		t.Fatal(err)
	}
	registration, err := store.RegisterLibrary(ctx, "Inventory import", root)
	if err != nil {
		t.Fatal(err)
	}
	fixture := jobFixture{ctx: ctx, s: store, a: actor, registration: registration, policy: jobTestPolicy()}
	job := fixture.submit(t, "import-scan")
	lease := fixture.claim(t, "inventory-import-worker")
	directory := fixture.directory(t, lease)
	if err := scan.New().ScanDirectory(ctx, directory, func(batch domain.ScanBatch) error { return store.SaveScanBatch(ctx, lease, directory, batch) }); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishJob(ctx, lease, domain.JobSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	entries, err := store.ListInventory(ctx, actor, job.ID, "", 10)
	if err != nil || len(entries) != 1 {
		t.Fatal("missing scan entry", err)
	}
	binary := filepath.Join(t.TempDir(), "jelee-cli")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", binary, "../../../cmd/jelee-cli") //nolint:staticcheck // SA1019: test helpers build with the toolchain running the test; project wrappers export GOROOT
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v %s", err, output)
	}
	credential := filepath.Join(t.TempDir(), "database-url")
	if err := os.WriteFile(credential, []byte(dsn), 0600); err != nil {
		t.Fatal(err)
	}
	run := func() error {
		command := exec.CommandContext(ctx, binary, "import-inventory", "--job", job.ID, "--entry", entries[0].ID, "--title", "Scanned movie", "--kind", "Movie")
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "JELEE_") {
				command.Env = append(command.Env, entry)
			}
		}
		command.Env = append(command.Env, "JELEE_DATABASE_URL_FILE="+credential)
		return command.Run()
	}
	if err := run(); err != nil {
		t.Fatal("actual CLI cannot import completed scan candidate", err)
	}
	var item string
	if err := store.Pool.QueryRow(ctx, `SELECT i.id::text FROM items i JOIN media_sources s ON s.item_id=i.id WHERE s.root_id=$1::uuid AND s.relative_path='movie.mkv' AND i.title='Scanned movie' AND i.kind='Movie'`, registration.RootID).Scan(&item); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetItem(ctx, actor.UserID, item); err != nil {
		t.Fatal("imported item absent from catalog", err)
	}
	if err := run(); err == nil {
		t.Fatal("duplicate scan import accepted")
	}
	var count int
	if err := store.Pool.QueryRow(ctx, `SELECT count(*) FROM items`).Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate left extra items", count, err)
	}
	got, err := os.ReadFile(filepath.Join(root, "movie.mkv"))
	if err != nil || string(got) != string(original) {
		t.Fatal("original media changed")
	}
}

func TestInventoryImportRejectsStaleCandidates(t *testing.T) {
	for _, scenario := range []string{"new scan", "generation", "baseline revision", "retained excluded", "review", "failed", "attributes", "foreign entry", "snapshot"} {
		t.Run(scenario, func(t *testing.T) {
			f := newJobFixture(t)
			job := f.complete(t, "accepted", []string{"film.mkv"}, 0)
			entries, err := f.s.ListInventory(f.ctx, f.a, job.ID, "", 10)
			if err != nil || len(entries) != 1 {
				t.Fatal(err)
			}
			source, err := f.s.ResolveInventoryImport(f.ctx, job.ID, entries[0].ID)
			if err != nil {
				t.Fatal("valid source", err)
			}
			var query string
			switch scenario {
			case "new scan":
				f.submit(t, "newer")
			case "generation":
				query = `UPDATE libraries SET inventory_generation=inventory_generation+1`
			case "baseline revision":
				query = `UPDATE libraries SET inventory_baseline_revision=inventory_baseline_revision+1`
			case "retained excluded":
				query = `UPDATE library_inventory_baseline SET observed_revision=1`
			case "review":
				query = `UPDATE jobs SET review_required=true`
			case "failed":
				query = `UPDATE jobs SET state='failed'`
			case "attributes":
				query = `UPDATE library_inventory_baseline SET size=size+1`
			case "foreign entry":
				source.EntryID = source.RootID
			case "snapshot":
				source.Size++
			}
			if query != "" {
				if _, err := f.s.Pool.Exec(f.ctx, query); err != nil {
					t.Fatal("fixture mutation", err)
				}
			}
			if _, err := f.s.ImportInventoryVideo(f.ctx, source, "Film", "Movie", ""); err == nil {
				t.Fatal("stale candidate imported")
			}
			var count int
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM items`).Scan(&count); err != nil || count != 0 {
				t.Fatal("rejection left item", count, err)
			}
		})
	}
}

func TestInventoryImportAtomicRollback(t *testing.T) {
	f := newJobFixture(t)
	job := f.complete(t, "accepted", []string{"film.mkv"}, 0)
	entries, err := f.s.ListInventory(f.ctx, f.a, job.ID, "", 10)
	if err != nil || len(entries) != 1 {
		t.Fatal(err)
	}
	source, err := f.s.ResolveInventoryImport(f.ctx, job.ID, entries[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"items", "media_sources", "audit_logs"} {
		t.Run(table, func(t *testing.T) {
			if _, err := f.s.Pool.Exec(f.ctx, `CREATE OR REPLACE FUNCTION reject_import() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected import failure'; END $$; CREATE TRIGGER reject_import BEFORE INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION reject_import()`); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.ImportInventoryVideo(f.ctx, source, "Film", "Movie", ""); !errors.Is(err, domain.ErrDatabase) {
				t.Fatal("fault did not reject", err)
			}
			if _, err := f.s.Pool.Exec(f.ctx, `DROP TRIGGER reject_import ON `+table); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM items)+(SELECT count(*) FROM media_sources)+(SELECT count(*) FROM audit_logs WHERE event='inventory.imported')`).Scan(&count); err != nil || count != 0 {
				t.Fatal("partial import", count, err)
			}
		})
	}
	if _, err := f.s.ImportInventoryVideo(f.ctx, source, "Film", "Movie", ""); err != nil {
		t.Fatal("recovered import", err)
	}
}
