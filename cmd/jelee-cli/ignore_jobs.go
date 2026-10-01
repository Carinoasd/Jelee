package main

import (
	"encoding/json"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// Treat the server cursor as opaque; only bound its transport representation.
func validCLIIgnoreCursor(cursor string) bool {
	if len(cursor) > 4096 {
		return false
	}
	for _, c := range cursor {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func decodeCLIIgnoreReport(raw []byte, limit int) (domain.IgnoreReport, bool) {
	var result domain.IgnoreReport
	fields, ok := jobsCLIObject(raw)
	if !ok || !jobsCLIHas(fields, "entries") || limit < 1 || limit > 100 {
		return result, false
	}
	if !jobsCLIPublicObject(raw, &result, []string{"jobId", "state", "enabled", "reviewRequired", "invalidated", "excludedFiles", "excludedDirectories", "unknown"}, "nextCursor") ||
		!domain.ValidID(result.JobID) || (result.State != domain.JobSucceeded && result.State != domain.JobFailed && result.State != domain.JobCancelled) ||
		result.ExcludedFiles < 0 || result.ExcludedDirectories < 0 || result.Unknown < 0 || !validCLIIgnoreCursor(result.NextCursor) {
		return domain.IgnoreReport{}, false
	}
	var entries []json.RawMessage
	if json.Unmarshal(fields["entries"], &entries) != nil || entries == nil || len(entries) > limit || result.NextCursor != "" && len(entries) != limit {
		return domain.IgnoreReport{}, false
	}
	result.Entries = make([]domain.IgnoreReportEntry, 0, len(entries))
	for _, rawEntry := range entries {
		var entry domain.IgnoreReportEntry
		if !jobsCLIPublicObject(rawEntry, &entry, []string{"source", "rootId", "path", "outcome"}, "kind", "ruleDirectory", "ruleLine", "matchedPath", "reason") || domain.ValidateIgnoreReportEntry(entry) != nil {
			return domain.IgnoreReport{}, false
		}
		result.Entries = append(result.Entries, entry)
	}
	return result, true
}
