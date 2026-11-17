package domain

// ContainerLayout names where a media file keeps the index a client needs
// before it can start or seek (G10.6, G10.10). It is a fact read from the
// file's own structure, never a decision to change it: Jelee does not
// rewrite, remux or move anything inside an original resource.
type ContainerLayout string

const (
	// LayoutMoovBeforeMdat is an MP4/MOV whose movie box precedes the media
	// data ("faststart"): a client can start from the first bytes.
	LayoutMoovBeforeMdat ContainerLayout = "moov_before_mdat"
	// LayoutMoovAtEnd is an MP4/MOV whose movie box follows the media data:
	// a client must fetch the end of the file before it can start.
	LayoutMoovAtEnd ContainerLayout = "moov_at_end"
	// LayoutFragmented is a fragmented MP4 (moof boxes after an initial moov).
	LayoutFragmented ContainerLayout = "fragmented"
	// LayoutCuesBeforeClusters is a Matroska/WebM file whose Cues element
	// precedes the first Cluster.
	LayoutCuesBeforeClusters ContainerLayout = "cues_before_clusters"
	// LayoutCuesAfterClusters is a Matroska/WebM file whose Cues element,
	// reached through the SeekHead, follows the clusters (the usual muxer
	// output; clients fetch it with one Range request).
	LayoutCuesAfterClusters ContainerLayout = "cues_after_clusters"
	// LayoutNoCues is a Matroska/WebM file without a Cues element reachable
	// from its SeekHead or before the first Cluster: clients cannot seek by
	// index.
	LayoutNoCues ContainerLayout = "no_cues"
	// LayoutUnknown is a file whose structure could not be read within the
	// bounded read (truncated, malformed, or too many boxes before the index).
	LayoutUnknown ContainerLayout = "unknown"
)

// ContainerLayouts lists every stored layout; the database CHECK mirrors it.
var ContainerLayouts = []ContainerLayout{LayoutMoovBeforeMdat, LayoutMoovAtEnd, LayoutFragmented,
	LayoutCuesBeforeClusters, LayoutCuesAfterClusters, LayoutNoCues, LayoutUnknown}

// Layout hint codes reported with playback information. Each is advice to
// the administrator; none is acted upon by the server.
const (
	// LayoutHintFaststart recommends remuxing an MP4/MOV with its movie box
	// first (for example ffmpeg -c copy -movflags +faststart, run by the
	// administrator on a copy). Jelee never modifies the file.
	LayoutHintFaststart = "faststart_recommended"
	// LayoutHintNoSeekIndex reports a Matroska/WebM file without a seek
	// index (Cues); clients may be unable to seek in it.
	LayoutHintNoSeekIndex = "no_seek_index"
)

// SourceLayoutVersion identifies the layout reader. Rows written by another
// version are read again on the next catalog sync.
const SourceLayoutVersion = "layout-v1"

// SourceLayoutInspectionBatch bounds one page of sources whose layout is
// read after a catalog sync, and SourceLayoutMaxPerJob the reads of one sync;
// the rest are read by the next sync.
const (
	SourceLayoutInspectionBatch = 64
	SourceLayoutMaxPerJob       = 20000
)

// SourceLayoutReadLimit bounds the bytes one layout read may take from a
// file, headers of every visited box or element included.
const SourceLayoutReadLimit = 64 << 10

// SourceLayoutIndexMax bounds a recorded index range; a larger moov or Cues
// element is still classified, only its range is not recorded for prefetch.
const SourceLayoutIndexMax int64 = 1 << 30

// LayoutHints returns the advice codes of a layout in a fixed order. A
// layout without advice returns nil.
func LayoutHints(layout ContainerLayout) []string {
	switch layout {
	case LayoutMoovAtEnd:
		return []string{LayoutHintFaststart}
	case LayoutNoCues:
		return []string{LayoutHintNoSeekIndex}
	}
	return nil
}

// LayoutContentTypes are the media source content types whose layout is
// read; other containers (AVI, MPEG-TS) are not inspected.
var LayoutContentTypes = []string{"video/mp4", "video/quicktime", "video/x-matroska", "video/webm"}

// SourceLayout is the stored layout of one media source, valid for the
// file stamp it was read against. IndexLength is zero when the index range
// is unknown or too large to record.
type SourceLayout struct {
	Layout                   ContainerLayout
	IndexOffset, IndexLength int64
}

// SourceLayoutTarget is one media source whose layout is missing or was read
// against another stamp or reader version. It carries the absolute library
// root and is redacted from diagnostics.
type SourceLayoutTarget struct {
	SourceID, ContentType  string
	RootPath, RelativePath string
	Size, ModifiedUnixNano int64
}

// String keeps the library root and path out of logs.
func (SourceLayoutTarget) String() string { return "source layout target (redacted)" }

// GoString keeps the library root and path out of %#v.
func (SourceLayoutTarget) GoString() string { return "source layout target (redacted)" }

// SourceLayoutInspection is what a bounded read of one media source found,
// for the stamp the read was made against.
type SourceLayoutInspection struct {
	SourceID               string
	Size, ModifiedUnixNano int64
	SourceLayout
}

// ValidContainerLayout reports whether layout is one of ContainerLayouts.
func ValidContainerLayout(layout ContainerLayout) bool {
	for _, known := range ContainerLayouts {
		if layout == known {
			return true
		}
	}
	return false
}

// ValidSourceLayoutInspection mirrors the database checks for a recorded
// result: a known layout and an index range inside the file.
func ValidSourceLayoutInspection(v SourceLayoutInspection) bool {
	if !ValidID(v.SourceID) || v.Size < 0 || !ValidContainerLayout(v.Layout) || v.IndexOffset < 0 || v.IndexLength < 0 || v.IndexLength > SourceLayoutIndexMax {
		return false
	}
	if v.IndexLength == 0 {
		return v.IndexOffset == 0
	}
	return v.IndexOffset <= v.Size-v.IndexLength
}
