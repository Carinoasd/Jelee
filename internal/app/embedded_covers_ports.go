package app

import (
	"context"
	"io"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// EmbeddedCoverRepository pages, under a catalog sync lease, the items whose
// probed media file carries an attached picture that still needs extraction
// (G40.4), and records each attempt. Recording re-checks the file stamp, the
// slot lock and the G40.10 priority in the same transaction; its result is
// one of the domain.EmbeddedCover* outcomes.
type EmbeddedCoverRepository interface {
	NextEmbeddedCoverCandidates(ctx context.Context, lease domain.JobLease, after string, limit int) (domain.EmbeddedCoverPage, error)
	RecordEmbeddedCover(ctx context.Context, lease domain.JobLease, result domain.EmbeddedCoverResult) (string, error)
}

// EmbeddedCoverExtractor copies one candidate's attached picture without
// decoding or re-encoding anything. It fails with domain.ErrEmbeddedCover*
// for a deterministic refusal, domain.ErrProbeSourceChanged when the file no
// longer matches the probe stamp, and other errors for transient failures.
type EmbeddedCoverExtractor interface {
	ExtractCover(ctx context.Context, candidate domain.EmbeddedCoverCandidate) (domain.EmbeddedCover, error)
}

// EmbeddedCoverStore keeps original bytes under their SHA-256 (G40.5).
type EmbeddedCoverStore interface {
	PutOriginal(ctx context.Context, input io.Reader, limit int64) ([32]byte, int64, error)
}
