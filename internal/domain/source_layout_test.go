package domain

import (
	"slices"
	"testing"
)

func TestLayoutHints(t *testing.T) {
	for _, layout := range ContainerLayouts {
		hints := LayoutHints(layout)
		switch layout {
		case LayoutMoovAtEnd:
			if !slices.Equal(hints, []string{LayoutHintFaststart}) {
				t.Fatal(layout, hints)
			}
		case LayoutNoCues:
			if !slices.Equal(hints, []string{LayoutHintNoSeekIndex}) {
				t.Fatal(layout, hints)
			}
		default:
			if hints != nil {
				t.Fatal(layout, hints)
			}
		}
	}
	if LayoutHints("rewrite") != nil || ValidContainerLayout("rewrite") {
		t.Fatal("unknown layout accepted")
	}
}

func TestValidSourceLayoutInspection(t *testing.T) {
	id := "00000000-0000-4000-8000-000000000001"
	valid := SourceLayoutInspection{SourceID: id, Size: 100, SourceLayout: SourceLayout{Layout: LayoutMoovAtEnd, IndexOffset: 90, IndexLength: 10}}
	if !ValidSourceLayoutInspection(valid) {
		t.Fatal("valid result refused")
	}
	for name, change := range map[string]func(*SourceLayoutInspection){
		"bad id":           func(v *SourceLayoutInspection) { v.SourceID = "x" },
		"negative size":    func(v *SourceLayoutInspection) { v.Size = -1 },
		"unknown layout":   func(v *SourceLayoutInspection) { v.Layout = "faststart" },
		"past the end":     func(v *SourceLayoutInspection) { v.IndexOffset = 91 },
		"negative offset":  func(v *SourceLayoutInspection) { v.IndexOffset = -1 },
		"negative length":  func(v *SourceLayoutInspection) { v.IndexLength = -1 },
		"offset no length": func(v *SourceLayoutInspection) { v.IndexLength = 0 },
		"too long": func(v *SourceLayoutInspection) {
			v.Size, v.IndexOffset, v.IndexLength = 4*SourceLayoutIndexMax, 0, SourceLayoutIndexMax+1
		},
	} {
		v := valid
		change(&v)
		if ValidSourceLayoutInspection(v) {
			t.Error(name, "accepted")
		}
	}
	none := SourceLayoutInspection{SourceID: id, SourceLayout: SourceLayout{Layout: LayoutUnknown}}
	if !ValidSourceLayoutInspection(none) {
		t.Fatal("unknown layout without range refused")
	}
}

func TestPlaybackSourceReportsLayout(t *testing.T) {
	record := PlaybackSourceRecord{ID: "00000000-0000-4000-8000-000000000001", ContentType: "video/mp4", FileName: "film.mp4",
		Layout: &SourceLayout{Layout: LayoutMoovAtEnd, IndexOffset: 10, IndexLength: 5}}
	source := BuildPlaybackSource(record)
	if source.Layout != LayoutMoovAtEnd || !slices.Equal(source.LayoutHints, []string{LayoutHintFaststart}) {
		t.Fatalf("layout %q hints %v", source.Layout, source.LayoutHints)
	}
	record.Layout = &SourceLayout{Layout: LayoutCuesAfterClusters}
	if source := BuildPlaybackSource(record); source.Layout != LayoutCuesAfterClusters || source.LayoutHints != nil {
		t.Fatalf("layout %q hints %v", source.Layout, source.LayoutHints)
	}
	record.Layout = &SourceLayout{Layout: "rewrite"}
	if source := BuildPlaybackSource(record); source.Layout != "" || source.LayoutHints != nil {
		t.Fatal("unknown stored layout reported")
	}
	record.Layout = nil
	if source := BuildPlaybackSource(record); source.Layout != "" || source.LayoutHints != nil {
		t.Fatal("unread layout reported")
	}
}
