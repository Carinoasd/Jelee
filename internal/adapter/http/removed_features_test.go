package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRemovedFeatureFamiliesRejectWithoutBackendWork(t *testing.T) {
	paths := []string{"/LiveTv", "/LiveTv/Info", "/LiveTv/Channels", "/LiveTv/Programs", "/LiveTv/Timers", "/LiveTv/SeriesTimers", "/LiveTv/TunerHosts", "/LiveTv/TunerHosts/Discover", "/LiveTv/Tuners/test/Reset", "/LiveTv/Recordings", "/LiveTv/Recordings/test", "/LiveTv/Recordings/test/stream", "/Channels", "/Channels/test/Items", "/Dlna", "/Dlna/test/description.xml", "/lIvEtV/Info"}
	methods := []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions}
	locales := []struct{ header, want, message string }{
		{"", "zh-CN", "不支持设备发现、直播电视、录制和频道。"},
		{"zh-TW", "zh-TW", "不支援裝置探索、直播電視、錄製與頻道。"},
		{"ja-JP", "ja-JP", "デバイス検出、ライブテレビ、録画、チャンネルには対応していません。"},
		{"fr-FR", "en-US", "Discovery, live TV, recordings and channels are not supported."},
	}
	for _, enabled := range []bool{false, true} {
		f := newFixture(t, enabled, enabled)
		for _, path := range paths {
			for _, method := range methods {
				for _, locale := range locales {
					request := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(`{"enable":true}`))
					request.Header.Set("Accept-Language", locale.header)
					response := httptest.NewRecorder()
					f.handler.ServeHTTP(response, request)
					assertProblem(t, response, 501, "feature_removed")
					if response.Header().Get("Content-Language") != locale.want || !strings.Contains(response.Body.String(), locale.message) {
						t.Fatalf("%s %s language mismatch: %s", method, path, response.Body.String())
					}
				}
			}
		}
		if f.backend.authCalls != 0 || f.repository.listCalls != 0 || f.repository.getCalls != 0 || f.resolver.calls != 0 {
			t.Fatal("removed feature request reached backend, catalog or media")
		}
	}
}

func TestRemovedFeatureBoundaryPreservesHostAndTranscodeGuards(t *testing.T) {
	f := newFixture(t, true, true)
	assertProblem(t, f.request(http.MethodGet, "/LiveTv/hls/master.m3u8", ""), 409, "transcode_disabled")
	request := httptest.NewRequest(http.MethodGet, "http://untrusted.example/LiveTv/Info", nil)
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	assertProblem(t, response, 400, "invalid_host")
	for _, path := range []string{"/LiveTvExtra", "/ChannelsExtra", "/DlnaExtra", "/other/LiveTv", "/api/v1/LiveTv", "/LiveTv/debug/pprof"} {
		assertProblem(t, f.request(http.MethodGet, path, ""), 404, "not_found")
	}
	assertProblem(t, f.request(http.MethodHead, "/LiveTv/Info", ""), 501, "feature_removed")
}

func TestRemovedFeatureOpenAPIContract(t *testing.T) {
	f := newFixture(t, false, false)
	response := f.request(http.MethodGet, "/api/v1/openapi.json", "")
	if response.Code != 200 {
		t.Fatal(response.Code)
	}
	var document struct {
		Removed struct {
			Roots  []string `json:"pathRoots"`
			Status int      `json:"status"`
			Code   string   `json:"code"`
		} `json:"x-jelee-removed-features"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document.Removed.Status != 501 || document.Removed.Code != "feature_removed" || len(document.Removed.Roots) != 3 {
		t.Fatalf("removed-feature specification mismatch: %+v", document)
	}
	for _, root := range document.Removed.Roots {
		assertProblem(t, f.request(http.MethodGet, root, ""), 501, "feature_removed")
	}
}

func TestRemovedFeatureNativeHTTPHeadHasNoBody(t *testing.T) {
	f := newFixture(t, true, true)
	server := httptest.NewServer(f.handler)
	defer server.Close()
	request, err := http.NewRequest(http.MethodHead, server.URL+"/LiveTv/Info", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Accept-Language", "fr-FR")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 501 || len(body) != 0 || response.Header.Get("Content-Language") != "en-US" {
		t.Fatalf("native HEAD status=%d body=%d locale=%s", response.StatusCode, len(body), response.Header.Get("Content-Language"))
	}
	if f.backend.authCalls != 0 || f.repository.listCalls != 0 || f.repository.getCalls != 0 || f.resolver.calls != 0 {
		t.Fatal("native unsupported probe reached a backend")
	}
}
