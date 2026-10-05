package app

import (
	"context"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// OpsMetricsSource reads the shared operational snapshot behind the default
// alert rules (G50.6) in one bounded statement. It must honor its context
// and returns no partial data.
type OpsMetricsSource interface {
	OpsMetrics(ctx context.Context) (OpsMetricsSnapshot, error)
}

// OpsMetricsSnapshot is shared by every instance using the same database.
type OpsMetricsSnapshot struct {
	// WebhookDead counts dead-lettered deliveries kept for replay;
	// WebhookPending counts deliveries still waiting for an attempt.
	WebhookDead    int64
	WebhookPending int64
	// ScanConsecutiveFailures is the longest run of failed inventory scans
	// at the end of a library's retained history, over all libraries.
	// ScanFailingLibraries counts libraries whose latest scan failed.
	ScanConsecutiveFailures int64
	ScanFailingLibraries    int64
	// DevModeActive reports an unexpired developer mode session (G45);
	// DevModeActiveSeconds is how long it has been on.
	DevModeActive        bool
	DevModeActiveSeconds float64
	// ConsistencyFindings holds, per check in domain.ConsistencyChecks
	// order, the findings of the latest finished run of every library.
	ConsistencyFindings domain.ConsistencyCheckCounts
	// ConsistencyLastFinished is the newest finished run; zero before any.
	ConsistencyLastFinished time.Time
}
