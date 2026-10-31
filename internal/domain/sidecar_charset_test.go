package domain

import "testing"

func TestSidecarCharsetState(t *testing.T) {
	for _, tc := range []struct {
		kind, format, subKind, charset string
		inspected                      bool
		want                           string
	}{
		{SidecarKindSubtitle, "srt", "", "", false, SidecarCharsetPending},
		{SidecarKindSubtitle, "srt", "", "", true, SidecarCharsetUndetermined},
		{SidecarKindSubtitle, "ass", "", "UTF-8", true, SidecarCharsetUTF8},
		{SidecarKindSubtitle, "ssa", "", "GB18030", true, SidecarCharsetConvertible},
		{SidecarKindSubtitle, "smi", "", "EUC-KR", true, SidecarCharsetConvertible},
		{SidecarKindSubtitle, "vtt", "", "UTF-16LE", true, SidecarCharsetConvertible},
		{SidecarKindSubtitle, "srt", "", "KOI8-R", true, SidecarCharsetUndetermined},
		// MicroDVD text only; VobSub data and an unchecked ".sub" without a
		// charset are not text to convert.
		{SidecarKindSubtitle, "sub", SidecarSubtitleMicroDVD, "Big5", true, SidecarCharsetConvertible},
		{SidecarKindSubtitle, "sub", SidecarSubtitleMicroDVD, "", true, SidecarCharsetUndetermined},
		{SidecarKindSubtitle, "sub", SidecarSubtitleVobSub, "", true, ""},
		{SidecarKindSubtitle, "sub", "", "", false, SidecarCharsetPending},
		{SidecarKindSubtitle, "sub", "", "", true, ""},
		// Formats that declare their own encoding, bitmaps, audio.
		{SidecarKindSubtitle, "ttml", "", "GB18030", true, ""},
		{SidecarKindSubtitle, "dfxp", "", "GB18030", true, ""},
		{SidecarKindSubtitle, "idx", "", "UTF-8", true, ""},
		{SidecarKindSubtitle, "sup", "", "", true, ""},
		{SidecarKindAudio, "flac", "", "", true, ""},
	} {
		if got := SidecarCharsetState(tc.kind, tc.format, tc.subKind, tc.charset, tc.inspected); got != tc.want {
			t.Errorf("%+v = %q", tc, got)
		}
	}
	// The playback read reports inspection without loading fingerprints.
	tracks := playbackExternalTracks([]SidecarTrackRecord{
		{ID: "a", Track: SidecarTrack{Kind: SidecarKindSubtitle, Format: "srt"}, Charset: "Big5", Inspected: true},
		{ID: "b", Track: SidecarTrack{Kind: SidecarKindSubtitle, Format: "srt"}, Inspected: true},
		{ID: "c", Track: SidecarTrack{Kind: SidecarKindSubtitle, Format: "srt"}, Fingerprint: make([]byte, SidecarFingerprintBytes)},
		{ID: "d", Track: SidecarTrack{Kind: SidecarKindAudio, Format: "ac3"}, Inspected: true},
	})
	want := []string{SidecarCharsetConvertible, SidecarCharsetUndetermined, SidecarCharsetUndetermined, ""}
	for i, track := range tracks {
		if track.CharsetState != want[i] {
			t.Errorf("track %s: %q", track.ID, track.CharsetState)
		}
	}
}
