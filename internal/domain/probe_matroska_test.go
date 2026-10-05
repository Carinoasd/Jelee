package domain

import (
	"strings"
	"testing"
)

func matroskaMetadata() MediaMetadata {
	duration := int64(1_000_000)
	codec := "subrip"
	return MediaMetadata{
		Format: MediaFormat{Names: []string{"matroska", "webm"}, DurationMicros: &duration},
		Streams: []MediaStream{
			{Index: 0, Kind: "video", Video: &MediaVideo{}},
			{Index: 1, Kind: "subtitle", Codec: &codec},
			{Index: 2, Kind: "attachment"},
		},
		Matroska: &MediaMatroska{
			Chapters:     []MediaNamedChapter{{StartMicros: 0, Title: "Part one"}, {StartMicros: 500_000, Title: "Part two"}},
			Attachments:  []MediaAttachment{{ID: 1, FileName: "Sans.ttf", Font: true}},
			Tags:         []MediaTag{{Name: "Title", Value: "Synthetic"}},
			StreamTitles: []MediaStreamTitle{{Index: 1, Title: "English"}},
		},
	}
}

func TestMatroskaSupplementWhitelist(t *testing.T) {
	if _, err := MarshalProbeMetadata(matroskaMetadata()); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*MediaMatroska){
		"empty":            func(m *MediaMatroska) { *m = MediaMatroska{} },
		"chapter order":    func(m *MediaMatroska) { m.Chapters[1].StartMicros = -1 },
		"chapter late":     func(m *MediaMatroska) { m.Chapters[1].StartMicros = 2_000_000 },
		"chapter control":  func(m *MediaMatroska) { m.Chapters[0].Title = "a\x00b" },
		"chapter long":     func(m *MediaMatroska) { m.Chapters[0].Title = strings.Repeat("x", MaxMatroskaTextBytes+1) },
		"attachment id":    func(m *MediaMatroska) { m.Attachments[0].ID = 2 },
		"attachment path":  func(m *MediaMatroska) { m.Attachments[0].FileName = "../Sans.ttf" },
		"attachment font":  func(m *MediaMatroska) { m.Attachments[0].Font = false },
		"tag name":         func(m *MediaMatroska) { m.Tags[0].Name = "bad name" },
		"tag duplicate":    func(m *MediaMatroska) { m.Tags = append(m.Tags, m.Tags[0]) },
		"tag empty":        func(m *MediaMatroska) { m.Tags[0].Value = "" },
		"title stream":     func(m *MediaMatroska) { m.StreamTitles[0].Index = 9 },
		"title duplicate":  func(m *MediaMatroska) { m.StreamTitles = append(m.StreamTitles, m.StreamTitles[0]) },
		"too many tags":    func(m *MediaMatroska) { m.Tags = make([]MediaTag, MaxMatroskaTags+1) },
		"invalid encoding": func(m *MediaMatroska) { m.Tags[0].Value = string([]byte{0xff}) },
	} {
		meta := matroskaMetadata()
		change(meta.Matroska)
		if _, err := MarshalProbeMetadata(meta); err != ErrInvalid {
			t.Errorf("%s accepted", name)
		}
	}
	if ValidMediaMatroska(nil, nil, nil) {
		t.Fatal("nil supplement valid")
	}
	if !IsFontFileName("A.OTF") || IsFontFileName("cover.png") || ValidAttachmentFileName("a/b.ttf") || ValidAttachmentFileName("..") {
		t.Fatal("file name rules")
	}
}

func TestPlaybackSourceListsExtractableTracksAndAttachments(t *testing.T) {
	meta := matroskaMetadata()
	source := BuildPlaybackSource(PlaybackSourceRecord{ID: "s", ContentType: "video/x-matroska", Metadata: &meta})
	if len(source.Subtitles) != 1 || !source.Subtitles[0].Extractable || source.Subtitles[0].Title != "English" {
		t.Fatalf("subtitles %+v", source.Subtitles)
	}
	if len(source.Attachments) != 1 || source.Attachments[0].StreamIndex == nil || *source.Attachments[0].StreamIndex != 2 || !source.Attachments[0].Font {
		t.Fatalf("attachments %+v", source.Attachments)
	}
	// Positions only pair when both lists agree; MP4 text is never extractable.
	meta.Streams = meta.Streams[:2]
	meta.Format.Names = []string{"mov", "mp4"}
	source = BuildPlaybackSource(PlaybackSourceRecord{ID: "s", ContentType: "video/mp4", Metadata: &meta})
	if source.Subtitles[0].Extractable || source.Attachments[0].StreamIndex != nil {
		t.Fatalf("mp4 %+v", source)
	}
	meta.Matroska = nil
	if BuildPlaybackSource(PlaybackSourceRecord{ID: "s", Metadata: &meta}).Attachments != nil {
		t.Fatal("attachments without a supplement")
	}
}
