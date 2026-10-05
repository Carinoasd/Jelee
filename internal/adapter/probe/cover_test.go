package probe

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/process"
)

// ffprobeDump formats bytes like ffprobe's -show_data writer.
func ffprobeDump(data []byte) string {
	var b strings.Builder
	b.WriteString("\n")
	for offset := 0; offset < len(data); offset += 16 {
		line := data[offset:min(offset+16, len(data))]
		fmt.Fprintf(&b, "%08x: ", offset)
		for i, c := range line {
			fmt.Fprintf(&b, "%02x", c)
			if i&1 == 1 {
				b.WriteByte(' ')
			}
		}
		b.WriteString(strings.Repeat(" ", 41-2*len(line)-len(line)/2))
		for _, c := range line {
			if c >= 32 && c < 127 {
				b.WriteByte(c)
			} else {
				b.WriteByte('.')
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func testPicture(t testing.TB, format string, width, height int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetNRGBA(x, y, color.NRGBA{uint8(x), uint8(y), 7, 255})
		}
	}
	var buffer bytes.Buffer
	var err error
	if format == "jpeg" {
		err = jpeg.Encode(&buffer, img, nil)
	} else {
		err = png.Encode(&buffer, img)
	}
	if err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

type coverDoc struct {
	stream, packetStream, attached int
	codecType, codec, size, hash   string
	data                           *string
	packets, streams               int
}

func coverJSON(t testing.TB, payload []byte, edit func(*coverDoc)) []byte {
	t.Helper()
	sum := sha256.Sum256(payload)
	dump := ffprobeDump(payload)
	d := coverDoc{stream: 2, packetStream: 2, attached: 1, codecType: "video", codec: "png", size: fmt.Sprint(len(payload)), hash: "SHA256:" + hex.EncodeToString(sum[:]), data: &dump, packets: 1, streams: 1}
	if edit != nil {
		edit(&d)
	}
	packet := map[string]any{"stream_index": d.packetStream, "size": d.size, "data_hash": d.hash}
	if d.data != nil {
		packet["data"] = *d.data
	}
	stream := map[string]any{"index": d.stream, "codec_name": d.codec, "codec_type": d.codecType, "width": 16, "height": 16, "disposition": map[string]any{"attached_pic": d.attached}}
	doc := map[string]any{"packets": []any{}, "programs": []any{}, "stream_groups": []any{}, "streams": []any{}}
	for i := 0; i < d.packets; i++ {
		doc["packets"] = append(doc["packets"].([]any), packet)
	}
	for i := 0; i < d.streams; i++ {
		doc["streams"] = append(doc["streams"].([]any), stream)
	}
	data, err := json.MarshalIndent(doc, "", "    ")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestParseCoverOutputReturnsTheExactPacket(t *testing.T) {
	for _, format := range []string{"png", "jpeg"} {
		payload := testPicture(t, format, 33, 17)
		codec := map[string]string{"png": "png", "jpeg": "mjpeg"}[format]
		cover, err := parseCoverOutput(coverJSON(t, payload, func(d *coverDoc) { d.codec = codec }), 2)
		if err != nil || !bytes.Equal(cover.Data, payload) || cover.SHA256 != sha256.Sum256(payload) || cover.Format != format || cover.Width != 33 || cover.Height != 17 {
			t.Fatal("cover payload", format, err)
		}
		if text := fmt.Sprintf("%v %#v", cover, cover); strings.Contains(text, string(payload[:4])) {
			t.Fatal("cover diagnostics expose data")
		}
	}
}

func TestParseCoverOutputRefusals(t *testing.T) {
	payload := testPicture(t, "png", 16, 16)
	garbage := ffprobeDump([]byte("not a picture at all, just bytes"))
	short := ffprobeDump(payload[:len(payload)-1])
	gap := strings.Replace(ffprobeDump(payload), "\n00000010: ", "\n00000020: ", 1)
	upper := strings.Replace(ffprobeDump(payload), "8950", "8950", 1)
	upper = strings.Replace(upper, "4e47", "4E47", 1)
	noLead := strings.TrimPrefix(ffprobeDump(payload), "\n")
	noTail := strings.TrimSuffix(ffprobeDump(payload), "\n")
	wide := strings.Replace(ffprobeDump(payload), "  .PNG", "   .PNG", 1)
	nonHex := strings.Replace(ffprobeDump(payload), "8950", "89zz", 1)
	shortLine := strings.Replace(ffprobeDump(payload), "\n00000010: ", "\n0010: ", 1)
	for name, tc := range map[string]struct {
		data []byte
		want error
	}{
		"empty":           {nil, domain.ErrEmbeddedCoverInvalid},
		"not_json":        {[]byte("{"), domain.ErrEmbeddedCoverInvalid},
		"no_stream":       {coverJSON(t, payload, func(d *coverDoc) { d.streams = 0 }), domain.ErrEmbeddedCoverInvalid},
		"two_streams":     {coverJSON(t, payload, func(d *coverDoc) { d.streams = 2 }), domain.ErrEmbeddedCoverInvalid},
		"wrong_stream":    {coverJSON(t, payload, func(d *coverDoc) { d.stream = 3 }), domain.ErrEmbeddedCoverAbsent},
		"not_attached":    {coverJSON(t, payload, func(d *coverDoc) { d.attached = 0 }), domain.ErrEmbeddedCoverAbsent},
		"not_video":       {coverJSON(t, payload, func(d *coverDoc) { d.codecType = "attachment" }), domain.ErrEmbeddedCoverAbsent},
		"video_codec":     {coverJSON(t, payload, func(d *coverDoc) { d.codec = "h264" }), domain.ErrEmbeddedCoverAbsent},
		"no_packet":       {coverJSON(t, payload, func(d *coverDoc) { d.packets = 0 }), domain.ErrEmbeddedCoverInvalid},
		"two_packets":     {coverJSON(t, payload, func(d *coverDoc) { d.packets = 2 }), domain.ErrEmbeddedCoverInvalid},
		"packet_stream":   {coverJSON(t, payload, func(d *coverDoc) { d.packetStream = 0 }), domain.ErrEmbeddedCoverInvalid},
		"no_data":         {coverJSON(t, payload, func(d *coverDoc) { d.data = nil }), domain.ErrEmbeddedCoverInvalid},
		"bad_size":        {coverJSON(t, payload, func(d *coverDoc) { d.size = "x" }), domain.ErrEmbeddedCoverInvalid},
		"zero_size":       {coverJSON(t, payload, func(d *coverDoc) { d.size = "0" }), domain.ErrEmbeddedCoverInvalid},
		"declared_large":  {coverJSON(t, payload, func(d *coverDoc) { d.size = fmt.Sprint(domain.EmbeddedCoverMaxBytes + 1) }), domain.ErrEmbeddedCoverTooLarge},
		"size_mismatch":   {coverJSON(t, payload, func(d *coverDoc) { d.data = &short }), domain.ErrEmbeddedCoverInvalid},
		"hash_mismatch":   {coverJSON(t, payload, func(d *coverDoc) { d.hash = "SHA256:" + strings.Repeat("0", 64) }), domain.ErrEmbeddedCoverInvalid},
		"offset_gap":      {coverJSON(t, payload, func(d *coverDoc) { d.data = &gap }), domain.ErrEmbeddedCoverInvalid},
		"uppercase_hex":   {coverJSON(t, payload, func(d *coverDoc) { d.data = &upper }), domain.ErrEmbeddedCoverInvalid},
		"no_leading_line": {coverJSON(t, payload, func(d *coverDoc) { d.data = &noLead }), domain.ErrEmbeddedCoverInvalid},
		"unterminated":    {coverJSON(t, payload, func(d *coverDoc) { d.data = &noTail }), domain.ErrEmbeddedCoverInvalid},
		"column_width":    {coverJSON(t, payload, func(d *coverDoc) { d.data = &wide }), domain.ErrEmbeddedCoverInvalid},
		"non_hex":         {coverJSON(t, payload, func(d *coverDoc) { d.data = &nonHex }), domain.ErrEmbeddedCoverInvalid},
		"short_offset":    {coverJSON(t, payload, func(d *coverDoc) { d.data = &shortLine }), domain.ErrEmbeddedCoverInvalid},
		"not_a_picture": {coverJSON(t, []byte("not a picture at all, just bytes"), func(d *coverDoc) {
			d.data = &garbage
		}), domain.ErrEmbeddedCoverInvalid},
		"jpeg_as_png": {coverJSON(t, testPicture(t, "jpeg", 8, 8), nil), domain.ErrEmbeddedCoverInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseCoverOutput(tc.data, 2); !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
		})
	}
	if _, err := decodeHexDump("\n", 0); !errors.Is(err, domain.ErrEmbeddedCoverInvalid) {
		t.Fatal("empty dump accepted")
	}
	// Whole lines missing at the end still fail the total size check.
	odd := testPicture(t, "png", 16, 16)
	if len(odd)%16 == 0 {
		odd = append(odd, 0)
	}
	if _, err := decodeHexDump(ffprobeDump(odd[:len(odd)-len(odd)%16]), len(odd)); !errors.Is(err, domain.ErrEmbeddedCoverInvalid) {
		t.Fatal("truncated dump accepted", err)
	}
	// A padding column used for a 33rd digit is refused.
	crowded := []byte(ffprobeDump(odd))
	crowded[1+50] = 'a'
	if _, err := decodeHexDump(string(crowded), len(odd)); !errors.Is(err, domain.ErrEmbeddedCoverInvalid) {
		t.Fatal("overfull hex column accepted", err)
	}
}

func TestCoverDimensionsPrecheck(t *testing.T) {
	// A PNG header declaring a huge frame is refused before any pixel decode.
	header := testPicture(t, "png", 1, 1)
	huge := append([]byte(nil), header...)
	huge[16], huge[17], huge[18], huge[19] = 0, 0, 0x50, 0x00 // width 20480
	binary.BigEndian.PutUint32(huge[29:33], crc32.ChecksumIEEE(huge[12:29]))
	if _, _, err := coverDimensions(huge, "png"); !errors.Is(err, domain.ErrEmbeddedCoverTooLarge) {
		t.Fatal("oversized frame passed the precheck", err)
	}
	if _, _, err := coverDimensions([]byte("\x89PNG\r\n\x1a\nbroken"), "png"); !errors.Is(err, domain.ErrEmbeddedCoverInvalid) {
		t.Fatal("broken header accepted", err)
	}
	if _, _, err := coverDimensions(header, "gif"); !errors.Is(err, domain.ErrEmbeddedCoverInvalid) {
		t.Fatal("unsupported format accepted", err)
	}
}

func coverCandidate(t *testing.T) (domain.EmbeddedCoverCandidate, string) {
	t.Helper()
	source, name, _ := adapterFixture(t)
	stamp, err := Inspect(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	return domain.EmbeddedCoverCandidate{ItemID: "11111111-1111-4111-8111-111111111111", LibraryID: "22222222-2222-4222-8222-222222222222",
		RootID: "33333333-3333-4333-8333-333333333333", RootPath: source.RootPath, RelativePath: source.RelativePath, Stamp: stamp, StreamIndex: 2, VideoIndex: 1}, name
}

func TestExtractCoverBindsTheProbedFile(t *testing.T) {
	payload := testPicture(t, "png", 20, 10)
	candidate, name := coverCandidate(t)
	calls := 0
	adapter := &CoverAdapter{runner: adapterExecutorFunc(func(_ context.Context, request process.Request) (process.Result, error) {
		calls++
		if request.Tool != "ffprobe" || request.Operation != "cover-1" || request.Stdin == nil {
			t.Fatal("unexpected cover request")
		}
		return process.Result{Stdout: coverJSON(t, payload, nil)}, nil
	})}
	cover, err := adapter.ExtractCover(context.Background(), candidate)
	if err != nil || !bytes.Equal(cover.Data, payload) || calls != 1 {
		t.Fatal("extract", err)
	}
	stale := candidate
	stale.Stamp.Fingerprint = strings.Repeat("0", 64)
	if _, err := adapter.ExtractCover(context.Background(), stale); !errors.Is(err, domain.ErrProbeSourceChanged) || calls != 1 {
		t.Fatal("stale fingerprint ran a child", err)
	}
	moved := candidate
	moved.Stamp.Size++
	if _, err := adapter.ExtractCover(context.Background(), moved); !errors.Is(err, domain.ErrProbeSourceChanged) || calls != 1 {
		t.Fatal("stale size ran a child", err)
	}
	// A write while the child runs is detected afterwards.
	writer := &CoverAdapter{runner: adapterExecutorFunc(func(context.Context, process.Request) (process.Result, error) {
		later := time.Now().Add(time.Hour)
		if err := os.WriteFile(name, []byte("replaced"), 0600); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(name, later, later)
		return process.Result{Stdout: coverJSON(t, payload, nil)}, nil
	})}
	if _, err := writer.ExtractCover(context.Background(), candidate); !errors.Is(err, domain.ErrProbeSourceChanged) {
		t.Fatal("concurrent change accepted", err)
	}
	missing := candidate
	missing.RelativePath = "folder/absent.mp4"
	if _, err := adapter.ExtractCover(context.Background(), missing); !errors.Is(err, domain.ErrProbeInputUnavailable) {
		t.Fatal("missing file", err)
	}
	invalid := candidate
	invalid.VideoIndex = domain.EmbeddedCoverMaxVideoIndex + 1
	if _, err := adapter.ExtractCover(context.Background(), invalid); !errors.Is(err, ErrInvalidInput) {
		t.Fatal("out-of-range index accepted", err)
	}
	if _, err := (*CoverAdapter)(nil).ExtractCover(context.Background(), candidate); !errors.Is(err, ErrInvalidInput) {
		t.Fatal("nil adapter")
	}
	if _, err := NewCoverAdapter(nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatal("nil runner accepted")
	}
	if a, err := NewCoverAdapter(&process.IsolatedRunner{}); err != nil || a == nil {
		t.Fatal("isolated runner refused", err)
	}
	// The registered path replaced or removed while the child ran.
	for _, replace := range []bool{true, false} {
		candidate, name := coverCandidate(t)
		swap := &CoverAdapter{runner: adapterExecutorFunc(func(context.Context, process.Request) (process.Result, error) {
			data, err := os.ReadFile(name)
			info, statErr := os.Stat(name)
			if err != nil || statErr != nil || os.Rename(name, name+".old") != nil {
				t.Fatal("swap fixture")
			}
			if replace {
				if os.WriteFile(name, data, 0600) != nil || os.Chtimes(name, info.ModTime(), info.ModTime()) != nil {
					t.Fatal("swap fixture")
				}
			}
			return process.Result{Stdout: coverJSON(t, payload, nil)}, nil
		})}
		if _, err := swap.ExtractCover(context.Background(), candidate); !errors.Is(err, domain.ErrProbeSourceChanged) {
			t.Fatal("replaced path accepted", replace, err)
		}
	}
	candidate, _ = coverCandidate(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancelling := &CoverAdapter{runner: adapterExecutorFunc(func(context.Context, process.Request) (process.Result, error) {
		cancel()
		return process.Result{}, process.ErrCancelled
	})}
	if _, err := cancelling.ExtractCover(ctx, candidate); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation", err)
	}
	garbled := &CoverAdapter{runner: adapterExecutorFunc(func(context.Context, process.Request) (process.Result, error) {
		return process.Result{Stdout: []byte("{}")}, nil
	})}
	if _, err := garbled.ExtractCover(context.Background(), candidate); !errors.Is(err, domain.ErrEmbeddedCoverInvalid) {
		t.Fatal("garbled output", err)
	}
}

func TestExtractCoverProcessErrors(t *testing.T) {
	candidate, _ := coverCandidate(t)
	for name, tc := range map[string]struct{ err, want error }{
		"output_limit": {process.ErrOutputLimit, domain.ErrEmbeddedCoverTooLarge},
		"tool_refused": {process.ErrExit, domain.ErrEmbeddedCoverInvalid},
		"timeout":      {process.ErrTimeout, context.DeadlineExceeded},
		"busy":         {process.ErrBusy, domain.ErrProbeBusy},
		"sandbox":      {process.ErrSandboxUnavailable, domain.ErrProbeRuntimeUnavailable},
		"unknown":      {errors.New("other"), domain.ErrProbeRuntimeUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			adapter := &CoverAdapter{runner: adapterExecutorFunc(func(context.Context, process.Request) (process.Result, error) { return process.Result{}, tc.err })}
			if _, err := adapter.ExtractCover(context.Background(), candidate); !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
		})
	}
}

func TestParseJSONRecordsAttachedPictureDisposition(t *testing.T) {
	metadata, err := ParseJSON([]byte(`{"format":{},"streams":[{"index":0,"codec_type":"video","codec_name":"h264","disposition":{"attached_pic":0}},{"index":1,"codec_type":"video","codec_name":"mjpeg","disposition":{"attached_pic":1}},{"index":2,"codec_type":"audio","codec_name":"aac"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if s := metadata.Streams; s[0].AttachedPic == nil || *s[0].AttachedPic || s[1].AttachedPic == nil || !*s[1].AttachedPic || s[2].AttachedPic != nil {
		t.Fatal("attached_pic disposition not recorded")
	}
	if stream, video, ok := domain.EmbeddedCoverStream(metadata); !ok || stream != 1 || video != 1 {
		t.Fatal("cover stream selection", stream, video, ok)
	}
	if _, err := ParseJSON([]byte(`{"format":{},"streams":[{"index":0,"codec_type":"video","disposition":{"attached_pic":2}}]}`)); err == nil {
		t.Fatal("non-boolean disposition accepted")
	}
}
