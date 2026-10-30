package probe

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/process"
)

// The testdata files are real ffprobe output for the sealed chapter read
// arguments (chapterArguments, stream v:1) on a generated MP4: one whose
// MJPEG track is the QuickTime chapter track (timed_thumbnails) with three
// chapters at 0, 2 and 4 seconds, and the same file before its chapter
// reference pointed at that track. The first packet of the chapter track is
// the attached picture copy the demuxer queues.
func TestParseChapterOutputRealFFprobe(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "chapters-timed-thumbnails.json"))
	if err != nil {
		t.Fatal(err)
	}
	images, err := parseChapterOutput(data, 1)
	if err != nil || len(images) != 3 {
		t.Fatalf("chapter pictures: %d %v", len(images), err)
	}
	wantHashes := []string{"c15e65a81ca2417c1ba0617634d3d3ba0886e59f5cbc45fbb814004260c49500", "522530615ffbaa6ec6c4be1236c972ef4004f6fe85fbf577e8372b6759b641d3", "0f41a98d73f9273d1dd0adccc98900f25fe33fe4c764ce35f870961046c738b9"}
	for i, image := range images {
		if image.Index != i || hex.EncodeToString(image.SHA256[:]) != wantHashes[i] || image.Format != "jpeg" || image.Width != 64 || image.Height != 36 ||
			sha256.Sum256(image.Data) != image.SHA256 {
			t.Fatalf("chapter %d: index %d %dx%d %s", i, image.Index, image.Width, image.Height, image.Format)
		}
	}
	if !domain.ValidChapterImages(images) {
		t.Fatal("parsed pictures fail the domain check")
	}
	if text := fmt.Sprintf("%v %#v", images[0], images[0]); strings.Contains(text, string(images[0].Data[:4])) {
		t.Fatal("chapter image diagnostics expose data")
	}
	plain, err := os.ReadFile(filepath.Join("testdata", "chapters-plain-stream.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseChapterOutput(plain, 1); !errors.Is(err, domain.ErrChapterImagesAbsent) {
		t.Fatal("ordinary picture stream accepted as chapter track", err)
	}
	if _, err := parseChapterOutput(data, 2); !errors.Is(err, domain.ErrChapterImagesAbsent) {
		t.Fatal("unexpected stream index accepted", err)
	}
}

type chapterPacket struct {
	stream int
	pts    *string
	data   []byte
	size   string
	hash   string
}

func chapterDoc(t testing.TB, edit func(stream map[string]any, chapters *[]any, packets *[]chapterPacket)) []byte {
	t.Helper()
	stream := map[string]any{"index": 3, "codec_name": "png", "codec_type": "video", "disposition": map[string]any{"attached_pic": 1, "timed_thumbnails": 1}}
	chapters := []any{map[string]any{"start_time": "0.000000", "end_time": "10.000000"}, map[string]any{"start_time": "10.000000", "end_time": "20.000000"}}
	time := func(v string) *string { return &v }
	packets := []chapterPacket{{stream: 3, pts: time("0.000000"), data: testPicture(t, "png", 8, 4)}, {stream: 3, pts: time("10.000000"), data: testPicture(t, "png", 9, 5)}}
	if edit != nil {
		edit(stream, &chapters, &packets)
	}
	var out []any
	for _, p := range packets {
		sum := sha256.Sum256(p.data)
		packet := map[string]any{"stream_index": p.stream, "size": fmt.Sprint(len(p.data)), "data": ffprobeDump(p.data), "data_hash": "SHA256:" + hex.EncodeToString(sum[:])}
		if p.size != "" {
			packet["size"] = p.size
		}
		if p.hash != "" {
			packet["data_hash"] = p.hash
		}
		if p.pts != nil {
			packet["pts_time"] = *p.pts
		}
		out = append(out, packet)
	}
	data, err := json.Marshal(map[string]any{"packets": out, "streams": []any{stream}, "chapters": chapters})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestParseChapterOutputMapsPacketsToChapters(t *testing.T) {
	time := func(v string) *string { return &v }
	// Unordered chapters, a packet before every chapter, a packet without a
	// time and a second packet in the same chapter are handled: the first
	// picture of a chapter wins and indexes follow chapter start order.
	images, err := parseChapterOutput(chapterDoc(t, func(_ map[string]any, chapters *[]any, packets *[]chapterPacket) {
		*chapters = append([]any{map[string]any{"start_time": "20.000000", "end_time": "30.000000"}}, *chapters...)
		*chapters = append(*chapters, map[string]any{"start_time": "-5.000000", "end_time": "-1.000000"})
		*packets = append(*packets,
			chapterPacket{stream: 3, pts: time("-0.5"), data: testPicture(t, "png", 3, 3)},
			chapterPacket{stream: 3, pts: nil, data: testPicture(t, "png", 4, 4)},
			chapterPacket{stream: 3, pts: time("N/A"), data: testPicture(t, "png", 4, 4)},
			chapterPacket{stream: 3, pts: time("12.5"), data: testPicture(t, "png", 5, 5)},
			chapterPacket{stream: 3, pts: time("25"), data: testPicture(t, "png", 6, 6)})
	}), 3)
	if err != nil || len(images) != 3 {
		t.Fatalf("pictures %d %v", len(images), err)
	}
	for i, want := range []struct{ index, width int }{{1, 8}, {2, 9}, {3, 6}} {
		if images[i].Index != want.index || images[i].Width != want.width || images[i].Format != "png" {
			t.Fatalf("picture %d: %+v", i, images[i].Index)
		}
	}
}

func TestParseChapterOutputRefusals(t *testing.T) {
	time := func(v string) *string { return &v }
	big := bytes.Repeat([]byte{0}, domain.ChapterImageMaxBytes+1)
	for name, tc := range map[string]struct {
		data []byte
		want error
	}{
		"empty":     {nil, domain.ErrChapterImagesInvalid},
		"not_json":  {[]byte("{"), domain.ErrChapterImagesInvalid},
		"no_stream": {[]byte(`{"streams":[],"packets":[],"chapters":[]}`), domain.ErrChapterImagesInvalid},
		"not_timed": {chapterDoc(t, func(s map[string]any, _ *[]any, _ *[]chapterPacket) {
			s["disposition"] = map[string]any{"attached_pic": 1, "timed_thumbnails": 0}
		}), domain.ErrChapterImagesAbsent},
		"audio":       {chapterDoc(t, func(s map[string]any, _ *[]any, _ *[]chapterPacket) { s["codec_type"] = "audio" }), domain.ErrChapterImagesAbsent},
		"codec":       {chapterDoc(t, func(s map[string]any, _ *[]any, _ *[]chapterPacket) { s["codec_name"] = "h264" }), domain.ErrChapterImagesAbsent},
		"no_chapters": {chapterDoc(t, func(_ map[string]any, c *[]any, _ *[]chapterPacket) { *c = nil }), domain.ErrChapterImagesAbsent},
		"bad_chapter": {chapterDoc(t, func(_ map[string]any, c *[]any, _ *[]chapterPacket) { (*c)[0] = map[string]any{"start_time": "x"} }), domain.ErrChapterImagesInvalid},
		"backwards": {chapterDoc(t, func(_ map[string]any, c *[]any, _ *[]chapterPacket) {
			(*c)[0] = map[string]any{"start_time": "5", "end_time": "1"}
		}), domain.ErrChapterImagesInvalid},
		"other_stream": {chapterDoc(t, func(_ map[string]any, _ *[]any, p *[]chapterPacket) { (*p)[1].stream = 4 }), domain.ErrChapterImagesInvalid},
		"bad_hash": {chapterDoc(t, func(_ map[string]any, _ *[]any, p *[]chapterPacket) {
			(*p)[1].hash = "SHA256:" + strings.Repeat("0", 64)
		}), domain.ErrChapterImagesInvalid},
		"bad_size":      {chapterDoc(t, func(_ map[string]any, _ *[]any, p *[]chapterPacket) { (*p)[0].size = "0" }), domain.ErrChapterImagesInvalid},
		"short_dump":    {chapterDoc(t, func(_ map[string]any, _ *[]any, p *[]chapterPacket) { (*p)[0].size = "9999" }), domain.ErrChapterImagesInvalid},
		"not_a_picture": {chapterDoc(t, func(_ map[string]any, _ *[]any, p *[]chapterPacket) { (*p)[0].data = []byte("plain bytes, no picture") }), domain.ErrChapterImagesInvalid},
		"one_too_large": {chapterDoc(t, func(_ map[string]any, _ *[]any, p *[]chapterPacket) { (*p)[0].data = big }), domain.ErrChapterImagesTooLarge},
		"outside_all": {chapterDoc(t, func(_ map[string]any, _ *[]any, p *[]chapterPacket) {
			*p = []chapterPacket{{stream: 3, pts: time("99"), data: testPicture(t, "png", 2, 2)}}
		}), domain.ErrChapterImagesAbsent},
		"too_many_packets": {chapterDoc(t, func(_ map[string]any, _ *[]any, p *[]chapterPacket) {
			for len(*p) <= 65 {
				*p = append(*p, (*p)[0])
			}
		}), domain.ErrChapterImagesInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseChapterOutput(tc.data, 3); !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
		})
	}
	// The total bound: many chapters whose pictures together exceed it.
	picture := testPicture(t, "jpeg", 400, 400)
	count := domain.ChapterImageMaxTotalBytes/len(picture) + 2
	if count > domain.ChapterImageMaxPerItem {
		t.Skip("fixture picture too small for the total bound")
	}
	total := chapterDoc(t, func(s map[string]any, c *[]any, p *[]chapterPacket) {
		s["codec_name"] = "mjpeg"
		*c, *p = nil, nil
		for i := range count {
			at := fmt.Sprint(i * 10)
			*c = append(*c, map[string]any{"start_time": at, "end_time": fmt.Sprint(i*10 + 10)})
			*p = append(*p, chapterPacket{stream: 3, pts: time(at), data: picture})
		}
	})
	if _, err := parseChapterOutput(total, 3); !errors.Is(err, domain.ErrChapterImagesTooLarge) {
		t.Fatal("total bound not enforced", err, len(picture), count)
	}
}

func chapterCandidate(t *testing.T) (domain.ChapterImageCandidate, string) {
	t.Helper()
	cover, name := coverCandidate(t)
	return domain.ChapterImageCandidate{ItemID: cover.ItemID, LibraryID: cover.LibraryID, RootID: cover.RootID, RootPath: cover.RootPath,
		RelativePath: cover.RelativePath, Stamp: cover.Stamp, Streams: []domain.ChapterImageStream{{StreamIndex: 1, VideoIndex: 1}, {StreamIndex: 3, VideoIndex: 2}}}, name
}

func TestExtractChapterImagesTriesStreamsOnTheProbedFile(t *testing.T) {
	candidate, name := chapterCandidate(t)
	var operations []string
	adapter := &ChapterAdapter{runner: adapterExecutorFunc(func(_ context.Context, request process.Request) (process.Result, error) {
		operations = append(operations, request.Operation)
		if request.Tool != "ffprobe" || request.Stdin == nil {
			t.Fatal("unexpected chapter request")
		}
		if request.Operation == "chapters-1" {
			// The first picture stream is an ordinary cover.
			return process.Result{Stdout: chapterDoc(t, func(s map[string]any, _ *[]any, _ *[]chapterPacket) {
				s["index"], s["disposition"] = 1, map[string]any{"attached_pic": 1, "timed_thumbnails": 0}
			})}, nil
		}
		return process.Result{Stdout: chapterDoc(t, nil)}, nil
	})}
	images, err := adapter.ExtractChapterImages(context.Background(), candidate)
	if err != nil || len(images) != 2 || strings.Join(operations, ",") != "chapters-1,chapters-2" {
		t.Fatal("extract", err, operations)
	}
	// No stream is a chapter track: absent after trying each once.
	operations = nil
	none := &ChapterAdapter{runner: adapterExecutorFunc(func(_ context.Context, request process.Request) (process.Result, error) {
		operations = append(operations, request.Operation)
		return process.Result{Stdout: chapterDoc(t, func(s map[string]any, _ *[]any, _ *[]chapterPacket) { s["disposition"] = map[string]any{} })}, nil
	})}
	if _, err := none.ExtractChapterImages(context.Background(), candidate); !errors.Is(err, domain.ErrChapterImagesAbsent) || len(operations) != 2 {
		t.Fatal("absent", err, operations)
	}
	// A refusal on the first stream ends the read.
	operations = nil
	limited := &ChapterAdapter{runner: adapterExecutorFunc(func(_ context.Context, request process.Request) (process.Result, error) {
		operations = append(operations, request.Operation)
		return process.Result{}, process.ErrOutputLimit
	})}
	if _, err := limited.ExtractChapterImages(context.Background(), candidate); !errors.Is(err, domain.ErrChapterImagesTooLarge) || len(operations) != 1 {
		t.Fatal("output limit", err, operations)
	}
	stale := candidate
	stale.Stamp.Fingerprint = strings.Repeat("0", 64)
	operations = nil
	if _, err := adapter.ExtractChapterImages(context.Background(), stale); !errors.Is(err, domain.ErrProbeSourceChanged) || len(operations) != 0 {
		t.Fatal("stale fingerprint ran a child", err)
	}
	writer := &ChapterAdapter{runner: adapterExecutorFunc(func(context.Context, process.Request) (process.Result, error) {
		if err := os.WriteFile(name, []byte("replaced"), 0600); err != nil {
			t.Fatal(err)
		}
		return process.Result{Stdout: chapterDoc(t, nil)}, nil
	})}
	if _, err := writer.ExtractChapterImages(context.Background(), candidate); !errors.Is(err, domain.ErrProbeSourceChanged) {
		t.Fatal("concurrent change accepted", err)
	}
	invalid := candidate
	invalid.Streams = []domain.ChapterImageStream{{StreamIndex: 40, VideoIndex: domain.EmbeddedCoverMaxVideoIndex + 1}}
	if _, err := adapter.ExtractChapterImages(context.Background(), invalid); !errors.Is(err, ErrInvalidInput) {
		t.Fatal("out-of-range index accepted", err)
	}
	if _, err := (*ChapterAdapter)(nil).ExtractChapterImages(context.Background(), candidate); !errors.Is(err, ErrInvalidInput) {
		t.Fatal("nil adapter")
	}
	if _, err := NewChapterAdapter(nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatal("nil runner accepted")
	}
	if a, err := NewChapterAdapter(&process.IsolatedRunner{}); err != nil || a == nil {
		t.Fatal("isolated runner refused", err)
	}
}

func TestExtractChapterImagesProcessErrors(t *testing.T) {
	for name, tc := range map[string]struct{ err, want error }{
		"output_limit": {process.ErrOutputLimit, domain.ErrChapterImagesTooLarge},
		"tool_refused": {process.ErrExit, domain.ErrChapterImagesInvalid},
		"timeout":      {process.ErrTimeout, context.DeadlineExceeded},
		"busy":         {process.ErrBusy, domain.ErrProbeBusy},
		"sandbox":      {process.ErrSandboxUnavailable, domain.ErrProbeRuntimeUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			candidate, _ := chapterCandidate(t)
			adapter := &ChapterAdapter{runner: adapterExecutorFunc(func(context.Context, process.Request) (process.Result, error) { return process.Result{}, tc.err })}
			if _, err := adapter.ExtractChapterImages(context.Background(), candidate); !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
		})
	}
}
