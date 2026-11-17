package scan

import (
	"context"
	"os"
	"path/filepath"
	"slices"

	"github.com/MoYuanCN/Jelee/internal/adapter/containerlayout"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// LayoutInspector reads where one media source keeps its start and seek
// index (G10.6): MP4/MOV box headers or the Matroska SeekHead and top-level
// element headers, at most domain.SourceLayoutReadLimit bytes. The file is
// opened read-only through the library root and never written, remuxed or
// moved; a faststart recommendation is only advice (G10.10).
type LayoutInspector struct{}

var _ app.SourceLayoutInspector = LayoutInspector{}

// InspectSourceLayout reads the layout of target's file if it still has the
// scanned stamp; a changed, missing or escaping file fails.
func (LayoutInspector) InspectSourceLayout(ctx context.Context, target domain.SourceLayoutTarget) (domain.SourceLayoutInspection, error) {
	if ctx == nil || !domain.ValidID(target.SourceID) || target.Size < 0 || !slices.Contains(domain.LayoutContentTypes, target.ContentType) {
		return domain.SourceLayoutInspection{}, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return domain.SourceLayoutInspection{}, err
	}
	if !supportedPlatform() || !filepath.IsAbs(target.RootPath) || validRelative(target.RelativePath) != nil || target.RelativePath == "." {
		return domain.SourceLayoutInspection{}, domain.ErrScanUnavailable
	}
	root, err := os.OpenRoot(filepath.Clean(target.RootPath))
	if err != nil {
		return domain.SourceLayoutInspection{}, scanError(ctx, domain.ErrScanUnavailable)
	}
	defer func() { _ = root.Close() }()
	file, err := root.OpenFile(filepath.FromSlash(target.RelativePath), fileFlags(), 0)
	if err != nil {
		return domain.SourceLayoutInspection{}, scanError(ctx, domain.ErrScanIO)
	}
	defer func() { _ = file.Close() }()
	if !sameFileStamp(file, target.Size, target.ModifiedUnixNano) {
		return domain.SourceLayoutInspection{}, scanError(ctx, domain.ErrConflict)
	}
	layout, err := containerlayout.Read(ctx, file, target.Size, target.ContentType)
	if err := ctx.Err(); err != nil {
		return domain.SourceLayoutInspection{}, err
	}
	if err != nil {
		return domain.SourceLayoutInspection{}, scanError(ctx, domain.ErrScanIO)
	}
	// A file rewritten while it was read is left for the next scan.
	if !sameFileStamp(file, target.Size, target.ModifiedUnixNano) {
		return domain.SourceLayoutInspection{}, scanError(ctx, domain.ErrConflict)
	}
	return domain.SourceLayoutInspection{SourceID: target.SourceID, Size: target.Size, ModifiedUnixNano: target.ModifiedUnixNano, SourceLayout: layout}, nil
}
