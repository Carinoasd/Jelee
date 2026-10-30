package app

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// ChapterImageRepository pages, under a catalog sync lease, the items whose
// probed media file has chapters and a picture stream that still needs a
// chapter image read (G40.1), and records each attempt. Recording re-checks
// the file stamp and, per chapter slot, the lock, manual flag and G40.10
// priority in the same transaction.
type ChapterImageRepository interface {
	NextChapterImageCandidates(ctx context.Context, lease domain.JobLease, after string, limit int) (domain.ChapterImagePage, error)
	RecordChapterImages(ctx context.Context, lease domain.JobLease, result domain.ChapterImageResult) (domain.ChapterImageRecord, error)
}

// ChapterImageExtractor copies one candidate's stored chapter pictures without
// decoding or re-encoding anything. It fails with domain.ErrChapterImages* for
// a deterministic refusal, domain.ErrProbeSourceChanged when the file no
// longer matches the probe stamp, and other errors for transient failures.
type ChapterImageExtractor interface {
	ExtractChapterImages(ctx context.Context, candidate domain.ChapterImageCandidate) ([]domain.ChapterImage, error)
}
