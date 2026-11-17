// Package containerlayout reads where an MP4/MOV or Matroska/WebM file keeps
// its start and seek index (G10.6, G10.10). It only reads box and element
// headers within a fixed byte budget and never writes, remuxes or moves
// anything: a faststart recommendation is advice for the administrator.
package containerlayout

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// errStructure ends a read whose structure is not what the container
// requires (truncated, malformed or implausible); the result is unknown.
var errStructure = errors.New("container structure not recognized")

// errBudget ends a read that would exceed the byte or element budget before
// the answer is known; the result is unknown.
var errBudget = errors.New("container layout read budget exhausted")

const (
	// maxBoxes bounds the top-level MP4 boxes visited; real files have a
	// handful, fragmented ones one moof/mdat pair per fragment, and the walk
	// stops at the first mdat after moov.
	maxBoxes = 4096
	// maxElements bounds the top-level Matroska elements visited before
	// the first Cluster.
	maxElements = 256
	// maxSeekHead bounds the bytes of one SeekHead that are parsed.
	maxSeekHead = 16 << 10
)

// Read classifies the layout of a file of the given size and content type.
// Unsupported content types and unreadable structures are reported as
// LayoutUnknown with a nil error; an error is only returned when reading
// the file itself failed or ctx ended, so the caller can try again later.
// At most domain.SourceLayoutReadLimit bytes are read.
func Read(ctx context.Context, file io.ReaderAt, size int64, contentType string) (domain.SourceLayout, error) {
	if ctx == nil || file == nil || size < 0 {
		return domain.SourceLayout{}, domain.ErrInvalid
	}
	b := &budget{ctx: ctx, file: file, size: size, left: domain.SourceLayoutReadLimit}
	var layout domain.SourceLayout
	var err error
	switch contentType {
	case "video/mp4", "video/quicktime":
		layout, err = readMP4(b)
	case "video/x-matroska", "video/webm":
		layout, err = readMatroska(b)
	default:
		return domain.SourceLayout{Layout: domain.LayoutUnknown}, nil
	}
	if errors.Is(err, errStructure) || errors.Is(err, errBudget) {
		return domain.SourceLayout{Layout: domain.LayoutUnknown}, nil
	}
	if err != nil {
		return domain.SourceLayout{}, err
	}
	if layout.IndexLength <= 0 || layout.IndexLength > domain.SourceLayoutIndexMax || layout.IndexOffset < 0 || layout.IndexOffset > size-layout.IndexLength {
		layout.IndexOffset, layout.IndexLength = 0, 0
	}
	return layout, nil
}

// budget is a bounded positional reader over the file.
type budget struct {
	ctx  context.Context
	file io.ReaderAt
	size int64
	left int
}

// read returns up to n bytes at off, fewer only at the end of the file.
func (b *budget) read(off int64, n int) ([]byte, error) {
	if err := b.ctx.Err(); err != nil {
		return nil, err
	}
	if off < 0 || off >= b.size || n <= 0 {
		return nil, errStructure
	}
	if remaining := b.size - off; int64(n) > remaining {
		n = int(remaining)
	}
	if n > b.left {
		return nil, errBudget
	}
	b.left -= n
	buf := make([]byte, n)
	got, err := b.file.ReadAt(buf, off)
	if got == n {
		return buf, nil
	}
	if err == nil || errors.Is(err, io.EOF) {
		// The file is shorter than its recorded size: it changed while it
		// was read, or the stamp was wrong. Nothing reliable can be said.
		return nil, errStructure
	}
	return nil, err
}

// ---- MP4 / QuickTime ----

// readMP4 walks top-level boxes by their headers, skipping over payloads,
// until the order of the movie box and the media data is known.
func readMP4(b *budget) (domain.SourceLayout, error) {
	var off int64
	moov := domain.SourceLayout{IndexOffset: -1}
	mdatSeen, moofSeen := false, false
	for boxes := 0; off < b.size; boxes++ {
		if boxes >= maxBoxes {
			return domain.SourceLayout{}, errBudget
		}
		if b.size-off < 8 {
			// Trailing bytes too short for a box header end the walk.
			break
		}
		header, err := b.read(off, 8)
		if err != nil {
			return domain.SourceLayout{}, err
		}
		kind := string(header[4:8])
		if !fourCC(header[4:8]) || boxes == 0 && !firstBox(kind) {
			return domain.SourceLayout{}, errStructure
		}
		length, headerLength := int64(binary.BigEndian.Uint32(header[:4])), int64(8)
		switch length {
		case 0:
			length = b.size - off
		case 1:
			large, err := b.read(off+8, 8)
			if err != nil {
				return domain.SourceLayout{}, err
			}
			if len(large) < 8 {
				return domain.SourceLayout{}, errStructure
			}
			value := binary.BigEndian.Uint64(large)
			if value > math.MaxInt64 {
				return domain.SourceLayout{}, errStructure
			}
			length, headerLength = int64(value), 16
		}
		if length < headerLength || length > b.size-off {
			return domain.SourceLayout{}, errStructure
		}
		switch kind {
		case "moov":
			if moov.IndexOffset >= 0 {
				return domain.SourceLayout{}, errStructure
			}
			moov.IndexOffset, moov.IndexLength = off, length
			if mdatSeen {
				moov.Layout = domain.LayoutMoovAtEnd
				return moov, nil
			}
		case "moof":
			if moov.IndexOffset < 0 {
				return domain.SourceLayout{}, errStructure
			}
			moofSeen = true
		case "mdat":
			if moov.IndexOffset >= 0 {
				moov.Layout = domain.LayoutMoovBeforeMdat
				if moofSeen {
					moov.Layout = domain.LayoutFragmented
				}
				return moov, nil
			}
			mdatSeen = true
		}
		off += length
	}
	switch {
	case moov.IndexOffset >= 0 && moofSeen:
		moov.Layout = domain.LayoutFragmented
	case moov.IndexOffset >= 0:
		// A movie without media data in this file (external references)
		// has nothing to wait for.
		moov.Layout = domain.LayoutMoovBeforeMdat
	default:
		return domain.SourceLayout{}, errStructure
	}
	return moov, nil
}

// fourCC accepts printable ASCII box types; top-level boxes never use the
// copyright-sign types of user data.
func fourCC(kind []byte) bool {
	for _, c := range kind {
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

// firstBox lists the boxes an MP4 or QuickTime file may start with.
func firstBox(kind string) bool {
	switch kind {
	case "ftyp", "styp", "moov", "mdat", "free", "skip", "wide", "pnot", "uuid", "junk":
		return true
	}
	return false
}

// ---- Matroska / WebM ----

const (
	idEBML     = 0x1A45DFA3
	idSegment  = 0x18538067
	idSeekHead = 0x114D9B74
	idSeek     = 0x4DBB
	idSeekID   = 0x53AB
	idSeekPos  = 0x53AC
	idCues     = 0x1C53BB6B
	idCluster  = 0x1F43B675
)

type element struct {
	id      uint32
	offset  int64 // of the ID
	data    int64 // of the payload
	size    int64 // payload size; -1 when unknown
	unknown bool
}

// end is the offset just after a known-size element.
func (e element) end() int64 { return e.data + e.size }

// readMatroska walks the Segment's top-level elements up to the first
// Cluster and follows at most two SeekHeads to find the Cues element.
func readMatroska(b *budget) (domain.SourceLayout, error) {
	header, err := readElement(b, 0)
	if err != nil {
		return domain.SourceLayout{}, err
	}
	if header.id != idEBML || header.unknown || header.end() > b.size {
		return domain.SourceLayout{}, errStructure
	}
	segment, err := readElement(b, header.end())
	if err != nil {
		return domain.SourceLayout{}, err
	}
	if segment.id != idSegment {
		return domain.SourceLayout{}, errStructure
	}
	segmentEnd := b.size
	if !segment.unknown && segment.end() < segmentEnd {
		segmentEnd = segment.end()
	}
	var cues element
	cuesFound := false
	cluster := int64(-1)
	var seekCues, seekHeads []int64
	visited := map[int64]bool{}
	off := segment.data
	for count := 0; off < segmentEnd; count++ {
		if count >= maxElements {
			return domain.SourceLayout{}, errBudget
		}
		e, err := readElement(b, off)
		if err != nil {
			return domain.SourceLayout{}, err
		}
		if e.id == idCluster {
			cluster = e.offset
			break
		}
		if e.unknown || e.end() > segmentEnd {
			return domain.SourceLayout{}, errStructure
		}
		switch e.id {
		case idSeekHead:
			visited[e.offset] = true
			c, h, err := readSeekHead(b, e, segment.data)
			if err != nil {
				return domain.SourceLayout{}, err
			}
			seekCues, seekHeads = append(seekCues, c...), append(seekHeads, h...)
		case idCues:
			if !cuesFound {
				cues, cuesFound = e, true
			}
		}
		off = e.end()
	}
	// A second SeekHead (often written at the end of the file) may be the
	// only one that names the Cues.
	for _, position := range seekHeads {
		if len(seekCues) > 0 || cuesFound {
			break
		}
		if visited[position] || position < 0 || position >= segmentEnd {
			continue
		}
		visited[position] = true
		e, err := readElement(b, position)
		if err != nil {
			return domain.SourceLayout{}, err
		}
		if e.id != idSeekHead || e.unknown || e.end() > segmentEnd {
			continue
		}
		c, _, err := readSeekHead(b, e, segment.data)
		if err != nil {
			return domain.SourceLayout{}, err
		}
		seekCues = append(seekCues, c...)
	}
	if !cuesFound {
		for _, position := range seekCues {
			if position < 0 || position >= segmentEnd {
				continue
			}
			e, err := readElement(b, position)
			if err != nil {
				return domain.SourceLayout{}, err
			}
			if e.id == idCues && !e.unknown && e.end() <= segmentEnd {
				cues, cuesFound = e, true
				break
			}
		}
	}
	if cluster < 0 {
		// The segment ended without a cluster: no media to seek in.
		return domain.SourceLayout{}, errStructure
	}
	switch {
	case cuesFound && cues.offset < cluster:
		return domain.SourceLayout{Layout: domain.LayoutCuesBeforeClusters, IndexOffset: cues.offset, IndexLength: cues.end() - cues.offset}, nil
	case cuesFound:
		return domain.SourceLayout{Layout: domain.LayoutCuesAfterClusters, IndexOffset: cues.offset, IndexLength: cues.end() - cues.offset}, nil
	}
	// Everything before the first cluster was read and no SeekHead names a
	// Cues element that exists.
	return domain.SourceLayout{Layout: domain.LayoutNoCues}, nil
}

// readSeekHead returns the absolute Cues and SeekHead positions a SeekHead
// names. Positions are relative to the segment payload.
func readSeekHead(b *budget, head element, segment int64) (cues, heads []int64, err error) {
	if head.size > maxSeekHead {
		return nil, nil, errBudget
	}
	if head.size == 0 {
		return nil, nil, nil
	}
	data, err := b.read(head.data, int(head.size))
	if err != nil {
		return nil, nil, err
	}
	if int64(len(data)) != head.size {
		return nil, nil, errStructure
	}
	for len(data) > 0 {
		id, size, n, ok := parseHeader(data)
		if !ok || size < 0 || size > int64(len(data)-n) {
			return nil, nil, errStructure
		}
		payload := data[n : n+int(size)]
		data = data[n+int(size):]
		if id != idSeek {
			continue
		}
		var target uint32
		position := int64(-1)
		for len(payload) > 0 {
			cid, csize, cn, ok := parseHeader(payload)
			if !ok || csize < 0 || csize > int64(len(payload)-cn) {
				return nil, nil, errStructure
			}
			value := payload[cn : cn+int(csize)]
			payload = payload[cn+int(csize):]
			switch cid {
			case idSeekID:
				if len(value) == 0 || len(value) > 4 {
					return nil, nil, errStructure
				}
				target = 0
				for _, c := range value {
					target = target<<8 | uint32(c)
				}
			case idSeekPos:
				if len(value) == 0 || len(value) > 8 {
					return nil, nil, errStructure
				}
				var v int64
				for _, c := range value {
					if v > (math.MaxInt64-int64(c))>>8 {
						return nil, nil, errStructure
					}
					v = v<<8 | int64(c)
				}
				if v > math.MaxInt64-segment {
					return nil, nil, errStructure
				}
				position = segment + v
			}
		}
		if position < 0 {
			continue
		}
		switch target {
		case idCues:
			cues = append(cues, position)
		case idSeekHead:
			heads = append(heads, position)
		}
	}
	return cues, heads, nil
}

// readElement reads one element header at off.
func readElement(b *budget, off int64) (element, error) {
	raw, err := b.read(off, 12)
	if err != nil {
		return element{}, err
	}
	id, size, n, ok := parseHeader(raw)
	if !ok {
		return element{}, errStructure
	}
	e := element{id: id, offset: off, data: off + int64(n), size: size, unknown: size < 0}
	if !e.unknown && e.size > b.size-e.data {
		return element{}, errStructure
	}
	return e, nil
}

// parseHeader decodes an EBML element ID (1 to 4 bytes, marker kept) and
// data size (1 to 8 bytes, marker removed; -1 when all value bits are set,
// the "unknown size" of live streams).
func parseHeader(buf []byte) (id uint32, size int64, n int, ok bool) {
	if len(buf) == 0 || buf[0] == 0 {
		return 0, 0, 0, false
	}
	idLength := leadingZeros(buf[0]) + 1
	if idLength > 4 || len(buf) < idLength {
		return 0, 0, 0, false
	}
	for _, c := range buf[:idLength] {
		id = id<<8 | uint32(c)
	}
	rest := buf[idLength:]
	if len(rest) == 0 || rest[0] == 0 {
		return 0, 0, 0, false
	}
	sizeLength := leadingZeros(rest[0]) + 1
	if len(rest) < sizeLength {
		return 0, 0, 0, false
	}
	value := uint64(rest[0]) & (0xff >> sizeLength)
	allOnes := value == 0xff>>sizeLength
	for _, c := range rest[1:sizeLength] {
		value = value<<8 | uint64(c)
		allOnes = allOnes && c == 0xff
	}
	if allOnes {
		return id, -1, idLength + sizeLength, true
	}
	if value > math.MaxInt64 {
		return 0, 0, 0, false
	}
	return id, int64(value), idLength + sizeLength, true
}

func leadingZeros(c byte) int {
	n := 0
	for mask := byte(0x80); mask != 0 && c&mask == 0; mask >>= 1 {
		n++
	}
	return n
}
