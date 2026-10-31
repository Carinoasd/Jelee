//go:build linux || windows

package postgres

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/media"
	"github.com/MoYuanCN/Jelee/internal/adapter/scan"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/jobs"
	"github.com/jackc/pgx/v5/pgxpool"
)

// fakeSidecarProber stands in for the sandboxed runtime: it answers by
// relative path and reports the file's real stamp, as the adapter does.
type fakeSidecarProber struct {
	mu     sync.Mutex
	root   string
	meta   map[string]domain.MediaMetadata
	fail   map[string]error
	probed map[string]int
}

func (p *fakeSidecarProber) ProbeSidecar(ctx context.Context, source domain.ProbeSource) (domain.ProbeObservation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.probed[source.RelativePath]++
	if err := p.fail[source.RelativePath]; err != nil {
		return domain.ProbeObservation{}, err
	}
	info, err := os.Stat(filepath.Join(p.root, filepath.FromSlash(source.RelativePath)))
	if err != nil {
		return domain.ProbeObservation{}, domain.ErrProbeInputUnavailable
	}
	return domain.ProbeObservation{Metadata: p.meta[source.RelativePath], Stamp: domain.ProbeStamp{Size: info.Size(), ModifiedUnixNano: info.ModTime().UnixNano()}}, ctx.Err()
}

func (p *fakeSidecarProber) count(path string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.probed[path]
}

func startSidecarProbeWorker(t *testing.T, f jobFixture, prober *fakeSidecarProber) func() {
	t.Helper()
	opts := jobs.DefaultOptions()
	opts.Workers, opts.PollInterval, opts.ScanConcurrency = 1, 100*time.Millisecond, 2
	opts.CatalogSync = &jobs.CatalogSyncOptions{Repository: f.s, Sidecars: f.s, Inspector: scan.SidecarInspector{},
		SidecarProbes: &jobs.SidecarProbeOptions{Repository: f.s, Prober: prober}}
	runner, err := jobs.New(f.s, scan.New(), opts, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err = runner.Start(f.ctx); err != nil {
		t.Fatal(err)
	}
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := runner.Stop(ctx); err != nil {
			t.Error(err)
		}
	}
}

type probedTrack struct {
	id, state, codec, kind, paired, charset string
	channels                                int
	probedSize                              *int64
}

func probedTracks(t *testing.T, f jobFixture) map[string]probedTrack {
	t.Helper()
	rows, err := f.s.Pool.Query(f.ctx, `SELECT relative_path,id::text,probe_state,COALESCE(codec,''),COALESCE(subtitle_kind,''),COALESCE(paired_track_id::text,''),
 COALESCE(charset,''),COALESCE(channels,0),probed_size FROM media_sidecar_tracks`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]probedTrack{}
	for rows.Next() {
		var path string
		var v probedTrack
		if err = rows.Scan(&path, &v.id, &v.state, &v.codec, &v.kind, &v.paired, &v.charset, &v.channels, &v.probedSize); err != nil {
			t.Fatal(err)
		}
		out[path] = v
	}
	return out
}

func stampSidecarFile(t *testing.T, root, name string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(filepath.Join(root, filepath.FromSlash(name)), at, at); err != nil {
		t.Fatal(err)
	}
}

// B4 end to end: a catalog sync inspects and pairs ".idx" with ".sub", tells
// VobSub from MicroDVD by content, probes audio, ".sub" and ".sup" once on
// demand, and lists the facts. A failed probe keeps the track listed and is
// not retried; a changed file goes back to pending and is probed again; a
// removed ".sub" unpairs its ".idx".
func TestCatalogSyncProbesSidecarsOnDemand(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Setenv("TMPDIR", "/tmp")
	}
	f := newJobFixture(t)
	root := syncRootPath(t, f)
	vobsub := append([]byte{0, 0, 1, 0xBA, 0x44, 0, 4, 0, 4, 1, 1, 0x89, 0xC3, 0xF8}, bytes.Repeat([]byte("binary but text-like "), 200)...)
	files := map[string][]byte{
		"Movie (2001)/Movie (2001).mkv":             []byte("video"),
		"Movie (2001)/Movie (2001).en.idx":          []byte("# VobSub index file, v7\nsize: 720x480\nid: en, index: 0\n"),
		"Movie (2001)/Movie (2001).en.sub":          vobsub,
		"Movie (2001)/Movie (2001).fr.sub":          []byte("{1}{1}23.976\r\n{0}{48}Bonjour.\r\n"),
		"Movie (2001)/Movie (2001).ja.sup":          []byte("PG\x00\x00\x00\x00"),
		"Movie (2001)/Movie (2001).en.mka":          []byte("matroska-audio"),
		"Movie (2001)/Movie (2001).de.dts":          []byte("broken-dts"),
		"Movie (2001)/Movie (2001).en.srt":          []byte("1\r\n00:00:01,000 --> 00:00:02,000\r\nHello.\r\n"),
		"Movie (2001)/Movie (2001).es.ogg":          []byte("busy-ogg"),
		"Movie (2001)/Movie (2001).it.idx":          []byte("# VobSub index file, v7\n"),
		"Movie (2001)/Movie (2001).commentary.flac": []byte("fLaC"),
	}
	for name, content := range files {
		writeSidecarFile(t, root, name, content)
	}
	text := func(v string) *string { return &v }
	six, rate := int64(6), int64(48000)
	prober := &fakeSidecarProber{root: root, probed: map[string]int{},
		meta: map[string]domain.MediaMetadata{
			"Movie (2001)/Movie (2001).en.mka": {Streams: []domain.MediaStream{{Index: 0, Kind: "audio", Codec: text("truehd"), Profile: text("Dolby TrueHD + Dolby Atmos"),
				Audio: &domain.MediaAudio{Channels: &six, ChannelLayout: text("5.1(side)"), SampleRate: &rate}}}},
			"Movie (2001)/Movie (2001).ja.sup":          {Streams: []domain.MediaStream{{Index: 0, Kind: "subtitle", Codec: text("hdmv_pgs_subtitle")}}},
			"Movie (2001)/Movie (2001).en.sub":          {Streams: []domain.MediaStream{{Index: 0, Kind: "subtitle", Codec: text("dvd_subtitle")}}},
			"Movie (2001)/Movie (2001).fr.sub":          {Streams: []domain.MediaStream{{Index: 0, Kind: "subtitle", Codec: text("microdvd")}}},
			"Movie (2001)/Movie (2001).commentary.flac": {Streams: []domain.MediaStream{{Index: 0, Kind: "audio", Codec: text("flac")}}},
		},
		fail: map[string]error{"Movie (2001)/Movie (2001).de.dts": domain.ErrProbeFailed, "Movie (2001)/Movie (2001).es.ogg": domain.ErrProbeBusy},
	}
	enableAutoSync(t, f)
	stop := startSidecarProbeWorker(t, f, prober)
	defer func() { stop() }()
	f.submit(t, "sidecar-probe-1")
	waitJobsIdle(t, f)

	tracks := probedTracks(t, f)
	idx, sub, micro := tracks["Movie (2001)/Movie (2001).en.idx"], tracks["Movie (2001)/Movie (2001).en.sub"], tracks["Movie (2001)/Movie (2001).fr.sub"]
	mka, dts, ogg, srt := tracks["Movie (2001)/Movie (2001).en.mka"], tracks["Movie (2001)/Movie (2001).de.dts"], tracks["Movie (2001)/Movie (2001).es.ogg"], tracks["Movie (2001)/Movie (2001).en.srt"]
	switch {
	case len(tracks) != 10:
		t.Fatalf("tracks %d: %v", len(tracks), tracks)
	case idx.paired != sub.id || idx.state != domain.SidecarProbeNotApplicable || tracks["Movie (2001)/Movie (2001).it.idx"].paired != "":
		t.Fatalf("idx pairing %+v %+v", idx, sub)
	case sub.kind != domain.SidecarSubtitleVobSub || sub.charset != "" || sub.state != domain.SidecarProbeReady || sub.codec != "dvd_subtitle":
		t.Fatalf("vobsub .sub %+v", sub)
	case micro.kind != domain.SidecarSubtitleMicroDVD || micro.charset == "" || micro.codec != "microdvd":
		t.Fatalf("microdvd .sub %+v", micro)
	case mka.state != domain.SidecarProbeReady || mka.codec != "truehd" || mka.channels != 6 || mka.probedSize == nil:
		t.Fatalf("mka %+v", mka)
	case dts.state != domain.SidecarProbeFailed || dts.codec != "" || dts.probedSize == nil:
		t.Fatalf("failed dts %+v", dts)
	case ogg.state != domain.SidecarProbePending:
		t.Fatalf("busy ogg must stay pending %+v", ogg)
	case srt.state != domain.SidecarProbeNotApplicable || prober.count("Movie (2001)/Movie (2001).en.srt") != 0:
		t.Fatalf("text subtitle probed %+v", srt)
	}

	// Playback lists the facts, hides the paired data half and still lists
	// failed and pending tracks with their state.
	reader := trackPrincipal(t, f, "sidecar-probe-reader", access.ClientNative, true)
	actor := domain.Actor{UserID: reader.UserID, SessionID: reader.SessionID}
	source := sourceOf(t, f, "Movie (2001)/Movie (2001).mkv")
	var item string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT item_id::text FROM media_sources WHERE id=$1::uuid`, source).Scan(&item); err != nil {
		t.Fatal(err)
	}
	records, err := f.s.ListPlaybackSources(f.ctx, actor, item)
	if err != nil || len(records) != 1 {
		t.Fatal("playback sources", err)
	}
	external := map[string]domain.PlaybackExternalTrack{}
	for _, track := range domain.BuildPlaybackSource(records[0]).External {
		external[track.ID] = track
	}
	if len(external) != 9 {
		t.Fatalf("external tracks %d", len(external))
	}
	if _, listed := external[sub.id]; listed || external[idx.id].PairedTrackID != sub.id || external[idx.id].Codec != "vobsub" {
		t.Fatal("paired .sub listed or .idx unpaired")
	}
	if got := external[mka.id]; got.Codec != "truehd" || !got.Atmos || got.Channels != 6 || got.SampleRate != 48000 || got.ChannelLayout != "5.1(side)" {
		t.Fatalf("mka playback %+v", got)
	}
	if got := external[dts.id]; got.ProbeState != domain.SidecarProbeFailed || got.Codec != "dts" {
		t.Fatalf("failed dts playback %+v", got)
	}
	if got := external[ogg.id]; got.ProbeState != domain.SidecarProbePending || got.Codec != "" {
		t.Fatalf("pending ogg playback %+v", got)
	}
	// The data half stays deliverable through the subtitle route, as opaque
	// bytes; MicroDVD is text.
	if w := sidecarRoute(t, f, reader, source, media.TrackSubtitle, sub.id); w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), vobsub) ||
		w.Header().Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("vobsub route %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	if w := sidecarRoute(t, f, reader, source, media.TrackSubtitle, micro.id); w.Code != http.StatusOK || w.Header().Get("Content-Type") != "text/x-microdvd; charset="+micro.charset {
		t.Fatalf("microdvd route %d %q", w.Code, w.Header().Get("Content-Type"))
	}

	// A rescan with nothing changed probes nothing again; the failed file is
	// not retried, the busy one is.
	prober.mu.Lock()
	delete(prober.fail, "Movie (2001)/Movie (2001).es.ogg")
	prober.meta["Movie (2001)/Movie (2001).es.ogg"] = domain.MediaMetadata{Streams: []domain.MediaStream{{Index: 0, Kind: "audio", Codec: text("vorbis")}}}
	prober.mu.Unlock()
	f.submit(t, "sidecar-probe-2")
	waitJobsIdle(t, f)
	again := probedTracks(t, f)
	if prober.count("Movie (2001)/Movie (2001).en.mka") != 1 || prober.count("Movie (2001)/Movie (2001).de.dts") != 1 || again["Movie (2001)/Movie (2001).es.ogg"].codec != "vorbis" {
		t.Fatal("unchanged files probed again or pending file not retried", prober.probed)
	}

	// Changing an audio file sends it back to pending and the next pass
	// probes it again; removing the ".sub" unpairs the ".idx".
	stampSidecarFile(t, root, "Movie (2001)/Movie (2001).en.mka", time.Unix(1700000500, 0))
	stampSidecarFile(t, root, "Movie (2001)/Movie (2001).de.dts", time.Unix(1700000500, 0))
	removeSidecarFile(t, root, "Movie (2001)/Movie (2001).en.sub")
	f.submit(t, "sidecar-probe-3")
	waitJobsIdle(t, f)
	changed := probedTracks(t, f)
	if prober.count("Movie (2001)/Movie (2001).en.mka") != 2 || changed["Movie (2001)/Movie (2001).en.mka"].state != domain.SidecarProbeReady ||
		prober.count("Movie (2001)/Movie (2001).de.dts") != 2 {
		t.Fatal("changed files not probed again", prober.probed)
	}
	if v := changed["Movie (2001)/Movie (2001).en.idx"]; v.paired != "" || v.id != idx.id {
		t.Fatalf("idx still paired after its .sub was removed %+v", v)
	}
	if _, found := changed["Movie (2001)/Movie (2001).en.sub"]; found {
		t.Fatal("removed .sub kept")
	}
}

// The store side of the invariants: a result applies only to a pending row
// with the probed stamp, a changed stamp resets the probe columns, audio
// facts are dropped for subtitles, and a page is one statement.
func TestSidecarProbeStoreInvariants(t *testing.T) {
	f := newSidecarFixture(t)
	mka, sup, srt := sidecarInput(f.jobFixture, "Movie.en.mka"), sidecarInput(f.jobFixture, "Movie.ja.sup"), sidecarInput(f.jobFixture, "Movie.en.srt")
	sidecarUpsert(t, f.jobFixture, f.source, mka, sup, srt)
	var mkaID, supID, srtID string
	for path, id := range map[string]*string{"Movie/Movie.en.mka": &mkaID, "Movie/Movie.ja.sup": &supID, "Movie/Movie.en.srt": &srtID} {
		if err := f.s.Pool.QueryRow(f.ctx, `SELECT id::text FROM media_sidecar_tracks WHERE relative_path=$1`, path).Scan(id); err != nil {
			t.Fatal(err)
		}
	}
	if n := sidecarCount(t, f.jobFixture, `SELECT count(*) FROM media_sidecar_tracks WHERE probe_state='pending'`); n != 2 {
		t.Fatal("initial pending rows", n)
	}
	syncJobForProbe(t, f.jobFixture)
	lease := syncLease(t, f.jobFixture, "sidecar-probe-store")
	config := f.s.Pool.Config()
	counter := &scanQueryCounter{}
	config.ConnConfig.Tracer = counter
	pool, err := pgxpool.NewWithConfig(f.ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	counted := &Store{Pool: pool}
	before := counter.calls.Load()
	page, err := counted.NextSidecarProbes(f.ctx, lease, "", domain.SidecarProbeBatch)
	if err != nil || len(page) != 2 {
		t.Fatal("pending page", err, len(page))
	}
	pageCalls := counter.calls.Load() - before
	facts := domain.SidecarProbeFacts{Codec: "eac3", Profile: "Dolby Digital Plus", Channels: 6, ChannelLayout: "5.1(side)", SampleRate: 48000, BitRate: 640000, DurationMicros: 9}
	results := []domain.SidecarProbeResult{
		{ID: mkaID, Size: mka.Size, ModifiedUnixNano: mka.ModifiedUnixNano + 1, State: domain.SidecarProbeReady, Facts: facts}, // stale stamp
		{ID: supID, Size: sup.Size, ModifiedUnixNano: sup.ModifiedUnixNano, State: domain.SidecarProbeReady, Facts: facts},     // audio facts on a subtitle
		{ID: srtID, Size: srt.Size, ModifiedUnixNano: srt.ModifiedUnixNano, State: domain.SidecarProbeReady, Facts: facts},     // not pending
	}
	before = counter.calls.Load()
	filled, err := counted.RecordSidecarProbes(f.ctx, lease, results)
	if err != nil || filled != 1 {
		t.Fatal("record", err, filled)
	}
	recordCalls := counter.calls.Load() - before
	// One page and one record are each a fixed handful of statements
	// (transaction, lease fence, the batch statement), never one per track:
	// one result costs as many statements as three.
	before = counter.calls.Load()
	if _, err := counted.RecordSidecarProbes(f.ctx, lease, results[2:]); err != nil {
		t.Fatal(err)
	}
	if single := counter.calls.Load() - before; pageCalls > 8 || recordCalls > 9 || single != recordCalls {
		t.Fatalf("statements per page %d, per record of 3 %d, of 1 %d", pageCalls, recordCalls, single)
	}
	var state, codec string
	var channels *int
	var duration *int64
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT probe_state,COALESCE(codec,''),channels,duration_micros FROM media_sidecar_tracks WHERE id=$1::uuid`, supID).Scan(&state, &codec, &channels, &duration); err != nil ||
		state != domain.SidecarProbeReady || codec != "eac3" || channels != nil || duration == nil || *duration != 9 {
		t.Fatal("subtitle result", err, state, codec, channels)
	}
	if n := sidecarCount(t, f.jobFixture, `SELECT count(*) FROM media_sidecar_tracks WHERE id=$1::uuid AND probe_state='pending' AND codec IS NULL`, mkaID); n != 1 {
		t.Fatal("stale stamp filled a row")
	}
	if n := sidecarCount(t, f.jobFixture, `SELECT count(*) FROM media_sidecar_tracks WHERE id=$1::uuid AND probe_state='not_applicable' AND codec IS NULL`, srtID); n != 1 {
		t.Fatal("not applicable row filled")
	}
	results[0].ModifiedUnixNano = mka.ModifiedUnixNano
	if filled, err := f.s.RecordSidecarProbes(f.ctx, lease, results[:1]); err != nil || filled != 1 {
		t.Fatal("record with the probed stamp", err, filled)
	}
	if filled, err := f.s.RecordSidecarProbes(f.ctx, lease, results[:1]); err != nil || filled != 0 {
		t.Fatal("a ready row was filled twice", err, filled)
	}
	if _, err := f.s.RecordSidecarProbes(f.ctx, lease, []domain.SidecarProbeResult{{ID: mkaID, State: domain.SidecarProbePending}}); err != domain.ErrInvalid {
		t.Fatal("pending result accepted", err)
	}
	// An unchanged rescan keeps the result; a changed stamp resets it.
	sidecarUpsert(t, f.jobFixture, f.source, mka, sup, srt)
	if n := sidecarCount(t, f.jobFixture, `SELECT count(*) FROM media_sidecar_tracks WHERE id=$1::uuid AND probe_state='ready' AND codec='eac3' AND channels=6`, mkaID); n != 1 {
		t.Fatal("unchanged file lost its probe")
	}
	mka.Size++
	sidecarUpsert(t, f.jobFixture, f.source, mka, sup, srt)
	if n := sidecarCount(t, f.jobFixture, `SELECT count(*) FROM media_sidecar_tracks WHERE id=$1::uuid AND probe_state='pending' AND codec IS NULL AND channels IS NULL
 AND probed_size IS NULL AND probed_modified_unix_nano IS NULL AND profile IS NULL`, mkaID); n != 1 {
		t.Fatal("changed file kept its old probe")
	}
	// The database refuses a result for another stamp even without the
	// application's guard.
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE media_sidecar_tracks SET probe_state='ready',probed_size=size+1,probed_modified_unix_nano=modified_unix_nano WHERE id=$1::uuid`, mkaID); err == nil {
		t.Fatal("result for another stamp stored")
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE media_sidecar_tracks SET probe_state='pending' WHERE id=$1::uuid`, srtID); err == nil {
		t.Fatal("text subtitle scheduled for probing")
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE media_sidecar_tracks SET subtitle_kind='vobsub' WHERE id=$1::uuid`, supID); err == nil {
		t.Fatal("content kind on a format other than .sub")
	}
}

// The ".idx" link names a ".sub" of the same source and clears itself when
// that row goes; the pair survives an unrelated replacement.
func TestSidecarPairingFollowsTheDataHalf(t *testing.T) {
	f := newSidecarFixture(t)
	idx, sub, other := sidecarInput(f.jobFixture, "Movie.en.idx"), sidecarInput(f.jobFixture, "Movie.en.sub"), sidecarInput(f.jobFixture, "Movie.fr.sub")
	sidecarUpsert(t, f.jobFixture, f.source, idx, sub, other)
	pair := func() (string, string) {
		var paired, subID string
		if err := f.s.Pool.QueryRow(f.ctx, `SELECT COALESCE(i.paired_track_id::text,''),COALESCE((SELECT id::text FROM media_sidecar_tracks WHERE relative_path='Movie/Movie.en.sub'),'')
 FROM media_sidecar_tracks i WHERE i.relative_path='Movie/Movie.en.idx'`).Scan(&paired, &subID); err != nil {
			t.Fatal(err)
		}
		return paired, subID
	}
	if paired, subID := pair(); paired == "" || paired != subID {
		t.Fatal("idx not paired", paired, subID)
	}
	sidecarUpsert(t, f.jobFixture, f.source, idx, sub)
	if paired, subID := pair(); paired != subID {
		t.Fatal("unrelated change broke the pair")
	}
	sidecarUpsert(t, f.jobFixture, f.source, idx)
	if paired, _ := pair(); paired != "" {
		t.Fatal("pair kept after the .sub was removed", paired)
	}
	sidecarUpsert(t, f.jobFixture, f.source, idx, sub)
	if paired, subID := pair(); paired == "" || paired != subID {
		t.Fatal("re-added .sub not paired", paired, subID)
	}
	// Deleting the data half directly (any cascade path) unpairs too.
	if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM media_sidecar_tracks WHERE relative_path='Movie/Movie.en.sub'`); err != nil {
		t.Fatal(err)
	}
	if paired, _ := pair(); paired != "" {
		t.Fatal("foreign key did not clear the pair")
	}
	// A pair may not cross sources or point from a non-.idx row.
	otherSource := sidecarSource(t, f.jobFixture, f.item, "Movie/Other.mkv")
	sidecarUpsert(t, f.jobFixture, otherSource, sidecarInput(f.jobFixture, "Movie.en.sub"))
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE media_sidecar_tracks SET paired_track_id=(SELECT id FROM media_sidecar_tracks WHERE source_id=$2::uuid)
 WHERE relative_path='Movie/Movie.en.idx' AND source_id=$1::uuid`, f.source, otherSource); err == nil {
		t.Fatal("pair across sources stored")
	}
	sidecarUpsert(t, f.jobFixture, f.source, idx, sub, other)
	if tag, err := f.s.Pool.Exec(f.ctx, `UPDATE media_sidecar_tracks SET paired_track_id=(SELECT id FROM media_sidecar_tracks WHERE relative_path='Movie/Movie.en.idx' AND source_id=$1::uuid)
 WHERE relative_path='Movie/Movie.fr.sub' AND source_id=$1::uuid`, f.source); err == nil {
		t.Fatal("pair from a .sub stored", tag.RowsAffected())
	}
}

// Schema 109 forgets only derived facts on the way down: the rows stay, and
// the way back up schedules audio, ".sub" and ".sup" for probing again.
func TestSidecarProbeMigrationRoundTrip(t *testing.T) {
	f := newSidecarFixture(t)
	sidecarUpsert(t, f.jobFixture, f.source, sidecarInput(f.jobFixture, "Movie.en.idx"), sidecarInput(f.jobFixture, "Movie.en.sub"),
		sidecarInput(f.jobFixture, "Movie.ja.flac"), sidecarInput(f.jobFixture, "Movie.en.srt"))
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE media_sidecar_tracks SET probe_state='ready',probed_size=size,probed_modified_unix_nano=modified_unix_nano,codec='flac',channels=2
 WHERE format='flac'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE media_sidecar_tracks SET fingerprint=decode(repeat('ab',32),'hex')`); err != nil {
		t.Fatal(err)
	}
	want := downgradeAboveMigration(t, f.jobFixture, "sidecar_probe")
	jobMetricMigration(t, f.jobFixture, "down", want-1)
	var gone bool
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT NOT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_name='media_sidecar_tracks'
 AND column_name IN ('probe_state','codec','paired_track_id','subtitle_kind')) AND to_regclass('media_sidecar_tracks_probe_pending_idx') IS NULL`).Scan(&gone); err != nil || !gone {
		t.Fatal("downgrade left probe objects", err)
	}
	if n := sidecarCount(t, f.jobFixture, `SELECT count(*) FROM media_sidecar_tracks`); n != 4 {
		t.Fatal("downgrade removed sidecar rows", n)
	}
	jobMetricMigration(t, f.jobFixture, "up", SchemaVersion)
	if err := f.s.Ready(f.ctx); err != nil {
		t.Fatal("store not ready after round trip", err)
	}
	if n := sidecarCount(t, f.jobFixture, `SELECT count(*) FROM media_sidecar_tracks WHERE probe_state='pending'`); n != 2 {
		t.Fatal("upgrade did not schedule probes", n)
	}
	if n := sidecarCount(t, f.jobFixture, `SELECT count(*) FROM media_sidecar_tracks WHERE probe_state='not_applicable' AND format IN ('idx','srt')`); n != 2 {
		t.Fatal("upgrade scheduled text subtitles", n)
	}
	// The upgrade pairs existing files at once and queues every ".sub" for
	// the content check.
	if n := sidecarCount(t, f.jobFixture, `SELECT count(*) FROM media_sidecar_tracks i JOIN media_sidecar_tracks s ON s.id=i.paired_track_id
 WHERE i.format='idx' AND s.relative_path='Movie/Movie.en.sub'`); n != 1 {
		t.Fatal("upgrade did not pair existing files", n)
	}
	if n := sidecarCount(t, f.jobFixture, `SELECT count(*) FROM media_sidecar_tracks WHERE format='sub' AND fingerprint IS NULL`); n != 1 {
		t.Fatal("upgrade did not queue the .sub for inspection", n)
	}
	if c := sidecarUpsert(t, f.jobFixture, f.source, sidecarInput(f.jobFixture, "Movie.en.idx"), sidecarInput(f.jobFixture, "Movie.en.sub")); c.Removed != 2 {
		t.Fatal("upsert after round trip", c)
	}
	if n := sidecarCount(t, f.jobFixture, `SELECT count(*) FROM media_sidecar_tracks WHERE format='idx' AND paired_track_id IS NOT NULL`); n != 1 {
		t.Fatal("pair lost", n)
	}
}

// syncJobForProbe queues one manual catalog sync job for the fixture
// library over a published baseline, so a lease can be claimed.
func syncJobForProbe(t *testing.T, f jobFixture) {
	t.Helper()
	f.complete(t, "sidecar-probe-baseline", []string{"Movie.mkv"}, 0)
	if _, _, err := f.s.SubmitCatalogSync(f.ctx, f.a, f.registration.Library.ID, "sidecar-probe-sync", domain.JobPriorityManual, f.policy); err != nil {
		t.Fatal(err)
	}
}
