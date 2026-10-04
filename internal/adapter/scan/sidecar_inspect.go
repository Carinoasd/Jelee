package scan

import (
	"context"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/MoYuanCN/Jelee/internal/adapter/probe"
	"github.com/MoYuanCN/Jelee/internal/adapter/subtitles"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// SidecarInspector reads the bounded facts of one external subtitle or audio
// file that the scan itself does not: the edge fingerprint shared with media
// probing (size plus at most 64 KiB from each end) and, for a text subtitle,
// the character set from at most subtitles.DefaultDetectLimit bytes. The file
// is opened read-only through the library root and never written, renamed
// or converted.
type SidecarInspector struct{}

var _ app.SidecarInspector = SidecarInspector{}

// sidecarTextFormats are the subtitle formats whose charset is meaningful.
// ".sub" is either MicroDVD text or VobSub data; binary data fails detection
// and keeps no charset.
var sidecarTextFormats = []string{"srt", "ass", "ssa", "vtt", "webvtt", "ttml", "dfxp", "smi", "sami", "sub", "idx"}

func (SidecarInspector) InspectSidecar(ctx context.Context, target domain.SidecarInspectionTarget) (domain.SidecarInspection, error) {
	if ctx == nil || !domain.ValidID(target.ID) || target.Size < 0 {
		return domain.SidecarInspection{}, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return domain.SidecarInspection{}, err
	}
	if !supportedPlatform() || !filepath.IsAbs(target.RootPath) || validRelative(target.RelativePath) != nil || target.RelativePath == "." {
		return domain.SidecarInspection{}, domain.ErrScanUnavailable
	}
	root, err := os.OpenRoot(filepath.Clean(target.RootPath))
	if err != nil {
		return domain.SidecarInspection{}, scanError(ctx, domain.ErrScanUnavailable)
	}
	defer root.Close()
	file, err := root.OpenFile(filepath.FromSlash(target.RelativePath), fileFlags(), 0)
	if err != nil {
		return domain.SidecarInspection{}, scanError(ctx, domain.ErrScanIO)
	}
	defer file.Close()
	if !sameStamp(file, target) {
		return domain.SidecarInspection{}, scanError(ctx, domain.ErrConflict)
	}
	digest, err := probe.EdgeFingerprint(ctx, file, target.Size)
	if err != nil {
		return domain.SidecarInspection{}, scanError(ctx, domain.ErrScanIO)
	}
	fingerprint, err := hex.DecodeString(digest)
	if err != nil || len(fingerprint) != domain.SidecarFingerprintBytes {
		return domain.SidecarInspection{}, domain.ErrScanIO
	}
	result := domain.SidecarInspection{ID: target.ID, Size: target.Size, ModifiedUnixNano: target.ModifiedUnixNano, Fingerprint: fingerprint}
	if target.Kind == domain.SidecarKindSubtitle && slices.Contains(sidecarTextFormats, target.Format) {
		charset, confidence, err := subtitles.DetectCharset(ctx, io.NewSectionReader(file, 0, target.Size), 0)
		if err := ctx.Err(); err != nil {
			return domain.SidecarInspection{}, err
		}
		// Low confidence is not recorded: delivery reports the charset to
		// clients, and a wrong one is worse than none.
		if err == nil && !confidence.Low && slices.Contains(domain.SidecarCharsets, string(charset)) {
			result.Charset = string(charset)
		}
	}
	// A file rewritten while it was read is left for the next scan.
	if !sameStamp(file, target) {
		return domain.SidecarInspection{}, scanError(ctx, domain.ErrConflict)
	}
	return result, nil
}

func sameStamp(file *os.File, target domain.SidecarInspectionTarget) bool {
	info, err := file.Stat()
	return err == nil && info.Mode().IsRegular() && info.Size() == target.Size && info.ModTime().UnixNano() == target.ModifiedUnixNano
}
