package postgres

import (
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// The ingestion migration marks every pre-existing reference except
// embedded covers manual, so scan maintenance never takes over rows a person
// or an import wrote; the downgrade only drops the flag and the index.
func TestItemImageIngestMigrationRoundTrip(t *testing.T) {
	f := newJobFixture(t)
	item := metadataItem(t, f)
	dsn := f.s.Pool.Config().ConnString()
	previous := migrationVersion(t, "item_image_ingest") - 1
	downgrade := func() {
		t.Helper()
		version, dirty, err := Migrate(f.ctx, dsn, "status")
		for err == nil && !dirty && version > previous {
			version, dirty, err = Migrate(f.ctx, dsn, "down")
		}
		if err != nil || dirty || version != previous {
			t.Fatal("downgrade", version, dirty, err)
		}
	}
	downgrade()
	imageRepositoryExec(t, f, `INSERT INTO item_images(item_id,library_id,image_type,image_index,source_kind,root_id,relative_path,remote_url) VALUES
 ($1::uuid,$2::uuid,'Primary',0,'local',$3::uuid,'Movie/poster.jpg',NULL),
 ($1::uuid,$2::uuid,'Primary',0,'remote',NULL,NULL,'https://img.example/p.jpg'),
 ($1::uuid,$2::uuid,'Primary',0,'embedded',$3::uuid,'Movie/Movie.mkv',NULL)`, item, f.registration.Library.ID, f.registration.RootID)
	if version, dirty, err := Migrate(f.ctx, dsn, "up"); err != nil || dirty || version != SchemaVersion {
		t.Fatal("upgrade", version, dirty, err)
	}
	if n := syncCount(t, f, `SELECT count(*) FROM item_images WHERE manual=(source_kind<>'embedded')`); n != 3 {
		t.Fatal("pre-existing rows not classified", n)
	}
	for file, want := range map[string]string{"poster.jpg": "", "Movie/poster.jpg": "Movie", "Show/Season 01/folder.jpg": "Show/Season 01"} {
		var got string
		if err := f.s.Pool.QueryRow(f.ctx, `SELECT inventory_parent_directory($1)`, file).Scan(&got); err != nil || got != want {
			t.Fatal("parent directory", file, got, err)
		}
		if parentDirectory(file) != want {
			t.Fatal("parent directory differs from the SQL function", file)
		}
	}
	// A manual row stays manual through a non-manual refresh.
	upsert, err := f.s.UpsertItemImage(f.ctx, f.a, domain.ItemImageInput{ItemID: item, Type: "Logo", SourceKind: domain.ImageSourceLocal, RootID: f.registration.RootID, RelativePath: "Movie/logo.png", Manual: true})
	if err != nil || !upsert.Image.Manual || upsert.Image.Locked {
		t.Fatal("manual upsert", err, upsert.Image.Manual)
	}
	tx, err := f.s.Pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	refresh, err := upsertItemImage(f.ctx, tx, domain.Actor{}, domain.ItemImageInput{ItemID: item, Type: "Logo", SourceKind: domain.ImageSourceLocal, RootID: f.registration.RootID, RelativePath: "Movie/other.png"})
	_ = tx.Rollback(f.ctx)
	if err != nil || !refresh.Skipped || refresh.Image.RelativePath != "Movie/logo.png" {
		t.Fatal("a scan refresh replaced a manual row", err, refresh.Skipped)
	}
	downgrade()
	if syncCount(t, f, `SELECT count(*) FROM item_images`) != 4 ||
		syncCount(t, f, `SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='item_images' AND column_name='manual'`) != 0 {
		t.Fatal("downgrade must keep every row and drop only the flag")
	}
	if version, dirty, err := Migrate(f.ctx, dsn, "up"); err != nil || dirty || version != SchemaVersion {
		t.Fatal("upgrade", version, dirty, err)
	}
}
