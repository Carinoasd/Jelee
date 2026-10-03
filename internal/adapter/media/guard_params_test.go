package media

import (
	"errors"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"unicode"
)

// keyVariants returns the upstream spelling plus case and separator variants
// that must all be treated as the same parameter.
func keyVariants(name string) []string {
	var snake strings.Builder
	for i, r := range name {
		if i > 0 && unicode.IsUpper(r) {
			snake.WriteByte('_')
		}
		snake.WriteRune(unicode.ToLower(r))
	}
	pascal := strings.ToUpper(name[:1]) + name[1:]
	return []string{name, pascal, strings.ToLower(name), strings.ToUpper(name), snake.String(), strings.ToUpper(snake.String())}
}

func guardQuery(t *testing.T, key, value string) error {
	t.Helper()
	target := "/Videos/id/stream?" + url.QueryEscape(key) + "=" + url.QueryEscape(value)
	return GuardProduction(httptest.NewRequest("GET", target, nil))
}

func guardBody(t *testing.T, contentType, body string) error {
	t.Helper()
	r := httptest.NewRequest("POST", "/Items/id/PlaybackInfo", strings.NewReader(body))
	r.Header.Set("Content-Type", contentType)
	return GuardProduction(r)
}

func TestGuardRejectsEveryTransformParameter(t *testing.T) {
	if len(transformParams) < 40 {
		t.Fatalf("transform parameter list shrank to %d entries", len(transformParams))
	}
	for _, name := range transformParams {
		for _, key := range keyVariants(name) {
			for _, value := range []string{"1", "true", "false", "copy", ""} {
				if err := guardQuery(t, key, value); !errors.Is(err, ErrTranscodeDisabled) {
					t.Errorf("query %s=%q: error=%v", key, value, err)
				}
			}
			if err := guardBody(t, "application/json", `{"`+key+`":1}`); !errors.Is(err, ErrTranscodeDisabled) {
				t.Errorf("json %s: error=%v", key, err)
			}
			if err := guardBody(t, "application/json", `{"Options":[{"`+key+`":null}]}`); !errors.Is(err, ErrTranscodeDisabled) {
				t.Errorf("nested json %s: error=%v", key, err)
			}
			if err := guardBody(t, "application/x-www-form-urlencoded", url.QueryEscape(key)+"=1"); !errors.Is(err, ErrTranscodeDisabled) {
				t.Errorf("form %s: error=%v", key, err)
			}
		}
	}
}

func TestGuardRejectsDisabledDirectSwitches(t *testing.T) {
	for _, name := range directDefaultTrue {
		for _, key := range keyVariants(name) {
			for _, value := range []string{"false", "FALSE", "False", "0", "no"} {
				if err := guardQuery(t, key, value); !errors.Is(err, ErrTranscodeDisabled) {
					t.Errorf("query %s=%s: error=%v", key, value, err)
				}
			}
			if err := guardBody(t, "application/json", `{"`+key+`":false}`); !errors.Is(err, ErrTranscodeDisabled) {
				t.Errorf("json %s=false: error=%v", key, err)
			}
			for _, value := range []string{"true", "TRUE", "True", "1", ""} {
				if err := guardQuery(t, key, value); err != nil {
					t.Errorf("query %s=%q blocked: %v", key, value, err)
				}
			}
			for _, value := range []string{"true", "null"} {
				if err := guardBody(t, "application/json", `{"`+key+`":`+value+`}`); err != nil {
					t.Errorf("json %s=%s blocked: %v", key, value, err)
				}
			}
		}
	}
	for _, key := range keyVariants("static") {
		for _, value := range []string{"false", "FALSE", "0", ""} {
			if err := guardQuery(t, key, value); !errors.Is(err, ErrTranscodeDisabled) {
				t.Errorf("query %s=%q: error=%v", key, value, err)
			}
		}
		for _, body := range []string{`{"` + key + `":false}`, `{"` + key + `":null}`} {
			if err := guardBody(t, "application/json", body); !errors.Is(err, ErrTranscodeDisabled) {
				t.Errorf("json %s: error=%v", body, err)
			}
		}
		for _, value := range []string{"true", "TRUE", "True"} {
			if err := guardQuery(t, key, value); err != nil {
				t.Errorf("query %s=%s blocked: %v", key, value, err)
			}
		}
	}
	for _, key := range keyVariants("enableTranscoding") {
		for _, value := range []string{"true", "TRUE", "1", ""} {
			if err := guardQuery(t, key, value); !errors.Is(err, ErrTranscodeDisabled) {
				t.Errorf("query %s=%q: error=%v", key, value, err)
			}
		}
		if err := guardQuery(t, key, "FALSE"); err != nil {
			t.Errorf("query %s=FALSE blocked: %v", key, err)
		}
	}
}

func TestGuardRejectsCodecQualifiedStreamOptions(t *testing.T) {
	for _, name := range streamOptionNames {
		for _, option := range keyVariants(name) {
			for _, codec := range []string{"h264", "HEVC", "aac"} {
				key := codec + "-" + option
				if err := guardQuery(t, key, "x"); !errors.Is(err, ErrTranscodeDisabled) {
					t.Errorf("query %s: error=%v", key, err)
				}
			}
		}
	}
}

func TestGuardRejectsPrefixedFamilies(t *testing.T) {
	for _, name := range []string{"segmentLength", "segmentContainer", "hlsSegmentLength", "dashProfile", "transcodingProtocol", "transcodingContainer", "transcodeSeekInfo", "transcodingMaxAudioChannels"} {
		for _, key := range keyVariants(name) {
			if err := guardQuery(t, key, "x"); !errors.Is(err, ErrTranscodeDisabled) {
				t.Errorf("query %s: error=%v", key, err)
			}
		}
	}
}

func TestGuardSubtitleMethods(t *testing.T) {
	for _, value := range []string{"Encode", "ENCODE", "burnin", "Burn-In", "Hls", "HLS", "Drop", "drop", "0", "3", "4", "Unknown"} {
		if err := guardQuery(t, "SubtitleMethod", value); !errors.Is(err, ErrTranscodeDisabled) {
			t.Errorf("subtitle method %s: error=%v", value, err)
		}
	}
	for _, body := range []string{`{"SubtitleMethod":0}`, `{"SubtitleMethod":3}`, `{"SubtitleMethod":{}}`} {
		if err := guardBody(t, "application/json", body); !errors.Is(err, ErrTranscodeDisabled) {
			t.Errorf("json %s: error=%v", body, err)
		}
	}
	for _, value := range []string{"External", "EXTERNAL", "external", "Embed", "embed", "1", "2"} {
		if err := guardQuery(t, "subtitleMethod", value); err != nil {
			t.Errorf("direct subtitle method %s blocked: %v", value, err)
		}
	}
	for _, body := range []string{`{"SubtitleMethod":1}`, `{"SubtitleMethod":2}`, `{"SubtitleMethod":null}`} {
		if err := guardBody(t, "application/json", body); err != nil {
			t.Errorf("json %s blocked: %v", body, err)
		}
	}
}

func TestGuardAllowsDirectOnlyParameters(t *testing.T) {
	targets := []string{
		"/Videos/id/stream.mkv?Static=true&MediaSourceId=abc&DeviceId=d&PlaySessionId=p&Tag=etag&api_key=k",
		"/Audio/id/stream.flac?static=TRUE&startTimeTicks=0&mediaSourceId=abc",
		"/Videos/id/stream?static=true&AudioStreamIndex=1&SubtitleStreamIndex=2&VideoStreamIndex=0&LiveStreamId=l",
		"/Items/id/PlaybackInfo?UserId=u&StartTimeTicks=0&EnableDirectPlay=true&EnableDirectStream=true&EnableTranscoding=false&AutoOpenLiveStream=false",
		"/Items/id/PlaybackInfo?AllowVideoStreamCopy=true&AllowAudioStreamCopy=true&EnableAutoStreamCopy=true&SubtitleMethod=External",
		"/Audio/id/universal?UserId=u&DeviceId=d&EnableRedirection=true&EnableRemoteMedia=false&Container=flac",
		"/Videos/id/stream?static=true&Context=Static",
	}
	for _, target := range targets {
		if err := GuardProduction(httptest.NewRequest("GET", target, nil)); err != nil {
			t.Errorf("direct request %s blocked: %v", target, err)
		}
	}
	body := `{"UserId":"u","StartTimeTicks":0,"AudioStreamIndex":1,"SubtitleStreamIndex":-1,"MediaSourceId":"m","LiveStreamId":null,` +
		`"EnableDirectPlay":true,"EnableDirectStream":true,"EnableTranscoding":false,"AllowVideoStreamCopy":true,"AllowAudioStreamCopy":true,` +
		`"AutoOpenLiveStream":false,"DeviceProfile":{"Name":"native","TranscodingProfiles":[]}}`
	if err := guardBody(t, "application/json", body); err != nil {
		t.Errorf("direct PlaybackInfo body blocked: %v", err)
	}
}

// TestGuardCoversUpstreamControllerParameters pins, independently of the
// guard's own tables, every transformation parameter accepted by the upstream
// video, audio, dynamic HLS, media info and universal audio controllers, with
// a value that would make upstream transcode, remux or segment.
func TestGuardCoversUpstreamControllerParameters(t *testing.T) {
	for _, query := range []string{
		"static=false", "params=h264%3Baac", "segmentContainer=ts", "segmentLength=6", "minSegments=1",
		"audioCodec=aac", "videoCodec=copy", "subtitleCodec=srt", "enableAutoStreamCopy=false",
		"allowVideoStreamCopy=false", "allowAudioStreamCopy=false", "audioSampleRate=48000", "maxAudioBitDepth=16",
		"audioBitRate=128000", "audioChannels=2", "maxAudioChannels=2", "profile=high", "level=41",
		"framerate=24", "maxFramerate=30", "copyTimestamps=true", "width=1280", "height=720", "maxWidth=1920",
		"maxHeight=1080", "videoBitRate=1000", "subtitleMethod=Encode", "maxRefFrames=4", "maxVideoBitDepth=8",
		"requireAvc=true", "deInterlace=true", "requireNonAnamorphic=true", "transcodingMaxAudioChannels=2",
		"cpuCoreLimit=2", "enableMpegtsM2TsMode=true", "transcodeReasons=ContainerNotSupported",
		"enableAudioVbrEncoding=false", "h264-profile=main", "hevc-level=120", "h264-deinterlace=true",
		"actualSegmentLengthTicks=1", "enableAdaptiveBitrateStreaming=true", "enableSubtitlesInManifest=true",
		"alwaysBurnInSubtitleWhenTranscoding=true", "maxStreamingBitrate=1", "enableDirectPlay=false",
		"enableDirectStream=false", "enableTranscoding=true", "transcodingAudioChannels=2",
		"transcodingContainer=ts", "transcodingProtocol=hls", "maxAudioSampleRate=44100", "breakOnNonKeyFrames=true",
	} {
		for _, form := range []string{query, strings.ToUpper(query), strings.ToLower(query)} {
			if err := GuardProduction(httptest.NewRequest("GET", "/Videos/id/stream?"+form, nil)); !errors.Is(err, ErrTranscodeDisabled) {
				t.Errorf("%s: error=%v", form, err)
			}
		}
	}
}
