package containerlayout

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// box builds one MP4 box with a 32-bit size.
func box(kind string, payload int) []byte {
	out := make([]byte, 8+payload)
	binary.BigEndian.PutUint32(out, uint32(8+payload))
	copy(out[4:], kind)
	return out
}

// largeBox builds one MP4 box with a 64-bit size.
func largeBox(kind string, payload int) []byte {
	out := make([]byte, 16+payload)
	binary.BigEndian.PutUint32(out, 1)
	copy(out[4:], kind)
	binary.BigEndian.PutUint64(out[8:], uint64(16+payload))
	return out
}

func join(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

func read(t *testing.T, data []byte, contentType string) domain.SourceLayout {
	t.Helper()
	got, err := Read(context.Background(), bytes.NewReader(data), int64(len(data)), contentType)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestReadMP4(t *testing.T) {
	ftyp := box("ftyp", 16)
	moov := box("moov", 100)
	for _, tc := range []struct {
		name   string
		data   []byte
		want   domain.ContainerLayout
		offset int64
		length int64
	}{
		{"faststart", join(ftyp, moov, box("mdat", 1000)), domain.LayoutMoovBeforeMdat, 24, 108},
		{"moov at end", join(ftyp, box("free", 8), box("mdat", 1000), moov), domain.LayoutMoovAtEnd, 24 + 16 + 1008, 108},
		{"moov at end after large mdat", join(ftyp, largeBox("mdat", 4000), moov), domain.LayoutMoovAtEnd, 24 + 4016, 108},
		{"fragmented", join(ftyp, moov, box("moof", 20), box("mdat", 100), box("moof", 20), box("mdat", 100)), domain.LayoutFragmented, 24, 108},
		{"fragmented with sidx", join(ftyp, moov, box("sidx", 12), box("moof", 20), box("mdat", 100)), domain.LayoutFragmented, 24, 108},
		{"moov only", join(ftyp, moov), domain.LayoutMoovBeforeMdat, 24, 108},
		{"quicktime without ftyp", join(box("wide", 0), box("mdat", 500), moov), domain.LayoutMoovAtEnd, 8 + 508, 108},
		{"zero-size final mdat after moov", append(join(ftyp, moov), 0, 0, 0, 0, 'm', 'd', 'a', 't', 1, 2, 3), domain.LayoutMoovBeforeMdat, 24, 108},
		{"no moov", join(ftyp, box("mdat", 100)), domain.LayoutUnknown, 0, 0},
		{"zero-size mdat before moov", append(join(ftyp), 0, 0, 0, 0, 'm', 'd', 'a', 't', 1, 2, 3), domain.LayoutUnknown, 0, 0},
		{"truncated mdat", join(ftyp, box("mdat", 100))[:60], domain.LayoutUnknown, 0, 0},
		{"box larger than file", join(ftyp, box("mdat", 100)[:8]), domain.LayoutUnknown, 0, 0},
		{"box shorter than header", join(ftyp, []byte{0, 0, 0, 4, 'm', 'd', 'a', 't'}), domain.LayoutUnknown, 0, 0},
		{"large size shorter than header", join(ftyp, []byte{0, 0, 0, 1, 'm', 'd', 'a', 't', 0, 0, 0, 0, 0, 0, 0, 8}), domain.LayoutUnknown, 0, 0},
		{"large size overflow", join(ftyp, []byte{0, 0, 0, 1, 'm', 'd', 'a', 't', 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}), domain.LayoutUnknown, 0, 0},
		{"binary type", join(ftyp, box("\x00\x01\x02\x03", 4)), domain.LayoutUnknown, 0, 0},
		{"not an mp4", []byte("RIFF\x00\x00\x00\x00AVI LIST"), domain.LayoutUnknown, 0, 0},
		{"duplicate moov", join(ftyp, moov, moov, box("mdat", 10)), domain.LayoutUnknown, 0, 0},
		{"fragment before moov", join(ftyp, box("moof", 10), moov, box("mdat", 10)), domain.LayoutUnknown, 0, 0},
		{"empty", nil, domain.LayoutUnknown, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := read(t, tc.data, "video/mp4")
			if got.Layout != tc.want || got.IndexOffset != tc.offset || got.IndexLength != tc.length {
				t.Fatalf("got %+v, want %s at %d+%d", got, tc.want, tc.offset, tc.length)
			}
		})
	}
}

func TestReadMP4BoxBudget(t *testing.T) {
	// Many tiny boxes before the movie box exhaust the walk, not the reader.
	parts := [][]byte{box("ftyp", 8)}
	for range maxBoxes {
		parts = append(parts, box("free", 0))
	}
	parts = append(parts, box("mdat", 10), box("moov", 10))
	if got := read(t, join(parts...), "video/mp4"); got.Layout != domain.LayoutUnknown {
		t.Fatalf("box budget: %+v", got)
	}
}

// countingReader records every byte range read.
type countingReader struct {
	data  []byte
	bytes int
	err   error
}

func (r *countingReader) ReadAt(p []byte, off int64) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	r.bytes += len(p)
	if off >= int64(len(r.data)) {
		return 0, io.EOF
	}
	n := copy(p, r.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func TestReadSkipsMediaData(t *testing.T) {
	data := join(box("ftyp", 16), box("mdat", 8<<20), box("moov", 1000))
	reader := &countingReader{data: data}
	got, err := Read(context.Background(), reader, int64(len(data)), "video/quicktime")
	if err != nil || got.Layout != domain.LayoutMoovAtEnd {
		t.Fatal(got, err)
	}
	if reader.bytes > 64 {
		t.Fatalf("read %d bytes for three box headers", reader.bytes)
	}
}

func TestReadReportsIOErrors(t *testing.T) {
	failure := errors.New("disk failed")
	data := join(box("ftyp", 16), box("moov", 10), box("mdat", 10))
	if _, err := Read(context.Background(), &countingReader{data: data, err: failure}, int64(len(data)), "video/mp4"); !errors.Is(err, failure) {
		t.Fatal("I/O error must be returned so the source is read again later:", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Read(ctx, bytes.NewReader(data), int64(len(data)), "video/mp4"); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled read:", err)
	}
	// A file shorter than its recorded stamp is not an I/O error.
	if got, err := Read(context.Background(), bytes.NewReader(data[:20]), int64(len(data)), "video/mp4"); err != nil || got.Layout != domain.LayoutUnknown {
		t.Fatal("shrunk file:", got, err)
	}
	if _, err := Read(context.Background(), nil, 1, "video/mp4"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("nil reader accepted")
	}
	if got := read(t, data, "video/x-msvideo"); got.Layout != domain.LayoutUnknown || got.IndexLength != 0 {
		t.Fatal("uninspected container:", got)
	}
}

// ---- Matroska ----

// vint encodes an EBML data size in the shortest form (8 bytes for huge).
func vint(n int64) []byte {
	for length := 1; length <= 8; length++ {
		if n < int64(1)<<(7*length)-1 {
			out := make([]byte, length)
			v := uint64(n)
			for i := length - 1; i >= 0; i-- {
				out[i] = byte(v)
				v >>= 8
			}
			out[0] |= 0x80 >> (length - 1)
			return out
		}
	}
	panic("size too large")
}

func idBytes(id uint32) []byte {
	switch {
	case id >= 1<<24:
		return []byte{byte(id >> 24), byte(id >> 16), byte(id >> 8), byte(id)}
	case id >= 1<<16:
		return []byte{byte(id >> 16), byte(id >> 8), byte(id)}
	case id >= 1<<8:
		return []byte{byte(id >> 8), byte(id)}
	}
	return []byte{byte(id)}
}

func ebml(id uint32, payload []byte) []byte {
	return join(idBytes(id), vint(int64(len(payload))), payload)
}

func uintBytes(v int64) []byte {
	out := []byte{}
	for v > 0 || len(out) == 0 {
		out = append([]byte{byte(v)}, out...)
		v >>= 8
	}
	return out
}

func seek(id uint32, position int64) []byte {
	return ebml(idSeek, join(ebml(idSeekID, idBytes(id)), ebml(idSeekPos, uintBytes(position))))
}

const (
	idInfo   = 0x1549A966
	idTracks = 0x1654AE6B
	idVoid   = 0xEC
)

// matroska assembles an EBML header and a segment from top-level elements.
// build receives each element's position relative to the segment payload
// and must return elements whose lengths do not depend on the positions
// (SeekHeads use fixed-width positions); it is called twice.
func matroska(build func(p []int64) [][]byte) []byte {
	header := ebml(idEBML, ebml(0x4282, []byte("matroska")))
	draft := build(make([]int64, 1024))
	positions := make([]int64, max(len(draft), 1024))
	pos := int64(0)
	for i, c := range draft {
		positions[i] = pos
		pos += int64(len(c))
	}
	body := join(build(positions)...)
	return join(header, idBytes(idSegment), vint(int64(len(body))), body)
}

// fixedSeek names id at a two-byte-wide position.
func fixedSeek(id uint32, position int64) []byte {
	return ebml(idSeek, join(ebml(idSeekID, idBytes(id)), ebml(idSeekPos, []byte{byte(position >> 8), byte(position)})))
}

func TestReadMatroska(t *testing.T) {
	info, tracks := ebml(idInfo, make([]byte, 20)), ebml(idTracks, make([]byte, 40))
	cluster := ebml(idCluster, make([]byte, 500))
	cues := ebml(idCues, make([]byte, 30))
	void := ebml(idVoid, make([]byte, 4))
	cuesAfter := matroska(func(p []int64) [][]byte {
		return [][]byte{ebml(idSeekHead, join(fixedSeek(idInfo, p[1]), fixedSeek(idTracks, p[2]), fixedSeek(idCues, p[5]))), info, tracks, cluster, cluster, cues}
	})
	cuesBefore := matroska(func(p []int64) [][]byte {
		return [][]byte{ebml(idSeekHead, join(fixedSeek(idInfo, p[1]), fixedSeek(idCues, p[3]))), info, tracks, cues, cluster}
	})
	noCues := matroska(func(p []int64) [][]byte {
		return [][]byte{ebml(idSeekHead, join(fixedSeek(idInfo, p[1]), fixedSeek(idTracks, p[2]))), info, tracks, cluster, cluster}
	})
	wrongTarget := matroska(func(p []int64) [][]byte {
		// Names the cluster as the Cues.
		return [][]byte{ebml(idSeekHead, fixedSeek(idCues, p[3])), info, tracks, cluster, cues}
	})
	chained := matroska(func(p []int64) [][]byte {
		return [][]byte{ebml(idSeekHead, join(fixedSeek(idInfo, p[1]), fixedSeek(idSeekHead, p[4]))), info, cluster, cues, ebml(idSeekHead, fixedSeek(idCues, p[3]))}
	})
	inlineCues := matroska(func([]int64) [][]byte { return [][]byte{void, info, cues, cluster} })
	unknownSize := join(ebml(idEBML, ebml(0x4282, []byte("webm"))), idBytes(idSegment), []byte{0x01, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, info, cues, cluster)
	for _, tc := range []struct {
		name string
		data []byte
		want domain.ContainerLayout
		// cues reports whether a recorded index must start at a Cues ID.
		cues bool
	}{
		{"cues after clusters", cuesAfter, domain.LayoutCuesAfterClusters, true},
		{"cues before clusters", cuesBefore, domain.LayoutCuesBeforeClusters, true},
		{"no cues", noCues, domain.LayoutNoCues, false},
		{"seekhead to wrong element", wrongTarget, domain.LayoutNoCues, false},
		{"second seekhead", chained, domain.LayoutCuesAfterClusters, true},
		{"inline cues without seekhead", inlineCues, domain.LayoutCuesBeforeClusters, true},
		{"unknown-size segment", unknownSize, domain.LayoutCuesBeforeClusters, true},
		{"no clusters", matroska(func([]int64) [][]byte { return [][]byte{void, info, tracks} }), domain.LayoutUnknown, false},
		{"truncated before cluster", cuesAfter[:60], domain.LayoutUnknown, false},
		{"not ebml", []byte("\x00\x00\x00\x18ftypisom"), domain.LayoutUnknown, false},
		{"ebml without segment", ebml(idEBML, nil), domain.LayoutUnknown, false},
		{"empty", nil, domain.LayoutUnknown, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := read(t, tc.data, "video/x-matroska")
			if got.Layout != tc.want {
				t.Fatalf("got %+v, want %s", got, tc.want)
			}
			if tc.cues != (got.IndexLength > 0) {
				t.Fatalf("index range %+v", got)
			}
			if tc.cues && !bytes.HasPrefix(tc.data[got.IndexOffset:], idBytes(idCues)) {
				t.Fatalf("index range does not start at the Cues element: %+v", got)
			}
			if tc.cues && got.IndexLength != int64(len(cues)) {
				t.Fatalf("index length %d, want %d", got.IndexLength, len(cues))
			}
		})
	}
}

func TestReadMatroskaElementBudget(t *testing.T) {
	children := [][]byte{}
	for range maxElements + 1 {
		children = append(children, ebml(idVoid, nil))
	}
	children = append(children, ebml(idCluster, make([]byte, 10)))
	data := matroska(func([]int64) [][]byte { return children })
	if got := read(t, data, "video/webm"); got.Layout != domain.LayoutUnknown {
		t.Fatalf("element budget: %+v", got)
	}
}

func TestParseHeader(t *testing.T) {
	for _, tc := range []struct {
		in   []byte
		id   uint32
		size int64
		n    int
		ok   bool
	}{
		{[]byte{0xEC, 0x80}, 0xEC, 0, 2, true},
		{[]byte{0x1A, 0x45, 0xDF, 0xA3, 0x9F}, idEBML, 31, 5, true},
		{[]byte{0x18, 0x53, 0x80, 0x67, 0x01, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, idSegment, -1, 12, true},
		{[]byte{0xEC, 0xff}, 0xEC, -1, 2, true},
		{[]byte{0xEC, 0x40, 0x10}, 0xEC, 16, 3, true},
		{[]byte{0x00, 0x80}, 0, 0, 0, false},                         // ID longer than 8 bits of marker
		{[]byte{0x08, 0x00, 0x00, 0x00, 0x00, 0x80}, 0, 0, 0, false}, // five-byte ID
		{[]byte{0xEC, 0x00}, 0, 0, 0, false},                         // size longer than 8 bytes
		{[]byte{0xEC, 0x40}, 0, 0, 0, false},                         // truncated size
		{[]byte{0x1A, 0x45}, 0, 0, 0, false},                         // truncated ID
		{nil, 0, 0, 0, false},
	} {
		id, size, n, ok := parseHeader(tc.in)
		if id != tc.id || size != tc.size || n != tc.n || ok != tc.ok {
			t.Errorf("%x: got %x %d %d %t", tc.in, id, size, n, ok)
		}
	}
}

func TestReadSeekHeadRejectsMalformedEntries(t *testing.T) {
	segment := int64(100)
	for _, tc := range []struct {
		name    string
		payload []byte
		ok      bool
	}{
		{"overlong seek id", ebml(idSeek, ebml(idSeekID, []byte{1, 2, 3, 4, 5})), false},
		{"empty position", ebml(idSeek, join(ebml(idSeekID, idBytes(idCues)), ebml(idSeekPos, nil))), false},
		{"overflowing position", ebml(idSeek, join(ebml(idSeekID, idBytes(idCues)), ebml(idSeekPos, []byte{0x7f, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}))), false},
		{"child beyond seek", join(idBytes(idSeek), vint(3), ebml(idSeekID, []byte{1, 2})), false},
		{"entry without position", ebml(idSeek, ebml(idSeekID, idBytes(idCues))), true},
		{"crc and void children", join(ebml(0xBF, make([]byte, 4)), seek(idCues, 7), ebml(idVoid, make([]byte, 3))), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := join(make([]byte, segment), ebml(idSeekHead, tc.payload))
			b := &budget{ctx: context.Background(), file: bytes.NewReader(data), size: int64(len(data)), left: domain.SourceLayoutReadLimit}
			head, err := readElement(b, segment)
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = readSeekHead(b, head, segment)
			if (err == nil) != tc.ok {
				t.Fatal(err)
			}
		})
	}
}

// FuzzRead checks that no input panics, reads beyond the budget or records
// an index range outside the file.
func FuzzRead(f *testing.F) {
	f.Add(join(box("ftyp", 16), box("moov", 10), box("mdat", 10)), true)
	f.Add(join(box("ftyp", 16), largeBox("mdat", 10), box("moov", 10)), true)
	f.Add(matroska(func([]int64) [][]byte {
		return [][]byte{ebml(idCues, make([]byte, 4)), ebml(idCluster, make([]byte, 4))}
	}), false)
	f.Add(matroska(func([]int64) [][]byte { return [][]byte{ebml(idSeekHead, seek(idCues, 1<<40)), ebml(idCluster, nil)} }), false)
	f.Fuzz(func(t *testing.T, data []byte, mp4 bool) {
		contentType := "video/x-matroska"
		if mp4 {
			contentType = "video/mp4"
		}
		reader := &countingReader{data: data}
		got, err := Read(context.Background(), reader, int64(len(data)), contentType)
		if err != nil {
			t.Fatal(err)
		}
		if reader.bytes > domain.SourceLayoutReadLimit {
			t.Fatalf("read %d bytes", reader.bytes)
		}
		if !domain.ValidContainerLayout(got.Layout) {
			t.Fatalf("layout %q", got.Layout)
		}
		if !domain.ValidSourceLayoutInspection(domain.SourceLayoutInspection{SourceID: "00000000-0000-4000-8000-000000000000", Size: int64(len(data)), SourceLayout: got}) {
			t.Fatalf("invalid result %+v for %d bytes", got, len(data))
		}
	})
}
