package postgres

import (
	"errors"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// TestPlaybackSelectionMigrationRoundTrip: schema 99 adds the version
// preference and session track choice tables with their checks, and drops
// them on downgrade (G20.2, G20.4).
func TestPlaybackSelectionMigrationRoundTrip(t *testing.T) {
	f := newJobFixture(t)
	dsn := f.s.Pool.Config().ConnString()
	height, rangeSDR := 1080, domain.VersionRangeSDR
	if _, err := f.s.SetVersionPreference(f.ctx, f.a, domain.VersionPreference{MaxHeight: &height, DynamicRange: &rangeSDR}); err != nil {
		t.Fatal(err)
	}
	p, err := f.s.VersionPreference(f.ctx, f.a)
	if err != nil || p.MaxHeight == nil || *p.MaxHeight != 1080 || *p.DynamicRange != "sdr" || p.PreferSmaller != nil || p.MaxBitrate != nil {
		t.Fatalf("stored preference %+v %v", p, err)
	}
	for name, query := range map[string]string{
		"height":       `INSERT INTO user_version_preferences(user_id,max_height) VALUES($1::uuid,1000) ON CONFLICT(user_id) DO UPDATE SET max_height=1000`,
		"empty":        `UPDATE user_version_preferences SET max_height=NULL,dynamic_range=NULL WHERE user_id=$1::uuid`,
		"bad subtitle": `INSERT INTO session_track_selections(user_id,item_id,source_id,play_key,subtitle_track) SELECT $1::uuid,gen_random_uuid(),gen_random_uuid(),'k','on'`,
	} {
		if _, err := f.s.Pool.Exec(f.ctx, query, f.a.UserID); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if err := f.s.RecordSessionTracks(f.ctx, f.a, "11111111-1111-4111-8111-111111111111", "k", domain.SessionTracks{SourceID: "11111111-1111-4111-8111-111111111111", AudioTrack: "e:1"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("choice for a missing item", err)
	}
	if err := f.s.RecordSessionTracks(f.ctx, f.a, "11111111-1111-4111-8111-111111111111", "k", domain.SessionTracks{SourceID: "11111111-1111-4111-8111-111111111111", SubtitleTrack: "on"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("malformed choice", err)
	}
	if _, err = f.s.SetVersionPreference(f.ctx, f.a, domain.VersionPreference{}); err != nil {
		t.Fatal(err)
	}
	if p, err = f.s.VersionPreference(f.ctx, f.a); err != nil || !p.Empty() {
		t.Fatal("empty preference not removed", p, err)
	}
	if _, err = f.s.SetVersionPreference(f.ctx, f.a, domain.VersionPreference{MaxHeight: &height}); err != nil {
		t.Fatal(err)
	}
	want := downgradeAboveMigration(t, f, "playback_selection")
	if version, dirty, err := Migrate(f.ctx, dsn, "down"); err != nil || dirty || version != want-1 {
		t.Fatalf("downgrade: %d %t %v", version, dirty, err)
	}
	if syncCount(t, f, `SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name IN ('user_version_preferences','session_track_selections')`) != 0 {
		t.Fatal("downgrade left the tables")
	}
	if version, dirty, err := Migrate(f.ctx, dsn, "up"); err != nil || dirty || version != SchemaVersion {
		t.Fatalf("upgrade: %d %t %v", version, dirty, err)
	}
	if p, err = f.s.VersionPreference(f.ctx, f.a); err != nil || !p.Empty() {
		t.Fatal("preference after round trip", p, err)
	}
}
