package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/access"
	httpapi "github.com/MoYuanCN/Jelee/internal/adapter/http"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

type playbackFixture struct {
	probeFixture
	item                             string
	probed, expired, stale, unprobed string
	root                             string
}

const playbackTestMetadata = `{"format":{"names":["matroska","webm"],"durationMicros":7200000000,"sizeBytes":9000000000,"bitRate":10000000},
 "streams":[{"index":0,"kind":"video","codec":"hevc","profile":"Main 10","video":{"width":3840,"height":2160}},
 {"index":1,"kind":"audio","codec":"truehd","language":"en","default":true,"audio":{"channels":8,"channelLayout":"7.1"}},
 {"index":2,"kind":"audio","codec":"ac3","language":"ja","audio":{"channels":6}},
 {"index":3,"kind":"subtitle","codec":"hdmv_pgs_subtitle","language":"en","forced":true}],"chapters":[]}`

// seedPlaybackProbe stores one ready probe row the way the probe commit
// would, with exact quota counters, and an expiry offset in hours.
func seedPlaybackProbe(t *testing.T, f probeFixture, relative string, size, mtime int64, expiresHours int) {
	t.Helper()
	tx, err := f.s.Pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(f.ctx)
	if err := ensureProbeLibrary(f.ctx, tx, f.registration.Library.ID); err != nil {
		t.Fatal("probe library scope", err)
	}
	if _, err := tx.Exec(f.ctx, `INSERT INTO probe_cache(root_id,relative_path,library_id,size,modified_unix_nano,fingerprint,fingerprint_version,tool_version_id,library_generation,root_generation,state,metadata,expires_at,charge_bytes)
 SELECT r.id,$2,r.library_id,$4,$5,decode(repeat('d',64),'hex'),'edge-sha256-v1',$3::uuid,l.probe_generation,r.probe_generation,'ready',$6::jsonb,
  clock_timestamp()+$7*interval '1 hour',2048+octet_length($6::jsonb::text)
 FROM library_roots r JOIN libraries l ON l.id=r.library_id WHERE r.id=$1::uuid`,
		f.registration.RootID, relative, f.identity.ID, size, mtime, playbackTestMetadata, expiresHours); err != nil {
		t.Fatal("seed probe row", err)
	}
	if _, err := tx.Exec(f.ctx, `UPDATE probe_cache_quota SET rows_used=(SELECT count(*) FROM probe_cache),bytes_used=(SELECT sum(charge_bytes) FROM probe_cache);
 UPDATE probe_library_quota q SET rows_used=(SELECT count(*) FROM probe_cache c WHERE c.library_id=q.library_id),bytes_used=(SELECT COALESCE(sum(charge_bytes),0) FROM probe_cache c WHERE c.library_id=q.library_id)`); err != nil {
		t.Fatal("derive probe counters", err)
	}
	if err := tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
}

func seedPlaybackScan(t *testing.T, f probeFixture, item, source, relative string, size, mtime int64) {
	t.Helper()
	imageRepositoryExec(t, f.jobFixture, `INSERT INTO catalog_scan_sources(source_id,library_id,item_id,root_id,relative_path,size,modified_unix_nano,parser_version)
 VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,$6,$7,'test')`, source, f.registration.Library.ID, item, f.registration.RootID, relative, size, mtime)
}

func newPlaybackFixture(t *testing.T) playbackFixture {
	t.Helper()
	f := playbackFixture{probeFixture: newProbeFixture(t)}
	f.item = metadataItem(t, f.jobFixture)
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT path FROM library_roots WHERE id=$1::uuid`, f.registration.RootID).Scan(&f.root); err != nil {
		t.Fatal(err)
	}
	// Probed: ready, unexpired and stamped like the last scan.
	f.probed = sidecarSource(t, f.jobFixture, f.item, "Movie/Movie.2160p.UHD.BluRay.mkv")
	seedPlaybackProbe(t, f.probeFixture, "Movie/Movie.2160p.UHD.BluRay.mkv", 9000000000, 11, 24)
	seedPlaybackScan(t, f.probeFixture, f.item, f.probed, "Movie/Movie.2160p.UHD.BluRay.mkv", 9000000000, 11)
	// Expired probe rows describe a file that must be checked again.
	f.expired = sidecarSource(t, f.jobFixture, f.item, "Movie/Movie.Expired.mkv")
	seedPlaybackProbe(t, f.probeFixture, "Movie/Movie.Expired.mkv", 1, 1, -1)
	// The scan saw a different file than the probe did.
	f.stale = sidecarSource(t, f.jobFixture, f.item, "Movie/Movie.Stale.mkv")
	seedPlaybackProbe(t, f.probeFixture, "Movie/Movie.Stale.mkv", 5, 5, 24)
	seedPlaybackScan(t, f.probeFixture, f.item, f.stale, "Movie/Movie.Stale.mkv", 6, 5)
	// Never probed at all.
	f.unprobed = sidecarSource(t, f.jobFixture, f.item, "Movie/Movie.720p.mkv")
	sidecarUpsert(t, f.jobFixture, f.probed, sidecarInput(f.jobFixture, "Movie.en.srt"), sidecarInput(f.jobFixture, "Movie.ja.forced.ass"),
		sidecarInput(f.jobFixture, "Movie.commentary.ac3"), sidecarInput(f.jobFixture, "Movie.mka"))
	return f
}

func TestPlaybackSourcesProbeSidecarsAndAuthorization(t *testing.T) {
	f := newPlaybackFixture(t)
	reader := imageRepositoryActor(t, f.jobFixture, "playback-native", access.ClientNative)
	web := imageRepositoryActor(t, f.jobFixture, "playback-web", access.ClientWeb)
	denied := func(stage string, actor domain.Actor, item string) {
		t.Helper()
		if v, err := f.s.ListPlaybackSources(f.ctx, actor, item); v != nil || err != domain.ErrNotFound {
			t.Fatalf("%s: %d sources, %v", stage, len(v), err)
		}
	}
	denied("native without grant", reader, f.item)
	for _, actor := range []domain.Actor{reader, web} {
		imageRepositoryExec(t, f.jobFixture, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid)`, actor.UserID, f.registration.Library.ID)
	}
	denied("granted web session", web, f.item)
	denied("missing item", reader, "10000000-0000-4000-8000-000000000099")
	denied("source id as item", reader, f.probed)
	denied("malformed item", reader, "invalid")
	denied("foreign session", domain.Actor{UserID: reader.UserID, SessionID: web.SessionID}, f.item)

	records, err := f.s.ListPlaybackSources(f.ctx, reader, f.item)
	if err != nil || len(records) != 4 {
		t.Fatalf("granted native list: %d %v", len(records), err)
	}
	byID := map[string]domain.PlaybackSourceRecord{}
	for _, r := range records {
		byID[r.ID] = r
		if text := fmt.Sprintf("%v %#v", r, r); strings.Contains(text, f.root) || strings.Contains(text, "Movie/") {
			t.Fatal("record diagnostics expose paths")
		}
	}
	probed := byID[f.probed]
	if probed.Metadata == nil || len(probed.Metadata.Streams) != 4 || probed.FileName != "Movie.2160p.UHD.BluRay.mkv" ||
		probed.ContentType != "video/x-matroska" || probed.ScanSize == nil || *probed.ScanSize != 9000000000 || len(probed.Sidecars) != 4 {
		t.Fatalf("probed source differs: %+v", probed.Metadata)
	}
	for _, id := range []string{f.expired, f.stale, f.unprobed} {
		if byID[id].Metadata != nil || byID[id].ID != id {
			t.Fatal("expired, stale or missing probe result was used")
		}
	}
	if byID[f.stale].ScanSize == nil || *byID[f.stale].ScanSize != 6 || byID[f.unprobed].ScanSize != nil {
		t.Fatal("scan size not carried")
	}

	catalog, err := app.NewCatalog(f.s).WithPlayback(f.s)
	if err != nil {
		t.Fatal(err)
	}
	sources, err := catalog.PlaybackSources(f.ctx, reader, f.item)
	if err != nil || len(sources) != 4 || sources[0].ID != f.probed || !sources[0].Probed {
		t.Fatal("best probed version is not first", err)
	}
	top := sources[0]
	if top.Container != "mkv" || top.BitRate == nil || *top.BitRate != 10000000 || len(top.Video) != 1 || !top.Video[0].Primary ||
		len(top.Audio) != 2 || len(top.Subtitles) != 1 || top.Subtitles[0].Format != "pgs" || len(top.External) != 4 || top.Version.Resolution != "2160p" {
		t.Fatalf("projected source differs: %+v", top)
	}
	kinds := map[string]domain.PlaybackExternalTrack{}
	for _, e := range top.External {
		kinds[e.Format] = e
	}
	if kinds["srt"].Language != "en" || kinds["srt"].Codec != "srt" || kinds["srt"].Charset != "UTF-8" || !kinds["ass"].Forced ||
		!kinds["ac3"].Commentary || kinds["ac3"].Kind != domain.SidecarKindAudio || kinds["mka"].Codec != "" || kinds["srt"].ID == "" {
		t.Fatalf("external tracks differ: %+v", top.External)
	}
	decisions, err := catalog.CheckPlayback(f.ctx, reader, f.item, domain.ClientCapabilities{Containers: []string{"matroska"},
		VideoCodecs: []string{"h265"}, AudioCodecs: []string{"ac3"}, SubtitleFormats: []string{"srt"}, MaxBitrate: 10000000})
	if err != nil || len(decisions) != 4 || !decisions[0].DirectPlay || decisions[0].SourceID != f.probed {
		t.Fatalf("decisions differ: %+v %v", decisions, err)
	}
	for _, d := range decisions[1:] {
		if d.DirectPlay || d.Code != domain.PlaybackUnsupportedCode || len(d.Reasons) != 1 || d.Reasons[0] != domain.PlaybackReasonSourceNotProbe {
			t.Fatalf("unprobed decision differs: %+v", d)
		}
	}

	// A visible item without sources is an empty list, not a missing item.
	empty := metadataItem(t, f.jobFixture)
	if v, err := f.s.ListPlaybackSources(f.ctx, reader, empty); err != nil || v == nil || len(v) != 0 {
		t.Fatal("visible empty item", err)
	}
	imageRepositoryExec(t, f.jobFixture, `UPDATE sessions SET revoked_at=clock_timestamp() WHERE id=$1::uuid`, reader.SessionID)
	denied("revoked session", reader, f.item)
	if _, err := f.s.ListPlaybackSources(nil, reader, f.item); err != domain.ErrInvalid {
		t.Fatal("nil context", err)
	}
	cancelled, cancel := context.WithCancel(f.ctx)
	cancel()
	if _, err := f.s.ListPlaybackSources(cancelled, web, f.item); err != context.Canceled {
		t.Fatal("cancellation ignored", err)
	}
}

func TestPlaybackHTTPNativeOnlyGuardAndHiddenItems(t *testing.T) {
	f := newPlaybackFixture(t)
	other, err := f.s.RegisterLibrary(f.ctx, "playback-hidden", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var hidden, hiddenSource string
	if err := f.s.Pool.QueryRow(f.ctx, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,'Hidden Playback Qz9','Movie') RETURNING id::text`, other.Library.ID).Scan(&hidden); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Pool.QueryRow(f.ctx, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type) VALUES($1::uuid,$2::uuid,$3::uuid,'Hidden/Qz9-secret.mkv','video/x-matroska') RETURNING id::text`, hidden, other.Library.ID, other.RootID).Scan(&hiddenSource); err != nil {
		t.Fatal(err)
	}
	native, err := f.s.Provision(f.ctx, "playback-http-native", access.ClientNative, false)
	if err != nil {
		t.Fatal(err)
	}
	web, err := f.s.Provision(f.ctx, "playback-http-web", access.ClientWeb, false)
	if err != nil {
		t.Fatal(err)
	}
	imageRepositoryExec(t, f.jobFixture, `INSERT INTO library_acl(user_id,library_id) SELECT id,$1::uuid FROM users WHERE name IN ('playback-http-native','playback-http-web')`, f.registration.Library.ID)
	catalog, err := app.NewCatalog(f.s).WithPlayback(f.s)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Resources: config.DefaultResourcesConfig(), Listen: "127.0.0.1:8097", AllowedHosts: []string{"localhost"}, DatabaseURL: "postgres://localhost/jelee_test",
		MaxConnections: 16, MaxStreams: 2, RequestTimeoutSeconds: 5, EnableCatalog: true, EnableDirect: true}
	handler, err := httpapi.New(cfg, f.s, catalog, f.s, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body, token string) (int, string, string) {
		t.Helper()
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		r := httptest.NewRequest(method, "http://localhost"+path, reader)
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		var envelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &envelope)
		return w.Code, envelope.Error.Code, w.Body.String()
	}
	info := "/api/v1/items/" + f.item + "/playback"
	check := info + "/check"
	caps := `{"containers":["mkv"],"videoCodecs":["hevc"],"audioCodecs":["ac3"],"subtitleFormats":["pgs"],"maxBitrate":9999999}`

	status, _, body := request("GET", info, "", native)
	if status != 200 || !strings.Contains(body, f.probed) || !strings.Contains(body, `"externalTracks"`) || !strings.Contains(body, `"transcoding":false`) || strings.Contains(body, f.root) {
		t.Fatalf("native playback info %d: %s", status, body)
	}
	status, _, body = request("POST", check, caps, native)
	var result struct {
		Data struct {
			DirectPlayable bool                      `json:"directPlayable"`
			Decisions      []domain.PlaybackDecision `json:"decisions"`
		} `json:"data"`
	}
	if status != 200 || json.Unmarshal([]byte(body), &result) != nil || result.Data.DirectPlayable || len(result.Data.Decisions) != 4 ||
		strings.Join(result.Data.Decisions[0].Reasons, ",") != domain.PlaybackReasonBitrate || result.Data.Decisions[0].Code != "direct_play_unsupported" {
		t.Fatalf("native check %d: %s", status, body)
	}
	for _, path := range []string{info, check} {
		method, payload := "GET", ""
		if path == check {
			method, payload = "POST", caps
		}
		if status, code, _ := request(method, path, payload, web); status != 403 || code != "web_playback_disabled" {
			t.Fatalf("%s web session: %d %s", path, status, code)
		}
		if status, code, _ := request(method, path+"?maxStreamingBitrate=1000", payload, native); status != 409 || code != "transcode_disabled" {
			t.Fatalf("%s transcode query: %d %s", path, status, code)
		}
		if status, code, _ := request(method, path+"?static=true", payload, native); status != 400 || code != "invalid_request" {
			t.Fatalf("%s unknown query: %d %s", path, status, code)
		}
		hiddenPath := strings.Replace(path, f.item, hidden, 1)
		missingPath := strings.Replace(path, f.item, "10000000-0000-4000-8000-000000000099", 1)
		hs, hc, hb := request(method, hiddenPath, payload, native)
		ms, mc, _ := request(method, missingPath, payload, native)
		if hs != 404 || hc != "not_found" || ms != hs || mc != hc || strings.Contains(hb, "Qz9") || strings.Contains(hb, hiddenSource) {
			t.Fatalf("%s hidden=%d/%s missing=%d/%s", path, hs, hc, ms, mc)
		}
	}
	for _, body := range []string{
		`{"containers":["mkv"],"videoCodec":"h264"}`,
		`{"containers":["mkv"],"maxStreamingBitrate":1000}`,
		`{"containers":["mkv"],"transcodingProfiles":[{"container":"ts"}]}`,
		`{"containers":["mkv"],"nested":{"audioCodec":"aac"}}`,
	} {
		if status, code, _ := request("POST", check, body, native); status != 409 || code != "transcode_disabled" {
			t.Fatalf("transcode body %s: %d %s", body, status, code)
		}
	}
	for _, body := range []string{`{"containers":["mkv"],"unknown":1}`, `{"containers":["a b"]}`, `{"maxBitrate":-1}`, `[]`, `{"containers":null}`} {
		if status, code, _ := request("POST", check, body, native); status != 400 || code != "invalid_request" {
			t.Fatalf("invalid body %s: %d %s", body, status, code)
		}
	}
}
