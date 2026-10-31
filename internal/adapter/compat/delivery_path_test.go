package compat

import (
	"net/http/httptest"
	"testing"
)

func TestIsDeliveryPath(t *testing.T) {
	const id = "33333333333343338333333333333333"
	for path, want := range map[string]bool{
		"/compat/Videos/" + id + "/stream":                           true,
		"/compat/videos/" + id + "/STREAM.mkv/":                      true,
		"/compat/Audio/" + id + "/stream.flac":                       true,
		"/compat/Videos/" + id + "/src/Subtitles/2/Stream.srt":       true,
		"/compat/Videos/" + id + "/src/Subtitles/2/1000/Stream.ass":  true,
		"/compat/Videos/" + id + "/src/Audio/1/Stream.aac":           true,
		"/compat/Videos/" + id + "/src/Attachments/0":                true,
		"/compat/Items/" + id + "/Images/Primary":                    true,
		"/compat/Items/" + id + "/PlaybackInfo":                      true,
		"/compat/Items/" + id:                                        false,
		"/compat/Videos/" + id + "/AdditionalParts":                  false,
		"/compat/Videos/" + id + "/src/Subtitles/2/Stream":           false,
		"/compat/Videos/" + id + "/src/Subtitles/2/1/2/3/Stream.srt": false,
		"/compat/Videos/" + id + "/src/Other/2/Stream.srt":           false,
		"/compat/Videos//stream":                                     false,
		"/compat/Audio/" + id + "/download":                          false,
		"/compat/Sessions/Playing/Progress":                          false,
		"/compat":                                                    false,
		"/compatible/Videos/" + id + "/stream":                       false,
		"/api/v1/sources/" + id + "/stream":                          false,
	} {
		if got := IsDeliveryPath(path); got != want {
			t.Errorf("IsDeliveryPath(%q) = %v", path, got)
		}
	}
}

func TestWriteStatusUsesTheLayerErrorShape(t *testing.T) {
	w := httptest.NewRecorder()
	WriteStatus(w, 429)
	if w.Code != 429 || w.Body.Len() != 0 || w.Header().Get("Content-Length") != "0" {
		t.Fatalf("%d %q %v", w.Code, w.Body, w.Header())
	}
}
