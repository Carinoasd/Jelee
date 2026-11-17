package postgres

import (
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// G40.7: the dominant color (schema 97) is stored, compared and read with
// the rest of the content, needs content like the average color, and its
// downgrade keeps every row.
func TestItemImageDominantColorPostgres(t *testing.T) {
	f, item := itemImageFixture(t)
	in := itemImageLocal(f, item, "poster.jpg")
	in.Content = itemImageContent("colors")
	dominant := 0x0a1b2c
	in.Content.DominantColor = &dominant
	created := itemImageUpsert(t, f, in)
	if c := created.Image.Content; c == nil || c.DominantColor == nil || *c.DominantColor != dominant || *c.AverageColor != 0x336699 {
		t.Fatal("dominant color not stored")
	}
	listed, err := f.s.ListItemImages(f.ctx, f.a, item)
	if err != nil || len(listed) != 1 || listed[0].Content.DominantColor == nil || *listed[0].Content.DominantColor != dominant {
		t.Fatal("dominant color not listed", err)
	}
	// A new color alone is a content change.
	other := 0x112233
	in.Content.DominantColor = &other
	if changed := itemImageUpsert(t, f, in); *changed.Image.Content.DominantColor != other || changed.Image.UpdatedAt.Equal(created.Image.UpdatedAt) {
		t.Fatal("dominant color change ignored")
	}
	// The schema refuses a color without content and outside 0xRRGGBB.
	for name, statement := range map[string]string{
		"without content": `INSERT INTO item_images(item_id,library_id,image_type,image_index,source_kind,remote_url,dominant_color) VALUES ($1::uuid,$2::uuid,'Logo',0,'remote','https://img.example/l.png',1)`,
		"out of range": `INSERT INTO item_images(item_id,library_id,image_type,image_index,source_kind,remote_url,content_sha256,format,byte_size,fetched_at,dominant_color)
 VALUES ($1::uuid,$2::uuid,'Logo',0,'remote','https://img.example/l.png',decode(repeat('ab',32),'hex'),'png',1,now(),16777216)`,
	} {
		_, err := f.s.Pool.Exec(f.ctx, statement, item, f.registration.Library.ID)
		if itemImageSQLState(err) != "23514" {
			t.Fatal(name, "accepted", err)
		}
	}
	invalid := -1
	in.Content.DominantColor = &invalid
	if _, err := f.s.UpsertItemImage(f.ctx, f.a, in); err != domain.ErrInvalid {
		t.Fatal("invalid dominant color passed validation", err)
	}

	dsn := f.s.Pool.Config().ConnString()
	previous := downgradeAboveMigration(t, f, "item_image_dominant_color")
	if version, dirty, err := Migrate(f.ctx, dsn, "down"); err != nil || dirty || version >= previous {
		t.Fatal("downgrade", version, dirty, err)
	}
	if syncCount(t, f, `SELECT count(*) FROM item_images`) != 1 ||
		syncCount(t, f, `SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='item_images' AND column_name='dominant_color'`) != 0 {
		t.Fatal("downgrade must keep the row and drop only the column")
	}
	if version, dirty, err := Migrate(f.ctx, dsn, "up"); err != nil || dirty || version != SchemaVersion {
		t.Fatal("upgrade", version, dirty, err)
	}
	if listed, err := f.s.ListItemImages(f.ctx, f.a, item); err != nil || len(listed) != 1 || listed[0].Content.DominantColor != nil || listed[0].Content.AverageColor == nil {
		t.Fatal("upgrade did not leave the dominant color unknown", err)
	}
}
