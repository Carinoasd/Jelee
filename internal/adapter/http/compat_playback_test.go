package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

type compatPlaybackInfo struct {
	MediaSources []struct {
		ID                   string `json:"Id"`
		Container            string `json:"Container"`
		SupportsDirectPlay   bool   `json:"SupportsDirectPlay"`
		SupportsDirectStream bool   `json:"SupportsDirectStream"`
		SupportsTranscoding  bool   `json:"SupportsTranscoding"`
		MediaStreams         []struct {
			Type           string `json:"Type"`
			Index          int    `json:"Index"`
			IsExternal     bool   `json:"IsExternal"`
			DeliveryMethod string `json:"DeliveryMethod"`
			DeliveryURL    string `json:"DeliveryUrl"`
		} `json:"MediaStreams"`
	} `json:"MediaSources"`
	PlaySessionID string `json:"PlaySessionId"`
	ErrorCode     string `json:"ErrorCode"`
}

// TestCompatPlaybackPostgres drives the playback module end to end through
// the real catalog, session and delivery code: a compatibility login, a
// listing, PlaybackInfo with a client's capability declaration, the original
// bytes over the stream and subtitle routes (full, Range, HEAD), refusals of
// every conversion, web sessions refused, and a running stream cut when its
// session is revoked.
func TestCompatPlaybackPostgres(t *testing.T) {
	ctx, store, dsn := leakStore(t)
	adminToken, err := store.Provision(ctx, "play-admin", access.ClientNative, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Provision(ctx, "play-viewer", access.ClientNative, false); err != nil {
		t.Fatal(err)
	}
	webToken, err := store.Provision(ctx, "play-web", access.ClientWeb, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetLocalPassword(ctx, "play-viewer", "$argon2id$v=19$m=65536,t=3,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNo"); err != nil {
		t.Fatal(err)
	}
	viewer := compatInsert(t, ctx, store, `SELECT id::text FROM users WHERE name='play-viewer'`)
	webUser := compatInsert(t, ctx, store, `SELECT id::text FROM users WHERE name='play-web'`)

	// One library with a 4 MiB Matroska original and an external subtitle,
	// granted to the viewer and the web user.
	root := t.TempDir()
	original := make([]byte, 4<<20)
	if _, err := rand.Read(original); err != nil {
		t.Fatal(err)
	}
	subtitle := []byte("1\n00:00:01,000 --> 00:00:02,000\nplayback\n")
	if err := os.WriteFile(filepath.Join(root, "film.mkv"), original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "film.zh.srt"), subtitle, 0o600); err != nil {
		t.Fatal(err)
	}
	library := compatInsert(t, ctx, store, `INSERT INTO libraries(name) VALUES('Playback Films') RETURNING id::text`)
	rootID := compatInsert(t, ctx, store, `INSERT INTO library_roots(library_id,path) VALUES($1::uuid,$2) RETURNING id::text`, library, root)
	item := compatInsert(t, ctx, store, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,'Playback Film','Movie') RETURNING id::text`, library)
	source := compatInsert(t, ctx, store, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type) VALUES($1::uuid,$2::uuid,$3::uuid,'film.mkv','video/x-matroska') RETURNING id::text`, item, library, rootID)
	compatInsert(t, ctx, store, `INSERT INTO media_sidecar_tracks(source_id,library_id,root_id,relative_path,kind,format,size,modified_unix_nano) VALUES($1::uuid,$2::uuid,$3::uuid,'film.zh.srt','subtitle','srt',$4,0) RETURNING id::text`, source, library, rootID, len(subtitle))
	for _, user := range []string{viewer, webUser} {
		compatInsert(t, ctx, store, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid) RETURNING user_id::text`, user, library)
	}
	itemWire, sourceWire := leakWire(item), leakWire(source)

	passwords := &httpAccountPasswords{verify: func(_ context.Context, password, _ string) (bool, error) {
		return password == "correct horse battery", nil
	}}
	cfg := leakConfig(t, dsn, 0)
	cfg.Accounts.LoginUserLimit = 100
	handler := leakHandlerWith(t, store, cfg, passwords)
	serve := func(method, target, body string, header http.Header) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, compatUserRequest(method, target, body, header))
		return w
	}
	native := accountRequest(http.MethodPut, "/api/v1/users/"+viewer+"/native", `{"allowNative":true}`, "")
	native.Header.Set("Authorization", "Bearer "+adminToken)
	w := httptest.NewRecorder()
	if handler.ServeHTTP(w, native); w.Code != http.StatusOK {
		t.Fatalf("enable native: %d %s", w.Code, w.Body.String())
	}
	login := func() string {
		t.Helper()
		w := serve(http.MethodPost, "/compat/Users/AuthenticateByName", `{"Username":"play-viewer","Pw":"correct horse battery"}`, http.Header{"Authorization": {compatLoginHeader}})
		var result struct {
			AccessToken string `json:"AccessToken"`
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.AccessToken == "" {
			t.Fatalf("login: %d %s", w.Code, w.Body.String())
		}
		return result.AccessToken
	}
	token := login()
	auth := compatTokenAuth(token)

	// Browse: the item is listed.
	if w := serve(http.MethodGet, "/compat/Items?Recursive=true&IncludeItemTypes=Movie", "", auth); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"Id":"`+itemWire+`"`) {
		t.Fatalf("browse: %d %s", w.Code, w.Body.String())
	}

	// PlaybackInfo with a capability declaration shaped like a real
	// client's: codec lists, transcoding profiles and a bit rate ceiling.
	declaration := `{"UserId":"` + leakWire(viewer) + `","MaxStreamingBitrate":120000000,"EnableTranscoding":true,"AllowVideoStreamCopy":true,"AutoOpenLiveStream":true,
"DeviceProfile":{"MaxStreamingBitrate":120000000,"DirectPlayProfiles":[{"Container":"mkv,webm","Type":"Video","VideoCodec":"h264,hevc,av1","AudioCodec":"aac,eac3,opus"}],
"TranscodingProfiles":[{"Container":"ts","Type":"Video","VideoCodec":"h264","AudioCodec":"aac","Protocol":"hls","Context":"Streaming"}],
"SubtitleProfiles":[{"Format":"srt","Method":"External"},{"Format":"ass","Method":"Encode"}]}}`
	w = serve(http.MethodPost, "/compat/Items/"+itemWire+"/PlaybackInfo?MaxStreamingBitrate=120000000", declaration, auth)
	var info compatPlaybackInfo
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &info) != nil {
		t.Fatalf("playback info: %d %s", w.Code, w.Body.String())
	}
	for _, banned := range []string{"TranscodingUrl", "TranscodingContainer", "TranscodingSubProtocol", `"Path"`, root, "film.mkv", "film.zh.srt"} {
		if strings.Contains(w.Body.String(), banned) {
			t.Fatalf("playback info publishes %q: %s", banned, w.Body.String())
		}
	}
	if len(info.MediaSources) != 1 || info.ErrorCode != "" || len(info.PlaySessionID) != 32 {
		t.Fatalf("playback info: %s", w.Body.String())
	}
	ms := info.MediaSources[0]
	if ms.ID != sourceWire || ms.Container != "mkv" || !ms.SupportsDirectPlay || ms.SupportsDirectStream || ms.SupportsTranscoding || len(ms.MediaStreams) != 1 {
		t.Fatalf("media source: %s", w.Body.String())
	}
	sub := ms.MediaStreams[0]
	subtitleURL := "/Videos/" + itemWire + "/" + sourceWire + "/Subtitles/0/0/Stream.srt"
	if sub.Type != "Subtitle" || !sub.IsExternal || sub.DeliveryMethod != "External" || sub.DeliveryURL != subtitleURL {
		t.Fatalf("external subtitle: %s", w.Body.String())
	}
	// A client that cannot play the original is told so; nothing else is offered.
	w = serve(http.MethodPost, "/compat/Items/"+itemWire+"/PlaybackInfo", `{"DeviceProfile":{"DirectPlayProfiles":[{"Container":"mp4","Type":"Video"}],"TranscodingProfiles":[{"Container":"ts","Protocol":"hls"}]}}`, auth)
	if w.Code != http.StatusOK || w.Body.String() != `{"MediaSources":[],"ErrorCode":"NoCompatibleStream"}` {
		t.Fatalf("no compatible stream: %d %s", w.Code, w.Body.String())
	}
	// A real conversion request on PlaybackInfo is still refused.
	assertProblem(t, serve(http.MethodPost, "/compat/Items/"+itemWire+"/PlaybackInfo?VideoCodec=h264", declaration, auth), http.StatusConflict, "transcode_disabled")

	// Stream: the original bytes, identical to the file.
	want := sha256.Sum256(original)
	streamURL := "/compat/Videos/" + itemWire + "/stream.mkv?Static=true&MediaSourceId=" + sourceWire + "&PlaySessionId=" + info.PlaySessionID + "&DeviceId=device-1"
	w = serve(http.MethodGet, streamURL, "", auth)
	if w.Code != http.StatusOK || sha256.Sum256(w.Body.Bytes()) != want || w.Header().Get("Content-Type") != "video/x-matroska" {
		t.Fatalf("stream: %d %d bytes", w.Code, w.Body.Len())
	}
	// api_key in the URL, as many players send it.
	w = serve(http.MethodGet, "/compat/Videos/"+itemWire+"/stream?static=true&api_key="+token, "", nil)
	if w.Code != http.StatusOK || sha256.Sum256(w.Body.Bytes()) != want {
		t.Fatalf("api_key stream: %d", w.Code)
	}
	rangeHeader := http.Header{"Range": {"bytes=1048576-1049599"}}
	for k, v := range auth {
		rangeHeader[k] = v
	}
	w = serve(http.MethodGet, streamURL, "", rangeHeader)
	if w.Code != http.StatusPartialContent || !bytes.Equal(w.Body.Bytes(), original[1048576:1049600]) || w.Header().Get("Content-Range") != "bytes 1048576-1049599/"+strconv.Itoa(len(original)) {
		t.Fatalf("range: %d %v", w.Code, w.Header())
	}
	w = serve(http.MethodHead, streamURL, "", auth)
	if w.Code != http.StatusOK || w.Body.Len() != 0 || w.Header().Get("Content-Length") != strconv.Itoa(len(original)) {
		t.Fatalf("head: %d %v", w.Code, w.Header())
	}
	w = serve(http.MethodGet, "/compat"+subtitleURL, "", auth)
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), subtitle) {
		t.Fatalf("subtitle: %d %q", w.Code, w.Body.String())
	}
	// Every conversion is 409 with the server's global code.
	for _, target := range []string{
		"/compat/Videos/" + itemWire + "/stream?static=true&VideoCodec=h264", "/compat/Videos/" + itemWire + "/stream?AudioCodec=aac",
		"/compat/Videos/" + itemWire + "/stream?static=true&MaxStreamingBitrate=1000000", "/compat/Videos/" + itemWire + "/stream?TranscodingMaxAudioChannels=2",
		"/compat/Videos/" + itemWire + "/stream?SegmentContainer=ts", "/compat/Videos/" + itemWire + "/stream?Static=false",
		"/compat/Videos/" + itemWire + "/stream?AudioStreamIndex=1", "/compat/Videos/" + itemWire + "/stream.mp4?static=true",
		"/compat/Videos/" + itemWire + "/master.m3u8", "/compat" + strings.Replace(subtitleURL, "Stream.srt", "Stream.vtt", 1),
	} {
		assertProblem(t, serve(http.MethodGet, target, "", auth), http.StatusConflict, "transcode_disabled")
	}

	// Web sessions are refused on every playback route, by header or URL.
	for _, target := range []string{"/compat/Items/" + itemWire + "/PlaybackInfo", "/compat/Videos/" + itemWire + "/stream?static=true", "/compat" + subtitleURL} {
		if w := serve(http.MethodGet, target, "", compatTokenAuth(webToken)); w.Code != http.StatusUnauthorized || w.Body.Len() != 0 {
			t.Fatalf("web session %s: %d", target, w.Code)
		}
		sep := "?"
		if strings.Contains(target, "?") {
			sep = "&"
		}
		if w := serve(http.MethodGet, target+sep+"api_key="+webToken, "", nil); w.Code != http.StatusUnauthorized {
			t.Fatalf("web api_key %s: %d", target, w.Code)
		}
	}

	// Revocation cuts a running stream. The stream is throttled so it is
	// still running when the session is revoked; the session watcher checks
	// every second.
	throttled := leakConfig(t, dsn, 0)
	throttled.Accounts.LoginUserLimit = 100
	throttled.Streaming = config.DefaultStreamingConfig()
	throttled.Streaming.RevokeCheckSeconds = 1
	throttled.Streaming.MaxKbpsPerUser = 1000
	server := httptest.NewServer(leakHandlerWith(t, store, throttled, passwords))
	t.Cleanup(server.Close)
	victim := login()
	request, err := http.NewRequest(http.MethodGet, server.URL+"/compat/Videos/"+itemWire+"/stream?static=true&api_key="+victim, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("throttled stream: %d", response.StatusCode)
	}
	head := make([]byte, 32<<10)
	if _, err := io.ReadFull(response.Body, head); err != nil {
		t.Fatal(err)
	}
	if w := serve(http.MethodPost, "/compat/Sessions/Logout", "", compatTokenAuth(victim)); w.Code != http.StatusNoContent {
		t.Fatalf("logout: %d", w.Code)
	}
	revoked := time.Now()
	rest, err := io.Copy(io.Discard, response.Body)
	cut := time.Since(revoked)
	if err == nil || len(head)+int(rest) >= len(original) || cut > 5*time.Second {
		t.Fatalf("revoked stream kept going: err=%v bytes=%d after %v", err, len(head)+int(rest), cut)
	}
	if errors.Is(err, io.EOF) {
		t.Fatal("revoked stream ended as if complete")
	}
	t.Logf("revoked stream cut %v after revocation with %d of %d bytes", cut, len(head)+int(rest), len(original))
	// The revoked token is dead everywhere; the other session still plays.
	if w := serve(http.MethodGet, "/compat/Videos/"+itemWire+"/stream?static=true&api_key="+victim, "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token: %d", w.Code)
	}
	if w := serve(http.MethodHead, streamURL, "", auth); w.Code != http.StatusOK {
		t.Fatalf("other session: %d", w.Code)
	}
}
