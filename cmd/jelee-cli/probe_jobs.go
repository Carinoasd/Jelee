package main

import "github.com/MoYuanCN/Jelee/internal/domain"

func decodeCLIProbeSummary(raw []byte) (domain.ProbeJobSummary, bool) {
	var result domain.ProbeJobSummary
	if !jobsCLIPublicObject(raw, &result, []string{"jobId", "libraryId", "enabled", "phase", "processed", "hits", "negativeHits", "succeeded", "failed", "changed", "unavailable"}, "scope", "targetItemId", "errorCode") ||
		!domain.ValidID(result.JobID) || !domain.ValidID(result.LibraryID) {
		return domain.ProbeJobSummary{}, false
	}
	var total int64
	for _, n := range []int64{result.Hits, result.NegativeHits, result.Succeeded, result.Failed, result.Changed, result.Unavailable} {
		if n < 0 || n > 500000 {
			return domain.ProbeJobSummary{}, false
		}
		total += n
	}
	if result.Processed < 0 || result.Processed > 500000 || result.Processed != total {
		return domain.ProbeJobSummary{}, false
	}
	if !result.Enabled {
		if result.Phase != domain.ProbeSummaryDisabled || result.Scope != "" || result.TargetItemID != "" || result.Processed != 0 || result.ErrorCode != "" {
			return domain.ProbeJobSummary{}, false
		}
		return result, true
	}
	if result.Scope == "" || domain.ValidateProbeIntent(domain.ProbeIntent{Scope: result.Scope, TargetItemID: result.TargetItemID}) != nil {
		return domain.ProbeJobSummary{}, false
	}
	switch result.Phase {
	case domain.ProbeSummaryWaitingScan, domain.ProbeSummaryRunning, domain.ProbeSummaryDone, domain.ProbeSummaryCancelled:
		if result.ErrorCode != "" {
			return domain.ProbeJobSummary{}, false
		}
	case domain.ProbeSummaryAborted:
		switch result.ErrorCode {
		case string(domain.ProbePhaseRuntimeUnavailable), string(domain.ProbePhaseCapacity), string(domain.ProbePhaseInvalidated), string(domain.ProbePhaseIdentityMismatch),
			"scan_unavailable", "scan_io", "scan_limit", "scan_failed", "job_timeout", "job_attempts_exhausted":
		default:
			return domain.ProbeJobSummary{}, false
		}
	default:
		return domain.ProbeJobSummary{}, false
	}
	return result, true
}
